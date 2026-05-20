package macos

import "testing"

func TestParseCPUTime(t *testing.T) {
	tests := []struct {
		in   string
		want uint64
	}{
		{"0:00.00", 0},
		{"0:01.50", 150},
		{"1:23.45", 8345},
		{"1:00:00", 360000},
	}
	for _, tc := range tests {
		got, err := parseCPUTime(tc.in)
		if err != nil {
			t.Errorf("parseCPUTime(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseCPUTime(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
