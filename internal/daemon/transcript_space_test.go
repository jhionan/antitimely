package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/rian/antitimely/internal/herdr"
	"github.com/rian/antitimely/internal/macos"
)

// A session uuid herdr's session.json does not name: the shape of a Claude
// session started headless (the security-guidance plugin's review hook, or any
// `claude -p`) from inside a herdr pane. herdr only records each pane's own
// interactive session, so these never resolve through SpaceForSession.
const unboundSessionID = "cccccccc-2222-2222-2222-222222222222"

// writeClaudeSessionFile mimics Claude Code's ~/.claude/sessions/<pid>.json,
// which it writes for every running session (headless included) and deletes
// on exit.
func writeClaudeSessionFile(t *testing.T, dir string, pid int, sessionID string) string {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("%d.json", pid))
	body := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/work/daas","kind":"interactive","entrypoint":"sdk-py"}`, pid, sessionID)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// newUnboundSessionPipeline wires a transcript pipeline whose only session is
// unknown to herdr, with a Claude sessions dir and a fake bridge to probe.
func newUnboundSessionPipeline(t *testing.T) (*Pipeline, *macos.FakeBridge, string) {
	t.Helper()
	root := t.TempDir()
	sessionsDir := t.TempDir()
	body := `{"cwd":"/work/daas","timestamp":"2026-06-24T02:30:29Z"}` + "\n"
	writeSession(t, root, "-work-daas", unboundSessionID, body)

	cache := NewCache()
	cache.Store(&CacheSnapshot{CwdPatterns: []string{"/work/daas"}})
	br := &macos.FakeBridge{}
	p := NewPipeline(nil, br, cache, PipelineConfig{
		TranscriptTracking: true,
		TranscriptRoot:     root,
		TranscriptGraceSec: 600,
		ClaudeSessionsDir:  sessionsDir,
	})
	p.herdr = herdr.NewResolver("../herdr/testdata/session.json")
	return p, br, sessionsDir
}

const transcriptNow = int64(1782268300) // 71s after the session's last turn

// TestCollectTranscript_UnboundSessionInheritsProcessPaneSpace is the fix for
// the 2026-09-23 misattribution: ~10 security-review sessions spawned from the
// DED space ran in the shared daas-back-end checkout, carried no space, and so
// fell through to the generic /daas/ cwd rule. The session's own process
// inherits HERDR_PANE_ID from the pane that spawned it; reading it recovers
// the space the review actually belongs to.
func TestCollectTranscript_UnboundSessionInheritsProcessPaneSpace(t *testing.T) {
	p, br, sessionsDir := newUnboundSessionPipeline(t)
	writeClaudeSessionFile(t, sessionsDir, 4242, unboundSessionID)
	br.EnvByPID = map[int]map[string]string{4242: {"HERDR_PANE_ID": "wN:p1"}}

	sigs := p.collectTranscriptSignals(context.Background(), p.cache.Snapshot(), transcriptNow)
	if len(sigs) != 1 {
		t.Fatalf("got %d signals, want 1", len(sigs))
	}
	if sigs[0].SpaceID != "wN" {
		t.Fatalf("SpaceID = %q, want %q (inherited from the session process's pane)", sigs[0].SpaceID, "wN")
	}
}

// TestCollectTranscript_PaneBindingOutlivesSessionProcess: Claude Code deletes
// sessions/<pid>.json when the process exits, but its transcript keeps billing
// for the grace window after the last turn. The learned binding must carry
// that tail, or every review's final minutes fall back to the cwd rule.
func TestCollectTranscript_PaneBindingOutlivesSessionProcess(t *testing.T) {
	p, br, sessionsDir := newUnboundSessionPipeline(t)
	f := writeClaudeSessionFile(t, sessionsDir, 4242, unboundSessionID)
	br.EnvByPID = map[int]map[string]string{4242: {"HERDR_PANE_ID": "wN:p1"}}

	p.collectTranscriptSignals(context.Background(), p.cache.Snapshot(), transcriptNow)
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	delete(br.EnvByPID, 4242) // the process is gone

	sigs := p.collectTranscriptSignals(context.Background(), p.cache.Snapshot(), transcriptNow+5)
	if len(sigs) != 1 || sigs[0].SpaceID != "wN" {
		t.Fatalf("after the process exits: want 1 signal in wN, got %+v", sigs)
	}
	if br.EnvCalls != 1 {
		t.Fatalf("ProcessEnvVar called %d times, want 1: a learned pane must not be re-probed", br.EnvCalls)
	}
}

// TestCollectTranscript_NonHerdrSessionProbedOnce: a session process with no
// HERDR_PANE_ID (a plain terminal) is a real answer, cached so it isn't
// re-probed with a `ps` every tick for the session's whole life.
func TestCollectTranscript_NonHerdrSessionProbedOnce(t *testing.T) {
	p, br, sessionsDir := newUnboundSessionPipeline(t)
	writeClaudeSessionFile(t, sessionsDir, 4242, unboundSessionID)

	for i := int64(0); i < 3; i++ {
		sigs := p.collectTranscriptSignals(context.Background(), p.cache.Snapshot(), transcriptNow+5*i)
		if len(sigs) != 1 || sigs[0].SpaceID != "" {
			t.Fatalf("tick %d: want 1 signal with no space, got %+v", i, sigs)
		}
	}
	if br.EnvCalls != 1 {
		t.Fatalf("ProcessEnvVar called %d times, want 1", br.EnvCalls)
	}
}

// TestCollectTranscript_EnvProbeFailureNotCached: a transient ps failure must
// not pin the session to "no space" for life; the next tick retries.
func TestCollectTranscript_EnvProbeFailureNotCached(t *testing.T) {
	p, br, sessionsDir := newUnboundSessionPipeline(t)
	writeClaudeSessionFile(t, sessionsDir, 4242, unboundSessionID)
	br.EnvByPID = map[int]map[string]string{4242: {"HERDR_PANE_ID": "wN:p1"}}
	br.EnvErr = errors.New("ps: signal: killed")

	if sigs := p.collectTranscriptSignals(context.Background(), p.cache.Snapshot(), transcriptNow); len(sigs) != 1 || sigs[0].SpaceID != "" {
		t.Fatalf("failed probe: want 1 signal with no space (signal kept), got %+v", sigs)
	}
	br.EnvErr = nil
	sigs := p.collectTranscriptSignals(context.Background(), p.cache.Snapshot(), transcriptNow+5)
	if len(sigs) != 1 || sigs[0].SpaceID != "wN" {
		t.Fatalf("after recovery: want 1 signal in wN, got %+v", sigs)
	}
}

// TestCollectTranscript_HerdrBoundSessionSkipsProbe: herdr's own record stays
// authoritative, and a session it already resolves costs no subprocess.
func TestCollectTranscript_HerdrBoundSessionSkipsProbe(t *testing.T) {
	root := t.TempDir()
	sessionsDir := t.TempDir()
	const bound = "bbbbbbbb-1111-1111-1111-111111111111" // wN in the fixture
	writeSession(t, root, "-work-daas", bound, `{"cwd":"/work/daas","timestamp":"2026-06-24T02:30:29Z"}`+"\n")
	writeClaudeSessionFile(t, sessionsDir, 4242, bound)

	cache := NewCache()
	cache.Store(&CacheSnapshot{CwdPatterns: []string{"/work/daas"}})
	br := &macos.FakeBridge{EnvByPID: map[int]map[string]string{4242: {"HERDR_PANE_ID": "wM:p1"}}}
	p := NewPipeline(nil, br, cache, PipelineConfig{
		TranscriptTracking: true, TranscriptRoot: root, TranscriptGraceSec: 600, ClaudeSessionsDir: sessionsDir,
	})
	p.herdr = herdr.NewResolver("../herdr/testdata/session.json")

	sigs := p.collectTranscriptSignals(context.Background(), p.cache.Snapshot(), transcriptNow)
	if len(sigs) != 1 || sigs[0].SpaceID != "wN" {
		t.Fatalf("want 1 signal in wN (herdr's binding), got %+v", sigs)
	}
	if br.EnvCalls != 0 {
		t.Fatalf("ProcessEnvVar called %d times, want 0 for a herdr-bound session", br.EnvCalls)
	}
}
