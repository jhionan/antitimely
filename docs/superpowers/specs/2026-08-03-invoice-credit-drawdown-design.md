# Advance credit and automatic invoice drawdown — design

**Date:** 2026-08-03
**Status:** approved (brainstorming) — revised after adversarial review — ready for implementation plan

## Problem

BClouder asked to be invoiced 20,000.00 CAD for August 2026. Hours actually tracked in the period came to 5,377.00 CAD (`ES-0006`), so the remaining 14,623.00 CAD was issued as an advance (`ES-0007`, 292.46 h at 50.00 CAD/h) — money paid before the work exists.

That advance has to be worked off. Every subsequent invoice should bill the full value of the hours tracked, then reduce that value by the advance — to zero while the advance still covers it, and to the excess once it doesn't — until the credit reaches zero and invoices resume billing normally.

Nothing in antitimely models this. `atl invoice generate` computes hours × rate and charges it. `--discount` exists but is a manual flat number the operator must compute and remember, recorded nowhere — so the remaining credit lives only in someone's head. `ES-0007` itself had to be produced by a throwaway Go program because there is no fixed-amount path for an hourly company.

## Goals

- **Store the advance** so the tool, not the operator, knows a credit exists.
- **Apply it automatically** on every `atl invoice generate`, reducing the invoice by `min(remaining_credit, line_total)` until the credit is exhausted.
- **Create advances in-tool**, from both the CLI and the interactive menu.
- **Make the balance readable** on demand, in money and in hours-to-break-even.
- **Keep the invoice honest**: the client sees hours worked, the advance applied (with the advance's invoice number), and what is due.
- **Never drift**: the balance must be derivable from issued documents, not held as a mutable counter.

## Non-goals

- **No remaining-balance line on the client-facing PDF.** The invoice explains its own total (hours, advance applied, amount due) but does not carry a running account balance — that is a statement fact, and Spanish autónomo invoices are better kept minimal. Reversible later: one line in `pdf.go`.
- **No `credits` table, no multi-advance lifecycle** (expiry, credits not tied to an invoice). The chosen model upgrades into one cleanly if that day comes.
- **No refund / credit-note / write-off path.** See *Accepted gaps*.
- No change to how hours are counted or to PDF layout beyond the reduction rows.

## Accepted gaps

- **Stranded credit has no exit.** If the relationship ends with credit remaining, there is no refund, write-off, or rectifying-document path; clearing it means hand-editing SQL or issuing a contrived invoice. The design self-terminates only while work continues.
- **Correction relies on `atl invoice delete`.** For an already-issued Spanish *factura* the conventional instrument is a rectifying document, not deletion — deletion punches a permanent gap in the numbering (`sender_state` never decrements) and orphans the PDF. Guarded (below) but not solved. Worth raising with the accountant, along with whether a 0.00-due invoice is acceptable as an issued factura.
- **Tax is hardcoded 0** (`pdf.go`, `Total tax`), consistent with services exported to a Canadian client. If this model is ever reused for a domestic/EU client, an advance's tax point normally falls at the date of payment and presenting a drawdown as a discount would understate the taxable base.

## Data model

Three columns on `invoices`, added to `schema.sql`'s `CREATE TABLE` (fresh DBs) **and** appended to the idempotent `invoiceMigrations` list in `internal/daemon/daemon.go` (existing DBs), tolerating `duplicate column` like the entries already there:

```sql
kind                  TEXT NOT NULL DEFAULT 'hourly'
                        CHECK (kind IN ('hourly','advance')),
credit_applied_cents  INTEGER NOT NULL DEFAULT 0,   -- drawdown; the ONLY thing the balance counts
discount_cents        INTEGER NOT NULL DEFAULT 0    -- manual goodwill; never touches the balance
```

The defaults are correct for the six pre-existing rows (`ES-0002`–`ES-0006` plus the numberless May anchor row at id 1): all hourly, none discounted. `ES-0007` is the one row the defaults get wrong — it is an advance — and is corrected by the backfill in *Manual steps*.

**Two columns, not one, and not a `discount_kind` enum.** An earlier draft reused a single `discount_cents` for both concepts and forbade `--discount` while a credit was live. Adversarial review showed the escape hatch (`--no-credit --discount=N`) walked straight through the guard: the balance subtracted all discounts unconditionally, so a 500 CAD goodwill discount silently destroyed 500 CAD of the client's prepayment. Splitting the column removes the ambiguity at the source, lets both coexist on one invoice, and deletes the error rule entirely.

**The `CHECK` ships with the column, not later.** SQLite accepts a `CHECK` on `ALTER TABLE ADD COLUMN` even on a STRICT, non-empty table (verified against a copy of the live DB with this project's own driver). Because `daemon.go` tolerates `duplicate column`, there is exactly one chance to include it — retrofitting later needs the full table-rebuild that `migrateObservationsSourceCheck` performs for `observations.source`. It matters because no Go code writes `'advance'` for the existing row: the only writer is a hand-typed `sqlite3 UPDATE`, and a silent `'Advance'` would zero the credit and re-bill the client 14,623.

The balance is **derived, never stored** — in `queries.sql`, then `make sqlc`:

```sql
-- name: CompanyCreditBalance :one
SELECT COALESCE(SUM(CASE WHEN kind = 'advance' THEN total_cents ELSE 0 END), 0)
     - COALESCE(SUM(credit_applied_cents), 0) AS remaining_cents
FROM invoices
WHERE company_id = ? AND currency = ?;

-- name: CompanyCreditRows :many
SELECT number, kind, total_cents, credit_applied_cents, sent_at
FROM invoices
WHERE company_id = ? AND currency = ?
  AND (kind = 'advance' OR credit_applied_cents > 0)
ORDER BY sent_at DESC, id DESC;
```

`currency` is a filter, not decoration: `SetCompanyBilling` can change a company's currency, after which a CAD credit would otherwise discount a EUR invoice at 1:1. The `id DESC` tie-break matters because `ES-0006` and `ES-0007` share a `sent_at` exactly.

This works because `total_cents` on an hourly row stores the **net** payable (`rpc_invoice.go` passes `doc.AmountDueCents()`), so advances-minus-drawdowns is the whole story. Deriving rather than storing buys no counter to drift, `--dry-run` safe by construction, and every CAD traceable to the invoice that consumed it.

**`CompanyCreditRows` returns nullable `number`/`total_cents`** (the id-1 anchor row has both NULL) — handle in Go, don't assume.

## Generation flow

One new step in `InvoiceGenerate` (`internal/daemon/rpc_invoice.go`), after the line item is computed and before `invoice.BuildDoc`:

```
line_total = hours × rate                            (unchanged)
goodwill   = args.DiscountCents                      (explicit --discount, usually 0)
credit     = CompanyCreditBalance(company, currency) (new)
applied    = max(0, min(credit, line_total − goodwill))
due        = line_total − goodwill − applied
```

Goodwill is applied first and the credit fills what remains, so `goodwill + applied` can never exceed `line_total` and trip `BuildDoc`'s validation.

**The `max(0, …)` clamp is load-bearing, not defensive.** Without it a negative balance — reachable by deleting an advance after a partial drawdown — makes `applied` negative, `BuildDoc` returns *"discount must not be negative"*, and **every** subsequent `atl invoice generate` for that company exits 1 with no CLI route back. The clamp degrades that into "credit is treated as zero", which is recoverable.

The row written back records `kind='hourly'`, `credit_applied_cents=applied`, `discount_cents=goodwill`, `total_cents=due`.

**Read the balance on `qtx`, or before `BeginTx`.** The insertion point sits inside the open transaction, and the daemon runs `db.SetMaxOpenConns(1)` — a `s.Q.CompanyCreditBalance(...)` there blocks on the single connection held by the transaction until the 10s handler deadline, surfacing as `context deadline exceeded`, which the CLI then retries three times. Indistinguishable from the known `accessibility_denied` stall, and invisible to any test that doesn't hold a transaction.

Worked example, 14,623.00 credit, no goodwill:

| Invoice | Hours | Line total | Applied | Due | Credit after |
|---|---:|---:|---:|---:|---:|
| ES-0008 | 120.00 h | 6,000.00 | 6,000.00 | **0.00** | 8,623.00 |
| ES-0009 | 400.00 h | 20,000.00 | 8,623.00 | **11,377.00** | 0.00 |
| ES-0010 | 200.00 h | 10,000.00 | 0.00 | **10,000.00** | 0.00 |

### Flags

| Flag | Behavior |
|---|---|
| *(none)* | Auto-applies the credit. The default, so it cannot be forgotten. |
| `--no-credit` | Bills the full hours and ignores the credit. New flag; needs a field on `rpcapi.InvoiceGenerateArgs`. |
| `--discount=N` | Manual goodwill reduction. **No longer conflicts with a live credit** — the two are separate columns and separate PDF rows. |
| `--dry-run` | Prints the drawdown and resulting balance; writes nothing. |

### PDF

`InvoiceDoc` gains `CreditAppliedCents int64` and `CreditAppliedRef string`; `AmountDueCents()` becomes `LineItem.TotalCents − DiscountCents − CreditAppliedCents`, and `BuildDoc` validates the sum against the line total. The renderer emits up to two reduction rows:

```
Subtotal                                    6,000.00 CAD
Advance applied (ES-0007)                  -6,000.00 CAD
Discount                                       -0.00 CAD   (omitted when zero)
Amount Due                                      0.00 CAD
```

Naming the advance's invoice number is what lets the client's bookkeeper tie the two documents together — a different thing from the running balance this design deliberately excludes. `CreditAppliedRef` is the **oldest advance with credit remaining** at generate time (FIFO), so it is deterministic without tracking per-advance allocation.

`DiscountLabel` is *not* how this is done. An earlier draft proposed one, defaulting to `"Discount"` — but Go's zero value for a string is `""`, so every existing caller would have rendered a blank label. Separate fields avoid the trap entirely. Note that `pdf_test.go`'s `sampleDoc` has a zero discount, so the reduction rows are currently never text-asserted; the new tests must assert them.

### Anchor semantics — now structural

`LastInvoiceSentForCompany` (`queries.sql`) gains one clause:

```sql
WHERE company_id = ? AND kind <> 'advance'
ORDER BY sent_at DESC LIMIT 1
```

Today `ES-0007` is anchor-neutral only because its `sent_at` was hand-pinned to `ES-0006`'s. That is prose, not code: a future advance stamped with `now` would move the anchor and silently drop every hour since the last real invoice — the exact failure `CLAUDE.md` warns about for the menu's "Send invoice". With the filter, neutrality is a property of the query, and new advances can carry their true issue time.

`ES-0007`'s pinned timestamp stays as-is (harmless once the filter ships, and re-stamping it is a data edit with no upside).

## Creating an advance

Both surfaces, sharing one RPC (`InvoiceAdvance`):

**CLI:** `atl invoice advance <company> --amount=14623 [--note=...] [--issue-date=YYYY-MM-DD] [--dry-run]`

**Menu:** `atl` → Invoices → *Issue advance*, following the pattern established in `2026-07-01-menu-invoice-generation-design.md` — numbered company picker (no typing names), amount prompt, preview-and-confirm before the number is burned, then open the PDF and reveal it in Finder.

Behavior: allocates the next number, renders with the hourly line shape (`amount ÷ rate` hours × rate, matching how `ES-0007` was produced), writes `kind='advance'`, `credit_applied_cents=0`, `total_cents=amount`, and **never auto-applies existing credit**.

That last rule is not cosmetic. Without an advance command the operator would use `generate` plus a hand-edit — and `generate` auto-applies credit first, so a fresh 20,000 advance issued while 14,623 remained would be recorded as 5,377 gross and under-state the total credit by exactly the old balance. This is why the command moved from non-goal to goal.

Rejects: a non-positive amount, an amount that isn't a whole number of cents, a company whose `billing_mode` is `none`, and a company with no `billed_from` sender.

## Reading the balance

`atl invoice balance <company>` (RPC + CLI, read-only):

```
BClouder
  Advance issued              14,623.00 CAD   ES-0007
  Applied so far               6,000.00 CAD   ES-0008
  ───────────────────────────────────────────────────
  Remaining credit             8,623.00 CAD   ≈ 172.46 h @ 50.00/h
  Tracked since anchor           120.00 h     (6,000.00 CAD)
```

Hours-to-break-even is shown as approximate (`≈`): at 50.00/h every line total is a multiple of 50 cents so it is exact today, but that does not hold for an arbitrary rate.

`atl invoice list` also marks advance rows (`ADV`) and shows the applied amount. **This is not a display-only tweak** — `ListInvoicesByCompany` and `ListAllInvoices` currently select only `id, company_name, sent_at, note`, and `rpcapi`'s list item carries no number or totals. It means two queries changed, `rpcapi` fields added, `make sqlc`, and the CLI printer reworked.

## Guards

- **`InvoiceDelete` refuses rows with `kind='advance'` or `credit_applied_cents > 0`** unless `--force`, and prints the orphaned PDF path. Deleting a drawdown invoice returns credit the client already saw applied on a PDF they hold; the tool would then spend that 6,000 twice. The row is deleted but the PDF stays on disk and the number is never reclaimed, so this is a correction that must be deliberate.
- **`atl company delete` refuses a company that has invoices** unless `--force`. `ON DELETE CASCADE` plus `foreign_keys(1)` means one unconfirmed command currently vaporises the advance along with the history.
- **Render the PDF to a temp file in the sender directory and `os.Rename` after `tx.Commit()`.** Today it is written to its final path before the insert; a crash or deadline between the two rolls back the row (the derived balance stays correct) but leaves a complete, numbered PDF on disk. Emailed by accident, the client holds an invoice the tool has no record of — and `invoiceGenerateRPC` retries three times on `context deadline exceeded`, so a lost reply after a successful commit can allocate a second number and consume the credit twice.

## Edge cases

- **Credit exceeds the month** → due 0.00, remainder carries. **Credit smaller** → due is the excess, credit lands exactly on 0.
- **Zero tracked hours** → line 0.00, nothing consumed; `--allow-empty` still required.
- **Several advances** → they sum, and each carries `credit_applied_cents=0` by construction.
- **Rounding** → at 50.00/h every line total is a multiple of 50 cents and the advance is too, so the credit hits 0 dead-on. More generally, integer-cent `min()` means a residue is never *stuck*: any later invoice of ≥ 1 cent consumes it.
- **Deleting the advance before any drawdown** → credit returns to 0, correct with no compensating logic.
- **No cross-company leak** — `company_id` is the only key, `companies.name` is UNIQUE, and rowid reuse is unreachable under `ON DELETE CASCADE`.

## Testing

Table-driven, matching existing style:

- `internal/invoice` — the `max(0, min(credit, line_total − goodwill))` arithmetic including the negative-credit clamp; `AmountDueCents()` with both reductions; `BuildDoc` rejecting `goodwill + applied > line_total`. Pure, no DB.
- `internal/store` — `CompanyCreditBalance` over: advance only, partial drawdown, exhausted credit, several advances, deleted advance, a goodwill discount (must **not** move the balance), and a currency mismatch (must be excluded).
- `internal/daemon` — `generate` applies credit and writes back the right three columns; `--no-credit` bills full; `--dry-run` writes nothing; `InvoiceAdvance` never auto-applies; `InvoiceDelete` refuses a drawdown row without `--force`; the balance read does not deadlock inside the transaction.
- `internal/cli` — `invoice balance` formatting including zero credit; the menu advance flow's preview-and-confirm.
- `internal/invoice/pdf_test.go` — assert both reduction rows render with the advance number, since the current `sampleDoc` never exercises them.

## Manual steps

Ordering matters: the migration alone leaves the balance at **0**, because `ES-0007` defaults to `kind='hourly'`.

```sh
cd ~/.antitimely
sqlite3 db.sqlite ".backup 'db.sqlite.backup-pre-kind-backfill-20260803'"
sqlite3 db.sqlite "UPDATE invoices SET kind='advance' WHERE id=7;"
sqlite3 db.sqlite "SELECT id, number, kind, total_cents, credit_applied_cents FROM invoices WHERE company_id=3;"
```

Deliberately not in `invoiceMigrations`: a code migration naming a specific invoice id would be wrong for any other database. No `SIGHUP` is needed — invoices are read per-call from the DB and are not part of the rule/allowlist cache.

## Billing-runbook corrections (independent of the code)

`ES-0006` and `ES-0007` share `sent_at` exactly, which **already breaks** `docs/billing-runbook.md`:

- **Step 2** derives the period from the two most recent `sent_at` values → START == END → the next timesheet computes **0 h**. Add `AND kind='hourly'` (or `AND number IS NOT NULL` before the column exists).
- **Step 3** reconciles `deduped hours × 50` against the invoice total; once a drawdown lands, the total is net. Compare against the **Subtotal**.
- **Step 6**'s final check ("timesheet total ≥ invoice total") needs the same rewording.
