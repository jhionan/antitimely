package invoice

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clientSampleSender() Sender {
	return Sender{
		LegalName:    "JHIONAN RIAN LARA DOS SANTOS",
		TaxID:        "ESZ2614896P",
		TaxIDLabel:   "VAT",
		AddressLines: []string{"Mislata", "Spain"},
		Invoice:      InvoiceSeed{NumberPrefix: "ES-", NumberPad: 4, NextNumber: 2},
		BankAccounts: map[string]Bank{
			"EUR": {AlsoAccepts: []string{"CAD"}, Title: "Bank details",
				Fields: []BankField{{Label: "IBAN", Value: "ES5115632626323269761258"}}},
		},
	}
}

func bclouderClient() Client {
	return Client{
		LegalName:    "Bclouder Inc",
		TaxID:        "773445408 RT0001",
		TaxIDLabel:   "GST/HST",
		Email:        "financial@bclouder.com",
		AddressLines: []string{"8959 Scurfield Dr NW", "Calgary, AB T3L 1H6", "Canada"},
	}
}

func TestBuildDoc_CarriesClientDetails(t *testing.T) {
	doc, err := BuildDoc(BuildDocInput{
		Now:           time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		ClientName:    "BClouder",
		Client:        bclouderClient(),
		BillingMode:   "hourly",
		Currency:      "CAD",
		RateCents:     5000,
		Sender:        clientSampleSender(),
		InvoiceNumber: "ES-0002",
		TickSec:       5,
		Ticks:         720, // 1h
	})
	if err != nil {
		t.Fatalf("BuildDoc: %v", err)
	}
	if doc.Client.LegalName != "Bclouder Inc" {
		t.Errorf("LegalName = %q, want Bclouder Inc", doc.Client.LegalName)
	}
	if doc.Client.TaxID != "773445408 RT0001" || doc.Client.Email != "financial@bclouder.com" {
		t.Errorf("client tax/email not carried: %+v", doc.Client)
	}
}

func TestRenderPDF_ShowsClientDetails(t *testing.T) {
	doc := sampleDoc()
	doc.ClientName = "BClouder"
	doc.Client = bclouderClient()

	path := filepath.Join(t.TempDir(), "client.pdf")
	if err := RenderPDF(doc, path); err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	text := extractPDFText(t, path)
	for _, want := range []string{"Bclouder Inc", "GST/HST", "773445408", "financial@bclouder.com", "Scurfield", "Calgary"} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered PDF missing client detail %q", want)
		}
	}
}
