package invoice

import (
	"testing"
	"time"
)

func TestBuildDoc_MonthlyFixed(t *testing.T) {
	sender := Sender{
		LegalName:    "JHIONAN RIAN LARA DOS SANTOS",
		TaxID:        "34.012.215/0001-44",
		TaxIDLabel:   "CNPJ",
		AddressLines: []string{"Mateus Leme 2830", "Brazil"},
		BankAccounts: map[string]Bank{
			"EUR": {Title: "Wise EUR", Fields: []BankField{{Label: "IBAN", Value: "BE16"}}},
		},
	}
	in := BuildDocInput{
		Now:           time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC),
		ClientName:    "Dentix",
		BillingMode:   "monthly_fixed",
		Currency:      "EUR",
		RateCents:     300000,
		Sender:        sender,
		InvoiceNumber: "INV-014",
		PeriodFrom:    time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		PeriodTo:      time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		DueDays:       0,
		LineItemLabel: "Software development",
		Ticks:         0,
		TickSec:       5,
	}
	doc, err := BuildDoc(in)
	if err != nil {
		t.Fatal(err)
	}
	if doc.LineItem.TotalCents != 300000 {
		t.Errorf("total = %d, want 300000", doc.LineItem.TotalCents)
	}
	if doc.Number != "INV-014" {
		t.Errorf("number = %q", doc.Number)
	}
	if doc.Bank.Fields[0].Value != "BE16" {
		t.Errorf("bank not chosen correctly: %+v", doc.Bank)
	}
}

func TestBuildDoc_HourlyWithCAD_via_AlsoAccepts(t *testing.T) {
	sender := Sender{
		LegalName: "Y",
		BankAccounts: map[string]Bank{
			"EUR": {AlsoAccepts: []string{"CAD"}, Title: "ES EUR", Fields: []BankField{{Label: "IBAN", Value: "ES51"}}},
		},
	}
	in := BuildDocInput{
		Now:           time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC),
		ClientName:    "BClouder",
		BillingMode:   "hourly",
		Currency:      "CAD",
		RateCents:     5000,
		Sender:        sender,
		InvoiceNumber: "ES-0001",
		PeriodFrom:    time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		PeriodTo:      time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC),
		DueDays:       7,
		LineItemLabel: "Software development",
		Ticks:         34200,
		TickSec:       5,
	}
	doc, err := BuildDoc(in)
	if err != nil {
		t.Fatal(err)
	}
	if doc.LineItem.TotalCents != 237500 {
		t.Errorf("total = %d, want 237500", doc.LineItem.TotalCents)
	}
	if doc.Bank.Title != "ES EUR" {
		t.Errorf("bank lookup via also_accepts failed: %+v", doc.Bank)
	}
	wantDue := in.Now.AddDate(0, 0, 7)
	if !doc.DueDate.Equal(wantDue) {
		t.Errorf("DueDate = %v, want %v", doc.DueDate, wantDue)
	}
}

func TestBuildDoc_Discount(t *testing.T) {
	sender := Sender{
		LegalName:    "Y",
		BankAccounts: map[string]Bank{"CAD": {Title: "ES CAD", Fields: []BankField{{Label: "IBAN", Value: "ES51"}}}},
	}
	base := BuildDocInput{
		Now:           time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC),
		ClientName:    "BClouder",
		BillingMode:   "hourly",
		Currency:      "CAD",
		RateCents:     5000,
		Sender:        sender,
		InvoiceNumber: "ES-0002",
		PeriodFrom:    time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC),
		PeriodTo:      time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC),
		DueDays:       7,
		LineItemLabel: "Software development",
		Ticks:         34200, // 47.5h × $50 = $2375.00 = 237500 cents
		TickSec:       5,
	}

	// $50.00 discount → net 232500.
	in := base
	in.DiscountCents = 5000
	doc, err := BuildDoc(in)
	if err != nil {
		t.Fatalf("BuildDoc: %v", err)
	}
	if doc.LineItem.TotalCents != 237500 {
		t.Errorf("gross line-item total = %d, want 237500 (discount must not alter the line item)", doc.LineItem.TotalCents)
	}
	if doc.DiscountCents != 5000 {
		t.Errorf("DiscountCents = %d, want 5000", doc.DiscountCents)
	}
	if got := doc.AmountDueCents(); got != 232500 {
		t.Errorf("AmountDueCents = %d, want 232500", got)
	}

	// Discount exceeding the total is rejected.
	bad := base
	bad.DiscountCents = 300000
	if _, err := BuildDoc(bad); err == nil {
		t.Error("expected error when discount exceeds line-item total")
	}

	// Negative discount is rejected.
	neg := base
	neg.DiscountCents = -1
	if _, err := BuildDoc(neg); err == nil {
		t.Error("expected error for negative discount")
	}

	// Zero discount keeps amount due == line-item total (backward compatible).
	zero := base
	zd, err := BuildDoc(zero)
	if err != nil {
		t.Fatalf("BuildDoc(zero): %v", err)
	}
	if zd.AmountDueCents() != zd.LineItem.TotalCents {
		t.Errorf("no-discount AmountDueCents = %d, want %d", zd.AmountDueCents(), zd.LineItem.TotalCents)
	}
}

func TestBuildDoc_CreditApplied(t *testing.T) {
	sender := Sender{
		LegalName:    "JHIONAN RIAN LARA DOS SANTOS",
		TaxID:        "34.012.215/0001-44",
		TaxIDLabel:   "CNPJ",
		AddressLines: []string{"Mateus Leme 2830", "Brazil"},
		BankAccounts: map[string]Bank{
			"EUR": {Title: "Wise EUR", Fields: []BankField{{Label: "IBAN", Value: "BE16"}}},
		},
	}
	in := BuildDocInput{
		Now:                time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC),
		ClientName:         "Dentix",
		BillingMode:        "monthly_fixed",
		Currency:           "EUR",
		RateCents:          600000, // line total 6,000.00
		Sender:             sender,
		InvoiceNumber:      "INV-014",
		PeriodFrom:         time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		PeriodTo:           time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		DueDays:            0,
		LineItemLabel:      "Software development",
		Ticks:              0,
		TickSec:            5,
		DiscountCents:      0,
		CreditAppliedCents: 600000,
		CreditAppliedRef:   "ES-0007",
	}

	doc, err := BuildDoc(in)
	if err != nil {
		t.Fatalf("BuildDoc: %v", err)
	}
	if doc.CreditAppliedCents != 600000 {
		t.Errorf("CreditAppliedCents = %d, want 600000", doc.CreditAppliedCents)
	}
	if doc.CreditAppliedRef != "ES-0007" {
		t.Errorf("CreditAppliedRef = %q, want %q", doc.CreditAppliedRef, "ES-0007")
	}
	if got := doc.AmountDueCents(); got != 0 {
		t.Errorf("AmountDueCents = %d, want 0", got)
	}
}

func TestBuildDoc_RejectsReductionsExceedingLineTotal(t *testing.T) {
	sender := Sender{
		LegalName:    "JHIONAN RIAN LARA DOS SANTOS",
		TaxID:        "34.012.215/0001-44",
		TaxIDLabel:   "CNPJ",
		AddressLines: []string{"Mateus Leme 2830", "Brazil"},
		BankAccounts: map[string]Bank{
			"EUR": {Title: "Wise EUR", Fields: []BankField{{Label: "IBAN", Value: "BE16"}}},
		},
	}
	in := BuildDocInput{
		Now:                time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC),
		ClientName:         "Dentix",
		BillingMode:        "monthly_fixed",
		Currency:           "EUR",
		RateCents:          600000,
		Sender:             sender,
		InvoiceNumber:      "INV-014",
		PeriodFrom:         time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		PeriodTo:           time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		DueDays:            0,
		LineItemLabel:      "Software development",
		Ticks:              0,
		TickSec:            5,
		DiscountCents:      100000,
		CreditAppliedCents: 550000, // 650000 > 600000
	}

	if _, err := BuildDoc(in); err == nil {
		t.Fatal("expected an error when discount+credit exceeds the line total")
	}
}

func TestBuildDoc_RejectsNegativeCredit(t *testing.T) {
	sender := Sender{
		LegalName:    "JHIONAN RIAN LARA DOS SANTOS",
		TaxID:        "34.012.215/0001-44",
		TaxIDLabel:   "CNPJ",
		AddressLines: []string{"Mateus Leme 2830", "Brazil"},
		BankAccounts: map[string]Bank{
			"EUR": {Title: "Wise EUR", Fields: []BankField{{Label: "IBAN", Value: "BE16"}}},
		},
	}
	in := BuildDocInput{
		Now:                time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC),
		ClientName:         "Dentix",
		BillingMode:        "monthly_fixed",
		Currency:           "EUR",
		RateCents:          300000,
		Sender:             sender,
		InvoiceNumber:      "INV-014",
		PeriodFrom:         time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		PeriodTo:           time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		DueDays:            0,
		LineItemLabel:      "Software development",
		Ticks:              0,
		TickSec:            5,
		CreditAppliedCents: -1,
	}
	if _, err := BuildDoc(in); err == nil {
		t.Fatal("expected an error for negative credit applied")
	}
}

func TestBuildDoc_RejectsMissingBankBlock(t *testing.T) {
	sender := Sender{LegalName: "X", BankAccounts: map[string]Bank{}}
	in := BuildDocInput{
		Now: time.Now(), ClientName: "x", BillingMode: "hourly",
		Currency: "USD", RateCents: 1, Sender: sender, InvoiceNumber: "X-1",
		PeriodFrom: time.Now(), PeriodTo: time.Now(),
		LineItemLabel: "X", Ticks: 720, TickSec: 5,
	}
	if _, err := BuildDoc(in); err == nil {
		t.Error("expected error for missing bank block, got nil")
	}
}
