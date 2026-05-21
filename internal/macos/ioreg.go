package macos

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// IdleSecondsReal reads the system's idle time (seconds since the last HID
// event) by parsing `ioreg -c IOHIDSystem`. The "HIDIdleTime" property is in
// nanoseconds.
//
// Callers that hit this on a hot loop should wrap it in a short-lived cache
// (RealBridge does this with a 1-second TTL) — the idle clock has 1s
// resolution anyway, and ioreg is slow.
func IdleSecondsReal(ctx context.Context) (int, error) {
	cctx, cancel := withTimeout(ctx, ioregDeadline)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ioreg", "-c", "IOHIDSystem", "-d", "1")
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ioreg: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
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
