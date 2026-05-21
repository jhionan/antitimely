package daemon

import "sync/atomic"

// PermissionTracker is a thread-safe holder for the current macOS permission
// state ("ok", "accessibility_denied", "unknown"). Shared between the
// polling pipeline (which detects failures) and the RPC service (which
// reports it via Status).
type PermissionTracker struct {
	state atomic.Pointer[string]
}

func NewPermissionTracker() *PermissionTracker {
	pt := &PermissionTracker{}
	ok := "ok"
	pt.state.Store(&ok)
	return pt
}

func (p *PermissionTracker) Set(state string) {
	s := state
	p.state.Store(&s)
}

func (p *PermissionTracker) Get() string {
	s := p.state.Load()
	if s == nil {
		return "unknown"
	}
	return *s
}
