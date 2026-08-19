# Space- and Glob-Based Attribution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Attribute time correctly when several herdr spaces share one working directory, and when MD work happens in short-lived `md-*` git worktrees.

**Architecture:** Two new match clauses in the existing rule engine — a glob form of `match_cwd_prefix`, and a new `match_space_id` resolved from herdr's `HERDR_PANE_ID` environment variable plus `~/.config/herdr/session.json`. Rule precedence is unchanged: priority ascending, then id ascending. Every new input is optional and degrades to today's behaviour.

**Tech Stack:** Go (no CGO), `modernc.org/sqlite`, sqlc-generated store, stdlib `flag` CLI, `net/rpc` over a Unix socket.

## Global Constraints

- **Never hand-edit `internal/store/*.go`.** It is sqlc-generated. SQL changes go in `schema.sql` / `queries.sql`, then `make sqlc`.
- **Keep `queries.sql` ASCII-only.** sqlc v1.30.0 tracks query text by byte offset but comment length in runes; one multi-byte character silently corrupts every query below it.
- **All subprocesses live in `internal/macos`.** No `exec.Command` anywhere else.
- **`internal/domain` stays pure and zero-dependency** (stdlib only).
- **`*` is permitted only in the final path segment** of a cwd pattern. SQLite `GLOB` lets `*` cross `/`; Go's `path.Match` does not. This restriction is what keeps the two matchers in agreement.
- **Match key for spaces is the workspace id** (e.g. `wN`), never the display name. A rename must not change attribution.
- **New CLI subcommands update both the dispatch switch and `printUsage`** (`internal/cli/dispatch.go`).
- Build with `make build` (never plain `go build` — it reverts to an ad-hoc signature and breaks the Accessibility grant).
- Test with `make test` (`go test ./... -count=1`).

---

### Task 1: Glob matching in the domain matcher

**Files:**
- Modify: `internal/domain/match.go`
- Modify: `internal/domain/types.go`
- Test: `internal/domain/match_test.go`

**Interfaces:**
- Consumes: nothing (pure, first task).
- Produces: `domain.MatchesCwd(pattern, cwd string) bool`; `domain.ValidateCwdPattern(pattern string) error`; `domain.RuleSpec.MatchSpaceID *string`; `domain.Signal.SpaceID string`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/domain/match_test.go`:

```go
func TestMatchesCwd(t *testing.T) {
	const wt = "/Users/rian/focaApp/bclouder/daas/daas-back-end/.claude/worktrees"
	cases := []struct {
		name    string
		pattern string
		cwd     string
		want    bool
	}{
		{"literal exact", wt + "/md-tracker", wt + "/md-tracker", true},
		{"literal subdir", wt + "/md-tracker", wt + "/md-tracker/DAAS.API", true},
		{"literal sibling not matched", wt + "/md-tracker", wt + "/md-tracker-scaffold", false},
		{"literal trailing slash", wt + "/md-tracker/", wt + "/md-tracker/DAAS.API", true},
		{"glob worktree", wt + "/md-*", wt + "/md-engine", true},
		{"glob nested build dir", wt + "/md-*", wt + "/md-engine/DAAS.Application.Services.Gos", true},
		{"glob future worktree", wt + "/md-*", wt + "/md-anything-new", true},
		{"glob excludes non-md", wt + "/md-*", wt + "/packing-slip-default-assignee", false},
		{"glob excludes repo root", wt + "/md-*", "/Users/rian/focaApp/bclouder/daas/daas-back-end", false},
		{"glob excludes worktrees parent", wt + "/md-*", wt, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchesCwd(tc.pattern, tc.cwd); got != tc.want {
				t.Fatalf("MatchesCwd(%q, %q) = %v, want %v", tc.pattern, tc.cwd, got, tc.want)
			}
		})
	}
}

func TestValidateCwdPattern(t *testing.T) {
	if err := ValidateCwdPattern("/a/b/md-*"); err != nil {
		t.Fatalf("star in final segment must be allowed: %v", err)
	}
	if err := ValidateCwdPattern("/a/b/c"); err != nil {
		t.Fatalf("literal must be allowed: %v", err)
	}
	if err := ValidateCwdPattern("/a/*/md-x"); err == nil {
		t.Fatal("star before the last / must be rejected")
	}
}

func TestMatchRulesSpaceID(t *testing.T) {
	space := "wN"
	daasCwd := "/Users/rian/focaApp/bclouder/daas/"
	rules := []RuleSpec{
		{ID: 18, ProjectID: 7, Priority: 100, MatchCwdPrefix: &daasCwd},
		{ID: 50, ProjectID: 8, Priority: 50, MatchSpaceID: &space},
	}
	sig := Signal{
		Source:  SourceAgent,
		Cwd:     "/Users/rian/focaApp/bclouder/daas/daas-back-end",
		SpaceID: "wN",
	}
	got := MatchRules(sig, rules)
	if got == nil || *got != 8 {
		t.Fatalf("priority-50 space rule must beat priority-100 cwd rule, got %v", got)
	}

	sig.SpaceID = "wM"
	got = MatchRules(sig, rules)
	if got == nil || *got != 7 {
		t.Fatalf("unbound space must fall through to the cwd rule, got %v", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/domain/ -run 'TestMatchesCwd|TestValidateCwdPattern|TestMatchRulesSpaceID' -v`
Expected: FAIL — `undefined: MatchesCwd`, `undefined: ValidateCwdPattern`, `unknown field MatchSpaceID`.

- [ ] **Step 3: Add the new fields**

In `internal/domain/types.go`, add `SpaceID string` to `Signal` (after `Cwd`), and `MatchSpaceID *string` to both `RuleSpec` and `ProposedRule` (after `MatchCwdPrefix`).

```go
type Signal struct {
	Source      Source
	BundleID    string
	WindowTitle string
	BinaryName  string
	Cwd         string
	// SpaceID is the herdr workspace id (e.g. "wN") this signal was produced
	// in, or "" when the work happened outside herdr. Empty never matches a
	// space-bound rule, so absence degrades to cwd-only attribution.
	SpaceID string
}
```

- [ ] **Step 4: Implement the matcher**

In `internal/domain/match.go`, add `"fmt"` and `"path"` to the imports, then:

```go
// MatchesCwd reports whether cwd satisfies a rule's cwd clause.
//
// A pattern containing '*' is a glob: it is tested with path.Match against cwd
// and each of its ancestor directories, so a pattern naming a worktree also
// matches build directories nested inside it. A pattern without '*' keeps the
// original literal-prefix rule: equal, or a true subdirectory.
func MatchesCwd(pattern, cwd string) bool {
	p := strings.TrimRight(pattern, "/")
	if p == "" {
		return false
	}
	if !strings.Contains(p, "*") {
		return cwd == p || strings.HasPrefix(cwd, p+"/")
	}
	for cur := cwd; ; {
		if ok, err := path.Match(p, cur); err == nil && ok {
			return true
		}
		parent := path.Dir(cur)
		if parent == cur {
			return false
		}
		cur = parent
	}
}

// ValidateCwdPattern rejects patterns whose '*' is not confined to the final
// path segment. SQLite's GLOB lets '*' cross '/' while path.Match does not, so
// a star earlier in the path would make the live matcher and the retroactive
// SQL disagree.
func ValidateCwdPattern(pattern string) error {
	p := strings.TrimRight(pattern, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 && strings.Contains(p[:i], "*") {
		return fmt.Errorf("'*' is only allowed in the final path segment: %q", pattern)
	}
	return nil
}
```

Then replace the cwd clause in `matchOne` and add the space clause:

```go
	if r.MatchCwdPrefix != nil && !MatchesCwd(*r.MatchCwdPrefix, sig.Cwd) {
		return false
	}
	if r.MatchSpaceID != nil && sig.SpaceID != *r.MatchSpaceID {
		return false
	}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/domain/ -count=1 -v`
Expected: PASS, including the pre-existing `TestMatchRules` cases.

- [ ] **Step 6: Commit**

```bash
git add internal/domain/match.go internal/domain/types.go internal/domain/match_test.go
git commit -m "feat(domain): glob cwd patterns and space-id rule matching"
```

---

### Task 2: Keep the agent track pre-filter in step with the matcher

**Files:**
- Modify: `internal/daemon/pipeline.go:548-559` (`cwdUnderAnyPrefix`)
- Modify: `internal/daemon/cache.go:14-26` (`CacheSnapshot`)
- Modify: `internal/daemon/rpc.go:330-360` (`ReloadCache` snapshot build)
- Test: `internal/daemon/pipeline_test.go`

**Interfaces:**
- Consumes: `domain.MatchesCwd` from Task 1.
- Produces: `CacheSnapshot.CwdPatterns []string` (replaces `CwdPrefixes`); `CacheSnapshot.BoundSpaceIDs map[string]bool`.

**Why this task exists:** `collectAgentSignals` only emits a signal when `track` is true, and `track` is computed from `snap.CwdPrefixes`. Its comment states the pre-filter and the rule matcher must never disagree. Without this task a glob rule or a space rule would match nothing, because the process would never produce a signal in the first place.

- [ ] **Step 1: Write the failing test**

Add to `internal/daemon/pipeline_test.go`:

```go
func TestCwdMatchesAnyPatternGlob(t *testing.T) {
	pats := []string{"/repo/.claude/worktrees/md-*"}
	if !cwdMatchesAnyPattern("/repo/.claude/worktrees/md-engine", pats) {
		t.Fatal("glob pattern must track the worktree itself")
	}
	if !cwdMatchesAnyPattern("/repo/.claude/worktrees/md-engine/DAAS.API", pats) {
		t.Fatal("glob pattern must track directories nested in the worktree")
	}
	if cwdMatchesAnyPattern("/repo/.claude/worktrees/packing-slip", pats) {
		t.Fatal("glob pattern must not track a non-matching sibling")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/daemon/ -run TestCwdMatchesAnyPatternGlob -v`
Expected: FAIL — `undefined: cwdMatchesAnyPattern`.

- [ ] **Step 3: Replace the pre-filter**

In `internal/daemon/pipeline.go`, delete `cwdUnderAnyPrefix` and add:

```go
// cwdMatchesAnyPattern reports whether cwd is covered by any rule cwd pattern.
// It delegates to domain.MatchesCwd so the upstream "is this dir tracked?"
// decision and the downstream "does rule X apply?" decision can never disagree.
func cwdMatchesAnyPattern(cwd string, patterns []string) bool {
	for _, p := range patterns {
		if domain.MatchesCwd(p, cwd) {
			return true
		}
	}
	return false
}
```

In `internal/daemon/cache.go`, rename the field and add the space set:

```go
	// CwdPatterns is the deduplicated set of cwd match values drawn from Rules
	// (literal prefixes and globs alike, verbatim — MatchesCwd handles trailing
	// slashes). Built once per ReloadCache so the agent pipeline can widen the
	// binary allowlist by tracked-directory match without rewalking Rules.
	CwdPatterns []string

	// BoundSpaceIDs is the set of herdr workspace ids referenced by any rule.
	// A process in a bound space is tracked even when its cwd matches no
	// pattern — that is the whole point of space attribution.
	BoundSpaceIDs map[string]bool
```

In `internal/daemon/rpc.go`, where the snapshot is built, collect both while walking rules (replacing the existing `CwdPrefixes` accumulation), and populate `spec.MatchSpaceID` alongside the other match columns:

```go
		if r.MatchSpaceID.Valid {
			v := r.MatchSpaceID.String
			spec.MatchSpaceID = &v
			boundSpaces[v] = true
		}
```

- [ ] **Step 4: Update the track decision**

In `collectAgentSignals`, the classification block becomes (note `spaceID` is threaded in by Task 6; for now pass `""`):

```go
			cached = procClass{
				name:  proc.Name,
				cwd:   cwd,
				track: snap.AllowedBinaries[proc.Name] || cwdMatchesAnyPattern(cwd, snap.CwdPatterns),
			}
```

- [ ] **Step 5: Run the full daemon suite**

Run: `go test ./internal/daemon/ -count=1`
Expected: PASS. Fix any remaining `CwdPrefixes` references the compiler reports.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/pipeline.go internal/daemon/cache.go internal/daemon/rpc.go internal/daemon/pipeline_test.go
git commit -m "refactor(daemon): make the agent track pre-filter glob-aware"
```

---

### Task 3: `internal/herdr` — resolve panes and sessions to spaces

**Files:**
- Create: `internal/herdr/herdr.go`
- Create: `internal/herdr/testdata/session.json`
- Test: `internal/herdr/herdr_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `herdr.Space{ID, Name string}`; `herdr.NewResolver(path string) *Resolver`; `(*Resolver).SpaceForPane(paneID string) (Space, bool)`; `(*Resolver).SpaceForSession(uuid string) (Space, bool)`; `herdr.DefaultSessionPath() string`.

- [ ] **Step 1: Write the fixture**

Create `internal/herdr/testdata/session.json` — a trimmed copy of the real file shape:

```json
{
  "version": 1,
  "workspaces": [
    {
      "id": "wM",
      "custom_name": null,
      "identity_cwd": "/repo/daas-back-end",
      "tabs": [
        {"panes": {"1": {"cwd": "/repo/daas-back-end",
          "agent_session": {"source": "herdr:claude", "agent": "claude", "kind": "id", "value": "aaaaaaaa-0000-0000-0000-000000000000"}}}}
      ]
    },
    {
      "id": "wN",
      "custom_name": "MD-tracker",
      "identity_cwd": "/repo/daas-back-end",
      "tabs": [
        {"panes": {"1": {"cwd": "/repo/daas-back-end",
          "agent_session": {"source": "herdr:claude", "agent": "claude", "kind": "id", "value": "bbbbbbbb-1111-1111-1111-111111111111"}},
                   "2": {"cwd": "/repo/daas-back-end"}}}
      ]
    }
  ]
}
```

- [ ] **Step 2: Write the failing test**

Create `internal/herdr/herdr_test.go`:

```go
package herdr

import "testing"

func TestSpaceForPane(t *testing.T) {
	r := NewResolver("testdata/session.json")
	s, ok := r.SpaceForPane("wN:p1")
	if !ok || s.ID != "wN" || s.Name != "MD-tracker" {
		t.Fatalf("SpaceForPane(wN:p1) = %+v, %v", s, ok)
	}
	s, ok = r.SpaceForPane("wM:p1")
	if !ok || s.ID != "wM" || s.Name != "" {
		t.Fatalf("unnamed space must resolve with an empty Name, got %+v, %v", s, ok)
	}
	if _, ok := r.SpaceForPane("wZ:p9"); ok {
		t.Fatal("unknown workspace must not resolve")
	}
	if _, ok := r.SpaceForPane(""); ok {
		t.Fatal("empty pane id must not resolve")
	}
}

func TestSpaceForSession(t *testing.T) {
	r := NewResolver("testdata/session.json")
	s, ok := r.SpaceForSession("bbbbbbbb-1111-1111-1111-111111111111")
	if !ok || s.ID != "wN" {
		t.Fatalf("SpaceForSession = %+v, %v", s, ok)
	}
	if _, ok := r.SpaceForSession("cccccccc-2222-2222-2222-222222222222"); ok {
		t.Fatal("unknown session must not resolve")
	}
}

func TestMissingFileIsNotAnError(t *testing.T) {
	r := NewResolver("testdata/does-not-exist.json")
	if _, ok := r.SpaceForPane("wN:p1"); ok {
		t.Fatal("missing session.json must resolve nothing, not panic")
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/herdr/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 4: Implement the resolver**

Create `internal/herdr/herdr.go`:

```go
// Package herdr resolves herdr workspace ("space") identity from herdr's
// on-disk session state. It performs no subprocess calls and no I/O beyond
// reading one JSON file, so it is unit-testable against a fixture.
//
// The file format is undocumented and may change on a herdr update; every
// lookup fails closed (returns ok=false) so attribution degrades to cwd rules
// rather than binding to the wrong project.
package herdr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Space is a herdr workspace. ID is stable across renames; Name is for display
// only and is empty for a workspace the user has not named.
type Space struct {
	ID   string
	Name string
}

// DefaultSessionPath is where herdr keeps its session state.
func DefaultSessionPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "herdr", "session.json")
}

type sessionFile struct {
	Workspaces []struct {
		ID         string  `json:"id"`
		CustomName *string `json:"custom_name"`
		Tabs       []struct {
			Panes map[string]struct {
				AgentSession *struct {
					Value string `json:"value"`
				} `json:"agent_session"`
			} `json:"panes"`
		} `json:"tabs"`
	} `json:"workspaces"`
}

// Resolver caches a parse of session.json, reloading when the file's mtime or
// size changes. Safe for concurrent use.
type Resolver struct {
	path string

	mu       sync.Mutex
	modTime  time.Time
	size     int64
	loaded   bool
	spaces   map[string]Space // workspace id -> Space
	sessions map[string]string // claude session uuid -> workspace id
}

func NewResolver(path string) *Resolver {
	return &Resolver{path: path}
}

// SpaceForPane resolves a HERDR_PANE_ID such as "wN:p1" to its workspace.
func (r *Resolver) SpaceForPane(paneID string) (Space, bool) {
	if paneID == "" {
		return Space{}, false
	}
	wid, _, _ := strings.Cut(paneID, ":")
	if wid == "" {
		return Space{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloadLocked()
	s, ok := r.spaces[wid]
	return s, ok
}

// SpaceForSession resolves a Claude Code session uuid to the workspace whose
// pane reported it.
func (r *Resolver) SpaceForSession(uuid string) (Space, bool) {
	if uuid == "" {
		return Space{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloadLocked()
	wid, ok := r.sessions[uuid]
	if !ok {
		return Space{}, false
	}
	s, ok := r.spaces[wid]
	return s, ok
}

// reloadLocked re-parses the file when it has changed. Any failure leaves the
// previous (possibly empty) maps in place; callers then simply resolve nothing.
func (r *Resolver) reloadLocked() {
	if r.path == "" {
		return
	}
	fi, err := os.Stat(r.path)
	if err != nil {
		if !r.loaded {
			r.spaces, r.sessions = map[string]Space{}, map[string]string{}
			r.loaded = true
		}
		return
	}
	if r.loaded && fi.ModTime().Equal(r.modTime) && fi.Size() == r.size {
		return
	}
	raw, err := os.ReadFile(r.path)
	if err != nil {
		return
	}
	var f sessionFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return
	}
	spaces := make(map[string]Space, len(f.Workspaces))
	sessions := map[string]string{}
	for _, w := range f.Workspaces {
		if w.ID == "" {
			continue
		}
		name := ""
		if w.CustomName != nil {
			name = *w.CustomName
		}
		spaces[w.ID] = Space{ID: w.ID, Name: name}
		for _, t := range w.Tabs {
			for _, p := range t.Panes {
				if p.AgentSession != nil && p.AgentSession.Value != "" {
					sessions[p.AgentSession.Value] = w.ID
				}
			}
		}
	}
	r.spaces, r.sessions = spaces, sessions
	r.modTime, r.size, r.loaded = fi.ModTime(), fi.Size(), true
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/herdr/ -count=1 -v`
Expected: PASS (4 tests).

- [ ] **Step 6: Commit**

```bash
git add internal/herdr/
git commit -m "feat(herdr): resolve panes and claude sessions to herdr spaces"
```

---

### Task 4: `ProcessEnvVar` in the macOS bridge

**Files:**
- Create: `internal/macos/psenv.go`
- Modify: `internal/macos/bridge.go`
- Modify: `internal/macos/real.go`
- Modify: `internal/macos/fake.go`
- Test: `internal/macos/psenv_test.go`

**Interfaces:**
- Consumes: `withTimeout`, `psDeadline` (existing, `internal/macos/real.go`).
- Produces: `Bridge.ProcessEnvVar(ctx context.Context, pid int, key string) (string, error)`; `FakeBridge.EnvByPID map[int]map[string]string`; `FakeBridge.EnvErr error`.

**Design note:** the API deliberately returns *one* variable rather than a whole environment map. `ps -Eww` prints the command line before the environment, and command arguments may themselves contain `=`, so parsing a complete map is unreliable. Scanning for a single `KEY=` token is not.

- [ ] **Step 1: Write the failing test**

Create `internal/macos/psenv_test.go`:

```go
package macos

import "testing"

func TestParseEnvVar(t *testing.T) {
	out := "  PID TTY           TIME CMD\n" +
		"22260 ??         1:23.45 claude --resume FOO=notenv " +
		"HERDR_ENV=1 HERDR_PANE_ID=wN:p1 SHELL=/bin/zsh\n"
	if got := parseEnvVar(out, "HERDR_PANE_ID"); got != "wN:p1" {
		t.Fatalf("parseEnvVar = %q, want %q", got, "wN:p1")
	}
	if got := parseEnvVar(out, "HERDR_SOCKET_PATH"); got != "" {
		t.Fatalf("absent key must yield empty string, got %q", got)
	}
	if got := parseEnvVar("", "HERDR_PANE_ID"); got != "" {
		t.Fatalf("empty output must yield empty string, got %q", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/macos/ -run TestParseEnvVar -v`
Expected: FAIL — `undefined: parseEnvVar`.

- [ ] **Step 3: Implement**

Create `internal/macos/psenv.go`:

```go
package macos

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ProcessEnvVarReal returns the value of one environment variable for pid, via
// `ps -Eww -p PID`.
//
// Returns ("", nil) when the variable is absent or the pid is gone (ps exits 1),
// matching ProcessCWDReal's convention: absence is normal, a broken subsystem
// is an error. Reading another process's environment succeeds only for the same
// uid; the daemon runs as a launchd user agent, so this holds in production and
// fails closed if it ever does not.
func ProcessEnvVarReal(ctx context.Context, pid int, key string) (string, error) {
	cctx, cancel := withTimeout(ctx, psDeadline)
	defer cancel()
	out, err := exec.CommandContext(cctx, "ps", "-Eww", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return "", nil // pid gone between ps -A and now
		}
		return "", fmt.Errorf("ps -E pid=%d: %w", pid, err)
	}
	return parseEnvVar(string(out), key), nil
}

// parseEnvVar scans ps -E output for a KEY=VALUE token and returns VALUE.
func parseEnvVar(out, key string) string {
	want := key + "="
	for _, tok := range strings.Fields(out) {
		if strings.HasPrefix(tok, want) {
			return strings.TrimPrefix(tok, want)
		}
	}
	return ""
}
```

Add to the `Bridge` interface in `internal/macos/bridge.go`:

```go
	ProcessEnvVar(ctx context.Context, pid int, key string) (string, error)
```

Add to `RealBridge` in `internal/macos/real.go`:

```go
func (r *RealBridge) ProcessEnvVar(ctx context.Context, pid int, key string) (string, error) {
	return ProcessEnvVarReal(ctx, pid, key)
}
```

Add to `FakeBridge` in `internal/macos/fake.go` (fields beside `CWDByPID`, method beside `ProcessCWD`):

```go
	// EnvByPID maps pid -> env key -> value. PIDs and keys absent from the map
	// return ("", nil), matching the real implementation when the variable is
	// unset or the process is gone.
	EnvByPID map[int]map[string]string

	// EnvErr applies to every pid when set.
	EnvErr error
```

```go
func (f *FakeBridge) ProcessEnvVar(ctx context.Context, pid int, key string) (string, error) {
	if f.EnvErr != nil {
		return "", f.EnvErr
	}
	return f.EnvByPID[pid][key], nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/macos/ -count=1`
Expected: PASS. The compile-time assertion `var _ Bridge = (*RealBridge)(nil)` also proves the interface is satisfied.

- [ ] **Step 5: Commit**

```bash
git add internal/macos/psenv.go internal/macos/psenv_test.go internal/macos/bridge.go internal/macos/real.go internal/macos/fake.go
git commit -m "feat(macos): read a single env var from a pid via ps -Eww"
```

---

### Task 5: Schema, migrations and sqlc regeneration

**Files:**
- Modify: `schema.sql`
- Modify: `queries.sql`
- Modify: `internal/daemon/migrate_observations.go`
- Create: `internal/daemon/migrate_rules.go`
- Modify: `internal/daemon/daemon.go` (call the new migrations beside `migrateObservationsSourceCheck`)
- Test: `internal/daemon/migrate_space_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `observations.space_id`; `rules.match_space_id`; `migrateObservationsSpaceID(db *sql.DB) error`; `migrateRulesSpaceID(db *sql.DB) error`; regenerated `store.UpsertObservationParams{SpaceID string}` and `store.AddRuleParams{MatchSpaceID sql.NullString}`.

- [ ] **Step 1: Write the failing migration test**

Create `internal/daemon/migrate_space_test.go`:

```go
package daemon

import (
	"database/sql"
	"strings"
	"testing"
)

// openPreSpaceDB builds a database with the pre-space observations and rules
// tables, so the migration has something real to upgrade.
func openPreSpaceDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/m.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE observations (
			id INTEGER PRIMARY KEY,
			source TEXT NOT NULL CHECK (source IN ('focus','agent','transcript')),
			bundle_id TEXT NOT NULL DEFAULT '',
			window_title TEXT NOT NULL DEFAULT '',
			binary_name TEXT NOT NULL DEFAULT '',
			cwd TEXT NOT NULL DEFAULT '',
			first_seen INTEGER NOT NULL,
			UNIQUE (source, bundle_id, window_title, binary_name, cwd)
		) STRICT`,
		`CREATE TABLE projects (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE,
			company_id INTEGER, paused INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL) STRICT`,
		`CREATE TABLE rules (
			id INTEGER PRIMARY KEY,
			project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			priority INTEGER NOT NULL DEFAULT 100,
			match_bundle_id TEXT, match_title_substr TEXT,
			match_binary_name TEXT, match_cwd_prefix TEXT,
			created_at INTEGER NOT NULL,
			CHECK (match_bundle_id IS NOT NULL OR match_title_substr IS NOT NULL
			    OR match_binary_name IS NOT NULL OR match_cwd_prefix IS NOT NULL)
		) STRICT`,
		`INSERT INTO projects (id,name,created_at) VALUES (8,'MD-Tracker',1)`,
		`INSERT INTO observations (id,source,cwd,first_seen) VALUES (9,'agent','/repo',1)`,
		`INSERT INTO rules (id,project_id,priority,match_cwd_prefix,created_at) VALUES (18,8,100,'/repo',1)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup %.40q: %v", s, err)
		}
	}
	return db
}

func TestMigrateObservationsSpaceID(t *testing.T) {
	db := openPreSpaceDB(t)
	defer db.Close()

	if err := migrateObservationsSpaceID(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var ddl string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='observations'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "space_id") {
		t.Fatalf("space_id missing after migration:\n%s", ddl)
	}

	// Row and id preservation matters: ticks.observation_id depends on it.
	var id int64
	var space string
	if err := db.QueryRow(`SELECT id, space_id FROM observations`).Scan(&id, &space); err != nil {
		t.Fatal(err)
	}
	if id != 9 || space != "" {
		t.Fatalf("row not preserved: id=%d space=%q", id, space)
	}

	// The new UNIQUE key must allow the same cwd in two different spaces.
	if _, err := db.Exec(
		`INSERT INTO observations (source,cwd,space_id,first_seen) VALUES ('agent','/repo','wN',1)`); err != nil {
		t.Fatalf("same cwd in a different space must be a distinct observation: %v", err)
	}

	if err := migrateObservationsSpaceID(db); err != nil {
		t.Fatalf("second run must be a no-op: %v", err)
	}
}

func TestMigrateRulesSpaceID(t *testing.T) {
	db := openPreSpaceDB(t)
	defer db.Close()

	if err := migrateRulesSpaceID(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// A space-only rule must satisfy the widened CHECK.
	if _, err := db.Exec(
		`INSERT INTO rules (project_id,priority,match_space_id,created_at) VALUES (8,50,'wN',1)`); err != nil {
		t.Fatalf("space-only rule rejected: %v", err)
	}
	// A rule with no match field at all must still be rejected.
	if _, err := db.Exec(
		`INSERT INTO rules (project_id,priority,created_at) VALUES (8,50,1)`); err == nil {
		t.Fatal("rule with no match field must violate CHECK")
	}
	if err := migrateRulesSpaceID(db); err != nil {
		t.Fatalf("second run must be a no-op: %v", err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/daemon/ -run 'TestMigrate.*SpaceID' -v`
Expected: FAIL — `undefined: migrateObservationsSpaceID`, `undefined: migrateRulesSpaceID`.

- [ ] **Step 3: Update `schema.sql`**

In `observations`, add `space_id TEXT NOT NULL DEFAULT ''` immediately before `first_seen`, and extend the UNIQUE:

```sql
    UNIQUE (source, bundle_id, window_title, binary_name, cwd, space_id)
```

In `rules`, add `match_space_id TEXT` after `match_cwd_prefix`, and widen the CHECK to include `match_space_id IS NOT NULL`.

- [ ] **Step 4: Write the migrations**

Append to `internal/daemon/migrate_observations.go` (leave `newObservationsDDL` and `migrateObservationsSourceCheck` untouched — they are the historical step, and running both in order is correct):

```go
// observationsWithSpaceDDL is the post-space table definition. Kept in sync
// with schema.sql so a migrated table matches a freshly-created one exactly.
const observationsWithSpaceDDL = `
CREATE TABLE observations_new (
    id              INTEGER PRIMARY KEY,
    source          TEXT NOT NULL CHECK (source IN ('focus', 'agent', 'transcript')),
    bundle_id       TEXT NOT NULL DEFAULT '',
    window_title    TEXT NOT NULL DEFAULT '',
    binary_name     TEXT NOT NULL DEFAULT '',
    cwd             TEXT NOT NULL DEFAULT '',
    space_id        TEXT NOT NULL DEFAULT '',
    first_seen      INTEGER NOT NULL,
    UNIQUE (source, bundle_id, window_title, binary_name, cwd, space_id)
) STRICT;`

// migrateObservationsSpaceID adds space_id and puts it in the UNIQUE key.
// SQLite cannot alter a UNIQUE in place, so we rebuild. Idempotent, and
// preserves ids because ticks.observation_id depends on their stability.
func migrateObservationsSpaceID(db *sql.DB) (retErr error) {
	var ddl string
	err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='observations'`,
	).Scan(&ddl)
	if err == sql.ErrNoRows {
		return nil // schema.sql will create the new form
	}
	if err != nil {
		return fmt.Errorf("read observations ddl: %w", err)
	}
	if strings.Contains(ddl, "space_id") {
		return nil // already migrated
	}

	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("fk off: %w", err)
	}
	defer func() {
		if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil && retErr == nil {
			retErr = fmt.Errorf("fk on: %w", err)
		}
	}()

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	stmts := []string{
		observationsWithSpaceDDL,
		`INSERT INTO observations_new (id, source, bundle_id, window_title, binary_name, cwd, space_id, first_seen)
		   SELECT id, source, bundle_id, window_title, binary_name, cwd, '', first_seen FROM observations`,
		`DROP TABLE observations`,
		`ALTER TABLE observations_new RENAME TO observations`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("rebuild observations for space_id (%.40q): %w", s, err)
		}
	}
	return tx.Commit()
}
```

Create `internal/daemon/migrate_rules.go`:

```go
package daemon

import (
	"database/sql"
	"fmt"
	"strings"
)

// rulesWithSpaceDDL is the post-space rules definition, kept in sync with
// schema.sql.
const rulesWithSpaceDDL = `
CREATE TABLE rules_new (
    id                  INTEGER PRIMARY KEY,
    project_id          INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    priority            INTEGER NOT NULL DEFAULT 100,
    match_bundle_id     TEXT,
    match_title_substr  TEXT,
    match_binary_name   TEXT,
    match_cwd_prefix    TEXT,
    match_space_id      TEXT,
    created_at          INTEGER NOT NULL,
    CHECK (
        match_bundle_id IS NOT NULL OR
        match_title_substr IS NOT NULL OR
        match_binary_name IS NOT NULL OR
        match_cwd_prefix IS NOT NULL OR
        match_space_id IS NOT NULL
    )
) STRICT;`

// migrateRulesSpaceID adds match_space_id and widens the CHECK so a space-only
// rule is legal. SQLite cannot alter a CHECK in place, so we rebuild. Nothing
// references rules, so only its own ids need preserving.
func migrateRulesSpaceID(db *sql.DB) error {
	var ddl string
	err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='rules'`,
	).Scan(&ddl)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read rules ddl: %w", err)
	}
	if strings.Contains(ddl, "match_space_id") {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	stmts := []string{
		rulesWithSpaceDDL,
		`INSERT INTO rules_new (id, project_id, priority, match_bundle_id, match_title_substr,
		                        match_binary_name, match_cwd_prefix, match_space_id, created_at)
		   SELECT id, project_id, priority, match_bundle_id, match_title_substr,
		          match_binary_name, match_cwd_prefix, NULL, created_at FROM rules`,
		`DROP TABLE rules`,
		`ALTER TABLE rules_new RENAME TO rules`,
		`CREATE INDEX IF NOT EXISTS idx_rules_priority ON rules(priority)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("rebuild rules for match_space_id (%.40q): %w", s, err)
		}
	}
	return tx.Commit()
}
```

In `internal/daemon/daemon.go`, call both immediately after the existing `migrateObservationsSourceCheck` block:

```go
	if err := migrateObservationsSpaceID(db); err != nil {
		return fmt.Errorf("migrate observations space_id: %w", err)
	}
	if err := migrateRulesSpaceID(db); err != nil {
		return fmt.Errorf("migrate rules match_space_id: %w", err)
	}
```

- [ ] **Step 5: Update `queries.sql` and regenerate**

`UpsertObservation` — add the column, the placeholder, and extend the conflict target:

```sql
-- name: UpsertObservation :one
INSERT INTO observations (source, bundle_id, window_title, binary_name, cwd, space_id, first_seen)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (source, bundle_id, window_title, binary_name, cwd, space_id)
DO UPDATE SET id = id
RETURNING id;
```

`AddRule`, `ListRules` and `ListRulesForCache` each gain `match_space_id` (a seventh insert column and placeholder for `AddRule`; an extra selected column for the two lists).

Then run: `make sqlc`
Expected: `internal/store` regenerates with `SpaceID` on `UpsertObservationParams` and `MatchSpaceID sql.NullString` on the rule types.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/daemon/ -run 'TestMigrate.*SpaceID' -count=1 -v && go build ./...`
Expected: both migration tests PASS. The build will report call sites needing the new fields — Task 6 fills them in.

- [ ] **Step 7: Commit**

```bash
git add schema.sql queries.sql internal/store internal/daemon/migrate_observations.go internal/daemon/migrate_rules.go internal/daemon/daemon.go internal/daemon/migrate_space_test.go
git commit -m "feat(store): add observations.space_id and rules.match_space_id"
```

---

### Task 6: Wire the space through the pipeline

**Files:**
- Modify: `internal/daemon/pipeline.go` (`RunTick` upsert, `collectAgentSignals`)
- Modify: `internal/daemon/transcript.go` (`collectTranscriptSignals`)
- Modify: `internal/daemon/daemon.go` (construct the resolver, inject into `Pipeline`)
- Test: `internal/daemon/pipeline_space_test.go`

**Interfaces:**
- Consumes: `herdr.NewResolver`, `(*herdr.Resolver).SpaceForPane`, `(*herdr.Resolver).SpaceForSession` (Task 3); `Bridge.ProcessEnvVar` (Task 4); `store.UpsertObservationParams.SpaceID` (Task 5); `CacheSnapshot.BoundSpaceIDs` (Task 2).
- Produces: agent and transcript signals carrying `SpaceID`; observations persisted with `space_id`.

- [ ] **Step 1: Write the failing test**

Create `internal/daemon/pipeline_space_test.go`. Two sessions share a cwd; only the space differs, and only one space is bound by a rule:

```go
func TestAgentSignalCarriesHerdrSpace(t *testing.T) {
	const cwd = "/repo/daas-back-end"
	fake := &macos.FakeBridge{
		Processes: []macos.ProcessSample{{PID: 100, Name: "claude", CPUTicks: 0}},
		CWDByPID:  map[int]string{100: cwd},
		EnvByPID:  map[int]map[string]string{100: {"HERDR_PANE_ID": "wN:p1"}},
	}
	p := newTestPipeline(t, fake) // existing helper; wires cfg, cache, queries
	p.herdr = herdr.NewResolver("../herdr/testdata/session.json")

	// First tick establishes the CPU baseline; second crosses the busy bar.
	fake.Processes[0].CPUTicks = 0
	p.collectAgentSignals(context.Background(), snapWithCwdPattern(cwd), true)
	fake.Processes[0].CPUTicks = 10_000
	sigs := p.collectAgentSignals(context.Background(), snapWithCwdPattern(cwd), true)

	if len(sigs) != 1 {
		t.Fatalf("want 1 agent signal, got %d", len(sigs))
	}
	if sigs[0].SpaceID != "wN" {
		t.Fatalf("SpaceID = %q, want %q", sigs[0].SpaceID, "wN")
	}
}

func TestAgentSignalWithoutHerdrHasEmptySpace(t *testing.T) {
	const cwd = "/repo/daas-back-end"
	fake := &macos.FakeBridge{
		Processes: []macos.ProcessSample{{PID: 101, Name: "claude", CPUTicks: 0}},
		CWDByPID:  map[int]string{101: cwd},
		// no EnvByPID entry: process is not running under herdr
	}
	p := newTestPipeline(t, fake)
	p.herdr = herdr.NewResolver("../herdr/testdata/session.json")

	p.collectAgentSignals(context.Background(), snapWithCwdPattern(cwd), true)
	fake.Processes[0].CPUTicks = 10_000
	sigs := p.collectAgentSignals(context.Background(), snapWithCwdPattern(cwd), true)

	if len(sigs) != 1 || sigs[0].SpaceID != "" {
		t.Fatalf("absent HERDR_PANE_ID must yield an empty SpaceID, got %+v", sigs)
	}
}
```

Add the `snapWithCwdPattern` helper in the same file, returning a `*CacheSnapshot` with `CwdPatterns: []string{cwd}` and empty maps.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/daemon/ -run TestAgentSignal -v`
Expected: FAIL — `p.herdr` undefined and `Signal.SpaceID` always empty.

- [ ] **Step 3: Add the resolver and the pid cache**

In `internal/daemon/pipeline.go`, add to the `Pipeline` struct:

```go
	herdr *herdr.Resolver
	// procSpace caches pid -> herdr workspace id. A process's environment is
	// immutable after exec, so one lookup per pid suffices; entries are swept
	// by the same livePIDs pass that clears prevCPU and procClass.
	procSpace map[int]string
```

Initialise `procSpace: map[int]string{}` wherever `procClass` is initialised, and add the sweep beside the others in the `defer` at the top of `collectAgentSignals`:

```go
		for pid := range p.procSpace {
			if !livePIDs[pid] {
				delete(p.procSpace, pid)
			}
		}
```

In `internal/daemon/daemon.go`, construct it where the Pipeline is built:

```go
	pipeline.herdr = herdr.NewResolver(herdr.DefaultSessionPath())
```

- [ ] **Step 4: Resolve the space in `collectAgentSignals`**

Replace the classification block so the space is resolved once per pid and feeds both `track` and the emitted signal:

```go
		spaceID, cached := p.procSpace[proc.PID], procClass{}
		if _, ok := p.procSpace[proc.PID]; !ok {
			paneID, err := p.bridge.ProcessEnvVar(ctx, proc.PID, "HERDR_PANE_ID")
			if err != nil {
				log.Printf("env pid=%d: %v", proc.PID, err)
			}
			if s, ok := p.herdr.SpaceForPane(paneID); ok {
				spaceID = s.ID
			}
			p.procSpace[proc.PID] = spaceID
		}

		cached, ok := p.procClass[proc.PID]
		if !ok {
			cwd, err := p.bridge.ProcessCWD(ctx, proc.PID)
			if err != nil {
				log.Printf("cwd pid=%d: %v", proc.PID, err)
				continue
			}
			if cwd == "" {
				continue
			}
			cached = procClass{
				name: proc.Name,
				cwd:  cwd,
				track: snap.AllowedBinaries[proc.Name] ||
					cwdMatchesAnyPattern(cwd, snap.CwdPatterns) ||
					(spaceID != "" && snap.BoundSpaceIDs[spaceID]),
			}
			p.procClass[proc.PID] = cached
		}
```

and extend the emitted signal:

```go
		out = append(out, domain.Signal{
			Source:     domain.SourceAgent,
			BinaryName: proc.Name,
			Cwd:        cached.cwd,
			SpaceID:    spaceID,
		})
```

- [ ] **Step 5: Resolve the space for transcript signals**

In `internal/daemon/transcript.go`, the session uuid is the transcript filename without its extension and is already used as the signal's `WindowTitle`. Where the transcript `domain.Signal` is constructed, set:

```go
			sessionID := strings.TrimSuffix(e.Name(), ".jsonl")
			spaceID := ""
			if s, ok := p.herdr.SpaceForSession(sessionID); ok {
				spaceID = s.ID
			}
```

and add `SpaceID: spaceID` to that `domain.Signal` literal.

- [ ] **Step 6: Persist the space**

In `RunTick`, extend the upsert:

```go
		obsID, err := p.q.UpsertObservation(ctx, store.UpsertObservationParams{
			Source:      string(sig.Source),
			BundleID:    sig.BundleID,
			WindowTitle: sig.WindowTitle,
			BinaryName:  sig.BinaryName,
			Cwd:         sig.Cwd,
			SpaceID:     sig.SpaceID,
			FirstSeen:   now,
		})
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/daemon/ -count=1`
Expected: PASS, including the two new space tests and the whole existing suite.

- [ ] **Step 8: Commit**

```bash
git add internal/daemon/pipeline.go internal/daemon/transcript.go internal/daemon/daemon.go internal/daemon/pipeline_space_test.go
git commit -m "feat(daemon): tag agent and transcript signals with their herdr space"
```

---

### Task 7: Retroactive matcher — space clause and boundary fix

**Files:**
- Modify: `queries.sql` (`ApplyRuleRetroactivelyCounted`)
- Modify: `internal/daemon/rpc.go` (`TagSignature` call site)
- Test: `internal/daemon/retro_parity_test.go`

**Interfaces:**
- Consumes: `domain.MatchesCwd` (Task 1); regenerated store params (Task 5).
- Produces: a retroactive matcher that agrees with the live matcher.

**Why the boundary fix belongs here:** the current clause is `cwd LIKE ? || '%'` with no `/` boundary, so a rule for `.../mb-tracker` retroactively sweeps in `.../mb-tracker-foo` — paths the live matcher rejects. Adding globs without fixing this would widen the divergence.

- [ ] **Step 1: Write the failing parity test**

Create `internal/daemon/retro_parity_test.go`:

```go
package daemon

import (
	"context"
	"testing"

	"github.com/rian/antitimely/internal/domain"
)

// TestRetroMatcherParity asserts the SQL used by ApplyRuleRetroactivelyCounted
// selects exactly the cwds that domain.MatchesCwd accepts. A divergence here
// silently retags the wrong ticks.
func TestRetroMatcherParity(t *testing.T) {
	const wt = "/repo/.claude/worktrees"
	corpus := []string{
		"/repo", "/repo/daas-back-end",
		wt, wt + "/md-tracker", wt + "/md-tracker/DAAS.API",
		wt + "/md-tracker-scaffold", wt + "/md-engine",
		wt + "/packing-slip", "/repo-other", "/repo-other/x",
		"/repo/mb-tracker", "/repo/mb-tracker-foo",
	}
	patterns := []string{
		"/repo", "/repo/", wt + "/md-*", wt + "/md-tracker",
		"/repo/mb-tracker",
	}

	db, q := newTestStore(t) // existing helper: migrated in-memory DB + *store.Queries
	ctx := context.Background()
	for _, cwd := range corpus {
		if _, err := db.Exec(
			`INSERT INTO observations (source,cwd,space_id,first_seen) VALUES ('agent',?,'',1)`, cwd); err != nil {
			t.Fatal(err)
		}
	}

	for _, pat := range patterns {
		rows, err := db.Query(retroCwdProbeSQL, pat, pat, pat, pat)
		if err != nil {
			t.Fatalf("probe %q: %v", pat, err)
		}
		sqlMatched := map[string]bool{}
		for rows.Next() {
			var cwd string
			if err := rows.Scan(&cwd); err != nil {
				t.Fatal(err)
			}
			sqlMatched[cwd] = true
		}
		rows.Close()

		for _, cwd := range corpus {
			want := domain.MatchesCwd(pat, cwd)
			if sqlMatched[cwd] != want {
				t.Errorf("pattern %q cwd %q: SQL=%v Go=%v", pat, cwd, sqlMatched[cwd], want)
			}
		}
	}
	_ = q
}
```

Add the probe constant in the same file — it must be the *identical* cwd clause used in `queries.sql`:

```go
// retroCwdProbeSQL mirrors the cwd clause of ApplyRuleRetroactivelyCounted.
// Keep the two in sync; the parity test is what enforces it.
const retroCwdProbeSQL = `
SELECT cwd FROM observations
WHERE CASE WHEN instr(?, '*') > 0
           THEN (cwd GLOB ? OR cwd GLOB ? || '/*')
           ELSE (cwd = rtrim(?, '/') OR cwd LIKE rtrim(?, '/') || '/%')
      END`
```

Note the probe takes five parameters; adjust the `db.Query` call to pass `pat` five times.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/daemon/ -run TestRetroMatcherParity -v`
Expected: FAIL — mismatches on `/repo/mb-tracker` vs `/repo/mb-tracker-foo` under the old clause, or a compile error until the constant is added.

- [ ] **Step 3: Update the retroactive query**

In `queries.sql`, replace `ApplyRuleRetroactivelyCounted` (keep the file ASCII-only):

```sql
-- name: ApplyRuleRetroactivelyCounted :execrows
UPDATE ticks
SET project_id = ?
WHERE project_id IS NULL
  AND observation_id IN (
      SELECT id FROM observations
      WHERE (? IS NULL OR bundle_id = ?)
        AND (? IS NULL OR window_title LIKE '%' || ? || '%')
        AND (? IS NULL OR binary_name = ?)
        AND (? IS NULL OR space_id = ?)
        AND (? IS NULL OR CASE WHEN instr(?, '*') > 0
                               THEN (cwd GLOB ? OR cwd GLOB ? || '/*')
                               ELSE (cwd = rtrim(?, '/') OR cwd LIKE rtrim(?, '/') || '/%')
                          END)
  );
```

Run: `make sqlc`

- [ ] **Step 4: Update the call site**

In `internal/daemon/rpc.go`, `TagSignature` passes the cwd value once per placeholder today; update it to supply the new space parameters and the repeated cwd parameters in the order the regenerated params struct declares. Verify with `go build ./...`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/daemon/ -count=1`
Expected: PASS — parity holds for every pattern/cwd pair, including the `mb-tracker` boundary case.

- [ ] **Step 6: Commit**

```bash
git add queries.sql internal/store internal/daemon/rpc.go internal/daemon/retro_parity_test.go
git commit -m "fix(store): make retroactive cwd matching boundary-correct and glob-aware"
```

---

### Task 8: CLI — `rules add` and `spaces`

**Files:**
- Modify: `internal/cli/rules.go`
- Create: `internal/cli/spaces.go`
- Modify: `internal/cli/dispatch.go` (switch **and** `printUsage`)
- Modify: `internal/rpcapi/rpcapi.go` (`RuleAddArgs`/`RuleAddReply`, space fields on `Rule`)
- Modify: `internal/daemon/rpc.go` (`RuleAdd` handler, space in `RulesList`)
- Test: `internal/cli/rules_test.go`

**Interfaces:**
- Consumes: `domain.ValidateCwdPattern` (Task 1); `herdr.NewResolver`, `herdr.DefaultSessionPath` (Task 3); `store.AddRuleParams.MatchSpaceID` (Task 5).
- Produces: `atl rules add`, `atl spaces`.

- [ ] **Step 1: Write the failing test**

Create `internal/cli/rules_test.go`:

```go
package cli

import "testing"

func TestRulesAddRejectsNoMatchField(t *testing.T) {
	if code := cmdRules([]string{"add", "--project=MD-Tracker"}); code != 64 {
		t.Fatalf("a rule with no match field must exit 64, got %d", code)
	}
}

func TestRulesAddRejectsStarBeforeLastSlash(t *testing.T) {
	code := cmdRules([]string{"add", "--project=MD-Tracker", "--cwd=/a/*/md-x"})
	if code != 64 {
		t.Fatalf("star before the last / must exit 64, got %d", code)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/cli/ -run TestRulesAdd -v`
Expected: FAIL — `rules add` is an unknown subcommand (the usage string still reads `<list|delete>`).

- [ ] **Step 3: Implement `rules add`**

In `internal/cli/rules.go`, extend the dispatch and usage:

```go
		fmt.Fprintln(os.Stderr, "usage: antitimely rules <list|add|delete> ...")
```

```go
	case "add":
		return rulesAdd(args[1:])
```

```go
func rulesAdd(args []string) int {
	fs := flag.NewFlagSet("rules add", flag.ExitOnError)
	project := fs.String("project", "", "project name (required)")
	priority := fs.Int64("priority", 100, "lower number wins")
	bundle := fs.String("bundle", "", "match bundle id exactly")
	title := fs.String("title", "", "match window title substring")
	binary := fs.String("binary", "", "match binary name exactly")
	cwd := fs.String("cwd", "", "match cwd prefix, or a glob with * in the final segment")
	space := fs.String("space", "", "match herdr workspace id (see: antitimely spaces)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 64
	}
	if *project == "" {
		fmt.Fprintln(os.Stderr, "usage: antitimely rules add --project=<name> [--priority=N] [--bundle=..] [--title=..] [--binary=..] [--cwd=..] [--space=..]")
		return 64
	}
	if *bundle == "" && *title == "" && *binary == "" && *cwd == "" && *space == "" {
		fmt.Fprintln(os.Stderr, "at least one match field is required")
		return 64
	}
	if *cwd != "" {
		if err := domain.ValidateCwdPattern(*cwd); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 64
		}
	}
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	var reply rpcapi.RuleAddReply
	err := client.Call(rpcapi.ServiceName+".RuleAdd", rpcapi.RuleAddArgs{
		ProjectName:      *project,
		Priority:         *priority,
		MatchBundleID:    *bundle,
		MatchTitleSubstr: *title,
		MatchBinaryName:  *binary,
		MatchCWDPrefix:   *cwd,
		MatchSpaceID:     *space,
	}, &reply)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Added rule %d\n", reply.ID)
	return 0
}
```

Add `SPACE` to the `rulesList` header and row format, printing `reply.Items[i].MatchSpaceID`.

In `internal/rpcapi`, add `RuleAddArgs` with those seven fields, `RuleAddReply{ID int64}`, and `MatchSpaceID string` on `Rule`. In `internal/daemon/rpc.go`, add the `RuleAdd` handler: resolve the project by name, convert empty strings to `sql.NullString{}`, call `AddRule`, then `ReloadCache` so the new rule takes effect without a SIGHUP.

- [ ] **Step 4: Implement `atl spaces`**

Create `internal/cli/spaces.go`:

```go
package cli

import (
	"fmt"

	"github.com/rian/antitimely/internal/herdr"
)

// cmdSpaces lists herdr workspaces so their ids are discoverable for
// `rules add --space`. It reads herdr's state directly; no daemon needed.
func cmdSpaces() int {
	r := herdr.NewResolver(herdr.DefaultSessionPath())
	spaces := r.Spaces()
	if len(spaces) == 0 {
		fmt.Println("(no herdr spaces found)")
		return 0
	}
	fmt.Printf("%-6s %s\n", "ID", "NAME")
	for _, s := range spaces {
		name := s.Name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Printf("%-6s %s\n", s.ID, name)
	}
	return 0
}
```

Add the backing method to `internal/herdr/herdr.go`:

```go
// Spaces returns every known workspace, ordered by id.
func (r *Resolver) Spaces() []Space {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloadLocked()
	out := make([]Space, 0, len(r.spaces))
	for _, s := range r.spaces {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
```

(add `"sort"` to that file's imports)

- [ ] **Step 5: Register the subcommand**

In `internal/cli/dispatch.go`, add to the switch:

```go
	case "spaces":
		return cmdSpaces()
```

and add both lines to `printUsage`:

```
  spaces                       list herdr spaces and their ids
```
plus updating the `rules` line to `rules <list|add|delete>`.

- [ ] **Step 6: Run the tests and build**

Run: `go test ./... -count=1 && make build`
Expected: PASS and a successful signed build.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/ internal/rpcapi/ internal/daemon/rpc.go internal/herdr/herdr.go
git commit -m "feat(cli): add rules add and spaces subcommands"
```

---

### Task 9: Roll out the MD-Tracker bindings

**Files:**
- None (operational).

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Rebuild and restart the daemon**

```bash
make rebuild
```

Note: use `make`, never a plain `go build` — a plain build reverts the binary to Go's ad-hoc signature and breaks the Accessibility grant.

- [ ] **Step 2: Confirm the migrations ran**

```bash
sqlite3 -readonly ~/.antitimely/db.sqlite \
  "SELECT sql FROM sqlite_master WHERE name IN ('observations','rules');" | grep -c space
```
Expected: `2` (one per table).

- [ ] **Step 3: Find the MD space id**

```bash
./antitimely spaces
```
Expected: a row `wN  MD-tracker` (confirm the id — it changes if the space was deleted and recreated).

- [ ] **Step 4: Add the three replacement rules**

```bash
./antitimely rules add --project=MD-Tracker --priority=50 --space=wN
./antitimely rules add --project=MD-Tracker --priority=50 \
  --cwd='/Users/rian/focaApp/bclouder/daas/daas-back-end/.claude/worktrees/md-*'
./antitimely rules add --project=MD-Tracker --priority=50 \
  --cwd='/Users/rian/focaApp/bclouder/daas/daas-front-end/.claude/worktrees/md-*'
```

- [ ] **Step 5: Delete the seven per-worktree rules**

```bash
./antitimely rules list        # identify the per-worktree MD-Tracker rules
./antitimely rules delete <id> # repeat for each
```
Four of them point at directories that no longer exist (`md-engine`, `md-mirror`, `md-material`, `md-notifications`).

- [ ] **Step 6: Verify live attribution**

```bash
sqlite3 -readonly ~/.antitimely/db.sqlite "
SELECT datetime(t.ts,'unixepoch','localtime'), p.name, o.source, o.space_id, o.cwd
FROM ticks t JOIN projects p ON p.id=t.project_id
JOIN observations o ON o.id=t.observation_id
ORDER BY t.ts DESC LIMIT 10;"
```
Expected: while working in the MD space, rows show `MD-Tracker` with `space_id = wN`, including for sessions whose cwd is the `daas-back-end` root.

---

## Self-Review

**Spec coverage** — every section maps to a task: matching semantics → Task 1; obtaining the space → Tasks 3, 4, 6; data model and migration → Task 5; CLI surface → Task 8; failure modes → Tasks 3, 4, 6 (empty values throughout); testing → each task's tests plus the parity test in Task 7; rollout → Task 9.

**Gap found and closed:** the spec did not mention the agent `track` pre-filter (`CwdPrefixes` / `cwdUnderAnyPrefix`). Without updating it, a glob or space rule would match nothing because the process would never emit a signal. Task 2 covers it; the spec should gain a sentence noting the pre-filter is part of the change.

**Placeholder scan** — no TBDs; every code step carries real code.

**Type consistency** — `MatchesCwd(pattern, cwd)` and `ValidateCwdPattern(pattern)` are used with those exact signatures in Tasks 2, 7 and 8. `CwdPrefixes` is renamed to `CwdPatterns` in Task 2 and referenced under the new name thereafter. `ProcessEnvVar(ctx, pid, key)` is defined in Task 4 and called with that signature in Task 6. `Resolver.Spaces()` is introduced in Task 8 alongside its only caller.
