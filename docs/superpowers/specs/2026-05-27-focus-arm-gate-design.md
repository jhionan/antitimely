# Focus-Arm Gate for Agent Signals

**Date:** 2026-05-27
**Status:** Design — awaiting implementation

## Problem

Long-running background processes whose CWD sits inside a tracked project tree
keep tripping the agent path (`pipeline.go:198`) and crediting ticks to projects
the user is not actively working on. Today's concrete trigger: a 2-day-old
`claude` session in `daas-back-end`, plus an overnight `npm start` + `esbuild`
pair in `daas-front-end`, generated 3,696 false-positive ticks for project
`Daas` (project_id 7) across 2026-05-27 even though no human focus signal for
DAAS landed at any point. The first tick rolled in at 00:00:07 — hours before
the user started work — which is the smoking gun that the activity is
mechanical, not human.

The daemon currently treats agent CPU as a self-sufficient presence signal,
strong enough to auto-resume a paused project (`pipeline.go:134-144`). It
cannot distinguish "user actively typing in this directory" from "stale
background process burning CPU in this directory."

## Goals

1. After `atl resume-all`, no project credits agent-source ticks until a focus
   signal lands for that project.
2. Focus signals always count and act as the disarm event.
3. Auto-resume on agent CPU (paused → active) still fires, but the
   auto-resumed project enters the same armed state — so a stale background
   process can no longer silently un-mute a paused project.
4. New projects created after a `resume-all` start armed by default, so the
   gate doesn't have a hole at project-creation time.

## Non-goals

- Persistence of arm state across daemon restart. Arm is in-memory only;
  restart clears it. The user re-establishes the gate by running `resume-all`
  again if they want it.
- Daily reset of arm state (e.g. at midnight). Arming is tied to explicit user
  actions (`resume-all`, project creation) and pipeline events (auto-resume),
  not the clock.
- Single-project `atl resume <name>` arming. Manual single-project resume is a
  deliberate "yes, count this now" gesture; it does not arm.
- Gating focus-source ticks. Focus is the ground-truth user-intent signal in
  this model.
- Time-based expiration of arm state.

## Data model

One new field on `CacheSnapshot` in `internal/daemon/cache.go`:

```go
type CacheSnapshot struct {
    // ...existing fields...
    ArmedProjects map[int64]bool // project_id -> armed (needs focus before agent ticks count)
}
```

Semantics:
- `ArmedProjects[id] == true` → project requires a focus signal before
  agent-source ticks credit. Focus-source ticks are never gated.
- Key absent or `false` → project counts normally.

`NewCache()` initializes `ArmedProjects` to an empty map. Projects start
un-armed at daemon boot.

## Cache mutators

All three follow the existing CAS-loop pattern in `MarkProjectActive`
(`cache.go:50-67`):

- `ArmAllProjects(ids []int64)` — replaces the map with `{id: true}` for every
  id. **Guard:** if `len(ids) == 0`, no-op (do not swap an empty map in).
  Reasoning: an empty input is a degenerate edge case; treating it as
  "disarm everyone" would silently widen the failure surface.
- `ArmProject(id int64)` — sets `ArmedProjects[id] = true`. Idempotent.
- `DisarmProject(id int64)` — removes `id` from the map. CAS-loop returns
  early if the key is already absent.

## Touch points

1. **`internal/daemon/cache.go`**
   - Add `ArmedProjects` to `CacheSnapshot`.
   - Initialize in `NewCache()`.
   - Implement `ArmAllProjects`, `ArmProject`, `DisarmProject`.
   - Add a new `Cache.StorePreservingRuntime(*CacheSnapshot)` method that
     copies the existing `ArmedProjects` into the incoming snapshot and CAS-
     swaps. Equivalent in shape to `MarkProjectActive` but the value being
     installed comes from the caller:
     ```go
     for {
         prev := c.ptr.Load()
         next.ArmedProjects = maps.Clone(prev.ArmedProjects)
         if next.ArmedProjects == nil { next.ArmedProjects = map[int64]bool{} }
         if c.ptr.CompareAndSwap(prev, next) { return }
     }
     ```
     `maps.Clone` is stdlib in Go 1.21+ (project is on 1.25). The DB-driven
     fields on `next` are built once by the caller; only the clone + swap is
     in the loop.

2. **`internal/daemon/pipeline.go`**
   - After `pid := domain.MatchRules(sig, snap.Rules)` and before
     `InsertTick`:
     - If `sig.IsFocus()` and `pid != nil` → `p.cache.DisarmProject(*pid)`.
     - If `sig.IsAgent()` and `pid != nil` and `snap.ArmedProjects[*pid]` →
       `continue` (drop the tick; the observation row from line 105 stays
       so history is preserved).
   - In the existing auto-resume branch (`pipeline.go:142`, after
     `MarkProjectActive`): add `p.cache.ArmProject(*pid)`. The newly-resumed
     project then drops its own current-tick agent signal via the gate above.

3. **`internal/daemon/rpc.go`**
   - `ReloadCache` (line 255): replace `s.Cache.Store(snap)` at line 327
     with `s.Cache.StorePreservingRuntime(snap)`. The DB-loading code above
     is unchanged. This single change ensures every existing call site
     (`ProjectPause`, `ProjectResume`, `ProjectPauseAll`, `WatchedProgram*`,
     rule edits, etc. — see line 327 callers) preserves arm state across
     reload.
   - `ProjectResumeAll` (line 473): rewrite body to —
     1. `s.Q.ResumeAllProjects(ctx)` — existing DB update.
     2. `s.ReloadCache()` — existing trailing reload, moved up so the
        snapshot's `PausedProjectIDs` is current before we arm.
     3. `s.Q.ListProjects(ctx)` — get all IDs.
     4. `s.Cache.ArmAllProjects(ids)` — install arm map as the final step.
        If `ListProjects` fails, return the error. Do not paper over —
        silent disarm is exactly the bug being fixed.
   - `ProjectAdd` (line 376): after `s.Q.AddProject(...)` succeeds, call
     `s.Cache.ArmProject(id)`.
   - `TagSignature` (line 531): in the create-on-the-fly branch, same call
     after the project insert. Note the existing function does a
     `ReloadCache` at the end of the rule-creation transaction
     (line 626 / 669) — `StorePreservingRuntime` ensures the just-armed new
     project survives that reload.
   - `ProjectResume` (single-project): unchanged. Single resume does not arm.

4. **`internal/cli/status.go`**
   - Surface arm state next to the existing `(paused)` note (~lines 67-90):
     when `ArmedProjects[id]` is true, append `(armed: needs focus)` to the
     row. Lets the user see why a project's agent ticks aren't landing.

5. **Tests** — see §Testing.

Not touched: `internal/domain/`, `queries.sql`, `schema.sql`, `rpcapi/api.go`.

## Data flow per event

**A. `atl resume-all`**
1. RPC `ProjectResumeAll` → `ResumeAllProjects` clears `paused` in DB.
2. `ReloadCache` rebuilds snapshot (paused IDs cleared, arm map preserved
   via `StorePreservingRuntime`).
3. `ListProjects` returns all IDs.
4. `Cache.ArmAllProjects(ids)` installs the armed map via CAS swap.
5. Pipeline's next `Snapshot()` reads the new map.

**B. Focus signal for project X (any time)**
1. `collectFocusSignal` returns `Signal{Source: SourceFocus, ...}`.
2. `MatchRules` → `pid = &X`.
3. `Cache.DisarmProject(X)` fires unconditionally (idempotent if already
   disarmed).
4. `InsertTick` runs normally. Focus is never gated.

**C. Agent signal for project X, X is armed**
1. `collectAgentSignals` returns `Signal{Source: SourceAgent, ...}`.
2. Observation upserted (existing line 105) — history preserved.
3. `MatchRules` → `pid = &X`.
4. Paused branch not taken (X is not paused).
5. Gate: `snap.ArmedProjects[X] == true` → `continue`. No tick row.

**D. Agent signal for project X, X is paused (auto-resume path)**
1. Paused-branch in `pipeline.go:134` triggers.
2. `ResumeProjectByID(X)` in DB; `MarkProjectActive(X)` in cache.
3. `ArmProject(X)` — newly-resumed project enters armed state.
4. Falls through to the gate check from Event C step 5 → tick dropped.
5. Net effect: project resumed but did not credit this tick. Future agent
   ticks won't credit until focus arrives.

**E. New project created (`ProjectAdd` or `TagSignature` create-on-the-fly)**
1. DB insert returns new ID.
2. `Cache.ArmProject(newID)` immediately after the insert.
3. First focus signal for the project disarms it; agent-only activity does
   not credit before that.

**F. `atl resume <project>` (single)**
1. Existing path — no change.
2. Arm state untouched. Single resume is a deliberate "count this now."

**G. Cache reload (rule or allowlist change via RPC)**
1. New snapshot built from DB.
2. `ArmedProjects` copied forward from the pre-rebuild snapshot.
3. Atomic pointer swap.

## Error handling and edge cases

- **`ListProjects` fails inside `ProjectResumeAll`** — DB resume already
  committed, arm step cannot run. Return the RPC error. Re-running
  `resume-all` succeeds idempotently on the DB side and arms on the second
  attempt.

- **CAS-loop contention** — handled by retry; the loop terminates only on
  successful swap. No external failure path.

- **Cache reload races with `DisarmProject`** — sub-millisecond window where
  a disarm might be overwritten by a reload that started before it. Cost: one
  tick's focus signal ignored; the next tick disarms. Acceptable.

- **Stale entries for deleted projects** — left in `ArmedProjects` until
  daemon restart. Harmless (rules cascade-deleted; no signal will match the
  dead pid). Not worth cleanup code.

- **`ArmAllProjects` with empty slice** — explicit no-op, see §Cache
  mutators. Prevents an empty `ListProjects` return from disarming the world.

- **Auto-resume DB failure** (`ResumeProjectByID` errors in pipeline) —
  existing log-and-continue path. Neither `MarkProjectActive` nor the new
  `ArmProject` call fires. Project stays paused. Already correct.

- **Idempotency**:
  - `ArmProject` on already-armed → no-op.
  - `DisarmProject` on already-disarmed → no-op (early return in CAS loop).
  - `ArmAllProjects` re-called → installs a fresh armed-everyone map; any
    project that had been disarmed becomes armed again. Intended.

## Testing

**`cache_test.go`**
- `ArmAllProjects([1,2,3])` installs the expected map.
- `ArmAllProjects(nil)` and `ArmAllProjects([]int64{})` leave the map
  unchanged.
- `ArmProject` / `DisarmProject` round-trip.
- Concurrent goroutines mutating disjoint IDs converge.
- `ArmedProjects` survives a snapshot rebuild from DB.

**`pipeline_test.go`** — via the existing fake bridge + scripted-signals
harness:
- Armed + agent-only → observation row inserted, tick row absent.
- Armed + focus signal → tick inserted, project disarmed afterward.
- Armed + focus then agent on next tick → both tick rows present.
- Unarmed + agent → tick inserted (regression guard).
- Focus for project A does not disarm project B.
- Paused + armed + agent (auto-resume path) → `paused=0`, armed=true, no
  tick row.

**`rpc_test.go`**
- `ProjectResumeAll` with three projects → all three armed.
- `ProjectResumeAll` with no projects → no panic, no side effects beyond
  the existing DB reset.
- `ProjectResume` (single) leaves the arm map untouched for that ID.
- `ProjectAdd` arms the new ID.
- `TagSignature` with `CreateProject: true` arms the new ID.
- Injected `ListProjects` failure in `ProjectResumeAll` → RPC error, arm
  map untouched.

**Not tested** (YAGNI):
- The `(armed: needs focus)` status-row string. Visual inspection.
- Daemon-restart behavior — explicit non-feature.
- Long-running stability under thousands of arm/disarm cycles — same CAS
  pattern as `MarkProjectActive`, already covered.

## Out of scope (explicit anti-features)

- DB schema change. No new column, no new query, no migration.
- New RPC method. Re-purposing existing `ProjectResumeAll`, `ProjectResume`,
  `ProjectAdd`, `TagSignature`.
- Cleanup of today's existing false-positive DAAS ticks. Tracked separately;
  the user has opted to delete today's DAAS ticks once the daemon side is
  fixed.
- Per-project arm duration / TTL.
