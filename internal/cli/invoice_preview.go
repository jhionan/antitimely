package cli

import (
	"fmt"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
)

// formatInvoicePreview renders a confirmation summary of a dry-run invoice.
// Hourly invoices include the billed hours; monthly_fixed ones omit them
// (there is no per-hour component).
//
// When advance credit is being drawn down it adds a second line. Without it
// the summary shows only TotalCents — which is the NET amount due — so a
// fully-covered period reads as "292.46h · CAD 0.00" and looks like a failed
// generate. An operator who reads it that way re-runs, burning a second
// invoice number and a second drawdown.
func formatInvoicePreview(r rpcapi.InvoiceGenerateReply) string {
	period := fmt.Sprintf("%s→%s",
		time.Unix(r.FromUnix, 0).Local().Format("2006-01-02"),
		time.Unix(r.ToUnix, 0).Local().Format("2006-01-02"))
	amount := previewMoney(r.TotalCents, r.Currency)
	var line string
	if r.BillingMode == "hourly" {
		hours := float64(r.Ticks) * 5 / 3600
		line = fmt.Sprintf("%s · %s · %.2fh · %s due", r.Number, period, hours, amount)
	} else {
		line = fmt.Sprintf("%s · %s · %s due", r.Number, period, amount)
	}
	if r.CreditAppliedCents > 0 {
		line += fmt.Sprintf("\n  advance applied: %s · credit remaining after this invoice: %s"+
			"\n  (the amount due is net of the advance; `atl invoice generate --no-credit` bills the full amount)",
			previewMoney(r.CreditAppliedCents, r.Currency),
			previewMoney(r.CreditRemainingCents, r.Currency))
	}
	return line
}

// previewMoney formats cents the way the preview line has always shown them:
// currency first, no thousands separator (invoice.FormatMoney puts the
// currency last, which reads badly mid-sentence here).
func previewMoney(cents int64, currency string) string {
	return fmt.Sprintf("%s %.2f", currency, float64(cents)/100)
}
