package daemon

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// decodeProjectDir reverses Claude Code's project-dir encoding (cwd path with
// '/' replaced by '-' and literal '-' escaped as '--'). This is lossy,
// so it is only a cheap hint; the authoritative cwd is read from the transcript
// body via parseTranscriptTail.
func decodeProjectDir(name string) string {
	// Decode: "--" is an escaped dash (literal "-"), single "-" is a path separator "/".
	// Strategy: "--" → placeholder, then "-" → "/", then placeholder → "-".
	const placeholder = "\x00"
	s := strings.ReplaceAll(name, "--", placeholder)
	s = strings.ReplaceAll(s, "-", "/")
	s = strings.ReplaceAll(s, placeholder, "-")
	return s
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
