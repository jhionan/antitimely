# Invoice PDF generation — design

Date: 2026-05-27
Status: Draft (pending implementation plan)

## Goal

Generate professional client invoices as PDF files, opened in the system viewer
for review. Replaces the current ad-hoc workflow (Wise's invoice generator) with
a built-in command driven by the data antitimely already tracks.

## Scope (v1)

- One command produces one PDF per generation, opens it in the default viewer.
- No email / cloud upload / payment integration.
- No tax/VAT computation in v1 (column reserved for future use).
- Single page output, one aggregated line item per invoice.

## Billing model

Each client company has exactly one billing mode:

| Mode | Behavior |
|---|---|
| `hourly` | Quantity = tracked hours in period; unit price = rate. |
| `monthly_fixed` | Quantity = 1; unit price = total = fixed monthly amount. |
| `none` (default) | Not billable. Used for own/internal entities. |

Two real billing contexts at design time:

- **BClouder** — hourly, 50 CAD/h, billed from sender `es`.
- **Dentix** — monthly fixed, 3,000 EUR/mo, billed from sender `br`.

Internal companies (Foca.app, RiskAlert) stay `mode='none'` — they appear in
project lists but no invoice can be generated for them.

## Multi-sender architecture

The user issues invoices from two registered entities:

- `br` — Brazilian entity at Mateus Leme 2830, Curitiba; CNPJ 34.012.215/0001-44.
- `es` — Spanish entity at Escultor Miquel Navarro Navarro 2, Mislata; VAT
  ESZ2614896P.

Each sender has its own:

- Legal name and address
- Tax ID / company register
- Logo (optional)
- **Invoice number sequence** — tax authorities typically require gapless
  per-entity numbering, so sequences must not be shared.
- Bank accounts per currency (with `also_accepts` fallback for accounts that
  receive multiple currencies via auto-conversion).

Senders live in `~/.antitimely/config.yaml` because they represent
slow-changing user policy. The DB stores only an `invoices.sender_key`
reference per row for audit, and a `sender_state` table tracks the per-sender
invoice number cursor.

## Data model

### Schema additions

```sql
ALTER TABLE companies ADD COLUMN billing_mode  TEXT NOT NULL DEFAULT 'none'
                                                CHECK (billing_mode IN ('none','hourly','monthly_fixed'));
ALTER TABLE companies ADD COLUMN currency      TEXT;       -- 'EUR', 'CAD', ...
ALTER TABLE companies ADD COLUMN rate_cents    INTEGER;    -- cents/h or cents/mo
ALTER TABLE companies ADD COLUMN billed_from   TEXT;       -- key into config.senders; NULL for own entities

ALTER TABLE invoices  ADD COLUMN number        TEXT;       -- e.g. 'INV-014'
ALTER TABLE invoices  ADD COLUMN pdf_path      TEXT;       -- absolute path on disk
ALTER TABLE invoices  ADD COLUMN total_cents   INTEGER;    -- billed total
ALTER TABLE invoices  ADD COLUMN currency      TEXT;
ALTER TABLE invoices  ADD COLUMN sender_key    TEXT;

CREATE TABLE IF NOT EXISTS sender_state (
    sender_key            TEXT PRIMARY KEY,
    next_invoice_number   INTEGER NOT NULL
) STRICT;
```

All additions are idempotent via the existing `ALTER TABLE ... ADD COLUMN` /
`CREATE TABLE IF NOT EXISTS` pattern that already exists in
`internal/daemon/daemon.go`. Pre-existing rows get the default values.

### Money representation

All monetary amounts stored as **integer cents**. CAD/EUR/USD each use 100
cents per major unit. No floats anywhere in the money path — display
formatting converts to a `major.minor` string at the very last step.

### One-time data migration

Executed once via `atl invoice setup` (idempotent):

1. Create company "Dentix".
2. Move project "Dentix" → company "Dentix".
3. Set BClouder: `billing_mode='hourly'`, `currency='CAD'`, `rate_cents=5000`,
   `billed_from='es'`.
4. Set Dentix: `billing_mode='monthly_fixed'`, `currency='EUR'`,
   `rate_cents=300000`, `billed_from='br'`.

## Config file additions

`~/.antitimely/config.yaml`:

```yaml
senders:
  br:
    legal_name: "JHIONAN RIAN LARA DOS SANTOS"
    tax_id: "34.012.215/0001-44"
    tax_id_label: "CNPJ"            # rendered as "CNPJ 34.012.215/0001-44"
    address_lines: ["Mateus Leme 2830", "curitiba", "82200000", "Paraná", "Brazil"]
    logo_path: ""
    invoice:
      number_prefix: "INV-"
      number_pad: 3
      next_number: 14
    bank_accounts:
      EUR:
        title: "Local bank details"
        subtitle: "Use these details to pay EUR from bank accounts inside the Eurozone"
        fields:
          - { label: "Account holder", value: "JHIONAN RIAN LARA DOS SANTOS" }
          - { label: "BIC",            value: "TRWIBEB1XXX" }
          - { label: "IBAN",           value: "BE16 9052 8808 7074" }
          - { label: "Bank name and address",
              value: "Wise\nRue du Trône 100, 3rd floor\nBrussels\n1050\nBelgium" }

  es:
    legal_name: "JHIONAN RIAN LARA DOS SANTOS"
    tax_id: "ESZ2614896P"
    tax_id_label: "VAT"             # rendered as "VAT ESZ2614896P"
    address_lines: ["Escultor Miquel Navarro Navarro 2", "Mislata", "46920", "Spain"]
    logo_path: ""
    invoice:
      number_prefix: "ES-"
      number_pad: 4
      next_number: 1
    bank_accounts:
      EUR:
        also_accepts: [CAD]
        title: "Bank details"
        subtitle: "Account accepts EUR; CAD payments are auto-converted at the receiving bank's rate."
        fields:
          - { label: "Account holder", value: "JHIONAN RIAN LARA DOS SANTOS" }
          - { label: "BIC",            value: "NTSBESM1" }
          - { label: "IBAN",           value: "ES5115632626323269761258" }
          - { label: "Bank name and address",
              value: "N26 Bank AG, Sucursal en España\nPaseo de la Castellana 43\n28046 Madrid\nSpain" }

invoice:
  output_dir: "~/.antitimely/invoices"
  line_item_label: "Software development"
  due_days: 0
```

The `next_number` field in config is the **seed** for the DB cursor — only
consulted when `sender_state` has no row for that key. Subsequent allocations
come from the DB. This means hand-editing `next_number` after the first
generation has no effect; to fix a slip, edit `sender_state` directly.

## CLI surface

```
atl invoice generate <company> [--from=YYYY-MM-DD] [--to=YYYY-MM-DD]
                                [--issue-date=YYYY-MM-DD] [--note=...]
                                [--dry-run] [--allow-empty]
atl invoice show-senders         # print parsed senders + validation
atl invoice setup                # one-shot migration (idempotent)
atl invoice list                 # unchanged
atl invoice delete <id>          # unchanged
atl invoice send                 # kept as deprecated alias (anchor only, no PDF)
```

Flag semantics:

- `--from` / `--to` — period of work to include. Defaults below.
- `--issue-date` — overrides the date that appears on the invoice and the
  `sent_at` column. Default = now.
- `--note` — stored in the `invoices.note` column. Not printed on the PDF in v1.
- `--dry-run` — render PDF to a temp file and open it; skip all DB writes and
  do not consume an invoice number.
- `--allow-empty` — for hourly mode, lets a 0-tick period proceed and generate
  a 0-amount invoice (default is to reject).

`show-senders` validation: parses the config and reports per-sender any of:
missing `legal_name`, missing `tax_id`, missing or unparseable `address_lines`,
`invoice.next_number < 1`, no `bank_accounts` defined, or any
`bank_accounts.<currency>.fields` empty. Exits non-zero if any sender is
invalid. Reports each issue with the YAML path that needs editing.

### Period defaults

| Billing mode | Default `--from` | Default `--to` |
|---|---|---|
| `monthly_fixed` | First day of current calendar month | Last day of current calendar month |
| `hourly` | Last invoice's `sent_at` for this company, else company.`created_at` | Now |

`--from` / `--to` always override.

## Generation flow

Server-side, in daemon (single DB connection, atomic):

1. Resolve company by name; reject if `billing_mode='none'` or `billed_from` is empty.
2. Read fresh `~/.antitimely/config.yaml`. Resolve sender from
   `senders[company.billed_from]`. Reject if missing or incomplete.
3. Resolve period from args + defaults above.
4. Compute line item:
   - **monthly_fixed**: `quantity=1`, `unit_cents=rate_cents`, `total_cents=rate_cents`.
   - **hourly**: `hours = math.RoundToEven(ticks_in_period × tickSec / 3600 × 100) / 100`
     (banker's rounding to 2 decimals, matches Go's `math.RoundToEven` semantics
     and avoids systematic upward bias when halves accumulate).
     `quantity=hours`, `unit_cents=rate_cents`,
     `total_cents = math.RoundToEven(hours × rate_cents)` — using the
     displayed `hours` value, so client-side recomputation
     (`displayed_qty × displayed_unit`) matches `displayed_total` exactly.
     No "off by 1 cent" reconciliation issues.
   - Issue date is `--issue-date` if given, else `time.Now()` truncated to
     local midnight. Due date = issue_date + `invoice.due_days` × 24h.
5. Pick bank block for `company.currency`: exact key, else any block where
   `also_accepts` includes the currency. Reject if neither matches.
6. Open DB transaction (`BEGIN IMMEDIATE`).
7. Allocate next invoice number from `sender_state` (atomic
   seed-or-increment).
8. Render PDF via maroto to the final path:
   `<output_dir>/<sender_key>/<number>.pdf`. `mkdir -p` parent with 0700 first.
9. Insert row into `invoices` with `number, pdf_path, total_cents, currency,
   sender_key, sent_at, note`.
10. Commit. On any failure between 7-9: rollback (no number consumed); on
    PDF write failure, also `os.Remove` the partial file.
11. CLI receives reply, opens PDF via macOS `open <path>` (best-effort —
    if `open` fails, log warning and still return the path to the user).

### Atomicity invariants

- The DB transaction is the source of truth. If commit succeeds, the PDF
  must exist at the final path; if commit fails, no row is recorded and the
  PDF is removed.
- The only inconsistency window: process killed (SIGKILL / power loss)
  between step 8 (PDF on disk) and step 10 (commit). The result is an
  orphan PDF with no DB row referencing it — harmless, manually cleanable.
  Numbers are not consumed (rollback never committed step 7).
- `--dry-run` skips steps 6-10 entirely: PDF goes to `os.TempDir()`, no DB
  writes, no number consumed.

## PDF layout

Single A4 page, 20mm margins. maroto v2 grid components.

```
┌────────────────────────────────────────────────────────────────┐
│  [logo]  Invoice              Invoice number    Issue date     │
│                               INV-014           May 27, 2026   │
├────────────────────────────────────────────────────────────────┤
│  Billed to                    Issued by                        │
│  BClouder                     JHIONAN RIAN LARA DOS SANTOS     │
│                               VAT ESZ2614896P                  │
│                               Escultor Miquel Navarro Navarro 2│
│                               Mislata                          │
│                               46920                            │
│                               Spain                            │
│                                                                │
│  Product or service         Qty    Unit price    Tax    Total  │
│  ─────────────────────────────────────────────────────────     │
│  Software development       47.5   50.00 CAD     —    2,375.00 │
│  May 1 – May 27, 2026                                  CAD     │
│                                                                │
│                              Total excluding tax  2,375.00 CAD │
│                              Total tax            0.00 CAD     │
│                              Amount Due           2,375.00 CAD │
│                              Due by               May 27, 2026 │
│                                                                │
│  Ways to pay                                                   │
│  Bank details                                                  │
│  Account accepts EUR; CAD payments are auto-converted at the   │
│  receiving bank's rate.                                        │
│                                                                │
│  Reference            INV-014                                  │
│  Account holder       JHIONAN RIAN LARA DOS SANTOS             │
│  BIC                  NTSBESM1                                 │
│  IBAN                 ES5115632626323269761258                 │
│  Bank name and        N26 Bank AG, Sucursal en España          │
│  address              Paseo de la Castellana 43                │
│                       28046 Madrid, Spain                      │
└────────────────────────────────────────────────────────────────┘
```

Layout breakdown:

- Header row: logo (optional, ~24×24 px, top-left) + "Invoice" title; right
  side shows invoice number + issue date as a two-column metadata block.
- Billed-to / Issued-by two-column block. Billed-to is the client company's
  `name` (no address — matching INV-013). Issued-by stacks legal_name, then
  `tax_id_label + " " + tax_id` (e.g. `CNPJ 34.012.215/0001-44`,
  `VAT ESZ2614896P`), then each entry of `address_lines` on its own line.
- Line-items table with columns: Product or service, Qty, Unit price, Tax,
  Total. One row, with the period range (`May 1 – May 27, 2026`) in a smaller
  subline under the description.
- Totals block (right-aligned, four lines): Total excluding tax, Total tax
  (always "0.00 CCC" in v1), Amount Due (bold), Due by (date).
- "Ways to pay" section: rendered from the selected bank block. Title +
  subtitle + fields. The invoice number is auto-prepended as a `Reference`
  field.

Rendering rules:

- Currency: `2,375.00 CAD` (thousands comma, decimal point, currency suffix).
- Date: `May 27, 2026` (`Mon DD, YYYY`).
- Tax column kept but empty in v1; structure stays stable for future tax support.
- No QR code, no "Pay online" link (Wise-specific).
- Logo dimensions: target 24mm × 24mm bounding box, preserve aspect ratio.
  If `logo_path` is empty or the file is missing, the column shrinks and the
  "Invoice" title slides left.

## Package layout

```
internal/invoice/
  sender.go        # parse + validate + lookup senders from config
  period.go        # default period per billing_mode
  lineitem.go      # ticks → quantity, unit, total
  pdf.go           # maroto renderer
  invoice.go      # orchestrates: gather data → render PDF → return path
  *_test.go

internal/daemon/
  rpc_invoice.go   # InvoiceGenerate RPC handler

internal/cli/
  invoice.go       # add `generate`, `show-senders`, `setup`

internal/rpcapi/api.go     # + InvoiceGenerateArgs/Reply

queries.sql + regenerated internal/store/queries.sql.go
```

## New SQL queries

- `SetCompanyBilling` — update billing_mode/currency/rate_cents/billed_from by name.
- `GetCompanyForInvoice` — fetch company with all billing fields.
- `LastInvoiceForCompany` — used for default `--from` in hourly mode.
- `TicksForCompanyInRange` — count ticks across the company's non-paused
  projects in `[from, to)`.
- `AllocateInvoiceNumber` — atomic seed-or-increment on `sender_state`,
  returns the consumed number.
- `InsertInvoiceWithMeta` — insert with the new columns.

## Error handling

| Condition | Behavior |
|---|---|
| billing_mode='none' | Reject: "company X is not billable". |
| billed_from='es' but `senders.es` missing in config | Reject naming the key; suggest `show-senders`. |
| Currency=CAD but no bank account block matches | Reject. |
| Hourly mode, 0 ticks in period | Reject ("no time tracked in [from..to]"); `--allow-empty` escapes. |
| Output dir doesn't exist | mkdir -p with mode 0700. |
| Two `generate` calls land concurrently | Serialized by `SetMaxOpenConns(1)`; second waits up to busy_timeout. |
| PDF write fails | DB transaction rolls back; partial PDF removed; sender_state unchanged. |
| `open` fails to launch viewer | Log warning; return PDF path so user can open manually. Not a generation failure. |

## Testing

### Unit tests (`internal/invoice/*_test.go`)

- Period resolution per mode, with and without `--from`/`--to`.
- Line item math: hourly rounding, monthly fixed straight-through, zero hours.
- Currency formatting: zero, normal, thousands boundary, very large.
- Invoice number formatting: padding, prefix.
- Sender config parsing: missing tax_id, missing currency block,
  `also_accepts` fallback, malformed YAML.

### Integration test (`internal/daemon/`)

- End-to-end `InvoiceGenerate` RPC against in-memory SQLite:
  - PDF file exists and is non-empty.
  - `invoices` row inserted with correct columns.
  - `sender_state.next_invoice_number` incremented by exactly 1.
  - Extracted PDF text contains invoice number, amount string, sender legal
    name, and client company name.
- A `--dry-run` test confirms no DB writes and no PDF in output_dir.

PDF text extraction in tests via `github.com/ledongthuc/pdf` (read-only,
zero-dep, used only in tests).

## Open items deliberately out of scope (v1)

- Tax/VAT computation.
- Multi-line items (per-project breakdown).
- Detailed timesheet attachment / second page.
- Email or cloud-upload delivery.
- Internationalized currency formatting beyond ASCII (no `€` in totals — sticks
  to "EUR" suffix for safety on missing fonts).
- Editing/regenerating an existing invoice — current flow is generate-once.
  Bug fix would mean `invoice delete` + regenerate.

## Risks / things to watch

- **maroto API stability** — v2 is the current major version; pin to a minor
  in go.mod. Layout regressions would surface in golden text assertions.
- **Currency-without-bank-account misconfiguration** — easy mistake. Mitigated
  by validating during `show-senders`.
- **Out-of-order numbers** — if a user manually `DELETE`s the latest invoice
  row, the cursor doesn't roll back. Documented as expected: `sender_state`
  is the authority, not derived from `MAX(number)`.
- **Wise-issued historical invoices (INV-001..013)** — not in our DB. Tax
  auditors would need both sources together. Out of scope to import.
