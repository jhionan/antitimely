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

	// CreditAppliedCents is the portion of an outstanding advance consumed by
	// this invoice. Distinct from DiscountCents: only this figure moves the
	// company's credit balance. CreditAppliedRef names the advance invoice it
	// came from (FIFO — oldest advance with credit remaining), so the client's
	// bookkeeper can tie the two documents together.
	CreditAppliedCents int64
	CreditAppliedRef   string

	// Bank block to render in "Ways to pay".
	Bank Bank

	// Logo (optional; absolute path; empty = no logo).
	LogoPath string
}

// AmountDueCents is the net payable: line-item total minus any flat discount
// and minus any advance credit applied.
func (d InvoiceDoc) AmountDueCents() int64 {
	return d.LineItem.TotalCents - d.DiscountCents - d.CreditAppliedCents
}
