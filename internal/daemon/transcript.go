package daemon

import (
	"bytes"
	"encoding/json"
	"io"
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

// collectTranscriptSignals scans transcript session files under
// cfg.TranscriptRoot and emits a transcript signal for each session whose
// resolved cwd is under a tracked prefix and whose newest turn is within the
// grace window. Tail-reads via per-session byte offset so large transcripts
// aren't re-parsed each tick.
func (p *Pipeline) collectTranscriptSignals(snap *CacheSnapshot, now int64) []domain.Signal {
	if !p.cfg.TranscriptTracking || p.cfg.TranscriptRoot == "" {
		return nil
	}
	grace := int64(p.cfg.TranscriptGraceSec)
	projDirs, err := os.ReadDir(p.cfg.TranscriptRoot)
	if err != nil {
		return nil // root absent ⇒ nothing to do
	}

	live := map[string]bool{}
	var out []domain.Signal
	for _, pd := range projDirs {
		if !pd.IsDir() {
			continue
		}
		sessDir := filepath.Join(p.cfg.TranscriptRoot, pd.Name())
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
			if fi.Size() > st.offset {
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
			if !cwdUnderAnyPrefix(cwd, snap.CwdPrefixes) {
				continue
			}
			if now-st.lastActivity >= grace {
				continue
			}
			sessionID := e.Name()[:len(e.Name())-len(".jsonl")]
			out = append(out, domain.Signal{
				Source:      domain.SourceTranscript,
				Cwd:         cwd,
				WindowTitle: sessionID,
			})
		}
	}
	// Prune state for files that no longer exist.
	for path := range p.transcriptState {
		if !live[path] {
			delete(p.transcriptState, path)
		}
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
