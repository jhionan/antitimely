package daemon

import (
	"database/sql"
	"fmt"
	"strings"
)

// rulesWithSpaceDDL is the post-space rules definition, kept in sync with
// schema.sql.
const rulesWithSpaceDDL = `
CREATE TABLE rules_new (
    id                  INTEGER PRIMARY KEY,
    project_id          INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    priority            INTEGER NOT NULL DEFAULT 100,
    match_bundle_id     TEXT,
    match_title_substr  TEXT,
    match_binary_name   TEXT,
    match_cwd_prefix    TEXT,
    match_space_id      TEXT,
    created_at          INTEGER NOT NULL,
    CHECK (
        match_bundle_id IS NOT NULL OR
        match_title_substr IS NOT NULL OR
        match_binary_name IS NOT NULL OR
        match_cwd_prefix IS NOT NULL OR
        match_space_id IS NOT NULL
    )
) STRICT;`

// migrateRulesSpaceID adds match_space_id and widens the CHECK so a space-only
// rule is legal. SQLite cannot alter a CHECK in place, so we rebuild. Nothing
// references rules, so only its own ids need preserving.
//
// "Already migrated" is checked structurally: the match_space_id column must
// exist (via pragma_table_info, not a DDL substring match) AND the live DDL
// must contain the widened CHECK clause text "match_space_id IS NOT NULL" —
// a CHECK isn't exposed via pragmas, so this half stays a text check, but a
// specific one instead of a bare column-name search.
func migrateRulesSpaceID(db *sql.DB) (retErr error) {
	var exists int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='rules'`,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check rules table: %w", err)
	}
	if exists == 0 {
		return nil
	}

	hasColumn, err := columnExists(db, "rules", "match_space_id")
	if err != nil {
		return err
	}
	if hasColumn {
		var ddl string
		if err := db.QueryRow(
			`SELECT sql FROM sqlite_master WHERE type='table' AND name='rules'`,
		).Scan(&ddl); err != nil {
			return fmt.Errorf("read rules ddl: %w", err)
		}
		if strings.Contains(ddl, "match_space_id IS NOT NULL") {
			return nil // already migrated: column present AND CHECK widened
		}
	}

	// Symmetric with the observations rebuild: rules.project_id is itself a
	// FK into projects, and enforcement must not be able to fail the swap
	// (and thus block the daemon from ever starting) over a pre-existing
	// dangling reference.
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

	matchSpaceIDExpr := "NULL"
	if hasColumn {
		matchSpaceIDExpr = "match_space_id"
	}

	stmts := []string{
		`DROP TABLE IF EXISTS rules_new`,
		rulesWithSpaceDDL,
		fmt.Sprintf(`INSERT INTO rules_new (id, project_id, priority, match_bundle_id, match_title_substr,
		                        match_binary_name, match_cwd_prefix, match_space_id, created_at)
		   SELECT id, project_id, priority, match_bundle_id, match_title_substr,
		          match_binary_name, match_cwd_prefix, %s, created_at FROM rules`, matchSpaceIDExpr),
		`DROP TABLE rules`,
		`ALTER TABLE rules_new RENAME TO rules`,
		`CREATE INDEX IF NOT EXISTS idx_rules_priority ON rules(priority)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("rebuild rules for match_space_id (%.40q): %w", s, err)
		}
	}
	return tx.Commit()
}
