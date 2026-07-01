package cli

import (
	"fmt"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
)

// formatInvoicePreview renders a one-line confirmation summary of a dry-run
// invoice. Hourly invoices include the billed hours; monthly_fixed ones omit
// them (there is no per-hour component).
func formatInvoicePreview(r rpcapi.InvoiceGenerateReply) string {
	period := fmt.Sprintf("%s→%s",
		time.Unix(r.FromUnix, 0).Local().Format("2006-01-02"),
		time.Unix(r.ToUnix, 0).Local().Format("2006-01-02"))
	amount := fmt.Sprintf("%s %.2f", r.Currency, float64(r.TotalCents)/100)
	if r.BillingMode == "hourly" {
		hours := float64(r.Ticks) * 5 / 3600
		return fmt.Sprintf("%s · %s · %.2fh · %s", r.Number, period, hours, amount)
	}
	return fmt.Sprintf("%s · %s · %s", r.Number, period, amount)
}
