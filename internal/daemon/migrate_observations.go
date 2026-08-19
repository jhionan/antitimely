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
func migrateObservationsSourceCheck(db *sql.DB) (retErr error) {
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
	defer func() {
		if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil && retErr == nil {
			retErr = fmt.Errorf("fk on: %w", err)
		}
	}()

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

// observationsWithSpaceDDL is the post-space table definition. Kept in sync
// with schema.sql so a migrated table matches a freshly-created one exactly.
const observationsWithSpaceDDL = `
CREATE TABLE observations_new (
    id              INTEGER PRIMARY KEY,
    source          TEXT NOT NULL CHECK (source IN ('focus', 'agent', 'transcript')),
    bundle_id       TEXT NOT NULL DEFAULT '',
    window_title    TEXT NOT NULL DEFAULT '',
    binary_name     TEXT NOT NULL DEFAULT '',
    cwd             TEXT NOT NULL DEFAULT '',
    space_id        TEXT NOT NULL DEFAULT '',
    first_seen      INTEGER NOT NULL,
    UNIQUE (source, bundle_id, window_title, binary_name, cwd, space_id)
) STRICT;`

// columnExists reports whether table has a column named exactly column,
// checked via SQLite's own metadata (pragma_table_info) rather than by
// scanning DDL text. A DDL substring check would treat a column merely
// ending in the target name (e.g. workspace_id matching a "space_id"
// search) as a match; this can't.
func columnExists(db *sql.DB, table, column string) (bool, error) {
	var n int
	if err := db.QueryRow(
		fmt.Sprintf(`SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name = ?`, table), column,
	).Scan(&n); err != nil {
		return false, fmt.Errorf("check column %s.%s: %w", table, column, err)
	}
	return n > 0, nil
}

// uniqueIndexHasColumn reports whether table has a UNIQUE index (origin 'u'
// — SQLite backs an inline UNIQUE constraint with an automatic index of
// this origin) that includes column among its indexed columns.
func uniqueIndexHasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf(`SELECT name FROM pragma_index_list('%s') WHERE origin = 'u'`, table))
	if err != nil {
		return false, fmt.Errorf("list indexes for %s: %w", table, err)
	}
	defer rows.Close()

	var idxNames []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, fmt.Errorf("scan index name for %s: %w", table, err)
		}
		idxNames = append(idxNames, name)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate indexes for %s: %w", table, err)
	}

	for _, idx := range idxNames {
		var n int
		if err := db.QueryRow(
			fmt.Sprintf(`SELECT COUNT(*) FROM pragma_index_info('%s') WHERE name = ?`, idx), column,
		).Scan(&n); err != nil {
			return false, fmt.Errorf("check index %s columns: %w", idx, err)
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}

// migrateObservationsSpaceID adds space_id and puts it in the UNIQUE key.
// SQLite cannot alter a UNIQUE in place, so we rebuild. Idempotent, and
// preserves ids because ticks.observation_id depends on their stability.
//
// "Already migrated" is checked structurally against SQLite's own pragmas,
// not by scanning the table's DDL text for the substring "space_id": a
// column merely ending in that name (e.g. workspace_id) would satisfy a
// text search without space_id ever existing, and a hand-added space_id
// column (without the widened UNIQUE) would satisfy it while two herdr
// spaces still collapse into one observation. Both are silent under-billing
// bugs for a tool whose whole job is billing.
func migrateObservationsSpaceID(db *sql.DB) (retErr error) {
	var exists int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='observations'`,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check observations table: %w", err)
	}
	if exists == 0 {
		return nil // schema.sql will create the new form
	}

	hasColumn, err := columnExists(db, "observations", "space_id")
	if err != nil {
		return err
	}
	if hasColumn {
		hasUnique, err := uniqueIndexHasColumn(db, "observations", "space_id")
		if err != nil {
			return err
		}
		if hasUnique {
			return nil // already migrated: column present AND in the UNIQUE
		}
	}

	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("fk off: %w", err)
	}
	defer func() {
		if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil && retErr == nil {
			retErr = fmt.Errorf("fk on: %w", err)
		}
	}()

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	// If space_id already exists (e.g. hand-added) but the UNIQUE wasn't
	// widened, carry its real values forward instead of clobbering them
	// with ''.
	spaceIDExpr := "''"
	if hasColumn {
		spaceIDExpr = "space_id"
	}

	stmts := []string{
		`DROP TABLE IF EXISTS observations_new`,
		observationsWithSpaceDDL,
		fmt.Sprintf(`INSERT INTO observations_new (id, source, bundle_id, window_title, binary_name, cwd, space_id, first_seen)
		   SELECT id, source, bundle_id, window_title, binary_name, cwd, %s, first_seen FROM observations`, spaceIDExpr),
		`DROP TABLE observations`,
		`ALTER TABLE observations_new RENAME TO observations`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("rebuild observations for space_id (%.40q): %w", s, err)
		}
	}
	return tx.Commit()
}
