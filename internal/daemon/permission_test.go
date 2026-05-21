package daemon

import "testing"

func TestPermissionTracker_DefaultsToOK(t *testing.T) {
	pt := NewPermissionTracker()
	if got := pt.Get(); got != "ok" {
		t.Errorf("Get() = %q, want %q", got, "ok")
	}
}

func TestPermissionTracker_Set(t *testing.T) {
	pt := NewPermissionTracker()
	pt.Set("accessibility_denied")
	if got := pt.Get(); got != "accessibility_denied" {
		t.Errorf("Get() after Set = %q, want %q", got, "accessibility_denied")
	}
	pt.Set("ok")
	if got := pt.Get(); got != "ok" {
		t.Errorf("Get() after reset = %q, want %q", got, "ok")
	}
}
