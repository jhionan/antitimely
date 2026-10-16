package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rian/antitimely/internal/domain"
)

func TestParseCodexTail(t *testing.T) {
	data := []byte(strings.Join([]string{
		`{"timestamp":"2026-10-16T10:19:02.539Z","type":"session_meta","payload":{"cwd":"/Users/rian/focaApp/bclouder","id":"x"}}`,
		`not json - must be skipped`,
		`{"timestamp":"2026-10-16T10:19:05.000Z","type":"turn_context","payload":{"cwd":"/Users/rian/focaApp/bclouder"}}`,
		`{"timestamp":"2026-10-16T10:19:09.250Z","type":"event_msg","payload":{"type":"item_completed","item":{"cwd":"file:///Users/rian/focaApp/bclouder/daas/daas-back-end%20wt"}}}`,
		// thread_settings and world_state carry a cwd too, but not the one the work ran in.
		`{"timestamp":"2026-10-16T10:19:10.000Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"cwd":"/elsewhere"}}}`,
		`{"timestamp":"2026-10-16T10:19:11.000Z","type":"token_usage_record","payload":"a string payload"}`,
	}, "\n") + "\n")

	got, ok := parseCodexTail(data, "")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got.sessionCwd != "/Users/rian/focaApp/bclouder" {
		t.Fatalf("sessionCwd = %q", got.sessionCwd)
	}
	if got.commandCwd != "/Users/rian/focaApp/bclouder/daas/daas-back-end wt" {
		t.Fatalf("commandCwd = %q (file:// stripped and percent-decoded expected)", got.commandCwd)
	}
	// 2026-10-16T10:19:11Z: the newest line wins even when its payload is not an object.
	if want := time.Date(2026, 10, 16, 10, 19, 11, 0, time.UTC).Unix(); got.newestUnix != want {
		t.Fatalf("newestUnix = %d, want %d", got.newestUnix, want)
	}
}

func TestParseCodexTail_NoTimestamp(t *testing.T) {
	if _, ok := parseCodexTail([]byte(`{"type":"session_meta","payload":{"cwd":"/x"}}`+"\n"), ""); ok {
		t.Fatal("ok = true, want false for entries without a timestamp")
	}
}

// writeCodexSession creates <root>/YYYY/MM/DD/rollout-<stamp>-<id>.jsonl for the
// day containing at, and returns its path.
func writeCodexSession(t *testing.T, root string, at time.Time, id, body string) string {
	t.Helper()
	dir := filepath.Join(root, at.Format("2006"), at.Format("01"), at.Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "rollout-"+at.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func codexLine(at time.Time, typ, payload string) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":%q,"payload":%s}`, at.UTC().Format("2006-01-02T15:04:05.000Z"), typ, payload) + "\n"
}

const codexSID = "01a14438-dae4-70f3-8df4-3dc65a87a007"

func newCodexPipeline(t *testing.T, root string, graceSec int, prefixes []string) *Pipeline {
	t.Helper()
	cache := NewCache()
	cache.Store(&CacheSnapshot{CwdPatterns: prefixes})
	return NewPipeline(nil, nil, cache, PipelineConfig{
		TranscriptTracking: true,
		TranscriptGraceSec: graceSec,
		CodexRoot:          root,
	})
}

func TestCollectCodex_PrefersCommandCwdOverWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	body := codexLine(now.Add(-90*time.Second), "session_meta", `{"cwd":"/work"}`) +
		codexLine(now.Add(-60*time.Second), "event_msg", `{"type":"item_completed","item":{"cwd":"file:///work/daas"}}`)
	writeCodexSession(t, root, now, codexSID, body)
	p := newCodexPipeline(t, root, 600, []string{"/work/daas"})

	sigs := p.collectCodexSignals(context.Background(), p.cache.Snapshot(), now.Unix())
	if len(sigs) != 1 {
		t.Fatalf("got %d signals, want 1", len(sigs))
	}
	s := sigs[0]
	if s.Source != domain.SourceTranscript || s.Cwd != "/work/daas" || s.BinaryName != "codex" || s.WindowTitle != codexSID {
		t.Fatalf("bad signal: %+v", s)
	}
}

// A session that has run no command yet is only known by its workspace root,
// which is still a valid cwd when a rule covers it.
func TestCollectCodex_FallsBackToSessionCwd(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	writeCodexSession(t, root, now, codexSID, codexLine(now.Add(-30*time.Second), "session_meta", `{"cwd":"/work/daas"}`))
	p := newCodexPipeline(t, root, 600, []string{"/work/daas"})

	sigs := p.collectCodexSignals(context.Background(), p.cache.Snapshot(), now.Unix())
	if len(sigs) != 1 || sigs[0].Cwd != "/work/daas" {
		t.Fatalf("got %+v, want one signal at /work/daas", sigs)
	}
}

func TestCollectCodex_StaleBeyondGrace(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	writeCodexSession(t, root, now, codexSID, codexLine(now.Add(-2*time.Hour), "session_meta", `{"cwd":"/work/daas"}`))
	p := newCodexPipeline(t, root, 600, []string{"/work/daas"})

	if sigs := p.collectCodexSignals(context.Background(), p.cache.Snapshot(), now.Unix()); len(sigs) != 0 {
		t.Fatalf("got %d signals, want 0 (stale)", len(sigs))
	}
}

func TestCollectCodex_CwdNotTracked(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	writeCodexSession(t, root, now, codexSID, codexLine(now.Add(-5*time.Second), "session_meta", `{"cwd":"/work/other"}`))
	p := newCodexPipeline(t, root, 600, []string{"/work/daas"})

	if sigs := p.collectCodexSignals(context.Background(), p.cache.Snapshot(), now.Unix()); len(sigs) != 0 {
		t.Fatalf("got %d signals, want 0 (untracked cwd)", len(sigs))
	}
}

// The command cwd survives a later turn_context that resets to the workspace
// root, and the incremental tail read must not forget it between ticks.
func TestCollectCodex_CommandCwdSticksAcrossTurns(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	path := writeCodexSession(t, root, now, codexSID,
		codexLine(now.Add(-120*time.Second), "event_msg", `{"type":"item_completed","item":{"cwd":"file:///work/daas"}}`))
	p := newCodexPipeline(t, root, 600, []string{"/work/daas"})
	if sigs := p.collectCodexSignals(context.Background(), p.cache.Snapshot(), now.Unix()); len(sigs) != 1 {
		t.Fatalf("first read: got %d signals, want 1", len(sigs))
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(codexLine(now.Add(-10*time.Second), "turn_context", `{"cwd":"/work"}`))
	f.Close()

	sigs := p.collectCodexSignals(context.Background(), p.cache.Snapshot(), now.Unix())
	if len(sigs) != 1 || sigs[0].Cwd != "/work/daas" {
		t.Fatalf("second read: got %+v, want one signal still at /work/daas", sigs)
	}
}

// The Claude collector prunes its per-file state for files it did not see.
// Codex files are not under TranscriptRoot, so they must not share that map.
func TestCollectCodex_StateSurvivesClaudeCollector(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	writeCodexSession(t, root, now, codexSID, codexLine(now.Add(-30*time.Second), "session_meta", `{"cwd":"/work/daas"}`))
	p := newCodexPipeline(t, root, 600, []string{"/work/daas"})
	p.cfg.TranscriptRoot = t.TempDir()

	p.collectCodexSignals(context.Background(), p.cache.Snapshot(), now.Unix())
	p.collectTranscriptSignals(context.Background(), p.cache.Snapshot(), now.Unix())
	if sigs := p.collectCodexSignals(context.Background(), p.cache.Snapshot(), now.Unix()); len(sigs) != 1 {
		t.Fatalf("got %d signals after the Claude collector ran, want 1", len(sigs))
	}
}

// A session that began on an earlier day lives in that day's directory and is
// still being written: yesterday's directory must be scanned.
func TestCollectCodex_SessionStartedYesterday(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	writeCodexSession(t, root, now.AddDate(0, 0, -1), codexSID, codexLine(now.Add(-20*time.Second), "turn_context", `{"cwd":"/work/daas"}`))
	p := newCodexPipeline(t, root, 600, []string{"/work/daas"})

	if sigs := p.collectCodexSignals(context.Background(), p.cache.Snapshot(), now.Unix()); len(sigs) != 1 {
		t.Fatalf("got %d signals, want 1", len(sigs))
	}
}

func TestCollectCodex_MissingRootAndDisabledAreInert(t *testing.T) {
	now := time.Now()
	p := newCodexPipeline(t, filepath.Join(t.TempDir(), "absent"), 600, []string{"/work/daas"})
	if sigs := p.collectCodexSignals(context.Background(), p.cache.Snapshot(), now.Unix()); len(sigs) != 0 {
		t.Fatalf("missing root: got %d signals", len(sigs))
	}

	root := t.TempDir()
	writeCodexSession(t, root, now, codexSID, codexLine(now.Add(-5*time.Second), "session_meta", `{"cwd":"/work/daas"}`))
	p = newCodexPipeline(t, "", 600, []string{"/work/daas"})
	if sigs := p.collectCodexSignals(context.Background(), p.cache.Snapshot(), now.Unix()); len(sigs) != 0 {
		t.Fatalf("empty root: got %d signals", len(sigs))
	}
	p = newCodexPipeline(t, root, 600, []string{"/work/daas"})
	p.cfg.TranscriptTracking = false
	if sigs := p.collectCodexSignals(context.Background(), p.cache.Snapshot(), now.Unix()); len(sigs) != 0 {
		t.Fatalf("tracking off: got %d signals", len(sigs))
	}
}

func TestPipelineConfigCarriesCodexRoot(t *testing.T) {
	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.CodexRoot = "/x/sessions"
	if got := pipelineConfigFor(cfg).CodexRoot; got != "/x/sessions" {
		t.Fatalf("CodexRoot = %q", got)
	}
}

// Real sessions are opened on the workspace root and run plenty of commands
// there (rg, git worktree list). Those say nothing about which repo is being
// worked on, so a root-level command must not displace the repo cwd.
func TestCollectCodex_RootLevelCommandDoesNotDisplaceRepo(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	body := codexLine(now.Add(-100*time.Second), "session_meta", `{"cwd":"/work"}`) +
		codexLine(now.Add(-80*time.Second), "event_msg", `{"type":"item_completed","item":{"cwd":"file:///work/daas"}}`) +
		codexLine(now.Add(-20*time.Second), "event_msg", `{"type":"item_completed","item":{"cwd":"file:///work"}}`)
	writeCodexSession(t, root, now, codexSID, body)
	p := newCodexPipeline(t, root, 600, []string{"/work/daas"})

	sigs := p.collectCodexSignals(context.Background(), p.cache.Snapshot(), now.Unix())
	if len(sigs) != 1 || sigs[0].Cwd != "/work/daas" {
		t.Fatalf("got %+v, want one signal at /work/daas", sigs)
	}
}

// The same holds when the root arrives in an earlier read than the command.
func TestCollectCodex_RootKnownFromEarlierRead(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	path := writeCodexSession(t, root, now, codexSID,
		codexLine(now.Add(-100*time.Second), "session_meta", `{"cwd":"/work"}`)+
			codexLine(now.Add(-90*time.Second), "event_msg", `{"type":"item_completed","item":{"cwd":"file:///work/daas"}}`))
	p := newCodexPipeline(t, root, 600, []string{"/work/daas"})
	p.collectCodexSignals(context.Background(), p.cache.Snapshot(), now.Unix())

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(codexLine(now.Add(-5*time.Second), "event_msg", `{"type":"item_completed","item":{"cwd":"file:///work"}}`))
	f.Close()

	sigs := p.collectCodexSignals(context.Background(), p.cache.Snapshot(), now.Unix())
	if len(sigs) != 1 || sigs[0].Cwd != "/work/daas" {
		t.Fatalf("got %+v, want one signal still at /work/daas", sigs)
	}
}
