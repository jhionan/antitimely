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
