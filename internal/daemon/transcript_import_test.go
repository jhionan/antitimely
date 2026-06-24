package daemon

import (
	"context"
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
	_ = context.Background()
}
