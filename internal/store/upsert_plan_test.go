package store

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// UpsertObservation runs for every signal on every 5s tick, so it must never
// scan ticks. Its original "DO UPDATE SET id = id" rewrote the primary
// key, and with foreign_keys on that made SQLite verify no tick still pointed
// at the old id: three full scans of ticks per upsert (nothing indexes ticks by
// observation_id alone). At 1.5M ticks that was ~43ms per signal and the
// daemon's chronic multi-second "write=" slow ticks, growing with history.
func TestUpsertObservationPlanDoesNotScanTicks(t *testing.T) {
	schema, err := os.ReadFile("../../schema.sql")
	if err != nil {
		t.Fatalf("read schema.sql: %v", err)
	}
	// foreign_keys on, as the daemon opens it: the scans are FK checks.
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	rows, err := db.Query("EXPLAIN QUERY PLAN "+upsertObservation, "agent", "", "", "claude", "/x", "", int64(0))
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	// One ticks scan stays in the static plan whatever the SET clause: SQLite
	// compiles a child scan for any parent-row write but guards it with
	// OP_FkIfZero, so it only runs while deferred FK violations are pending -
	// never, here. Measured on the live DB: 0 full-scan steps per upsert after
	// the fix, 1,341,503 before. Rewriting the key adds unguarded scans on top.
	scans := 0
	for _, step := range plan {
		if strings.Contains(step, "SCAN ticks") {
			scans++
		}
	}
	if scans > 1 {
		t.Fatalf("UpsertObservation plan scans ticks %d times, want at most the 1 FkIfZero-guarded scan; full plan:\n%s",
			scans, strings.Join(plan, "\n"))
	}
}
