package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rian/antitimely/internal/domain"
)

func TestDecodeProjectDir(t *testing.T) {
	// decode is intentionally lossy (Claude Code does not escape literal '-'),
	// so test a dash-free path; the authoritative cwd comes from the jsonl body.
	got := decodeProjectDir("-Users-rian-focaApp-daas")
	want := "/Users/rian/focaApp/daas"
	if got != want {
		t.Fatalf("decodeProjectDir = %q, want %q", got, want)
	}
}

func TestParseTranscriptTail(t *testing.T) {
	// Two real-ish entries + one junk line that must be skipped.
	data := []byte(`{"type":"user","cwd":"/Users/rian/daas","timestamp":"2026-06-24T01:55:35.000Z"}
not json — must be skipped
{"type":"assistant","cwd":"/Users/rian/daas","timestamp":"2026-06-24T02:30:29.000Z"}
`)
	cwd, newest, ok := parseTranscriptTail(data)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if cwd != "/Users/rian/daas" {
		t.Fatalf("cwd = %q", cwd)
	}
	// 2026-06-24T02:30:29Z == 1782268229 unix.
	if newest != 1782268229 {
		t.Fatalf("newest = %d, want 1782268229", newest)
	}
}

func TestParseTranscriptTail_NoTimestamp(t *testing.T) {
	_, _, ok := parseTranscriptTail([]byte(`{"type":"summary"}` + "\n"))
	if ok {
		t.Fatal("ok = true, want false for entries without a timestamp")
	}
}

// writeSession creates <root>/<encoded>/<id>.jsonl with the given entries.
func writeSession(t *testing.T, root, encoded, id, body string) {
	t.Helper()
	dir := filepath.Join(root, encoded)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newTranscriptPipeline(t *testing.T, root string, graceSec int, prefixes []string) (*Pipeline, *Cache) {
	t.Helper()
	cache := NewCache()
	// Install a snapshot carrying the cwd prefixes (no rules needed; the
	// collector only consults CwdPrefixes).
	cache.swapForTest(&CacheSnapshot{CwdPrefixes: prefixes})
	p := NewPipeline(nil, nil, cache, PipelineConfig{
		TranscriptTracking: true,
		TranscriptRoot:     root,
		TranscriptGraceSec: graceSec,
	})
	return p, cache
}

func TestCollectTranscript_EmitsWithinGrace(t *testing.T) {
	root := t.TempDir()
	now := int64(1782268300)
	// last entry 71s before now; grace 600s ⇒ active.
	body := `{"cwd":"/work/daas","timestamp":"2026-06-24T02:30:29Z"}` + "\n"
	writeSession(t, root, "-work-daas", "sess1", body)
	p, _ := newTranscriptPipeline(t, root, 600, []string{"/work/daas"})

	sigs := p.collectTranscriptSignals(p.cache.Snapshot(), now)
	if len(sigs) != 1 {
		t.Fatalf("got %d signals, want 1", len(sigs))
	}
	if sigs[0].Source != domain.SourceTranscript || sigs[0].Cwd != "/work/daas" {
		t.Fatalf("bad signal: %+v", sigs[0])
	}
}

func TestCollectTranscript_StaleBeyondGrace(t *testing.T) {
	root := t.TempDir()
	now := int64(1782290000) // ~2h after the entry
	body := `{"cwd":"/work/daas","timestamp":"2026-06-24T02:30:29Z"}` + "\n"
	writeSession(t, root, "-work-daas", "sess1", body)
	p, _ := newTranscriptPipeline(t, root, 600, []string{"/work/daas"})

	if sigs := p.collectTranscriptSignals(p.cache.Snapshot(), now); len(sigs) != 0 {
		t.Fatalf("got %d signals, want 0 (stale)", len(sigs))
	}
}

func TestCollectTranscript_CwdNotTracked(t *testing.T) {
	root := t.TempDir()
	now := int64(1782268300)
	body := `{"cwd":"/work/other","timestamp":"2026-06-24T02:30:29Z"}` + "\n"
	writeSession(t, root, "-work-other", "sess1", body)
	p, _ := newTranscriptPipeline(t, root, 600, []string{"/work/daas"})

	if sigs := p.collectTranscriptSignals(p.cache.Snapshot(), now); len(sigs) != 0 {
		t.Fatalf("got %d signals, want 0 (untracked cwd)", len(sigs))
	}
}
