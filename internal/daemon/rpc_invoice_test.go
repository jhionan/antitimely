package daemon

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
