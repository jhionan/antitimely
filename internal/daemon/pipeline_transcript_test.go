package daemon

import (
	"context"
	"database/sql"
	"testing"

	"github.com/rian/antitimely/internal/store"
)

// reloadCacheForTest rebuilds the cache snapshot from db. Mirrors what the
// daemon's ReloadCache does — enough for pipeline tests.
func reloadCacheForTest(t *testing.T, db *sql.DB, cache *Cache) {
	t.Helper()
	svc := &AntitimelyService{DB: db, Q: store.New(db), Cache: cache}
	if err := svc.ReloadCache(); err != nil {
		t.Fatalf("ReloadCache: %v", err)
	}
}

// seedProjectWithCwdRule inserts a project + a cwd-prefix rule and reloads the
// cache so MatchRules and CwdPrefixes both cover prefix.
func seedProjectWithCwdRule(t *testing.T, db *sql.DB, cache *Cache, name, prefix string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO projects (name, created_at) VALUES (?, 0)`, name)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
	pid, _ := res.LastInsertId()
	if _, err := db.Exec(
		`INSERT INTO rules (project_id, priority, match_cwd_prefix, created_at) VALUES (?, 0, ?, 0)`, pid, prefix,
	); err != nil {
		t.Fatalf("insert rule: %v", err)
	}
	reloadCacheForTest(t, db, cache)
	return pid
}

func countTicks(t *testing.T, db *sql.DB, projectID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ticks WHERE project_id=?`, projectID).Scan(&n); err != nil {
		t.Fatalf("count ticks: %v", err)
	}
	return n
}

func TestRunTick_TranscriptCountsWhileIdleAndZeroCPU(t *testing.T) {
	root := t.TempDir()
	now := int64(1782268300)
	prefix := "/work/daas"
	writeSession(t, root, "-work-daas", "s1",
		`{"cwd":"/work/daas","timestamp":"2026-06-24T02:30:29Z"}`+"\n") // ~71s before now

	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     5,
		CPUDeltaThreshIdle: 5,
		TranscriptTracking: true,
		TranscriptRoot:     root,
		TranscriptGraceSec: 600,
	})
	defer db.Close()
	pid := seedProjectWithCwdRule(t, db, cache, "Daas", prefix)

	br.IdleSecondsVal = 9999 // fully idle (remote)
	br.Processes = nil       // zero CPU

	if err := p.RunTick(context.Background(), now); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if got := countTicks(t, db, pid); got != 1 {
		t.Fatalf("ticks = %d, want 1 (transcript should count while idle/zero-CPU)", got)
	}
}

func TestRunTick_TranscriptResumesPausedProject(t *testing.T) {
	root := t.TempDir()
	now := int64(1782268300)
	writeSession(t, root, "-work-daas", "s1",
		`{"cwd":"/work/daas","timestamp":"2026-06-24T02:30:29Z"}`+"\n")
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec: 120, CPUDeltaThresh: 5, CPUDeltaThreshIdle: 5,
		TranscriptTracking: true, TranscriptRoot: root, TranscriptGraceSec: 600,
	})
	defer db.Close()
	pid := seedProjectWithCwdRule(t, db, cache, "Daas", "/work/daas")
	// Pause it.
	if _, err := db.Exec(`UPDATE projects SET paused=1 WHERE id=?`, pid); err != nil {
		t.Fatal(err)
	}
	reloadCacheForTest(t, db, cache)
	br.IdleSecondsVal = 9999
	br.Processes = nil

	if err := p.RunTick(context.Background(), now); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if got := countTicks(t, db, pid); got != 1 {
		t.Fatalf("ticks = %d, want 1 (transcript overrides pause)", got)
	}
	var paused int
	db.QueryRow(`SELECT paused FROM projects WHERE id=?`, pid).Scan(&paused)
	if paused != 0 {
		t.Fatalf("project still paused; transcript should auto-resume")
	}
}
