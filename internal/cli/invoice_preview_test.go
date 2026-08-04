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

// TestFormatInvoicePreviewShowsDrawdown covers the menu-confirmation case
// that used to read as a failure: a period fully covered by advance credit
// shows a zero amount due. Without the drawdown lines the operator sees
// "292.46h · CAD 0.00", assumes nothing was generated, re-runs, and burns a
// second invoice number plus a second drawdown.
func TestFormatInvoicePreviewShowsDrawdown(t *testing.T) {
	got := formatInvoicePreview(rpcapi.InvoiceGenerateReply{
		Number: "ES-0008", BillingMode: "hourly",
		FromUnix: time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local).Unix(),
		ToUnix:   time.Date(2026, 8, 1, 0, 0, 0, 0, time.Local).Unix(),
		Ticks:    210571, TotalCents: 0, Currency: "CAD",

		CreditAppliedCents:   1462300,
		CreditRemainingCents: 400000,
	})
	for _, want := range []string{
		"ES-0008",
		"292.46h",
		"CAD 0.00 due",
		"advance applied: CAD 14623.00",
		"credit remaining after this invoice: CAD 4000.00",
		"--no-credit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("drawdown preview missing %q:\n%s", want, got)
		}
	}
}

// A preview with no credit in play must stay a single quiet line.
func TestFormatInvoicePreviewNoDrawdownStaysQuiet(t *testing.T) {
	got := formatInvoicePreview(rpcapi.InvoiceGenerateReply{
		Number: "ES-0009", BillingMode: "hourly",
		FromUnix: time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local).Unix(),
		ToUnix:   time.Date(2026, 8, 1, 0, 0, 0, 0, time.Local).Unix(),
		Ticks:    720, TotalCents: 5000, Currency: "CAD",

		CreditAppliedCents:   0,
		CreditRemainingCents: 900000, // company holds credit it chose not to spend
	})
	if strings.Contains(got, "\n") {
		t.Errorf("preview should be one line when no credit is applied:\n%s", got)
	}
	if strings.Contains(got, "advance") {
		t.Errorf("preview should not mention an advance when none is applied: %s", got)
	}
}
