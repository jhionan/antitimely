package daemon

import "testing"

// A tracker that has not observed anything yet knows nothing. Reporting "ok"
// before the first probe is a guess, and it is the optimistic half of the bug
// that made `atl status` unreliable in both directions.
func TestPermissionTracker_DefaultsToUnknown(t *testing.T) {
	pt := NewPermissionTracker()
	if got := pt.Get(1000); got != "unknown" {
		t.Errorf("Get() = %q, want %q", got, "unknown")
	}
}

func TestPermissionTracker_Set(t *testing.T) {
	pt := NewPermissionTracker()
	pt.Set("accessibility_denied", 1000)
	if got := pt.Get(1000); got != "accessibility_denied" {
		t.Errorf("Get() after Set = %q, want %q", got, "accessibility_denied")
	}
	pt.Set("ok", 1001)
	if got := pt.Get(1001); got != "ok" {
		t.Errorf("Get() after reset = %q, want %q", got, "ok")
	}
}

// The state is only ever written from collectFocusSignal, which is reachable
// only while an allowlisted app is frontmost. Focus a non-allowlisted app (a
// browser, a terminal that was never added to the allowlist) and no probe runs
// at all — so an old denial would otherwise be reported as current fact
// forever, telling the user their permission is missing long after they
// granted it. Past the staleness window we have no evidence, so we say so.
func TestPermissionTracker_StaleDenialReadsAsUnknown(t *testing.T) {
	pt := NewPermissionTracker()
	pt.Set("accessibility_denied", 1000)
	if got := pt.Get(1000 + permStaleAfterSec + 1); got != "unknown" {
		t.Errorf("Get() on a stale denial = %q, want %q", got, "unknown")
	}
}

// A denial that is still being re-confirmed must keep warning. The title probe
// backs off to at most titleBackoffMaxSec between attempts, so a daemon that is
// genuinely denied refreshes the state well inside the staleness window.
func TestPermissionTracker_FreshDenialIsReported(t *testing.T) {
	pt := NewPermissionTracker()
	pt.Set("accessibility_denied", 1000)
	if got := pt.Get(1000 + titleBackoffMaxSec); got != "accessibility_denied" {
		t.Errorf("Get() on a freshly re-probed denial = %q, want %q", got, "accessibility_denied")
	}
}

// The staleness window must sit above the probe's own backoff ceiling,
// otherwise an actively-probing denial would decay to "unknown" between two
// legitimate attempts and the warning would flicker.
func TestPermissionStaleWindowExceedsProbeBackoff(t *testing.T) {
	if permStaleAfterSec <= titleBackoffMaxSec {
		t.Errorf("permStaleAfterSec (%d) must exceed titleBackoffMaxSec (%d)",
			permStaleAfterSec, titleBackoffMaxSec)
	}
}

// A success goes stale too: "ok" from hours ago is no more evidence about the
// present than a denial from hours ago.
func TestPermissionTracker_StaleOKReadsAsUnknown(t *testing.T) {
	pt := NewPermissionTracker()
	pt.Set("ok", 1000)
	if got := pt.Get(1000 + permStaleAfterSec + 1); got != "unknown" {
		t.Errorf("Get() on a stale ok = %q, want %q", got, "unknown")
	}
}
