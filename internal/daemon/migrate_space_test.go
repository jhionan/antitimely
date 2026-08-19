package daemon

import (
	"database/sql"
	"strings"
	"testing"
)

// openPreSpaceDB builds a database with the pre-space observations and rules
// tables, so the migration has something real to upgrade.
func openPreSpaceDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/m.sqlite")
	if err != nil {
		t.Fatal(err)
	}
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
		`INSERT INTO projects (id,name,created_at) VALUES (8,'MD-Tracker',1)`,
		`INSERT INTO observations (id,source,cwd,first_seen) VALUES (9,'agent','/repo',1)`,
		`INSERT INTO rules (id,project_id,priority,match_cwd_prefix,created_at) VALUES (18,8,100,'/repo',1)`,
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

	// The new UNIQUE key must allow the same cwd in two different spaces.
	if _, err := db.Exec(
		`INSERT INTO observations (source,cwd,space_id,first_seen) VALUES ('agent','/repo','wN',1)`); err != nil {
		t.Fatalf("same cwd in a different space must be a distinct observation: %v", err)
	}

	if err := migrateObservationsSpaceID(db); err != nil {
		t.Fatalf("second run must be a no-op: %v", err)
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
