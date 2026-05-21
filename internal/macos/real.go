package macos

import (
	"context"
	"sync"
	"time"
)

// Per-tool deadlines. macOS subprocesses can hang indefinitely (osascript when
// System Events is unresponsive after sleep/wake; ioreg under thermal load);
// without a deadline the daemon's poll loop freezes silently.
const (
	osascriptDeadline = 4 * time.Second
	ioregDeadline     = 3 * time.Second
	psDeadline        = 3 * time.Second
	lsofDeadline      = 2 * time.Second
)

// withTimeout returns a child context that is the more restrictive of the
// caller's ctx and the given per-tool deadline. Callers must defer the
// returned cancel.
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}

// RealBridge implements Bridge by delegating to the *Real functions in this
// package. The HID idle counter is cached for one second so the daemon's
// hot poll path doesn't shell out to ioreg on every tick.
type RealBridge struct {
	idleMu   sync.Mutex
	idleAt   time.Time
	idleVal  int
}

func (r *RealBridge) Frontmost(ctx context.Context) (FrontmostInfo, error) {
	return FrontmostReal(ctx)
}
func (r *RealBridge) FocusedWindowTitle(ctx context.Context) (string, error) {
	return FocusedWindowTitleReal(ctx)
}
func (r *RealBridge) IdleSeconds(ctx context.Context) (int, error) {
	now := time.Now()
	r.idleMu.Lock()
	if !r.idleAt.IsZero() && now.Sub(r.idleAt) < time.Second {
		v := r.idleVal
		r.idleMu.Unlock()
		return v, nil
	}
	r.idleMu.Unlock()

	v, err := IdleSecondsReal(ctx)
	if err != nil {
		return 0, err
	}
	r.idleMu.Lock()
	r.idleVal = v
	r.idleAt = now
	r.idleMu.Unlock()
	return v, nil
}
func (r *RealBridge) ListProcesses(ctx context.Context) ([]ProcessSample, error) {
	return ListProcessesReal(ctx)
}
func (r *RealBridge) ProcessCWD(ctx context.Context, pid int) (string, error) {
	return ProcessCWDReal(ctx, pid)
}

// Compile-time assertion that *RealBridge implements Bridge.
var _ Bridge = (*RealBridge)(nil)
