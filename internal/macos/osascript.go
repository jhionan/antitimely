package macos

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrAccessibilityDenied is returned when osascript fails due to System Events
// permission being denied. Callers should warn the user once and continue
// degraded.
var ErrAccessibilityDenied = errors.New("macos: accessibility/automation permission denied")

// Tab is used as the inter-field delimiter in AppleScript return values because
// bundle IDs and app names can legitimately contain "|", but never a tab.
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
	return bid & tab & pn & tab & (pp as string)
end tell
`

// FrontmostReal returns the currently focused application's bundle id, name,
// and pid.
func FrontmostReal(ctx context.Context) (FrontmostInfo, error) {
	out, err := runOsascript(ctx, frontmostScript)
	if err != nil {
		return FrontmostInfo{}, err
	}
	// Replace embedded newlines defensively in case an app name contains one.
	clean := strings.ReplaceAll(strings.TrimSpace(out), "\n", " ")
	parts := strings.SplitN(clean, "\t", 3)
	if len(parts) != 3 {
		return FrontmostInfo{}, fmt.Errorf("unexpected output: %q", out)
	}
	pid, err := parseInt(parts[2])
	if err != nil {
		return FrontmostInfo{}, fmt.Errorf("parse pid %q: %w", parts[2], err)
	}
	return FrontmostInfo{BundleID: parts[0], Name: parts[1], PID: pid}, nil
}

const titleScript = `
tell application "System Events"
	set frontProc to first application process whose frontmost is true
	try
		set winList to windows of frontProc
		repeat with w in winList
			try
				set wn to name of w
				if wn is not missing value and wn is not "" then
					return wn as string
				end if
			end try
			-- Fallback: try AXTitle attribute (some Electron apps don't expose 'name' but do expose AXTitle)
			try
				set wt to value of attribute "AXTitle" of w
				if wt is not missing value and wt is not "" then
					return wt as string
				end if
			end try
		end repeat
		return ""
	on error
		return ""
	end try
end tell
`

// FocusedWindowTitleReal returns the title of the focused window, or "" if
// the frontmost app has no window. Returns ErrAccessibilityDenied if the
// Automation permission is denied.
func FocusedWindowTitleReal(ctx context.Context) (string, error) {
	out, err := runOsascript(ctx, titleScript)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// FocusedWindowDebugReal returns a multi-line report describing every window
// of the frontmost process: index, name, AXTitle, AXRole, AXSubrole. Used
// by 'atl debug windows' to diagnose title-detection issues.
func FocusedWindowDebugReal(ctx context.Context) (string, error) {
	const debugScript = `
tell application "System Events"
	set frontProc to first application process whose frontmost is true
	set out to "frontmost: " & (name of frontProc) & " (bundle: " & (bundle identifier of frontProc) & ")" & linefeed
	try
		set winList to windows of frontProc
		set out to out & "  windows: " & (count of winList) & linefeed
		set i to 0
		repeat with w in winList
			set i to i + 1
			set out to out & "  [" & i & "]"
			try
				set wn to name of w
				if wn is missing value then set wn to "(missing)"
				set out to out & " name=" & quoted form of (wn as string)
			on error
				set out to out & " name=(error)"
			end try
			try
				set wt to value of attribute "AXTitle" of w
				if wt is missing value then set wt to "(missing)"
				set out to out & " AXTitle=" & quoted form of (wt as string)
			on error
				set out to out & " AXTitle=(error)"
			end try
			try
				set wr to value of attribute "AXRole" of w
				set out to out & " AXRole=" & (wr as string)
			on error
			end try
			try
				set wsr to value of attribute "AXSubrole" of w
				set out to out & " AXSubrole=" & (wsr as string)
			on error
			end try
			set out to out & linefeed
		end repeat
		return out
	on error errMsg
		return out & "ERROR: " & errMsg
	end try
end tell
`
	out, err := runOsascript(ctx, debugScript)
	if err != nil {
		return "", err
	}
	return out, nil
}

func runOsascript(ctx context.Context, script string) (string, error) {
	cctx, cancel := withTimeout(ctx, osascriptDeadline)
	defer cancel()
	cmd := exec.CommandContext(cctx, "osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := string(out)
		// -1743 is the canonical AppleScript error code for "not authorized".
		// Anchor on it first so non-English locales still get the typed sentinel;
		// keep the English-string fallbacks as a secondary signal.
		if strings.Contains(s, "(-1743)") ||
			strings.Contains(s, "not allowed assistive access") ||
			strings.Contains(s, "not authorized") {
			return "", ErrAccessibilityDenied
		}
		return "", fmt.Errorf("osascript: %w (%s)", err, strings.TrimSpace(s))
	}
	return string(out), nil
}

func parseInt(s string) (int, error) {
	var n int
	for _, c := range strings.TrimSpace(s) {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not an integer: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
