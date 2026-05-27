package invoice

import "testing"

func TestLineItem_MonthlyFixed(t *testing.T) {
	li := ComputeLineItem("monthly_fixed", 0, 5, 300000)
	if li.QuantityHoursTimes100 != 100 { // we represent "1" as 100 to share the unit
		t.Errorf("QuantityHoursTimes100 = %d, want 100 (representing 1.00)", li.QuantityHoursTimes100)
	}
	if li.UnitCents != 300000 {
		t.Errorf("UnitCents = %d, want 300000", li.UnitCents)
	}
	if li.TotalCents != 300000 {
		t.Errorf("TotalCents = %d, want 300000", li.TotalCents)
	}
}

func TestLineItem_Hourly_47p5h(t *testing.T) {
	// 47.5 hours = 47.5 * 3600 = 171000 seconds = 34200 ticks at 5s/tick
	li := ComputeLineItem("hourly", 34200, 5, 5000) // 50.00 CAD/h
	// hours = 47.5 → represented as 4750
	if li.QuantityHoursTimes100 != 4750 {
		t.Errorf("QuantityHoursTimes100 = %d, want 4750", li.QuantityHoursTimes100)
	}
	if li.UnitCents != 5000 {
		t.Errorf("UnitCents = %d, want 5000", li.UnitCents)
	}
	// total = round(47.5 × 5000) = 237500
	if li.TotalCents != 237500 {
		t.Errorf("TotalCents = %d, want 237500", li.TotalCents)
	}
}

func TestLineItem_Hourly_BankersRounding(t *testing.T) {
	// 1.005h × 100 = 100.5 → banker's rounding to even → 100 (rounds to even)
	// Set up: ticks × tickSec / 3600 = 1.005 → ticks*tickSec = 3618
	// With tickSec=1, ticks=3618. Then hours*100 = 100.5
	li := ComputeLineItem("hourly", 3618, 1, 5000)
	// math.RoundToEven(100.5) = 100 (round half to even)
	if li.QuantityHoursTimes100 != 100 {
		t.Errorf("QuantityHoursTimes100 = %d, want 100 (banker's rounding)", li.QuantityHoursTimes100)
	}
}

func TestLineItem_Hourly_ZeroTicks(t *testing.T) {
	li := ComputeLineItem("hourly", 0, 5, 5000)
	if li.QuantityHoursTimes100 != 0 {
		t.Errorf("QuantityHoursTimes100 = %d, want 0", li.QuantityHoursTimes100)
	}
	if li.TotalCents != 0 {
		t.Errorf("TotalCents = %d, want 0", li.TotalCents)
	}
}
