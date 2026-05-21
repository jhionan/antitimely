package macos

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ProcessCWDReal returns the current working directory of the given pid, by
// shelling out to `lsof -a -p PID -d cwd -Fn`. Output format is:
//
//	p<pid>
//	f<fd>
//	n<path>
//
// Returns ("", nil) when lsof exits 1 (lsof's documented "no matches" code —
// pid gone, process has no cwd open, sandboxed-out). Any other failure
// (lsof missing, killed by signal, parse error) returns a non-nil error so
// callers can distinguish "not found" from "subsystem broken".
func ProcessCWDReal(ctx context.Context, pid int) (string, error) {
	cctx, cancel := withTimeout(ctx, lsofDeadline)
	defer cancel()
	cmd := exec.CommandContext(cctx, "lsof", "-a", "-p", fmt.Sprintf("%d", pid), "-d", "cwd", "-Fn")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			// Documented "no matches" — normal for short-lived or restricted pids.
			return "", nil
		}
		return "", fmt.Errorf("lsof pid=%d: %w", pid, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return strings.TrimPrefix(line, "n"), nil
		}
	}
	return "", nil
}
