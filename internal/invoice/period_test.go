package invoice

import (
	"testing"
	"time"
)

func TestDefaultPeriod_MonthlyFixed(t *testing.T) {
	// 2026-05-27 → period = 2026-05-01 .. 2026-05-31 (end-of-month inclusive,
	// returned as exclusive upper bound 2026-06-01 midnight).
	now := time.Date(2026, 5, 27, 14, 0, 0, 0, time.UTC)
	from, to := DefaultPeriod("monthly_fixed", now, 0)
	wantFrom := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	wantTo := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if !from.Equal(wantFrom) {
		t.Errorf("from = %v, want %v", from, wantFrom)
	}
	if !to.Equal(wantTo) {
		t.Errorf("to = %v, want %v", to, wantTo)
	}
}

func TestDefaultPeriod_HourlyWithPriorInvoice(t *testing.T) {
	now := time.Date(2026, 5, 27, 14, 0, 0, 0, time.UTC)
	lastInvoiceUnix := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC).Unix()
	from, to := DefaultPeriod("hourly", now, lastInvoiceUnix)
	wantFrom := time.Unix(lastInvoiceUnix, 0).UTC()
	if !from.Equal(wantFrom) {
		t.Errorf("from = %v, want %v", from, wantFrom)
	}
	if !to.Equal(now) {
		t.Errorf("to = %v, want %v", to, now)
	}
}

func TestDefaultPeriod_HourlyNoPriorInvoice(t *testing.T) {
	now := time.Date(2026, 5, 27, 14, 0, 0, 0, time.UTC)
	// lastInvoiceUnix == 0 means caller signals "no prior invoice"; caller
	// supplies company.created_at separately, so DefaultPeriod returns
	// (zero time, now) and caller fills the from itself.
	from, to := DefaultPeriod("hourly", now, 0)
	if !from.IsZero() {
		t.Errorf("from = %v, want zero", from)
	}
	if !to.Equal(now) {
		t.Errorf("to = %v, want %v", to, now)
	}
}
