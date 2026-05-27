# Focus-Arm Gate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Gate agent-source ticks behind a focus signal after `resume-all` / project creation / auto-resume, so stale background processes can no longer credit time to projects the user is not actively working on.

**Architecture:** New `ArmedProjects map[int64]bool` lives on `CacheSnapshot`, mutated via CAS-loops that match the existing `MarkProjectActive` pattern. Pipeline drops agent-source ticks for armed projects; focus signals always count and act as the disarm event. A new `StorePreservingRuntime` cache method makes arm state survive every existing `ReloadCache` call. All in-memory — no schema change.

**Tech Stack:** Go 1.25, `atomic.Pointer`, `maps.Clone` (stdlib), `modernc.org/sqlite` for tests.

**Spec:** `docs/superpowers/specs/2026-05-27-focus-arm-gate-design.md`

**IMPORTANT — Working-tree hygiene:** The repo has ~11 modified files unrelated to this work at the time the plan was written. Every commit step stages files by explicit path (`git add path1 path2`). **Never use `git add -A` or `git add .` for any commit in this plan.**

**Test harness recap:**
- `internal/daemon/cache_test.go` uses plain `NewCache()`.
- `internal/daemon/pipeline_test.go` provides `newTestPipeline(t)` → `(*Pipeline, *macos.FakeBridge, *Cache, *sql.DB)`.
- `internal/daemon/rpc_test.go` provides `setupRPCServer(t)` → `(*rpc.Client, *sql.DB, *Cache)`.
- Schema loads from `../../schema.sql`.

---

## File structure

| File | Responsibility | Changes |
|------|---------------|---------|
| `internal/daemon/cache.go` | In-memory snapshot + lock-free swap | Add `ArmedProjects` field; new methods `ArmAllProjects`, `ArmProject`, `DisarmProject`, `StorePreservingRuntime` |
| `internal/daemon/cache_test.go` | Cache unit tests | New tests for the four cache methods |
| `internal/daemon/pipeline.go` | Tick loop, signal collection, rule matching | Gate agent ticks; disarm on focus; arm on auto-resume |
| `internal/daemon/pipeline_test.go` | Pipeline behavior | New gate-behavior tests |
| `internal/daemon/rpc.go` | RPC handlers | Rewrite `ProjectResumeAll`; arm in `ProjectAdd` + `TagSignature`; flip `ReloadCache` to preserving variant |
| `internal/daemon/rpc_test.go` | RPC handler tests | Tests for `ProjectResumeAll`, `ProjectAdd`, `TagSignature` arming |
| `internal/cli/status.go` | `atl status` rendering | Append `(armed: needs focus)` for armed projects |

---

## Task 1: Add `ArmedProjects` field to `CacheSnapshot`

**Files:**
- Modify: `internal/daemon/cache.go`
- Test: `internal/daemon/cache_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/daemon/cache_test.go`:

```go
func TestCache_InitialArmedProjectsEmpty(t *testing.T) {
	c := NewCache()
	snap := c.Snapshot()
	if snap.ArmedProjects == nil {
		t.Fatal("ArmedProjects should be initialized, got nil")
	}
	if len(snap.ArmedProjects) != 0 {
		t.Errorf("expected empty ArmedProjects, got %v", snap.ArmedProjects)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestCache_InitialArmedProjectsEmpty -v`
Expected: FAIL with `snap.ArmedProjects undefined` (compile error).

- [ ] **Step 3: Add the field and initialize**

In `internal/daemon/cache.go`, inside `CacheSnapshot` struct (after `PausedProjectIDs`):

```go
ArmedProjects map[int64]bool // project_id -> armed (needs focus before agent ticks count)
```

In `NewCache()`, inside the initial `&CacheSnapshot{...}`:

```go
ArmedProjects:    map[int64]bool{},
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestCache_InitialArmedProjectsEmpty -v`
Expected: PASS.

- [ ] **Step 5: Run the full daemon package test suite**

Run: `go test ./internal/daemon/ -count=1`
Expected: PASS for all existing tests (regression guard).

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/cache.go internal/daemon/cache_test.go
git commit -m "$(cat <<'EOF'
feat(cache): add ArmedProjects field to CacheSnapshot

Foundation for the focus-arm gate: per-project flag indicating that
the project requires a focus signal before agent-source ticks count.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Implement `ArmAllProjects`

**Files:**
- Modify: `internal/daemon/cache.go`
- Test: `internal/daemon/cache_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/daemon/cache_test.go`:

```go
func TestCache_ArmAllProjects(t *testing.T) {
	c := NewCache()
	c.ArmAllProjects([]int64{1, 2, 3})
	got := c.Snapshot().ArmedProjects
	for _, id := range []int64{1, 2, 3} {
		if !got[id] {
			t.Errorf("expected project %d armed, got %v", id, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("expected exactly 3 armed, got %d", len(got))
	}
}

func TestCache_ArmAllProjects_EmptyIsNoop(t *testing.T) {
	c := NewCache()
	c.ArmAllProjects([]int64{1, 2})
	c.ArmAllProjects(nil)
	if len(c.Snapshot().ArmedProjects) != 2 {
		t.Errorf("nil input should be no-op, got %v", c.Snapshot().ArmedProjects)
	}
	c.ArmAllProjects([]int64{})
	if len(c.Snapshot().ArmedProjects) != 2 {
		t.Errorf("empty slice input should be no-op, got %v", c.Snapshot().ArmedProjects)
	}
}

func TestCache_ArmAllProjects_ReplacesPreviousMap(t *testing.T) {
	c := NewCache()
	c.ArmAllProjects([]int64{1, 2})
	c.ArmAllProjects([]int64{3, 4, 5})
	got := c.Snapshot().ArmedProjects
	if got[1] || got[2] {
		t.Errorf("previous IDs should be gone, got %v", got)
	}
	for _, id := range []int64{3, 4, 5} {
		if !got[id] {
			t.Errorf("expected project %d armed, got %v", id, got)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/daemon/ -run TestCache_ArmAllProjects -v`
Expected: FAIL with `c.ArmAllProjects undefined`.

- [ ] **Step 3: Implement the method**

Append to `internal/daemon/cache.go`:

```go
// ArmAllProjects atomically installs a fresh ArmedProjects map containing
// every supplied ID. An empty or nil slice is a no-op — used by the
// ProjectResumeAll RPC handler whose ListProjects call might degenerately
// return zero rows; silently disarming the world is exactly the failure
// mode this feature exists to prevent.
func (c *Cache) ArmAllProjects(ids []int64) {
	if len(ids) == 0 {
		return
	}
	for {
		cur := c.ptr.Load()
		next := *cur
		next.ArmedProjects = make(map[int64]bool, len(ids))
		for _, id := range ids {
			next.ArmedProjects[id] = true
		}
		if c.ptr.CompareAndSwap(cur, &next) {
			return
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run TestCache_ArmAllProjects -v`
Expected: PASS (all three).

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/cache.go internal/daemon/cache_test.go
git commit -m "$(cat <<'EOF'
feat(cache): add Cache.ArmAllProjects

CAS-swaps a fresh ArmedProjects map containing every supplied project ID.
Empty/nil input is a no-op to prevent a degenerate ListProjects result
from silently disarming everyone.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Implement `ArmProject`

**Files:**
- Modify: `internal/daemon/cache.go`
- Test: `internal/daemon/cache_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/daemon/cache_test.go`:

```go
func TestCache_ArmProject(t *testing.T) {
	c := NewCache()
	c.ArmProject(42)
	if !c.Snapshot().ArmedProjects[42] {
		t.Errorf("expected project 42 armed, got %v", c.Snapshot().ArmedProjects)
	}
}

func TestCache_ArmProject_Idempotent(t *testing.T) {
	c := NewCache()
	c.ArmProject(7)
	c.ArmProject(7)
	got := c.Snapshot().ArmedProjects
	if !got[7] || len(got) != 1 {
		t.Errorf("expected exactly {7:true}, got %v", got)
	}
}

func TestCache_ArmProject_DoesNotClobberOthers(t *testing.T) {
	c := NewCache()
	c.ArmAllProjects([]int64{1, 2})
	c.ArmProject(3)
	got := c.Snapshot().ArmedProjects
	for _, id := range []int64{1, 2, 3} {
		if !got[id] {
			t.Errorf("expected project %d armed, got %v", id, got)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/daemon/ -run TestCache_ArmProject -v`
Expected: FAIL with `c.ArmProject undefined`.

- [ ] **Step 3: Implement the method**

Append to `internal/daemon/cache.go`:

```go
// ArmProject atomically sets ArmedProjects[id]=true. Idempotent.
func (c *Cache) ArmProject(id int64) {
	for {
		cur := c.ptr.Load()
		if cur.ArmedProjects[id] {
			return
		}
		next := *cur
		next.ArmedProjects = make(map[int64]bool, len(cur.ArmedProjects)+1)
		for k, v := range cur.ArmedProjects {
			next.ArmedProjects[k] = v
		}
		next.ArmedProjects[id] = true
		if c.ptr.CompareAndSwap(cur, &next) {
			return
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run TestCache_ArmProject -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/cache.go internal/daemon/cache_test.go
git commit -m "$(cat <<'EOF'
feat(cache): add Cache.ArmProject

CAS-swaps the ArmedProjects map with the single ID added. Used by
ProjectAdd, TagSignature create-on-the-fly, and the pipeline's
auto-resume branch.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Implement `DisarmProject`

**Files:**
- Modify: `internal/daemon/cache.go`
- Test: `internal/daemon/cache_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/daemon/cache_test.go`:

```go
func TestCache_DisarmProject(t *testing.T) {
	c := NewCache()
	c.ArmAllProjects([]int64{1, 2, 3})
	c.DisarmProject(2)
	got := c.Snapshot().ArmedProjects
	if got[2] {
		t.Errorf("expected project 2 disarmed, got %v", got)
	}
	if !got[1] || !got[3] {
		t.Errorf("expected projects 1 and 3 still armed, got %v", got)
	}
}

func TestCache_DisarmProject_Absent_NoChange(t *testing.T) {
	c := NewCache()
	c.ArmAllProjects([]int64{1, 2})
	c.DisarmProject(99) // never armed
	got := c.Snapshot().ArmedProjects
	if len(got) != 2 || !got[1] || !got[2] {
		t.Errorf("expected {1,2} still armed, got %v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/daemon/ -run TestCache_DisarmProject -v`
Expected: FAIL with `c.DisarmProject undefined`.

- [ ] **Step 3: Implement the method**

Append to `internal/daemon/cache.go`:

```go
// DisarmProject atomically removes id from ArmedProjects. No-op if absent.
func (c *Cache) DisarmProject(id int64) {
	for {
		cur := c.ptr.Load()
		if !cur.ArmedProjects[id] {
			return
		}
		next := *cur
		next.ArmedProjects = make(map[int64]bool, len(cur.ArmedProjects)-1)
		for k, v := range cur.ArmedProjects {
			if k != id {
				next.ArmedProjects[k] = v
			}
		}
		if c.ptr.CompareAndSwap(cur, &next) {
			return
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run TestCache_DisarmProject -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/cache.go internal/daemon/cache_test.go
git commit -m "$(cat <<'EOF'
feat(cache): add Cache.DisarmProject

CAS-swaps the ArmedProjects map with the single ID removed. Used by
the pipeline when a focus signal lands for an armed project.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Implement `StorePreservingRuntime`

**Files:**
- Modify: `internal/daemon/cache.go`
- Test: `internal/daemon/cache_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/daemon/cache_test.go`:

```go
func TestCache_StorePreservingRuntime_KeepsArmed(t *testing.T) {
	c := NewCache()
	c.ArmAllProjects([]int64{10, 20})

	// Simulate ReloadCache: build a fresh snapshot from "DB" with paused
	// projects and rules, then install via StorePreservingRuntime.
	rebuilt := &CacheSnapshot{
		AllowedBundles:   map[string]bool{"com.example": true},
		AllowedBinaries:  map[string]bool{},
		Rules:            nil,
		PausedProjectIDs: map[int64]bool{30: true},
	}
	c.StorePreservingRuntime(rebuilt)

	snap := c.Snapshot()
	if !snap.AllowedBundles["com.example"] {
		t.Errorf("DB-driven fields lost, got %v", snap.AllowedBundles)
	}
	if !snap.PausedProjectIDs[30] {
		t.Errorf("PausedProjectIDs lost, got %v", snap.PausedProjectIDs)
	}
	if !snap.ArmedProjects[10] || !snap.ArmedProjects[20] {
		t.Errorf("ArmedProjects not preserved across reload, got %v", snap.ArmedProjects)
	}
}

func TestCache_StorePreservingRuntime_HandlesNilPrev(t *testing.T) {
	c := NewCache() // ArmedProjects starts as empty (not nil)
	rebuilt := &CacheSnapshot{}
	c.StorePreservingRuntime(rebuilt)
	if c.Snapshot().ArmedProjects == nil {
		t.Errorf("ArmedProjects should be non-nil after preserve, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/daemon/ -run TestCache_StorePreservingRuntime -v`
Expected: FAIL with `c.StorePreservingRuntime undefined`.

- [ ] **Step 3: Implement the method**

Add the `maps` import at the top of `internal/daemon/cache.go` (if not already present):

```go
import (
	"maps"
	"sync/atomic"

	"github.com/rian/antitimely/internal/domain"
)
```

Append to `internal/daemon/cache.go`:

```go
// StorePreservingRuntime installs next as the current snapshot, but first
// copies ArmedProjects forward from the previous snapshot. Arm state is
// runtime-only (not DB-derived) and must survive every ReloadCache caller:
// rule edits, watched-program changes, single project pause/resume, etc.
//
// Called instead of Store from ReloadCache.
func (c *Cache) StorePreservingRuntime(next *CacheSnapshot) {
	for {
		prev := c.ptr.Load()
		next.ArmedProjects = maps.Clone(prev.ArmedProjects)
		if next.ArmedProjects == nil {
			next.ArmedProjects = map[int64]bool{}
		}
		if c.ptr.CompareAndSwap(prev, next) {
			return
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run TestCache_StorePreservingRuntime -v`
Expected: PASS.

- [ ] **Step 5: Run the full daemon package test suite**

Run: `go test ./internal/daemon/ -count=1`
Expected: PASS for all tests.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/cache.go internal/daemon/cache_test.go
git commit -m "$(cat <<'EOF'
feat(cache): add StorePreservingRuntime for cache reloads

ReloadCache and other DB-derived snapshot rebuilds use this to install
fresh DB state while preserving runtime-only fields (ArmedProjects).
maps.Clone keeps the operation lock-free under the existing
atomic.Pointer + CAS pattern.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Wire `ReloadCache` to use `StorePreservingRuntime`

**Files:**
- Modify: `internal/daemon/rpc.go:327`
- Test: `internal/daemon/cache_test.go` (integration through RPC tested in Task 10; this task gets a focused regression test)

- [ ] **Step 1: Write the failing test**

Append to `internal/daemon/rpc_test.go` (note: imports `github.com/rian/antitimely/internal/store`):

```go
func TestRPC_ReloadCache_PreservesArmedProjects(t *testing.T) {
	client, _, cache := setupRPCServer(t)
	_ = client // unused; we invoke ReloadCache via the service struct directly

	// Pre-arm two project IDs.
	cache.ArmAllProjects([]int64{100, 200})

	// Trigger a cache reload (the simplest way from a test is via a no-op
	// watched-program add, which calls ReloadCache).
	if err := client.Call(rpcapi.ServiceName+".WatchAdd",
		rpcapi.WatchAddArgs{Kind: "bundle", Identifier: "com.preserve.test"},
		&rpcapi.WatchAddReply{}); err != nil {
		t.Fatalf("WatchAdd: %v", err)
	}

	snap := cache.Snapshot()
	if !snap.ArmedProjects[100] || !snap.ArmedProjects[200] {
		t.Errorf("ArmedProjects lost across ReloadCache, got %v", snap.ArmedProjects)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestRPC_ReloadCache_PreservesArmedProjects -v`
Expected: FAIL — the existing `c.Store(snap)` in `ReloadCache` replaces the whole snapshot, dropping ArmedProjects.

- [ ] **Step 3: Flip the call site**

In `internal/daemon/rpc.go`, find line 327 (inside `ReloadCache`):

```go
s.Cache.Store(snap)
```

Change to:

```go
s.Cache.StorePreservingRuntime(snap)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestRPC_ReloadCache_PreservesArmedProjects -v`
Expected: PASS.

- [ ] **Step 5: Run the full daemon package test suite**

Run: `go test ./internal/daemon/ -count=1`
Expected: PASS — regression guard against accidental tear-out of DB-driven fields.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/rpc.go internal/daemon/rpc_test.go
git commit -m "$(cat <<'EOF'
feat(daemon): preserve arm state across cache reloads

ReloadCache now installs the DB-derived snapshot via
StorePreservingRuntime, copying ArmedProjects forward from the previous
snapshot. Every existing ReloadCache caller (pause/resume, rule edits,
watched-program changes) now keeps runtime arm state intact.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Pipeline — focus signal disarms

**Files:**
- Modify: `internal/daemon/pipeline.go`
- Test: `internal/daemon/pipeline_test.go`

Spec reference: Event B in the data flow section.

- [ ] **Step 1: Write the failing test**

Append to `internal/daemon/pipeline_test.go`:

```go
func TestPipeline_FocusSignal_DisarmsProject(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "armed-proj", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	bundle := "com.example.editor"
	title := "armed-proj"
	cache.Store(&CacheSnapshot{
		AllowedBundles:   map[string]bool{bundle: true},
		AllowedBinaries:  map[string]bool{},
		Rules:            []domain.RuleSpec{{ID: 1, ProjectID: projID, Priority: 100, MatchBundleID: &bundle, MatchTitleSubstr: &title}},
		PausedProjectIDs: map[int64]bool{},
		ArmedProjects:    map[int64]bool{projID: true},
	})
	br.IdleSecondsVal = 5
	br.FrontmostInfoVal = macos.FrontmostInfo{BundleID: bundle, PID: 1234}
	br.FocusedTitle = "armed-proj — main"

	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick: %v", err)
	}

	// Focus tick should land.
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks WHERE project_id=?`, projID).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 focus tick, got %d", n)
	}
	// Project should be disarmed afterward.
	if cache.Snapshot().ArmedProjects[projID] {
		t.Errorf("project should be disarmed after focus signal, still armed: %v", cache.Snapshot().ArmedProjects)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestPipeline_FocusSignal_DisarmsProject -v`
Expected: FAIL — the focus tick lands (existing behavior is correct), but the project remains armed because no disarm wiring exists yet.

- [ ] **Step 3: Implement disarm-on-focus**

In `internal/daemon/pipeline.go`, inside `RunTick`'s per-signal loop. Locate the block right after the rule-match call (around line 126):

```go
pid := domain.MatchRules(sig, snap.Rules)
```

Immediately after, add:

```go
if sig.IsFocus() && pid != nil {
    p.cache.DisarmProject(*pid)
}
```

- [ ] **Step 4: Add the isolation test (focus on A does not disarm B)**

Append to `internal/daemon/pipeline_test.go`:

```go
func TestPipeline_FocusSignal_DoesNotDisarmOtherProjects(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	idA, err := q.AddProject(ctx, store.AddProjectParams{Name: "alpha", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("AddProject alpha: %v", err)
	}
	idB, err := q.AddProject(ctx, store.AddProjectParams{Name: "beta", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("AddProject beta: %v", err)
	}
	bundle := "com.example.editor"
	titleA := "alpha"
	cache.Store(&CacheSnapshot{
		AllowedBundles:   map[string]bool{bundle: true},
		AllowedBinaries:  map[string]bool{},
		Rules:            []domain.RuleSpec{{ID: 1, ProjectID: idA, Priority: 100, MatchBundleID: &bundle, MatchTitleSubstr: &titleA}},
		PausedProjectIDs: map[int64]bool{},
		ArmedProjects:    map[int64]bool{idA: true, idB: true},
	})
	br.IdleSecondsVal = 5
	br.FrontmostInfoVal = macos.FrontmostInfo{BundleID: bundle, PID: 1234}
	br.FocusedTitle = "alpha — main"

	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick: %v", err)
	}

	got := cache.Snapshot().ArmedProjects
	if got[idA] {
		t.Errorf("expected alpha disarmed, still armed: %v", got)
	}
	if !got[idB] {
		t.Errorf("expected beta still armed (focus was for alpha), got %v", got)
	}
}
```

- [ ] **Step 5: Run all focus tests to verify**

Run: `go test ./internal/daemon/ -run "TestPipeline_FocusSignal_(DisarmsProject|DoesNotDisarmOtherProjects)" -v`
Expected: PASS for both.

- [ ] **Step 6: Run the full daemon package test suite**

Run: `go test ./internal/daemon/ -count=1`
Expected: PASS — verify no regressions in existing focus tests.

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/pipeline.go internal/daemon/pipeline_test.go
git commit -m "$(cat <<'EOF'
feat(pipeline): disarm project on focus signal

A focus signal resolving to an armed project clears its arm flag.
Focus is the ground-truth user-intent signal and is never gated; it
also acts as the gate-release event for subsequent agent ticks.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Pipeline — agent gate

**Files:**
- Modify: `internal/daemon/pipeline.go`
- Test: `internal/daemon/pipeline_test.go`

Spec reference: Event C in the data flow section. This is the heart of the feature.

- [ ] **Step 1: Write the failing tests**

Append to `internal/daemon/pipeline_test.go`:

```go
func TestPipeline_AgentSignal_Armed_DropsTick(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "gated", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	bin := "claude"
	cwdPrefix := "/Users/rian/work/gated"
	cache.Store(&CacheSnapshot{
		AllowedBundles:   map[string]bool{},
		AllowedBinaries:  map[string]bool{bin: true},
		Rules:            []domain.RuleSpec{{ID: 1, ProjectID: projID, Priority: 100, MatchBinaryName: &bin, MatchCwdPrefix: &cwdPrefix}},
		CwdPrefixes:      []string{cwdPrefix},
		PausedProjectIDs: map[int64]bool{},
		ArmedProjects:    map[int64]bool{projID: true},
	})
	br.IdleSecondsVal = 200 // no focus signal possible
	br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: 100}}
	br.CWDByPID = map[int]string{999: cwdPrefix + "/src"}

	// First tick: establish prev CPU sample.
	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick #1: %v", err)
	}
	// Second tick: CPU delta over threshold ⇒ agent signal would fire,
	// but project is armed so the tick must be dropped.
	br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: 200}}
	if err := p.RunTick(ctx, 1005); err != nil {
		t.Fatalf("RunTick #2: %v", err)
	}

	var nTicks int
	db.QueryRow(`SELECT COUNT(*) FROM ticks WHERE project_id=?`, projID).Scan(&nTicks)
	if nTicks != 0 {
		t.Errorf("expected 0 ticks for armed project, got %d", nTicks)
	}

	var nObs int
	db.QueryRow(`SELECT COUNT(*) FROM observations WHERE source='agent' AND binary_name=?`, bin).Scan(&nObs)
	if nObs == 0 {
		t.Errorf("expected observation row preserved (history), got %d", nObs)
	}
}

func TestPipeline_AgentSignal_Unarmed_CountsNormally(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "open", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	bin := "claude"
	cwdPrefix := "/Users/rian/work/open"
	cache.Store(&CacheSnapshot{
		AllowedBundles:   map[string]bool{},
		AllowedBinaries:  map[string]bool{bin: true},
		Rules:            []domain.RuleSpec{{ID: 1, ProjectID: projID, Priority: 100, MatchBinaryName: &bin, MatchCwdPrefix: &cwdPrefix}},
		CwdPrefixes:      []string{cwdPrefix},
		PausedProjectIDs: map[int64]bool{},
		ArmedProjects:    map[int64]bool{}, // NOT armed
	})
	br.IdleSecondsVal = 200
	br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: 100}}
	br.CWDByPID = map[int]string{999: cwdPrefix + "/src"}

	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick #1: %v", err)
	}
	br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: 200}}
	if err := p.RunTick(ctx, 1005); err != nil {
		t.Fatalf("RunTick #2: %v", err)
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks WHERE project_id=?`, projID).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 tick for unarmed project, got %d", n)
	}
}

func TestPipeline_AgentSignal_AfterFocusDisarm_Counts(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "armed-then-focused", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	bundle := "com.example.editor"
	title := "armed-then-focused"
	bin := "claude"
	cwdPrefix := "/Users/rian/work/armed-then-focused"
	cache.Store(&CacheSnapshot{
		AllowedBundles:  map[string]bool{bundle: true},
		AllowedBinaries: map[string]bool{bin: true},
		Rules: []domain.RuleSpec{
			{ID: 1, ProjectID: projID, Priority: 100, MatchBundleID: &bundle, MatchTitleSubstr: &title},
			{ID: 2, ProjectID: projID, Priority: 100, MatchBinaryName: &bin, MatchCwdPrefix: &cwdPrefix},
		},
		CwdPrefixes:      []string{cwdPrefix},
		PausedProjectIDs: map[int64]bool{},
		ArmedProjects:    map[int64]bool{projID: true},
	})
	br.IdleSecondsVal = 5
	br.FrontmostInfoVal = macos.FrontmostInfo{BundleID: bundle, PID: 1234}
	br.FocusedTitle = "armed-then-focused — main"
	br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: 100}}
	br.CWDByPID = map[int]string{999: cwdPrefix + "/src"}

	// Tick #1: focus disarms; agent has no prev CPU sample yet so no agent tick.
	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick #1: %v", err)
	}
	// Tick #2: focus tick lands again (still disarmed), agent tick now also lands.
	br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: 200}}
	if err := p.RunTick(ctx, 1005); err != nil {
		t.Fatalf("RunTick #2: %v", err)
	}

	var nFocus, nAgent int
	db.QueryRow(`SELECT COUNT(*) FROM ticks t JOIN observations o ON o.id=t.observation_id WHERE t.project_id=? AND o.source='focus'`, projID).Scan(&nFocus)
	db.QueryRow(`SELECT COUNT(*) FROM ticks t JOIN observations o ON o.id=t.observation_id WHERE t.project_id=? AND o.source='agent'`, projID).Scan(&nAgent)
	if nFocus != 2 {
		t.Errorf("expected 2 focus ticks, got %d", nFocus)
	}
	if nAgent != 1 {
		t.Errorf("expected 1 agent tick after disarm, got %d", nAgent)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/daemon/ -run TestPipeline_AgentSignal_Armed -v`
Expected: FAIL — the armed agent tick lands because no gate exists yet.

- [ ] **Step 3: Implement the gate**

In `internal/daemon/pipeline.go`, inside `RunTick`'s per-signal loop. After the existing paused-project branch (~line 144 in the current code, immediately after the auto-resume block, but before `var projectID sql.NullInt64`), add:

```go
if sig.IsAgent() && pid != nil && snap.ArmedProjects[*pid] {
    continue
}
```

This sits between the paused-handling block (which already ends with `continue` for the non-agent paused case) and the `projectID := sql.NullInt64{...}` block.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run TestPipeline_AgentSignal_Armed -v`
Expected: PASS for all three new tests.

- [ ] **Step 5: Run the full daemon package test suite**

Run: `go test ./internal/daemon/ -count=1`
Expected: PASS — no regressions in the existing agent-signal suite.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/pipeline.go internal/daemon/pipeline_test.go
git commit -m "$(cat <<'EOF'
feat(pipeline): gate agent ticks behind armed-project state

When a signal is agent-source and the resolved project is armed, drop
the tick. Observation row still gets upserted so the user can see
"claude was burning CPU in <dir>" in history, but no time credits to
the project until a focus signal arrives.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Pipeline — auto-resume arms

**Files:**
- Modify: `internal/daemon/pipeline.go`
- Test: `internal/daemon/pipeline_test.go`

Spec reference: Event D in the data flow section.

- [ ] **Step 1: Write the failing test**

Append to `internal/daemon/pipeline_test.go`:

```go
func TestPipeline_AutoResume_ArmsAndDropsCurrentTick(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "paused-arm", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if err := q.SetProjectPaused(ctx, store.SetProjectPausedParams{Paused: 1, Name: "paused-arm"}); err != nil {
		t.Fatalf("SetProjectPaused: %v", err)
	}

	bin := "claude"
	cwdPrefix := "/Users/rian/work/paused-arm"
	cache.Store(&CacheSnapshot{
		AllowedBundles:   map[string]bool{},
		AllowedBinaries:  map[string]bool{bin: true},
		Rules:            []domain.RuleSpec{{ID: 1, ProjectID: projID, Priority: 100, MatchBinaryName: &bin, MatchCwdPrefix: &cwdPrefix}},
		CwdPrefixes:      []string{cwdPrefix},
		PausedProjectIDs: map[int64]bool{projID: true},
		ArmedProjects:    map[int64]bool{},
	})
	br.IdleSecondsVal = 200
	br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: 100}}
	br.CWDByPID = map[int]string{999: cwdPrefix + "/src"}

	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick #1: %v", err)
	}
	br.Processes = []macos.ProcessSample{{PID: 999, Name: bin, CPUTicks: 200}}
	if err := p.RunTick(ctx, 1005); err != nil {
		t.Fatalf("RunTick #2: %v", err)
	}

	// Project should be unpaused in DB.
	var paused int
	db.QueryRow(`SELECT paused FROM projects WHERE id=?`, projID).Scan(&paused)
	if paused != 0 {
		t.Errorf("expected project auto-resumed (paused=0), got %d", paused)
	}
	// And in cache.
	if cache.Snapshot().PausedProjectIDs[projID] {
		t.Errorf("expected project not in PausedProjectIDs after auto-resume")
	}
	// But armed.
	if !cache.Snapshot().ArmedProjects[projID] {
		t.Errorf("auto-resumed project should be armed, got %v", cache.Snapshot().ArmedProjects)
	}
	// And no agent tick for this round.
	var nTicks int
	db.QueryRow(`SELECT COUNT(*) FROM ticks WHERE project_id=?`, projID).Scan(&nTicks)
	if nTicks != 0 {
		t.Errorf("expected 0 ticks (gate drops auto-resumed agent signal), got %d", nTicks)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestPipeline_AutoResume_ArmsAndDropsCurrentTick -v`
Expected: FAIL — auto-resume currently happens but doesn't arm; the agent tick lands.

- [ ] **Step 3: Implement arming in the auto-resume path**

In `internal/daemon/pipeline.go`, inside the existing paused-project / auto-resume branch (around line 142). Find:

```go
if err := p.q.ResumeProjectByID(ctx, *pid); err != nil {
    log.Printf("auto-resume project %d: %v", *pid, err)
    continue
}
p.cache.MarkProjectActive(*pid)
log.Printf("auto-resumed project %d: agent activity (binary=%q cwd=%q)", *pid, sig.BinaryName, sig.Cwd)
```

Modify to call `ArmProject` and then explicitly `continue` (do NOT rely on the Task 8 gate to drop this tick):

```go
if err := p.q.ResumeProjectByID(ctx, *pid); err != nil {
    log.Printf("auto-resume project %d: %v", *pid, err)
    continue
}
p.cache.MarkProjectActive(*pid)
p.cache.ArmProject(*pid)
log.Printf("auto-resumed project %d: agent activity (binary=%q cwd=%q)", *pid, sig.BinaryName, sig.Cwd)
continue
```

**Why the explicit `continue`:** The `snap` variable inside `RunTick` was captured at the very start of the tick (line 64), before this `ArmProject` mutation. Reading `snap.ArmedProjects[*pid]` from the downstream Task 8 gate would still see `false` (the project wasn't armed at tick-start). The explicit `continue` here makes the auto-resume's "drop this tick" intent independent of snapshot freshness. End effect is identical to what Event D in the spec describes.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestPipeline_AutoResume_ArmsAndDropsCurrentTick -v`
Expected: PASS.

- [ ] **Step 5: Run the full daemon package test suite**

Run: `go test ./internal/daemon/ -count=1`
Expected: PASS — verify existing `TestPipeline_PausedProject_AgentSignal_AutoResumes` still passes (it should: under the new behavior, the project still auto-resumes; the only diff is no tick lands on the resume tick, which the existing test doesn't assert on).

- [ ] **Step 6: Verify the existing auto-resume test specifically**

Run: `go test ./internal/daemon/ -run TestPipeline_PausedProject_AgentSignal_AutoResumes -v`
Expected: PASS. If this fails, read the existing test in `pipeline_test.go:576` and confirm whether it asserts on tick count — if it does, the test now needs to assert "0 ticks (because armed)" instead of "≥1 tick"; document the behavior change in the commit.

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/pipeline.go internal/daemon/pipeline_test.go
git commit -m "$(cat <<'EOF'
feat(pipeline): arm project on auto-resume from agent CPU

When agent CPU triggers an auto-resume of a paused project, the
resumed project enters the armed state. The downstream gate then
drops the triggering agent tick, so a stale background process can
no longer silently un-mute a paused project AND credit time on the
same tick.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: `ProjectResumeAll` arms all projects

**Files:**
- Modify: `internal/daemon/rpc.go:473`
- Test: `internal/daemon/rpc_test.go`

Spec reference: Touch points item 3 / Event A.

- [ ] **Step 1: Write the failing tests**

Append to `internal/daemon/rpc_test.go`:

```go
func TestRPC_ProjectResumeAll_ArmsAllProjects(t *testing.T) {
	client, db, cache := setupRPCServer(t)
	q := store.New(db)
	ctx := context.Background()

	for _, name := range []string{"alpha", "beta", "gamma"} {
		if _, err := q.AddProject(ctx, store.AddProjectParams{Name: name, CreatedAt: 1000}); err != nil {
			t.Fatalf("AddProject %s: %v", name, err)
		}
	}

	var reply rpcapi.ProjectResumeAllReply
	if err := client.Call(rpcapi.ServiceName+".ProjectResumeAll",
		rpcapi.ProjectResumeAllArgs{}, &reply); err != nil {
		t.Fatalf("ProjectResumeAll: %v", err)
	}

	snap := cache.Snapshot()
	if len(snap.ArmedProjects) != 3 {
		t.Errorf("expected 3 armed projects, got %d (%v)", len(snap.ArmedProjects), snap.ArmedProjects)
	}
}

func TestRPC_ProjectResumeAll_NoProjects_NoPanic(t *testing.T) {
	client, _, cache := setupRPCServer(t)

	var reply rpcapi.ProjectResumeAllReply
	if err := client.Call(rpcapi.ServiceName+".ProjectResumeAll",
		rpcapi.ProjectResumeAllArgs{}, &reply); err != nil {
		t.Fatalf("ProjectResumeAll: %v", err)
	}

	if len(cache.Snapshot().ArmedProjects) != 0 {
		t.Errorf("expected empty arm map, got %v", cache.Snapshot().ArmedProjects)
	}
}

func TestRPC_ProjectResume_Single_DoesNotArm(t *testing.T) {
	client, db, cache := setupRPCServer(t)
	q := store.New(db)
	ctx := context.Background()

	if _, err := q.AddProject(ctx, store.AddProjectParams{Name: "solo", CreatedAt: 1000}); err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	if err := client.Call(rpcapi.ServiceName+".ProjectResume",
		rpcapi.ProjectResumeArgs{Name: "solo"},
		&rpcapi.ProjectResumeReply{}); err != nil {
		t.Fatalf("ProjectResume: %v", err)
	}

	if len(cache.Snapshot().ArmedProjects) != 0 {
		t.Errorf("single resume should not arm anything, got %v", cache.Snapshot().ArmedProjects)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/daemon/ -run "TestRPC_ProjectResumeAll|TestRPC_ProjectResume_Single" -v`
Expected: FAIL — `ProjectResumeAll` currently does no arming.

- [ ] **Step 3: Rewrite `ProjectResumeAll`**

In `internal/daemon/rpc.go`, replace the body of `ProjectResumeAll` (line 473-482). Current:

```go
func (s *AntitimelyService) ProjectResumeAll(args rpcapi.ProjectResumeAllArgs, reply *rpcapi.ProjectResumeAllReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	n, err := s.Q.ResumeAllProjects(ctx)
	if err != nil {
		return err
	}
	reply.Count = n
	return s.ReloadCache()
}
```

New:

```go
func (s *AntitimelyService) ProjectResumeAll(args rpcapi.ProjectResumeAllArgs, reply *rpcapi.ProjectResumeAllReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	n, err := s.Q.ResumeAllProjects(ctx)
	if err != nil {
		return err
	}
	reply.Count = n
	if err := s.ReloadCache(); err != nil {
		return err
	}
	rows, err := s.Q.ListProjects(ctx)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	s.Cache.ArmAllProjects(ids)
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run "TestRPC_ProjectResumeAll|TestRPC_ProjectResume_Single" -v`
Expected: PASS for all three.

- [ ] **Step 5: Run the full daemon package test suite**

Run: `go test ./internal/daemon/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/rpc.go internal/daemon/rpc_test.go
git commit -m "$(cat <<'EOF'
feat(rpc): ProjectResumeAll arms every project

After the DB resume + cache reload, ListProjects enumerates every
project ID and ArmAllProjects installs them as needing focus before
agent ticks count. Single-project ProjectResume is unchanged (a
deliberate "count this now" gesture should not arm).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: `ProjectAdd` arms the new project

**Files:**
- Modify: `internal/daemon/rpc.go:376`
- Test: `internal/daemon/rpc_test.go`

Spec reference: Event E, touch points item 3 bullet 3.

- [ ] **Step 1: Write the failing test**

Append to `internal/daemon/rpc_test.go`:

```go
func TestRPC_ProjectAdd_ArmsNewProject(t *testing.T) {
	client, _, cache := setupRPCServer(t)

	var reply rpcapi.ProjectAddReply
	if err := client.Call(rpcapi.ServiceName+".ProjectAdd",
		rpcapi.ProjectAddArgs{Name: "fresh"},
		&reply); err != nil {
		t.Fatalf("ProjectAdd: %v", err)
	}
	if reply.ID == 0 {
		t.Fatalf("expected non-zero new project id, got 0")
	}
	if !cache.Snapshot().ArmedProjects[reply.ID] {
		t.Errorf("expected new project %d armed, got %v", reply.ID, cache.Snapshot().ArmedProjects)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestRPC_ProjectAdd_ArmsNewProject -v`
Expected: FAIL — `ProjectAdd` does not arm yet.

- [ ] **Step 3: Add the arm call**

In `internal/daemon/rpc.go`, inside `ProjectAdd` (line 376-397). Find:

```go
id, err := s.Q.AddProject(ctx, store.AddProjectParams{
    Name:      args.Name,
    CompanyID: companyID,
    CreatedAt: time.Now().Unix(),
})
if err != nil {
    return err
}
reply.ID = id
return nil
```

Modify to:

```go
id, err := s.Q.AddProject(ctx, store.AddProjectParams{
    Name:      args.Name,
    CompanyID: companyID,
    CreatedAt: time.Now().Unix(),
})
if err != nil {
    return err
}
s.Cache.ArmProject(id)
reply.ID = id
return nil
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestRPC_ProjectAdd_ArmsNewProject -v`
Expected: PASS.

- [ ] **Step 5: Run the full daemon package test suite**

Run: `go test ./internal/daemon/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/rpc.go internal/daemon/rpc_test.go
git commit -m "$(cat <<'EOF'
feat(rpc): ProjectAdd arms newly created project

Closes the gate-hole at project-creation time: a project created after
a prior resume-all would otherwise miss the arm step and start
counting agent ticks immediately.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: `TagSignature` arms create-on-the-fly projects

**Files:**
- Modify: `internal/daemon/rpc.go:531`
- Test: `internal/daemon/rpc_test.go`

Spec reference: Event E (second creation path), touch points item 3 bullet 4.

- [ ] **Step 1: Write the failing test**

First, inspect `TagSignature`'s args in `internal/rpcapi/api.go` to confirm the field names (`ProjectName`, `ObservationID`, `Rule`, `CreateProject`). Then append to `internal/daemon/rpc_test.go`:

```go
func TestRPC_TagSignature_CreateProject_Arms(t *testing.T) {
	client, db, cache := setupRPCServer(t)
	q := store.New(db)
	ctx := context.Background()

	// Seed a single observation row that the tag will reference.
	obsID, err := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "claude", Cwd: "/Users/rian/work/onfly", FirstSeen: 1000,
	})
	if err != nil {
		t.Fatalf("UpsertObservation: %v", err)
	}

	var reply rpcapi.TagSignatureReply
	if err := client.Call(rpcapi.ServiceName+".TagSignature",
		rpcapi.TagSignatureArgs{
			ProjectName:   "onfly-new",
			ObservationID: obsID,
			CreateProject: true,
			// Rule is nil — just create-project + retag single observation.
		},
		&reply); err != nil {
		t.Fatalf("TagSignature: %v", err)
	}

	// Read back the newly-created project id.
	row := db.QueryRow(`SELECT id FROM projects WHERE name=?`, "onfly-new")
	var newID int64
	if err := row.Scan(&newID); err != nil {
		t.Fatalf("scan new project id: %v", err)
	}
	if !cache.Snapshot().ArmedProjects[newID] {
		t.Errorf("expected create-on-the-fly project %d armed, got %v", newID, cache.Snapshot().ArmedProjects)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestRPC_TagSignature_CreateProject_Arms -v`
Expected: FAIL — `TagSignature` does not arm yet.

- [ ] **Step 3: Add the arm call**

In `internal/daemon/rpc.go`, inside `TagSignature` (line 531). Find:

```go
id, err2 := s.Q.AddProject(ctx, store.AddProjectParams{Name: args.ProjectName, CreatedAt: now})
if err2 != nil {
    return err2
}
proj.ID = id
proj.Name = args.ProjectName
```

Modify to:

```go
id, err2 := s.Q.AddProject(ctx, store.AddProjectParams{Name: args.ProjectName, CreatedAt: now})
if err2 != nil {
    return err2
}
s.Cache.ArmProject(id)
proj.ID = id
proj.Name = args.ProjectName
```

The downstream rule-creation transaction (and its terminal `ReloadCache`) will not lose the arm because Task 6 routed `ReloadCache` through `StorePreservingRuntime`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestRPC_TagSignature_CreateProject_Arms -v`
Expected: PASS.

- [ ] **Step 5: Run the full daemon package test suite**

Run: `go test ./internal/daemon/ -count=1`
Expected: PASS — covers the rule-insert ReloadCache path too.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/rpc.go internal/daemon/rpc_test.go
git commit -m "$(cat <<'EOF'
feat(rpc): TagSignature arms create-on-the-fly project

Second project-creation path (besides ProjectAdd) — must also arm so
the gate has no creation-time hole. StorePreservingRuntime ensures the
arm survives the rule-insert ReloadCache that runs later in the same
RPC.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 13: CLI status — show `(armed: needs focus)` note

**Files:**
- Modify: `internal/cli/status.go`
- No new tests (CLI string rendering — visual inspection per spec §Testing).

Spec reference: Touch points item 4.

**Context (verified on the codebase):** the CLI is a separate process from the daemon and reads state via the `Status` RPC. The per-project struct returned is `rpcapi.ProjectTotals` (`internal/rpcapi/api.go:36-40`). The Status handler populates it at `internal/daemon/rpc.go:194-199`. The status rendering has two identical blocks at `internal/cli/status.go:66-77` and `87-98`. Spec touch points item 4 implicitly requires extending the RPC reply with `Armed bool` — pinning the missing piece here.

- [ ] **Step 1: Extend `rpcapi.ProjectTotals`**

In `internal/rpcapi/api.go` around line 36-40, locate:

```go
type ProjectTotals struct {
    Name            string
    BillableSeconds int64
    TodaySeconds    int64
    Paused          bool
}
```

Add the field (no JSON tags — existing fields have none, this is Go RPC):

```go
type ProjectTotals struct {
    Name            string
    BillableSeconds int64
    TodaySeconds    int64
    Paused          bool
    Armed           bool
}
```

- [ ] **Step 2: Populate `Armed` in the Status handler**

In `internal/daemon/rpc.go` at line 194-199, locate:

```go
pt := rpcapi.ProjectTotals{
    Name:            pr.Name,
    BillableSeconds: billableTicks * tickSec,
    TodaySeconds:    todayTicks * tickSec,
    Paused:          isPaused,
}
```

Add the `Armed` line. The per-project source variable here is `pr.ID` (from the `projRows` loop at line 182):

```go
pt := rpcapi.ProjectTotals{
    Name:            pr.Name,
    BillableSeconds: billableTicks * tickSec,
    TodaySeconds:    todayTicks * tickSec,
    Paused:          isPaused,
    Armed:           s.Cache.Snapshot().ArmedProjects[pr.ID],
}
```

Note: calling `Snapshot()` once per project is fine — it's a lock-free atomic load. If the surrounding loop is tight enough to worry about (it isn't, in practice), hoist it once outside the loop.

- [ ] **Step 3: Add the rendering in both status.go blocks**

In `internal/cli/status.go` at lines 66-77, locate:

```go
for _, pr := range co.Projects {
    pausedNote := ""
    if pr.Paused {
        pausedNote = "  (paused)"
    }
    fmt.Printf("    %-36s %s   (today: %s)%s\n",
        pr.Name,
        fmtDuration(pr.BillableSeconds),
        fmtDuration(pr.TodaySeconds),
        pausedNote,
    )
}
```

Change to:

```go
for _, pr := range co.Projects {
    pausedNote := ""
    if pr.Paused {
        pausedNote = "  (paused)"
    }
    armedNote := ""
    if pr.Armed {
        armedNote = "  (armed: needs focus)"
    }
    fmt.Printf("    %-36s %s   (today: %s)%s%s\n",
        pr.Name,
        fmtDuration(pr.BillableSeconds),
        fmtDuration(pr.TodaySeconds),
        pausedNote,
        armedNote,
    )
}
```

Apply the same change to the second identical block at lines 87-98 (the no-company branch).

- [ ] **Step 4: Build the binary**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 5: Smoke test manually (the user has the daemon running)**

```bash
# Rebuild and restart the daemon so the new code is loaded.
./scripts/rebuild.sh
# Then:
./antitimely resume-all
./antitimely status
```

Expected: every project row shows `(armed: needs focus)` after `resume-all`. Focus an IDE window matching one project's rules, wait for one tick (~5s), then re-run `atl status` — that project should no longer show the note.

If the manual smoke test passes, proceed to commit. If it fails, STOP and use `superpowers:systematic-debugging` to find the root cause before further edits.

- [ ] **Step 6: Commit**

```bash
git add internal/rpcapi/api.go internal/daemon/rpc.go internal/cli/status.go
git commit -m "$(cat <<'EOF'
feat(cli): show '(armed: needs focus)' in status

Status RPC reply now carries the per-project armed flag drawn from
Cache.ArmedProjects. The CLI status command renders it next to the
existing (paused) note so the user can see why an armed project's
agent ticks aren't landing.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Final verification

- [ ] **Run the full test suite from the repo root**

Run: `go test ./... -count=1`
Expected: PASS for every package.

- [ ] **Build the binary**

Run: `go build ./...`
Expected: no errors.

- [ ] **End-to-end manual smoke**

If the user wants a live verification:

1. Rebuild and restart the daemon (`./scripts/rebuild.sh` if it exists, or whatever the established workflow is).
2. `atl resume-all` — confirm `atl status` shows every project with `(armed: needs focus)`.
3. Open Antigravity IDE on one project. Wait ~5s.
4. `atl status` — that one project should drop the armed note; agent activity in its tree should now credit ticks.
5. For the original DAAS false-positive scenario: leave DAAS unfocused. Verify no new DAAS ticks are recorded after `resume-all`, even though the stale `claude` session in `daas-back-end` is still burning CPU.

- [ ] **Cleanup of today's bad DAAS ticks (separate task, outside this plan)**

The spec marks this as out of scope. Once the daemon-side fix is verified, the user separately runs the cleanup query they've already opted into:

```sql
-- Backup first
.backup '/Users/rian/.antitimely/db.sqlite.backup-pre-cleanup'

DELETE FROM ticks WHERE project_id=7 AND ts>=<midnight-of-2026-05-27>;
```

(Exact midnight unix-epoch can be computed at cleanup time.)
