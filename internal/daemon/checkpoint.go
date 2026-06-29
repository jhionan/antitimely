package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// checkpointWAL runs a TRUNCATE checkpoint: it flushes the write-ahead log into
// the main database file and shrinks the -wal file back to zero.
//
// SQLite's default *passive* auto-checkpoint keeps the database current but
// never truncates the WAL file. Under the daemon's long-running single-writer
// workload that let the WAL grow without bound (observed at 207 MB / ~50k
// frames after ~3 days), which slows every read — the heavy Status query was
// blowing past its context deadline. An explicit periodic TRUNCATE checkpoint
// is the standard remedy.
func checkpointWAL(ctx context.Context, db *sql.DB) error {
	// PRAGMA wal_checkpoint returns one row: (busy, log_frames, checkpointed).
	var busy, logFrames, checkpointed int
	err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).
		Scan(&busy, &logFrames, &checkpointed)
	if err != nil {
		return fmt.Errorf("wal_checkpoint: %w", err)
	}
	if busy != 0 {
		// A reader held an old snapshot, so the WAL could not be truncated.
		// Not fatal — the next pass will retry — but worth surfacing.
		return fmt.Errorf("wal_checkpoint busy: truncation blocked (log=%d checkpointed=%d)", logFrames, checkpointed)
	}
	return nil
}

// runCheckpointer truncates the WAL every interval until ctx is cancelled, then
// does one final checkpoint on shutdown so the WAL never lingers large.
func runCheckpointer(ctx context.Context, db *sql.DB, interval time.Duration) {
	// Immediate checkpoint on startup truncates a WAL left large by a prior run
	// or crash, so recovery doesn't wait a full interval.
	if err := checkpointWAL(ctx, db); err != nil {
		log.Printf("wal checkpoint (startup): %v", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// Use a fresh context: ctx is already cancelled, but the final
			// checkpoint must still run during shutdown.
			if err := checkpointWAL(context.Background(), db); err != nil {
				log.Printf("wal checkpoint (shutdown): %v", err)
			}
			return
		case <-t.C:
			if err := checkpointWAL(ctx, db); err != nil {
				log.Printf("wal checkpoint: %v", err)
			}
		}
	}
}
