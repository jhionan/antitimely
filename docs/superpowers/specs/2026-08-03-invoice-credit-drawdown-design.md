# Advance credit and automatic invoice drawdown — design

**Date:** 2026-08-03
**Status:** approved (brainstorming) — ready for implementation plan

## Problem

BClouder asked to be invoiced 20,000.00 CAD for August 2026. Hours actually tracked in the period came to 5,377.00 CAD (`ES-0006`), so the remaining 14,623.00 CAD was issued as an advance (`ES-0007`, 292.46 h at 50.00 CAD/h) — money paid before the work exists.

That advance has to be worked off. Every subsequent invoice should bill the full value of the hours tracked, then discount that value down — to zero while the advance still covers it, and to the excess once it doesn't — until the credit reaches zero and invoices resume billing normally.

Nothing in antitimely models this. `atl invoice generate` computes hours × rate and charges it. `--discount` exists but is a manual flat number the operator must compute and remember, capped at the line-item total, and recorded nowhere — so the remaining credit lives only in someone's head. `ES-0007` itself had to be produced by a throwaway Go program because there is no fixed-amount path for an hourly company.

## Goals

- **Store the advance** so the tool, not the operator, knows a credit exists.
- **Apply it automatically** on every `atl invoice generate`, discounting the invoice by `min(remaining_credit, line_total)` until the credit is exhausted.
- **Make the balance readable** on demand, in money and in hours-to-break-even.
- **Keep the invoice honest**: the client sees hours worked, the advance applied, and what is due.
- **Never drift**: the balance must be derivable from issued documents, not held as a mutable counter.

## Non-goals

- **No `atl invoice advance` command.** Creating a *future* advance stays a manual job (as `ES-0007` was). Adding it is ~30 lines reusing the generate path, but there is exactly one advance so far; revisit on the second.
- **No remaining-balance line on the client-facing PDF.** The invoice explains its own total (hours, advance applied, amount due) but does not carry a running account balance — that is a statement fact, and Spanish autónomo invoices are better kept minimal. Reversible later: one line in `pdf.go`.
- **No `credits` table, no multi-advance lifecycle** (partial refunds, expiry, credits not tied to an invoice). The chosen model upgrades into one cleanly if that day comes.
- No change to how hours are counted, how the anchor is chosen for hourly invoices, or how PDFs are laid out beyond a single label.

## Data model

Two columns on `invoices`, added to `schema.sql`'s `CREATE TABLE` (fresh DBs) **and** appended to the idempotent `invoiceMigrations` list in `internal/daemon/daemon.go` (existing DBs), tolerating `duplicate column` like the entries already there:

```sql
kind            TEXT NOT NULL DEFAULT 'hourly',   -- 'hourly' | 'advance'
discount_cents  INTEGER NOT NULL DEFAULT 0        -- what this invoice consumed
```

The defaults are correct for the six pre-existing rows (`ES-0001`–`ES-0006`): they are hourly and they discounted nothing. `ES-0007` is the one row the defaults get wrong — it is an advance — and is corrected by the one-off backfill in **Manual step** below.

The balance is **derived, never stored** — one query in `queries.sql`, then `make sqlc`:

```sql
-- name: CompanyCreditBalance :one
SELECT COALESCE(SUM(CASE WHEN kind = 'advance' THEN total_cents ELSE 0 END), 0)
     - COALESCE(SUM(discount_cents), 0) AS remaining_cents
FROM invoices WHERE company_id = ?;
```

This works because `total_cents` on an hourly row already stores the **net** payable (`AmountDueCents()`, i.e. after discount), so advances-minus-discounts-applied is the entire story. For BClouder today it evaluates to `1462300 - 0` = 14,623.00 CAD.

Deriving rather than storing buys three things: no counter to drift, `--dry-run` safe by construction, and a full audit trail — every CAD of the advance is traceable to the invoice that consumed it.

**`kind` is validated in Go, not by the DB.** SQLite cannot add a `CHECK` constraint via `ALTER TABLE ADD COLUMN`; enforcing it would need the table-rebuild dance that `migrateObservationsSourceCheck` performs for `observations.source`. Not worth it — the only writer is our own code.

## Generation flow

One new step in `InvoiceGenerate` (`internal/daemon/rpc_invoice.go`), after the line item is computed and before `invoice.BuildDoc`:

```
line_total = hours × rate                    (unchanged)
credit     = CompanyCreditBalance(company)   (new)
applied    = min(credit, line_total)
due        = line_total − applied
```

`applied` is passed as the existing `BuildDocInput.DiscountCents`, so the PDF's `Subtotal` / `Discount` / `Amount Due` block and `InvoiceDoc.AmountDueCents()` work untouched. The row written back records `kind='hourly'`, `discount_cents=applied`, `total_cents=due` — which is what keeps the next balance query correct.

Worked example, 14,623.00 credit:

| Invoice | Hours | Line total | Applied | Due | Credit after |
|---|---:|---:|---:|---:|---:|
| ES-0008 | 120.00 h | 6,000.00 | 6,000.00 | **0.00** | 8,623.00 |
| ES-0009 | 400.00 h | 20,000.00 | 8,623.00 | **11,377.00** | 0.00 |
| ES-0010 | 200.00 h | 10,000.00 | 0.00 | **10,000.00** | 0.00 |

It self-terminates — there is no "until repaid" condition to implement.

### Flags

| Flag | Behavior |
|---|---|
| *(none)* | Auto-applies the credit. The default, so it cannot be forgotten. |
| `--no-credit` | Bills the full hours and ignores the credit. Escape hatch for a month to be charged in full. |
| `--discount=N` | **Error** when credit > 0, naming the remaining balance. A manual goodwill discount and a drawdown would be indistinguishable in `discount_cents`; refusing the ambiguity is cheaper than a `discount_kind` column. Combine with `--no-credit` to force a manual discount. |
| `--dry-run` | Prints the drawdown and resulting balance; writes nothing. |

### PDF label

`InvoiceDoc` gains `DiscountLabel string` (default `"Discount"`, set to `"Advance applied"` when the discount came from a drawdown). `pdf.go` line ~153 renders that field instead of the literal `"Discount"`. Zero-value behaviour is unchanged, so existing callers and tests keep passing.

### Anchor semantics

Unchanged and deliberately split:

- **Hourly invoices** close a billing period and move the anchor (`sent_at = now`), as today.
- **Advances** close no period and must not move it. `ES-0007` established this by hand: its `sent_at` is pinned to `ES-0006`'s (`1785766121`), so `LastInvoiceSentForCompany` — which is `MAX(sent_at)` — is unaffected and no tracked hours are silently dropped. Any future advance must do the same.

## Reading the balance

A new read-only `atl invoice balance <company>` (RPC + CLI):

```
BClouder
  Advance issued              14,623.00 CAD   ES-0007
  Applied so far               6,000.00 CAD   ES-0008
  ───────────────────────────────────────────────────
  Remaining credit             8,623.00 CAD   = 172.46 h @ 50.00/h
  Tracked since anchor           120.00 h     (6,000.00 CAD)
```

The per-invoice lines (which advance, which invoices consumed it) are **not** derivable from `CompanyCreditBalance`, which returns a single total. The command needs a second query alongside it — `CompanyCreditRows`: the company's rows where `kind='advance' OR discount_cents > 0`, returning `number`, `kind`, `total_cents`, `discount_cents`, ordered by `sent_at`. The aggregate stays the authority for the remaining figure; the rows are presentation.

`atl invoice list` additionally marks advance rows (`ADV`) and shows `discount_cents`, so a drawdown is visible without a second command.

## Edge cases

All fall out of `min()` and the derived query; none needs special-casing:

- **Credit exceeds the month** → due 0.00, remainder carries.
- **Credit smaller than the month** → due is the excess, credit lands exactly on 0.
- **Zero tracked hours** → line 0.00, applied 0.00, nothing consumed; `--allow-empty` still required, unchanged.
- **Several advances** → they sum.
- **Invoice deleted** → deleting the advance removes the credit; deleting a drawdown returns it. Both correct with no compensating logic.
- **Balance can never go negative** — `applied ≤ credit` and `applied ≤ line_total`.

## Testing

Table-driven, matching existing style:

- `internal/invoice` — drawdown arithmetic and the `DiscountLabel` switch. Pure, no DB.
- `internal/store` — `CompanyCreditBalance` over: advance only, advance + partial drawdown, exhausted credit, several advances, deleted advance.
- `internal/daemon` — `generate` applies the credit and writes back the correct `discount_cents`/`total_cents`; `--discount` with a live credit errors; `--no-credit` bills full; `--dry-run` writes nothing.
- `internal/cli` — `invoice balance` output formatting, including the zero-credit case.

## Manual step

Backfill the one existing advance, with a DB backup first (per the convention in `docs/billing-runbook.md` and prior manual edits):

```sh
cd ~/.antitimely
sqlite3 db.sqlite ".backup 'db.sqlite.backup-pre-kind-backfill-20260803'"
sqlite3 db.sqlite "UPDATE invoices SET kind='advance' WHERE id=7;"
sqlite3 db.sqlite "SELECT id, number, kind FROM invoices WHERE company_id=3;"   # verify
```

Deliberately not in `invoiceMigrations`: a code migration naming a specific invoice id would be wrong for any other database.

Then `kill -HUP` is **not** required — invoices are read straight from the DB and are not part of the rule/allowlist cache.
