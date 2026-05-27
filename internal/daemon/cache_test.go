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

func TestCache_ArmAllProjects(t *testing.T) {
	c := NewCache()
	c.ArmAllProjects([]int64{1, 2, 3})
	got := c.Snapshot().ArmedProjects
	for _, id := range []int64{1, 2, 3} {
		if !got[id] {
			t.Errorf("expected project %d armed, got %v", id, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("expected exactly 3 armed, got %d", len(got))
	}
}

func TestCache_ArmAllProjects_EmptyIsNoop(t *testing.T) {
	c := NewCache()
	c.ArmAllProjects([]int64{1, 2})
	c.ArmAllProjects(nil)
	if len(c.Snapshot().ArmedProjects) != 2 {
		t.Errorf("nil input should be no-op, got %v", c.Snapshot().ArmedProjects)
	}
	c.ArmAllProjects([]int64{})
	if len(c.Snapshot().ArmedProjects) != 2 {
		t.Errorf("empty slice input should be no-op, got %v", c.Snapshot().ArmedProjects)
	}
}

func TestCache_ArmAllProjects_ReplacesPreviousMap(t *testing.T) {
	c := NewCache()
	c.ArmAllProjects([]int64{1, 2})
	c.ArmAllProjects([]int64{3, 4, 5})
	got := c.Snapshot().ArmedProjects
	if got[1] || got[2] {
		t.Errorf("previous IDs should be gone, got %v", got)
	}
	for _, id := range []int64{3, 4, 5} {
		if !got[id] {
			t.Errorf("expected project %d armed, got %v", id, got)
		}
	}
}
