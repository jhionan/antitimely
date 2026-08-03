package daemon

import (
	"context"
	"database/sql"
	"net/rpc"
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

// senderYAMLForRPCHourly numbers from ES-0008 (continuing the fixture's
// ES-0007 advance) and offers a CAD bank account, matching the hourly
// credit-drawdown tests' company currency.
const senderYAMLForRPCHourly = `
senders:
  br:
    legal_name: "JHIONAN RIAN LARA DOS SANTOS"
    tax_id: "34.012.215/0001-44"
    tax_id_label: "CNPJ"
    address_lines: ["Mateus Leme 2830", "Brazil"]
    invoice:
      number_prefix: "ES-"
      number_pad: 4
      next_number: 8
    bank_accounts:
      CAD:
        title: "Wise CAD"
        fields:
          - { label: "Account", value: "1234567" }

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
	q := store.New(db)
	if err := q.SetCompanyBilling(context.Background(), store.SetCompanyBillingParams{
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

	var reply rpcapi.InvoiceGenerateReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceGenerate",
		rpcapi.InvoiceGenerateArgs{CompanyName: "Dentix"}, &reply); err != nil {
		t.Fatalf("InvoiceGenerate: %v", err)
	}

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

	info, err := os.Stat(reply.PDFPath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("PDF missing or empty at %s: %v", reply.PDFPath, err)
	}
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

	row := db.QueryRow("SELECT next_invoice_number FROM sender_state WHERE sender_key='br'")
	var n int64
	if err := row.Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 15 {
		t.Errorf("sender_state.next_invoice_number = %d, want 15", n)
	}

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
	if err := q.SetCompanyBilling(context.Background(), store.SetCompanyBillingParams{
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
	if reply.BillingMode != "monthly_fixed" {
		t.Errorf("BillingMode = %q, want monthly_fixed", reply.BillingMode)
	}
	if reply.ToUnix <= reply.FromUnix {
		t.Errorf("period not set: from=%d to=%d", reply.FromUnix, reply.ToUnix)
	}
	var n int64
	if err := db.QueryRow("SELECT COUNT(*) FROM invoices").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("invoices rows = %d on dry-run, want 0", n)
	}
	entries, _ := os.ReadDir(outDir)
	for _, e := range entries {
		if !e.IsDir() {
			t.Errorf("dry-run left a file in output_dir: %s", e.Name())
		}
	}
	if _, err := os.Stat(reply.PDFPath); err != nil {
		t.Errorf("dry-run PDF missing at %s", reply.PDFPath)
	}
}

func TestCompanyCreditBalance(t *testing.T) {
	_, db, _ := setupRPCServer(t)
	q := store.New(db)
	ctx := context.Background()

	if _, err := db.Exec(`INSERT INTO companies (id, name, created_at) VALUES (3, 'BClouder', 0)`); err != nil {
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

	if _, err := db.Exec(`INSERT INTO companies (id, name, created_at) VALUES (3, 'BClouder', 0)`); err != nil {
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

// seedHourlyCompanyWithTicks creates company "BClouder" billed hourly at
// 50.00 CAD/hr from sender "br", backdates its created_at far enough into
// the past that hours*3600 seconds worth of 5s-grid ticks land safely
// before "now" (the upper bound of the default hourly period), and writes
// the sender config used by InvoiceGenerate.
func seedHourlyCompanyWithTicks(t *testing.T, client *rpc.Client, db *sql.DB, hours float64) {
	t.Helper()

	var add rpcapi.CompanyAddReply
	if err := client.Call(rpcapi.ServiceName+".CompanyAdd",
		rpcapi.CompanyAddArgs{Name: "BClouder"}, &add); err != nil {
		t.Fatal(err)
	}
	q := store.New(db)
	ctx := context.Background()
	if err := q.SetCompanyBilling(ctx, store.SetCompanyBillingParams{
		BillingMode: "hourly",
		Currency:    sql.NullString{String: "CAD", Valid: true},
		RateCents:   sql.NullInt64{Int64: 5000, Valid: true},
		BilledFrom:  sql.NullString{String: "br", Valid: true},
		Name:        "BClouder",
	}); err != nil {
		t.Fatal(err)
	}

	createdAt := time.Now().Add(-200 * time.Hour).Unix()
	if _, err := db.Exec(`UPDATE companies SET created_at = ? WHERE id = ?`, createdAt, add.ID); err != nil {
		t.Fatal(err)
	}

	projRes, err := db.Exec(
		`INSERT INTO projects (name, company_id, paused, created_at) VALUES ('BClouder-work', ?, 0, ?)`,
		add.ID, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := projRes.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	obsRes, err := db.Exec(
		`INSERT INTO observations (source, binary_name, first_seen) VALUES ('agent', 'claude', ?)`,
		createdAt)
	if err != nil {
		t.Fatal(err)
	}
	obsID, err := obsRes.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	ticks := int64(hours * 720) // hours*3600s / 5s-grid
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO ticks (ts, observation_id, project_id) VALUES (?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < ticks; i++ {
		if _, err := stmt.Exec(createdAt+5+i*5, obsID, projectID); err != nil {
			t.Fatal(err)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	outDir := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	body := strings.Replace(senderYAMLForRPCHourly, "%s", outDir, 1)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTITIMELY_CONFIG", cfgPath)
}

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
