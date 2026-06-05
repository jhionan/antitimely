package invoice

import (
	"fmt"
	"time"
)

// BuildDocInput is the fully-resolved set of inputs needed to build an
// InvoiceDoc. Caller (daemon-side orchestration) has already done DB lookups
// (last invoice, tick count, allocated number) and config parsing.
type BuildDocInput struct {
	Now           time.Time
	ClientName    string
	Client        Client
	BillingMode   string // "hourly" | "monthly_fixed"
	Currency      string
	RateCents     int64
	Sender        Sender
	InvoiceNumber string
	PeriodFrom    time.Time
	PeriodTo      time.Time
	DueDays       int
	LineItemLabel string
	Ticks         int64
	TickSec       int
	DiscountCents int64 // flat discount in Currency; 0 = none
}

// BuildDoc gathers an InvoiceDoc from the resolved inputs. Pure: no IO, no
// DB. Returns an error for the only thing it can detect: no bank account
// for the target currency.
func BuildDoc(in BuildDocInput) (InvoiceDoc, error) {
	bank, ok := in.Sender.BankFor(in.Currency)
	if !ok {
		return InvoiceDoc{}, fmt.Errorf("sender has no bank account for currency %q (and no also_accepts fallback)", in.Currency)
	}
	li := ComputeLineItem(in.BillingMode, in.Ticks, in.TickSec, in.RateCents)
	if in.DiscountCents < 0 {
		return InvoiceDoc{}, fmt.Errorf("discount must not be negative (got %d cents)", in.DiscountCents)
	}
	if in.DiscountCents > li.TotalCents {
		return InvoiceDoc{}, fmt.Errorf("discount %d cents exceeds line-item total %d cents", in.DiscountCents, li.TotalCents)
	}
	due := in.Now.AddDate(0, 0, in.DueDays)
	return InvoiceDoc{
		Number:        in.InvoiceNumber,
		IssueDate:     in.Now,
		DueDate:       due,
		PeriodFrom:    in.PeriodFrom,
		PeriodTo:      in.PeriodTo,
		Currency:      in.Currency,
		ClientName:    in.ClientName,
		Client:        in.Client,
		Sender:        in.Sender,
		LineItemLabel: in.LineItemLabel,
		LineItem:      li,
		DiscountCents: in.DiscountCents,
		Bank:          bank,
		LogoPath:      in.Sender.LogoPath,
	}, nil
}
