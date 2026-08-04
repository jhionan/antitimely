package cli

import (
	"errors"
	"os"
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

// TestDeleteFlagSetsDoNotExitOnBadInput guards the interactive menu. Both
// commands are reachable from it with raw typed input ("Invoice ID to
// delete:", "Company name to delete:"), where anything starting with "-"
// parses as an unknown flag. With flag.ExitOnError these calls os.Exit(2) and
// take the whole menu session down with them -- and, run from here, the test
// binary itself. They must return 64 instead so the menu loop survives.
//
// Both return before dialing the daemon, so this needs no running daemon.
func TestDeleteFlagSetsDoNotExitOnBadInput(t *testing.T) {
	// Keep the flag package's own usage dump out of the test log.
	silenceStderr(t)

	if code := invoiceDelete([]string{"-1"}); code != 64 {
		t.Errorf("invoiceDelete(-1) = %d, want 64", code)
	}
	if code := companyDelete([]string{"-oops"}); code != 64 {
		t.Errorf("companyDelete(-oops) = %d, want 64", code)
	}
}

// silenceStderr redirects os.Stderr to /dev/null for the duration of the test.
func silenceStderr(t *testing.T) {
	t.Helper()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = devNull
	t.Cleanup(func() {
		os.Stderr = orig
		devNull.Close()
	})
}
