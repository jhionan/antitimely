package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setMTime backdates a session file so the collector sees it as untouched
// since then.
func setMTime(t *testing.T, root, encoded, id string, unix int64) string {
	t.Helper()
	path := filepath.Join(root, encoded, id+".jsonl")
	when := time.Unix(unix, 0)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	return path
}

// A transcript untouched for longer than the grace window cannot produce a
// signal this tick no matter what it contains: every entry in it was written
// at or before its mtime. Parsing it anyway is what made a daemon restart
// re-read every byte of every transcript ever written (311MB, 6.7s, measured
// on 2026-08-24) while holding the one SQLite connection the CLI also needs.
func TestTranscriptSkipsReadingSessionsIdleBeyondGrace(t *testing.T) {
	root := t.TempDir()
	now := int64(1782268300)
	const grace = 600

	// Written long ago, but with a parseable entry: the old code would read
	// and parse it, recording lastActivity from the body.
	writeSession(t, root, "-work-daas", "stale",
		`{"cwd":"/work/daas","timestamp":"2026-06-24T00:00:00Z"}`+"\n")
	path := setMTime(t, root, "-work-daas", "stale", now-grace-60)

	p, _ := newTranscriptPipeline(t, root, grace, []string{"/work/daas"})
	if sigs := p.collectTranscriptSignals(p.cache.Snapshot(), now); len(sigs) != 0 {
		t.Fatalf("stale session emitted %d signals, want 0", len(sigs))
	}

	st, tracked := p.transcriptState[path]
	if !tracked {
		t.Fatal("stale session was not tracked at all; its offset would restart from 0 next tick")
	}
	if st.lastActivity != 0 {
		t.Errorf("stale session was parsed (lastActivity=%d); a transcript idle past the grace window should not be read at all", st.lastActivity)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.offset != fi.Size() {
		t.Errorf("offset = %d, want %d (the whole file must be marked consumed, or the next tick re-reads it)", st.offset, fi.Size())
	}
}

// The skip must key on the grace window, not on "have I seen this file
// before": a session written moments ago has to be read on the very first
// tick, which is the restart-recovery path.
func TestTranscriptReadsSessionsTouchedInsideGrace(t *testing.T) {
	root := t.TempDir()
	now := int64(1782268300)
	const grace = 600

	writeSession(t, root, "-work-daas", "fresh",
		`{"cwd":"/work/daas","timestamp":"2026-06-24T02:30:29Z"}`+"\n") // ~71s before now
	path := setMTime(t, root, "-work-daas", "fresh", now-30)

	p, _ := newTranscriptPipeline(t, root, grace, []string{"/work/daas"})
	sigs := p.collectTranscriptSignals(p.cache.Snapshot(), now)
	if len(sigs) != 1 {
		t.Fatalf("fresh session emitted %d signals, want 1", len(sigs))
	}
	if got := p.transcriptState[path].cwd; got != "/work/daas" {
		t.Errorf("cwd = %q, want /work/daas read from the transcript body", got)
	}
}
