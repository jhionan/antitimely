package macos

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// IdleSecondsReal reads the system's idle time (seconds since the last HID
// event) by parsing `ioreg -r -c IOHIDSystem`. The "HIDIdleTime" property is
// in nanoseconds.
//
// The -r flag is load-bearing: it roots the output at the IOHIDSystem object
// so its properties (HIDIdleTime among them) are always printed. The earlier
// `-c IOHIDSystem -d 1` truncated the registry tree at depth 1, but the
// IOHIDSystem node sits ~4 levels deep, so HIDIdleTime fell outside the depth
// cap and was never found — idle detection failed continuously (the daemon
// then fails open to "user present", which let paused projects accrue agent
// ticks around the clock). With -r, -d 1 is relative to the rooted object, so
// the output stays small (~36 lines) and the property is always present.
//
// Callers that hit this on a hot loop should wrap it in a short-lived cache
// (RealBridge does this with a 1-second TTL) — the idle clock has 1s
// resolution anyway, and ioreg is slow.
func IdleSecondsReal(ctx context.Context) (int, error) {
	cctx, cancel := withTimeout(ctx, ioregDeadline)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ioreg", "-r", "-c", "IOHIDSystem", "-d", "1")
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ioreg: %w", err)
	}
	return parseHIDIdleTime(string(out))
}

// parseHIDIdleTime extracts the HIDIdleTime property (nanoseconds) from ioreg
// output and returns whole seconds. Split out from the exec so the parse is
// unit-testable against captured fixtures.
func parseHIDIdleTime(out string) (int, error) {
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "HIDIdleTime") {
			continue
		}
		eq := strings.LastIndex(line, "=")
		if eq < 0 {
			continue
		}
		raw := strings.TrimSpace(line[eq+1:])
		// Extract only the leading numeric token. Newer macOS versions can
		// append annotations (e.g., units) after the number.
		if fields := strings.Fields(raw); len(fields) > 0 {
			raw = fields[0]
		}
		ns, err := strconv.ParseUint(raw, 0, 64)
		if err != nil {
			return 0, fmt.Errorf("parse HIDIdleTime %q: %w", raw, err)
		}
		return int(ns / 1_000_000_000), nil
	}
	return 0, fmt.Errorf("HIDIdleTime not found in ioreg output")
}
