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
// is an error. An absent variable and one set to the empty string are
// indistinguishable — both yield ("", nil) — which is the intended contract,
// not a bug to fix.
//
// Contract: the returned value is truncated at the first whitespace character
// (see parseEnvVar) — this call is suitable only for variables whose values
// contain no whitespace, such as HERDR_PANE_ID's compact "wN:p1" token. A
// value containing a space, e.g. KEY=foo bar, is silently truncated to "foo".
//
// Reading another process's environment succeeds only for the same uid; ps
// exits 0 but prints no environment tokens at all for a pid owned by a
// different uid (verified against a live root-owned pid), so a cross-uid
// read looks identical to an absent variable — ("", nil), not an error. The
// daemon runs as a launchd user agent, so same-uid reads are the norm in
// production and a cross-uid read fails closed to "absent" rather than
// erroring.
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
//
// The match is a single whitespace-delimited token: out is split on all
// whitespace (strings.Fields), so a value containing a space is truncated at
// the space (KEY=foo bar yields "foo", silently dropping "bar"). ps -Eww
// does not quote or escape environment values, so a complete key/value
// parse is not possible in general — a value could itself contain a
// substring that looks like NEXTKEY=. This function deliberately does not
// attempt to disambiguate that; it is correct only for values known to
// contain no whitespace. An absent key and a key set to the empty string
// both return "".
func parseEnvVar(out, key string) string {
	want := key + "="
	for _, tok := range strings.Fields(out) {
		if strings.HasPrefix(tok, want) {
			return strings.TrimPrefix(tok, want)
		}
	}
	return ""
}
