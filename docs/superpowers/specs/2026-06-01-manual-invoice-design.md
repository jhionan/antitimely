# Manual invoices (`atl invoice new`)

## Motivation

Today every invoice is derived from tracked time: `atl invoice generate <company>`
counts ticks for the company's projects and multiplies by the company's rate.

Some billable work has no tracked hours in this database. Concretely: work
billed from the Brazilian entity (`br`) to BClouder at a different hourly rate
than the tracked `es → BClouder` stream. The user needs to issue an invoice by
entering the hours and rate by hand, choosing the billed-to client and the
sending entity, without any logged time.

## Scope

A new interactive command, `atl invoice new`, that builds and records an invoice
from manually-entered hours + rate. It reuses the existing
`BuildDoc → RenderPDF` rendering path and the existing per-sender invoice
numbering. It does **not** change how tracked (`hourly` / `monthly_fixed`)
invoices are generated, except to make the "last invoice" anchor ignore manual
invoices.

Out of scope for v1: issue/due-date overrides (defaults only), decimal hours,
editing/voiding a recorded manual invoice, multiple line items.

## User flow (wizard)

```
$ atl invoice new
Sender (who bills)?   [1] br  [2] es                 > 1
Currency?             [1] EUR  [2] CAD               > 2
Client (billed to)?   [1] BClouder [2] Foca.app …    > 1
Hours?                                               > 40
Rate per hour (CAD)?                                 > 60
Service description (optional)?                      > May 2026 — infra/wifi
── Preview ──  br → BClouder · 40h × $60.00 = $2,400.00 CAD · INV-014 ──
Generate?  [y/N]                                     > y
✓ INV-014 → ~/.antitimely/invoices/br/INV-014.pdf
```

Prompt rules:

- **Sender** — numbered list of `senders` keys from config. Required.
- **Currency** — numbered list of the chosen sender's bank-account currencies,
  i.e. the `bank_accounts` keys plus any `also_accepts` currencies. Required.
  Guarantees `BankFor(currency)` succeeds, so the wizard can never dead-end on a
  missing bank account.
- **Client** — numbered list of all companies in the DB. The selected company is
  the billed-to. If it has a `clients:` config entry, the full billed-to block
  (legal name, tax id, email, address) renders; otherwise the company name only.
- **Hours** — whole number ≥ 1. Re-prompt on invalid input.
- **Rate per hour** — major-currency amount, may include cents (e.g. `60` or
  `60.50`); parsed to integer cents. Re-prompt on invalid input.
- **Service description** — optional free text. Rendered under the line item in
  the slot where tracked invoices show the date range. Blank = no sub-line.
- **Generate? [y/N]** — the confirm gate doubles as dry-run: `N`/empty aborts
  with no PDF written and **no invoice number consumed**.

A matching entry is added to the interactive `atl` menu ("New manual invoice").

## Data model

```sql
-- idempotent, alongside the existing ADD COLUMN migrations in daemon.go
ALTER TABLE invoices ADD COLUMN manual INTEGER NOT NULL DEFAULT 0;
```

`manual = 1` marks a hand-entered invoice. Existing rows default to `0`.

### Stream independence

The hourly generator computes its period from the last invoice sent to the
company (`LastInvoiceSentForCompany`). That query gains `AND manual = 0` so a
manual `br → BClouder` invoice never moves BClouder's `es` hourly anchor. The
two streams are numbered by sender (`br` → `INV-`, `es` → `ES-`) and never
interfere.

## Components

### `internal/cli/invoice_new.go` (new)

The wizard. Reads `senders`/`clients` from config (via `LoadSendersConfig`) and
the company list via the existing `CompanyList` RPC. Validates each
prompt, builds the preview line, and on confirm calls `InvoiceGenerateManual`.
Pure prompt/format/validate logic (e.g. `parseHours`, `parseRateCents`) is
extracted so it is unit-testable without stdin.

### `InvoiceGenerateManual` RPC (`internal/daemon/rpc_invoice.go`)

Args:

```go
type InvoiceGenerateManualArgs struct {
    SenderKey     string // "br" | "es"
    ClientCompany string // billed-to company name (must exist)
    Currency      string
    Hours         int64  // whole hours, >= 1
    RateCents     int64  // per hour
    Description   string // optional, shown under the line item
    DryRun        bool
}
```

Flow (mirrors `InvoiceGenerate`, sharing its helpers):

1. Load + validate senders config; resolve `SenderKey` → `Sender`. Error if absent.
2. Resolve `ClientCompany` → company row (for `company_id`). Error if absent.
3. `sender.BankFor(Currency)` must succeed (the wizard already constrains this,
   but the RPC re-checks for non-wizard callers).
4. Build the line item from `Hours × RateCents` (see invoice package change).
5. Allocate the next number from the sender sequence
   (`SeedSenderState` + `AllocateNextInvoiceNumber`), inside a tx, exactly as the
   tracked path does. `DryRun` reads the next number without consuming it.
6. Render to `<output_dir>/<sender_key>/<number>.pdf` (temp file when `DryRun`).
7. Record in `invoices` with `manual = 1`, `company_id`, `sender_key`,
   `total_cents`, `currency`, `number`, `pdf_path`. Skipped when `DryRun`.

Reply mirrors `InvoiceGenerateReply` (number, path, total, currency, sender).

### `internal/invoice` (small addition)

`BuildDocInput` gains `BillingMode == "manual"` support with an explicit
quantity: a new field `ManualHoursTimes100 int64`. `ComputeLineItem` gets a
`manual` branch returning
`LineItem{QuantityHoursTimes100: hours×100, UnitCents: rateCents,
TotalCents: hours × rateCents}` (whole hours → exact, no rounding). The renderer
(`pdf.go`) is unchanged; `Description` is passed through `InvoiceDoc` and shown
in the existing period slot (the period line becomes the description when set,
and is omitted for manual invoices with no description).

## Error handling

- Unknown sender / client / unsupported currency → clear error before any write.
- Invalid hours/rate at the prompt → re-prompt (wizard); validated again in the
  RPC for non-interactive safety.
- Render or commit failure → the temp/output PDF is removed and the tx rolls
  back, leaving no orphan number or file (same guarantees as the tracked path).

## Testing

- `internal/invoice`: unit-test the `manual` line-item math (e.g. 40h × $60.00 =
  $2,400.00; 1h × $0.01) and a renderer test asserting the description renders
  and that a blank description omits the sub-line.
- `internal/cli`: unit-test `parseHours` (rejects 0, negatives, decimals,
  non-numbers) and `parseRateCents` (handles `60`, `60.50`, rejects junk).
- `internal/daemon`: a `manual = 1` insert is excluded from
  `LastInvoiceSentForCompany`; a dry-run consumes no number; a real run
  allocates and records.

## Build sequence

1. Schema column + anchor query filter.
2. `invoice` package: manual line-item math + description passthrough (TDD).
3. `InvoiceGenerateManual` RPC + reply, sharing helpers with `InvoiceGenerate`.
4. CLI wizard + menu entry; pure parse/validate helpers tested.
5. Manual end-to-end check: `atl invoice new` dry-run for `br → BClouder` in CAD.
