package macos

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// IdleSecondsReal reads the system's idle time (seconds since the last HID
// event) by parsing `ioreg -c IOHIDSystem`. The "HIDIdleTime" property is in
// nanoseconds.
func IdleSecondsReal() (int, error) {
	cmd := exec.Command("ioreg", "-c", "IOHIDSystem")
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
		ns, err := strconv.ParseUint(raw, 0, 64)
		if err != nil {
			return 0, fmt.Errorf("parse HIDIdleTime %q: %w", raw, err)
		}
		return int(ns / 1_000_000_000), nil
	}
	return 0, fmt.Errorf("HIDIdleTime not found in ioreg output")
}
