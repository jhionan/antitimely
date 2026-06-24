package daemon

import (
	"database/sql"
	"fmt"
	"strings"
)

// newObservationsDDL is the post-transcript table definition. Kept in sync with
// schema.sql; the rebuild below uses it so the migrated table's CHECK matches a
// freshly-created one exactly.
const newObservationsDDL = `
CREATE TABLE observations_new (
    id              INTEGER PRIMARY KEY,
    source          TEXT NOT NULL CHECK (source IN ('focus', 'agent', 'transcript')),
    bundle_id       TEXT NOT NULL DEFAULT '',
    window_title    TEXT NOT NULL DEFAULT '',
    binary_name     TEXT NOT NULL DEFAULT '',
    cwd             TEXT NOT NULL DEFAULT '',
    first_seen      INTEGER NOT NULL,
    UNIQUE (source, bundle_id, window_title, binary_name, cwd)
) STRICT;`

// migrateObservationsSourceCheck widens observations.source to allow
// 'transcript'. SQLite can't ALTER a CHECK in place, so we rebuild the table.
// Idempotent: if the current definition already permits 'transcript', it's a
// no-op. Preserves rows and ids (ticks.observation_id FK relies on id stability).
func migrateObservationsSourceCheck(db *sql.DB) error {
	var ddl string
	err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='observations'`,
	).Scan(&ddl)
	if err == sql.ErrNoRows {
		return nil // table not created yet; schema.sql will create the new form
	}
	if err != nil {
		return fmt.Errorf("read observations ddl: %w", err)
	}
	if strings.Contains(ddl, "transcript") {
		return nil // already migrated
	}

	// FK references (ticks.observation_id) must not trip during the swap; we
	// preserve ids so referential integrity holds across the rename.
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("fk off: %w", err)
	}
	defer db.Exec(`PRAGMA foreign_keys=ON`)

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	stmts := []string{
		newObservationsDDL,
		`INSERT INTO observations_new (id, source, bundle_id, window_title, binary_name, cwd, first_seen)
		   SELECT id, source, bundle_id, window_title, binary_name, cwd, first_seen FROM observations`,
		`DROP TABLE observations`,
		`ALTER TABLE observations_new RENAME TO observations`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("rebuild observations (%.40q): %w", s, err)
		}
	}
	return tx.Commit()
}
