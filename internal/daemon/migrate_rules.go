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
func migrateRulesSpaceID(db *sql.DB) error {
	var ddl string
	err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='rules'`,
	).Scan(&ddl)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read rules ddl: %w", err)
	}
	if strings.Contains(ddl, "match_space_id") {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	stmts := []string{
		rulesWithSpaceDDL,
		`INSERT INTO rules_new (id, project_id, priority, match_bundle_id, match_title_substr,
		                        match_binary_name, match_cwd_prefix, match_space_id, created_at)
		   SELECT id, project_id, priority, match_bundle_id, match_title_substr,
		          match_binary_name, match_cwd_prefix, NULL, created_at FROM rules`,
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
