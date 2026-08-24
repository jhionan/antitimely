package daemon

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/rian/antitimely/internal/store"
)

// newTotalsDB builds an in-memory DB with the real schema, two projects and a
// spread of ticks that exercise every clause TotalsByProjectSince has to get
// right: same-timestamp rows for one project (must dedup), an unassigned tick
// (must not surface as a project) and a tick below the cutoff (must be cut).
func newTotalsDB(t *testing.T) (*store.Queries, int64, int64) {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(loadSchema(t)); err != nil {
		t.Fatal(err)
	}

	exec := func(q string, args ...any) sql.Result {
		t.Helper()
		res, err := db.Exec(q, args...)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return res
	}
	id := func(res sql.Result) int64 {
		t.Helper()
		n, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	projA := id(exec(`INSERT INTO projects (name, created_at) VALUES ('A', 0)`))
	projB := id(exec(`INSERT INTO projects (name, created_at) VALUES ('B', 0)`))

	obs := func(title string) int64 {
		return id(exec(
			`INSERT INTO observations (source, bundle_id, window_title, binary_name, cwd, space_id, first_seen)
			 VALUES ('agent', '', ?, 'claude', '/tmp', '', 0)`, title))
	}
	obs1, obs2, obs3 := obs("one"), obs("two"), obs("three")

	// The ticks PK is (ts, observation_id), so every row at the same second
	// needs its own observation — which is exactly the parallel-work case the
	// DISTINCT is there to collapse.
	tick := func(ts, obsID int64, proj any) {
		exec(`INSERT INTO ticks (ts, observation_id, project_id) VALUES (?, ?, ?)`, ts, obsID, proj)
	}
	// A: two observations at the same second — one billable second, not two.
	tick(100, obs1, projA)
	tick(100, obs2, projA)
	// B: two distinct seconds.
	tick(100, obs3, projB)
	tick(105, obs1, projB)
	// Unassigned: must never be reported as a project's total.
	tick(110, obs2, nil)
	// Below the cutoff: must be excluded.
	tick(50, obs1, projA)

	return store.New(db), projA, projB
}

func TestTotalsByProjectSinceCountsDistinctSecondsPerProject(t *testing.T) {
	q, projA, projB := newTotalsDB(t)

	rows, err := q.TotalsByProjectSince(context.Background(), 100)
	if err != nil {
		t.Fatalf("TotalsByProjectSince: %v", err)
	}

	got := map[int64]int64{}
	for _, r := range rows {
		got[r.ProjectID] = r.TickCount
	}
	want := map[int64]int64{projA: 1, projB: 2}
	if len(got) != len(want) {
		t.Fatalf("got %d project rows %v, want %d %v", len(got), got, len(want), want)
	}
	for id, n := range want {
		if got[id] != n {
			t.Errorf("project %d: got %d distinct seconds, want %d", id, got[id], n)
		}
	}
}

// Ticks whose project row is gone must not be reported under a project id that
// no longer resolves — Status looks every id up in ListProjectsWithCompany, so
// an orphan row is at best dead weight and at worst a total attributed to a
// recycled id.
func TestTotalsByProjectSinceSkipsOrphanedTicks(t *testing.T) {
	q, projA, projB := newTotalsDB(t)

	rows, err := q.TotalsByProjectSince(context.Background(), 0)
	if err != nil {
		t.Fatalf("TotalsByProjectSince: %v", err)
	}
	for _, r := range rows {
		if r.ProjectID != projA && r.ProjectID != projB {
			t.Errorf("unexpected project id %d in totals (unassigned or orphaned ticks leaked in)", r.ProjectID)
		}
	}
}
