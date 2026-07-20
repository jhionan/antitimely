package macos

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// ErrAccessibilityDenied is returned when osascript fails due to System Events
// permission being denied. Callers should warn the user once and continue
// degraded.
var ErrAccessibilityDenied = errors.New("macos: accessibility/automation permission denied")

// ErrOsascriptTimeout reports that an osascript call outlived its deadline and
// had to be killed. Callers must treat this differently from an ordinary
// failure: a hung osascript is usually a wedged System Events, and respawning
// one every tick is how the daemon accumulated multi-GB orphans in production.
var ErrOsascriptTimeout = errors.New("macos: osascript timed out")

// Tab is used as the inter-field delimiter in AppleScript return values because
// bundle IDs and app names can legitimately contain "|", but never a tab.
//
// This deliberately uses NSWorkspace rather than System Events. The old script
// asked System Events for `first application process whose frontmost is true`;
// that `whose` clause makes System Events materialise and evaluate EVERY
// application process, which is both slow and the second GB-balloon vector
// (the first, `windows of`, was removed from titleScript). Worse, it runs
// through the Accessibility/Automation bridge, so a wedged or unauthorised
// System Events hangs it until the deadline kills it — every 5s, forever.
//
// NSWorkspace's frontmostApplication is an O(1) lookup that needs no
// Accessibility grant at all. It costs ~330ms vs ~187ms in the happy path
// (framework load), which at a 5s poll is a fine trade for immunity to the
// hang, and it means focus tracking keeps working at bundle level even while
// window titles are denied.
const frontmostScript = `
use framework "Foundation"
set ws to current application's NSWorkspace's sharedWorkspace()
set a to ws's frontmostApplication()
set bid to ""
try
	set maybeId to (a's bundleIdentifier())
	if maybeId is not missing value then set bid to (maybeId as string)
end try
return bid & tab & (a's localizedName() as string) & tab & ((a's processIdentifier()) as string)
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
		-- Read ONLY one window's title, never "windows of frontProc".
		-- Enumerating the whole window list materialises every window of a
		-- heavy app (browser/Electron), ballooning osascript to GB and is the
		-- primary process-leak vector when System Events is slow. Prefer the
		-- focused window; fall back to window 1 for apps (some Electron ones)
		-- that don't expose AXFocusedWindow. A genuine denial fails both and
		-- propagates to the on-error handler below.
		try
			set w to value of attribute "AXFocusedWindow" of frontProc
		on error
			set w to window 1 of frontProc
		end try
		set wt to value of attribute "AXTitle" of w
		if wt is missing value or wt is "" then set wt to (name of w)
		if wt is missing value then set wt to ""
		return wt as string
	on error errMsg number errNum
		-- Emit a structured, control-byte-framed payload instead of swallowing
		-- the error as "". osascript can exit 0 even on an assistive-access
		-- denial, so this is how the denial reaches Go (see parseTitleOutput).
		set sep to (character id 1)
		return sep & "ATL_ERR" & sep & (errNum as string) & sep & errMsg
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
	return parseTitleOutput(out)
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

const (
	// titleErrSep is an SOH control byte (U+0001). No window title contains
	// it, so it safely frames the structured error the title script emits via
	// `on error` (see titleScript). titleErrPrefix marks an error payload.
	titleErrSep    = "\x01"
	titleErrPrefix = "\x01ATL_ERR\x01"
)

// classifyOsascriptErr returns ErrAccessibilityDenied when osascript output
// looks like a TCC assistive-access / Automation denial, else nil. macOS
// embeds the numeric AppleScript error code in parentheses regardless of UI
// language, so we anchor on the code first (-1743 is the classic "not
// authorized" code; -1728 is what a Spanish-locale machine reports for an
// assistive-access denial). The English phrase fallbacks catch codes we
// haven't catalogued on locales that still emit English text.
//
// Codes: -1743 (errAEEventNotPermitted, Automation denial), -1728
// (errAENoSuchObject, seen on some locales for assistive-access denial), and
// -25211 (kAXErrorAPIDisabled, returned when the AX API rejects an untrusted
// binary). The same assistive-access denial surfaces with different numeric
// codes depending on which AX access trips it — observed: -25211 (`windows
// of`), -1728 (`AXFocusedWindow` attribute), -1719 (`window 1`) — so all are
// treated as denials.
func classifyOsascriptErr(s string) error {
	for _, code := range []string{"(-1743)", "(-1728)", "(-25211)", "(-1719)"} {
		if strings.Contains(s, code) {
			return ErrAccessibilityDenied
		}
	}
	low := strings.ToLower(s)
	if strings.Contains(low, "not allowed assistive access") ||
		strings.Contains(low, "not authorized") {
		return ErrAccessibilityDenied
	}
	return nil
}

// parseTitleOutput interprets the stdout of titleScript. A real (possibly
// empty) title is returned as-is. A structured error payload — emitted by the
// script's `on error` handler so denials survive osascript's exit-0 behaviour —
// is decoded into ErrAccessibilityDenied or a generic error. This is what lets
// the caller distinguish "app has no title" from "I'm not allowed to read it".
func parseTitleOutput(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	rest, isErr := strings.CutPrefix(s, titleErrPrefix)
	if !isErr {
		return s, nil
	}
	code, msg, _ := strings.Cut(rest, titleErrSep)
	// Reconstruct the parenthesised code so classifyOsascriptErr can anchor on
	// it exactly as it does for raw osascript stderr.
	if err := classifyOsascriptErr("(" + code + ") " + msg); err != nil {
		return "", err
	}
	return "", fmt.Errorf("osascript title error %s: %s", code, msg)
}

func runOsascript(ctx context.Context, script string) (string, error) {
	cctx, cancel := withTimeout(ctx, osascriptDeadline)
	defer cancel()
	cmd := exec.CommandContext(cctx, "osascript", "-e", script)
	// Put osascript in its own process group so a hung one (blocked in an Apple
	// Event to an unresponsive System Events) can be killed as a group, taking
	// any helper children with it rather than orphaning them.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // negative pid = group
		return cmd.Process.Kill()
	}
	// If the process ignores the kill (stuck in an uninterruptible mach call),
	// give up after WaitDelay so CombinedOutput returns instead of blocking the
	// poll loop forever, and close the pipes so we don't leak fds.
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := string(out)
		if derr := classifyOsascriptErr(s); derr != nil {
			return "", derr
		}
		// A deadline hit means we killed it, not that the script failed. Report
		// it as such so callers can back off rather than respawning into the
		// same wedge. Checked after the denial classification because a denial
		// that happens to race the deadline is still a denial.
		if cctx.Err() != nil {
			return "", fmt.Errorf("%w after %s: %v", ErrOsascriptTimeout, osascriptDeadline, err)
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
