package daemon

import (
	"context"
	"testing"

	"github.com/rian/antitimely/internal/domain"
	"github.com/rian/antitimely/internal/macos"
	"github.com/rian/antitimely/internal/store"
)

func TestCache_Suppressed_AddAndClearOnDisarm(t *testing.T) {
	c := NewCache()
	c.ArmProject(5)
	if got := c.AddSuppressed(5); got != 1 {
		t.Fatalf("first AddSuppressed=%d, want 1", got)
	}
	if got := c.AddSuppressed(5); got != 2 {
		t.Fatalf("second AddSuppressed=%d, want 2", got)
	}
	if got := c.SuppressedAll()[5]; got != 2 {
		t.Fatalf("SuppressedAll[5]=%d, want 2", got)
	}
	// Disarming a project clears its suppressed counter so the "Xm suppressed"
	// warning disappears once the project is flowing again.
	c.DisarmProject(5)
	if got := c.SuppressedAll()[5]; got != 0 {
		t.Fatalf("after disarm SuppressedAll[5]=%d, want 0", got)
	}
}

// An armed project with sustained matching agent activity while the user is
// present must auto-disarm after AutoDisarmAgentTicks ticks and start counting
// — this is the self-heal that prevents permanently-stuck arming when title
// capture (the normal focus-disarm path) is unavailable.
func TestPipeline_ArmedProject_AutoDisarmsAfterSustainedAgentActivity(t *testing.T) {
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:     120,
		CPUDeltaThresh:       5,
		CPUDeltaThreshIdle:   5,
		AutoDisarmAgentTicks: 3,
	})
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "sticky", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	bin := "claude"
	cwd := "/Users/rian/work/sticky"
	cache.Store(&CacheSnapshot{
		AllowedBinaries: map[string]bool{bin: true},
		Rules:           []domain.RuleSpec{{ID: 1, ProjectID: projID, Priority: 100, MatchBinaryName: &bin, MatchCwdPrefix: &cwd}},
		CwdPrefixes:     []string{cwd},
		ArmedProjects:   map[int64]bool{projID: true},
	})
	br.IdleSecondsVal = 5 // user present
	br.CWDByPID = map[int]string{999: cwd + "/src"}

	// CPU climbs 100 each tick. Tick @1000 establishes prevCPU (no signal).
	// Ticks @1005/@1010/@1015 each produce an agent signal: streak 1,2,3.
	// On the 3rd (threshold) the project auto-disarms and that tick lands.
	for _, s := range []struct {
		ts  int64
		cpu uint64
	}{{1000, 100}, {1005, 200}, {1010, 300}, {1015, 400}} {
		br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: s.cpu}}
		if err := p.RunTick(ctx, s.ts); err != nil {
			t.Fatalf("RunTick @%d: %v", s.ts, err)
		}
	}

	if cache.Snapshot().ArmedProjects[projID] {
		t.Errorf("project should be auto-disarmed after sustained activity, still armed: %v", cache.Snapshot().ArmedProjects)
	}
	var nTicks int
	db.QueryRow(`SELECT COUNT(*) FROM ticks WHERE project_id=?`, projID).Scan(&nTicks)
	if nTicks != 1 {
		t.Errorf("expected exactly 1 agent tick (the auto-disarming tick), got %d", nTicks)
	}
}

// When the user is idle, sustained agent activity must NOT auto-disarm — idle
// activity is exactly the stale/overnight case arming exists to gate. The
// suppressed ticks are still counted so status can surface them.
func TestPipeline_ArmedProject_NoAutoDisarmWhenIdle_ButTracksSuppressed(t *testing.T) {
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:     120,
		CPUDeltaThresh:       5,
		CPUDeltaThreshIdle:   5,
		AutoDisarmAgentTicks: 3,
	})
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "idle-sticky", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	bin := "claude"
	cwd := "/Users/rian/work/idle-sticky"
	cache.Store(&CacheSnapshot{
		AllowedBinaries: map[string]bool{bin: true},
		Rules:           []domain.RuleSpec{{ID: 1, ProjectID: projID, Priority: 100, MatchBinaryName: &bin, MatchCwdPrefix: &cwd}},
		CwdPrefixes:     []string{cwd},
		ArmedProjects:   map[int64]bool{projID: true},
	})
	br.IdleSecondsVal = 200 // user idle
	br.CWDByPID = map[int]string{999: cwd + "/src"}

	// 1 prevCPU tick + 3 active ticks (well past the threshold).
	for _, s := range []struct {
		ts  int64
		cpu uint64
	}{{1000, 100}, {1005, 200}, {1010, 300}, {1015, 400}} {
		br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: s.cpu}}
		if err := p.RunTick(ctx, s.ts); err != nil {
			t.Fatalf("RunTick @%d: %v", s.ts, err)
		}
	}

	if !cache.Snapshot().ArmedProjects[projID] {
		t.Errorf("idle activity must not auto-disarm; project should still be armed")
	}
	var nTicks int
	db.QueryRow(`SELECT COUNT(*) FROM ticks WHERE project_id=?`, projID).Scan(&nTicks)
	if nTicks != 0 {
		t.Errorf("armed+idle should drop all agent ticks, got %d", nTicks)
	}
	if got := cache.SuppressedAll()[projID]; got != 3 {
		t.Errorf("expected 3 suppressed ticks tracked for visibility, got %d", got)
	}
}
