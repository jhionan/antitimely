# Advance Credit and Invoice Drawdown Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Record client prepayments as `advance` invoices and have `atl invoice generate` automatically discount later invoices by the remaining credit until it is exhausted.

**Architecture:** Three columns on `invoices` (`kind`, `credit_applied_cents`, `discount_cents`) make the credit balance *derivable* — `SUM(advance totals) − SUM(credit_applied)` — so no mutable counter can drift. Invoice generation gains one clamped `min()` step; a new `InvoiceAdvance` RPC creates advances from CLI and menu; advances are filtered out of the billing-anchor query so they close no period.

**Tech Stack:** Go 1.22+, no CGO (`modernc.org/sqlite`), sqlc-generated bindings, `net/rpc` over a Unix socket, maroto for PDF rendering.

**Spec:** `docs/superpowers/specs/2026-08-03-invoice-credit-drawdown-design.md`

## Global Constraints

- **`internal/store/*.go` is generated.** Never hand-edit. SQL changes go in `schema.sql` / `queries.sql`, then `make sqlc`.
- **All money is integer cents.** No floats in money math. Hours are `hours × 100` integers (`QuantityHoursTimes100`).
- **`CHECK (kind IN ('hourly','advance'))` must ship in the same `ALTER TABLE ADD COLUMN` as the column.** `daemon.go` tolerates `duplicate column`, so a later attempt silently no-ops and retrofitting needs a full table rebuild.
- **The credit balance is read on `qtx` or before `BeginTx`.** The daemon runs `db.SetMaxOpenConns(1)`; querying via `s.Q` inside an open transaction deadlocks until the 10s handler deadline.
- **CLI flags come before positional args** (hand-rolled stdlib `flag`, no Cobra). Company names are case-sensitive.
- **New subcommands must be added to both** the `cli.Dispatch` switch **and** `printUsage`.
- **`make test` is `go test ./... -count=1`.** Run `go vet ./...` before each commit.
- Tests use `setupRPCServer(t)` (`internal/daemon/rpc_test.go:18`), which applies `schema.sql` to an in-memory DB — schema changes reach tests automatically; migration changes do not.

## File Structure

| File | Responsibility |
|---|---|
| `internal/invoice/credit.go` *(new)* | Pure drawdown arithmetic. Zero deps. |
| `internal/invoice/doc.go` | `InvoiceDoc` gains `CreditAppliedCents`, `CreditAppliedRef`; `AmountDueCents()` subtracts both reductions. |
| `internal/invoice/invoice.go` | `BuildDocInput` gains the same two fields; validates `goodwill + credit ≤ line total`. |
| `internal/invoice/pdf.go` | Renders up to two reduction rows. |
| `schema.sql` | Three new columns on `invoices` (fresh DBs). |
| `internal/daemon/daemon.go` | Idempotent `ALTER TABLE` entries (existing DBs). |
| `queries.sql` | `CompanyCreditBalance`, `CompanyCreditRows`; `LastInvoiceSentForCompany` excludes advances; list queries gain columns. |
| `internal/rpcapi/api.go` | `NoCredit` on generate args; `InvoiceAdvance*`, `InvoiceBalance*` types; list-item fields. |
| `internal/daemon/rpc_invoice.go` | Drawdown in `InvoiceGenerate`; new `InvoiceAdvance`; PDF rename-after-commit. |
| `internal/daemon/rpc.go` | `InvoiceBalance`; delete guards. |
| `internal/cli/invoice.go` | `--no-credit`, `invoice advance`, `invoice balance`, list printer. |
| `internal/cli/menu.go` | "Issue advance" menu entry. |
| `internal/cli/dispatch.go` | Subcommand routing + `printUsage`. |

---

### Task 1: Pure drawdown arithmetic

**Files:**
- Create: `internal/invoice/credit.go`
- Test: `internal/invoice/credit_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func ApplyCredit(creditCents, lineTotalCents, goodwillCents int64) int64`

- [ ] **Step 1: Write the failing test**

```go
package invoice

import "testing"

func TestApplyCredit(t *testing.T) {
	tests := []struct {
		name                          string
		credit, lineTotal, goodwill   int64
		want                          int64
	}{
		{"credit covers the whole invoice", 1462300, 600000, 0, 600000},
		{"credit smaller than invoice", 862300, 2000000, 0, 862300},
		{"no credit", 0, 1000000, 0, 0},
		{"goodwill takes precedence, credit fills the rest", 1462300, 600000, 100000, 500000},
		{"goodwill alone exceeds the line", 1462300, 600000, 700000, 0},
		{"zero-value invoice consumes nothing", 1462300, 0, 0, 0},
		{"negative credit is clamped, never negative", -600000, 1000000, 0, 0},
		{"exact exhaustion", 600000, 600000, 0, 600000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ApplyCredit(tc.credit, tc.lineTotal, tc.goodwill); got != tc.want {
				t.Errorf("ApplyCredit(%d, %d, %d) = %d, want %d",
					tc.credit, tc.lineTotal, tc.goodwill, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/invoice/ -run TestApplyCredit -v`
Expected: FAIL — `undefined: ApplyCredit`

- [ ] **Step 3: Write minimal implementation**

```go
package invoice

// ApplyCredit returns how much of an outstanding advance credit this invoice
// consumes. Goodwill discount is applied first and the credit fills whatever
// room remains, so goodwill+credit can never exceed the line total and trip
// BuildDoc's validation.
//
// The lower clamp is load-bearing, not defensive: a negative balance is
// reachable (delete an advance after a partial drawdown) and an unclamped
// negative would make BuildDoc reject every subsequent invoice for that
// company, with no route back through the CLI.
func ApplyCredit(creditCents, lineTotalCents, goodwillCents int64) int64 {
	room := lineTotalCents - goodwillCents
	if room < 0 {
		room = 0
	}
	applied := creditCents
	if applied > room {
		applied = room
	}
	if applied < 0 {
		applied = 0
	}
	return applied
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/invoice/ -run TestApplyCredit -v`
Expected: PASS (8 subtests)

- [ ] **Step 5: Commit**

```bash
go vet ./internal/invoice/
git add internal/invoice/credit.go internal/invoice/credit_test.go
git commit -m "feat(invoice): pure drawdown arithmetic with a negative-credit clamp"
```

---

### Task 2: Credit fields on the invoice document

**Files:**
- Modify: `internal/invoice/doc.go` (add fields, change `AmountDueCents`)
- Modify: `internal/invoice/invoice.go` (`BuildDocInput` fields + validation)
- Test: `internal/invoice/invoice_test.go` (append)

**Interfaces:**
- Consumes: `ApplyCredit` (Task 1) — not called here, but the values it returns are what these fields carry.
- Produces: `InvoiceDoc.CreditAppliedCents int64`, `InvoiceDoc.CreditAppliedRef string`, `BuildDocInput.CreditAppliedCents int64`, `BuildDocInput.CreditAppliedRef string`. `AmountDueCents() = LineItem.TotalCents - DiscountCents - CreditAppliedCents`.

- [ ] **Step 1: Write the failing test**

```go
func TestBuildDoc_CreditApplied(t *testing.T) {
	in := validBuildDocInput() // existing helper in this file; adjust name if different
	in.BillingMode = "monthly_fixed"
	in.RateCents = 600000 // line total 6,000.00
	in.DiscountCents = 0
	in.CreditAppliedCents = 600000
	in.CreditAppliedRef = "ES-0007"

	doc, err := BuildDoc(in)
	if err != nil {
		t.Fatalf("BuildDoc: %v", err)
	}
	if doc.CreditAppliedCents != 600000 {
		t.Errorf("CreditAppliedCents = %d, want 600000", doc.CreditAppliedCents)
	}
	if doc.CreditAppliedRef != "ES-0007" {
		t.Errorf("CreditAppliedRef = %q, want %q", doc.CreditAppliedRef, "ES-0007")
	}
	if got := doc.AmountDueCents(); got != 0 {
		t.Errorf("AmountDueCents = %d, want 0", got)
	}
}

func TestBuildDoc_RejectsReductionsExceedingLineTotal(t *testing.T) {
	in := validBuildDocInput()
	in.BillingMode = "monthly_fixed"
	in.RateCents = 600000
	in.DiscountCents = 100000
	in.CreditAppliedCents = 550000 // 650000 > 600000

	if _, err := BuildDoc(in); err == nil {
		t.Fatal("expected an error when discount+credit exceeds the line total")
	}
}

func TestBuildDoc_RejectsNegativeCredit(t *testing.T) {
	in := validBuildDocInput()
	in.CreditAppliedCents = -1
	if _, err := BuildDoc(in); err == nil {
		t.Fatal("expected an error for negative credit applied")
	}
}
```

> If `validBuildDocInput()` does not exist in `invoice_test.go`, build the input literally from the fields already used by `TestBuildDoc_*` in that file — do not invent a helper in a different package.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/invoice/ -run TestBuildDoc_Credit -v`
Expected: FAIL — `in.CreditAppliedCents undefined`

- [ ] **Step 3: Write minimal implementation**

In `internal/invoice/doc.go`, add to `InvoiceDoc` beside `DiscountCents`:

```go
	// CreditAppliedCents is the portion of an outstanding advance consumed by
	// this invoice. Distinct from DiscountCents: only this figure moves the
	// company's credit balance. CreditAppliedRef names the advance invoice it
	// came from (FIFO — oldest advance with credit remaining), so the client's
	// bookkeeper can tie the two documents together.
	CreditAppliedCents int64
	CreditAppliedRef   string
```

Replace `AmountDueCents`:

```go
// AmountDueCents is the net payable: line-item total minus any flat discount
// and minus any advance credit applied.
func (d InvoiceDoc) AmountDueCents() int64 {
	return d.LineItem.TotalCents - d.DiscountCents - d.CreditAppliedCents
}
```

In `internal/invoice/invoice.go`, add to `BuildDocInput` beside `DiscountCents`:

```go
	CreditAppliedCents int64  // advance credit consumed by this invoice; 0 = none
	CreditAppliedRef   string // invoice number of the advance being drawn down
```

Replace the discount validation block with:

```go
	if in.DiscountCents < 0 {
		return InvoiceDoc{}, fmt.Errorf("discount must not be negative (got %d cents)", in.DiscountCents)
	}
	if in.CreditAppliedCents < 0 {
		return InvoiceDoc{}, fmt.Errorf("credit applied must not be negative (got %d cents)", in.CreditAppliedCents)
	}
	if in.DiscountCents+in.CreditAppliedCents > li.TotalCents {
		return InvoiceDoc{}, fmt.Errorf(
			"discount %d + credit applied %d exceeds line-item total %d cents",
			in.DiscountCents, in.CreditAppliedCents, li.TotalCents)
	}
```

And carry both fields into the returned `InvoiceDoc`:

```go
		DiscountCents:      in.DiscountCents,
		CreditAppliedCents: in.CreditAppliedCents,
		CreditAppliedRef:   in.CreditAppliedRef,
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/invoice/ -count=1`
Expected: PASS — including the pre-existing `BuildDoc` tests, which use zero-value credit fields and are unaffected.

- [ ] **Step 5: Commit**

```bash
go vet ./internal/invoice/
git add internal/invoice/doc.go internal/invoice/invoice.go internal/invoice/invoice_test.go
git commit -m "feat(invoice): carry advance credit on the invoice document"
```

---

### Task 3: Render the reduction rows

**Files:**
- Modify: `internal/invoice/pdf.go` (the totals block, around the existing `Discount` line)
- Test: `internal/invoice/pdf_test.go` (append)

**Interfaces:**
- Consumes: `InvoiceDoc.CreditAppliedCents`, `InvoiceDoc.CreditAppliedRef` (Task 2).
- Produces: nothing consumed by later tasks.

- [ ] **Step 1: Write the failing test**

`pdf_test.go` already extracts text via `github.com/ledongthuc/pdf` in `TestRenderPDF_TotalsBlock` — reuse that extraction helper.

```go
func TestRenderPDF_CreditAppliedRow(t *testing.T) {
	doc := sampleDoc()
	doc.LineItem = LineItem{QuantityHoursTimes100: 12000, UnitCents: 5000, TotalCents: 600000}
	doc.DiscountCents = 0
	doc.CreditAppliedCents = 600000
	doc.CreditAppliedRef = "ES-0007"

	out := filepath.Join(t.TempDir(), "credit.pdf")
	if err := RenderPDF(doc, out); err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	text := extractPDFText(t, out) // same helper TestRenderPDF_TotalsBlock uses

	if !strings.Contains(text, "Advance applied (ES-0007)") {
		t.Errorf("expected the advance row naming ES-0007; got:\n%s", text)
	}
	if strings.Contains(text, "Discount") {
		t.Errorf("Discount row must be omitted when zero; got:\n%s", text)
	}
	if !strings.Contains(text, "0.00 CAD") {
		t.Errorf("expected an Amount Due of 0.00 CAD; got:\n%s", text)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/invoice/ -run TestRenderPDF_CreditAppliedRow -v`
Expected: FAIL — the extracted text has no `Advance applied` row.

- [ ] **Step 3: Write minimal implementation**

In `pdf.go`, replace the single discount branch (currently `if doc.DiscountCents > 0 { ... totalLine{"Discount", ...} }`) with:

```go
	if doc.DiscountCents > 0 || doc.CreditAppliedCents > 0 {
		lines = append(lines, totalLine{"Subtotal", FormatMoney(doc.LineItem.TotalCents, doc.Currency), false})
	}
	if doc.CreditAppliedCents > 0 {
		label := "Advance applied"
		if doc.CreditAppliedRef != "" {
			label = "Advance applied (" + doc.CreditAppliedRef + ")"
		}
		lines = append(lines, totalLine{label, "-" + FormatMoney(doc.CreditAppliedCents, doc.Currency), false})
	}
	if doc.DiscountCents > 0 {
		lines = append(lines, totalLine{"Discount", "-" + FormatMoney(doc.DiscountCents, doc.Currency), false})
	}
```

> Match the surrounding code exactly — the existing block builds `totalLine{...}` values in a slice before rendering. Keep the Subtotal line's existing construction if one is already emitted for the discount case; do not emit it twice.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/invoice/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
go vet ./internal/invoice/
git add internal/invoice/pdf.go internal/invoice/pdf_test.go
git commit -m "feat(invoice): render the advance-applied row with its invoice number"
```

---

### Task 4: Schema columns and migration

**Files:**
- Modify: `schema.sql` (the `invoices` `CREATE TABLE`)
- Modify: `internal/daemon/daemon.go` (`invoiceMigrations` slice)
- Test: `internal/daemon/migrate_invoice_credit_test.go` *(new)*

**Interfaces:**
- Consumes: nothing.
- Produces: columns `kind TEXT NOT NULL DEFAULT 'hourly'`, `credit_applied_cents INTEGER NOT NULL DEFAULT 0`, `discount_cents INTEGER NOT NULL DEFAULT 0` on `invoices`.

- [ ] **Step 1: Write the failing test**

```go
package daemon

import (
	"database/sql"
	"strings"
	"testing"
)

// A DB created before this feature: invoices without the credit columns.
const preCreditSchema = `
CREATE TABLE companies (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE) STRICT;
CREATE TABLE invoices (
    id           INTEGER PRIMARY KEY,
    company_id   INTEGER NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    sent_at      INTEGER NOT NULL,
    note         TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    number       TEXT,
    total_cents  INTEGER
) STRICT;
`

func TestInvoiceCreditMigration(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(preCreditSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO companies (id, name) VALUES (1, 'BClouder');
		INSERT INTO invoices (id, company_id, sent_at, created_at, number, total_cents)
		VALUES (1, 1, 100, 100, 'ES-0006', 537700);`); err != nil {
		t.Fatal(err)
	}

	for _, q := range invoiceCreditMigrations {
		if _, err := db.Exec(q); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			t.Fatalf("migrate %q: %v", q, err)
		}
	}

	// Existing row gets correct defaults.
	var kind string
	var applied, discount int64
	if err := db.QueryRow(
		`SELECT kind, credit_applied_cents, discount_cents FROM invoices WHERE id=1`,
	).Scan(&kind, &applied, &discount); err != nil {
		t.Fatal(err)
	}
	if kind != "hourly" || applied != 0 || discount != 0 {
		t.Errorf("defaults = (%q,%d,%d), want (hourly,0,0)", kind, applied, discount)
	}

	// The CHECK must be live: a typo'd kind is rejected.
	if _, err := db.Exec(`UPDATE invoices SET kind='Advance' WHERE id=1`); err == nil {
		t.Fatal("expected CHECK constraint to reject kind='Advance'")
	}

	// Re-running is idempotent.
	for _, q := range invoiceCreditMigrations {
		if _, err := db.Exec(q); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			t.Fatalf("second run of %q: %v", q, err)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestInvoiceCreditMigration -v`
Expected: FAIL — `undefined: invoiceCreditMigrations`

- [ ] **Step 3: Write minimal implementation**

In `schema.sql`, inside `CREATE TABLE IF NOT EXISTS invoices (...)`, after `sender_key TEXT`:

```sql
    kind                  TEXT NOT NULL DEFAULT 'hourly'
                            CHECK (kind IN ('hourly','advance')),
    credit_applied_cents  INTEGER NOT NULL DEFAULT 0,
    discount_cents        INTEGER NOT NULL DEFAULT 0
```

In `internal/daemon/daemon.go`, add a package-level slice next to `invoiceMigrations` and run it in the same loop:

```go
// invoiceCreditMigrations adds the advance-credit columns. The CHECK must ship
// in the same statement as the column: the loop below tolerates "duplicate
// column", so a later attempt to add the constraint silently no-ops and the
// only remaining route is a full table rebuild.
var invoiceCreditMigrations = []string{
	"ALTER TABLE invoices ADD COLUMN kind TEXT NOT NULL DEFAULT 'hourly' CHECK (kind IN ('hourly','advance'))",
	"ALTER TABLE invoices ADD COLUMN credit_applied_cents INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE invoices ADD COLUMN discount_cents INTEGER NOT NULL DEFAULT 0",
}
```

Then extend the existing migration loop to iterate `append(invoiceMigrations, invoiceCreditMigrations...)`, or add a second loop with the same `duplicate column` tolerance.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run TestInvoiceCreditMigration -v && go test ./... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
go vet ./...
git add schema.sql internal/daemon/daemon.go internal/daemon/migrate_invoice_credit_test.go
git commit -m "feat(db): add kind, credit_applied_cents and discount_cents to invoices"
```

---

### Task 5: Balance queries and anchor filter

**Files:**
- Modify: `queries.sql` (two new queries; `LastInvoiceSentForCompany`)
- Regenerate: `internal/store/*` via `make sqlc`
- Test: `internal/daemon/rpc_invoice_test.go` (append a store-level test)

**Interfaces:**
- Consumes: Task 4's columns.
- Produces: `store.Queries.CompanyCreditBalance(ctx, CompanyCreditBalanceParams{CompanyID int64, Currency sql.NullString}) (int64, error)` and `CompanyCreditRows(ctx, CompanyCreditRowsParams{...}) ([]CompanyCreditRowsRow, error)`. Exact generated names must be read from `internal/store/queries.sql.go` after `make sqlc` and used verbatim in later tasks.

- [ ] **Step 1: Write the failing test**

```go
func TestCompanyCreditBalance(t *testing.T) {
	_, db, _ := setupRPCServer(t)
	q := store.New(db)
	ctx := context.Background()

	if _, err := db.Exec(`INSERT INTO companies (id, name) VALUES (3, 'BClouder')`); err != nil {
		t.Fatal(err)
	}
	// Advance of 14,623.00; a drawdown of 6,000.00; a goodwill discount of 500.00
	// that must NOT move the balance; and a EUR row that must be excluded.
	if _, err := db.Exec(`
		INSERT INTO invoices (company_id, sent_at, created_at, number, total_cents, currency, kind, credit_applied_cents, discount_cents) VALUES
		 (3, 100, 100, 'ES-0007', 1462300, 'CAD', 'advance', 0, 0),
		 (3, 200, 200, 'ES-0008',       0, 'CAD', 'hourly', 600000, 0),
		 (3, 300, 300, 'ES-0009',  950000, 'CAD', 'hourly',      0, 50000),
		 (3, 400, 400, 'ES-0010',  100000, 'EUR', 'advance',     0, 0)`); err != nil {
		t.Fatal(err)
	}

	got, err := q.CompanyCreditBalance(ctx, store.CompanyCreditBalanceParams{
		CompanyID: 3,
		Currency:  sql.NullString{String: "CAD", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != 862300 { // 1462300 - 600000; goodwill and the EUR advance excluded
		t.Errorf("CompanyCreditBalance = %d, want 862300", got)
	}
}

func TestLastInvoiceSentForCompany_IgnoresAdvances(t *testing.T) {
	_, db, _ := setupRPCServer(t)
	q := store.New(db)
	ctx := context.Background()

	if _, err := db.Exec(`INSERT INTO companies (id, name) VALUES (3, 'BClouder')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO invoices (company_id, sent_at, created_at, number, total_cents, currency, kind) VALUES
		 (3, 1000, 1000, 'ES-0006', 537700, 'CAD', 'hourly'),
		 (3, 5000, 5000, 'ES-0007', 1462300, 'CAD', 'advance')`); err != nil {
		t.Fatal(err)
	}

	got, err := q.LastInvoiceSentForCompany(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1000 {
		t.Errorf("anchor = %d, want 1000 (the advance at 5000 must not move it)", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run 'TestCompanyCreditBalance|TestLastInvoiceSentForCompany_IgnoresAdvances' -v`
Expected: FAIL — `q.CompanyCreditBalance undefined`, and the anchor test returns 5000.

- [ ] **Step 3: Write minimal implementation**

Append to `queries.sql`:

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

Change `LastInvoiceSentForCompany` to exclude advances:

```sql
-- name: LastInvoiceSentForCompany :one
SELECT sent_at FROM invoices
WHERE company_id = ? AND kind <> 'advance'
ORDER BY sent_at DESC, id DESC LIMIT 1;
```

Then:

```bash
make sqlc
```

Open `internal/store/queries.sql.go` and note the exact generated signatures — later tasks must use them verbatim. `total_cents` and `number` are nullable (`sql.NullInt64` / `sql.NullString`) because the May anchor row has both NULL.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
go vet ./...
git add queries.sql internal/store/ internal/daemon/rpc_invoice_test.go
git commit -m "feat(db): derive the credit balance and exclude advances from the anchor"
```

---

### Task 6: Apply the credit during generate

**Files:**
- Modify: `internal/rpcapi/api.go` (`InvoiceGenerateArgs.NoCredit`; reply fields)
- Modify: `internal/daemon/rpc_invoice.go` (drawdown step + write-back)
- Modify: `internal/cli/invoice.go` (`--no-credit`)
- Test: `internal/daemon/rpc_invoice_test.go` (append)

**Interfaces:**
- Consumes: `ApplyCredit` (Task 1), `CreditAppliedCents`/`CreditAppliedRef` (Task 2), `CompanyCreditBalance`/`CompanyCreditRows` (Task 5).
- Produces: `InvoiceGenerateArgs.NoCredit bool`; `InvoiceGenerateReply.CreditAppliedCents int64` and `.CreditRemainingCents int64` for CLI display.

- [ ] **Step 1: Write the failing test**

```go
func TestRPC_InvoiceGenerate_AppliesAdvanceCredit(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	seedHourlyCompanyWithTicks(t, client, db, 120.0) // 120 h at 50.00/h = 6,000.00

	if _, err := db.Exec(`
		INSERT INTO invoices (company_id, sent_at, created_at, number, total_cents, currency, kind)
		SELECT id, 1, 1, 'ES-0007', 1462300, 'CAD', 'advance' FROM companies WHERE name='BClouder'`); err != nil {
		t.Fatal(err)
	}

	var reply rpcapi.InvoiceGenerateReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceGenerate",
		rpcapi.InvoiceGenerateArgs{CompanyName: "BClouder"}, &reply); err != nil {
		t.Fatal(err)
	}

	if reply.CreditAppliedCents != 600000 {
		t.Errorf("CreditAppliedCents = %d, want 600000", reply.CreditAppliedCents)
	}
	if reply.CreditRemainingCents != 862300 {
		t.Errorf("CreditRemainingCents = %d, want 862300", reply.CreditRemainingCents)
	}

	var total, applied int64
	var kind string
	if err := db.QueryRow(
		`SELECT total_cents, credit_applied_cents, kind FROM invoices WHERE number='ES-0008'`,
	).Scan(&total, &applied, &kind); err != nil {
		t.Fatal(err)
	}
	if total != 0 || applied != 600000 || kind != "hourly" {
		t.Errorf("row = (total %d, applied %d, kind %q), want (0, 600000, hourly)", total, applied, kind)
	}
}

func TestRPC_InvoiceGenerate_NoCreditBillsFull(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	seedHourlyCompanyWithTicks(t, client, db, 120.0)
	if _, err := db.Exec(`
		INSERT INTO invoices (company_id, sent_at, created_at, number, total_cents, currency, kind)
		SELECT id, 1, 1, 'ES-0007', 1462300, 'CAD', 'advance' FROM companies WHERE name='BClouder'`); err != nil {
		t.Fatal(err)
	}

	var reply rpcapi.InvoiceGenerateReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceGenerate",
		rpcapi.InvoiceGenerateArgs{CompanyName: "BClouder", NoCredit: true}, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.CreditAppliedCents != 0 {
		t.Errorf("CreditAppliedCents = %d, want 0 with --no-credit", reply.CreditAppliedCents)
	}
	if reply.TotalCents != 600000 {
		t.Errorf("TotalCents = %d, want 600000", reply.TotalCents)
	}
}
```

> `seedHourlyCompanyWithTicks` is a new local test helper: create the company via `CompanyAdd`, call `SetCompanyBilling` with `hourly`/`CAD`/`5000`/`br`, insert one observation and `hours*720` tick rows on the 5s grid, and write the sender YAML the same way `TestRPC_InvoiceGenerate_MonthlyFixedHappyPath` does. Write it in this file; do not import test helpers across packages.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestRPC_InvoiceGenerate_Applies -v`
Expected: FAIL — `unknown field NoCredit`

- [ ] **Step 3: Write minimal implementation**

`internal/rpcapi/api.go` — add to `InvoiceGenerateArgs`:

```go
	NoCredit bool // skip automatic advance-credit drawdown
```

and to `InvoiceGenerateReply`:

```go
	CreditAppliedCents   int64
	CreditRemainingCents int64
```

`internal/daemon/rpc_invoice.go` — **before `BeginTx`** (so the single connection is free), read the balance:

```go
	var creditRemaining int64
	var creditRef string
	if !args.NoCredit {
		creditRemaining, err = s.Q.CompanyCreditBalance(ctx, store.CompanyCreditBalanceParams{
			CompanyID: co.ID,
			Currency:  co.Currency,
		})
		if err != nil {
			return fmt.Errorf("read credit balance: %w", err)
		}
		rows, err := s.Q.CompanyCreditRows(ctx, store.CompanyCreditRowsParams{
			CompanyID: co.ID,
			Currency:  co.Currency,
		})
		if err != nil {
			return fmt.Errorf("read credit rows: %w", err)
		}
		// FIFO: the oldest advance is the one we name on the document.
		for i := len(rows) - 1; i >= 0; i-- {
			if rows[i].Kind == "advance" && rows[i].Number.Valid {
				creditRef = rows[i].Number.String
				break
			}
		}
	}
```

After the line item is computed, apply it:

```go
	lineTotal := invoice.ComputeLineItem(co.BillingMode, ticks, s.TickIntervalSeconds, co.RateCents.Int64).TotalCents
	applied := invoice.ApplyCredit(creditRemaining, lineTotal, args.DiscountCents)
	if applied == 0 {
		creditRef = ""
	}
```

Pass both into `BuildDocInput`:

```go
		CreditAppliedCents: applied,
		CreditAppliedRef:   creditRef,
```

Write them back on insert (`InsertInvoiceFull` params — regenerate the query in `queries.sql` to accept `kind`, `credit_applied_cents`, `discount_cents`, then `make sqlc`):

```go
		Kind:               "hourly",
		CreditAppliedCents: applied,
		DiscountCents:      args.DiscountCents,
```

Populate the reply:

```go
	reply.CreditAppliedCents = applied
	reply.CreditRemainingCents = creditRemaining - applied
```

`internal/cli/invoice.go` — add the flag beside `--discount` and pass it through:

```go
	noCredit := fs.Bool("no-credit", false, "Do not apply any outstanding advance credit")
	// ...
	args.NoCredit = *noCredit
```

Print the drawdown after a successful generate:

```go
	if reply.CreditAppliedCents > 0 {
		fmt.Printf("Advance applied: %s (remaining %s)\n",
			invoice.FormatMoney(reply.CreditAppliedCents, reply.Currency),
			invoice.FormatMoney(reply.CreditRemainingCents, reply.Currency))
	}
```

Update `printUsage` to list `[--no-credit]` on the `invoice generate` line.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
go vet ./...
git add internal/rpcapi/api.go internal/daemon/rpc_invoice.go internal/cli/invoice.go queries.sql internal/store/ internal/daemon/rpc_invoice_test.go
git commit -m "feat(invoice): auto-apply advance credit when generating an invoice"
```

---

### Task 7: `atl invoice advance`

**Files:**
- Modify: `internal/rpcapi/api.go` (`InvoiceAdvanceArgs`, `InvoiceAdvanceReply`)
- Modify: `internal/daemon/rpc_invoice.go` (new `InvoiceAdvance` handler)
- Modify: `internal/cli/invoice.go`, `internal/cli/dispatch.go`
- Test: `internal/daemon/rpc_invoice_test.go` (append)

**Interfaces:**
- Consumes: everything from Tasks 2–6.
- Produces: `InvoiceAdvanceArgs{CompanyName string, AmountCents int64, Note string, IssueDateUnix int64, DryRun bool}`, `InvoiceAdvanceReply{Number, PDFPath, Currency string, TotalCents, CreditRemainingCents int64}`.

- [ ] **Step 1: Write the failing test**

```go
func TestRPC_InvoiceAdvance_CreatesCreditWithoutConsumingIt(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	seedHourlyCompanyWithTicks(t, client, db, 120.0)
	// Pre-existing credit that must NOT be auto-applied to a new advance.
	if _, err := db.Exec(`
		INSERT INTO invoices (company_id, sent_at, created_at, number, total_cents, currency, kind)
		SELECT id, 1, 1, 'ES-0007', 1462300, 'CAD', 'advance' FROM companies WHERE name='BClouder'`); err != nil {
		t.Fatal(err)
	}

	var reply rpcapi.InvoiceAdvanceReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceAdvance",
		rpcapi.InvoiceAdvanceArgs{CompanyName: "BClouder", AmountCents: 2000000}, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.TotalCents != 2000000 {
		t.Errorf("TotalCents = %d, want 2000000 gross", reply.TotalCents)
	}
	if reply.CreditRemainingCents != 3462300 {
		t.Errorf("CreditRemainingCents = %d, want 3462300", reply.CreditRemainingCents)
	}

	var kind string
	var applied int64
	if err := db.QueryRow(
		`SELECT kind, credit_applied_cents FROM invoices WHERE number=?`, reply.Number,
	).Scan(&kind, &applied); err != nil {
		t.Fatal(err)
	}
	if kind != "advance" || applied != 0 {
		t.Errorf("row = (%q, %d), want (advance, 0)", kind, applied)
	}
}

func TestRPC_InvoiceAdvance_RejectsNonPositiveAmount(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	seedHourlyCompanyWithTicks(t, client, db, 1.0)
	var reply rpcapi.InvoiceAdvanceReply
	err := client.Call(rpcapi.ServiceName+".InvoiceAdvance",
		rpcapi.InvoiceAdvanceArgs{CompanyName: "BClouder", AmountCents: 0}, &reply)
	if err == nil {
		t.Fatal("expected an error for a zero amount")
	}
}

func TestRPC_InvoiceAdvance_DoesNotMoveAnchor(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	seedHourlyCompanyWithTicks(t, client, db, 1.0)
	q := store.New(db)
	ctx := context.Background()
	if _, err := db.Exec(`
		INSERT INTO invoices (company_id, sent_at, created_at, number, total_cents, currency, kind)
		SELECT id, 1000, 1000, 'ES-0006', 537700, 'CAD', 'hourly' FROM companies WHERE name='BClouder'`); err != nil {
		t.Fatal(err)
	}
	var reply rpcapi.InvoiceAdvanceReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceAdvance",
		rpcapi.InvoiceAdvanceArgs{CompanyName: "BClouder", AmountCents: 100000}, &reply); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM companies WHERE name='BClouder'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	anchor, err := q.LastInvoiceSentForCompany(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if anchor != 1000 {
		t.Errorf("anchor = %d, want 1000 — an advance must close no period", anchor)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestRPC_InvoiceAdvance -v`
Expected: FAIL — `rpcapi.InvoiceAdvanceArgs` undefined

- [ ] **Step 3: Write minimal implementation**

`internal/rpcapi/api.go`:

```go
type InvoiceAdvanceArgs struct {
	CompanyName   string
	AmountCents   int64
	Note          string
	IssueDateUnix int64
	DryRun        bool
}

type InvoiceAdvanceReply struct {
	Number               string
	PDFPath              string
	Currency             string
	TotalCents           int64
	CreditRemainingCents int64
}
```

`internal/daemon/rpc_invoice.go` — a new handler mirroring `InvoiceGenerate`'s config/sender/number/render/insert sequence, with these differences:

```go
func (s *AntitimelyService) InvoiceAdvance(args rpcapi.InvoiceAdvanceArgs, reply *rpcapi.InvoiceAdvanceReply) error {
	// ... resolve company, sender, config exactly as InvoiceGenerate does ...
	if args.AmountCents <= 0 {
		return fmt.Errorf("advance amount must be positive (got %d cents)", args.AmountCents)
	}
	if co.RateCents.Int64 <= 0 {
		return fmt.Errorf("company %q has no rate; cannot express an advance in hours", co.Name)
	}
	// Hourly line shape: amount / rate hours at rate. Exact when the amount is a
	// whole multiple of the rate's cents-per-hour; the LineItem carries the
	// authoritative total either way.
	li := invoice.LineItem{
		QuantityHoursTimes100: args.AmountCents * 100 / co.RateCents.Int64,
		UnitCents:             co.RateCents.Int64,
		TotalCents:            args.AmountCents,
	}
	// ... build InvoiceDoc directly with li, CreditAppliedCents: 0, DiscountCents: 0 ...
	// ... insert with Kind: "advance", CreditAppliedCents: 0, SentAt: now.Unix() ...
}
```

`sent_at` is `now` — no pinning is needed, because Task 5 removed advances from the anchor query.

`internal/cli/invoice.go` — a `cmdInvoiceAdvance` using `flag.NewFlagSet("invoice advance", ...)` with `--amount` (parsed by the existing `parseMoneyCents`), `--note`, `--issue-date`, `--dry-run`. Route it in `dispatch.go`'s `invoice` switch and add it to `printUsage`:

```
  antitimely invoice  advance <company> --amount=AMOUNT [--note=...] [--issue-date=YYYY-MM-DD] [--dry-run]
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
go vet ./...
git add internal/rpcapi/api.go internal/daemon/rpc_invoice.go internal/cli/invoice.go internal/cli/dispatch.go internal/daemon/rpc_invoice_test.go
git commit -m "feat(invoice): atl invoice advance creates a prepayment credit"
```

---

### Task 8: `atl invoice balance`

**Files:**
- Modify: `internal/rpcapi/api.go`, `internal/daemon/rpc.go`, `internal/cli/invoice.go`, `internal/cli/dispatch.go`
- Test: `internal/daemon/rpc_invoice_test.go` (append)

**Interfaces:**
- Consumes: `CompanyCreditBalance`, `CompanyCreditRows` (Task 5).
- Produces: `InvoiceBalanceArgs{CompanyName string}`, `InvoiceBalanceReply{Currency string, RemainingCents, RateCents int64, Rows []InvoiceBalanceRow}` where `InvoiceBalanceRow{Number, Kind string, TotalCents, CreditAppliedCents int64}`.

- [ ] **Step 1: Write the failing test**

```go
func TestRPC_InvoiceBalance(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	seedHourlyCompanyWithTicks(t, client, db, 1.0)
	if _, err := db.Exec(`
		INSERT INTO invoices (company_id, sent_at, created_at, number, total_cents, currency, kind, credit_applied_cents)
		SELECT id, 100, 100, 'ES-0007', 1462300, 'CAD', 'advance', 0 FROM companies WHERE name='BClouder'
		UNION ALL
		SELECT id, 200, 200, 'ES-0008', 0, 'CAD', 'hourly', 600000 FROM companies WHERE name='BClouder'`); err != nil {
		t.Fatal(err)
	}

	var reply rpcapi.InvoiceBalanceReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceBalance",
		rpcapi.InvoiceBalanceArgs{CompanyName: "BClouder"}, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.RemainingCents != 862300 {
		t.Errorf("RemainingCents = %d, want 862300", reply.RemainingCents)
	}
	if len(reply.Rows) != 2 {
		t.Fatalf("len(Rows) = %d, want 2", len(reply.Rows))
	}
	if reply.Rows[0].Number != "ES-0008" { // newest first
		t.Errorf("Rows[0].Number = %q, want ES-0008", reply.Rows[0].Number)
	}
}

func TestRPC_InvoiceBalance_NoCredit(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	seedHourlyCompanyWithTicks(t, client, db, 1.0)
	var reply rpcapi.InvoiceBalanceReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceBalance",
		rpcapi.InvoiceBalanceArgs{CompanyName: "BClouder"}, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.RemainingCents != 0 || len(reply.Rows) != 0 {
		t.Errorf("want zero balance and no rows, got %d / %d rows", reply.RemainingCents, len(reply.Rows))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestRPC_InvoiceBalance -v`
Expected: FAIL — `rpcapi.InvoiceBalanceArgs` undefined

- [ ] **Step 3: Write minimal implementation**

Add the types to `rpcapi`, a handler in `rpc.go` that calls `GetCompanyForInvoice`, then `CompanyCreditBalance` and `CompanyCreditRows` (skipping rows whose `Number` is not `Valid`), and a `cmdInvoiceBalance` printer:

```go
	fmt.Printf("%s\n", args.CompanyName)
	for _, r := range reply.Rows {
		if r.Kind == "advance" {
			fmt.Printf("  Advance issued   %14s   %s\n", invoice.FormatMoney(r.TotalCents, reply.Currency), r.Number)
		} else {
			fmt.Printf("  Applied          %14s   %s\n", invoice.FormatMoney(r.CreditAppliedCents, reply.Currency), r.Number)
		}
	}
	fmt.Printf("  %s\n", strings.Repeat("─", 46))
	hours := float64(reply.RemainingCents) / float64(reply.RateCents)
	fmt.Printf("  Remaining credit %14s   ≈ %.2f h @ %s/h\n",
		invoice.FormatMoney(reply.RemainingCents, reply.Currency), hours,
		invoice.FormatMoney(reply.RateCents, reply.Currency))
```

Route in `dispatch.go` and add to `printUsage`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
go vet ./...
git add internal/rpcapi/api.go internal/daemon/rpc.go internal/cli/invoice.go internal/cli/dispatch.go internal/daemon/rpc_invoice_test.go
git commit -m "feat(cli): atl invoice balance reports remaining advance credit"
```

---

### Task 9: Safety guards

**Files:**
- Modify: `internal/daemon/rpc.go` (`InvoiceDelete`, `CompanyDelete`)
- Modify: `internal/daemon/rpc_invoice.go` (PDF rename-after-commit)
- Modify: `internal/rpcapi/api.go` (`Force` fields), `internal/cli/invoice.go`, `internal/cli/company.go`
- Test: `internal/daemon/rpc_invoice_test.go` (append)

**Interfaces:**
- Consumes: Task 4's columns.
- Produces: `InvoiceDeleteArgs.Force bool`, `CompanyDeleteArgs.Force bool`.

- [ ] **Step 1: Write the failing test**

```go
func TestRPC_InvoiceDelete_RefusesDrawdownWithoutForce(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	seedHourlyCompanyWithTicks(t, client, db, 1.0)
	if _, err := db.Exec(`
		INSERT INTO invoices (id, company_id, sent_at, created_at, number, total_cents, currency, kind, credit_applied_cents)
		SELECT 50, id, 200, 200, 'ES-0008', 0, 'CAD', 'hourly', 600000 FROM companies WHERE name='BClouder'`); err != nil {
		t.Fatal(err)
	}
	var reply rpcapi.InvoiceDeleteReply
	err := client.Call(rpcapi.ServiceName+".InvoiceDelete",
		rpcapi.InvoiceDeleteArgs{ID: 50}, &reply)
	if err == nil {
		t.Fatal("expected a refusal: deleting a drawdown re-issues credit the client already saw")
	}
	if !strings.Contains(err.Error(), "force") {
		t.Errorf("error should mention --force, got: %v", err)
	}

	// With Force it succeeds.
	if err := client.Call(rpcapi.ServiceName+".InvoiceDelete",
		rpcapi.InvoiceDeleteArgs{ID: 50, Force: true}, &reply); err != nil {
		t.Fatalf("forced delete: %v", err)
	}
}

func TestRPC_CompanyDelete_RefusesWithInvoices(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	seedHourlyCompanyWithTicks(t, client, db, 1.0)
	if _, err := db.Exec(`
		INSERT INTO invoices (company_id, sent_at, created_at, number, total_cents, currency, kind)
		SELECT id, 100, 100, 'ES-0007', 1462300, 'CAD', 'advance' FROM companies WHERE name='BClouder'`); err != nil {
		t.Fatal(err)
	}
	var reply rpcapi.CompanyDeleteReply
	err := client.Call(rpcapi.ServiceName+".CompanyDelete",
		rpcapi.CompanyDeleteArgs{Name: "BClouder"}, &reply)
	if err == nil {
		t.Fatal("expected a refusal: ON DELETE CASCADE would vaporise the advance")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run 'TestRPC_InvoiceDelete_Refuses|TestRPC_CompanyDelete_Refuses' -v`
Expected: FAIL — both deletes currently succeed.

- [ ] **Step 3: Write minimal implementation**

In `InvoiceDelete`, before deleting, fetch the row's `kind`, `credit_applied_cents` and `pdf_path` (add a `GetInvoiceByID` query + `make sqlc` if none exists) and refuse:

```go
	if !args.Force && (inv.Kind == "advance" || inv.CreditAppliedCents > 0) {
		return fmt.Errorf(
			"invoice %s carries advance credit (kind=%s, applied=%d cents); deleting it changes the credit balance "+
				"while the client still holds the PDF at %s — pass --force if that is intended",
			inv.Number.String, inv.Kind, inv.CreditAppliedCents, inv.PdfPath.String)
	}
```

In `CompanyDelete`, count invoices first and refuse unless `args.Force`.

In `rpc_invoice.go`, render to a temp path in the sender directory and rename after commit:

```go
	tmpPath := filepath.Join(senderDir, "."+number+".pdf.tmp")
	if err := invoice.RenderPDF(doc, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	// ... insert + tx.Commit() ...
	if err := os.Rename(tmpPath, pdfPath); err != nil {
		return fmt.Errorf("invoice %s committed but the PDF could not be moved into place: %w", number, err)
	}
```

Add `--force` to the CLI for both deletes and mention it in `printUsage`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
go vet ./...
git add internal/daemon/rpc.go internal/daemon/rpc_invoice.go internal/rpcapi/api.go internal/cli/ queries.sql internal/store/ internal/daemon/rpc_invoice_test.go
git commit -m "feat(invoice): guard credit-bearing deletes and land the PDF after commit"
```

---

### Task 10: Menu entry for issuing an advance

**Files:**
- Modify: `internal/cli/menu.go`
- Test: `internal/cli/menu_test.go` (append; create if absent)

**Interfaces:**
- Consumes: `InvoiceAdvanceArgs`/`Reply` (Task 7).
- Produces: nothing.

- [ ] **Step 1: Write the failing test**

Follow whatever the existing menu tests do for the generate flow. If `menu_test.go` does not exist, test the pure helper instead — extract the amount prompt into a testable function rather than testing terminal I/O:

```go
func TestParseAdvanceAmount(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"14623", 1462300, false},
		{"14623.00", 1462300, false},
		{"0", 0, true},
		{"-5", 0, true},
		{"abc", 0, true},
	}
	for _, tc := range tests {
		got, err := parseAdvanceAmount(tc.in)
		if tc.wantErr && err == nil {
			t.Errorf("parseAdvanceAmount(%q): expected an error", tc.in)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("parseAdvanceAmount(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestParseAdvanceAmount -v`
Expected: FAIL — `undefined: parseAdvanceAmount`

- [ ] **Step 3: Write minimal implementation**

```go
// parseAdvanceAmount wraps parseMoneyCents with the advance-specific rule that
// the amount must be strictly positive.
func parseAdvanceAmount(s string) (int64, error) {
	cents, err := parseMoneyCents(s)
	if err != nil {
		return 0, err
	}
	if cents <= 0 {
		return 0, fmt.Errorf("advance amount must be positive")
	}
	return cents, nil
}
```

Then add the menu entry under Invoices, following the existing generate flow in `menu.go` (numbered company picker → prompt → preview → confirm → call → open + reveal):

```go
	case "3": // Issue advance
		co, ok := pickCompany(companies)
		if !ok {
			return
		}
		raw := prompt("Advance amount (" + co.Currency + "): ")
		cents, err := parseAdvanceAmount(raw)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
		fmt.Printf("Issue an advance of %s to %s? This burns an invoice number. [y/N] ",
			invoice.FormatMoney(cents, co.Currency), co.Name)
		if !confirmYes() {
			return
		}
		// ... call InvoiceAdvance, then openAndReveal(reply.PDFPath) ...
```

> Use the exact helper names already in `menu.go` (`pickCompany`, `prompt`, `confirmYes`, the open/reveal helper). Read the file before writing — do not invent names.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
go vet ./...
git add internal/cli/menu.go internal/cli/menu_test.go
git commit -m "feat(cli): issue an advance from the interactive menu"
```

---

### Task 11: Show credit in `atl invoice list`

**Files:**
- Modify: `queries.sql` (`ListInvoicesByCompany`, `ListAllInvoices`), `internal/store/` via `make sqlc`
- Modify: `internal/rpcapi/api.go` (list item fields), `internal/daemon/rpc.go`, `internal/cli/invoice.go`
- Test: `internal/daemon/rpc_invoice_test.go` (append)

**Interfaces:**
- Consumes: Task 4's columns.
- Produces: `InvoiceListItem.Number string`, `.Kind string`, `.TotalCents int64`, `.CreditAppliedCents int64`, `.Currency string`.

- [ ] **Step 1: Write the failing test**

```go
func TestRPC_InvoiceList_ShowsKindAndCredit(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	seedHourlyCompanyWithTicks(t, client, db, 1.0)
	if _, err := db.Exec(`
		INSERT INTO invoices (company_id, sent_at, created_at, number, total_cents, currency, kind, credit_applied_cents)
		SELECT id, 100, 100, 'ES-0007', 1462300, 'CAD', 'advance', 0 FROM companies WHERE name='BClouder'`); err != nil {
		t.Fatal(err)
	}
	var reply rpcapi.InvoiceListReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceList",
		rpcapi.InvoiceListArgs{CompanyName: "BClouder"}, &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(reply.Items))
	}
	if reply.Items[0].Kind != "advance" || reply.Items[0].Number != "ES-0007" {
		t.Errorf("item = (%q, %q), want (advance, ES-0007)", reply.Items[0].Kind, reply.Items[0].Number)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestRPC_InvoiceList_ShowsKindAndCredit -v`
Expected: FAIL — `Kind` undefined on the list item

- [ ] **Step 3: Write minimal implementation**

Extend both list queries to select `number, kind, total_cents, credit_applied_cents, currency`, and add `ORDER BY sent_at DESC, id DESC` so the ES-0006/ES-0007 timestamp tie is deterministic. `make sqlc`. Add the fields to `rpcapi.InvoiceListItem`, populate them in the handler (guarding `sql.Null*`), and extend the CLI printer with `NUMBER`, `KIND` (`ADV` for advances) and `APPLIED` columns.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
go vet ./...
git add queries.sql internal/store/ internal/rpcapi/api.go internal/daemon/rpc.go internal/cli/invoice.go internal/daemon/rpc_invoice_test.go
git commit -m "feat(cli): show invoice kind and applied credit in invoice list"
```

---

### Task 12: Ship it and backfill ES-0007

**Files:**
- Modify: `CLAUDE.md` (gotchas), `docs/billing-runbook.md` (drop the interim note)
- No test — this is deployment.

**Interfaces:**
- Consumes: everything.
- Produces: a correct live database.

- [ ] **Step 1: Build and restart the daemon**

```bash
make rebuild
atl status
```

Expected: daemon running. The migration runs at startup.

- [ ] **Step 2: Verify the columns landed with the CHECK**

```bash
sqlite3 ~/.antitimely/db.sqlite ".schema invoices" | grep -E "kind|credit_applied|discount_cents"
```

Expected: all three columns, and `CHECK (kind IN ('hourly','advance'))` present on `kind`. **If the CHECK is missing, stop** — the column shipped without it and adding it now needs a table rebuild.

- [ ] **Step 3: Back up, then backfill ES-0007**

```bash
cd ~/.antitimely
sqlite3 db.sqlite ".backup 'db.sqlite.backup-pre-kind-backfill-20260803'"
sqlite3 db.sqlite "UPDATE invoices SET kind='advance' WHERE id=7;"
sqlite3 db.sqlite "SELECT id, number, kind, total_cents, credit_applied_cents FROM invoices WHERE company_id=3;"
```

Expected: id 7 is `advance` with `total_cents=1462300`; every other row `hourly`.

- [ ] **Step 4: Verify the balance reads 14,623.00**

```bash
atl invoice balance BClouder
atl invoice generate BClouder --dry-run
```

Expected: balance 14,623.00 CAD; the dry run reports the credit it would apply and writes nothing.

- [ ] **Step 5: Update docs and commit**

Add to `CLAUDE.md` gotchas:

```markdown
- **Advance invoices are anchor-neutral.** `kind='advance'` rows are excluded from `LastInvoiceSentForCompany`, so they close no billing period. `atl invoice generate` auto-applies the remaining credit (`--no-credit` opts out); `atl invoice balance <company>` reports what's left. Deleting a credit-bearing invoice needs `--force`.
```

Remove the interim `AND i.number IS NOT NULL` fallback wording from `docs/billing-runbook.md` Step 2 now that `kind` exists.

```bash
git add CLAUDE.md docs/billing-runbook.md
git commit -m "docs: record advance-credit behaviour and drop the interim runbook filter"
```

---

## Self-Review

**Spec coverage:** data model → Task 4; derived balance + currency filter + tie-break → Task 5; anchor filter → Task 5; `max(0, min(...))` clamp → Task 1; goodwill-before-credit ordering → Tasks 1, 6; balance read outside the transaction → Task 6; two PDF reduction rows with the advance number → Tasks 2, 3; `--no-credit` → Task 6; advance creation on CLI **and** menu → Tasks 7, 10; `invoice balance` → Task 8; delete guards, company-delete guard, PDF rename-after-commit → Task 9; `invoice list` columns → Task 11; manual backfill and its ordering → Task 12; runbook corrections → already committed in `4bf3f24`, with the interim wording removed in Task 12.

**Placeholder scan:** no TBDs. Three steps intentionally say "read the file first" rather than inventing names (`validBuildDocInput` in Task 2, the `totalLine` construction in Task 3, `menu.go`'s helpers in Task 10) — these are pointers to existing code, not deferred work.

**Type consistency:** `ApplyCredit(credit, lineTotal, goodwill)` keeps the same argument order in Tasks 1 and 6. `CreditAppliedCents`/`CreditAppliedRef` are spelled identically across `BuildDocInput`, `InvoiceDoc`, the RPC types and the DB column (`credit_applied_cents`). `Kind` is the Go field for the `kind` column throughout. The generated sqlc parameter structs (`CompanyCreditBalanceParams`, `CompanyCreditRowsParams`) are named as sqlc will emit them for a two-argument query — Task 5 Step 3 instructs the implementer to confirm the emitted names before Tasks 6–11 consume them.

**Known risk:** Task 6 changes `InsertInvoiceFull`'s parameters, so every existing caller must be updated in the same commit or the package will not build.
