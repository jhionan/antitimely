package invoice

import "testing"

func TestApplyCredit(t *testing.T) {
	tests := []struct {
		name                        string
		credit, lineTotal, goodwill int64
		want                        int64
	}{
		{"credit covers the whole invoice", 1462300, 600000, 0, 600000},
		{"credit smaller than invoice", 862300, 2000000, 0, 862300},
		{"no credit", 0, 1000000, 0, 0},
		{"goodwill takes precedence, credit fills the rest", 1462300, 600000, 100000, 500000},
		{"goodwill alone exceeds the line", 1462300, 600000, 700000, 0},
		{"zero-value invoice consumes nothing", 1462300, 0, 0, 0},
		{"negative credit is clamped, never negative", -600000, 1000000, 0, 0},
		{"exact exhaustion", 600000, 600000, 0, 600000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ApplyCredit(tc.credit, tc.lineTotal, tc.goodwill); got != tc.want {
				t.Errorf("ApplyCredit(%d, %d, %d) = %d, want %d",
					tc.credit, tc.lineTotal, tc.goodwill, got, tc.want)
			}
		})
	}
}
