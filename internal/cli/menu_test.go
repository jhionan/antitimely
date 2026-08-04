package cli

import "testing"

func TestParseAdvanceAmount(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"14623", 1462300, false},
		{"14623.00", 1462300, false},
		{"0", 0, true},
		{"-5", 0, true},
		{"abc", 0, true},
	}
	for _, tc := range tests {
		got, err := parseAdvanceAmount(tc.in)
		if tc.wantErr && err == nil {
			t.Errorf("parseAdvanceAmount(%q): expected an error", tc.in)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("parseAdvanceAmount(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestCompanyBillableForAdvance(t *testing.T) {
	tests := []struct {
		name     string
		currency string
		rate     int64
		want     bool
	}{
		{"fully configured", "CAD", 5000, true},
		{"empty currency", "", 5000, false},
		{"zero rate", "CAD", 0, false},
		{"negative rate", "CAD", -1, false},
		{"nothing configured", "", 0, false},
	}
	for _, tc := range tests {
		if got := companyBillableForAdvance(tc.currency, tc.rate); got != tc.want {
			t.Errorf("%s: companyBillableForAdvance(%q, %d) = %v, want %v",
				tc.name, tc.currency, tc.rate, got, tc.want)
		}
	}
}
