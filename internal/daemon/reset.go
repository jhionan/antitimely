package daemon

import (
	"context"
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
//
// Reset assumes the daemon is not running (the caller should have stopped
// it first). The DSN sets the same WAL/busy_timeout pragmas as the daemon
// so concurrent access from a leftover process surfaces as a real lock
// error instead of an instant failure.
func Reset(dbPath string, scope ResetScope) error {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(2000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

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

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()

	for _, q := range queries {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	rollback = false

	// VACUUM cannot run inside a transaction so it runs after the commit.
	if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}
	return nil
}
