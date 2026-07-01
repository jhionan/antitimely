package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
)

func TestFormatInvoicePreviewHourly(t *testing.T) {
	from := time.Date(2026, 6, 16, 15, 52, 0, 0, time.Local)
	to := time.Date(2026, 7, 1, 18, 11, 0, 0, time.Local)
	got := formatInvoicePreview(rpcapi.InvoiceGenerateReply{
		Number: "ES-0004", BillingMode: "hourly",
		FromUnix: from.Unix(), ToUnix: to.Unix(),
		Ticks: 54706, TotalCents: 379900, Currency: "CAD",
	})
	for _, want := range []string{"ES-0004", "2026-06-16", "2026-07-01", "75.98h", "CAD 3799.00"} {
		if !strings.Contains(got, want) {
			t.Errorf("preview missing %q: %s", want, got)
		}
	}
}

func TestFormatInvoicePreviewMonthlyFixed(t *testing.T) {
	got := formatInvoicePreview(rpcapi.InvoiceGenerateReply{
		Number: "INV-014", BillingMode: "monthly_fixed",
		FromUnix: time.Date(2026, 6, 1, 0, 0, 0, 0, time.Local).Unix(),
		ToUnix:   time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local).Unix(),
		Ticks:    0, TotalCents: 300000, Currency: "EUR",
	})
	if !strings.Contains(got, "EUR 3000.00") || !strings.Contains(got, "INV-014") {
		t.Errorf("fixed preview wrong: %s", got)
	}
	if strings.Contains(got, "h ") || strings.Contains(got, "h·") || strings.Contains(got, "0.00h") {
		t.Errorf("monthly_fixed preview must not show hours: %s", got)
	}
}
