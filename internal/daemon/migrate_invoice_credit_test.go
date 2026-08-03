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
