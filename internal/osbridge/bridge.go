package osbridge

import (
	"context"
	"errors"
)

// Bridge is the only seam between the daemon and the OS. All system calls,
// subprocess invocations, and platform-specific code go through this interface.
// Tests inject FakeBridge; production wires the Real* implementations.
//
// Every method takes a context.Context so callers can bound execution time
// and propagate shutdown cancellation. Implementations are expected to apply
// their own per-tool deadline on top of (not in place of) the caller's ctx.
type Bridge interface {
	Frontmost(ctx context.Context) (FrontmostInfo, error)
	FocusedWindowTitle(ctx context.Context, focused FrontmostInfo) (string, error)
	IdleSeconds(ctx context.Context) (int, error)
	ListProcesses(ctx context.Context) ([]ProcessSample, error)
	ProcessCWD(ctx context.Context, pid int) (string, error)
	// ProcessEnvVars reads several environment variables of pid in one probe.
	// Absent keys are omitted from the map (or map to ""); a gone pid yields
	// an empty map and nil error.
	ProcessEnvVars(ctx context.Context, pid int, keys ...string) (map[string]string, error)
}

type FrontmostInfo struct {
	BundleID string
	PID      int
	Name     string
	window   uintptr // Windows foreground window handle for consistent title capture
}

type ProcessSample struct {
	PID      int
	Name     string // executable basename, e.g. "claude"
	CPUTicks uint64 // monotonic counter, units = centiseconds
	Started  uint64 // opaque process creation time; zero if unavailable
}

// The errors below are part of the Bridge contract, so they live here rather
// than beside the darwin implementation that returns them: the pipeline
// checks for them on every platform.

// ErrAccessibilityDenied is returned when osascript fails due to System Events
// permission being denied. Callers should warn the user once and continue
// degraded.
var ErrAccessibilityDenied = errors.New("macos: accessibility/automation permission denied")

// ErrOsascriptTimeout reports that an osascript call outlived its deadline and
// had to be killed. Callers must treat this differently from an ordinary
// failure: a hung osascript is usually a wedged System Events, and respawning
// one every tick is how the daemon accumulated multi-GB orphans in production.
var ErrOsascriptTimeout = errors.New("macos: osascript timed out")

// ErrForegroundChanged means the window changed after the app was identified.
var ErrForegroundChanged = errors.New("foreground window changed")
