# Invoice PDF Generation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generate professional client invoices as PDFs from the data antitimely already tracks, opening the result in the system viewer.

**Architecture:** Pure-Go PDF rendering via maroto v2 (no external runtime deps). Multi-sender architecture (BR + ES legal entities) with each sender having its own gapless invoice number sequence stored in a new `sender_state` DB table. Per-company billing modes (`hourly` | `monthly_fixed` | `none`). Money is stored as integer cents. Atomic generation via a single DB transaction that wraps invoice-number allocation, file write, and `invoices` row insert — rollback removes the partial PDF.

**Tech Stack:** Go 1.25, modernc.org/sqlite (existing), sqlc (existing), gopkg.in/yaml.v3 (existing), `github.com/johnfercher/maroto/v2` (new), `github.com/ledongthuc/pdf` (new, test-only).

**Reference:** Spec at `docs/superpowers/specs/2026-05-27-invoice-pdf-design.md`.

---

## File Structure

**Create:**
- `internal/invoice/sender.go` — sender config types, YAML parsing, validation, bank-account lookup with `also_accepts` fallback
- `internal/invoice/sender_test.go`
- `internal/invoice/period.go` — default-period resolution per billing mode
- `internal/invoice/period_test.go`
- `internal/invoice/lineitem.go` — line item math (hourly vs monthly_fixed), banker's rounding
- `internal/invoice/lineitem_test.go`
- `internal/invoice/format.go` — currency string ("2,375.00 CAD"), date string ("May 27, 2026"), invoice-number formatting
- `internal/invoice/format_test.go`
- `internal/invoice/doc.go` — `InvoiceDoc` struct (the gathered data passed to the renderer)
- `internal/invoice/pdf.go` — maroto v2 renderer for `InvoiceDoc`
- `internal/invoice/pdf_test.go` — renders a fixture doc, extracts text, asserts contents
- `internal/invoice/invoice.go` — orchestration: company + sender + period + ticks → InvoiceDoc → PDF path
- `internal/daemon/rpc_invoice.go` — InvoiceGenerate RPC handler
- `internal/daemon/rpc_invoice_test.go`

**Modify:**
- `schema.sql` — add columns + `sender_state` table
- `internal/daemon/daemon.go` — idempotent `ALTER TABLE`s for the new columns
- `queries.sql` — new sqlc queries
- `internal/store/queries.sql.go` — sqlc-regenerated
- `internal/rpcapi/api.go` — new RPC types
- `internal/cli/invoice.go` — `generate`, `show-senders`, `setup` subcommands
- `internal/cli/dispatch.go` — usage text update
- `go.mod` / `go.sum` — add maroto, ledongthuc/pdf

---

## Task 1: Add new dependencies

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Add maroto v2 + ledongthuc/pdf**

Run:
```bash
go get github.com/johnfercher/maroto/v2@latest
go get github.com/ledongthuc/pdf@latest
```

- [ ] **Step 2: Verify the build still works**

Run: `go build ./...`
Expected: exits 0, no errors.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add maroto v2 + ledongthuc/pdf for invoice generation"
```

---

## Task 2: Schema additions for billing columns + sender_state table

**Files:**
- Modify: `schema.sql`
- Modify: `internal/daemon/daemon.go:73-83` (the existing ALTER TABLE region for `projects.paused`)

- [ ] **Step 1: Update schema.sql**

Open `schema.sql`. After the `invoices` CREATE TABLE block, append:

```sql
-- Per-sender invoice number cursor. Seed value comes from config the first
-- time a sender_key is used; subsequent allocations increment the row in this
-- table. Tax authorities require gapless numbering per legal entity, so we
-- never reset the counter or pull from MAX(invoices.number).
CREATE TABLE IF NOT EXISTS sender_state (
    sender_key            TEXT PRIMARY KEY,
    next_invoice_number   INTEGER NOT NULL
) STRICT;
```

In the same file, edit the `companies` CREATE TABLE to include the new columns at end:

```sql
CREATE TABLE IF NOT EXISTS companies (
    id            INTEGER PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    created_at    INTEGER NOT NULL,
    billing_mode  TEXT NOT NULL DEFAULT 'none'
                  CHECK (billing_mode IN ('none','hourly','monthly_fixed')),
    currency      TEXT,
    rate_cents    INTEGER,
    billed_from   TEXT
) STRICT;
```

And the `invoices` table:

```sql
CREATE TABLE IF NOT EXISTS invoices (
    id           INTEGER PRIMARY KEY,
    company_id   INTEGER NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    sent_at      INTEGER NOT NULL,
    note         TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    number       TEXT,
    pdf_path     TEXT,
    total_cents  INTEGER,
    currency     TEXT,
    sender_key   TEXT
) STRICT;
```

- [ ] **Step 2: Add idempotent ALTER TABLE migrations in daemon.go**

In `internal/daemon/daemon.go`, find the block starting `// Idempotent column add for older DBs that predate the paused feature.` (around line 78). After that existing `ALTER TABLE projects ADD COLUMN paused` block, append:

```go
// Idempotent column adds for older DBs that predate the invoice feature.
invoiceMigrations := []string{
    "ALTER TABLE companies ADD COLUMN billing_mode TEXT NOT NULL DEFAULT 'none'",
    "ALTER TABLE companies ADD COLUMN currency TEXT",
    "ALTER TABLE companies ADD COLUMN rate_cents INTEGER",
    "ALTER TABLE companies ADD COLUMN billed_from TEXT",
    "ALTER TABLE invoices ADD COLUMN number TEXT",
    "ALTER TABLE invoices ADD COLUMN pdf_path TEXT",
    "ALTER TABLE invoices ADD COLUMN total_cents INTEGER",
    "ALTER TABLE invoices ADD COLUMN currency TEXT",
    "ALTER TABLE invoices ADD COLUMN sender_key TEXT",
}
for _, q := range invoiceMigrations {
    if _, err := db.Exec(q); err != nil && !strings.Contains(err.Error(), "duplicate column") {
        return fmt.Errorf("migrate %q: %w", q, err)
    }
}
```

- [ ] **Step 3: Verify the daemon starts cleanly against a fresh DB**

Run:
```bash
rm -rf /tmp/atl-test-db && mkdir /tmp/atl-test-db
go run . daemon --db-path=/tmp/atl-test-db/db.sqlite &
DAEMON=$!
sleep 1
sqlite3 /tmp/atl-test-db/db.sqlite ".schema companies"
sqlite3 /tmp/atl-test-db/db.sqlite ".schema sender_state"
kill $DAEMON
```

Expected:
- `companies` schema shows `billing_mode`, `currency`, `rate_cents`, `billed_from`.
- `sender_state` schema exists.

If the daemon binary doesn't accept `--db-path`, just start `./antitimely daemon` and verify the columns on `~/.antitimely/db.sqlite` (it should already be on the local user's DB).

- [ ] **Step 4: Verify existing daemon DB also got the columns via migration**

Run: `sqlite3 ~/.antitimely/db.sqlite ".schema companies"`
Expected: shows the four new columns. (Daemon must have been restarted to apply migrations.)

- [ ] **Step 5: Commit**

```bash
git add schema.sql internal/daemon/daemon.go
git commit -m "feat(schema): add billing columns + sender_state table

Adds billing_mode/currency/rate_cents/billed_from to companies and
number/pdf_path/total_cents/currency/sender_key to invoices, plus the
new sender_state table that holds the per-sender invoice number cursor.

All additions are idempotent via the existing ALTER TABLE migration
pattern."
```

---

## Task 3: Add sqlc queries

**Files:**
- Modify: `queries.sql`
- Regenerate: `internal/store/queries.sql.go`

- [ ] **Step 1: Append new queries to queries.sql**

Add to end of `queries.sql`:

```sql
-- name: GetCompanyForInvoice :one
SELECT id, name, created_at, billing_mode, currency, rate_cents, billed_from
FROM companies WHERE name = ?;

-- name: SetCompanyBilling :exec
UPDATE companies
SET billing_mode = ?, currency = ?, rate_cents = ?, billed_from = ?
WHERE name = ?;

-- name: LastInvoiceSentForCompany :one
SELECT sent_at FROM invoices WHERE company_id = ? ORDER BY sent_at DESC LIMIT 1;

-- name: CountTicksForCompanyInRange :one
SELECT COUNT(*) FROM ticks t
JOIN projects p ON p.id = t.project_id
WHERE p.company_id = ? AND p.paused = 0
  AND t.ts >= ? AND t.ts < ?;

-- name: SeedSenderState :exec
INSERT OR IGNORE INTO sender_state (sender_key, next_invoice_number) VALUES (?, ?);

-- name: AllocateNextInvoiceNumber :one
UPDATE sender_state
SET next_invoice_number = next_invoice_number + 1
WHERE sender_key = ?
RETURNING next_invoice_number - 1 AS allocated;

-- name: InsertInvoiceFull :one
INSERT INTO invoices (
    company_id, sent_at, note, created_at,
    number, pdf_path, total_cents, currency, sender_key
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;
```

- [ ] **Step 2: Regenerate sqlc**

Run: `sqlc generate`
Expected: exits 0; `internal/store/queries.sql.go` updated.

- [ ] **Step 3: Verify generated names exist**

Run:
```bash
grep -E '^func.*\b(GetCompanyForInvoice|SetCompanyBilling|LastInvoiceSentForCompany|CountTicksForCompanyInRange|SeedSenderState|AllocateNextInvoiceNumber|InsertInvoiceFull)\b' internal/store/queries.sql.go
```
Expected: 7 lines, one per query.

- [ ] **Step 4: Verify the whole project still compiles**

Run: `go build ./...`
Expected: exits 0.

- [ ] **Step 5: Commit**

```bash
git add queries.sql internal/store/queries.sql.go
git commit -m "feat(store): queries for invoice generation + sender_state"
```

---

## Task 4: Sender config types and parsing

**Files:**
- Create: `internal/invoice/sender.go`
- Create: `internal/invoice/sender_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/invoice/sender_test.go`:

```go
package invoice

import (
	"os"
	"path/filepath"
	"testing"
)

const validSenderYAML = `
senders:
  br:
    legal_name: "JHIONAN RIAN LARA DOS SANTOS"
    tax_id: "34.012.215/0001-44"
    tax_id_label: "CNPJ"
    address_lines: ["Mateus Leme 2830", "curitiba", "82200000", "Paraná", "Brazil"]
    logo_path: ""
    invoice:
      number_prefix: "INV-"
      number_pad: 3
      next_number: 14
    bank_accounts:
      EUR:
        title: "Local bank details"
        subtitle: "Wise EUR"
        fields:
          - { label: "IBAN", value: "BE16..." }
  es:
    legal_name: "JHIONAN RIAN LARA DOS SANTOS"
    tax_id: "ESZ2614896P"
    tax_id_label: "VAT"
    address_lines: ["Escultor Miquel Navarro Navarro 2", "Mislata", "46920", "Spain"]
    invoice:
      number_prefix: "ES-"
      number_pad: 4
      next_number: 1
    bank_accounts:
      EUR:
        also_accepts: [CAD]
        fields:
          - { label: "IBAN", value: "ES51..." }

invoice:
  output_dir: "/tmp/inv"
  line_item_label: "Software development"
  due_days: 0
`

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadSendersConfig_ParsesValidFile(t *testing.T) {
	path := writeYAML(t, validSenderYAML)
	cfg, err := LoadSendersConfig(path)
	if err != nil {
		t.Fatalf("LoadSendersConfig: %v", err)
	}
	if len(cfg.Senders) != 2 {
		t.Fatalf("want 2 senders, got %d", len(cfg.Senders))
	}
	br, ok := cfg.Senders["br"]
	if !ok {
		t.Fatal("missing br sender")
	}
	if br.TaxIDLabel != "CNPJ" {
		t.Errorf("br.TaxIDLabel = %q, want CNPJ", br.TaxIDLabel)
	}
	if br.Invoice.NextNumber != 14 {
		t.Errorf("br.Invoice.NextNumber = %d, want 14", br.Invoice.NextNumber)
	}
	es := cfg.Senders["es"]
	if got := es.BankAccounts["EUR"].AlsoAccepts; len(got) != 1 || got[0] != "CAD" {
		t.Errorf("es EUR.AlsoAccepts = %v, want [CAD]", got)
	}
	if cfg.Invoice.LineItemLabel != "Software development" {
		t.Errorf("cfg.Invoice.LineItemLabel = %q", cfg.Invoice.LineItemLabel)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/invoice/... -run TestLoadSendersConfig_ParsesValidFile`
Expected: FAIL — `LoadSendersConfig` undefined.

- [ ] **Step 3: Implement sender.go**

Create `internal/invoice/sender.go`:

```go
// Package invoice generates client invoices as PDFs from the data antitimely
// tracks. Pure-logic types (Sender, BankAccount, InvoiceDoc, ...) and the
// maroto renderer live here; the daemon-side orchestration lives in
// internal/daemon/rpc_invoice.go.
package invoice

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Sender struct {
	LegalName    string         `yaml:"legal_name"`
	TaxID        string         `yaml:"tax_id"`
	TaxIDLabel   string         `yaml:"tax_id_label"`
	AddressLines []string       `yaml:"address_lines"`
	LogoPath     string         `yaml:"logo_path"`
	Invoice      InvoiceSeed    `yaml:"invoice"`
	BankAccounts map[string]Bank `yaml:"bank_accounts"`
}

type InvoiceSeed struct {
	NumberPrefix string `yaml:"number_prefix"`
	NumberPad    int    `yaml:"number_pad"`
	NextNumber   int64  `yaml:"next_number"`
}

type Bank struct {
	Title       string      `yaml:"title"`
	Subtitle    string      `yaml:"subtitle"`
	AlsoAccepts []string    `yaml:"also_accepts"`
	Fields      []BankField `yaml:"fields"`
}

type BankField struct {
	Label string `yaml:"label"`
	Value string `yaml:"value"`
}

type GlobalInvoiceConfig struct {
	OutputDir     string `yaml:"output_dir"`
	LineItemLabel string `yaml:"line_item_label"`
	DueDays       int    `yaml:"due_days"`
}

type SendersConfig struct {
	Senders map[string]Sender   `yaml:"senders"`
	Invoice GlobalInvoiceConfig `yaml:"invoice"`
}

// LoadSendersConfig parses the senders + invoice blocks from a YAML file.
// Other top-level keys (daemon settings) are ignored — the file is shared
// with the daemon's config.
func LoadSendersConfig(path string) (*SendersConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c SendersConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &c, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/invoice/... -run TestLoadSendersConfig_ParsesValidFile -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/invoice/sender.go internal/invoice/sender_test.go
git commit -m "feat(invoice): sender config types + YAML loader"
```

---

## Task 5: Sender validation

**Files:**
- Modify: `internal/invoice/sender.go`
- Modify: `internal/invoice/sender_test.go`

- [ ] **Step 1: Add failing tests**

Append to `internal/invoice/sender_test.go`:

```go
func TestValidate_HappyPath(t *testing.T) {
	cfg, _ := LoadSendersConfig(writeYAML(t, validSenderYAML))
	issues := cfg.Validate()
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

func TestValidate_DetectsMissingFields(t *testing.T) {
	bad := `
senders:
  bad:
    legal_name: ""
    tax_id: ""
    address_lines: []
    invoice:
      number_prefix: ""
      number_pad: 0
      next_number: 0
    bank_accounts: {}
invoice:
  output_dir: ""
`
	cfg, err := LoadSendersConfig(writeYAML(t, bad))
	if err != nil {
		t.Fatal(err)
	}
	issues := cfg.Validate()
	if len(issues) == 0 {
		t.Fatal("expected issues, got none")
	}
	// Should report all of: legal_name, tax_id, address_lines, number_prefix,
	// next_number, bank_accounts empty, output_dir empty.
	wantSubstr := []string{"legal_name", "tax_id", "address_lines",
		"number_prefix", "next_number", "bank_accounts", "output_dir"}
	for _, want := range wantSubstr {
		found := false
		for _, iss := range issues {
			if contains(iss, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no issue mentions %q; got %v", want, issues)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/invoice/... -run TestValidate -v`
Expected: FAIL — `Validate` undefined.

- [ ] **Step 3: Implement Validate**

Append to `internal/invoice/sender.go`:

```go
// Validate returns a list of human-readable issues found in the config.
// Empty slice = config is good to use.
func (c *SendersConfig) Validate() []string {
	var issues []string
	if c.Invoice.OutputDir == "" {
		issues = append(issues, "invoice.output_dir is empty")
	}
	if c.Invoice.LineItemLabel == "" {
		issues = append(issues, "invoice.line_item_label is empty")
	}
	for key, s := range c.Senders {
		if s.LegalName == "" {
			issues = append(issues, fmt.Sprintf("senders.%s.legal_name is empty", key))
		}
		if s.TaxID == "" {
			issues = append(issues, fmt.Sprintf("senders.%s.tax_id is empty", key))
		}
		if len(s.AddressLines) == 0 {
			issues = append(issues, fmt.Sprintf("senders.%s.address_lines is empty", key))
		}
		if s.Invoice.NumberPrefix == "" {
			issues = append(issues, fmt.Sprintf("senders.%s.invoice.number_prefix is empty", key))
		}
		if s.Invoice.NextNumber < 1 {
			issues = append(issues, fmt.Sprintf("senders.%s.invoice.next_number must be >= 1", key))
		}
		if len(s.BankAccounts) == 0 {
			issues = append(issues, fmt.Sprintf("senders.%s.bank_accounts has no entries", key))
		}
		for ccy, bank := range s.BankAccounts {
			if len(bank.Fields) == 0 {
				issues = append(issues, fmt.Sprintf("senders.%s.bank_accounts.%s.fields is empty", key, ccy))
			}
		}
	}
	return issues
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/invoice/... -run TestValidate -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/invoice/sender.go internal/invoice/sender_test.go
git commit -m "feat(invoice): sender config validation"
```

---

## Task 6: Bank account lookup with also_accepts fallback

**Files:**
- Modify: `internal/invoice/sender.go`
- Modify: `internal/invoice/sender_test.go`

- [ ] **Step 1: Add failing tests**

Append to `internal/invoice/sender_test.go`:

```go
func TestBankFor_DirectMatch(t *testing.T) {
	cfg, _ := LoadSendersConfig(writeYAML(t, validSenderYAML))
	br := cfg.Senders["br"]
	bank, ok := br.BankFor("EUR")
	if !ok {
		t.Fatal("EUR not found on br")
	}
	if len(bank.Fields) == 0 || bank.Fields[0].Label != "IBAN" {
		t.Errorf("unexpected bank: %+v", bank)
	}
}

func TestBankFor_AlsoAcceptsFallback(t *testing.T) {
	cfg, _ := LoadSendersConfig(writeYAML(t, validSenderYAML))
	es := cfg.Senders["es"]
	// es has only EUR but EUR.also_accepts=[CAD] → lookup for CAD finds EUR.
	bank, ok := es.BankFor("CAD")
	if !ok {
		t.Fatal("CAD not found on es via also_accepts")
	}
	if len(bank.AlsoAccepts) != 1 || bank.AlsoAccepts[0] != "CAD" {
		t.Errorf("found wrong bank: %+v", bank)
	}
}

func TestBankFor_NoMatch(t *testing.T) {
	cfg, _ := LoadSendersConfig(writeYAML(t, validSenderYAML))
	br := cfg.Senders["br"]
	if _, ok := br.BankFor("USD"); ok {
		t.Error("USD should not match anything on br")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/invoice/... -run TestBankFor -v`
Expected: FAIL — `BankFor` undefined.

- [ ] **Step 3: Implement BankFor**

Append to `internal/invoice/sender.go`:

```go
// BankFor returns the bank account block to display for the given currency.
// Tries an exact key match first; falls back to any block whose AlsoAccepts
// list contains the currency. Returns (zero, false) if neither matches.
func (s Sender) BankFor(currency string) (Bank, bool) {
	if b, ok := s.BankAccounts[currency]; ok {
		return b, true
	}
	for _, b := range s.BankAccounts {
		for _, alt := range b.AlsoAccepts {
			if alt == currency {
				return b, true
			}
		}
	}
	return Bank{}, false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/invoice/... -run TestBankFor -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/invoice/sender.go internal/invoice/sender_test.go
git commit -m "feat(invoice): bank-account lookup with also_accepts fallback"
```

---

## Task 7: Currency, date, and invoice-number formatting

**Files:**
- Create: `internal/invoice/format.go`
- Create: `internal/invoice/format_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/invoice/format_test.go`:

```go
package invoice

import (
	"testing"
	"time"
)

func TestFormatMoney(t *testing.T) {
	cases := []struct {
		cents    int64
		currency string
		want     string
	}{
		{0, "EUR", "0.00 EUR"},
		{50, "EUR", "0.50 EUR"},
		{300000, "EUR", "3,000.00 EUR"},
		{237500, "CAD", "2,375.00 CAD"},
		{1234567890, "USD", "12,345,678.90 USD"},
	}
	for _, tc := range cases {
		got := FormatMoney(tc.cents, tc.currency)
		if got != tc.want {
			t.Errorf("FormatMoney(%d, %q) = %q, want %q", tc.cents, tc.currency, got, tc.want)
		}
	}
}

func TestFormatHours(t *testing.T) {
	cases := []struct {
		hoursTimes100 int64 // hours expressed as hours*100 (since we round to 2dp)
		want          string
	}{
		{0, "0"},
		{50, "0.5"},
		{4750, "47.5"},
		{475, "4.75"},
		{12345, "123.45"},
	}
	for _, tc := range cases {
		got := FormatHours(tc.hoursTimes100)
		if got != tc.want {
			t.Errorf("FormatHours(%d) = %q, want %q", tc.hoursTimes100, got, tc.want)
		}
	}
}

func TestFormatDate(t *testing.T) {
	d := time.Date(2026, 5, 27, 14, 0, 0, 0, time.UTC)
	got := FormatDate(d)
	if got != "May 27, 2026" {
		t.Errorf("FormatDate = %q, want %q", got, "May 27, 2026")
	}
}

func TestFormatInvoiceNumber(t *testing.T) {
	cases := []struct {
		prefix string
		pad    int
		n      int64
		want   string
	}{
		{"INV-", 3, 14, "INV-014"},
		{"INV-", 3, 1, "INV-001"},
		{"ES-", 4, 1, "ES-0001"},
		{"ES-", 4, 1234, "ES-1234"},
		{"X", 0, 7, "X7"},
	}
	for _, tc := range cases {
		got := FormatInvoiceNumber(tc.prefix, tc.pad, tc.n)
		if got != tc.want {
			t.Errorf("FormatInvoiceNumber(%q, %d, %d) = %q, want %q",
				tc.prefix, tc.pad, tc.n, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/invoice/... -run TestFormat -v`
Expected: FAIL — undefined functions.

- [ ] **Step 3: Implement format.go**

Create `internal/invoice/format.go`:

```go
package invoice

import (
	"fmt"
	"strings"
	"time"
)

// FormatMoney renders an integer-cents amount as "X,XXX.YY CCC".
func FormatMoney(cents int64, currency string) string {
	negative := cents < 0
	if negative {
		cents = -cents
	}
	major := cents / 100
	minor := cents % 100
	majorStr := withThousandsSeparator(major)
	sign := ""
	if negative {
		sign = "-"
	}
	return fmt.Sprintf("%s%s.%02d %s", sign, majorStr, minor, currency)
}

func withThousandsSeparator(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteByte(',')
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	return b.String()
}

// FormatHours renders an hours-times-100 integer ("4750" = 47.5h) without
// trailing-zero noise. 0 → "0"; 50 → "0.5"; 4750 → "47.5"; 475 → "4.75".
func FormatHours(hoursTimes100 int64) string {
	whole := hoursTimes100 / 100
	frac := hoursTimes100 % 100
	if frac == 0 {
		return fmt.Sprintf("%d", whole)
	}
	if frac%10 == 0 {
		return fmt.Sprintf("%d.%d", whole, frac/10)
	}
	return fmt.Sprintf("%d.%02d", whole, frac)
}

// FormatDate renders "May 27, 2026".
func FormatDate(t time.Time) string {
	return t.Format("January 2, 2006")
}

// FormatInvoiceNumber renders a zero-padded invoice number.
func FormatInvoiceNumber(prefix string, pad int, n int64) string {
	if pad <= 0 {
		return fmt.Sprintf("%s%d", prefix, n)
	}
	return fmt.Sprintf("%s%0*d", prefix, pad, n)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/invoice/... -run TestFormat -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/invoice/format.go internal/invoice/format_test.go
git commit -m "feat(invoice): currency/date/invoice-number formatting"
```

---

## Task 8: Period defaults

**Files:**
- Create: `internal/invoice/period.go`
- Create: `internal/invoice/period_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/invoice/period_test.go`:

```go
package invoice

import (
	"testing"
	"time"
)

func TestDefaultPeriod_MonthlyFixed(t *testing.T) {
	// 2026-05-27 → period = 2026-05-01 .. 2026-05-31 (end-of-month inclusive,
	// returned as exclusive upper bound 2026-06-01 midnight).
	now := time.Date(2026, 5, 27, 14, 0, 0, 0, time.UTC)
	from, to := DefaultPeriod("monthly_fixed", now, 0)
	wantFrom := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	wantTo := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if !from.Equal(wantFrom) {
		t.Errorf("from = %v, want %v", from, wantFrom)
	}
	if !to.Equal(wantTo) {
		t.Errorf("to = %v, want %v", to, wantTo)
	}
}

func TestDefaultPeriod_HourlyWithPriorInvoice(t *testing.T) {
	now := time.Date(2026, 5, 27, 14, 0, 0, 0, time.UTC)
	lastInvoiceUnix := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC).Unix()
	from, to := DefaultPeriod("hourly", now, lastInvoiceUnix)
	wantFrom := time.Unix(lastInvoiceUnix, 0).UTC()
	if !from.Equal(wantFrom) {
		t.Errorf("from = %v, want %v", from, wantFrom)
	}
	if !to.Equal(now) {
		t.Errorf("to = %v, want %v", to, now)
	}
}

func TestDefaultPeriod_HourlyNoPriorInvoice(t *testing.T) {
	now := time.Date(2026, 5, 27, 14, 0, 0, 0, time.UTC)
	// lastInvoiceUnix == 0 means caller signals "no prior invoice"; caller
	// supplies company.created_at separately, so DefaultPeriod returns
	// (zero time, now) and caller fills the from itself.
	from, to := DefaultPeriod("hourly", now, 0)
	if !from.IsZero() {
		t.Errorf("from = %v, want zero", from)
	}
	if !to.Equal(now) {
		t.Errorf("to = %v, want %v", to, now)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/invoice/... -run TestDefaultPeriod -v`
Expected: FAIL — `DefaultPeriod` undefined.

- [ ] **Step 3: Implement period.go**

Create `internal/invoice/period.go`:

```go
package invoice

import "time"

// DefaultPeriod returns the default (from, to) window for an invoice based on
// the billing mode.
//
//	monthly_fixed: first day of `now`'s calendar month → first day of next month
//	hourly:        time.Unix(lastInvoiceSentAt) → now (caller substitutes
//	               company.created_at when lastInvoiceSentAt == 0)
//
// Returns (zero, now) for hourly when no prior invoice exists; the caller is
// responsible for substituting a sensible from (typically company.created_at).
func DefaultPeriod(billingMode string, now time.Time, lastInvoiceSentAt int64) (from, to time.Time) {
	switch billingMode {
	case "monthly_fixed":
		from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		to = from.AddDate(0, 1, 0)
		return
	case "hourly":
		to = now
		if lastInvoiceSentAt > 0 {
			from = time.Unix(lastInvoiceSentAt, 0).In(now.Location())
		}
		return
	}
	return time.Time{}, now
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/invoice/... -run TestDefaultPeriod -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/invoice/period.go internal/invoice/period_test.go
git commit -m "feat(invoice): default period resolution per billing mode"
```

---

## Task 9: Line-item computation

**Files:**
- Create: `internal/invoice/lineitem.go`
- Create: `internal/invoice/lineitem_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/invoice/lineitem_test.go`:

```go
package invoice

import "testing"

func TestLineItem_MonthlyFixed(t *testing.T) {
	li := ComputeLineItem("monthly_fixed", 0, 5, 300000)
	if li.QuantityHoursTimes100 != 100 { // we represent "1" as 100 to share the unit
		t.Errorf("QuantityHoursTimes100 = %d, want 100 (representing 1.00)", li.QuantityHoursTimes100)
	}
	if li.UnitCents != 300000 {
		t.Errorf("UnitCents = %d, want 300000", li.UnitCents)
	}
	if li.TotalCents != 300000 {
		t.Errorf("TotalCents = %d, want 300000", li.TotalCents)
	}
}

func TestLineItem_Hourly_47p5h(t *testing.T) {
	// 47.5 hours = 47.5 * 3600 = 171000 seconds = 34200 ticks at 5s/tick
	li := ComputeLineItem("hourly", 34200, 5, 5000) // 50.00 CAD/h
	// hours = 47.5 → represented as 4750
	if li.QuantityHoursTimes100 != 4750 {
		t.Errorf("QuantityHoursTimes100 = %d, want 4750", li.QuantityHoursTimes100)
	}
	if li.UnitCents != 5000 {
		t.Errorf("UnitCents = %d, want 5000", li.UnitCents)
	}
	// total = round(47.5 × 5000) = 237500
	if li.TotalCents != 237500 {
		t.Errorf("TotalCents = %d, want 237500", li.TotalCents)
	}
}

func TestLineItem_Hourly_BankersRounding(t *testing.T) {
	// 1.005h × 100 = 100.5 → banker's rounding to even → 100 (rounds to even)
	// Set up: ticks × tickSec / 3600 = 1.005 → ticks*tickSec = 3618
	// With tickSec=1, ticks=3618. Then hours*100 = 100.5
	li := ComputeLineItem("hourly", 3618, 1, 5000)
	// math.RoundToEven(100.5) = 100 (round half to even)
	if li.QuantityHoursTimes100 != 100 {
		t.Errorf("QuantityHoursTimes100 = %d, want 100 (banker's rounding)", li.QuantityHoursTimes100)
	}
}

func TestLineItem_Hourly_ZeroTicks(t *testing.T) {
	li := ComputeLineItem("hourly", 0, 5, 5000)
	if li.QuantityHoursTimes100 != 0 {
		t.Errorf("QuantityHoursTimes100 = %d, want 0", li.QuantityHoursTimes100)
	}
	if li.TotalCents != 0 {
		t.Errorf("TotalCents = %d, want 0", li.TotalCents)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/invoice/... -run TestLineItem -v`
Expected: FAIL — `ComputeLineItem`/`LineItem` undefined.

- [ ] **Step 3: Implement lineitem.go**

Create `internal/invoice/lineitem.go`:

```go
package invoice

import "math"

// LineItem holds the rendered numbers for the single aggregated row on an
// invoice. Quantity is stored as hours×100 (integer) to keep all money math
// in integers downstream.
type LineItem struct {
	QuantityHoursTimes100 int64 // 4750 = 47.50h; 100 = "1" for monthly_fixed
	UnitCents             int64
	TotalCents            int64
}

// ComputeLineItem produces the LineItem for the chosen billing mode.
//
//	monthly_fixed:  quantity=1 (encoded as 100), unit=rate_cents, total=rate_cents
//	hourly:         hours = banker's-round(ticks*tickSec/3600, 2)
//	                quantity = hours (×100), unit = rate_cents,
//	                total = banker's-round(hours × rate_cents)
//
// All rounding uses math.RoundToEven (banker's) to avoid systematic upward
// bias across many invoices. The total is computed from the *displayed*
// quantity so client-side multiplication matches our total exactly.
func ComputeLineItem(billingMode string, ticks int64, tickSec int, rateCents int64) LineItem {
	switch billingMode {
	case "monthly_fixed":
		return LineItem{
			QuantityHoursTimes100: 100,
			UnitCents:             rateCents,
			TotalCents:            rateCents,
		}
	case "hourly":
		hoursTimes100 := int64(math.RoundToEven(float64(ticks*int64(tickSec)) / 3600.0 * 100.0))
		// total_cents = round(hours * rate_cents) = round(hoursTimes100 * rateCents / 100)
		totalCents := int64(math.RoundToEven(float64(hoursTimes100*rateCents) / 100.0))
		return LineItem{
			QuantityHoursTimes100: hoursTimes100,
			UnitCents:             rateCents,
			TotalCents:            totalCents,
		}
	}
	return LineItem{}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/invoice/... -run TestLineItem -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/invoice/lineitem.go internal/invoice/lineitem_test.go
git commit -m "feat(invoice): line-item math with banker's rounding"
```

---

## Task 10: InvoiceDoc — the data passed to the renderer

**Files:**
- Create: `internal/invoice/doc.go`

- [ ] **Step 1: Create doc.go**

Create `internal/invoice/doc.go`:

```go
package invoice

import "time"

// InvoiceDoc is the fully-resolved data that the PDF renderer consumes.
// All composition / rate logic / DB lookups happen before this struct is
// built; the renderer only formats and lays out.
type InvoiceDoc struct {
	Number      string    // e.g. "INV-014"
	IssueDate   time.Time
	DueDate     time.Time
	PeriodFrom  time.Time
	PeriodTo    time.Time
	Currency    string    // "EUR", "CAD"

	// Billed-to: client company (we only show the name, like Wise).
	ClientName  string

	// Issued-by: us.
	Sender Sender

	// Line item (single aggregated row).
	LineItemLabel string // "Software development"
	LineItem      LineItem

	// Bank block to render in "Ways to pay".
	Bank Bank

	// Logo (optional; absolute path; empty = no logo).
	LogoPath string
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/invoice/...`
Expected: exits 0.

- [ ] **Step 3: Commit**

```bash
git add internal/invoice/doc.go
git commit -m "feat(invoice): InvoiceDoc type for renderer input"
```

---

## Task 11: PDF renderer — minimal scaffold + header

**Files:**
- Create: `internal/invoice/pdf.go`
- Create: `internal/invoice/pdf_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/invoice/pdf_test.go`:

```go
package invoice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ledongthuc/pdf"
)

func sampleDoc() InvoiceDoc {
	issue := time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC)
	return InvoiceDoc{
		Number:    "INV-014",
		IssueDate: issue,
		DueDate:   issue,
		PeriodFrom: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		PeriodTo:   time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC),
		Currency:  "EUR",
		ClientName: "Dentix",
		Sender: Sender{
			LegalName:    "JHIONAN RIAN LARA DOS SANTOS",
			TaxID:        "34.012.215/0001-44",
			TaxIDLabel:   "CNPJ",
			AddressLines: []string{"Mateus Leme 2830", "curitiba", "82200000", "Paraná", "Brazil"},
		},
		LineItemLabel: "Software development",
		LineItem: LineItem{
			QuantityHoursTimes100: 100,
			UnitCents:             300000,
			TotalCents:            300000,
		},
		Bank: Bank{
			Title:    "Local bank details",
			Subtitle: "Wise EUR",
			Fields: []BankField{
				{Label: "Account holder", Value: "JHIONAN RIAN LARA DOS SANTOS"},
				{Label: "BIC", Value: "TRWIBEB1XXX"},
				{Label: "IBAN", Value: "BE16 9052 8808 7074"},
			},
		},
	}
}

func extractPDFText(t *testing.T, path string) string {
	t.Helper()
	f, r, err := pdf.Open(path)
	if err != nil {
		t.Fatalf("pdf.Open: %v", err)
	}
	defer f.Close()
	var b strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		rows, _ := p.GetTextByRow()
		for _, row := range rows {
			for _, w := range row.Content {
				b.WriteString(w.S)
				b.WriteByte(' ')
			}
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func TestRenderPDF_HeaderHasInvoiceTitleAndNumber(t *testing.T) {
	doc := sampleDoc()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.pdf")
	if err := RenderPDF(doc, out); err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	info, err := os.Stat(out)
	if err != nil || info.Size() == 0 {
		t.Fatalf("output file missing or empty: %v", err)
	}
	text := extractPDFText(t, out)
	for _, want := range []string{"Invoice", "INV-014", "May 27, 2026"} {
		if !strings.Contains(text, want) {
			t.Errorf("PDF text missing %q\n---\n%s\n---", want, text)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/invoice/... -run TestRenderPDF_HeaderHasInvoiceTitleAndNumber -v`
Expected: FAIL — `RenderPDF` undefined.

- [ ] **Step 3: Implement minimal renderer (header only)**

Create `internal/invoice/pdf.go`:

```go
package invoice

import (
	"fmt"

	"github.com/johnfercher/maroto/v2"
	"github.com/johnfercher/maroto/v2/pkg/components/col"
	"github.com/johnfercher/maroto/v2/pkg/components/row"
	"github.com/johnfercher/maroto/v2/pkg/components/text"
	"github.com/johnfercher/maroto/v2/pkg/config"
	"github.com/johnfercher/maroto/v2/pkg/consts/align"
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/core"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

// RenderPDF writes the invoice document to outPath. Caller owns the path
// (mkdir parents beforehand). Overwrites any existing file at outPath.
func RenderPDF(doc InvoiceDoc, outPath string) error {
	cfg := config.NewBuilder().
		WithMargins(20, 20, 20).
		WithPageNumber(props.PageNumber{}).
		Build()
	m := maroto.New(cfg)

	// Header: "Invoice" big, with number + issue date on the right.
	m.AddRow(20,
		col.New(6).Add(
			text.New("Invoice", props.Text{
				Size: 24, Style: fontstyle.Bold,
			}),
		),
		col.New(3).Add(
			text.New("Invoice number", props.Text{Size: 8, Color: &props.GrayColor}),
			text.New(doc.Number, props.Text{Size: 10, Top: 4}),
		),
		col.New(3).Add(
			text.New("Issue date", props.Text{Size: 8, Color: &props.GrayColor}),
			text.New(FormatDate(doc.IssueDate), props.Text{Size: 10, Top: 4}),
		),
	)
	m.AddRows(row.New(2)) // small spacer
	// Horizontal divider below the header.
	m.AddRow(2, col.New(12).Add(
		text.New(strRepeat("_", 120), props.Text{Size: 6, Color: &props.GrayColor, Align: align.Left}),
	))

	if err := generateAndSave(m, outPath); err != nil {
		return err
	}
	return nil
}

func generateAndSave(m core.Maroto, outPath string) error {
	doc, err := m.Generate()
	if err != nil {
		return fmt.Errorf("maroto generate: %w", err)
	}
	if err := doc.Save(outPath); err != nil {
		return fmt.Errorf("save pdf: %w", err)
	}
	return nil
}

func strRepeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
```

> **Note:** maroto v2's API has small package-name variations between versions. If `props.GrayColor` doesn't exist on your version, replace with `props.Color{Red: 128, Green: 128, Blue: 128}`. If `align.Left` or `fontstyle.Bold` paths differ, adjust the import paths — the build error will name the right path. Treat any compile error as an instruction to refactor the import; do not change behavior.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/invoice/... -run TestRenderPDF_HeaderHasInvoiceTitleAndNumber -v`
Expected: PASS — PDF generated, text extraction finds "Invoice", "INV-014", "May 27, 2026".

If it fails on missing maroto symbols, fix the imports per the note above and re-run. If it fails on missing text in the PDF extract, that's a real renderer bug — fix the renderer.

- [ ] **Step 5: Commit**

```bash
git add internal/invoice/pdf.go internal/invoice/pdf_test.go
git commit -m "feat(invoice): PDF renderer scaffold + invoice header"
```

---

## Task 12: PDF renderer — Billed-to / Issued-by block

**Files:**
- Modify: `internal/invoice/pdf.go`
- Modify: `internal/invoice/pdf_test.go`

- [ ] **Step 1: Add failing test**

Append to `internal/invoice/pdf_test.go`:

```go
func TestRenderPDF_BilledTo_IssuedBy(t *testing.T) {
	doc := sampleDoc()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.pdf")
	if err := RenderPDF(doc, out); err != nil {
		t.Fatal(err)
	}
	text := extractPDFText(t, out)
	want := []string{
		"Billed to", "Dentix",
		"Issued by", "JHIONAN RIAN LARA DOS SANTOS",
		"CNPJ 34.012.215/0001-44",
		"Mateus Leme 2830", "curitiba", "Brazil",
	}
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("PDF text missing %q\n---\n%s\n---", w, text)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/invoice/... -run TestRenderPDF_BilledTo_IssuedBy -v`
Expected: FAIL — strings not in PDF.

- [ ] **Step 3: Add the Billed-to / Issued-by block to RenderPDF**

In `internal/invoice/pdf.go`, in `RenderPDF`, after the divider row, append the new section. Replace the line `if err := generateAndSave(m, outPath); err != nil {` and what's above with:

```go
	// (everything from the existing header section stays the same up to the divider)

	// Billed-to / Issued-by two-column block.
	m.AddRow(6,
		col.New(6).Add(
			text.New("Billed to", props.Text{Size: 9, Style: fontstyle.Bold}),
		),
		col.New(6).Add(
			text.New("Issued by", props.Text{Size: 9, Style: fontstyle.Bold}),
		),
	)
	taxLine := doc.Sender.TaxIDLabel
	if taxLine != "" {
		taxLine = taxLine + " " + doc.Sender.TaxID
	} else {
		taxLine = doc.Sender.TaxID
	}
	addressLines := append([]string{doc.Sender.LegalName, taxLine}, doc.Sender.AddressLines...)
	clientLines := []string{doc.ClientName}
	// Render the columns aligned to the same baseline by rendering each line
	// as its own row of two text cells.
	maxLines := len(addressLines)
	if len(clientLines) > maxLines {
		maxLines = len(clientLines)
	}
	for i := 0; i < maxLines; i++ {
		var left, right string
		if i < len(clientLines) {
			left = clientLines[i]
		}
		if i < len(addressLines) {
			right = addressLines[i]
		}
		m.AddRow(4,
			col.New(6).Add(text.New(left, props.Text{Size: 9})),
			col.New(6).Add(text.New(right, props.Text{Size: 9})),
		)
	}
	m.AddRows(row.New(4)) // spacer

	if err := generateAndSave(m, outPath); err != nil {
		return err
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/invoice/... -run TestRenderPDF_BilledTo_IssuedBy -v`
Expected: PASS.

- [ ] **Step 5: Re-run the previous PDF test to ensure no regression**

Run: `go test ./internal/invoice/... -run TestRenderPDF -v`
Expected: both header + billed-to tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/invoice/pdf.go internal/invoice/pdf_test.go
git commit -m "feat(invoice): PDF Billed-to / Issued-by block"
```

---

## Task 13: PDF renderer — line items table

**Files:**
- Modify: `internal/invoice/pdf.go`
- Modify: `internal/invoice/pdf_test.go`

- [ ] **Step 1: Add failing test**

Append to `internal/invoice/pdf_test.go`:

```go
func TestRenderPDF_LineItemsTable(t *testing.T) {
	doc := sampleDoc()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.pdf")
	if err := RenderPDF(doc, out); err != nil {
		t.Fatal(err)
	}
	text := extractPDFText(t, out)
	for _, w := range []string{
		"Product or service", "Qty", "Unit price", "Tax", "Total",
		"Software development",
		"1",                           // monthly_fixed quantity
		"3,000.00 EUR",                // unit + total
	} {
		if !strings.Contains(text, w) {
			t.Errorf("PDF text missing %q\n---\n%s\n---", w, text)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/invoice/... -run TestRenderPDF_LineItemsTable -v`
Expected: FAIL.

- [ ] **Step 3: Add line items table**

In `internal/invoice/pdf.go`, before `if err := generateAndSave(m, outPath)`, insert:

```go
	// Line items table — header row.
	m.AddRow(6,
		col.New(5).Add(text.New("Product or service", props.Text{Size: 9, Style: fontstyle.Bold})),
		col.New(1).Add(text.New("Qty", props.Text{Size: 9, Style: fontstyle.Bold, Align: align.Right})),
		col.New(2).Add(text.New("Unit price", props.Text{Size: 9, Style: fontstyle.Bold, Align: align.Right})),
		col.New(1).Add(text.New("Tax", props.Text{Size: 9, Style: fontstyle.Bold, Align: align.Right})),
		col.New(3).Add(text.New("Total", props.Text{Size: 9, Style: fontstyle.Bold, Align: align.Right})),
	)
	// Divider.
	m.AddRow(2, col.New(12).Add(
		text.New(strRepeat("_", 120), props.Text{Size: 6, Color: &props.GrayColor}),
	))

	// One row: the aggregated line item.
	qtyStr := FormatHours(doc.LineItem.QuantityHoursTimes100)
	unitStr := FormatMoney(doc.LineItem.UnitCents, doc.Currency)
	totalStr := FormatMoney(doc.LineItem.TotalCents, doc.Currency)
	periodStr := FormatDate(doc.PeriodFrom) + " – " + FormatDate(doc.PeriodTo.AddDate(0, 0, -1))

	m.AddRow(5,
		col.New(5).Add(text.New(doc.LineItemLabel, props.Text{Size: 9})),
		col.New(1).Add(text.New(qtyStr, props.Text{Size: 9, Align: align.Right})),
		col.New(2).Add(text.New(unitStr, props.Text{Size: 9, Align: align.Right})),
		col.New(1).Add(text.New("—", props.Text{Size: 9, Align: align.Right})),
		col.New(3).Add(text.New(totalStr, props.Text{Size: 9, Align: align.Right})),
	)
	m.AddRow(4,
		col.New(5).Add(text.New(periodStr, props.Text{Size: 7, Color: &props.GrayColor})),
		col.New(7),
	)
	m.AddRows(row.New(4)) // spacer
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/invoice/... -run TestRenderPDF -v`
Expected: all three PDF tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/invoice/pdf.go internal/invoice/pdf_test.go
git commit -m "feat(invoice): PDF line-items table"
```

---

## Task 14: PDF renderer — totals block + due date

**Files:**
- Modify: `internal/invoice/pdf.go`
- Modify: `internal/invoice/pdf_test.go`

- [ ] **Step 1: Add failing test**

Append to `internal/invoice/pdf_test.go`:

```go
func TestRenderPDF_TotalsBlock(t *testing.T) {
	doc := sampleDoc()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.pdf")
	if err := RenderPDF(doc, out); err != nil {
		t.Fatal(err)
	}
	text := extractPDFText(t, out)
	for _, w := range []string{
		"Total excluding tax", "Total tax", "0.00 EUR",
		"Amount Due", "3,000.00 EUR",
		"Due by", "May 27, 2026",
	} {
		if !strings.Contains(text, w) {
			t.Errorf("PDF text missing %q", w)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/invoice/... -run TestRenderPDF_TotalsBlock -v`
Expected: FAIL.

- [ ] **Step 3: Add totals block**

In `internal/invoice/pdf.go`, before `if err := generateAndSave(m, outPath)`, insert:

```go
	// Totals block — right-aligned, 4 lines.
	total := FormatMoney(doc.LineItem.TotalCents, doc.Currency)
	zero := FormatMoney(0, doc.Currency)
	totalLines := []struct {
		Label string
		Value string
		Bold  bool
	}{
		{"Total excluding tax", total, false},
		{"Total tax", zero, false},
		{"Amount Due", total, true},
		{"Due by", FormatDate(doc.DueDate), false},
	}
	for _, tl := range totalLines {
		style := fontstyle.Normal
		if tl.Bold {
			style = fontstyle.Bold
		}
		m.AddRow(4,
			col.New(6),
			col.New(3).Add(text.New(tl.Label, props.Text{Size: 9, Style: style, Align: align.Right})),
			col.New(3).Add(text.New(tl.Value, props.Text{Size: 9, Style: style, Align: align.Right})),
		)
	}
	m.AddRows(row.New(6)) // spacer
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/invoice/... -run TestRenderPDF -v`
Expected: all PDF tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/invoice/pdf.go internal/invoice/pdf_test.go
git commit -m "feat(invoice): PDF totals block + due date"
```

---

## Task 15: PDF renderer — bank details ("Ways to pay")

**Files:**
- Modify: `internal/invoice/pdf.go`
- Modify: `internal/invoice/pdf_test.go`

- [ ] **Step 1: Add failing test**

Append to `internal/invoice/pdf_test.go`:

```go
func TestRenderPDF_BankBlock(t *testing.T) {
	doc := sampleDoc()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.pdf")
	if err := RenderPDF(doc, out); err != nil {
		t.Fatal(err)
	}
	text := extractPDFText(t, out)
	for _, w := range []string{
		"Ways to pay", "Local bank details", "Wise EUR",
		"Reference", "INV-014",
		"Account holder", "BIC", "TRWIBEB1XXX",
		"IBAN", "BE16 9052 8808 7074",
	} {
		if !strings.Contains(text, w) {
			t.Errorf("PDF text missing %q", w)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/invoice/... -run TestRenderPDF_BankBlock -v`
Expected: FAIL.

- [ ] **Step 3: Add bank block to renderer**

In `internal/invoice/pdf.go`, before `if err := generateAndSave(m, outPath)`, insert:

```go
	// "Ways to pay" section.
	m.AddRow(5, col.New(12).Add(
		text.New("Ways to pay", props.Text{Size: 11, Style: fontstyle.Bold}),
	))
	m.AddRow(4, col.New(12).Add(
		text.New(doc.Bank.Title, props.Text{Size: 9, Style: fontstyle.Bold}),
	))
	if doc.Bank.Subtitle != "" {
		m.AddRow(4, col.New(12).Add(
			text.New(doc.Bank.Subtitle, props.Text{Size: 8, Color: &props.GrayColor}),
		))
	}
	m.AddRows(row.New(2)) // spacer

	// Reference is always the invoice number — auto-prepended.
	fields := append([]BankField{{Label: "Reference", Value: doc.Number}}, doc.Bank.Fields...)
	for _, f := range fields {
		// Value can be multi-line; render each line on its own row with the
		// label only on the first line.
		valueLines := splitLines(f.Value)
		for i, vl := range valueLines {
			label := ""
			if i == 0 {
				label = f.Label
			}
			m.AddRow(4,
				col.New(3).Add(text.New(label, props.Text{Size: 8, Color: &props.GrayColor})),
				col.New(9).Add(text.New(vl, props.Text{Size: 9})),
			)
		}
	}
```

Add helper at file end (still in pdf.go):

```go
func splitLines(s string) []string {
	out := []string{""}
	for _, r := range s {
		if r == '\n' {
			out = append(out, "")
			continue
		}
		out[len(out)-1] += string(r)
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/invoice/... -run TestRenderPDF -v`
Expected: all PDF tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/invoice/pdf.go internal/invoice/pdf_test.go
git commit -m "feat(invoice): PDF Ways-to-pay bank block"
```

---

## Task 16: Optional logo at top-left

**Files:**
- Modify: `internal/invoice/pdf.go`
- Modify: `internal/invoice/pdf_test.go`

- [ ] **Step 1: Add tests for both no-logo and with-logo paths**

Append to `internal/invoice/pdf_test.go`:

```go
func TestRenderPDF_NoLogo_RendersCleanly(t *testing.T) {
	doc := sampleDoc()
	doc.LogoPath = ""
	dir := t.TempDir()
	out := filepath.Join(dir, "out.pdf")
	if err := RenderPDF(doc, out); err != nil {
		t.Fatalf("RenderPDF without logo: %v", err)
	}
	info, _ := os.Stat(out)
	if info == nil || info.Size() == 0 {
		t.Error("output missing")
	}
}

func TestRenderPDF_LogoMissingFile_IsIgnored(t *testing.T) {
	doc := sampleDoc()
	doc.LogoPath = "/does/not/exist.png"
	dir := t.TempDir()
	out := filepath.Join(dir, "out.pdf")
	// Renderer must NOT fail when the logo file is missing — it should
	// silently render without one. This is the spec's "missing-file → skip"
	// rule for logo_path.
	if err := RenderPDF(doc, out); err != nil {
		t.Fatalf("RenderPDF with missing logo: %v", err)
	}
}
```

- [ ] **Step 2: Run tests — TestRenderPDF_LogoMissingFile_IsIgnored may already pass, but the with-logo path needs to exist**

Run: `go test ./internal/invoice/... -run TestRenderPDF -v`
Expected: existing tests still PASS; new ones PASS too (since we don't yet add logo logic, both paths just render without).

- [ ] **Step 3: Add logo support to RenderPDF**

In `internal/invoice/pdf.go`, in `RenderPDF`, replace the existing header row with:

```go
	// Header: optional logo + "Invoice" big, with number + issue date on the right.
	logoCol := col.New(1)
	titleCol := col.New(5)
	if doc.LogoPath != "" {
		if _, err := osStat(doc.LogoPath); err == nil {
			logoCol = col.New(1).Add(
				image.NewFromFile(doc.LogoPath, props.Rect{Center: true, Percent: 100}),
			)
		}
	}
	titleCol = titleCol.Add(text.New("Invoice", props.Text{Size: 24, Style: fontstyle.Bold}))
	m.AddRow(20,
		logoCol,
		titleCol,
		col.New(3).Add(
			text.New("Invoice number", props.Text{Size: 8, Color: &props.GrayColor}),
			text.New(doc.Number, props.Text{Size: 10, Top: 4}),
		),
		col.New(3).Add(
			text.New("Issue date", props.Text{Size: 8, Color: &props.GrayColor}),
			text.New(FormatDate(doc.IssueDate), props.Text{Size: 10, Top: 4}),
		),
	)
```

Add the image import and the `osStat` indirection (for testability) at the top of the file:

```go
import (
	// ... existing imports ...
	"os"

	"github.com/johnfercher/maroto/v2/pkg/components/image"
)

var osStat = os.Stat
```

- [ ] **Step 4: Run tests to verify everything still passes**

Run: `go test ./internal/invoice/... -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/invoice/pdf.go internal/invoice/pdf_test.go
git commit -m "feat(invoice): optional logo in PDF header"
```

---

## Task 17: Orchestration — invoice.go

**Files:**
- Create: `internal/invoice/invoice.go`
- Create: `internal/invoice/invoice_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/invoice/invoice_test.go`:

```go
package invoice

import (
	"testing"
	"time"
)

func TestBuildDoc_MonthlyFixed(t *testing.T) {
	sender := Sender{
		LegalName:    "JHIONAN RIAN LARA DOS SANTOS",
		TaxID:        "34.012.215/0001-44",
		TaxIDLabel:   "CNPJ",
		AddressLines: []string{"Mateus Leme 2830", "Brazil"},
		BankAccounts: map[string]Bank{
			"EUR": {Title: "Wise EUR", Fields: []BankField{{Label: "IBAN", Value: "BE16"}}},
		},
	}
	in := BuildDocInput{
		Now:            time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC),
		ClientName:     "Dentix",
		BillingMode:    "monthly_fixed",
		Currency:       "EUR",
		RateCents:      300000,
		Sender:         sender,
		InvoiceNumber:  "INV-014",
		PeriodFrom:     time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		PeriodTo:       time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		DueDays:        0,
		LineItemLabel:  "Software development",
		Ticks:          0,
		TickSec:        5,
	}
	doc, err := BuildDoc(in)
	if err != nil {
		t.Fatal(err)
	}
	if doc.LineItem.TotalCents != 300000 {
		t.Errorf("total = %d, want 300000", doc.LineItem.TotalCents)
	}
	if doc.Number != "INV-014" {
		t.Errorf("number = %q", doc.Number)
	}
	if doc.Bank.Fields[0].Value != "BE16" {
		t.Errorf("bank not chosen correctly: %+v", doc.Bank)
	}
}

func TestBuildDoc_HourlyWithCAD_via_AlsoAccepts(t *testing.T) {
	sender := Sender{
		LegalName: "Y",
		BankAccounts: map[string]Bank{
			"EUR": {AlsoAccepts: []string{"CAD"}, Title: "ES EUR", Fields: []BankField{{Label: "IBAN", Value: "ES51"}}},
		},
	}
	in := BuildDocInput{
		Now:            time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC),
		ClientName:     "BClouder",
		BillingMode:    "hourly",
		Currency:       "CAD",
		RateCents:      5000,
		Sender:         sender,
		InvoiceNumber:  "ES-0001",
		PeriodFrom:     time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		PeriodTo:       time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC),
		DueDays:        7,
		LineItemLabel:  "Software development",
		Ticks:          34200,
		TickSec:        5,
	}
	doc, err := BuildDoc(in)
	if err != nil {
		t.Fatal(err)
	}
	if doc.LineItem.TotalCents != 237500 {
		t.Errorf("total = %d, want 237500", doc.LineItem.TotalCents)
	}
	if doc.Bank.Title != "ES EUR" {
		t.Errorf("bank lookup via also_accepts failed: %+v", doc.Bank)
	}
	wantDue := in.Now.AddDate(0, 0, 7)
	if !doc.DueDate.Equal(wantDue) {
		t.Errorf("DueDate = %v, want %v", doc.DueDate, wantDue)
	}
}

func TestBuildDoc_RejectsMissingBankBlock(t *testing.T) {
	sender := Sender{LegalName: "X", BankAccounts: map[string]Bank{}}
	in := BuildDocInput{
		Now: time.Now(), ClientName: "x", BillingMode: "hourly",
		Currency: "USD", RateCents: 1, Sender: sender, InvoiceNumber: "X-1",
		PeriodFrom: time.Now(), PeriodTo: time.Now(),
		LineItemLabel: "X", Ticks: 720, TickSec: 5,
	}
	if _, err := BuildDoc(in); err == nil {
		t.Error("expected error for missing bank block, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/invoice/... -run TestBuildDoc -v`
Expected: FAIL — `BuildDoc` undefined.

- [ ] **Step 3: Implement invoice.go**

Create `internal/invoice/invoice.go`:

```go
package invoice

import (
	"fmt"
	"time"
)

// BuildDocInput is the fully-resolved set of inputs needed to build an
// InvoiceDoc. Caller (daemon-side orchestration) has already done DB lookups
// (last invoice, tick count, allocated number) and config parsing.
type BuildDocInput struct {
	Now            time.Time
	ClientName     string
	BillingMode    string // "hourly" | "monthly_fixed"
	Currency       string
	RateCents      int64
	Sender         Sender
	InvoiceNumber  string
	PeriodFrom     time.Time
	PeriodTo       time.Time
	DueDays        int
	LineItemLabel  string
	Ticks          int64
	TickSec        int
}

// BuildDoc gathers an InvoiceDoc from the resolved inputs. Pure: no IO, no
// DB. Returns an error for the only thing it can detect: no bank account
// for the target currency.
func BuildDoc(in BuildDocInput) (InvoiceDoc, error) {
	bank, ok := in.Sender.BankFor(in.Currency)
	if !ok {
		return InvoiceDoc{}, fmt.Errorf("sender has no bank account for currency %q (and no also_accepts fallback)", in.Currency)
	}
	li := ComputeLineItem(in.BillingMode, in.Ticks, in.TickSec, in.RateCents)
	due := in.Now.AddDate(0, 0, in.DueDays)
	return InvoiceDoc{
		Number:        in.InvoiceNumber,
		IssueDate:     in.Now,
		DueDate:       due,
		PeriodFrom:    in.PeriodFrom,
		PeriodTo:      in.PeriodTo,
		Currency:      in.Currency,
		ClientName:    in.ClientName,
		Sender:        in.Sender,
		LineItemLabel: in.LineItemLabel,
		LineItem:      li,
		Bank:          bank,
		LogoPath:      in.Sender.LogoPath,
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/invoice/... -run TestBuildDoc -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/invoice/invoice.go internal/invoice/invoice_test.go
git commit -m "feat(invoice): BuildDoc orchestration (pure)"
```

---

## Task 18: RPC types for InvoiceGenerate

**Files:**
- Modify: `internal/rpcapi/api.go`

- [ ] **Step 1: Add types**

In `internal/rpcapi/api.go`, in the `--- Invoices ---` section, append:

```go
type InvoiceGenerateArgs struct {
	CompanyName  string
	FromUnix     int64  // 0 = use default period for the company's billing_mode
	ToUnix       int64  // 0 = use default
	IssueDateUnix int64 // 0 = now
	Note         string
	DryRun       bool
	AllowEmpty   bool
}

type InvoiceGenerateReply struct {
	InvoiceID    int64  // 0 when DryRun=true
	Number       string
	PDFPath      string
	TotalCents   int64
	Currency     string
	SenderKey    string
	IssueDateUnix int64
	DueDateUnix  int64
}
```

- [ ] **Step 2: Verify compile**

Run: `go build ./...`
Expected: exits 0.

- [ ] **Step 3: Commit**

```bash
git add internal/rpcapi/api.go
git commit -m "feat(rpcapi): InvoiceGenerate args/reply"
```

---

## Task 19: InvoiceGenerate RPC handler

**Files:**
- Create: `internal/daemon/rpc_invoice.go`
- Create: `internal/daemon/rpc_invoice_test.go`

- [ ] **Step 1: Write the failing integration test**

Create `internal/daemon/rpc_invoice_test.go`:

```go
package daemon

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ledongthuc/pdf"

	"github.com/rian/antitimely/internal/rpcapi"
	"github.com/rian/antitimely/internal/store"
)

const senderYAMLForRPC = `
senders:
  br:
    legal_name: "JHIONAN RIAN LARA DOS SANTOS"
    tax_id: "34.012.215/0001-44"
    tax_id_label: "CNPJ"
    address_lines: ["Mateus Leme 2830", "Brazil"]
    invoice:
      number_prefix: "INV-"
      number_pad: 3
      next_number: 14
    bank_accounts:
      EUR:
        title: "Wise EUR"
        fields:
          - { label: "IBAN", value: "BE16" }

invoice:
  output_dir: "%s"
  line_item_label: "Software development"
  due_days: 0
`

func TestRPC_InvoiceGenerate_MonthlyFixedHappyPath(t *testing.T) {
	client, db, _ := setupRPCServer(t)

	// Seed: create company Dentix as monthly_fixed billed_from=br.
	var add rpcapi.CompanyAddReply
	if err := client.Call(rpcapi.ServiceName+".CompanyAdd",
		rpcapi.CompanyAddArgs{Name: "Dentix"}, &add); err != nil {
		t.Fatal(err)
	}
	// Direct SQL update for billing config (no setup command yet).
	q := store.New(db)
	if err := q.SetCompanyBilling(t.Context(), store.SetCompanyBillingParams{
		BillingMode: "monthly_fixed",
		Currency:    sql.NullString{String: "EUR", Valid: true},
		RateCents:   sql.NullInt64{Int64: 300000, Valid: true},
		BilledFrom:  sql.NullString{String: "br", Valid: true},
		Name:        "Dentix",
	}); err != nil {
		t.Fatal(err)
	}

	// Write the sender config file.
	outDir := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	body := strings.Replace(senderYAMLForRPC, "%s", outDir, 1)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTITIMELY_CONFIG", cfgPath)

	// Pin the daemon's clock — handler uses time.Now(), so use a real one
	// close to the test's expectations.
	_ = time.Now

	var reply rpcapi.InvoiceGenerateReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceGenerate",
		rpcapi.InvoiceGenerateArgs{CompanyName: "Dentix"}, &reply); err != nil {
		t.Fatalf("InvoiceGenerate: %v", err)
	}

	// Reply assertions.
	if reply.InvoiceID == 0 {
		t.Error("InvoiceID should be set")
	}
	if reply.Number != "INV-014" {
		t.Errorf("Number = %q, want INV-014", reply.Number)
	}
	if reply.TotalCents != 300000 {
		t.Errorf("TotalCents = %d", reply.TotalCents)
	}
	if reply.Currency != "EUR" {
		t.Errorf("Currency = %q", reply.Currency)
	}
	if reply.SenderKey != "br" {
		t.Errorf("SenderKey = %q", reply.SenderKey)
	}

	// PDF assertions.
	info, err := os.Stat(reply.PDFPath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("PDF missing or empty at %s: %v", reply.PDFPath, err)
	}
	// Extract text and check key strings.
	f, r, err := pdf.Open(reply.PDFPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var sb strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		rows, _ := p.GetTextByRow()
		for _, row := range rows {
			for _, w := range row.Content {
				sb.WriteString(w.S)
				sb.WriteByte(' ')
			}
		}
	}
	text := sb.String()
	for _, want := range []string{"INV-014", "Dentix", "3,000.00 EUR", "JHIONAN RIAN LARA DOS SANTOS"} {
		if !strings.Contains(text, want) {
			t.Errorf("PDF missing %q\n---\n%s\n---", want, text)
		}
	}

	// sender_state advanced to 15.
	row := db.QueryRow("SELECT next_invoice_number FROM sender_state WHERE sender_key='br'")
	var n int64
	if err := row.Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 15 {
		t.Errorf("sender_state.next_invoice_number = %d, want 15", n)
	}

	// invoices row inserted with metadata.
	row = db.QueryRow("SELECT number, total_cents, currency, sender_key FROM invoices WHERE id=?", reply.InvoiceID)
	var (
		num, curr, sk string
		tot           int64
	)
	if err := row.Scan(&num, &tot, &curr, &sk); err != nil {
		t.Fatal(err)
	}
	if num != "INV-014" || tot != 300000 || curr != "EUR" || sk != "br" {
		t.Errorf("invoices row mismatch: num=%q tot=%d curr=%q sk=%q", num, tot, curr, sk)
	}
}

func TestRPC_InvoiceGenerate_RejectsNonBillable(t *testing.T) {
	client, _, _ := setupRPCServer(t)
	if err := client.Call(rpcapi.ServiceName+".CompanyAdd",
		rpcapi.CompanyAddArgs{Name: "Foca.app"}, &rpcapi.CompanyAddReply{}); err != nil {
		t.Fatal(err)
	}
	// Not configured for billing — defaults to mode='none'.
	var reply rpcapi.InvoiceGenerateReply
	err := client.Call(rpcapi.ServiceName+".InvoiceGenerate",
		rpcapi.InvoiceGenerateArgs{CompanyName: "Foca.app"}, &reply)
	if err == nil {
		t.Error("expected error for non-billable company")
	}
}

func TestRPC_InvoiceGenerate_DryRun(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	if err := client.Call(rpcapi.ServiceName+".CompanyAdd",
		rpcapi.CompanyAddArgs{Name: "Dentix"}, &rpcapi.CompanyAddReply{}); err != nil {
		t.Fatal(err)
	}
	q := store.New(db)
	if err := q.SetCompanyBilling(t.Context(), store.SetCompanyBillingParams{
		BillingMode: "monthly_fixed",
		Currency:    sql.NullString{String: "EUR", Valid: true},
		RateCents:   sql.NullInt64{Int64: 300000, Valid: true},
		BilledFrom:  sql.NullString{String: "br", Valid: true},
		Name:        "Dentix",
	}); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	body := strings.Replace(senderYAMLForRPC, "%s", outDir, 1)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTITIMELY_CONFIG", cfgPath)

	var reply rpcapi.InvoiceGenerateReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceGenerate",
		rpcapi.InvoiceGenerateArgs{CompanyName: "Dentix", DryRun: true}, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.InvoiceID != 0 {
		t.Error("InvoiceID should be 0 on dry-run")
	}
	// No row inserted.
	var n int64
	if err := db.QueryRow("SELECT COUNT(*) FROM invoices").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("invoices rows = %d on dry-run, want 0", n)
	}
	// No output_dir file persisted.
	entries, _ := os.ReadDir(outDir)
	for _, e := range entries {
		if !e.IsDir() {
			t.Errorf("dry-run left a file in output_dir: %s", e.Name())
		}
	}
	// PDF should still exist at a temp path so the user could review.
	if _, err := os.Stat(reply.PDFPath); err != nil {
		t.Errorf("dry-run PDF missing at %s", reply.PDFPath)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/daemon/... -run TestRPC_InvoiceGenerate -v`
Expected: FAIL — `InvoiceGenerate` RPC not registered.

- [ ] **Step 3: Implement the handler**

Create `internal/daemon/rpc_invoice.go`:

```go
package daemon

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rian/antitimely/internal/invoice"
	"github.com/rian/antitimely/internal/rpcapi"
	"github.com/rian/antitimely/internal/store"
)

// configPath returns the senders/invoice config path. Honors ANTITIMELY_CONFIG
// for tests; otherwise defaults to ~/.antitimely/config.yaml.
func configPath() (string, error) {
	if p := os.Getenv("ANTITIMELY_CONFIG"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".antitimely", "config.yaml"), nil
}

// InvoiceGenerate implements the full generation flow per the design spec.
func (s *AntitimelyService) InvoiceGenerate(args rpcapi.InvoiceGenerateArgs, reply *rpcapi.InvoiceGenerateReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()

	// 1. Resolve company.
	co, err := s.Q.GetCompanyForInvoice(ctx, args.CompanyName)
	if err != nil {
		return fmt.Errorf("company %q not found: %w", args.CompanyName, err)
	}
	if co.BillingMode == "none" {
		return fmt.Errorf("company %q is not billable (billing_mode='none')", co.Name)
	}
	if !co.BilledFrom.Valid || co.BilledFrom.String == "" {
		return fmt.Errorf("company %q has no billed_from sender", co.Name)
	}

	// 2. Load fresh config.
	cfgPath, err := configPath()
	if err != nil {
		return err
	}
	cfg, err := invoice.LoadSendersConfig(cfgPath)
	if err != nil {
		return err
	}
	if issues := cfg.Validate(); len(issues) > 0 {
		return fmt.Errorf("invalid senders config: %v", issues)
	}
	senderKey := co.BilledFrom.String
	sender, ok := cfg.Senders[senderKey]
	if !ok {
		return fmt.Errorf("sender %q not in config (run `atl invoice show-senders`)", senderKey)
	}

	// 3. Resolve period.
	var now time.Time
	if args.IssueDateUnix > 0 {
		now = time.Unix(args.IssueDateUnix, 0).Local()
	} else {
		now = time.Now()
	}
	lastSent, err := s.Q.LastInvoiceSentForCompany(ctx, co.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		lastSent = 0
	case err != nil:
		return err
	}
	from, to := invoice.DefaultPeriod(co.BillingMode, now, lastSent)
	if args.FromUnix > 0 {
		from = time.Unix(args.FromUnix, 0).Local()
	}
	if args.ToUnix > 0 {
		to = time.Unix(args.ToUnix, 0).Local()
	}
	if from.IsZero() && co.BillingMode == "hourly" {
		// No prior invoice + no --from override → use company.created_at.
		from = time.Unix(co.CreatedAt, 0).Local()
	}

	// 4. Tick count.
	var ticks int64
	if co.BillingMode == "hourly" {
		t, err := s.Q.CountTicksForCompanyInRange(ctx, store.CountTicksForCompanyInRangeParams{
			CompanyID: sql.NullInt64{Int64: co.ID, Valid: true},
			Ts:        from.Unix(),
			Ts_2:      to.Unix(),
		})
		if err != nil {
			return err
		}
		ticks = t
		if ticks == 0 && !args.AllowEmpty {
			return fmt.Errorf("no time tracked for %q in %s..%s; pass --allow-empty to override",
				co.Name, from.Format("2006-01-02"), to.Format("2006-01-02"))
		}
	}

	// 5. Allocate invoice number atomically (skipped on dry-run; we synthesize a preview).
	if !args.DryRun {
		// Seed sender_state if missing.
		if err := s.Q.SeedSenderState(ctx, store.SeedSenderStateParams{
			SenderKey:         senderKey,
			NextInvoiceNumber: sender.Invoice.NextNumber,
		}); err != nil {
			return err
		}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	qtx := s.Q.WithTx(tx)

	var allocatedNumber int64
	if args.DryRun {
		// Preview: read current cursor without incrementing. If sender_state has
		// no row, fall back to config's seed.
		// Best-effort: SELECT then revert if INSERT happens (we don't INSERT).
		row := tx.QueryRowContext(ctx, "SELECT next_invoice_number FROM sender_state WHERE sender_key = ?", senderKey)
		if err := row.Scan(&allocatedNumber); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				allocatedNumber = sender.Invoice.NextNumber
			} else {
				return err
			}
		}
	} else {
		a, err := qtx.AllocateNextInvoiceNumber(ctx, senderKey)
		if err != nil {
			return fmt.Errorf("allocate invoice number: %w", err)
		}
		allocatedNumber = a
	}

	number := invoice.FormatInvoiceNumber(sender.Invoice.NumberPrefix, sender.Invoice.NumberPad, allocatedNumber)

	// 6. Build the doc.
	doc, err := invoice.BuildDoc(invoice.BuildDocInput{
		Now:           now,
		ClientName:    co.Name,
		BillingMode:   co.BillingMode,
		Currency:      co.Currency.String,
		RateCents:     co.RateCents.Int64,
		Sender:        sender,
		InvoiceNumber: number,
		PeriodFrom:    from,
		PeriodTo:      to,
		DueDays:       cfg.Invoice.DueDays,
		LineItemLabel: cfg.Invoice.LineItemLabel,
		Ticks:         ticks,
		TickSec:       s.TickIntervalSeconds,
	})
	if err != nil {
		return err
	}

	// 7. Render PDF.
	var pdfPath string
	if args.DryRun {
		f, ferr := os.CreateTemp("", "atl-dryrun-*.pdf")
		if ferr != nil {
			return ferr
		}
		f.Close()
		pdfPath = f.Name()
	} else {
		outDir := expandHome(cfg.Invoice.OutputDir)
		senderDir := filepath.Join(outDir, senderKey)
		if err := os.MkdirAll(senderDir, 0o700); err != nil {
			return err
		}
		pdfPath = filepath.Join(senderDir, number+".pdf")
	}
	if err := invoice.RenderPDF(doc, pdfPath); err != nil {
		_ = os.Remove(pdfPath)
		return err
	}

	// 8. Insert invoices row (skipped on dry-run).
	var invoiceID int64
	if !args.DryRun {
		id, err := qtx.InsertInvoiceFull(ctx, store.InsertInvoiceFullParams{
			CompanyID:  co.ID,
			SentAt:     now.Unix(),
			Note:       args.Note,
			CreatedAt:  time.Now().Unix(),
			Number:     sql.NullString{String: number, Valid: true},
			PdfPath:    sql.NullString{String: pdfPath, Valid: true},
			TotalCents: sql.NullInt64{Int64: doc.LineItem.TotalCents, Valid: true},
			Currency:   sql.NullString{String: co.Currency.String, Valid: true},
			SenderKey:  sql.NullString{String: senderKey, Valid: true},
		})
		if err != nil {
			_ = os.Remove(pdfPath)
			return err
		}
		invoiceID = id
	}

	if err := tx.Commit(); err != nil {
		_ = os.Remove(pdfPath)
		return fmt.Errorf("commit: %w", err)
	}
	committed = true

	reply.InvoiceID = invoiceID
	reply.Number = number
	reply.PDFPath = pdfPath
	reply.TotalCents = doc.LineItem.TotalCents
	reply.Currency = doc.Currency
	reply.SenderKey = senderKey
	reply.IssueDateUnix = doc.IssueDate.Unix()
	reply.DueDateUnix = doc.DueDate.Unix()
	return nil
}

// expandHome replaces a leading "~/" with the user's home dir.
func expandHome(p string) string {
	if len(p) >= 2 && p[:2] == "~/" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/... -run TestRPC_InvoiceGenerate -v`
Expected: PASS — all three sub-tests.

If `t.Context()` is not available (Go < 1.24), replace with `context.Background()` and add the import.

- [ ] **Step 5: Run the full daemon test suite to verify no regressions**

Run: `go test ./internal/daemon/... -v`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/rpc_invoice.go internal/daemon/rpc_invoice_test.go
git commit -m "feat(daemon): InvoiceGenerate RPC handler with atomic generation"
```

---

## Task 20: CLI — `atl invoice generate` and `atl invoice show-senders`

**Files:**
- Modify: `internal/cli/invoice.go`
- Modify: `internal/cli/dispatch.go`

- [ ] **Step 1: Add `generate` and `show-senders` subcommands**

In `internal/cli/invoice.go`, in the `cmdInvoice` switch statement, add cases:

```go
	case "generate":
		return invoiceGenerate(args[1:])
	case "show-senders":
		return invoiceShowSenders(args[1:])
```

Update the usage line at the top:

```go
fmt.Fprintln(os.Stderr, "usage: antitimely invoice <send|list|delete|generate|show-senders> ...")
```

Append the implementations to the same file:

```go
func invoiceGenerate(args []string) int {
	fs := flag.NewFlagSet("invoice generate", flag.ExitOnError)
	from := fs.String("from", "", "Period start: YYYY-MM-DD (defaults per billing mode)")
	to := fs.String("to", "", "Period end: YYYY-MM-DD (defaults per billing mode)")
	issueDate := fs.String("issue-date", "", "Date on the invoice: YYYY-MM-DD (default: today)")
	note := fs.String("note", "", "Stored in invoices.note (not printed on PDF)")
	dryRun := fs.Bool("dry-run", false, "Render PDF to a temp file without DB writes")
	allowEmpty := fs.Bool("allow-empty", false, "For hourly mode, allow 0-tick periods")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: antitimely invoice generate <company> [--from=YYYY-MM-DD] [--to=YYYY-MM-DD] [--issue-date=YYYY-MM-DD] [--note=...] [--dry-run] [--allow-empty]")
		return 64
	}
	company := fs.Arg(0)
	fromUnix, err := parseOptionalDate(*from)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid --from:", err)
		return 64
	}
	toUnix, err := parseOptionalDate(*to)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid --to:", err)
		return 64
	}
	issueUnix, err := parseOptionalDate(*issueDate)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid --issue-date:", err)
		return 64
	}

	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	var reply rpcapi.InvoiceGenerateReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceGenerate", rpcapi.InvoiceGenerateArgs{
		CompanyName:   company,
		FromUnix:      fromUnix,
		ToUnix:        toUnix,
		IssueDateUnix: issueUnix,
		Note:          *note,
		DryRun:        *dryRun,
		AllowEmpty:    *allowEmpty,
	}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	tag := ""
	if *dryRun {
		tag = " (dry-run)"
	}
	fmt.Printf("Generated %s%s — %s\n", reply.Number, tag, reply.PDFPath)
	// Open in viewer (best-effort).
	if err := exec.Command("open", reply.PDFPath).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "(warning: could not open viewer:", err, ")")
	}
	return 0
}

func invoiceShowSenders(args []string) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cfgPath := filepath.Join(home, ".antitimely", "config.yaml")
	if env := os.Getenv("ANTITIMELY_CONFIG"); env != "" {
		cfgPath = env
	}
	cfg, err := invoice.LoadSendersConfig(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	issues := cfg.Validate()
	fmt.Printf("Config: %s\n\n", cfgPath)
	for key, s := range cfg.Senders {
		fmt.Printf("[%s] %s\n  %s %s\n  %s\n  Invoice cursor (config seed): %s%0*d\n",
			key, s.LegalName, s.TaxIDLabel, s.TaxID,
			strings.Join(s.AddressLines, ", "),
			s.Invoice.NumberPrefix, s.Invoice.NumberPad, s.Invoice.NextNumber)
		for ccy, bk := range s.BankAccounts {
			extra := ""
			if len(bk.AlsoAccepts) > 0 {
				extra = " (also accepts: " + strings.Join(bk.AlsoAccepts, ", ") + ")"
			}
			fmt.Printf("  Bank for %s%s: %d field(s)\n", ccy, extra, len(bk.Fields))
		}
		fmt.Println()
	}
	if len(issues) > 0 {
		fmt.Println("Issues:")
		for _, iss := range issues {
			fmt.Println("  -", iss)
		}
		return 1
	}
	fmt.Println("Config OK.")
	return 0
}

func parseOptionalDate(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return 0, err
	}
	return t.Unix(), nil
}
```

Add the necessary imports at the top:

```go
import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rian/antitimely/internal/invoice"
	"github.com/rian/antitimely/internal/rpcapi"
)
```

(`strconv` may already be present; keep one copy.)

- [ ] **Step 2: Update dispatch usage line**

In `internal/cli/dispatch.go`, find the line listing invoice subcommands and replace with:

```
  antitimely invoice  generate <company> [--from=YYYY-MM-DD] [--to=YYYY-MM-DD] [--issue-date=YYYY-MM-DD] [--note=...] [--dry-run] [--allow-empty]
                      send [--at=YYYY-MM-DD] [--note=...] <company>   (anchor only, no PDF — kept for back-compat)
                      list [<company>]  |  delete <id>  |  show-senders
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: exits 0.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/invoice.go internal/cli/dispatch.go
git commit -m "feat(cli): invoice generate + show-senders subcommands"
```

---

## Task 21: CLI — `atl invoice setup` (one-shot data migration)

**Files:**
- Modify: `internal/cli/invoice.go`
- Modify: `internal/rpcapi/api.go`
- Modify: `internal/daemon/rpc.go` (add ProjectMove RPC for moving Dentix)
- Modify: `queries.sql` + regenerate

- [ ] **Step 1: Add SetProjectCompany + idempotent helper queries**

The existing `ProjectSetCompany` RPC already moves a project between companies — we can reuse it. But we also need:
- `SetCompanyBilling` (already added in Task 3 ✓)
- A way to "ensure company exists" idempotently

Add to `queries.sql`:

```sql
-- name: EnsureCompany :one
INSERT INTO companies (name, created_at)
VALUES (?, ?)
ON CONFLICT(name) DO UPDATE SET name = name
RETURNING id;
```

Regenerate sqlc: `sqlc generate`.

- [ ] **Step 2: Add ProjectMoveOrCreate logic (via existing RPCs) and SetCompanyBilling RPC**

In `internal/rpcapi/api.go`, append:

```go
type SetCompanyBillingArgs struct {
	Name        string
	BillingMode string
	Currency    string
	RateCents   int64
	BilledFrom  string
}
type SetCompanyBillingReply struct{}
```

In `internal/daemon/rpc.go`, append:

```go
func (s *AntitimelyService) SetCompanyBilling(args rpcapi.SetCompanyBillingArgs, reply *rpcapi.SetCompanyBillingReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	return s.Q.SetCompanyBilling(ctx, store.SetCompanyBillingParams{
		Name:        args.Name,
		BillingMode: args.BillingMode,
		Currency:    nullStr(args.Currency),
		RateCents:   nullInt(args.RateCents),
		BilledFrom:  nullStr(args.BilledFrom),
	})
}

func nullInt(n int64) sql.NullInt64 {
	if n == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: n, Valid: true}
}
```

- [ ] **Step 3: Implement `atl invoice setup`**

In `internal/cli/invoice.go`:

Add case to `cmdInvoice`:
```go
	case "setup":
		return invoiceSetup(args[1:])
```

Append the function:

```go
func invoiceSetup(args []string) int {
	_ = args
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()

	// Step 1: Ensure Dentix company exists.
	if err := client.Call(rpcapi.ServiceName+".CompanyAdd",
		rpcapi.CompanyAddArgs{Name: "Dentix"}, &rpcapi.CompanyAddReply{}); err != nil {
		// Ignore "UNIQUE constraint" errors — company already exists.
		if !strings.Contains(err.Error(), "UNIQUE") && !strings.Contains(err.Error(), "already exists") {
			fmt.Fprintln(os.Stderr, "CompanyAdd Dentix:", err)
			return 1
		}
	}

	// Step 2: Move project Dentix → company Dentix.
	if err := client.Call(rpcapi.ServiceName+".ProjectSetCompany",
		rpcapi.ProjectSetCompanyArgs{ProjectName: "Dentix", CompanyName: "Dentix"},
		&rpcapi.ProjectSetCompanyReply{}); err != nil {
		fmt.Fprintln(os.Stderr, "ProjectSetCompany Dentix → Dentix:", err)
		return 1
	}

	// Step 3: Configure billing for BClouder and Dentix.
	plans := []rpcapi.SetCompanyBillingArgs{
		{Name: "BClouder", BillingMode: "hourly", Currency: "CAD", RateCents: 5000, BilledFrom: "es"},
		{Name: "Dentix", BillingMode: "monthly_fixed", Currency: "EUR", RateCents: 300000, BilledFrom: "br"},
	}
	for _, p := range plans {
		if err := client.Call(rpcapi.ServiceName+".SetCompanyBilling",
			p, &rpcapi.SetCompanyBillingReply{}); err != nil {
			fmt.Fprintf(os.Stderr, "SetCompanyBilling %s: %v\n", p.Name, err)
			return 1
		}
		fmt.Printf("  set %s → mode=%s currency=%s rate=%d billed_from=%s\n",
			p.Name, p.BillingMode, p.Currency, p.RateCents, p.BilledFrom)
	}

	fmt.Println("\nSetup complete. Next steps:")
	fmt.Println("  1. Edit ~/.antitimely/config.yaml to add `senders:` block (see docs/superpowers/specs/2026-05-27-invoice-pdf-design.md)")
	fmt.Println("  2. Run `atl invoice show-senders` to validate.")
	fmt.Println("  3. Run `atl invoice generate <company>` to produce your first PDF.")
	return 0
}
```

- [ ] **Step 4: Verify build**

Run: `go build ./...`
Expected: exits 0.

- [ ] **Step 5: Add a unit test for SetCompanyBilling RPC**

Create `internal/daemon/rpc_setbilling_test.go`:

```go
package daemon

import (
	"testing"

	"github.com/rian/antitimely/internal/rpcapi"
)

func TestRPC_SetCompanyBilling(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	if err := client.Call(rpcapi.ServiceName+".CompanyAdd",
		rpcapi.CompanyAddArgs{Name: "BClouder"}, &rpcapi.CompanyAddReply{}); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(rpcapi.ServiceName+".SetCompanyBilling",
		rpcapi.SetCompanyBillingArgs{
			Name: "BClouder", BillingMode: "hourly",
			Currency: "CAD", RateCents: 5000, BilledFrom: "es",
		}, &rpcapi.SetCompanyBillingReply{}); err != nil {
		t.Fatal(err)
	}
	row := db.QueryRow("SELECT billing_mode, currency, rate_cents, billed_from FROM companies WHERE name='BClouder'")
	var bm, curr, bf string
	var rate int64
	if err := row.Scan(&bm, &curr, &rate, &bf); err != nil {
		t.Fatal(err)
	}
	if bm != "hourly" || curr != "CAD" || rate != 5000 || bf != "es" {
		t.Errorf("got %s/%s/%d/%s", bm, curr, rate, bf)
	}
}
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/daemon/... -run TestRPC_SetCompanyBilling -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/invoice.go internal/rpcapi/api.go internal/daemon/rpc.go internal/daemon/rpc_setbilling_test.go queries.sql internal/store/queries.sql.go
git commit -m "feat(cli): atl invoice setup + SetCompanyBilling RPC"
```

---

## Task 22: End-to-end smoke test against the live daemon

**Files:**
- Modify: existing — no new files

- [ ] **Step 1: Rebuild and restart the daemon**

Run: `make rebuild`
Expected: daemon restarted with new binary; status check passes.

- [ ] **Step 2: Run setup**

Run: `./antitimely invoice setup`
Expected: prints "set BClouder → ..." and "set Dentix → ...", followed by next-steps message.

- [ ] **Step 3: Add the senders block to your real config**

Edit `~/.antitimely/config.yaml` and paste the full senders+invoice block from the spec (`docs/superpowers/specs/2026-05-27-invoice-pdf-design.md` section "Config file additions"). Save.

- [ ] **Step 4: Validate config**

Run: `./antitimely invoice show-senders`
Expected: prints both `br` and `es` senders with their bank accounts; final line says `Config OK.`.

- [ ] **Step 5: Generate a dry-run invoice for Dentix**

Run: `./antitimely invoice generate Dentix --dry-run`
Expected: prints `Generated INV-014 (dry-run) — /tmp/atl-dryrun-*.pdf`, opens the PDF in Preview. The PDF shows the Dentix monthly invoice with the BR sender's address and Wise bank details.

- [ ] **Step 6: Generate a real invoice for Dentix**

Run: `./antitimely invoice generate Dentix`
Expected: prints `Generated INV-014 — ~/.antitimely/invoices/br/INV-014.pdf`, opens it. The DB has a row in `invoices` with number=INV-014, and `sender_state.next_invoice_number` for `br` is 15.

- [ ] **Step 7: Verify DB state**

Run:
```bash
sqlite3 ~/.antitimely/db.sqlite "SELECT number, total_cents, currency, sender_key FROM invoices WHERE number='INV-014';"
sqlite3 ~/.antitimely/db.sqlite "SELECT sender_key, next_invoice_number FROM sender_state;"
```
Expected: invoice row exists with correct fields; sender_state shows `br=15`.

- [ ] **Step 8: Commit anything left**

```bash
git status
# If clean: nothing to commit. If not: review and commit any final tweaks.
```

---

## Self-Review (post-write)

After writing this plan I checked it against the spec:

**Spec coverage (one task per requirement):**
- Schema additions → Task 2 ✓
- sqlc queries → Tasks 3, 21 ✓
- Money as cents → Tasks 7 (FormatMoney), 9 (lineitem) ✓
- One-time data migration → Task 21 (`atl invoice setup`) ✓
- Config additions (senders block) → Tasks 4–6 ✓
- CLI surface (`generate`, `show-senders`, `setup`) → Tasks 20–21 ✓
- Period defaults → Task 8 ✓
- Generation flow + atomicity → Task 19 ✓
- PDF layout → Tasks 11–16 ✓
- Error handling (every spec row) → Task 19 handler + Task 5 validate ✓
- Tests (unit + integration + PDF text extraction) → Tasks 4–17, 19, 21 ✓

**Placeholder scan:** None. Every code step has full code blocks. Error and edge case behavior is concretely specified.

**Type consistency:**
- `Sender`, `Bank`, `BankField`, `InvoiceSeed`, `GlobalInvoiceConfig`, `SendersConfig`, `LineItem`, `InvoiceDoc`, `BuildDocInput` — all referenced names match their definitions across tasks.
- `BuildDoc` consistent signature between Task 17 (defined) and Task 19 (used).
- `FormatInvoiceNumber(prefix, pad, n)`, `FormatDate(t)`, `FormatMoney(cents, currency)`, `FormatHours(hoursTimes100)` consistent.
- `store.GetCompanyForInvoice`, `store.SetCompanyBillingParams`, `store.SeedSenderStateParams`, `store.AllocateNextInvoiceNumber`, `store.InsertInvoiceFullParams`, `store.CountTicksForCompanyInRangeParams` — all match Task 3's query names exactly.

**Out-of-band notes:**
- maroto v2 import paths can drift between minor versions. Task 11 includes an explicit instruction: trust the compiler errors and fix imports, but don't change behavior.
- `t.Context()` is Go 1.24+. Substitute `context.Background()` on older versions; spec'd Go version is 1.25 so this is fine.
- The setup command in Task 21 assumes you already have a project named "Dentix" under "Foca.app". If your DB is different, edit Task 21's plans to match.
