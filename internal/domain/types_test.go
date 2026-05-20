package domain

import "testing"

func TestSignal_IsAgent(t *testing.T) {
	tests := []struct {
		name string
		sig  Signal
		want bool
	}{
		{"focus signal", Signal{Source: SourceFocus, BundleID: "x"}, false},
		{"agent signal", Signal{Source: SourceAgent, BinaryName: "claude"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sig.IsAgent(); got != tc.want {
				t.Errorf("IsAgent() = %v, want %v", got, tc.want)
			}
		})
	}
}
