package invoice

import "time"

// DefaultPeriod returns the default (from, to) window for an invoice based on
// the billing mode.
//
//	monthly_fixed: first day of `now`'s calendar month → first day of next month
//	hourly:        time.Unix(lastInvoiceSentAt) → now (caller substitutes
//	               company.created_at when lastInvoiceSentAt == 0)
//
// Returns (zero, now) for hourly when no prior invoice exists; the caller is
// responsible for substituting a sensible from (typically company.created_at).
func DefaultPeriod(billingMode string, now time.Time, lastInvoiceSentAt int64) (from, to time.Time) {
	switch billingMode {
	case "monthly_fixed":
		from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		to = from.AddDate(0, 1, 0)
		return
	case "hourly":
		to = now
		if lastInvoiceSentAt > 0 {
			from = time.Unix(lastInvoiceSentAt, 0).In(now.Location())
		}
		return
	}
	return time.Time{}, now
}
