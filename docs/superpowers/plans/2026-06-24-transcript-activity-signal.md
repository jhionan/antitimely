# Transcript-Activity Signal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Capture Claude Code work (remote-driven and planning sessions) by ticking the matching project whenever a session transcript for a tracked cwd is being written — independent of CPU, focus, or HID idle.

**Architecture:** Add a third signal source (`transcript`) beside focus and CPU in the daemon's per-tick pipeline. A collector tail-reads `~/.claude/projects/<encoded-cwd>/*.jsonl`, resolves each session's cwd to a project via the existing cwd-prefix rules, and emits a signal while the session is within a grace window of its last turn. Transcript signals bypass the arming gate, disarm the project, and override pause; they flow through the existing `MatchRules` → `InsertTick` path.

**Tech Stack:** Go, `modernc.org/sqlite` (cgo-free), sqlc-generated `internal/store`, table-driven tests with `internal/macos.FakeBridge` and in-memory SQLite.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-06-24-transcript-activity-signal-design.md`.
- Observation `source` values are constrained by a CHECK; the new value is exactly `'transcript'`.
- Signals store **timing only** — `(ts, project)` ticks. Never derive or store step narrative (the feature-level description is the timesheet layer's job).
- Transcript activity **overrides pause** (auto-resumes a paused project and counts). Background *agent CPU* on a paused project stays suppressed (unchanged).
- Default grace window: **600 seconds (10 min)**. Default enabled. Default root: `$CLAUDE_CONFIG_DIR/projects` else `~/.claude/projects`.
- Follow existing patterns: idempotent startup migrations in `daemon.go`; pipeline tests via `newTestPipelineWithCfg`; cwd-prefix match semantics identical to `cwdUnderAnyPrefix` (equal or true subdir).
- Commit messages: no `Co-Authored-By` and no "Generated with Claude Code" footer. End each commit body with `Claude-Session: https://claude.ai/code/session_01DoZmnhAYdFKT6aAikxDT6D`.
- Branch: `jhionan/transcript-activity-signal` (already created and pushed).

---

## File Structure

- `internal/domain/types.go` — add `SourceTranscript` + `IsTranscript()`. (modify)
- `schema.sql` — observations `source` CHECK gains `'transcript'`. (modify)
- `internal/daemon/migrate_observations.go` — guarded CHECK-rebuild migration. (create)
- `internal/daemon/migrate_observations_test.go` — migration test. (create)
- `internal/daemon/transcript.go` — encoded-dir decode, jsonl tail parse, `collectTranscriptSignals`. (create)
- `internal/daemon/transcript_test.go` — pure-helper + collector tests. (create)
- `internal/daemon/pipeline.go` — add transcript config fields + per-session state; wire collection, disarm, paused-override, dedup into `RunTick`. (modify)
- `internal/daemon/pipeline_transcript_test.go` — RunTick integration tests. (create)
- `internal/daemon/daemon.go` — call the migration; thread config into `PipelineConfig`. (modify)
- `internal/daemon/config_file.go` — `transcript_tracking` / `transcript_grace` / `transcript_root` keys + defaults. (modify)
- `internal/daemon/config_file_test.go` — config parse test. (modify or create alongside existing)

Phase 2 (importer) files are listed in their tasks.

---

## Task 1: `transcript` source value + schema CHECK migration

**Files:**
- Modify: `internal/domain/types.go`
- Modify: `schema.sql` (observations CHECK)
- Create: `internal/daemon/migrate_observations.go`
- Test: `internal/daemon/migrate_observations_test.go`

**Interfaces:**
- Produces: `domain.SourceTranscript Source = "transcript"`; `func (s Signal) IsTranscript() bool`.
- Produces: `func migrateObservationsSourceCheck(db *sql.DB) error` — idempotent; rebuilds the `observations` table to allow `source='transcript'` if (and only if) the current table definition does not already allow it.

- [ ] **Step 1: Add the source constant + predicate (no test yet — trivial constant)**

In `internal/domain/types.go`, extend the const block and predicates:

```go
const (
	SourceFocus      Source = "focus"
	SourceAgent      Source = "agent"
	SourceTranscript Source = "transcript"
)
```

```go
func (s Signal) IsAgent() bool      { return s.Source == SourceAgent }
func (s Signal) IsFocus() bool      { return s.Source == SourceFocus }
func (s Signal) IsTranscript() bool { return s.Source == SourceTranscript }
```

- [ ] **Step 2: Update schema.sql for fresh DBs**

In `schema.sql`, change the observations CHECK line to:

```sql
    source          TEXT NOT NULL CHECK (source IN ('focus', 'agent', 'transcript')),
```

- [ ] **Step 3: Write the failing migration test**

Create `internal/daemon/migrate_observations_test.go`:

```go
package daemon

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// oldObservationsDDL is the pre-transcript table definition (2-value CHECK).
const oldObservationsDDL = `
CREATE TABLE observations (
    id              INTEGER PRIMARY KEY,
    source          TEXT NOT NULL CHECK (source IN ('focus', 'agent')),
    bundle_id       TEXT NOT NULL DEFAULT '',
    window_title    TEXT NOT NULL DEFAULT '',
    binary_name     TEXT NOT NULL DEFAULT '',
    cwd             TEXT NOT NULL DEFAULT '',
    first_seen      INTEGER NOT NULL,
    UNIQUE (source, bundle_id, window_title, binary_name, cwd)
) STRICT;`

func TestMigrateObservationsSourceCheck(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(oldObservationsDDL); err != nil {
		t.Fatalf("old ddl: %v", err)
	}
	// Seed an existing row to prove the rebuild preserves data + ids.
	if _, err := db.Exec(
		`INSERT INTO observations (id, source, binary_name, first_seen) VALUES (7, 'agent', 'claude', 100)`,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := migrateObservationsSourceCheck(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// transcript inserts must now succeed.
	if _, err := db.Exec(
		`INSERT INTO observations (source, cwd, first_seen) VALUES ('transcript', '/x', 200)`,
	); err != nil {
		t.Fatalf("transcript insert after migrate: %v", err)
	}
	// Existing row + id preserved.
	var name string
	if err := db.QueryRow(`SELECT binary_name FROM observations WHERE id=7`).Scan(&name); err != nil {
		t.Fatalf("preserved row: %v", err)
	}
	if name != "claude" {
		t.Fatalf("preserved row binary_name = %q, want claude", name)
	}
	// Idempotent: second call is a no-op and does not error.
	if err := migrateObservationsSourceCheck(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var ddl string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='observations'`,
	).Scan(&ddl); err != nil {
		t.Fatalf("read ddl: %v", err)
	}
	if !strings.Contains(ddl, "transcript") {
		t.Fatalf("post-migrate ddl missing transcript: %s", ddl)
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestMigrateObservationsSourceCheck -v`
Expected: FAIL — `undefined: migrateObservationsSourceCheck`.

- [ ] **Step 5: Implement the migration**

Create `internal/daemon/migrate_observations.go`:

```go
package daemon

import (
	"database/sql"
	"fmt"
	"strings"
)

// newObservationsDDL is the post-transcript table definition. Kept in sync with
// schema.sql; the rebuild below uses it so the migrated table's CHECK matches a
// freshly-created one exactly.
const newObservationsDDL = `
CREATE TABLE observations_new (
    id              INTEGER PRIMARY KEY,
    source          TEXT NOT NULL CHECK (source IN ('focus', 'agent', 'transcript')),
    bundle_id       TEXT NOT NULL DEFAULT '',
    window_title    TEXT NOT NULL DEFAULT '',
    binary_name     TEXT NOT NULL DEFAULT '',
    cwd             TEXT NOT NULL DEFAULT '',
    first_seen      INTEGER NOT NULL,
    UNIQUE (source, bundle_id, window_title, binary_name, cwd)
) STRICT;`

// migrateObservationsSourceCheck widens observations.source to allow
// 'transcript'. SQLite can't ALTER a CHECK in place, so we rebuild the table.
// Idempotent: if the current definition already permits 'transcript', it's a
// no-op. Preserves rows and ids (ticks.observation_id FK relies on id stability).
func migrateObservationsSourceCheck(db *sql.DB) error {
	var ddl string
	err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='observations'`,
	).Scan(&ddl)
	if err == sql.ErrNoRows {
		return nil // table not created yet; schema.sql will create the new form
	}
	if err != nil {
		return fmt.Errorf("read observations ddl: %w", err)
	}
	if strings.Contains(ddl, "transcript") {
		return nil // already migrated
	}

	// FK references (ticks.observation_id) must not trip during the swap; we
	// preserve ids so referential integrity holds across the rename.
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("fk off: %w", err)
	}
	defer db.Exec(`PRAGMA foreign_keys=ON`)

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	stmts := []string{
		newObservationsDDL,
		`INSERT INTO observations_new (id, source, bundle_id, window_title, binary_name, cwd, first_seen)
		   SELECT id, source, bundle_id, window_title, binary_name, cwd, first_seen FROM observations`,
		`DROP TABLE observations`,
		`ALTER TABLE observations_new RENAME TO observations`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("rebuild observations (%.40q): %w", s, err)
		}
	}
	return tx.Commit()
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run TestMigrateObservationsSourceCheck -v` and `go test ./internal/domain/ -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/domain/types.go schema.sql internal/daemon/migrate_observations.go internal/daemon/migrate_observations_test.go
git commit -m "feat(daemon): add transcript observation source + CHECK migration

Claude-Session: https://claude.ai/code/session_01DoZmnhAYdFKT6aAikxDT6D"
```

---

## Task 2: Transcript parse helpers (pure functions)

**Files:**
- Create: `internal/daemon/transcript.go`
- Test: `internal/daemon/transcript_test.go`

**Interfaces:**
- Produces: `func decodeProjectDir(name string) string` — `"-Users-rian-foo"` → `"/Users/rian/foo"` (best-effort hint only; authoritative cwd comes from the jsonl).
- Produces: `func parseTranscriptTail(data []byte) (cwd string, newestUnix int64, ok bool)` — scans JSONL lines; `newestUnix` is the max parseable `timestamp` (RFC3339, `Z`/offset); `cwd` is the last non-empty `cwd` field seen; `ok` is false when no timestamp was found. Unparseable lines are skipped.

- [ ] **Step 1: Write the failing tests**

Create `internal/daemon/transcript_test.go`:

```go
package daemon

import "testing"

func TestDecodeProjectDir(t *testing.T) {
	got := decodeProjectDir("-Users-rian-focaApp-bclouder-daas-daas-back-end")
	want := "/Users/rian/focaApp/bclouder/daas/daas-back-end"
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
	// 2026-06-24T02:30:29Z == 1782282629 unix.
	if newest != 1782282629 {
		t.Fatalf("newest = %d, want 1782282629", newest)
	}
}

func TestParseTranscriptTail_NoTimestamp(t *testing.T) {
	_, _, ok := parseTranscriptTail([]byte(`{"type":"summary"}` + "\n"))
	if ok {
		t.Fatal("ok = true, want false for entries without a timestamp")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/daemon/ -run 'TestDecodeProjectDir|TestParseTranscriptTail' -v`
Expected: FAIL — `undefined: decodeProjectDir` / `parseTranscriptTail`.

- [ ] **Step 3: Implement the helpers**

Create `internal/daemon/transcript.go`:

```go
package daemon

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// decodeProjectDir reverses Claude Code's project-dir encoding (cwd path with
// '/' replaced by '-'). This is lossy (a literal '-' in a path is ambiguous),
// so it is only a cheap hint; the authoritative cwd is read from the transcript
// body via parseTranscriptTail.
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run 'TestDecodeProjectDir|TestParseTranscriptTail' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/transcript.go internal/daemon/transcript_test.go
git commit -m "feat(daemon): transcript jsonl parse + project-dir decode helpers

Claude-Session: https://claude.ai/code/session_01DoZmnhAYdFKT6aAikxDT6D"
```

---

## Task 3: The collector — `collectTranscriptSignals`

**Files:**
- Modify: `internal/daemon/pipeline.go` (PipelineConfig fields, Pipeline state field, NewPipeline init)
- Modify: `internal/daemon/transcript.go` (the collector method)
- Test: `internal/daemon/transcript_test.go` (append)

**Interfaces:**
- Consumes: `parseTranscriptTail`, `cwdUnderAnyPrefix` (exists in pipeline.go), `domain.SourceTranscript`.
- Produces: `func (p *Pipeline) collectTranscriptSignals(snap *CacheSnapshot, now int64) []domain.Signal`. Emits one signal per session whose resolved cwd is under a tracked prefix and whose last activity is within `TranscriptGraceSec` of `now`. Signal: `{Source: SourceTranscript, Cwd: <cwd>, WindowTitle: <session-id>}`.
- Produces (config): `PipelineConfig.TranscriptTracking bool`, `PipelineConfig.TranscriptRoot string`, `PipelineConfig.TranscriptGraceSec int`.

- [ ] **Step 1: Add config fields + per-session state to the Pipeline**

In `internal/daemon/pipeline.go`, add to `PipelineConfig`:

```go
	// TranscriptTracking enables the Claude Code transcript signal source.
	TranscriptTracking bool
	// TranscriptRoot is the dir holding per-cwd session subdirs
	// (~/.claude/projects). Empty ⇒ transcript tracking is inert.
	TranscriptRoot string
	// TranscriptGraceSec: a session counts as actively worked for this many
	// seconds after its newest turn, stitching gaps between turns into one
	// continuous billable block.
	TranscriptGraceSec int
```

Add a state map + type. Near `activityState`:

```go
// transcriptSession is per-session-file bookkeeping so each tick only reads new
// tail bytes rather than re-parsing the whole transcript.
type transcriptSession struct {
	offset       int64  // bytes consumed so far
	lastActivity int64  // unix seconds of newest entry seen
	cwd          string // authoritative cwd from the transcript body
}
```

Add the field to `Pipeline` (next to `procActivity`):

```go
	// transcriptState is per-session-file tail/offset + last-activity state,
	// keyed by absolute .jsonl path.
	transcriptState map[string]transcriptSession
```

Initialize it in `NewPipeline`:

```go
		transcriptState:  map[string]transcriptSession{},
```

- [ ] **Step 2: Write the failing collector tests**

Append to `internal/daemon/transcript_test.go`:

```go
import (
	"os"
	"path/filepath"
	"testing"
)
// (merge imports with the existing file; testing is already imported)

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
	now := int64(1782282700)
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
	now := int64(1782282700)
	body := `{"cwd":"/work/other","timestamp":"2026-06-24T02:30:29Z"}` + "\n"
	writeSession(t, root, "-work-other", "sess1", body)
	p, _ := newTranscriptPipeline(t, root, 600, []string{"/work/daas"})

	if sigs := p.collectTranscriptSignals(p.cache.Snapshot(), now); len(sigs) != 0 {
		t.Fatalf("got %d signals, want 0 (untracked cwd)", len(sigs))
	}
}
```

This test needs a cache test helper `swapForTest`. Add it in `internal/daemon/cache.go`:

```go
// swapForTest installs a snapshot directly. Test-only; production swaps via
// ReloadCache.
func (c *Cache) swapForTest(s *CacheSnapshot) { c.ptr.Store(s) }
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/daemon/ -run TestCollectTranscript -v`
Expected: FAIL — `p.collectTranscriptSignals undefined`.

- [ ] **Step 4: Implement the collector**

Append to `internal/daemon/transcript.go` (add imports `os`, `path/filepath`, and `github.com/rian/antitimely/internal/domain`):

```go
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
```

Add `"io"` to the imports.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run TestCollectTranscript -v`
Expected: PASS (all three).

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/transcript.go internal/daemon/transcript_test.go internal/daemon/pipeline.go internal/daemon/cache.go
git commit -m "feat(daemon): transcript collector with grace window + tail-read state

Claude-Session: https://claude.ai/code/session_01DoZmnhAYdFKT6aAikxDT6D"
```

---

## Task 4: Wire transcript signals into `RunTick` (paused-override, disarm, dedup)

**Files:**
- Modify: `internal/daemon/pipeline.go` (`RunTick`)
- Test: `internal/daemon/pipeline_transcript_test.go`

**Interfaces:**
- Consumes: `collectTranscriptSignals`, `domain.Signal.IsTranscript()`, existing `MatchRules`, cache arm/pause helpers.
- Produces: transcript ticks landing on the matched project even when HID idle is high and CPU is zero; paused projects auto-resumed by transcript activity; at most one tick per project per cycle when transcript coincides with focus/agent.

- [ ] **Step 1: Write the failing integration tests**

Create `internal/daemon/pipeline_transcript_test.go`. These reuse the in-memory harness; they seed a project with a cwd-prefix rule and a transcript file, then assert ticks. Helper SQL mirrors existing pipeline tests.

```go
package daemon

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// seedProjectWithCwdRule inserts a project + a cwd-prefix rule and reloads the
// cache so MatchRules and CwdPrefixes both cover prefix.
func seedProjectWithCwdRule(t *testing.T, db *sql.DB, cache *Cache, name, prefix string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO projects (name, created_at) VALUES (?, 0)`, name)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
	pid, _ := res.LastInsertId()
	if _, err := db.Exec(
		`INSERT INTO rules (project_id, priority, match_cwd_prefix) VALUES (?, 0, ?)`, pid, prefix,
	); err != nil {
		t.Fatalf("insert rule: %v", err)
	}
	reloadCacheForTest(t, db, cache)
	return pid
}

func countTicks(t *testing.T, db *sql.DB, projectID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ticks WHERE project_id=?`, projectID).Scan(&n); err != nil {
		t.Fatalf("count ticks: %v", err)
	}
	return n
}

func TestRunTick_TranscriptCountsWhileIdleAndZeroCPU(t *testing.T) {
	root := t.TempDir()
	now := int64(1782282700)
	prefix := "/work/daas"
	writeSession(t, root, "-work-daas", "s1",
		`{"cwd":"/work/daas","timestamp":"2026-06-24T02:30:29Z"}`+"\n") // ~71s before now

	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     5,
		CPUDeltaThreshIdle: 5,
		TranscriptTracking: true,
		TranscriptRoot:     root,
		TranscriptGraceSec: 600,
	})
	defer db.Close()
	pid := seedProjectWithCwdRule(t, db, cache, "Daas", prefix)

	br.IdleSecondsVal = 9999 // fully idle (remote)
	br.Processes = nil       // zero CPU

	if err := p.RunTick(context.Background(), now); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if got := countTicks(t, db, pid); got != 1 {
		t.Fatalf("ticks = %d, want 1 (transcript should count while idle/zero-CPU)", got)
	}
}

func TestRunTick_TranscriptResumesPausedProject(t *testing.T) {
	root := t.TempDir()
	now := int64(1782282700)
	writeSession(t, root, "-work-daas", "s1",
		`{"cwd":"/work/daas","timestamp":"2026-06-24T02:30:29Z"}`+"\n")
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec: 120, CPUDeltaThresh: 5, CPUDeltaThreshIdle: 5,
		TranscriptTracking: true, TranscriptRoot: root, TranscriptGraceSec: 600,
	})
	defer db.Close()
	pid := seedProjectWithCwdRule(t, db, cache, "Daas", "/work/daas")
	// Pause it.
	if _, err := db.Exec(`UPDATE projects SET paused=1 WHERE id=?`, pid); err != nil {
		t.Fatal(err)
	}
	reloadCacheForTest(t, db, cache)
	br.IdleSecondsVal = 9999
	br.Processes = nil

	if err := p.RunTick(context.Background(), now); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if got := countTicks(t, db, pid); got != 1 {
		t.Fatalf("ticks = %d, want 1 (transcript overrides pause)", got)
	}
	var paused int
	db.QueryRow(`SELECT paused FROM projects WHERE id=?`, pid).Scan(&paused)
	if paused != 0 {
		t.Fatalf("project still paused; transcript should auto-resume")
	}
}
```

If `reloadCacheForTest` does not already exist in the test package, add this helper in `pipeline_transcript_test.go`:

```go
// reloadCacheForTest rebuilds the cache snapshot from db. Mirrors what the
// daemon's ReloadCache does — enough for pipeline tests.
func reloadCacheForTest(t *testing.T, db *sql.DB, cache *Cache) {
	t.Helper()
	svc := &AntitimelyService{DB: db, Q: storeNew(db), Cache: cache}
	if err := svc.ReloadCache(); err != nil {
		t.Fatalf("ReloadCache: %v", err)
	}
}
```

…where `storeNew` is `store.New`. Import `"github.com/rian/antitimely/internal/store"` and replace `storeNew(db)` with `store.New(db)`. (If an equivalent reload helper already exists in the test files, use that instead and drop this.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/daemon/ -run TestRunTick_Transcript -v`
Expected: FAIL — transcript signals not yet collected/handled in `RunTick` (ticks = 0).

- [ ] **Step 3: Collect transcript signals in RunTick**

In `internal/daemon/pipeline.go` `RunTick`, after the agent-signals append, add transcript collection (always — not gated by `userPresent`):

```go
	signals = append(signals, p.collectAgentSignals(ctx, snap, userPresent)...)
	signals = append(signals, p.collectTranscriptSignals(snap, now)...)
```

- [ ] **Step 4: Handle disarm + paused-override + dedup for transcript signals**

In the per-signal loop:

(a) Extend the disarm clause to include transcript:

```go
		if (sig.IsFocus() || sig.IsTranscript()) && pid != nil {
			p.cache.DisarmProject(*pid)
			delete(p.armedAgentStreak, *pid)
			disarmedThisTick[*pid] = true
		}
```

(b) Extend the paused branch so transcript resumes regardless of idle:

```go
		if pid != nil && snap.PausedProjectIDs[*pid] {
			resume := (sig.IsAgent() && userPresent) || sig.IsTranscript()
			if !resume {
				continue
			}
			if err := p.q.ResumeProjectByID(ctx, *pid); err != nil {
				log.Printf("auto-resume project %d: %v", *pid, err)
				continue
			}
			p.cache.MarkProjectActive(*pid)
			p.cache.ArmProject(*pid)
			delete(p.armedAgentStreak, *pid)
			log.Printf("auto-resumed project %d: %s activity (binary=%q cwd=%q)", *pid, sig.Source, sig.BinaryName, sig.Cwd)
			continue
		}
```

Note: a transcript signal that resumes a paused project `continue`s this tick (matching the existing agent-resume behavior — the resume itself is the signal; the next tick counts). This is intentional and the test seeds a *single* tick expectation by NOT pre-pausing? Re-check: `TestRunTick_TranscriptResumesPausedProject` expects 1 tick. To make the resume tick *also* count this cycle, do NOT `continue` for transcript resumes — fall through to InsertTick. Adjust:

```go
		if pid != nil && snap.PausedProjectIDs[*pid] {
			switch {
			case sig.IsTranscript():
				// Real work — resume and let this tick count.
				if err := p.q.ResumeProjectByID(ctx, *pid); err != nil {
					log.Printf("auto-resume project %d: %v", *pid, err)
					continue
				}
				p.cache.MarkProjectActive(*pid)
				delete(p.armedAgentStreak, *pid)
				disarmedThisTick[*pid] = true
				log.Printf("auto-resumed project %d: transcript activity (cwd=%q)", *pid, sig.Cwd)
				// fall through to InsertTick below.
			case sig.IsAgent() && userPresent:
				if err := p.q.ResumeProjectByID(ctx, *pid); err != nil {
					log.Printf("auto-resume project %d: %v", *pid, err)
					continue
				}
				p.cache.MarkProjectActive(*pid)
				p.cache.ArmProject(*pid)
				delete(p.armedAgentStreak, *pid)
				log.Printf("auto-resumed project %d: agent activity (binary=%q cwd=%q)", *pid, sig.BinaryName, sig.Cwd)
				continue
			default:
				continue
			}
		}
```

(c) Add per-project dedup so a transcript signal doesn't double-tick a project already ticked by focus/CPU this cycle. Declare a set near `disarmedThisTick`:

```go
	tickedThisTick := map[int64]bool{}
```

Right before the final `InsertTick`, guard transcript duplicates and record ticks:

```go
		if pid != nil {
			if sig.IsTranscript() && tickedThisTick[*pid] {
				continue // focus/CPU already counted this project this cycle
			}
		}
		var projectID sql.NullInt64
		if pid != nil {
			projectID = sql.NullInt64{Int64: *pid, Valid: true}
		}
		if err := p.q.InsertTick(ctx, store.InsertTickParams{
			Ts: now, ObservationID: obsID, ProjectID: projectID,
		}); err != nil {
			log.Printf("insert tick: %v", err)
			continue
		}
		if pid != nil {
			tickedThisTick[*pid] = true
		}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run TestRunTick_Transcript -v`
Expected: PASS (both). Then run the full daemon suite to catch regressions:
Run: `go test ./internal/daemon/ -v`
Expected: PASS (existing focus/agent/arming tests unaffected — transcript is opt-in via config and disabled by default in `newTestPipeline`).

- [ ] **Step 6: Add the dedup test**

Append to `pipeline_transcript_test.go`:

```go
func TestRunTick_TranscriptDedupWithFocus(t *testing.T) {
	root := t.TempDir()
	now := int64(1782282700)
	writeSession(t, root, "-work-daas", "s1",
		`{"cwd":"/work/daas","timestamp":"2026-06-24T02:30:29Z"}`+"\n")
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec: 120, CPUDeltaThresh: 5, CPUDeltaThreshIdle: 5,
		TranscriptTracking: true, TranscriptRoot: root, TranscriptGraceSec: 600,
	})
	defer db.Close()
	pid := seedProjectWithCwdRule(t, db, cache, "Daas", "/work/daas")
	// Add a focus rule on a bundle and present a matching frontmost window so
	// focus ALSO maps to Daas this tick.
	if _, err := db.Exec(`INSERT INTO rules (project_id, priority, match_bundle_id) VALUES (?, 0, 'com.daas.app')`, pid); err != nil {
		t.Fatal(err)
	}
	reloadCacheForTest(t, db, cache)
	br.IdleSecondsVal = 0 // present, so focus is collected
	br.FrontmostInfoVal.BundleID = "com.daas.app"
	// allow the bundle
	// (AllowedBundles is derived from watched programs in ReloadCache; if the
	// harness needs it, insert a watched_programs row for com.daas.app before
	// reload.)

	if err := p.RunTick(context.Background(), now); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if got := countTicks(t, db, pid); got != 1 {
		t.Fatalf("ticks = %d, want 1 (focus+transcript dedup to one project tick)", got)
	}
}
```

If wiring `AllowedBundles` in the harness proves heavy, simplify this test to assert the dedup invariant directly: collect focus+transcript signals for the same pid and assert one tick. Keep the test only if it stays within the existing harness's reach; otherwise drop it and rely on the dedup guard's unit coverage. (Reviewer's call — do not block the task on bundle plumbing.)

- [ ] **Step 7: Run + commit**

Run: `go test ./internal/daemon/ -v`
Expected: PASS.

```bash
git add internal/daemon/pipeline.go internal/daemon/pipeline_transcript_test.go
git commit -m "feat(daemon): tick on transcript activity — bypass idle, override pause, dedup

Claude-Session: https://claude.ai/code/session_01DoZmnhAYdFKT6aAikxDT6D"
```

---

## Task 5: Config keys, defaults, and daemon wiring

**Files:**
- Modify: `internal/daemon/config_file.go` (FileConfig fields + mapping + defaults)
- Modify: `internal/daemon/daemon.go` (call migration; thread config into PipelineConfig)
- Modify: `internal/daemon/daemon.go` Config struct (add fields)
- Test: `internal/daemon/config_file_test.go`

**Interfaces:**
- Consumes: `migrateObservationsSourceCheck`, the `PipelineConfig.Transcript*` fields.
- Produces: yaml keys `transcript_tracking` (bool, default true), `transcript_grace` (Go duration, default `10m`), `transcript_root` (path, default `$CLAUDE_CONFIG_DIR/projects` else `~/.claude/projects`). New `Config` fields `TranscriptTracking bool`, `TranscriptGraceSec int`, `TranscriptRoot string`.

- [ ] **Step 1: Write the failing config test**

In `internal/daemon/config_file_test.go` (create if absent; otherwise append), add:

```go
func TestParseConfig_TranscriptDefaultsAndOverrides(t *testing.T) {
	// Defaults: tracking on, 600s grace, non-empty root.
	def := defaultFileConfigForTest(t, "")
	if !def.TranscriptTracking {
		t.Fatal("transcript tracking should default on")
	}
	if def.TranscriptGraceSec != 600 {
		t.Fatalf("grace default = %d, want 600", def.TranscriptGraceSec)
	}
	if def.TranscriptRoot == "" {
		t.Fatal("transcript root should default to a path")
	}

	// Overrides.
	yaml := "transcript_tracking: false\ntranscript_grace: 5m\ntranscript_root: /tmp/x\n"
	cfg := defaultFileConfigForTest(t, yaml)
	if cfg.TranscriptTracking {
		t.Fatal("override should disable tracking")
	}
	if cfg.TranscriptGraceSec != 300 {
		t.Fatalf("grace = %d, want 300", cfg.TranscriptGraceSec)
	}
	if cfg.TranscriptRoot != "/tmp/x" {
		t.Fatalf("root = %q, want /tmp/x", cfg.TranscriptRoot)
	}
}
```

`defaultFileConfigForTest` writes `yaml` (if non-empty) to a temp file, runs the same parse path the daemon uses, and returns the resolved `Config`. Implement it next to the test using the package's existing parse entrypoint (the function that turns a `FileConfig`/path into `Config` — match whatever `config_file.go` exposes; e.g. `LoadConfig(path)` or `applyFileConfig`). If the existing parser is `func Load(path string) (Config, error)`, the helper calls it.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestParseConfig_Transcript -v`
Expected: FAIL — fields/keys undefined.

- [ ] **Step 3: Add FileConfig keys + Config fields + mapping**

In `internal/daemon/config_file.go`, add to the `FileConfig` struct:

```go
	TranscriptTracking *bool  `yaml:"transcript_tracking,omitempty"`
	TranscriptGrace    string `yaml:"transcript_grace,omitempty"`
	TranscriptRoot     string `yaml:"transcript_root,omitempty"`
```

(Use `*bool` so "unset" is distinguishable from explicit `false`.)

In `internal/daemon/daemon.go`, add to `Config`:

```go
	TranscriptTracking bool
	TranscriptGraceSec int
	TranscriptRoot     string
```

In the config mapping (where defaults are set before applying the file), set defaults, then apply overrides:

```go
	// defaults
	cfg.TranscriptTracking = true
	cfg.TranscriptGraceSec = 600
	cfg.TranscriptRoot = defaultTranscriptRoot()
```

```go
	if fc.TranscriptTracking != nil {
		cfg.TranscriptTracking = *fc.TranscriptTracking
	}
	if fc.TranscriptGrace != "" {
		if d, err := time.ParseDuration(fc.TranscriptGrace); err == nil && d > 0 {
			cfg.TranscriptGraceSec = int(d.Seconds())
		}
	}
	if fc.TranscriptRoot != "" {
		if p, err := expandHome(fc.TranscriptRoot); err == nil {
			cfg.TranscriptRoot = p
		}
	}
```

Add `defaultTranscriptRoot` in `config_file.go`:

```go
// defaultTranscriptRoot is $CLAUDE_CONFIG_DIR/projects when set, else
// ~/.claude/projects.
func defaultTranscriptRoot() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}
```

(Ensure `os`, `path/filepath`, `time` are imported.)

- [ ] **Step 4: Run config test to verify it passes**

Run: `go test ./internal/daemon/ -run TestParseConfig_Transcript -v`
Expected: PASS.

- [ ] **Step 5: Wire migration + config into daemon boot**

In `internal/daemon/daemon.go` `Run`, after the existing invoice migrations loop, call:

```go
	if err := migrateObservationsSourceCheck(db); err != nil {
		return fmt.Errorf("migrate observations source: %w", err)
	}
```

In the `NewPipeline(...)` call, add the transcript fields:

```go
	pipeline := NewPipeline(q, bridge, cache, PipelineConfig{
		IdleThresholdSec:     cfg.IdleThresholdSec,
		CPUDeltaThresh:       cfg.AgentCPUThresh,
		CPUDeltaThreshIdle:   cfg.AgentCPUThreshIdle,
		AutoDisarmAgentTicks: autoDisarmTicks,
		AgentBusyRiseTicks:   cfg.AgentBusyRiseTicks,
		AgentBusyFallTicks:   cfg.AgentBusyFallTicks,
		TranscriptTracking:   cfg.TranscriptTracking,
		TranscriptRoot:       cfg.TranscriptRoot,
		TranscriptGraceSec:   cfg.TranscriptGraceSec,
	})
```

- [ ] **Step 6: Run full suite + build**

Run: `go build ./... && go test ./...`
Expected: PASS / build clean.

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/config_file.go internal/daemon/daemon.go internal/daemon/config_file_test.go
git commit -m "feat(daemon): transcript config keys + boot wiring + migration call

Claude-Session: https://claude.ai/code/session_01DoZmnhAYdFKT6aAikxDT6D"
```

---

## Task 6: Status annotation + config-template docs

**Files:**
- Modify: `internal/daemon/config_file.go` (the commented config template block — add the three keys with explanations)
- Modify: `README.md` (document the transcript signal in the tracking section)
- Modify: status rendering (the file that builds the `(armed: needs focus)` annotation) — add `(live: claude-code)` when a project was ticked by a transcript signal this session.

**Interfaces:**
- Consumes: nothing new. This is presentation only.

- [ ] **Step 1: Locate the status annotation site**

Run: `grep -rn "armed: needs focus" internal/`
Use the file/line it reports as the insertion point.

- [ ] **Step 2: Add the config-template comment block**

In `config_file.go`'s embedded default config text (where `interval` / `idle_threshold` are documented), add:

```
# Count Claude Code work (incl. remote-driven and planning sessions) by watching
# session transcripts. Captures work that produces no local CPU/focus.
# transcript_tracking: true
# transcript_grace: 10m          # a session counts this long after its last turn
# transcript_root: ~/.claude/projects
```

- [ ] **Step 3: Document in README**

Add a short subsection under the tracking/signals docs explaining the three signal sources (focus, agent CPU, transcript) and that transcript activity overrides pause and is bounded by the grace window.

- [ ] **Step 4: (Optional) status annotation**

If feasible within the existing status struct, surface `(live: claude-code)` for a project with transcript ticks in the current day. If the status layer has no per-source breakdown readily available, skip this and note it as a follow-up — do not expand the RPC surface just for the annotation.

- [ ] **Step 5: Commit**

```bash
git add README.md internal/daemon/config_file.go internal/daemon/*.go
git commit -m "docs(daemon): document transcript signal + config keys; status annotation

Claude-Session: https://claude.ai/code/session_01DoZmnhAYdFKT6aAikxDT6D"
```

---

## Task 7 (Phase 2): `atl import-transcripts` historical back-fill

**Files:**
- Create: `internal/daemon/transcript_import.go` (replay logic, reuses `parseTranscriptTail` + grace stitching)
- Create: `internal/daemon/transcript_import_test.go`
- Modify: `internal/rpcapi/api.go` (args/reply types)
- Modify: `internal/daemon/rpc.go` (RPC handler) or a new `rpc_transcript.go`
- Modify: `internal/cli/dispatch.go` (`import-transcripts` subcommand) + new `internal/cli/import.go`

**Interfaces:**
- Produces: `func importTranscripts(db *sql.DB, q *store.Queries, snap *CacheSnapshot, root string, fromUnix, toUnix int64, graceSec, intervalSec int) (inserted int, err error)`. Replays every session's entries in `[from,to]`, stitches turns within `graceSec` into continuous blocks, and inserts `source='transcript'` ticks on the matched project at `intervalSec` spacing, skipping `(ts, project)` already present.
- Produces RPC `TranscriptImport(TranscriptImportArgs{FromUnix, ToUnix int64}) TranscriptImportReply{Inserted int}` and CLI `atl import-transcripts [--from=YYYY-MM-DD] [--to=YYYY-MM-DD]`.

- [ ] **Step 1: Write the failing importer test**

Create `internal/daemon/transcript_import_test.go`:

```go
package daemon

import (
	"context"
	"testing"
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

	from := int64(1782280800) // 2026-06-24T02:00:00Z
	to := int64(1782283200)   // 2026-06-24T03:00:00Z
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestImportTranscripts -v`
Expected: FAIL — `undefined: importTranscripts`.

- [ ] **Step 3: Implement the importer**

Create `internal/daemon/transcript_import.go`. Algorithm: for each session file, collect all entry `(unixTs)` within `[from,to]` (reusing a per-line variant of `parseTranscriptTail`); sort; split into blocks where consecutive gaps exceed `graceSec`; for each block fill ticks from `blockStart` to `blockEnd` (inclusive) every `intervalSec`; map cwd via `MatchRules` against a synthetic `domain.Signal{Source: SourceTranscript, Cwd: cwd}`; upsert a `source='transcript'` observation; `INSERT OR IGNORE`-style skip via the ticks PK `(ts, observation_id)` plus a pre-check on `(ts, project_id)`.

```go
package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rian/antitimely/internal/domain"
	"github.com/rian/antitimely/internal/store"
)

func importTranscripts(db *sql.DB, q *store.Queries, snap *CacheSnapshot, root string, fromUnix, toUnix int64, graceSec, intervalSec int) (int, error) {
	ctx := context.Background()
	projDirs, err := os.ReadDir(root)
	if err != nil {
		return 0, nil
	}
	inserted := 0
	for _, pd := range projDirs {
		if !pd.IsDir() {
			continue
		}
		sessDir := filepath.Join(root, pd.Name())
		files, err := os.ReadDir(sessDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".jsonl" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(sessDir, f.Name()))
			if err != nil {
				continue
			}
			cwd, stamps := scanEntries(data, fromUnix, toUnix)
			if cwd == "" {
				cwd = decodeProjectDir(pd.Name())
			}
			if len(stamps) == 0 {
				continue
			}
			pid := domain.MatchRules(domain.Signal{Source: domain.SourceTranscript, Cwd: cwd}, snap.Rules)
			if pid == nil {
				continue
			}
			obsID, err := q.UpsertObservation(ctx, store.UpsertObservationParams{
				Source: string(domain.SourceTranscript), Cwd: cwd,
				WindowTitle: strings.TrimSuffix(f.Name(), ".jsonl"), FirstSeen: stamps[0],
			})
			if err != nil {
				continue
			}
			for _, blk := range splitBlocks(stamps, int64(graceSec)) {
				for ts := blk[0]; ts <= blk[1]; ts += int64(intervalSec) {
					var exists int
					db.QueryRowContext(ctx, `SELECT 1 FROM ticks WHERE ts=? AND project_id=?`, ts, *pid).Scan(&exists)
					if exists == 1 {
						continue
					}
					if err := q.InsertTick(ctx, store.InsertTickParams{
						Ts: ts, ObservationID: obsID, ProjectID: sql.NullInt64{Int64: *pid, Valid: true},
					}); err == nil {
						inserted++
					}
				}
			}
		}
	}
	return inserted, nil
}

// scanEntries returns the last cwd and all in-range entry timestamps (sorted).
func scanEntries(data []byte, fromUnix, toUnix int64) (string, []int64) {
	var cwd string
	var out []int64
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e transcriptEntry
		if json.Unmarshal([]byte(line), &e) != nil {
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
		u := t.Unix()
		if u >= fromUnix && u <= toUnix {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return cwd, out
}

// splitBlocks groups sorted timestamps into [start,end] blocks, breaking where
// the gap between consecutive stamps exceeds graceSec.
func splitBlocks(stamps []int64, graceSec int64) [][2]int64 {
	if len(stamps) == 0 {
		return nil
	}
	var blocks [][2]int64
	start, prev := stamps[0], stamps[0]
	for _, s := range stamps[1:] {
		if s-prev > graceSec {
			blocks = append(blocks, [2]int64{start, prev})
			start = s
		}
		prev = s
	}
	blocks = append(blocks, [2]int64{start, prev})
	return blocks
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestImportTranscripts -v`
Expected: PASS.

- [ ] **Step 5: Add RPC + CLI**

In `internal/rpcapi/api.go`:

```go
type TranscriptImportArgs struct{ FromUnix, ToUnix int64 }
type TranscriptImportReply struct{ Inserted int }
```

Add a handler (new `internal/daemon/rpc_transcript.go`) that resolves the configured root/grace/interval from the service and calls `importTranscripts`. The service already holds `Q`, `DB`, `Cache`, `TickIntervalSeconds`; thread the transcript root/grace onto `AntitimelyService` at construction (in `Run`) so the handler can read them.

```go
func (s *AntitimelyService) TranscriptImport(args rpcapi.TranscriptImportArgs, reply *rpcapi.TranscriptImportReply) error {
	n, err := importTranscripts(s.DB, s.Q, s.Cache.Snapshot(), s.TranscriptRoot, args.FromUnix, args.ToUnix, s.TranscriptGraceSec, s.TickIntervalSeconds)
	if err != nil {
		return err
	}
	reply.Inserted = n
	return nil
}
```

In `internal/cli/dispatch.go` add `case "import-transcripts": return importTranscriptsCmd(args[1:])` and create `internal/cli/import.go` mirroring `invoiceGenerate`'s flag parsing (`--from`, `--to` via `parseOptionalDate`), dialing the daemon and printing `Imported N ticks`.

- [ ] **Step 6: Run + commit**

Run: `go build ./... && go test ./...`

```bash
git add internal/daemon/transcript_import.go internal/daemon/transcript_import_test.go internal/rpcapi/api.go internal/daemon/rpc_transcript.go internal/daemon/daemon.go internal/cli/dispatch.go internal/cli/import.go
git commit -m "feat: atl import-transcripts — back-fill ticks from historical transcripts

Claude-Session: https://claude.ai/code/session_01DoZmnhAYdFKT6aAikxDT6D"
```

---

## Self-Review

**Spec coverage:**
- Third signal source / collector → Task 3, wired in Task 4. ✓
- cwd→project via existing prefix rules → Task 3 (`cwdUnderAnyPrefix`) + Task 4 (`MatchRules`). ✓
- Grace-window session stitching → Task 3 (live) + Task 7 (`splitBlocks`). ✓
- Bypass arming gate / disarm → Task 4 (a). ✓
- Transcript overrides pause (decision #2) → Task 4 (b). ✓
- Dedup per project per tick → Task 4 (c). ✓
- Schema CHECK + migration → Task 1. ✓
- Config keys + defaults + root resolution → Task 5. ✓
- Status/docs → Task 6. ✓
- Phase-2 importer → Task 7. ✓
- Granularity principle (ticks store time only, no narrative) → honored: no task derives step text; importer/collector emit only ticks. ✓
- Tail-read efficiency → Task 3 (offset state). ✓

**Placeholder scan:** No "TBD"/"handle edge cases" without code. Task 4 Step 6 and Task 6 Step 4 are explicitly marked reviewer-optional with a fallback, not vague placeholders.

**Type consistency:** `collectTranscriptSignals(snap, now)`, `parseTranscriptTail([]byte) (string,int64,bool)`, `migrateObservationsSourceCheck(db)`, `importTranscripts(db,q,snap,root,from,to,graceSec,intervalSec)`, `PipelineConfig.Transcript{Tracking,Root,GraceSec}`, `transcriptSession{offset,lastActivity,cwd}` are used consistently across tasks. `SourceTranscript`/`IsTranscript` defined in Task 1, used in Tasks 3–7.

**Note for the implementer:** confirm the exact config-parse entrypoint name in `config_file.go` (Task 5 references it generically) and the status-annotation file (Task 6 Step 1 locates it via grep) before writing those tasks — both are existing-code touchpoints whose names should be read, not assumed.
