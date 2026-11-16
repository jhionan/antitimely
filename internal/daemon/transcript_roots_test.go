package daemon

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rian/antitimely/internal/herdr"
	"github.com/rian/antitimely/internal/osbridge"
	"github.com/rian/antitimely/internal/store"
)

// Rian runs three Claude Code config dirs side by side (claude, claude-work,
// claude-j). Each keeps its own projects/ transcripts and sessions/ registry.
// Before multi-root support only ~/.claude/projects was read, so work/j
// sessions billed only while their process burned CPU: the ES-0014 audit
// found 8.67h of BClouder work missing that way (2026-11-02..16).

const (
	workSessionID = "dddddddd-3333-3333-3333-333333333333"
	jSessionID    = "eeeeeeee-4444-4444-4444-444444444444"
)

func newMultiRootPipeline(t *testing.T) (*Pipeline, *osbridge.FakeBridge, [2]ClaudeRoot) {
	t.Helper()
	var roots [2]ClaudeRoot
	for i := range roots {
		dir := t.TempDir()
		roots[i] = ClaudeRoot{Projects: filepath.Join(dir, "projects"), Sessions: filepath.Join(dir, "sessions")}
		for _, d := range []string{roots[i].Projects, roots[i].Sessions} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	body := `{"cwd":"/work/daas","timestamp":"2026-06-24T02:30:29Z"}` + "\n"
	writeSession(t, roots[0].Projects, "-work-daas", workSessionID, body)
	writeSession(t, roots[1].Projects, "-work-daas", jSessionID, body)

	cache := NewCache()
	cache.Store(&CacheSnapshot{CwdPatterns: []string{"/work/daas"}})
	br := &osbridge.FakeBridge{}
	p := NewPipeline(nil, br, cache, PipelineConfig{
		TranscriptTracking: true,
		ClaudeRoots:        roots[:],
		TranscriptGraceSec: 600,
	})
	p.herdr = herdr.NewResolver("../herdr/testdata/session.json")
	return p, br, roots
}

func TestCollectTranscript_ReadsEveryClaudeConfigDir(t *testing.T) {
	p, _, _ := newMultiRootPipeline(t)

	sigs := p.collectTranscriptSignals(context.Background(), p.cache.Snapshot(), transcriptNow)
	got := map[string]bool{}
	for _, s := range sigs {
		got[s.WindowTitle] = true
	}
	if len(sigs) != 2 || !got[workSessionID] || !got[jSessionID] {
		t.Fatalf("want one signal per config dir (%s, %s), got %+v", workSessionID, jSessionID, sigs)
	}
}

// A second root's state must survive the first root's prune pass, or every
// tick would re-read it from byte 0.
func TestCollectTranscript_MultiRootStateSurvivesPrune(t *testing.T) {
	p, _, roots := newMultiRootPipeline(t)
	p.collectTranscriptSignals(context.Background(), p.cache.Snapshot(), transcriptNow)
	for _, r := range roots {
		path := filepath.Join(r.Projects, "-work-daas")
		entries, _ := os.ReadDir(path)
		for _, e := range entries {
			if _, ok := p.transcriptState[filepath.Join(path, e.Name())]; !ok {
				t.Fatalf("state for %s pruned after one tick", e.Name())
			}
		}
	}
}

// A headless session's pid is registered in the sessions/ dir of the config
// dir it runs under. Looking only in ~/.claude/sessions would leave a
// claude-work review with cwd-only attribution.
func TestCollectTranscript_SpaceUsesOwnConfigDirSessions(t *testing.T) {
	p, br, roots := newMultiRootPipeline(t)
	writeClaudeSessionFile(t, roots[1].Sessions, 4343, jSessionID)
	br.EnvByPID = map[int]map[string]string{4343: {"HERDR_PANE_ID": "wN:p1"}}

	for _, s := range p.collectTranscriptSignals(context.Background(), p.cache.Snapshot(), transcriptNow) {
		if s.WindowTitle == jSessionID && s.SpaceID != "wN" {
			t.Fatalf("claude-j session SpaceID = %q, want wN from its own sessions/ registry", s.SpaceID)
		}
		if s.WindowTitle == workSessionID && s.SpaceID != "" {
			t.Fatalf("work session SpaceID = %q, want empty (no registry entry)", s.SpaceID)
		}
	}
}

func TestDefaultTranscriptRoots_DiscoversEveryClaudeConfigDir(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{".claude/projects", ".claude-work/projects", ".claude-j/projects", ".claude-hfi", "elsewhere/projects"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	want := []string{
		filepath.Join(home, ".claude/projects"),
		filepath.Join(home, ".claude-j/projects"),
		filepath.Join(home, ".claude-work/projects"),
	}
	if got := discoverTranscriptRoots(home, ""); !reflect.DeepEqual(got, want) {
		t.Fatalf("roots = %v, want %v (.claude-hfi has no projects/)", got, want)
	}
	// CLAUDE_CONFIG_DIR is included even outside the home convention, and a
	// dir already discovered is not listed twice.
	got := discoverTranscriptRoots(home, filepath.Join(home, "elsewhere"))
	if len(got) != 4 || got[0] != filepath.Join(home, "elsewhere/projects") {
		t.Fatalf("with CLAUDE_CONFIG_DIR=elsewhere: %v", got)
	}
	if got := discoverTranscriptRoots(home, filepath.Join(home, ".claude-work")); len(got) != 3 {
		t.Fatalf("CLAUDE_CONFIG_DIR duplicate not collapsed: %v", got)
	}
}

func TestParseConfig_TranscriptRoots(t *testing.T) {
	cfg := defaultFileConfigForTest(t, "transcript_roots: [/tmp/a/projects, /tmp/b/projects]\n")
	if want := []string{"/tmp/a/projects", "/tmp/b/projects"}; !reflect.DeepEqual(cfg.TranscriptRoots, want) {
		t.Fatalf("roots = %v, want %v", cfg.TranscriptRoots, want)
	}
	// The legacy single-root key still pins exactly one root.
	cfg = defaultFileConfigForTest(t, "transcript_root: /tmp/x\n")
	if want := []string{"/tmp/x"}; !reflect.DeepEqual(cfg.TranscriptRoots, want) {
		t.Fatalf("legacy transcript_root: roots = %v, want %v", cfg.TranscriptRoots, want)
	}
}

func TestPipelineConfigDerivesSessionsPerRoot(t *testing.T) {
	got := pipelineConfigFor(Config{TranscriptRoots: []string{"/u/.claude/projects", "/u/.claude-work/projects"}}).ClaudeRoots
	want := []ClaudeRoot{
		{Projects: "/u/.claude/projects", Sessions: "/u/.claude/sessions"},
		{Projects: "/u/.claude-work/projects", Sessions: "/u/.claude-work/sessions"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaudeRoots = %+v, want %+v", got, want)
	}
}

func TestImportTranscripts_ReadsEveryRoot(t *testing.T) {
	_, _, cache, db := newTestPipelineWithCfg(t, PipelineConfig{})
	defer db.Close()
	seedProjectWithCwdRule(t, db, cache, "Daas", "/work/daas")
	rootA, rootB := t.TempDir(), t.TempDir()
	writeSession(t, rootA, "-work-daas", "a1", `{"cwd":"/work/daas","timestamp":"2026-06-24T02:30:00Z"}`+"\n")
	writeSession(t, rootB, "-work-daas", "b1", `{"cwd":"/work/daas","timestamp":"2026-06-24T03:30:00Z"}`+"\n")

	from, to := int64(1782266400), int64(1782273600) // 02:00..04:00Z
	n, err := importTranscripts(db, store.New(db), cache.Snapshot(), []string{rootA, rootB}, from, to, 60, 5)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("inserted 0 ticks from two roots")
	}
	var obs int
	if err := db.QueryRow(`SELECT count(*) FROM observations WHERE source='transcript' AND window_title IN ('a1','b1')`).Scan(&obs); err != nil {
		t.Fatal(err)
	}
	if obs != 2 {
		t.Fatalf("observations = %d, want one per root's session", obs)
	}
}
