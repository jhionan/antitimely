package invoice

import (
	"fmt"
	"strings"

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

// gray is a grey color suitable for secondary text (labels).
var gray = &props.Color{Red: 128, Green: 128, Blue: 128}

// RenderPDF writes the invoice document to outPath. Caller owns the path
// (mkdir parents beforehand). Overwrites any existing file at outPath.
func RenderPDF(doc InvoiceDoc, outPath string) error {
	cfg := config.NewBuilder().
		WithLeftMargin(20).
		WithTopMargin(20).
		WithRightMargin(20).
		Build()
	m := maroto.New(cfg)

	// Header: "Invoice" big title, with number + issue date on the right.
	m.AddRow(20,
		col.New(6).Add(
			text.New("Invoice", props.Text{
				Size:  24,
				Style: fontstyle.Bold,
			}),
		),
		col.New(3).Add(
			text.New("Invoice number", props.Text{Size: 8, Color: gray}),
			text.New(doc.Number, props.Text{Size: 10, Top: 4}),
		),
		col.New(3).Add(
			text.New("Issue date", props.Text{Size: 8, Color: gray}),
			text.New(FormatDate(doc.IssueDate), props.Text{Size: 10, Top: 4}),
		),
	)
	m.AddRows(row.New(2)) // small spacer
	// Horizontal divider below the header.
	m.AddRow(2, col.New(12).Add(
		text.New(strings.Repeat("_", 120), props.Text{Size: 6, Color: gray, Align: align.Left}),
	))

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
		text.New(strings.Repeat("_", 120), props.Text{Size: 6, Color: gray}),
	))

	// One row: the aggregated line item.
	qtyStr := FormatHours(doc.LineItem.QuantityHoursTimes100)
	unitStr := FormatMoney(doc.LineItem.UnitCents, doc.Currency)
	totalStr := FormatMoney(doc.LineItem.TotalCents, doc.Currency)
	// Period: from inclusive, to exclusive — show "from – (to-1day)" so the
	// reader sees the actual last day worked.
	periodStr := FormatDate(doc.PeriodFrom) + " – " + FormatDate(doc.PeriodTo.AddDate(0, 0, -1))

	m.AddRow(5,
		col.New(5).Add(text.New(doc.LineItemLabel, props.Text{Size: 9})),
		col.New(1).Add(text.New(qtyStr, props.Text{Size: 9, Align: align.Right})),
		col.New(2).Add(text.New(unitStr, props.Text{Size: 9, Align: align.Right})),
		col.New(1).Add(text.New("—", props.Text{Size: 9, Align: align.Right})),
		col.New(3).Add(text.New(totalStr, props.Text{Size: 9, Align: align.Right})),
	)
	m.AddRow(4,
		col.New(5).Add(text.New(periodStr, props.Text{Size: 7, Color: gray})),
		col.New(7),
	)
	m.AddRows(row.New(4)) // spacer

	// Totals block — right-aligned, 4 lines (right-aligned columns 9 + 10-12).
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
		style := fontstyle.Type("")
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

	// "Ways to pay" section.
	m.AddRow(5, col.New(12).Add(
		text.New("Ways to pay", props.Text{Size: 11, Style: fontstyle.Bold}),
	))
	m.AddRow(4, col.New(12).Add(
		text.New(doc.Bank.Title, props.Text{Size: 9, Style: fontstyle.Bold}),
	))
	if doc.Bank.Subtitle != "" {
		m.AddRow(4, col.New(12).Add(
			text.New(doc.Bank.Subtitle, props.Text{Size: 8, Color: gray}),
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
				col.New(3).Add(text.New(label, props.Text{Size: 8, Color: gray})),
				col.New(9).Add(text.New(vl, props.Text{Size: 9})),
			)
		}
	}

	return generateAndSave(m, outPath)
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

// splitLines splits s on '\n'. Returns at least one element (possibly empty
// string) so the renderer always has something to draw.
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
