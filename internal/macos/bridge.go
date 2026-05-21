package macos

import "context"

// Bridge is the only seam between the daemon and macOS. All system calls,
// subprocess invocations, and platform-specific code go through this interface.
// Tests inject FakeBridge; production wires the Real* implementations.
//
// Every method takes a context.Context so callers can bound execution time
// and propagate shutdown cancellation. Implementations are expected to apply
// their own per-tool deadline on top of (not in place of) the caller's ctx.
type Bridge interface {
	Frontmost(ctx context.Context) (FrontmostInfo, error)
	FocusedWindowTitle(ctx context.Context) (string, error)
	IdleSeconds(ctx context.Context) (int, error)
	ListProcesses(ctx context.Context) ([]ProcessSample, error)
	ProcessCWD(ctx context.Context, pid int) (string, error)
}

type FrontmostInfo struct {
	BundleID string
	PID      int
	Name     string
}

type ProcessSample struct {
	PID      int
	Name     string // executable basename, e.g. "claude"
	CPUTicks uint64 // monotonic counter, units = centiseconds
}
