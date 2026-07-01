package cli

import (
	"errors"
	"testing"
)

func TestIsDaemonStall(t *testing.T) {
	if !isDaemonStall(errors.New("context deadline exceeded")) {
		t.Error("should classify context-deadline as a stall")
	}
	if isDaemonStall(errors.New("company not found")) {
		t.Error("should not classify a real error as a stall")
	}
	if isDaemonStall(nil) {
		t.Error("nil is not a stall")
	}
}
