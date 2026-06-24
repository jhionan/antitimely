package daemon

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// oldObservationsDDL is the pre-transcript table definition (2-value CHECK).
const oldObservationsDDL = `
CREATE TABLE observations (
    id              INTEGER PRIMARY KEY,
    source          TEXT NOT NULL CHECK (source IN ('focus', 'agent')),
    bundle_id       TEXT NOT NULL DEFAULT '',
    window_title    TEXT NOT NULL DEFAULT '',
    binary_name     TEXT NOT NULL DEFAULT '',
    cwd             TEXT NOT NULL DEFAULT '',
    first_seen      INTEGER NOT NULL,
    UNIQUE (source, bundle_id, window_title, binary_name, cwd)
) STRICT;`

func TestMigrateObservationsSourceCheck(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(oldObservationsDDL); err != nil {
		t.Fatalf("old ddl: %v", err)
	}
	// Seed an existing row to prove the rebuild preserves data + ids.
	if _, err := db.Exec(
		`INSERT INTO observations (id, source, binary_name, first_seen) VALUES (7, 'agent', 'claude', 100)`,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := migrateObservationsSourceCheck(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// transcript inserts must now succeed.
	if _, err := db.Exec(
		`INSERT INTO observations (source, cwd, first_seen) VALUES ('transcript', '/x', 200)`,
	); err != nil {
		t.Fatalf("transcript insert after migrate: %v", err)
	}
	// Existing row + id preserved.
	var name string
	if err := db.QueryRow(`SELECT binary_name FROM observations WHERE id=7`).Scan(&name); err != nil {
		t.Fatalf("preserved row: %v", err)
	}
	if name != "claude" {
		t.Fatalf("preserved row binary_name = %q, want claude", name)
	}
	// Idempotent: second call is a no-op and does not error.
	if err := migrateObservationsSourceCheck(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var ddl string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='observations'`,
	).Scan(&ddl); err != nil {
		t.Fatalf("read ddl: %v", err)
	}
	if !strings.Contains(ddl, "transcript") {
		t.Fatalf("post-migrate ddl missing transcript: %s", ddl)
	}
}
