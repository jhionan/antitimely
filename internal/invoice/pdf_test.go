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
		Number:     "INV-014",
		IssueDate:  issue,
		DueDate:    issue,
		PeriodFrom: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		PeriodTo:   time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC),
		Currency:   "EUR",
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
		"1",            // monthly_fixed quantity (FormatHours(100) = "1")
		"3,000.00 EUR", // unit + total
	} {
		if !strings.Contains(text, w) {
			t.Errorf("PDF text missing %q\n---\n%s\n---", w, text)
		}
	}
}

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
	text := extractPDFText(t, out)

	if !strings.Contains(text, "Advance applied (ES-0007)") {
		t.Errorf("expected the advance row naming ES-0007; got:\n%s", text)
	}
	if strings.Contains(text, "Discount") {
		t.Errorf("Discount row must be omitted when zero; got:\n%s", text)
	}
	// sampleDoc's Currency is EUR, not the brief's illustrative CAD.
	if !strings.Contains(text, "0.00 EUR") {
		t.Errorf("expected an Amount Due of 0.00 EUR; got:\n%s", text)
	}
}

func TestRenderPDF_CreditAppliedNoRef_FallsBackToBareLabel(t *testing.T) {
	doc := sampleDoc()
	doc.LineItem = LineItem{QuantityHoursTimes100: 12000, UnitCents: 5000, TotalCents: 600000}
	doc.CreditAppliedCents = 200000
	doc.CreditAppliedRef = ""

	out := filepath.Join(t.TempDir(), "credit-noref.pdf")
	if err := RenderPDF(doc, out); err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	text := extractPDFText(t, out)

	if !strings.Contains(text, "Advance applied") {
		t.Errorf("expected the bare Advance applied row; got:\n%s", text)
	}
	if strings.Contains(text, "Advance applied (") {
		t.Errorf("expected no ref parenthetical when CreditAppliedRef is empty; got:\n%s", text)
	}
}

func TestRenderPDF_DiscountAndCredit_SubtotalRenderedOnce(t *testing.T) {
	doc := sampleDoc()
	doc.LineItem = LineItem{QuantityHoursTimes100: 12000, UnitCents: 5000, TotalCents: 600000}
	doc.DiscountCents = 50000
	doc.CreditAppliedCents = 100000
	doc.CreditAppliedRef = "ES-0007"

	out := filepath.Join(t.TempDir(), "credit-and-discount.pdf")
	if err := RenderPDF(doc, out); err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	text := extractPDFText(t, out)

	if got := strings.Count(text, "Subtotal"); got != 1 {
		t.Errorf("expected exactly one Subtotal row, got %d; text:\n%s", got, text)
	}
	if !strings.Contains(text, "Discount") {
		t.Errorf("expected the Discount row; got:\n%s", text)
	}
	if !strings.Contains(text, "Advance applied (ES-0007)") {
		t.Errorf("expected the advance row naming ES-0007; got:\n%s", text)
	}
	// 600000 - 50000 - 100000 = 450000
	if !strings.Contains(text, "4,500.00 EUR") {
		t.Errorf("expected an Amount Due of 4,500.00 EUR; got:\n%s", text)
	}
}

func TestRenderPDF_NoDiscountNoCredit_NoReductionRows(t *testing.T) {
	doc := sampleDoc()
	out := filepath.Join(t.TempDir(), "plain.pdf")
	if err := RenderPDF(doc, out); err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	text := extractPDFText(t, out)

	for _, unwanted := range []string{"Subtotal", "Discount", "Advance applied"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("did not expect %q in the zero-reduction case; got:\n%s", unwanted, text)
		}
	}
}

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
