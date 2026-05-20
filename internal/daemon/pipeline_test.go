package daemon

import (
	"context"
	"database/sql"
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

func newTestPipeline(t *testing.T) (*Pipeline, *macos.FakeBridge, *Cache, *sql.DB) {
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
	p := NewPipeline(q, br, cache, PipelineConfig{
		IdleThresholdSec: 120,
		CPUDeltaThresh:   5,
	})
	return p, br, cache, db
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
