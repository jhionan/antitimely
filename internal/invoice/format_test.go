package invoice

import (
	"testing"
	"time"
)

func TestFormatMoney(t *testing.T) {
	cases := []struct {
		cents    int64
		currency string
		want     string
	}{
		{0, "EUR", "0.00 EUR"},
		{50, "EUR", "0.50 EUR"},
		{300000, "EUR", "3,000.00 EUR"},
		{237500, "CAD", "2,375.00 CAD"},
		{1234567890, "USD", "12,345,678.90 USD"},
	}
	for _, tc := range cases {
		got := FormatMoney(tc.cents, tc.currency)
		if got != tc.want {
			t.Errorf("FormatMoney(%d, %q) = %q, want %q", tc.cents, tc.currency, got, tc.want)
		}
	}
}

func TestFormatHours(t *testing.T) {
	cases := []struct {
		hoursTimes100 int64 // hours expressed as hours*100 (since we round to 2dp)
		want          string
	}{
		{0, "0"},
		{50, "0.5"},
		{4750, "47.5"},
		{475, "4.75"},
		{12345, "123.45"},
	}
	for _, tc := range cases {
		got := FormatHours(tc.hoursTimes100)
		if got != tc.want {
			t.Errorf("FormatHours(%d) = %q, want %q", tc.hoursTimes100, got, tc.want)
		}
	}
}

func TestFormatDate(t *testing.T) {
	d := time.Date(2026, 5, 27, 14, 0, 0, 0, time.UTC)
	got := FormatDate(d)
	if got != "May 27, 2026" {
		t.Errorf("FormatDate = %q, want %q", got, "May 27, 2026")
	}
}

func TestFormatInvoiceNumber(t *testing.T) {
	cases := []struct {
		prefix string
		pad    int
		n      int64
		want   string
	}{
		{"INV-", 3, 14, "INV-014"},
		{"INV-", 3, 1, "INV-001"},
		{"ES-", 4, 1, "ES-0001"},
		{"ES-", 4, 1234, "ES-1234"},
		{"X", 0, 7, "X7"},
	}
	for _, tc := range cases {
		got := FormatInvoiceNumber(tc.prefix, tc.pad, tc.n)
		if got != tc.want {
			t.Errorf("FormatInvoiceNumber(%q, %d, %d) = %q, want %q",
				tc.prefix, tc.pad, tc.n, got, tc.want)
		}
	}
}
