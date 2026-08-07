package daemon

import "sync/atomic"

// permStaleAfterSec is how long a probe result stays evidence about the
// present. Permission state is only ever written from collectFocusSignal,
// which runs solely while an allowlisted app is frontmost — focus anything
// else (a browser, a terminal that was never added to the allowlist) and no
// probe happens at all. Without an expiry, a denial recorded days ago is
// reported as current fact forever, which told users their permission was
// missing long after they had granted it.
//
// It must stay above titleBackoffMaxSec: a denial that is still being
// re-probed refreshes the state at least that often, and would otherwise
// decay to "unknown" between two legitimate attempts and flicker the warning.
const permStaleAfterSec int64 = 3 * titleBackoffMaxSec

// probeResult is the last observed permission state and the tick it was
// observed on. Stored behind a single atomic pointer so state and timestamp
// can never be read out of step.
type probeResult struct {
	state string
	at    int64
}

// PermissionTracker is a thread-safe holder for the most recent macOS
// permission probe result ("ok", "accessibility_denied",
// "idle_detection_failed") and when it was taken. Shared between the polling
// pipeline (which probes) and the RPC service (which reports it via Status).
//
// It reports "unknown" both before the first probe and once the last one has
// gone stale. Neither an absent probe nor an old one is evidence about now,
// and claiming otherwise is what made `atl status` unreliable in both
// directions.
type PermissionTracker struct {
	last atomic.Pointer[probeResult]
}

func NewPermissionTracker() *PermissionTracker {
	return &PermissionTracker{}
}

// Set records the result of a probe taken at unix time at.
func (p *PermissionTracker) Set(state string, at int64) {
	p.last.Store(&probeResult{state: state, at: at})
}

// Get returns the permission state as of unix time now, or "unknown" if
// nothing has been probed recently enough to still count as evidence.
func (p *PermissionTracker) Get(now int64) string {
	last := p.last.Load()
	if last == nil || now-last.at > permStaleAfterSec {
		return "unknown"
	}
	return last.state
}
