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
