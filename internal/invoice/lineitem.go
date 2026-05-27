package invoice

import "math"

// LineItem holds the rendered numbers for the single aggregated row on an
// invoice. Quantity is stored as hours×100 (integer) to keep all money math
// in integers downstream.
type LineItem struct {
	QuantityHoursTimes100 int64 // 4750 = 47.50h; 100 = "1" for monthly_fixed
	UnitCents             int64
	TotalCents            int64
}

// ComputeLineItem produces the LineItem for the chosen billing mode.
//
//	monthly_fixed:  quantity=1 (encoded as 100), unit=rate_cents, total=rate_cents
//	hourly:         hours = banker's-round(ticks*tickSec/3600, 2)
//	                quantity = hours (×100), unit = rate_cents,
//	                total = banker's-round(hours × rate_cents)
//
// All rounding uses math.RoundToEven (banker's) to avoid systematic upward
// bias across many invoices. The total is computed from the *displayed*
// quantity so client-side multiplication matches our total exactly.
func ComputeLineItem(billingMode string, ticks int64, tickSec int, rateCents int64) LineItem {
	switch billingMode {
	case "monthly_fixed":
		return LineItem{
			QuantityHoursTimes100: 100,
			UnitCents:             rateCents,
			TotalCents:            rateCents,
		}
	case "hourly":
		hoursTimes100 := int64(math.RoundToEven(float64(ticks*int64(tickSec)) / 3600.0 * 100.0))
		// total_cents = round(hours * rate_cents) = round(hoursTimes100 * rateCents / 100)
		totalCents := int64(math.RoundToEven(float64(hoursTimes100*rateCents) / 100.0))
		return LineItem{
			QuantityHoursTimes100: hoursTimes100,
			UnitCents:             rateCents,
			TotalCents:            totalCents,
		}
	}
	return LineItem{}
}
