package macos

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ProcessEnvVarReal returns the value of one environment variable for pid, via
// `ps -Eww -p PID`.
//
// Returns ("", nil) when the variable is absent or the pid is gone (ps exits 1),
// matching ProcessCWDReal's convention: absence is normal, a broken subsystem
// is an error. Reading another process's environment succeeds only for the same
// uid; the daemon runs as a launchd user agent, so this holds in production and
// fails closed if it ever does not.
func ProcessEnvVarReal(ctx context.Context, pid int, key string) (string, error) {
	cctx, cancel := withTimeout(ctx, psDeadline)
	defer cancel()
	out, err := exec.CommandContext(cctx, "ps", "-Eww", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return "", nil // pid gone between ps -A and now
		}
		return "", fmt.Errorf("ps -E pid=%d: %w", pid, err)
	}
	return parseEnvVar(string(out), key), nil
}

// parseEnvVar scans ps -E output for a KEY=VALUE token and returns VALUE.
func parseEnvVar(out, key string) string {
	want := key + "="
	for _, tok := range strings.Fields(out) {
		if strings.HasPrefix(tok, want) {
			return strings.TrimPrefix(tok, want)
		}
	}
	return ""
}
