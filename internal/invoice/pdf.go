package invoice

import (
	"fmt"
	"os"

	"github.com/johnfercher/maroto/v2"
	"github.com/johnfercher/maroto/v2/pkg/components/col"
	"github.com/johnfercher/maroto/v2/pkg/components/image"
	"github.com/johnfercher/maroto/v2/pkg/components/line"
	"github.com/johnfercher/maroto/v2/pkg/components/row"
	"github.com/johnfercher/maroto/v2/pkg/components/text"
	"github.com/johnfercher/maroto/v2/pkg/config"
	"github.com/johnfercher/maroto/v2/pkg/consts/align"
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/core"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

// divider draws a full-width horizontal rule (margin to margin — SizePercent
// 100, since maroto defaults to 90) followed by padding, so the next block
// doesn't sit flush against the line.
func divider(m core.Maroto) {
	m.AddRow(1, col.New(12).Add(
		line.New(props.Line{Thickness: 0.2, Color: gray, SizePercent: 100}),
	))
	m.AddRows(row.New(8)) // ~24px padding below the divider
}

// gray is a grey color suitable for secondary text (labels).
var gray = &props.Color{Red: 128, Green: 128, Blue: 128}

// osStat is an indirection over os.Stat so tests can inject a mock.
var osStat = os.Stat

// RenderPDF writes the invoice document to outPath. Caller owns the path
// (mkdir parents beforehand). Overwrites any existing file at outPath.
func RenderPDF(doc InvoiceDoc, outPath string) error {
	cfg := config.NewBuilder().
		WithLeftMargin(20).
		WithTopMargin(20).
		WithRightMargin(20).
		Build()
	m := maroto.New(cfg)

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
			text.New("Invoice number", props.Text{Size: 8, Color: gray}),
			text.New(doc.Number, props.Text{Size: 10, Top: 4}),
		),
		col.New(3).Add(
			text.New("Issue date", props.Text{Size: 8, Color: gray}),
			text.New(FormatDate(doc.IssueDate), props.Text{Size: 10, Top: 4}),
		),
	)
	m.AddRows(row.New(2)) // small spacer
	divider(m)

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
	clientLines := buildClientLines(doc)
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
	m.AddRows(row.New(8)) // ~24px margin below the billed-to section

	// Line items table — header row.
	m.AddRow(6,
		col.New(5).Add(text.New("Product or service", props.Text{Size: 9, Style: fontstyle.Bold})),
		col.New(1).Add(text.New("Qty", props.Text{Size: 9, Style: fontstyle.Bold, Align: align.Right})),
		col.New(2).Add(text.New("Unit price", props.Text{Size: 9, Style: fontstyle.Bold, Align: align.Right})),
		col.New(1).Add(text.New("Tax", props.Text{Size: 9, Style: fontstyle.Bold, Align: align.Right})),
		col.New(3).Add(text.New("Total", props.Text{Size: 9, Style: fontstyle.Bold, Align: align.Right})),
	)
	divider(m)

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

	// Totals block — right-aligned. With a discount we surface the gross
	// subtotal and the reduction explicitly so the net "Amount Due" is
	// auditable; without one we keep the original 4-line layout unchanged.
	type totalLine struct {
		Label string
		Value string
		Bold  bool
	}
	amountDue := FormatMoney(doc.AmountDueCents(), doc.Currency)
	zero := FormatMoney(0, doc.Currency)
	var totalLines []totalLine
	if doc.DiscountCents > 0 {
		totalLines = append(totalLines,
			totalLine{"Subtotal", FormatMoney(doc.LineItem.TotalCents, doc.Currency), false},
			totalLine{"Discount", "-" + FormatMoney(doc.DiscountCents, doc.Currency), false},
		)
	}
	totalLines = append(totalLines,
		totalLine{"Total excluding tax", amountDue, false},
		totalLine{"Total tax", zero, false},
		totalLine{"Amount Due", amountDue, true},
		totalLine{"Due by", FormatDate(doc.DueDate), false},
	)
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
	m.AddRows(row.New(4)) // spacer

	divider(m)

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
			// AddAutoRow so a long value (e.g. a bank address) wraps across the
			// full column width instead of being clipped to a fixed height.
			m.AddAutoRow(
				col.New(3).Add(text.New(label, props.Text{Size: 8, Color: gray})),
				col.New(9).Add(text.New(vl, props.Text{Size: 9})),
			)
		}
		m.AddRows(row.New(2)) // gap between fields (auto-rows are otherwise flush)
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

// buildClientLines composes the "Billed to" lines. Falls back to the company
// name alone when no Client details are configured (back-compat). Order: legal
// name, "Label TaxID", email, then address lines — mirroring the sender block.
func buildClientLines(doc InvoiceDoc) []string {
	name := doc.ClientName
	if doc.Client.LegalName != "" {
		name = doc.Client.LegalName
	}
	lines := []string{name}
	if doc.Client.TaxID != "" {
		tax := doc.Client.TaxID
		if doc.Client.TaxIDLabel != "" {
			tax = doc.Client.TaxIDLabel + " " + doc.Client.TaxID
		}
		lines = append(lines, tax)
	}
	if doc.Client.Email != "" {
		lines = append(lines, doc.Client.Email)
	}
	return append(lines, doc.Client.AddressLines...)
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
