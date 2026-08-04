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
