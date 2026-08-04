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

// TestLastInvoicePerCompany_IgnoresAdvances covers the OTHER anchor query.
// Status uses this one for each company's unbilled "(since: ...)" figure. If
// it counted advances, issuing one (which stamps `now`) would reset the
// dashboard's unbilled hours to ~0 while invoice generate still billed from
// the last real invoice.
func TestLastInvoicePerCompany_IgnoresAdvances(t *testing.T) {
	_, db, _ := setupRPCServer(t)
	q := store.New(db)
	ctx := context.Background()

	if _, err := db.Exec(`
		INSERT INTO companies (id, name, created_at) VALUES (3, 'BClouder', 0), (4, 'AdvanceOnly', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO invoices (company_id, sent_at, created_at, number, total_cents, currency, kind) VALUES
		 (3, 1000, 1000, 'ES-0006',  537700, 'CAD', 'hourly'),
		 (3, 5000, 5000, 'ES-0007', 1462300, 'CAD', 'advance'),
		 (4, 7000, 7000, 'ES-0008',  100000, 'CAD', 'advance')`); err != nil {
		t.Fatal(err)
	}

	rows, err := q.LastInvoicePerCompany(ctx)
	if err != nil {
		t.Fatal(err)
	}
	anchors := map[int64]int64{}
	for _, r := range rows {
		anchors[r.CompanyID] = asInt64(r.LastSent)
	}
	if anchors[3] != 1000 {
		t.Errorf("anchor for BClouder = %d, want 1000 — the advance at 5000 must not move it", anchors[3])
	}
	if _, ok := anchors[4]; ok {
		t.Errorf("AdvanceOnly has only an advance, so it must have no anchor at all; got %d", anchors[4])
	}
}

// seedHourlyCompanyWithTicks creates company "BClouder" billed hourly at
// 50.00 CAD/hr from sender "br", backdates its created_at far enough into
// the past that hours*3600 seconds worth of 5s-grid ticks land safely
// before "now" (the upper bound of the default hourly period), and writes
// the sender config used by InvoiceGenerate. Returns the configured
// invoice.output_dir (real-run PDFs land in <outDir>/<senderKey>/); most
// callers ignore it.
func seedHourlyCompanyWithTicks(t *testing.T, client *rpc.Client, db *sql.DB, hours float64) string {
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
	return outDir
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
	// CreditRemainingCents documents itself as the remaining balance. Under
	// --no-credit nothing is drawn down, so it must still report the credit
	// the company actually holds, not 0.
	if reply.CreditRemainingCents != 1462300 {
		t.Errorf("CreditRemainingCents = %d, want 1462300 — --no-credit leaves the balance untouched, it does not zero it",
			reply.CreditRemainingCents)
	}
}

// TestRPC_InvoiceGenerate_NamesOldestAdvanceWithCreditLeft covers the FIFO
// reference rule: once the first advance is fully consumed, the document must
// cite the next advance, not the exhausted one the client's bookkeeper has
// already reconciled.
func TestRPC_InvoiceGenerate_NamesOldestAdvanceWithCreditLeft(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	seedHourlyCompanyWithTicks(t, client, db, 20.0) // 20 h at 50.00/h = 1,000.00

	// ES-0005 advanced 1,000.00 and was fully drawn down by ES-0006.
	// ES-0007 advanced 5,000.00 and is untouched — that is the live one.
	if _, err := db.Exec(`
		INSERT INTO invoices (company_id, sent_at, created_at, number, total_cents, currency, kind, credit_applied_cents)
		SELECT id, 100, 100, 'ES-0005', 100000, 'CAD', 'advance', 0 FROM companies WHERE name='BClouder'
		UNION ALL
		SELECT id, 200, 200, 'ES-0006',      0, 'CAD', 'hourly', 100000 FROM companies WHERE name='BClouder'
		UNION ALL
		SELECT id, 300, 300, 'ES-0007', 500000, 'CAD', 'advance', 0 FROM companies WHERE name='BClouder'`); err != nil {
		t.Fatal(err)
	}

	var reply rpcapi.InvoiceGenerateReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceGenerate",
		rpcapi.InvoiceGenerateArgs{CompanyName: "BClouder"}, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.CreditAppliedCents != 100000 {
		t.Fatalf("CreditAppliedCents = %d, want 100000", reply.CreditAppliedCents)
	}

	text := extractPDFTextRPC(t, reply.PDFPath)
	if !strings.Contains(text, "ES-0007") {
		t.Errorf("PDF should cite ES-0007 (the advance with credit left); got:\n%s", text)
	}
	if strings.Contains(text, "ES-0005") {
		t.Errorf("PDF still cites the exhausted advance ES-0005; got:\n%s", text)
	}
}

// TestRPC_InvoiceGenerate_LeavesNoPDFWhenInsertFails pins the render-to-temp
// half of "render the PDF, but only land it after the row commits": a failed
// insert must leave nothing behind at either the client-facing path or the
// hidden temp path beside it.
func TestRPC_InvoiceGenerate_LeavesNoPDFWhenInsertFails(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	outDir := seedHourlyCompanyWithTicks(t, client, db, 120.0)

	// Force the invoice INSERT to fail after the PDF has been rendered.
	if _, err := db.Exec(
		`CREATE TRIGGER refuse_invoice BEFORE INSERT ON invoices BEGIN SELECT RAISE(ABORT, 'boom'); END`,
	); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DROP TRIGGER refuse_invoice`)

	var reply rpcapi.InvoiceGenerateReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceGenerate",
		rpcapi.InvoiceGenerateArgs{CompanyName: "BClouder"}, &reply); err == nil {
		t.Fatal("expected InvoiceGenerate to fail: the insert is rigged to abort")
	}

	senderDir := filepath.Join(outDir, "br")
	final := filepath.Join(senderDir, "ES-0008.pdf")
	tmp := filepath.Join(senderDir, ".ES-0008.pdf.tmp")
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Errorf("orphan PDF left at the client-facing path %s (stat err = %v)", final, err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("rendered temp PDF left at %s (stat err = %v)", tmp, err)
	}
	var n int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM invoices`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("invoices rows = %d after the aborted insert, want 0", n)
	}
}

// TestRPC_InvoiceGenerate_RendersToTempThenRenames pins the ORDERING that
// TestRPC_InvoiceGenerate_LeavesNoPDFWhenInsertFails cannot: rendering
// straight to the final path would still pass that test, because the failure
// branch removes whatever it rendered.
//
// Here the final path is occupied by a directory, so the two implementations
// diverge observably. Rendering to a temp file first succeeds, the row
// commits, and only the closing os.Rename fails — leaving a committed
// invoice and an error naming both paths. Rendering straight to the final
// path would instead fail before any row existed.
func TestRPC_InvoiceGenerate_RendersToTempThenRenames(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	outDir := seedHourlyCompanyWithTicks(t, client, db, 120.0)

	senderDir := filepath.Join(outDir, "br")
	if err := os.MkdirAll(filepath.Join(senderDir, "ES-0008.pdf"), 0o700); err != nil {
		t.Fatal(err)
	}

	var reply rpcapi.InvoiceGenerateReply
	err := client.Call(rpcapi.ServiceName+".InvoiceGenerate",
		rpcapi.InvoiceGenerateArgs{CompanyName: "BClouder"}, &reply)
	if err == nil {
		t.Fatal("expected the final rename to fail: a directory occupies the destination")
	}
	if !strings.Contains(err.Error(), "committed") {
		t.Errorf("error should report that the row committed and only the move failed; got: %v", err)
	}

	// The row must exist: the commit happens before the file is landed, so a
	// rename failure never rolls the invoice back.
	var n int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM invoices WHERE number='ES-0008'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("invoices rows for ES-0008 = %d, want 1 — the PDF is rendered to a temp path so the row can commit first", n)
	}
}

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

func TestRPC_InvoiceAdvance_RejectsNonBillable(t *testing.T) {
	client, _, _ := setupRPCServer(t)
	if err := client.Call(rpcapi.ServiceName+".CompanyAdd",
		rpcapi.CompanyAddArgs{Name: "Foca.app"}, &rpcapi.CompanyAddReply{}); err != nil {
		t.Fatal(err)
	}
	var reply rpcapi.InvoiceAdvanceReply
	err := client.Call(rpcapi.ServiceName+".InvoiceAdvance",
		rpcapi.InvoiceAdvanceArgs{CompanyName: "Foca.app", AmountCents: 100000}, &reply)
	if err == nil {
		t.Error("expected error for a company with billing_mode='none'")
	}
}

func TestRPC_InvoiceAdvance_RejectsNoBilledFrom(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	q := store.New(db)
	ctx := context.Background()
	if err := client.Call(rpcapi.ServiceName+".CompanyAdd",
		rpcapi.CompanyAddArgs{Name: "NoSender"}, &rpcapi.CompanyAddReply{}); err != nil {
		t.Fatal(err)
	}
	if err := q.SetCompanyBilling(ctx, store.SetCompanyBillingParams{
		BillingMode: "hourly",
		Currency:    sql.NullString{String: "CAD", Valid: true},
		RateCents:   sql.NullInt64{Int64: 5000, Valid: true},
		Name:        "NoSender",
	}); err != nil {
		t.Fatal(err)
	}
	var reply rpcapi.InvoiceAdvanceReply
	err := client.Call(rpcapi.ServiceName+".InvoiceAdvance",
		rpcapi.InvoiceAdvanceArgs{CompanyName: "NoSender", AmountCents: 100000}, &reply)
	if err == nil {
		t.Error("expected error for a company with no billed_from sender")
	}
}

func TestRPC_InvoiceAdvance_RejectsNoRate(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	q := store.New(db)
	ctx := context.Background()
	if err := client.Call(rpcapi.ServiceName+".CompanyAdd",
		rpcapi.CompanyAddArgs{Name: "NoRate"}, &rpcapi.CompanyAddReply{}); err != nil {
		t.Fatal(err)
	}
	if err := q.SetCompanyBilling(ctx, store.SetCompanyBillingParams{
		BillingMode: "hourly",
		Currency:    sql.NullString{String: "CAD", Valid: true},
		BilledFrom:  sql.NullString{String: "br", Valid: true},
		Name:        "NoRate",
	}); err != nil {
		t.Fatal(err)
	}
	var reply rpcapi.InvoiceAdvanceReply
	err := client.Call(rpcapi.ServiceName+".InvoiceAdvance",
		rpcapi.InvoiceAdvanceArgs{CompanyName: "NoRate", AmountCents: 100000}, &reply)
	if err == nil {
		t.Error("expected error for a company with no rate")
	}
}

func TestRPC_InvoiceAdvance_DryRun(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	seedHourlyCompanyWithTicks(t, client, db, 1.0)

	var reply rpcapi.InvoiceAdvanceReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceAdvance",
		rpcapi.InvoiceAdvanceArgs{CompanyName: "BClouder", AmountCents: 100000, DryRun: true}, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.TotalCents != 100000 {
		t.Errorf("TotalCents = %d, want 100000", reply.TotalCents)
	}
	if _, err := os.Stat(reply.PDFPath); err != nil {
		t.Errorf("dry-run PDF missing at %s", reply.PDFPath)
	}
	var n int64
	if err := db.QueryRow("SELECT COUNT(*) FROM invoices").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("invoices rows = %d on dry-run, want 0", n)
	}
}

// extractPDFTextRPC re-reads a rendered PDF's text content. Mirrors the
// extraction already inlined in TestRPC_InvoiceGenerate_MonthlyFixedHappyPath;
// pulled out here so TestRPC_InvoiceAdvance_PDFShowsIssueMonthPeriod doesn't
// have to repeat it.
func extractPDFTextRPC(t *testing.T, path string) string {
	t.Helper()
	f, r, err := pdf.Open(path)
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
	return sb.String()
}

// TestRPC_InvoiceAdvance_PDFShowsIssueMonthPeriod covers Task 7's review
// finding: InvoiceAdvance built its InvoiceDoc without PeriodFrom/PeriodTo,
// so pdf.go's unconditional period line rendered the zero-value range
// "January 1, 0001 – December 31, 0000" on every advance PDF. The fix pins
// PeriodFrom to the issue date and PeriodTo to the first of the following
// month (exclusive, so the printed end date is the issue month's last day) —
// this asserts on the rendered PDF text, not on doc struct fields, since the
// original bug was a renderer-visible defect that no struct-level assertion
// would have caught.
func TestRPC_InvoiceAdvance_PDFShowsIssueMonthPeriod(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	seedHourlyCompanyWithTicks(t, client, db, 1.0)

	issueDate := time.Date(2026, time.August, 4, 12, 0, 0, 0, time.Local)

	var reply rpcapi.InvoiceAdvanceReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceAdvance",
		rpcapi.InvoiceAdvanceArgs{
			CompanyName:   "BClouder",
			AmountCents:   100000,
			IssueDateUnix: issueDate.Unix(),
		}, &reply); err != nil {
		t.Fatal(err)
	}

	text := extractPDFTextRPC(t, reply.PDFPath)
	if !strings.Contains(text, "August 4, 2026") {
		t.Errorf("PDF missing period start %q\n---\n%s\n---", "August 4, 2026", text)
	}
	if !strings.Contains(text, "August 31, 2026") {
		t.Errorf("PDF missing period end %q\n---\n%s\n---", "August 31, 2026", text)
	}
	if strings.Contains(text, "January 1, 0001") || strings.Contains(text, "December 31, 0000") {
		t.Errorf("PDF still shows the zero-value period; got:\n%s", text)
	}
}

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

// TestRPC_InvoiceBalance_SkipsNullNumber covers the one legacy row observed
// in the real database: an advance with a NULL number (and NULL total_cents)
// that must be silently skipped rather than rendered with an empty
// identifier. It still satisfies CompanyCreditRows' WHERE clause
// (kind = 'advance') so the guard in InvoiceBalance is what filters it out,
// not the query.
func TestRPC_InvoiceBalance_SkipsNullNumber(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	seedHourlyCompanyWithTicks(t, client, db, 1.0)
	if _, err := db.Exec(`
		INSERT INTO invoices (company_id, sent_at, created_at, number, total_cents, currency, kind, credit_applied_cents)
		SELECT id, 50, 50, NULL, NULL, 'CAD', 'advance', 0 FROM companies WHERE name='BClouder'
		UNION ALL
		SELECT id, 100, 100, 'ES-0007', 1462300, 'CAD', 'advance', 0 FROM companies WHERE name='BClouder'`); err != nil {
		t.Fatal(err)
	}

	var reply rpcapi.InvoiceBalanceReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceBalance",
		rpcapi.InvoiceBalanceArgs{CompanyName: "BClouder"}, &reply); err != nil {
		t.Fatal(err)
	}
	for _, r := range reply.Rows {
		if r.Number == "" {
			t.Errorf("Rows contains a row with an empty Number: %+v", r)
		}
	}
	found := false
	for _, r := range reply.Rows {
		if r.Number == "ES-0007" {
			found = true
		}
	}
	if !found {
		t.Errorf("Rows missing valid row ES-0007; got %+v", reply.Rows)
	}
}

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

// TestRPC_InvoiceList_OrdersByIDDescOnSentAtTie covers the real ES-0006/
// ES-0007 case: the advance is anchor-neutral, so it gets stamped with the
// previous invoice's sent_at and the two rows tie exactly on that column.
// Without "ORDER BY sent_at DESC, id DESC" their relative order is
// unspecified by SQLite and can flip between runs. Both list RPCs
// (all-companies and single-company) share the same ORDER BY, so this also
// exercises ListAllInvoices via the company_name == "" branch.
func TestRPC_InvoiceList_OrdersByIDDescOnSentAtTie(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	seedHourlyCompanyWithTicks(t, client, db, 1.0)
	if _, err := db.Exec(`
		INSERT INTO invoices (company_id, sent_at, created_at, number, total_cents, currency, kind)
		SELECT id, 1000, 1000, 'ES-0006', 537700, 'CAD', 'hourly' FROM companies WHERE name='BClouder'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO invoices (company_id, sent_at, created_at, number, total_cents, currency, kind, credit_applied_cents)
		SELECT id, 1000, 1000, 'ES-0007', 1462300, 'CAD', 'advance', 0 FROM companies WHERE name='BClouder'`); err != nil {
		t.Fatal(err)
	}
	var id6, id7 int64
	if err := db.QueryRow(`SELECT id FROM invoices WHERE number='ES-0006'`).Scan(&id6); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT id FROM invoices WHERE number='ES-0007'`).Scan(&id7); err != nil {
		t.Fatal(err)
	}
	if id7 <= id6 {
		t.Fatalf("test setup invariant broken: id7 (%d) must be > id6 (%d)", id7, id6)
	}

	var reply rpcapi.InvoiceListReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceList",
		rpcapi.InvoiceListArgs{CompanyName: "BClouder"}, &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(reply.Items))
	}
	if reply.Items[0].SentAtUnix != reply.Items[1].SentAtUnix {
		t.Fatalf("test setup invariant broken: sent_at must tie, got %d and %d",
			reply.Items[0].SentAtUnix, reply.Items[1].SentAtUnix)
	}
	if reply.Items[0].ID != id7 || reply.Items[1].ID != id6 {
		t.Errorf("order = (%d, %d), want (%d, %d) — higher id first on a sent_at tie",
			reply.Items[0].ID, reply.Items[1].ID, id7, id6)
	}

	// Same assertion through ListAllInvoices (CompanyName == "").
	var allReply rpcapi.InvoiceListReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceList",
		rpcapi.InvoiceListArgs{}, &allReply); err != nil {
		t.Fatal(err)
	}
	if len(allReply.Items) != 2 {
		t.Fatalf("len(allReply.Items) = %d, want 2", len(allReply.Items))
	}
	if allReply.Items[0].ID != id7 || allReply.Items[1].ID != id6 {
		t.Errorf("ListAllInvoices order = (%d, %d), want (%d, %d) — higher id first on a sent_at tie",
			allReply.Items[0].ID, allReply.Items[1].ID, id7, id6)
	}
}

// TestRPC_InvoiceList_LegacyRowHasZeroValues covers a real row in the
// production database (the May anchor invoice), which predates the
// number/total_cents/currency columns and so carries NULL in all three. The
// RPC handler unwraps sql.NullString/sql.NullInt64 to Go zero values rather
// than propagating sql.Null* across the wire (see rpc.go InvoiceList and the
// field comments on rpcapi.InvoiceEntry). This asserts that boundary: the
// RPC reply must carry the documented zero values, not panic and not drop
// the row.
//
// The "-" substitution for NULL and the "0.00"-vs-"-" distinction for zero
// applied credit are printer behavior in internal/cli/invoice.go
// (invoiceList). There is no stdout-capturing test harness for
// internal/cli — building one is out of scope for this fix — so that half
// of the rendering (RPC zero-value -> CLI "-") remains unverified by an
// automated test; it was checked by hand in the prior implementer's report
// ("Sample list output").
func TestRPC_InvoiceList_LegacyRowHasZeroValues(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	seedHourlyCompanyWithTicks(t, client, db, 1.0)
	if _, err := db.Exec(`
		INSERT INTO invoices (company_id, sent_at, created_at, note)
		SELECT id, 1621331580, 1621331580, 'May anchor' FROM companies WHERE name='BClouder'`); err != nil {
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
	item := reply.Items[0]
	if item.Number != "" {
		t.Errorf("Number = %q, want \"\" for a legacy NULL-number row", item.Number)
	}
	if item.TotalCents != 0 {
		t.Errorf("TotalCents = %d, want 0 for a legacy NULL-total_cents row", item.TotalCents)
	}
	if item.Currency != "" {
		t.Errorf("Currency = %q, want \"\" for a legacy NULL-currency row", item.Currency)
	}
	if item.Kind != "hourly" {
		t.Errorf("Kind = %q, want \"hourly\" (schema default)", item.Kind)
	}
	if item.CreditAppliedCents != 0 {
		t.Errorf("CreditAppliedCents = %d, want 0 (schema default)", item.CreditAppliedCents)
	}
	if item.Note != "May anchor" {
		t.Errorf("Note = %q, want %q", item.Note, "May anchor")
	}
}
