package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rian/antitimely/internal/domain"
)

// codexScanDays is how many day-directories back the Codex collector looks. A
// rollout file lives in the directory of the day its session began, and keeps
// being written for as long as the session runs, so today's directory alone
// would lose any session that crossed midnight.
const codexScanDays = 7

// defaultCodexRoot is Codex's rollout directory: $CODEX_HOME/sessions when
// set, else ~/.codex/sessions.
func defaultCodexRoot() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return filepath.Join(d, "sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

// codexTail is what one read of new rollout bytes yields.
type codexTail struct {
	// sessionCwd is the workspace root (session_meta / turn_context). For a
	// session opened on a parent folder this is the folder, not any repo in it.
	sessionCwd string
	// commandCwd is where the newest command actually ran, from an
	// item_completed payload (a file:// URL, decoded here). This is what
	// project rules can attribute.
	commandCwd string
	newestUnix int64
}

// parseCodexTail scans rollout JSONL bytes. Lines that do not parse are
// skipped; a line whose payload is not an object still counts for its
// timestamp. ok is false if no line carried a timestamp.
//
// root is the session's workspace root when an earlier read already learned
// it. A command run at the root says nothing about which repo is being worked
// on, so only commands below it set commandCwd.
func parseCodexTail(data []byte, root string) (t codexTail, ok bool) {
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var e struct {
			Timestamp string          `json:"timestamp"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &e) != nil || e.Timestamp == "" {
			continue
		}
		ts, err := time.Parse(time.RFC3339, e.Timestamp)
		if err != nil {
			continue
		}
		if u := ts.Unix(); u > t.newestUnix {
			t.newestUnix = u
		}
		ok = true

		var pl struct {
			Cwd  string `json:"cwd"`
			Item *struct {
				Cwd string `json:"cwd"`
			} `json:"item"`
		}
		if json.Unmarshal(e.Payload, &pl) != nil {
			continue
		}
		// thread_settings and world_state carry a cwd as well, but it is the
		// configured one, not where work ran.
		if (e.Type == "session_meta" || e.Type == "turn_context") && pl.Cwd != "" {
			t.sessionCwd = pl.Cwd
			root = pl.Cwd
		}
		if pl.Item != nil && pl.Item.Cwd != "" {
			if c := decodeFileURL(pl.Item.Cwd); c != root {
				t.commandCwd = c
			}
		}
	}
	return t, ok
}

// decodeFileURL turns "file:///a/b%20c" into "/a/b c"; any other string is
// returned unchanged.
func decodeFileURL(s string) string {
	rest, found := strings.CutPrefix(s, "file://")
	if !found {
		return s
	}
	if d, err := url.PathUnescape(rest); err == nil {
		return d
	}
	return rest
}

// codexSessionID is the trailing uuid of rollout-<local time>-<uuid>.jsonl.
func codexSessionID(name string) string {
	id := strings.TrimSuffix(name, ".jsonl")
	if len(id) > 36 {
		id = id[len(id)-36:]
	}
	return id
}

// collectCodexSignals emits a transcript signal for each Codex rollout file
// whose newest line is within the grace window and whose working directory is
// under a tracked cwd prefix. Like the Claude collector it tail-reads from a
// per-file offset, but it keeps its own state: collectTranscriptSignals prunes
// every path it did not see, which would drop these files each tick.
//
// Codex sessions are usually opened on a parent folder (the BClouder root),
// which no project rule covers; the repo the agent actually worked in comes
// from its command events, so that is preferred over the session's own cwd.
// Space attribution is not resolved (no pid registry), so cwd only.
func (p *Pipeline) collectCodexSignals(ctx context.Context, snap *CacheSnapshot, now int64) []domain.Signal {
	if !p.cfg.TranscriptTracking || p.cfg.CodexRoot == "" {
		return nil
	}
	if p.codexState == nil {
		p.codexState = map[string]transcriptSession{}
	}
	grace := int64(p.cfg.TranscriptGraceSec)

	live := map[string]bool{}
	var out []domain.Signal
	for i := 0; i < codexScanDays; i++ {
		day := time.Unix(now, 0).AddDate(0, 0, -i)
		dir := filepath.Join(p.cfg.CodexRoot, day.Format("2006"), day.Format("01"), day.Format("02"))
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasPrefix(name, "rollout-") || filepath.Ext(name) != ".jsonl" {
				continue
			}
			path := filepath.Join(dir, name)
			live[path] = true
			st := p.codexState[path]

			fi, err := os.Stat(path)
			if err != nil {
				continue
			}
			if fi.Size() < st.offset {
				st.offset = 0
			}
			if now-fi.ModTime().Unix() >= grace {
				// Untouched for longer than grace: nothing in it can count.
				// Skip the bytes rather than re-read every old rollout on
				// each daemon restart (see collectTranscriptSignals).
				st.offset = fi.Size()
			} else if fi.Size() > st.offset {
				if data, err := readFrom(path, st.offset); err == nil {
					// Consume whole lines only: Codex may be mid-write, and a
					// half line skipped now would be lost for good.
					if nl := bytes.LastIndexByte(data, '\n'); nl >= 0 {
						data = data[:nl+1]
						if tail, ok := parseCodexTail(data, st.cwd); ok {
							if tail.newestUnix > st.lastActivity {
								st.lastActivity = tail.newestUnix
							}
							if tail.sessionCwd != "" {
								st.cwd = tail.sessionCwd
							}
							if tail.commandCwd != "" {
								st.cmdCwd = tail.commandCwd
							}
						}
						st.offset += int64(len(data))
					}
				}
			}
			p.codexState[path] = st

			if now-st.lastActivity >= grace {
				continue
			}
			cwd := st.cmdCwd
			if cwd == "" {
				cwd = st.cwd
			}
			if cwd == "" || !cwdMatchesAnyPattern(cwd, snap.CwdPatterns) {
				continue
			}
			out = append(out, domain.Signal{
				Source:      domain.SourceTranscript,
				BinaryName:  "codex",
				Cwd:         cwd,
				WindowTitle: codexSessionID(name),
			})
		}
	}
	for path := range p.codexState {
		if !live[path] {
			delete(p.codexState, path)
		}
	}
	return out
}
