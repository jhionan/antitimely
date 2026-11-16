package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/rian/antitimely/internal/herdr"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rian/antitimely/internal/domain"
)

// decodeProjectDir reverses Claude Code's project-dir encoding (cwd path with
// '/' replaced by '-'). The encoding is lossy — a literal '-' in a path is
// indistinguishable from a separator — so this is only a best-effort hint. The
// authoritative cwd is read from the transcript body via parseTranscriptTail.
func decodeProjectDir(name string) string {
	return strings.ReplaceAll(name, "-", "/")
}

// transcriptEntry is the subset of a transcript JSONL line we read.
type transcriptEntry struct {
	Cwd       string `json:"cwd"`
	Timestamp string `json:"timestamp"`
}

// parseTranscriptTail scans JSONL bytes and returns the newest entry timestamp
// (unix seconds) and the last non-empty cwd. Lines that don't parse are
// skipped. ok is false if no entry carried a timestamp.
func parseTranscriptTail(data []byte) (cwd string, newestUnix int64, ok bool) {
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var e transcriptEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if e.Cwd != "" {
			cwd = e.Cwd
		}
		if e.Timestamp == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, e.Timestamp)
		if err != nil {
			continue
		}
		if u := t.Unix(); u > newestUnix {
			newestUnix = u
		}
		ok = true
	}
	return cwd, newestUnix, ok
}

// ClaudeRoot is one Claude Code config dir (~/.claude, ~/.claude-work, …):
// its projects/ transcripts and its sessions/<pid>.json registry, used to
// find the process behind a transcript herdr doesn't know. Sessions empty ⇒
// that fallback is off for this root.
type ClaudeRoot struct {
	Projects string
	Sessions string
}

// collectTranscriptSignals scans transcript session files under every
// cfg.ClaudeRoots projects dir and emits a transcript signal for each session
// whose resolved cwd is under a tracked prefix and whose newest turn is within
// the grace window. Tail-reads via per-session byte offset so large
// transcripts aren't re-parsed each tick.
func (p *Pipeline) collectTranscriptSignals(ctx context.Context, snap *CacheSnapshot, now int64) []domain.Signal {
	if !p.cfg.TranscriptTracking || len(p.cfg.ClaudeRoots) == 0 {
		return nil
	}
	live := map[string]bool{}
	var out []domain.Signal
	for _, root := range p.cfg.ClaudeRoots {
		out = p.collectClaudeRoot(ctx, snap, now, root, live, out)
	}
	// Prune state for files that no longer exist under any root.
	for path := range p.transcriptState {
		if !live[path] {
			delete(p.transcriptState, path)
			delete(p.sessionPane, strings.TrimSuffix(filepath.Base(path), ".jsonl"))
		}
	}
	return out
}

// collectClaudeRoot scans one config dir's projects/, marking every transcript
// it sees in live and appending its signals to out.
func (p *Pipeline) collectClaudeRoot(ctx context.Context, snap *CacheSnapshot, now int64, root ClaudeRoot, live map[string]bool, out []domain.Signal) []domain.Signal {
	grace := int64(p.cfg.TranscriptGraceSec)
	projDirs, err := os.ReadDir(root.Projects)
	if err != nil {
		return out // root absent ⇒ nothing to do
	}
	var pids map[string]int // this root's registry, loaded at most once per tick, on first need
	for _, pd := range projDirs {
		if !pd.IsDir() {
			continue
		}
		sessDir := filepath.Join(root.Projects, pd.Name())
		entries, err := os.ReadDir(sessDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
				continue // skip subagents/ subdir and non-transcripts
			}
			path := filepath.Join(sessDir, e.Name())
			live[path] = true
			st := p.transcriptState[path]

			fi, err := os.Stat(path)
			if err != nil {
				continue
			}
			// Only read new bytes since last offset; reset if truncated/rotated.
			if fi.Size() < st.offset {
				st.offset = 0
			}
			if now-fi.ModTime().Unix() >= grace {
				// A session untouched for longer than the grace window cannot
				// carry a turn recent enough to count: an entry is written at
				// or before the mtime it produces, so the grace gate below
				// would drop it whatever the body says. Mark the bytes
				// consumed without reading them. Parsing them anyway is what
				// made every daemon restart re-read every transcript ever
				// written from byte 0 (311MB / 6.7s measured on 2026-08-24),
				// stalling the tick loop and the single SQLite connection the
				// CLI shares with it.
				st.offset = fi.Size()
			} else if fi.Size() > st.offset {
				if data, err := readFrom(path, st.offset); err == nil {
					if cwd, newest, ok := parseTranscriptTail(data); ok {
						if newest > st.lastActivity {
							st.lastActivity = newest
						}
						if cwd != "" {
							st.cwd = cwd
						}
					}
					st.offset = fi.Size()
				}
			}
			p.transcriptState[path] = st

			cwd := st.cwd
			if cwd == "" {
				cwd = decodeProjectDir(pd.Name())
			}
			// The grace gate goes first: it is free, while resolving an
			// unbound session's space can cost a `ps` probe.
			if now-st.lastActivity >= grace {
				continue
			}
			// Resolve the session's space before the track gate: a
			// space-bound project must admit this signal even when the cwd
			// matches no pattern, mirroring collectAgentSignals' track logic.
			// Without this, a space-only-bound project would silently lose
			// every transcript signal — no log line, just missing time.
			sessionID := e.Name()[:len(e.Name())-len(".jsonl")]
			spaceID := p.transcriptSpace(ctx, sessionID, root.Sessions, &pids)
			if !cwdMatchesAnyPattern(cwd, snap.CwdPatterns) && !(spaceID != "" && snap.BoundSpaceIDs[spaceID]) {
				continue
			}
			out = append(out, domain.Signal{
				Source:      domain.SourceTranscript,
				Cwd:         cwd,
				WindowTitle: sessionID,
				SpaceID:     spaceID,
			})
		}
	}
	return out
}

// transcriptSpace resolves the herdr space a Claude session ran in.
//
// herdr's session.json is authoritative, but it names only each pane's own
// interactive session. A session started headless from inside a pane — the
// security-guidance plugin's review hook, any `claude -p` — is absent there,
// so on 2026-09-23 ten reviews spawned from the DED space fell through to the
// generic /daas/ cwd rule. Such a process still inherits HERDR_PANE_ID, and
// Claude Code's sessions/<pid>.json names its pid, so read the pane from the
// process itself. sessionsDir is the registry of the config dir the
// transcript lives in; *pids is filled from it on first need.
func (p *Pipeline) transcriptSpace(ctx context.Context, sessionID, sessionsDir string, pids *map[string]int) string {
	if s, ok := p.herdr.SpaceForSession(sessionID); ok {
		return s.ID
	}
	paneID, known := p.sessionPane[sessionID]
	if !known {
		if p.bridge == nil || sessionsDir == "" {
			return ""
		}
		if *pids == nil {
			*pids = claudeSessionPIDs(sessionsDir)
		}
		pid, ok := (*pids)[sessionID]
		if !ok {
			return "" // not running, or not yet registered: retry next tick
		}
		env, err := p.bridge.ProcessEnvVars(ctx, pid, herdr.PaneEnvKeys...)
		if err != nil {
			// Not cached: one transient failure must not pin the session to
			// cwd-only matching for life (same contract as procPane).
			log.Printf("env pid=%d session=%s: %v", pid, sessionID, err)
			return ""
		}
		paneID = herdr.PaneFromEnv(env)
		p.sessionPane[sessionID] = paneID
	}
	if paneID == "" {
		return ""
	}
	if s, ok := p.herdr.SpaceForPane(paneID); ok {
		return s.ID
	}
	return ""
}

// claudeSessionPIDs reads Claude Code's per-process session registry
// (<dir>/<pid>.json, written for every running session and deleted on exit)
// into session uuid -> pid. Unreadable or malformed entries are skipped: the
// registry is Claude Code's internal format, so a change to it degrades to
// cwd-only attribution rather than an error.
func claudeSessionPIDs(dir string) map[string]int {
	out := map[string]int{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var reg struct {
			PID       int    `json:"pid"`
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(data, &reg) != nil || reg.PID <= 0 || reg.SessionID == "" {
			continue
		}
		out[reg.SessionID] = reg.PID
	}
	return out
}

// readFrom reads bytes from offset to EOF.
func readFrom(path string, offset int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, 0); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}
