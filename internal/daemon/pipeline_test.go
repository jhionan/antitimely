package daemon

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/rian/antitimely/internal/domain"
	"github.com/rian/antitimely/internal/macos"
	"github.com/rian/antitimely/internal/store"
)

func loadSchema(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../schema.sql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	return string(b)
}

func newTestPipelineWithCfg(t *testing.T, cfg PipelineConfig) (*Pipeline, *macos.FakeBridge, *Cache, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(loadSchema(t)); err != nil {
		t.Fatalf("schema: %v", err)
	}
	q := store.New(db)
	br := &macos.FakeBridge{CWDByPID: map[int]string{}}
	cache := NewCache()
	p := NewPipeline(q, br, cache, cfg)
	return p, br, cache, db
}

func newTestPipeline(t *testing.T) (*Pipeline, *macos.FakeBridge, *Cache, *sql.DB) {
	t.Helper()
	return newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     5,
		CPUDeltaThreshIdle: 5, // same as active to keep existing test behaviour
	})
}

func TestPipeline_NoSignals_WritesNothing(t *testing.T) {
	p, br, _, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	br.IdleSecondsVal = 200 // user idle
	br.Processes = nil

	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick: %v", err)
	}

	var n int
	row := db.QueryRow(`SELECT COUNT(*) FROM ticks`)
	if err := row.Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 ticks, got %d", n)
	}
}

func TestPipeline_FocusSignal_WritesUnassignedTick(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	cache.Store(&CacheSnapshot{
		AllowedBundles: map[string]bool{"com.google.antigravity": true},
	})
	br.IdleSecondsVal = 5 // user present
	br.FrontmostInfoVal = macos.FrontmostInfo{BundleID: "com.google.antigravity", PID: 1234}
	br.FocusedTitle = "foca-api — main — Antigravity"

	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ticks WHERE project_id IS NULL`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 unassigned tick, got %d", n)
	}
}

func TestPipeline_AgentSignal_CountsOnlyWhenActive(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	cache.Store(&CacheSnapshot{
		AllowedBinaries: map[string]bool{"claude": true},
	})
	br.IdleSecondsVal = 200 // user idle — focus signal won't fire
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 100}}
	br.CWDByPID = map[int]string{999: "/Users/rian/work/foca-api/src"}

	// First tick: no prevCPU, should not emit.
	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick #1: %v", err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 0 {
		t.Errorf("first tick should not emit (no prev sample), got %d ticks", n)
	}

	// Second tick: CPU delta = 50 (above threshold of 5) → emit.
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 150}}
	if err := p.RunTick(ctx, 1005); err != nil {
		t.Fatalf("RunTick #2: %v", err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 1 {
		t.Errorf("second tick should emit 1, got %d", n)
	}

	// Third tick: CPU delta = 0 → no emit.
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 150}}
	if err := p.RunTick(ctx, 1010); err != nil {
		t.Fatalf("RunTick #3: %v", err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 1 {
		t.Errorf("third tick (idle) should not emit; total still 1, got %d", n)
	}
}

func TestPipeline_AgentSignal_IdleThresholdHigherWhenUserIdle(t *testing.T) {
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     5,   // low when active
		CPUDeltaThreshIdle: 100, // high when idle
	})
	defer db.Close()
	ctx := context.Background()

	cache.Store(&CacheSnapshot{
		AllowedBinaries: map[string]bool{"claude": true},
	})

	// --- Scenario: user is idle, claude burns just 10 centi (below idle threshold) ---
	br.IdleSecondsVal = 200 // idle
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 1000}}
	br.CWDByPID = map[int]string{999: "/work/x"}
	_ = p.RunTick(ctx, 1000) // establish prev
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 1010}}
	_ = p.RunTick(ctx, 1005) // delta 10 < idle threshold 100, no emit

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 0 {
		t.Errorf("idle user + low CPU should emit 0 ticks, got %d", n)
	}

	// --- Scenario: user is idle, claude burns 200 centi (above idle threshold) ---
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 1210}}
	_ = p.RunTick(ctx, 1010) // delta 200 >= idle threshold 100, emit

	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 1 {
		t.Errorf("idle user + high CPU should emit 1 tick, got %d total", n)
	}

	// --- Scenario: user becomes active, claude back to low CPU ---
	br.IdleSecondsVal = 5 // active
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 1220}}
	_ = p.RunTick(ctx, 1015) // delta 10 >= active threshold 5, emit

	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 2 {
		t.Errorf("active user + low CPU should emit 1 more tick; total %d, want 2", n)
	}
}

func TestPipeline_AgentSignal_TaggedByRule(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "foca-api", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("add project: %v", err)
	}
	bin := "claude"
	cwd := "/Users/rian/work/foca-api/"
	cache.Store(&CacheSnapshot{
		AllowedBinaries: map[string]bool{"claude": true},
		Rules: []domain.RuleSpec{
			{ID: 1, ProjectID: projID, Priority: 100,
				MatchBinaryName: &bin, MatchCwdPrefix: &cwd},
		},
	})
	br.IdleSecondsVal = 200
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 100}}
	br.CWDByPID = map[int]string{999: "/Users/rian/work/foca-api/src"}
	_ = p.RunTick(ctx, 1000)

	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 200}}
	_ = p.RunTick(ctx, 1005)

	rows, _ := q.TotalsByProject(ctx, store.TotalsByProjectParams{Ts: 0, Ts_2: 9999})
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d (%+v)", len(rows), rows)
	}
	if rows[0].Name != "foca-api" || rows[0].TickCount != 1 {
		t.Errorf("got %+v", rows[0])
	}
}

func TestPipeline_IdleError_FailsOpen(t *testing.T) {
	// When IdleSeconds returns an error the pipeline should fail open
	// (treat the user as present) so focus tracking continues. The
	// alternative ("treat as idle") silently flips the agent CPU threshold
	// to a 20x stricter bar and drops focus signals entirely — a silent
	// degradation. The error is logged + surfaced via the permission
	// tracker so a human can investigate.
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	pt := NewPermissionTracker()
	p.SetPermissionTracker(pt)
	ctx := context.Background()

	cache.Store(&CacheSnapshot{
		AllowedBundles: map[string]bool{"com.example.app": true},
	})
	br.FrontmostInfoVal = macos.FrontmostInfo{BundleID: "com.example.app", PID: 1}
	br.FocusedTitle = "some window"
	br.IdleErr = errors.New("boom")

	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 tick (fail-open keeps tracking), got %d", n)
	}
	// The Frontmost call succeeded after the idle failure, so the focus
	// signal handler overwrites permission state back to "ok" — exercising
	// the recovery path. The important thing is that focus tracking ran
	// at all.
	if got := pt.Get(); got != "ok" {
		t.Errorf("permission state = %q, want ok (post-success)", got)
	}
}

func TestPipeline_IdleError_SurfacedWhenFrontmostFails(t *testing.T) {
	// When idle fails AND the subsequent Frontmost call fails, the
	// idle-failed state should remain visible. Catches the case where
	// a regression silently re-overwrites the permission state.
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	pt := NewPermissionTracker()
	p.SetPermissionTracker(pt)
	ctx := context.Background()

	cache.Store(&CacheSnapshot{})
	br.IdleErr = errors.New("ioreg boom")
	br.FrontmostErr = errors.New("appkit boom")

	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if got := pt.Get(); got != "idle_detection_failed" {
		t.Errorf("permission state = %q, want idle_detection_failed", got)
	}
}

func TestPipeline_AgentSignal_StalePIDsPrunedOnPSFailure(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	cache.Store(&CacheSnapshot{
		AllowedBinaries: map[string]bool{"claude": true},
	})
	br.IdleSecondsVal = 5 // user present
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 100}}
	br.CWDByPID = map[int]string{999: "/work/x"}

	// Establish prevCPU entry via a successful tick.
	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick #1: %v", err)
	}
	if _, ok := p.prevCPU[999]; !ok {
		t.Fatal("expected prevCPU[999] to be set after first tick")
	}

	// Now make ListProcesses fail.
	br.ProcessesErr = errors.New("ps failed")

	if err := p.RunTick(ctx, 1005); err != nil {
		t.Fatalf("RunTick #2: %v", err)
	}

	// The defer in collectAgentSignals should have pruned all stale PIDs.
	if len(p.prevCPU) != 0 {
		t.Errorf("expected prevCPU to be empty after ps failure, got %d entries", len(p.prevCPU))
	}
}

// Non-allowlisted binary running in a directory covered by a rule's
// cwd_prefix should still be tracked and attributed to that project. This is
// the bug-driven case: ad-hoc tools (go, bun, make, …) shouldn't be invisible
// just because the user hasn't pre-registered every binary they ever use.
func TestPipeline_AgentSignal_CwdRuleWidensBinaryAllowlist(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "dentix", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("add project: %v", err)
	}
	cwdPrefix := "/Users/rian/focaApp/dentix/"
	cache.Store(&CacheSnapshot{
		// Note: 'go' is NOT in AllowedBinaries.
		AllowedBinaries: map[string]bool{},
		Rules: []domain.RuleSpec{
			{ID: 1, ProjectID: projID, Priority: 100, MatchCwdPrefix: &cwdPrefix},
		},
		CwdPrefixes: []string{"/Users/rian/focaApp/dentix"},
	})
	br.IdleSecondsVal = 5
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "go", CPUTicks: 100}}
	br.CWDByPID = map[int]string{999: "/Users/rian/focaApp/dentix/dentix-flutter-app"}
	_ = p.RunTick(ctx, 1000) // establish prevCPU

	br.Processes = []macos.ProcessSample{{PID: 999, Name: "go", CPUTicks: 200}}
	_ = p.RunTick(ctx, 1005)

	rows, _ := q.TotalsByProject(ctx, store.TotalsByProjectParams{Ts: 0, Ts_2: 9999})
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d (%+v)", len(rows), rows)
	}
	if rows[0].Name != "dentix" || rows[0].TickCount != 1 {
		t.Errorf("got %+v, want dentix/1", rows[0])
	}
}

// A non-allowlisted binary whose cwd matches no rule must remain invisible
// — i.e. the widening doesn't degenerate into "track everything".
func TestPipeline_AgentSignal_NonAllowlistedOutsideTrackedDir_Skipped(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	cache.Store(&CacheSnapshot{
		AllowedBinaries: map[string]bool{},
		CwdPrefixes:     []string{"/Users/rian/work"},
	})
	br.IdleSecondsVal = 5
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "ffmpeg", CPUTicks: 100}}
	br.CWDByPID = map[int]string{999: "/tmp"}
	_ = p.RunTick(ctx, 1000)

	br.Processes = []macos.ProcessSample{{PID: 999, Name: "ffmpeg", CPUTicks: 5000}}
	_ = p.RunTick(ctx, 1005)

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 0 {
		t.Errorf("ffmpeg in /tmp should not be tracked; got %d ticks", n)
	}
}

// Once a PID is classified as "ignored", subsequent ticks must skip it
// without re-running the cwd lookup. The lsof bridge call is expensive in
// real life; the cache is the whole point of this design.
func TestPipeline_AgentSignal_IgnoredPID_DoesNotRelsof(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	cache.Store(&CacheSnapshot{
		AllowedBinaries: map[string]bool{},
		CwdPrefixes:     []string{"/work/tracked"},
	})
	br.IdleSecondsVal = 5
	br.Processes = []macos.ProcessSample{{PID: 42, Name: "ffmpeg", CPUTicks: 100}}
	br.CWDByPID = map[int]string{42: "/tmp"}
	_ = p.RunTick(ctx, 1000)
	// Threshold crossing on tick 2 → classify as ignored.
	br.Processes = []macos.ProcessSample{{PID: 42, Name: "ffmpeg", CPUTicks: 200}}
	_ = p.RunTick(ctx, 1005)

	cls, ok := p.procClass[42]
	if !ok {
		t.Fatal("expected procClass[42] to be populated after threshold crossing")
	}
	if cls.track {
		t.Errorf("procClass[42].track = true, want false (cwd not in tracked prefix)")
	}

	// Now make cwd lookup fail loudly. If the pipeline tries lsof again,
	// the test will detect a tick was attempted past the cache layer.
	br.CWDErr = errors.New("lsof must not be called for cached-ignored PID")
	br.Processes = []macos.ProcessSample{{PID: 42, Name: "ffmpeg", CPUTicks: 300}}
	if err := p.RunTick(ctx, 1010); err != nil {
		t.Fatalf("RunTick: %v", err)
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 0 {
		t.Errorf("ignored PID should produce 0 ticks across all runs; got %d", n)
	}
}

// PID reuse with a new binary name must invalidate the cached classification
// so the replacement process gets evaluated afresh.
func TestPipeline_AgentSignal_PIDReuse_BinaryNameChange_Reclassifies(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "dentix", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("add project: %v", err)
	}
	cwd := "/work/dentix/"
	cache.Store(&CacheSnapshot{
		AllowedBinaries: map[string]bool{},
		Rules: []domain.RuleSpec{
			{ID: 1, ProjectID: projID, Priority: 100, MatchCwdPrefix: &cwd},
		},
		CwdPrefixes: []string{"/work/dentix"},
	})
	br.IdleSecondsVal = 5

	// Phase 1: PID 100 = ffmpeg in /tmp → classified as ignored.
	br.Processes = []macos.ProcessSample{{PID: 100, Name: "ffmpeg", CPUTicks: 100}}
	br.CWDByPID = map[int]string{100: "/tmp"}
	_ = p.RunTick(ctx, 1000)
	br.Processes = []macos.ProcessSample{{PID: 100, Name: "ffmpeg", CPUTicks: 200}}
	_ = p.RunTick(ctx, 1005)
	if cls := p.procClass[100]; cls.track {
		t.Fatalf("phase 1: expected ffmpeg/PID 100 to be classified !track, got %+v", cls)
	}

	// Phase 2: PID 100 reused — same pid, different binary, fresh CPU counter
	// (lower than what ffmpeg had). The CPU regression is what bumps prev down;
	// the name change is what invalidates the cache entry.
	br.Processes = []macos.ProcessSample{{PID: 100, Name: "go", CPUTicks: 10}}
	br.CWDByPID = map[int]string{100: "/work/dentix/dentix-flutter-app"}
	_ = p.RunTick(ctx, 1010) // CPU regressed: invalidate, skip emit.
	br.Processes = []macos.ProcessSample{{PID: 100, Name: "go", CPUTicks: 110}}
	_ = p.RunTick(ctx, 1015) // delta=100 ≥ threshold: classify fresh as tracked.

	cls, ok := p.procClass[100]
	if !ok {
		t.Fatal("phase 2: expected procClass[100] to be repopulated after reuse")
	}
	if cls.name != "go" {
		t.Errorf("phase 2: cached name = %q, want %q", cls.name, "go")
	}
	if !cls.track {
		t.Errorf("phase 2: reused PID with tracked cwd should classify as track=true")
	}

	rows, _ := q.TotalsByProject(ctx, store.TotalsByProjectParams{Ts: 0, Ts_2: 9999})
	if len(rows) != 1 || rows[0].Name != "dentix" || rows[0].TickCount != 1 {
		t.Errorf("expected 1 dentix tick from reused PID; got %+v", rows)
	}
}

// PID reuse where the new process happens to share a binary name with the
// old one is detected via the CPU regression check alone.
func TestPipeline_AgentSignal_PIDReuse_CPURegression_Reclassifies(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	cache.Store(&CacheSnapshot{
		AllowedBinaries: map[string]bool{},
		CwdPrefixes:     []string{"/work/tracked"},
	})
	br.IdleSecondsVal = 5

	// Phase 1: PID 50 = zsh in /tmp → ignored, CPU rises to 500.
	br.Processes = []macos.ProcessSample{{PID: 50, Name: "zsh", CPUTicks: 400}}
	br.CWDByPID = map[int]string{50: "/tmp"}
	_ = p.RunTick(ctx, 1000)
	br.Processes = []macos.ProcessSample{{PID: 50, Name: "zsh", CPUTicks: 500}}
	_ = p.RunTick(ctx, 1005)
	if _, ok := p.procClass[50]; !ok {
		t.Fatal("phase 1: expected zsh to be classified")
	}

	// Phase 2: PID 50 reused as a new zsh inside the tracked dir. CPU starts
	// fresh at 0; the regression triggers cache invalidation.
	br.Processes = []macos.ProcessSample{{PID: 50, Name: "zsh", CPUTicks: 0}}
	br.CWDByPID = map[int]string{50: "/work/tracked/repo"}
	_ = p.RunTick(ctx, 1010)
	if _, ok := p.procClass[50]; ok {
		t.Errorf("phase 2: CPU regression should have evicted procClass[50]")
	}

	// Phase 3: new zsh accumulates CPU; reclassifies as tracked via cwd rule.
	br.Processes = []macos.ProcessSample{{PID: 50, Name: "zsh", CPUTicks: 100}}
	_ = p.RunTick(ctx, 1015)
	cls, ok := p.procClass[50]
	if !ok {
		t.Fatal("phase 3: expected fresh classification after threshold crossing")
	}
	if !cls.track {
		t.Errorf("phase 3: new zsh in tracked dir should classify as track=true; got %+v", cls)
	}
}

// Swapping the cache snapshot (i.e. ReloadCache after a rule/watch mutation)
// must drop classifications so a long-running process gets re-evaluated
// against the new rules. Otherwise adding a cwd rule wouldn't retroactively
// catch processes that the daemon already decided were "ignored".
func TestPipeline_CacheSnapshotSwap_InvalidatesClassifications(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	// Initial cache: no rules; ffmpeg in /work/dentix is ignored.
	cache.Store(&CacheSnapshot{
		AllowedBinaries: map[string]bool{},
		CwdPrefixes:     nil,
	})
	br.IdleSecondsVal = 5
	br.Processes = []macos.ProcessSample{{PID: 7, Name: "ffmpeg", CPUTicks: 100}}
	br.CWDByPID = map[int]string{7: "/work/dentix/x"}
	_ = p.RunTick(ctx, 1000)
	br.Processes = []macos.ProcessSample{{PID: 7, Name: "ffmpeg", CPUTicks: 200}}
	_ = p.RunTick(ctx, 1005)
	if cls := p.procClass[7]; cls.track {
		t.Fatalf("pre-swap: expected ignored; got %+v", cls)
	}

	// Swap in a snapshot that adds the cwd prefix. The pipeline should
	// notice the pointer change and clear procClass.
	cache.Store(&CacheSnapshot{
		AllowedBinaries: map[string]bool{},
		CwdPrefixes:     []string{"/work/dentix"},
	})
	br.Processes = []macos.ProcessSample{{PID: 7, Name: "ffmpeg", CPUTicks: 300}}
	_ = p.RunTick(ctx, 1010)

	cls, ok := p.procClass[7]
	if !ok {
		t.Fatal("post-swap: expected procClass[7] to be re-populated against the new snapshot")
	}
	if !cls.track {
		t.Errorf("post-swap: expected ffmpeg to be re-classified as tracked; got %+v", cls)
	}
}

// Agent CPU activity in a paused project's directory WHILE THE USER IS PRESENT
// should auto-resume the project (flip the DB flag + clear the in-memory pause
// entry), arm it, and drop the current tick. The tick is dropped because the
// project is armed on resume — the agent must wait for a focus disarm before
// ticks land again. Presence is required: idle background CPU must not resume
// (see TestPipeline_PausedProject_AgentSignal_UserIdle_StaysPaused). Focus
// signals don't get auto-resume treatment — see the companion test below.
func TestPipeline_PausedProject_AgentSignal_AutoResumes(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "paused-proj", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := q.SetProjectPaused(ctx, store.SetProjectPausedParams{Paused: 1, Name: "paused-proj"}); err != nil {
		t.Fatalf("pause: %v", err)
	}

	bin := "claude"
	cwd := "/work/paused-proj/"
	cache.Store(&CacheSnapshot{
		AllowedBinaries:  map[string]bool{"claude": true},
		Rules:            []domain.RuleSpec{{ID: 1, ProjectID: projID, Priority: 100, MatchBinaryName: &bin, MatchCwdPrefix: &cwd}},
		PausedProjectIDs: map[int64]bool{projID: true},
		ArmedProjects:    map[int64]bool{},
	})
	br.IdleSecondsVal = 5 // user present — required for auto-resume
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 100}}
	br.CWDByPID = map[int]string{999: "/work/paused-proj/x"}
	_ = p.RunTick(ctx, 1000) // establish prevCPU

	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 200}}
	_ = p.RunTick(ctx, 1005)

	// Auto-resume arms the project and drops the tick — 0 ticks on resume tick.
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 0 {
		t.Errorf("auto-resume should drop the resume tick (armed); got %d ticks", n)
	}

	// DB-level pause flag flipped.
	var paused int64
	db.QueryRow(`SELECT paused FROM projects WHERE id=?`, projID).Scan(&paused)
	if paused != 0 {
		t.Errorf("project.paused should be 0 after auto-resume; got %d", paused)
	}

	// In-memory cache snapshot updated too — next tick must not see the
	// project as paused.
	if cache.Snapshot().PausedProjectIDs[projID] {
		t.Errorf("cache snapshot should no longer flag project as paused")
	}

	// Project is armed so agent ticks wait for a focus disarm.
	if !cache.Snapshot().ArmedProjects[projID] {
		t.Errorf("auto-resumed project should be armed; got %v", cache.Snapshot().ArmedProjects)
	}
}

// Agent CPU in a paused project's directory while the user is IDLE must NOT
// auto-resume the project. This is the overnight-accrual bug: unattended
// background processes (claude, dev servers, language servers, builds) burn
// CPU in tracked dirs while the user is away, and auto-resume silently
// un-paused the project so it billed around the clock. Resume requires the
// user to actually be present.
func TestPipeline_PausedProject_AgentSignal_UserIdle_StaysPaused(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "paused-proj", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := q.SetProjectPaused(ctx, store.SetProjectPausedParams{Paused: 1, Name: "paused-proj"}); err != nil {
		t.Fatalf("pause: %v", err)
	}

	bin := "claude"
	cwd := "/work/paused-proj/"
	cache.Store(&CacheSnapshot{
		AllowedBinaries:  map[string]bool{"claude": true},
		Rules:            []domain.RuleSpec{{ID: 1, ProjectID: projID, Priority: 100, MatchBinaryName: &bin, MatchCwdPrefix: &cwd}},
		PausedProjectIDs: map[int64]bool{projID: true},
		ArmedProjects:    map[int64]bool{},
	})
	br.IdleSecondsVal = 200 // user idle — the whole point: background CPU must not resurrect a paused project
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 100}}
	br.CWDByPID = map[int]string{999: "/work/paused-proj/x"}
	_ = p.RunTick(ctx, 1000) // establish prevCPU

	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 200}}
	_ = p.RunTick(ctx, 1005)

	// No ticks counted — the project is paused and the user is away.
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 0 {
		t.Errorf("paused project + idle user should write no ticks; got %d", n)
	}
	// DB flag stays paused.
	var paused int64
	db.QueryRow(`SELECT paused FROM projects WHERE id=?`, projID).Scan(&paused)
	if paused != 1 {
		t.Errorf("project.paused should remain 1 (no resume while idle); got %d", paused)
	}
	// Cache snapshot still flags it paused.
	if !cache.Snapshot().PausedProjectIDs[projID] {
		t.Errorf("cache snapshot should still flag project paused after idle agent activity")
	}
}

func TestPipeline_FocusSignal_DisarmsProject(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "armed-proj", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	bundle := "com.example.editor"
	title := "armed-proj"
	cache.Store(&CacheSnapshot{
		AllowedBundles:   map[string]bool{bundle: true},
		AllowedBinaries:  map[string]bool{},
		Rules:            []domain.RuleSpec{{ID: 1, ProjectID: projID, Priority: 100, MatchBundleID: &bundle, MatchTitleSubstr: &title}},
		PausedProjectIDs: map[int64]bool{},
		ArmedProjects:    map[int64]bool{projID: true},
	})
	br.IdleSecondsVal = 5
	br.FrontmostInfoVal = macos.FrontmostInfo{BundleID: bundle, PID: 1234}
	br.FocusedTitle = "armed-proj — main"

	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick: %v", err)
	}

	// Focus tick should land.
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks WHERE project_id=?`, projID).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 focus tick, got %d", n)
	}
	// Project should be disarmed afterward.
	if cache.Snapshot().ArmedProjects[projID] {
		t.Errorf("project should be disarmed after focus signal, still armed: %v", cache.Snapshot().ArmedProjects)
	}
}

// Focus signal alone is too weak to imply "user is working again" — an open
// window in the background shouldn't override an explicit pause. The
// observation is still upserted so it remains visible in review history.
func TestPipeline_PausedProject_FocusSignal_StillSkipped(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "paused-proj", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := q.SetProjectPaused(ctx, store.SetProjectPausedParams{Paused: 1, Name: "paused-proj"}); err != nil {
		t.Fatalf("pause: %v", err)
	}

	bundle := "com.example.app"
	title := "paused-proj"
	cache.Store(&CacheSnapshot{
		AllowedBundles:   map[string]bool{bundle: true},
		Rules:            []domain.RuleSpec{{ID: 1, ProjectID: projID, Priority: 100, MatchBundleID: &bundle, MatchTitleSubstr: &title}},
		PausedProjectIDs: map[int64]bool{projID: true},
	})
	br.IdleSecondsVal = 5
	br.FrontmostInfoVal = macos.FrontmostInfo{BundleID: bundle, PID: 1}
	br.FocusedTitle = "paused-proj — main"
	_ = p.RunTick(ctx, 1000)

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 0 {
		t.Errorf("focus signal on paused project should write no ticks; got %d", n)
	}
	var paused int64
	db.QueryRow(`SELECT paused FROM projects WHERE id=?`, projID).Scan(&paused)
	if paused != 1 {
		t.Errorf("project.paused should remain 1 after focus-only activity; got %d", paused)
	}
	// Observation still upserted for review history.
	var obsCount int
	db.QueryRow(`SELECT COUNT(*) FROM observations WHERE source='focus'`).Scan(&obsCount)
	if obsCount == 0 {
		t.Errorf("expected focus observation to be upserted even for paused project")
	}
}

func TestPipeline_FocusSignal_DoesNotDisarmOtherProjects(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	idA, err := q.AddProject(ctx, store.AddProjectParams{Name: "alpha", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("AddProject alpha: %v", err)
	}
	idB, err := q.AddProject(ctx, store.AddProjectParams{Name: "beta", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("AddProject beta: %v", err)
	}
	bundle := "com.example.editor"
	titleA := "alpha"
	cache.Store(&CacheSnapshot{
		AllowedBundles:   map[string]bool{bundle: true},
		AllowedBinaries:  map[string]bool{},
		Rules:            []domain.RuleSpec{{ID: 1, ProjectID: idA, Priority: 100, MatchBundleID: &bundle, MatchTitleSubstr: &titleA}},
		PausedProjectIDs: map[int64]bool{},
		ArmedProjects:    map[int64]bool{idA: true, idB: true},
	})
	br.IdleSecondsVal = 5
	br.FrontmostInfoVal = macos.FrontmostInfo{BundleID: bundle, PID: 1234}
	br.FocusedTitle = "alpha — main"

	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick: %v", err)
	}

	got := cache.Snapshot().ArmedProjects
	if got[idA] {
		t.Errorf("expected alpha disarmed, still armed: %v", got)
	}
	if !got[idB] {
		t.Errorf("expected beta still armed (focus was for alpha), got %v", got)
	}
}

func TestPipeline_AgentSignal_Armed_DropsTick(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "gated", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	bin := "claude"
	cwdPrefix := "/Users/rian/work/gated"
	cache.Store(&CacheSnapshot{
		AllowedBundles:   map[string]bool{},
		AllowedBinaries:  map[string]bool{bin: true},
		Rules:            []domain.RuleSpec{{ID: 1, ProjectID: projID, Priority: 100, MatchBinaryName: &bin, MatchCwdPrefix: &cwdPrefix}},
		CwdPrefixes:      []string{cwdPrefix},
		PausedProjectIDs: map[int64]bool{},
		ArmedProjects:    map[int64]bool{projID: true},
	})
	br.IdleSecondsVal = 200 // no focus signal possible
	br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: 100}}
	br.CWDByPID = map[int]string{999: cwdPrefix + "/src"}

	// First tick: establish prev CPU sample.
	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick #1: %v", err)
	}
	// Second tick: CPU delta over threshold ⇒ agent signal would fire,
	// but project is armed so the tick must be dropped.
	br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: 200}}
	if err := p.RunTick(ctx, 1005); err != nil {
		t.Fatalf("RunTick #2: %v", err)
	}

	var nTicks int
	db.QueryRow(`SELECT COUNT(*) FROM ticks WHERE project_id=?`, projID).Scan(&nTicks)
	if nTicks != 0 {
		t.Errorf("expected 0 ticks for armed project, got %d", nTicks)
	}

	var nObs int
	db.QueryRow(`SELECT COUNT(*) FROM observations WHERE source='agent' AND binary_name=?`, bin).Scan(&nObs)
	if nObs == 0 {
		t.Errorf("expected observation row preserved (history), got %d", nObs)
	}
}

func TestPipeline_AgentSignal_Unarmed_CountsNormally(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "open", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	bin := "claude"
	cwdPrefix := "/Users/rian/work/open"
	cache.Store(&CacheSnapshot{
		AllowedBundles:   map[string]bool{},
		AllowedBinaries:  map[string]bool{bin: true},
		Rules:            []domain.RuleSpec{{ID: 1, ProjectID: projID, Priority: 100, MatchBinaryName: &bin, MatchCwdPrefix: &cwdPrefix}},
		CwdPrefixes:      []string{cwdPrefix},
		PausedProjectIDs: map[int64]bool{},
		ArmedProjects:    map[int64]bool{}, // NOT armed
	})
	br.IdleSecondsVal = 200
	br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: 100}}
	br.CWDByPID = map[int]string{999: cwdPrefix + "/src"}

	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick #1: %v", err)
	}
	br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: 200}}
	if err := p.RunTick(ctx, 1005); err != nil {
		t.Fatalf("RunTick #2: %v", err)
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks WHERE project_id=?`, projID).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 tick for unarmed project, got %d", n)
	}
}

func TestPipeline_AutoResume_ArmsAndDropsCurrentTick(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "paused-arm", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if err := q.SetProjectPaused(ctx, store.SetProjectPausedParams{Paused: 1, Name: "paused-arm"}); err != nil {
		t.Fatalf("SetProjectPaused: %v", err)
	}

	bin := "claude"
	cwdPrefix := "/Users/rian/work/paused-arm"
	cache.Store(&CacheSnapshot{
		AllowedBundles:   map[string]bool{},
		AllowedBinaries:  map[string]bool{bin: true},
		Rules:            []domain.RuleSpec{{ID: 1, ProjectID: projID, Priority: 100, MatchBinaryName: &bin, MatchCwdPrefix: &cwdPrefix}},
		CwdPrefixes:      []string{cwdPrefix},
		PausedProjectIDs: map[int64]bool{projID: true},
		ArmedProjects:    map[int64]bool{},
	})
	br.IdleSecondsVal = 5 // user present — required for auto-resume
	br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: 100}}
	br.CWDByPID = map[int]string{999: cwdPrefix + "/src"}

	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick #1: %v", err)
	}
	br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: 200}}
	if err := p.RunTick(ctx, 1005); err != nil {
		t.Fatalf("RunTick #2: %v", err)
	}

	// Project should be unpaused in DB.
	var paused int
	db.QueryRow(`SELECT paused FROM projects WHERE id=?`, projID).Scan(&paused)
	if paused != 0 {
		t.Errorf("expected project auto-resumed (paused=0), got %d", paused)
	}
	// And in cache.
	if cache.Snapshot().PausedProjectIDs[projID] {
		t.Errorf("expected project not in PausedProjectIDs after auto-resume")
	}
	// But armed.
	if !cache.Snapshot().ArmedProjects[projID] {
		t.Errorf("auto-resumed project should be armed, got %v", cache.Snapshot().ArmedProjects)
	}
	// And no agent tick for this round.
	var nTicks int
	db.QueryRow(`SELECT COUNT(*) FROM ticks WHERE project_id=?`, projID).Scan(&nTicks)
	if nTicks != 0 {
		t.Errorf("expected 0 ticks (gate drops auto-resumed agent signal), got %d", nTicks)
	}
}

func TestPipeline_Busy_RiseRequiresSustainedTicks(t *testing.T) {
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     15,
		CPUDeltaThreshIdle: 100,
		AgentBusyRiseTicks: 2,
		AgentBusyFallTicks: 3,
	})
	defer db.Close()
	ctx := context.Background()
	cache.Store(&CacheSnapshot{AllowedBinaries: map[string]bool{"claude": true}})
	br.IdleSecondsVal = 5 // present → busy bar 15
	br.CWDByPID = map[int]string{999: "/work/x"}

	// Tick 1: establish prev (no delta yet).
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 0}}
	_ = p.RunTick(ctx, 1000)
	// Tick 2: delta 30 ≥ 15 → aboveStreak=1, not yet working (riseTicks=2).
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 30}}
	_ = p.RunTick(ctx, 1005)
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 0 {
		t.Fatalf("after 1 above-bar tick, expected 0 emits (rise not met), got %d", n)
	}
	// Tick 3: delta 30 ≥ 15 → aboveStreak=2 → working → emit.
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 60}}
	_ = p.RunTick(ctx, 1010)
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 1 {
		t.Fatalf("after 2 sustained above-bar ticks, expected 1 emit, got %d", n)
	}
}

func TestPipeline_Busy_SingleBlipNeverFlips(t *testing.T) {
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     15,
		CPUDeltaThreshIdle: 100,
		AgentBusyRiseTicks: 2,
		AgentBusyFallTicks: 3,
	})
	defer db.Close()
	ctx := context.Background()
	cache.Store(&CacheSnapshot{AllowedBinaries: map[string]bool{"claude": true}})
	br.IdleSecondsVal = 5
	br.CWDByPID = map[int]string{999: "/work/x"}

	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 0}}
	_ = p.RunTick(ctx, 1000) // prev
	// One blip above, then quiet.
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 40}}
	_ = p.RunTick(ctx, 1005) // aboveStreak=1
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 42}}
	_ = p.RunTick(ctx, 1010) // delta 2 < 15 → belowStreak resets above
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 44}}
	_ = p.RunTick(ctx, 1015) // delta 2 < 15

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 0 {
		t.Errorf("a single above-bar blip must never reach working; got %d emits", n)
	}
}

func TestPipeline_Busy_FallHysteresisBridgesDips(t *testing.T) {
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     15,
		CPUDeltaThreshIdle: 100,
		AgentBusyRiseTicks: 2,
		AgentBusyFallTicks: 3,
	})
	defer db.Close()
	ctx := context.Background()
	cache.Store(&CacheSnapshot{AllowedBinaries: map[string]bool{"claude": true}})
	br.IdleSecondsVal = 5
	br.CWDByPID = map[int]string{999: "/work/x"}

	// Drive to working: prev + 2 sustained above-bar ticks.
	seq := []uint64{0, 30, 60} // deltas: -, 30, 30
	ts := int64(1000)
	for _, c := range seq {
		br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: c}}
		_ = p.RunTick(ctx, ts)
		ts += 5
	}
	var afterRise int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&afterRise)
	if afterRise != 1 {
		t.Fatalf("expected working+1 emit after rise, got %d", afterRise)
	}

	// One dip below bar (delta 2) then back above (delta 30). fallTicks=3, so a
	// single dip must NOT drop working — the dip tick itself still emits.
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 62}}
	_ = p.RunTick(ctx, ts) // delta 2 < 15 → belowStreak=1, still working → emit
	ts += 5
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 92}}
	_ = p.RunTick(ctx, ts) // delta 30 → above resets belowStreak → emit
	ts += 5

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 3 {
		t.Errorf("expected 3 emits (rise tick + dip tick + recovery tick), got %d", n)
	}
}

func TestPipeline_Busy_FallStopsAfterSustainedQuiet(t *testing.T) {
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     15,
		CPUDeltaThreshIdle: 100,
		AgentBusyRiseTicks: 2,
		AgentBusyFallTicks: 3,
	})
	defer db.Close()
	ctx := context.Background()
	cache.Store(&CacheSnapshot{AllowedBinaries: map[string]bool{"claude": true}})
	br.IdleSecondsVal = 5
	br.CWDByPID = map[int]string{999: "/work/x"}

	// Rise to working: prev + 2 sustained above-bar ticks.
	ts := int64(1000)
	for _, c := range []uint64{0, 30, 60} {
		br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: c}}
		_ = p.RunTick(ctx, ts)
		ts += 5
	}
	// Now quiet ticks (delta 1 each, below bar). With fall=3: belowStreak 1,2
	// keep working (emit), belowStreak 3 flips working off in the same tick
	// BEFORE the emit gate (no emit), and subsequent quiet ticks don't emit.
	base := uint64(60)
	for range 5 {
		base += 1 // delta 1 < 15
		br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: base}}
		_ = p.RunTick(ctx, ts)
		ts += 5
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	// Rise emitted 1. Quiet ticks: belowStreak 1 (emit), 2 (emit), 3→stop (no
	// emit), then no emits. Total = 1 + 2 = 3.
	if n != 3 {
		t.Errorf("expected 3 total emits (1 rise + 2 quiet-while-working), got %d", n)
	}
}

func TestPipeline_Busy_PresentToIdleRegimeDropsModerate(t *testing.T) {
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     15,  // present bar
		CPUDeltaThreshIdle: 100, // idle bar
		AgentBusyRiseTicks: 2,
		AgentBusyFallTicks: 3,
	})
	defer db.Close()
	ctx := context.Background()
	cache.Store(&CacheSnapshot{AllowedBinaries: map[string]bool{"claude": true}})
	br.CWDByPID = map[int]string{999: "/work/x"}

	// Present: a moderate ~50cs/poll process rises to working (above 15).
	br.IdleSecondsVal = 5
	c := uint64(0)
	ts := int64(1000)
	for range 3 { // prev + 2 above-bar ⇒ working, last tick emits
		br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: c}}
		_ = p.RunTick(ctx, ts)
		c += 50
		ts += 5
	}
	var afterRise int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&afterRise)
	if afterRise != 1 {
		t.Fatalf("present moderate process should rise to working (1 emit), got %d", afterRise)
	}

	// User leaves: bar jumps to 100. The same ~50cs/poll is now below bar →
	// belowStreak accrues; ticks 1,2 still emit (working), tick 3 flips off.
	br.IdleSecondsVal = 200
	for range 4 {
		br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: c}}
		_ = p.RunTick(ctx, ts)
		c += 50
		ts += 5
	}
	var total int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&total)
	// 1 (rise) + 2 (belowStreak 1,2 while still working) = 3; tick 3 drops it.
	if total != 3 {
		t.Errorf("moderate process should stop counting after going idle; got %d emits, want 3", total)
	}
}

func TestPipeline_PausedProject_LowCPUWatcher_DoesNotResume(t *testing.T) {
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     15,
		CPUDeltaThreshIdle: 100,
		AgentBusyRiseTicks: 2,
		AgentBusyFallTicks: 3,
	})
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "paused-proj", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := q.SetProjectPaused(ctx, store.SetProjectPausedParams{Paused: 1, Name: "paused-proj"}); err != nil {
		t.Fatalf("pause: %v", err)
	}
	bin := "claude"
	cwd := "/work/paused-proj/"
	cache.Store(&CacheSnapshot{
		AllowedBinaries:  map[string]bool{"claude": true},
		Rules:            []domain.RuleSpec{{ID: 1, ProjectID: projID, Priority: 100, MatchBinaryName: &bin, MatchCwdPrefix: &cwd}},
		PausedProjectIDs: map[int64]bool{projID: true},
		ArmedProjects:    map[int64]bool{},
	})
	br.IdleSecondsVal = 5 // user present — but the watcher is below the busy bar
	br.CWDByPID = map[int]string{999: "/work/paused-proj/x"}

	// Idle watcher: ~2cs/poll, never reaches working.
	base := uint64(1000)
	for i := 0; i < 5; i++ {
		base += 2
		br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: base}}
		_ = p.RunTick(ctx, 1000+int64(i)*5)
	}

	var paused int64
	db.QueryRow(`SELECT paused FROM projects WHERE id=?`, projID).Scan(&paused)
	if paused != 1 {
		t.Errorf("low-CPU watcher must not auto-resume a paused project; paused=%d, want 1", paused)
	}
	if !cache.Snapshot().PausedProjectIDs[projID] {
		t.Errorf("cache should still flag project paused")
	}
}

func TestPipeline_AgentSignal_AfterFocusDisarm_Counts(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "armed-then-focused", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	bundle := "com.example.editor"
	title := "armed-then-focused"
	bin := "claude"
	cwdPrefix := "/Users/rian/work/armed-then-focused"
	cache.Store(&CacheSnapshot{
		AllowedBundles:  map[string]bool{bundle: true},
		AllowedBinaries: map[string]bool{bin: true},
		Rules: []domain.RuleSpec{
			{ID: 1, ProjectID: projID, Priority: 100, MatchBundleID: &bundle, MatchTitleSubstr: &title},
			{ID: 2, ProjectID: projID, Priority: 100, MatchBinaryName: &bin, MatchCwdPrefix: &cwdPrefix},
		},
		CwdPrefixes:      []string{cwdPrefix},
		PausedProjectIDs: map[int64]bool{},
		ArmedProjects:    map[int64]bool{projID: true},
	})
	br.IdleSecondsVal = 5
	br.FrontmostInfoVal = macos.FrontmostInfo{BundleID: bundle, PID: 1234}
	br.FocusedTitle = "armed-then-focused — main"
	br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: 100}}
	br.CWDByPID = map[int]string{999: cwdPrefix + "/src"}

	// Tick #1: focus disarms; agent has no prev CPU sample yet so no agent tick.
	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick #1: %v", err)
	}
	// Tick #2: focus tick lands again (still disarmed), agent tick now also lands.
	br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: 200}}
	if err := p.RunTick(ctx, 1005); err != nil {
		t.Fatalf("RunTick #2: %v", err)
	}

	var nFocus, nAgent int
	db.QueryRow(`SELECT COUNT(*) FROM ticks t JOIN observations o ON o.id=t.observation_id WHERE t.project_id=? AND o.source='focus'`, projID).Scan(&nFocus)
	db.QueryRow(`SELECT COUNT(*) FROM ticks t JOIN observations o ON o.id=t.observation_id WHERE t.project_id=? AND o.source='agent'`, projID).Scan(&nAgent)
	if nFocus != 2 {
		t.Errorf("expected 2 focus ticks, got %d", nFocus)
	}
	if nAgent != 1 {
		t.Errorf("expected 1 agent tick after disarm, got %d", nAgent)
	}
}

func TestPipeline_Busy_PIDReuseResetsWorkingState(t *testing.T) {
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     15,
		CPUDeltaThreshIdle: 100,
		AgentBusyRiseTicks: 2,
		AgentBusyFallTicks: 3,
	})
	defer db.Close()
	ctx := context.Background()
	cache.Store(&CacheSnapshot{AllowedBinaries: map[string]bool{"claude": true, "node": true}})
	br.IdleSecondsVal = 5
	br.CWDByPID = map[int]string{999: "/work/x"}

	// Drive PID 999 (claude) to working: prev + 2 sustained above-bar ticks.
	ts := int64(1000)
	for _, c := range []uint64{0, 30, 60} {
		br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: c}}
		_ = p.RunTick(ctx, ts)
		ts += 5
	}
	var afterRise int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&afterRise)
	if afterRise != 1 {
		t.Fatalf("expected 1 emit after rise, got %d", afterRise)
	}

	// PID 999 reused as a different binary "node" with a HIGHER cumulative
	// counter (no CPU regression), so ONLY the binary-name-change path can
	// reset state. If the reset were missing, the stale working=true would
	// emit immediately on this tick.
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "node", CPUTicks: 200}}
	_ = p.RunTick(ctx, ts) // name change resets activity; delta is large but aboveStreak restarts at 1 → not working
	ts += 5
	var afterReuse1 int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&afterReuse1)
	if afterReuse1 != 1 {
		t.Fatalf("reused PID must re-accumulate rise ticks; stale working leaked (got %d emits, want 1)", afterReuse1)
	}

	// Second above-bar tick for the new process → reaches working → emits.
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "node", CPUTicks: 230}}
	_ = p.RunTick(ctx, ts)
	var afterReuse2 int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&afterReuse2)
	if afterReuse2 != 2 {
		t.Errorf("reused PID should emit after re-accumulating rise ticks; got %d, want 2", afterReuse2)
	}
}
