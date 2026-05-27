package daemon

import (
	"testing"

	"github.com/rian/antitimely/internal/domain"
)

func TestCache_LoadInitial(t *testing.T) {
	c := NewCache()
	snap := c.Snapshot()
	if snap == nil {
		t.Fatal("expected non-nil initial snapshot")
	}
	if len(snap.AllowedBundles) != 0 || len(snap.AllowedBinaries) != 0 || len(snap.Rules) != 0 {
		t.Errorf("expected empty cache initially, got %+v", snap)
	}
}

func TestCache_SwapVisible(t *testing.T) {
	c := NewCache()
	want := &CacheSnapshot{
		AllowedBundles:  map[string]bool{"com.foo": true},
		AllowedBinaries: map[string]bool{"claude": true},
		Rules: []domain.RuleSpec{
			{ID: 1, ProjectID: 10, Priority: 100,
				MatchBinaryName: strPtrLocal("claude")},
		},
	}
	c.Store(want)
	got := c.Snapshot()
	if !got.AllowedBundles["com.foo"] || !got.AllowedBinaries["claude"] {
		t.Errorf("allowlist not updated: %+v", got)
	}
	if len(got.Rules) != 1 || got.Rules[0].ID != 1 {
		t.Errorf("rules not updated: %+v", got.Rules)
	}
}

func TestCache_InitialArmedProjectsEmpty(t *testing.T) {
	c := NewCache()
	snap := c.Snapshot()
	if snap.ArmedProjects == nil {
		t.Fatal("ArmedProjects should be initialized, got nil")
	}
	if len(snap.ArmedProjects) != 0 {
		t.Errorf("expected empty ArmedProjects, got %v", snap.ArmedProjects)
	}
}

func strPtrLocal(s string) *string { return &s }
