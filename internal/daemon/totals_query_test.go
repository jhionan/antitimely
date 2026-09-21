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

// Two projects of one company working the same second must bill that second
// once; the same second spent in a different company counts for each company.
// Projects with no company roll into one bucket whose row has a NULL name.
func TestCompanyDedupTotalsInRangeBillsSharedSecondsOncePerCompany(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(loadSchema(t)); err != nil {
		t.Fatal(err)
	}

	exec := func(query string, args ...any) {
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	id := func(query string, args ...any) int64 {
		var n int64
		if err := db.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}

	co1 := id(`INSERT INTO companies (name, created_at) VALUES ('Co1', 0) RETURNING id`)
	co2 := id(`INSERT INTO companies (name, created_at) VALUES ('Co2', 0) RETURNING id`)
	p1 := id(`INSERT INTO projects (name, company_id, created_at) VALUES ('P1', ?, 0) RETURNING id`, co1)
	p2 := id(`INSERT INTO projects (name, company_id, created_at) VALUES ('P2', ?, 0) RETURNING id`, co1)
	p3 := id(`INSERT INTO projects (name, company_id, created_at) VALUES ('P3', ?, 0) RETURNING id`, co2)
	p4 := id(`INSERT INTO projects (name, created_at) VALUES ('P4', 0) RETURNING id`)

	obs := func(cwd string) int64 {
		return id(`INSERT INTO observations (source, binary_name, cwd, first_seen)
			VALUES ('agent', 'claude', ?, 0) RETURNING id`, cwd)
	}
	o1, o2, o3, o4 := obs("/1"), obs("/2"), obs("/3"), obs("/4")

	tick := func(ts, obsID, proj any) {
		exec(`INSERT INTO ticks (ts, observation_id, project_id) VALUES (?, ?, ?)`, ts, obsID, proj)
	}
	// Co1: two projects sharing the same second — one billable second.
	tick(100, o1, p1)
	tick(100, o2, p2)
	tick(105, o1, p1)
	// Co2: its own company's distinct second, same ts as Co1 work.
	tick(100, o3, p3)
	// Company-less project: the "(no company)" bucket.
	tick(105, o4, p4)
	// Outside [60, 800): must be excluded.
	tick(50, o1, p1)
	tick(900, o1, p1)

	q := store.New(db)
	rows, err := q.CompanyDedupTotalsInRange(context.Background(), store.CompanyDedupTotalsInRangeParams{Ts: 60, Ts_2: 800})
	if err != nil {
		t.Fatalf("CompanyDedupTotalsInRange: %v", err)
	}

	got := map[string]int64{}
	for _, r := range rows {
		name := "(no company)"
		if r.Name.Valid {
			name = r.Name.String
		}
		if _, dup := got[name]; dup {
			t.Fatalf("duplicate company row %q (%+v): GROUP BY leaked a second bucket", name, r)
		}
		got[name] = r.TickCount
	}
	want := map[string]int64{"Co1": 2, "Co2": 1, "(no company)": 1}
	if len(got) != len(want) {
		t.Fatalf("got %d company rows %v, want %d %v", len(got), got, len(want), want)
	}
	for name, n := range want {
		if got[name] != n {
			t.Errorf("company %q: got %d distinct seconds, want %d", name, got[name], n)
		}
	}
}
