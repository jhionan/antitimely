package daemon

import (
	"testing"

	"github.com/rian/antitimely/internal/store"
)

func TestImportTranscripts_StitchesAndSkipsExisting(t *testing.T) {
	root := t.TempDir()
	// Turns at 02:00:00, 02:05:00 (gap 5m < grace), then 02:30:00 (gap 25m > grace).
	body := `{"cwd":"/work/daas","timestamp":"2026-06-24T02:00:00Z"}
{"cwd":"/work/daas","timestamp":"2026-06-24T02:05:00Z"}
{"cwd":"/work/daas","timestamp":"2026-06-24T02:30:00Z"}
`
	writeSession(t, root, "-work-daas", "s1", body)

	_, _, cache, db := newTestPipelineWithCfg(t, PipelineConfig{})
	defer db.Close()
	pid := seedProjectWithCwdRule(t, db, cache, "Daas", "/work/daas")

	from := int64(1782266400) // 2026-06-24T02:00:00Z
	to := int64(1782270000)   // 2026-06-24T03:00:00Z
	inserted, err := importTranscripts(db, store.New(db), cache.Snapshot(), root, from, to, 600, 5)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	// Block A: 02:00:00..02:05:00 = 300s ⇒ 61 ticks at 5s (inclusive).
	// Block B: single turn 02:30:00 ⇒ 1 tick.
	want := 61 + 1
	if inserted != want {
		t.Fatalf("inserted = %d, want %d", inserted, want)
	}
	if got := countTicks(t, db, pid); got != want {
		t.Fatalf("ticks in db = %d, want %d", got, want)
	}

	// Re-running imports nothing (idempotent skip of existing (ts,project)).
	again, err := importTranscripts(db, store.New(db), cache.Snapshot(), root, from, to, 600, 5)
	if err != nil {
		t.Fatalf("reimport: %v", err)
	}
	if again != 0 {
		t.Fatalf("reimport inserted = %d, want 0", again)
	}
}

// TestImportTranscripts_SkipsOffsetGridOverlap guards the billing-critical case
// the live data exposed: the daemon's tick grid and the importer's transcript
// grid have independent phases, so an exact (ts,project) dedup almost never
// matches over already-tracked time and would re-lay a ~2s-shifted grid —
// doubling COUNT(DISTINCT ts). The importer must skip a candidate when the
// project already has a tick within one interval (wall-clock) of it.
func TestImportTranscripts_SkipsOffsetGridOverlap(t *testing.T) {
	root := t.TempDir()
	B := int64(1782266400) // 2026-06-24T02:00:00Z
	// One stitched block [B, B+240] (two turns 4m apart, within grace).
	body := `{"cwd":"/work/daas","timestamp":"2026-06-24T02:00:00Z"}
{"cwd":"/work/daas","timestamp":"2026-06-24T02:04:00Z"}
`
	writeSession(t, root, "-work-daas", "s1", body)

	_, _, cache, db := newTestPipelineWithCfg(t, PipelineConfig{})
	defer db.Close()
	pid := seedProjectWithCwdRule(t, db, cache, "Daas", "/work/daas")

	// Simulate the daemon already tracking the FIRST HALF [B, B+120] on a +2s
	// offset grid (a different phase than the importer's grid).
	res, err := db.Exec(`INSERT INTO observations (source, binary_name, first_seen) VALUES ('agent','claude',?)`, B)
	if err != nil {
		t.Fatal(err)
	}
	obs, _ := res.LastInsertId()
	for ts := B + 2; ts <= B+122; ts += 5 {
		if _, err := db.Exec(`INSERT INTO ticks (ts, observation_id, project_id) VALUES (?,?,?)`, ts, obs, pid); err != nil {
			t.Fatal(err)
		}
	}

	inserted, err := importTranscripts(db, store.New(db), cache.Snapshot(), root, B, B+240, 600, 5)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	// The block is ~49 slots; the first ~25 are already tracked (offset). With
	// wall-clock dedup the importer fills only the ~23 gap slots, NOT the whole
	// block. Exact-only dedup would insert ~49 here (the bug).
	if inserted > 26 {
		t.Fatalf("inserted %d ticks; offset-grid overlap not deduped (want only the ~23 gap slots)", inserted)
	}
	// Billable distinct-seconds must reflect the block roughly ONCE (~49), not
	// existing(~25) + full-block(~49) ≈ 74 (the double-count).
	var distinct int
	if err := db.QueryRow(`SELECT count(DISTINCT ts) FROM ticks WHERE project_id=?`, pid).Scan(&distinct); err != nil {
		t.Fatal(err)
	}
	if distinct > 52 {
		t.Fatalf("distinct seconds = %d; offset double-count present (want ~49)", distinct)
	}
}
