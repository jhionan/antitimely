package daemon

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// openPreSpaceDB builds a database with the pre-space observations and rules
// tables, so the migration has something real to upgrade. It also builds the
// two tables that reference observations.id by foreign key (ticks,
// ignored_observations) and turns foreign key enforcement ON, so tests can
// verify the migration's PRAGMA foreign_keys=OFF/ON handling actually
// protects them across the DROP TABLE/rename swap.
func openPreSpaceDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/m.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	// Migrations run under production's single-connection model
	// (SetMaxOpenConns(1) in daemon.go); pin the pool here too so
	// PRAGMA foreign_keys=ON, a per-connection setting, actually applies to
	// every statement below instead of a random pooled connection.
	db.SetMaxOpenConns(1)

	stmts := []string{
		`CREATE TABLE observations (
			id INTEGER PRIMARY KEY,
			source TEXT NOT NULL CHECK (source IN ('focus','agent','transcript')),
			bundle_id TEXT NOT NULL DEFAULT '',
			window_title TEXT NOT NULL DEFAULT '',
			binary_name TEXT NOT NULL DEFAULT '',
			cwd TEXT NOT NULL DEFAULT '',
			first_seen INTEGER NOT NULL,
			UNIQUE (source, bundle_id, window_title, binary_name, cwd)
		) STRICT`,
		`CREATE TABLE projects (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE,
			company_id INTEGER, paused INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL) STRICT`,
		`CREATE TABLE rules (
			id INTEGER PRIMARY KEY,
			project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			priority INTEGER NOT NULL DEFAULT 100,
			match_bundle_id TEXT, match_title_substr TEXT,
			match_binary_name TEXT, match_cwd_prefix TEXT,
			created_at INTEGER NOT NULL,
			CHECK (match_bundle_id IS NOT NULL OR match_title_substr IS NOT NULL
			    OR match_binary_name IS NOT NULL OR match_cwd_prefix IS NOT NULL)
		) STRICT`,
		`CREATE TABLE ticks (
			ts INTEGER NOT NULL,
			observation_id INTEGER NOT NULL REFERENCES observations(id),
			project_id INTEGER REFERENCES projects(id),
			PRIMARY KEY (ts, observation_id)
		) STRICT`,
		`CREATE TABLE ignored_observations (
			observation_id INTEGER PRIMARY KEY REFERENCES observations(id) ON DELETE CASCADE,
			ignored_at INTEGER NOT NULL
		) STRICT`,
		`INSERT INTO projects (id,name,created_at) VALUES (8,'MD-Tracker',1)`,
		`INSERT INTO observations (id,source,cwd,first_seen) VALUES (9,'agent','/repo',1)`,
		`INSERT INTO rules (id,project_id,priority,match_cwd_prefix,created_at) VALUES (18,8,100,'/repo',1)`,
		`INSERT INTO ticks (ts,observation_id,project_id) VALUES (100,9,8)`,
		`INSERT INTO ticks (ts,observation_id,project_id) VALUES (105,9,8)`,
		`INSERT INTO ignored_observations (observation_id,ignored_at) VALUES (9,1)`,
		`PRAGMA foreign_keys=ON`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup %.40q: %v", s, err)
		}
	}
	return db
}

func TestMigrateObservationsSpaceID(t *testing.T) {
	db := openPreSpaceDB(t)
	defer db.Close()

	if err := migrateObservationsSpaceID(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var ddl string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='observations'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "space_id") {
		t.Fatalf("space_id missing after migration:\n%s", ddl)
	}

	// Row and id preservation matters: ticks.observation_id depends on it.
	var id int64
	var space string
	if err := db.QueryRow(`SELECT id, space_id FROM observations`).Scan(&id, &space); err != nil {
		t.Fatal(err)
	}
	if id != 9 || space != "" {
		t.Fatalf("row not preserved: id=%d space=%q", id, space)
	}

	// PRAGMA foreign_keys=OFF during the rebuild must not have let the
	// DROP TABLE observations touch rows in tables that reference it.
	var tickCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ticks WHERE observation_id = 9`).Scan(&tickCount); err != nil {
		t.Fatal(err)
	}
	if tickCount != 2 {
		t.Fatalf("ticks referencing observation 9 not preserved: got %d, want 2", tickCount)
	}

	// ignored_observations is ON DELETE CASCADE against observations(id):
	// without the FK-off/on protection, the rebuild's DROP TABLE would
	// cascade-delete this row and silently un-suppress a phantom billing
	// signature the user deliberately ignored.
	var ignoredCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ignored_observations WHERE observation_id = 9`).Scan(&ignoredCount); err != nil {
		t.Fatal(err)
	}
	if ignoredCount != 1 {
		t.Fatalf("ignored_observations row not preserved (ON DELETE CASCADE would silently erase it): got %d, want 1", ignoredCount)
	}

	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var violations []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		violations = append(violations, fmt.Sprintf("%v", vals))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(violations) > 0 {
		t.Fatalf("foreign_key_check found violations: %v", violations)
	}

	// The new UNIQUE key must allow the same cwd in two different spaces.
	if _, err := db.Exec(
		`INSERT INTO observations (source,cwd,space_id,first_seen) VALUES ('agent','/repo','wN',1)`); err != nil {
		t.Fatalf("same cwd in a different space must be a distinct observation: %v", err)
	}

	if err := migrateObservationsSpaceID(db); err != nil {
		t.Fatalf("second run must be a no-op: %v", err)
	}
}

// TestMigrateObservationsSpaceID_ColumnSuffixIsNotAMatch proves the
// "already migrated" guard is structural, not a DDL substring check. Against
// the old `strings.Contains(ddl, "space_id")` guard, a column that merely
// ends in "space_id" (workspace_id) would satisfy the substring search and
// make the migration skip adding the real column — this test fails under
// that guard and passes under the pragma-based one.
func TestMigrateObservationsSpaceID_ColumnSuffixIsNotAMatch(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/m.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	stmts := []string{
		`CREATE TABLE observations (
			id INTEGER PRIMARY KEY,
			source TEXT NOT NULL CHECK (source IN ('focus','agent','transcript')),
			bundle_id TEXT NOT NULL DEFAULT '',
			window_title TEXT NOT NULL DEFAULT '',
			binary_name TEXT NOT NULL DEFAULT '',
			cwd TEXT NOT NULL DEFAULT '',
			workspace_id TEXT NOT NULL DEFAULT '',
			first_seen INTEGER NOT NULL,
			UNIQUE (source, bundle_id, window_title, binary_name, cwd)
		) STRICT`,
		`INSERT INTO observations (id,source,cwd,workspace_id,first_seen) VALUES (9,'agent','/repo','w1',1)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup %.40q: %v", s, err)
		}
	}

	if err := migrateObservationsSpaceID(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('observations') WHERE name = 'space_id'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("space_id column missing: a workspace_id column made the guard falsely report 'already migrated'")
	}
}

func TestMigrateRulesSpaceID(t *testing.T) {
	db := openPreSpaceDB(t)
	defer db.Close()

	if err := migrateRulesSpaceID(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// A space-only rule must satisfy the widened CHECK.
	if _, err := db.Exec(
		`INSERT INTO rules (project_id,priority,match_space_id,created_at) VALUES (8,50,'wN',1)`); err != nil {
		t.Fatalf("space-only rule rejected: %v", err)
	}
	// A rule with no match field at all must still be rejected.
	if _, err := db.Exec(
		`INSERT INTO rules (project_id,priority,created_at) VALUES (8,50,1)`); err == nil {
		t.Fatal("rule with no match field must violate CHECK")
	}
	if err := migrateRulesSpaceID(db); err != nil {
		t.Fatalf("second run must be a no-op: %v", err)
	}
}
