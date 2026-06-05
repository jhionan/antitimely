package invoice

import "time"

// InvoiceDoc is the fully-resolved data that the PDF renderer consumes.
// All composition / rate logic / DB lookups happen before this struct is
// built; the renderer only formats and lays out.
type InvoiceDoc struct {
	Number     string // e.g. "INV-014"
	IssueDate  time.Time
	DueDate    time.Time
	PeriodFrom time.Time
	PeriodTo   time.Time
	Currency   string // "EUR", "CAD"

	// Billed-to: client company. ClientName is always set (the company name);
	// Client carries optional full details (legal name, tax id, email,
	// address). Zero-value Client renders name-only.
	ClientName string
	Client     Client

	// Issued-by: us.
	Sender Sender

	// Line item (single aggregated row).
	LineItemLabel string // "Software development"
	LineItem      LineItem

	// DiscountCents is a flat reduction (in Currency) applied to the line-item
	// total. 0 = no discount. When > 0 the PDF shows an explicit Subtotal and
	// Discount line; AmountDueCents is the net.
	DiscountCents int64

	// Bank block to render in "Ways to pay".
	Bank Bank

	// Logo (optional; absolute path; empty = no logo).
	LogoPath string
}

// AmountDueCents is the net payable: line-item total minus any flat discount.
func (d InvoiceDoc) AmountDueCents() int64 {
	return d.LineItem.TotalCents - d.DiscountCents
}
