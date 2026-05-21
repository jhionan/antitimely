package daemon

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// ResetScope determines which tables get wiped.
type ResetScope string

const (
	ResetAll   ResetScope = "all"   // everything
	ResetTicks ResetScope = "ticks" // only time-tracking data
)

// Reset wipes data from the SQLite DB at dbPath. The schema is preserved.
// Returns an error if the DB can't be opened or any DELETE fails.
func Reset(dbPath string, scope ResetScope) error {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	var queries []string
	switch scope {
	case ResetAll:
		// Order matters because of foreign keys:
		// ignored_observations → observations
		// ticks → observations, projects
		// rules → projects
		// projects → companies (with ON DELETE SET NULL on company_id)
		queries = []string{
			"DELETE FROM ticks",
			"DELETE FROM ignored_observations",
			"DELETE FROM observations",
			"DELETE FROM rules",
			"DELETE FROM watched_programs",
			"DELETE FROM projects",
			"DELETE FROM companies",
		}
	case ResetTicks:
		queries = []string{
			"DELETE FROM ticks",
			"DELETE FROM ignored_observations",
			"DELETE FROM observations",
		}
	default:
		return fmt.Errorf("unknown reset scope: %q", scope)
	}

	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
	}

	// Reclaim disk space — not strictly necessary but keeps the file small.
	// VACUUM cannot run inside a transaction so it runs after the deletes.
	if _, err := db.Exec("VACUUM"); err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}
	return nil
}
