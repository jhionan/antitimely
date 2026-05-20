package macos

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ErrAccessibilityDenied is returned when osascript fails due to System Events
// permission being denied. Callers should warn the user once and continue
// degraded.
var ErrAccessibilityDenied = errors.New("macos: accessibility/automation permission denied")

const frontmostScript = `
tell application "System Events"
	set p to first application process whose frontmost is true
	set bid to ""
	try
		set maybeId to bundle identifier of p
		if maybeId is not missing value then
			set bid to maybeId
		end if
	end try
	set pn to name of p
	set pp to unix id of p
	return bid & "|" & pn & "|" & (pp as string)
end tell
`

// FrontmostReal returns the currently focused application's bundle id, name,
// and pid.
func FrontmostReal() (FrontmostInfo, error) {
	out, err := runOsascript(frontmostScript)
	if err != nil {
		return FrontmostInfo{}, err
	}
	parts := strings.SplitN(strings.TrimSpace(out), "|", 3)
	if len(parts) != 3 {
		return FrontmostInfo{}, fmt.Errorf("unexpected output: %q", out)
	}
	pid, err := strconv.Atoi(parts[2])
	if err != nil {
		return FrontmostInfo{}, fmt.Errorf("parse pid %q: %w", parts[2], err)
	}
	return FrontmostInfo{BundleID: parts[0], Name: parts[1], PID: pid}, nil
}

const titleScript = `
tell application "System Events"
	set p to first application process whose frontmost is true
	try
		set w to first window of p
		return name of w
	on error
		return ""
	end try
end tell
`

// FocusedWindowTitleReal returns the title of the focused window, or "" if
// the frontmost app has no window. Returns ErrAccessibilityDenied if the
// Automation permission is denied.
func FocusedWindowTitleReal() (string, error) {
	out, err := runOsascript(titleScript)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func runOsascript(script string) (string, error) {
	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := string(out)
		if strings.Contains(s, "not allowed assistive access") ||
			strings.Contains(s, "not authorized") ||
			strings.Contains(s, "(-1743)") {
			return "", ErrAccessibilityDenied
		}
		return "", fmt.Errorf("osascript: %w (%s)", err, strings.TrimSpace(s))
	}
	return string(out), nil
}
