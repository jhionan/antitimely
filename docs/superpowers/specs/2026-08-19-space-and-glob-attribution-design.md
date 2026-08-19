# Space- and glob-based attribution — design

**Date:** 2026-08-19
**Status:** approved (brainstorming) — ready for implementation plan

## Problem

`MD-Tracker` ("Master Data") is a module **inside** the DAAS repository, not a separate
checkout. Attribution keys on a process's working directory, so MD work is
indistinguishable from Daas work by cwd alone, and rule 18
(`match_cwd_prefix = /Users/rian/focaApp/bclouder/daas/`, priority 100) claims all of it.

Two distinct failure modes were measured on 2026-08-18 and 2026-08-19:

1. **Ephemeral worktrees.** MD work happens in short-lived git worktrees under
   `.claude/worktrees/` — `md-tracker-scaffold`, `md-engine`, `md-mirror`, `md-material`,
   `md-notifications`, `md-tracker`, and `md-fe` (the last under `daas-front-end`) all
   appeared within two days, and four were **already deleted** by the time rules were
   written for them. A per-worktree `match_cwd_prefix` rule is always authored *after* the
   time has been misattributed, and prefix matching cannot express "starts with `md-`"
   because the matcher requires a `/` boundary (`match.go`: `HasPrefix(cwd, prefix+"/")`).
   On 2026-08-19 this accounted for roughly 2.6 h.

2. **Root-cwd sessions.** The `MD-tracker` herdr space runs its agent with cwd set to the
   repository root, byte-identical to the `daas-back-end` space. Two live processes
   confirmed it: pid 22260 (`HERDR_PANE_ID=wN:p1`, the MD space) and pid 22625
   (`wM:p1`, the Daas space) shared the exact same cwd. No cwd rule can separate them.
   On 2026-08-19 this accounted for roughly 2.3 h.

Together those were ~4.9 h of a 7.69 h day attributed to the wrong project. The damage is
confined to the per-project timesheet — company-level dedup means the invoice total is
unaffected — but the timesheet is what the client reads.

Corrections have so far required hand-written SQL, because both retroactive queries
(`ApplyRuleRetroactivelyCounted`, `RetagSingleObservation`) are gated on
`project_id IS NULL`. Once a tick is stamped `Daas`, no CLI path can move it.

## Goals

- **Attribute ephemeral worktrees automatically** — one rule covers every current and
  future `md-*` worktree, with no per-worktree maintenance.
- **Attribute root-cwd work by herdr space**, so two sessions sharing a cwd resolve to
  different projects.
- **Keep one precedence system** — both mechanisms are ordinary `match_*` clauses decided
  by the existing (priority, id) ordering.
- **Survive a space rename** without silently misattributing.
- **Make rules self-service** — creating a rule must not require raw SQL.
- **Fail closed** — any missing input degrades to today's behaviour, never to a wrong
  binding.

## Non-goals

- **No focus-signal tagging.** Ghostty is deliberately absent from `watched_programs` and
  its window title is the constant string `herdr`, so focus produces no attributable ticks
  from terminal work regardless.
- **No herdr socket IPC.** The `HERDR_PANE_ID` environment variable carries the same
  information without depending on an undocumented protocol or on herdr running.
- **No automatic retagging of historical ticks.** Existing attribution stays as-is;
  2026-08-18 and 2026-08-19 were already corrected by hand.
- **No general glob engine.** `*` is supported only in the final path segment (see below).

## Design

### 1. Matching semantics

Both additions are new clauses in `domain.matchOne`. Rule ordering — priority ascending,
then id ascending — is unchanged, so no new precedence concept is introduced.

**Glob in `match_cwd_prefix`.** A value containing `*` is treated as a pattern. The star is
permitted **only in the final path segment**; a pattern with `*` before the last `/` is
rejected at creation time. A pattern matches when it matches the cwd itself or any
ancestor directory of it, always at `/` boundaries:

    pattern  .../daas-back-end/.claude/worktrees/md-*
      .../worktrees/md-engine                                  match
      .../worktrees/md-engine/DAAS.Application.Services.Gos    match   (ancestor)
      .../worktrees/md-anything-created-tomorrow               match
      .../worktrees/packing-slip-default-assignee              no match
      .../daas-back-end                                        no match

Ancestor matching is what makes nested build directories resolve: the dotnet `Ce` processes
run several levels below the worktree root.

Values without `*` keep exactly today's behaviour.

The last-segment restriction is not arbitrary. SQLite's `GLOB` lets `*` cross `/`, while
Go's `path.Match` does not. Confining the star to the final segment is what keeps the Go
matcher and the SQL clause provably in agreement rather than subtly divergent.

**`match_space_id`.** A new rule column holding a herdr workspace id (for example `wN`),
compared for exact equality against the signal's `space_id`.

The id is stored rather than the display name. A space rename must never break
attribution, because a silent break bills the wrong project — precisely the bug class this
work closes. The human-readable name is resolved for display only. The known trade-off:
deleting and recreating a space yields a new id and requires re-binding.

### 2. Obtaining the space

herdr exports `HERDR_ENV=1`, `HERDR_SOCKET_PATH` and `HERDR_PANE_ID` into every pane; this
was confirmed by reading the environment of live processes. Child processes inherit the
environment, so an agent launched into a worktree from a pane — and the build processes it
spawns — all carry the pane's id.

- **`internal/macos`** gains `ProcessEnv(pid int) (map[string]string, error)`, implemented
  with `ps -Eww -p <pid>` and added to the Bridge interface plus `fake.go`. This keeps
  every subprocess inside `internal/macos`.
- **`internal/herdr`** (new package) parses `~/.config/herdr/session.json`, cached on
  (mtime, size). It exposes `SpaceForPane(paneID string) (Space, bool)` — splitting
  `wN:p1` on `:` — and `SpaceForSession(uuid string) (Space, bool)`, which scans panes for
  a matching `agent_session.value`. `Space` carries `ID` and `Name`. The package performs
  no subprocess calls, so it is unit-testable against a fixture file.
- **`internal/daemon/pipeline.go`** wires both signals:
  - *agent*: `pid → HERDR_PANE_ID → SpaceForPane`. A `pid → space` cache is swept by the
    existing `livePIDs` cleanup alongside `prevCPU` and `procClass`. A process's
    environment is immutable after `exec`, so this costs one `ps` per newly-busy pid, and
    the busy classifier already gates that to a handful per tick.
  - *transcript*: the Claude Code session UUID is already parsed (it is stored in
    `observations.window_title`) → `SpaceForSession`.
- **`domain.Signal`** gains `SpaceID string`; **`domain.RuleSpec`** gains
  `MatchSpaceID *string`.

The agent pipeline's **tracking pre-filter must change in step**. `collectAgentSignals`
only emits a signal when `procClass.track` is true, and `track` is computed from
`CacheSnapshot.CwdPrefixes` via `cwdUnderAnyPrefix`, whose comment states the upstream
"is this dir tracked?" decision and the downstream "does rule X apply?" decision must never
disagree. That field becomes `CwdPatterns` (holding literals and globs alike, matched with
`MatchesCwd`), and a new `BoundSpaceIDs` set makes a process in a rule-bound space tracked
even when its cwd matches no pattern. Without both, a glob or space rule would match
nothing, because the process would never produce a signal at all.

### 3. Data model and migration

- **`observations`** gains `space_id TEXT NOT NULL DEFAULT ''`, **added to the UNIQUE key**:
  `(source, bundle_id, window_title, binary_name, cwd, space_id)`. Two sessions in the same
  cwd but different spaces then become distinct observations, which is what makes
  `atl review` able to show the space and a future rule able to retag past ticks.
- **`rules`** gains `match_space_id TEXT`. Its `CHECK` constraint (at least one match field
  non-null) must widen to include the new column, otherwise a space-only rule is rejected.

SQLite cannot alter `UNIQUE` or `CHECK` in place, so both require table rebuilds.
`migrateObservationsSpaceID` follows the existing `migrateObservationsSourceCheck` exactly:
read the current DDL from `sqlite_master`, no-op if it already contains `space_id`, disable
foreign keys, rebuild inside a transaction, and preserve row ids because
`ticks.observation_id` depends on their stability. `newObservationsDDL` is updated in step
with `schema.sql`. The `rules` rebuild is simpler — the table holds tens of rows and nothing
references it.

Existing rows migrate to `space_id = ''` and `match_space_id = NULL`, so no existing rule
changes behaviour.

SQL changes go in `schema.sql` and `queries.sql` followed by `make sqlc`; the generated
`internal/store` is never hand-edited. `queries.sql` must stay ASCII-only.

`ApplyRuleRetroactivelyCounted` gains a space clause and its cwd clause becomes
boundary-correct in both modes:

    literal:  (cwd = p OR cwd LIKE p || '/%')
    glob:     (cwd GLOB p OR cwd GLOB p || '/*')

The literal form replaces today's `cwd LIKE p || '%'`, which has no `/` boundary and so
retroactively matches paths the live matcher would reject — a rule for `.../mb-tracker`
would sweep in `.../mb-tracker-foo`. This is a pre-existing defect fixed here because a
glob feature would otherwise widen the divergence.

### 4. CLI surface

- **`atl rules add`** — flags `--project`, `--priority`, `--bundle`, `--title`, `--binary`,
  `--cwd`, `--space`; validates star position and rejects a rule with no match field.
  `atl rules` currently exposes only `list|delete`, and `atl review` only operates on
  *unassigned* signatures, so there is presently no way to author a rule without SQL.
- **`atl spaces`** — lists herdr spaces with their ids, making `wN` discoverable.
- **`atl rules list`** and **`atl review`** display the space as `name (id)`.

Both new subcommands update the dispatch switch **and** `printUsage`. Flags precede
positional arguments, matching the hand-rolled stdlib `flag` dispatch.

### 5. Failure modes

Every input is optional and every absence degrades to current behaviour:

| Condition | Result |
| --- | --- |
| herdr not running / not installed | `space_id = ''`; cwd rules unchanged |
| `HERDR_PANE_ID` absent from a process | `space_id = ''` for that signal |
| `session.json` missing or unparseable | all spaces unresolved; logged once, not per tick |
| `ps -Eww` denied (non-matching uid) | `space_id = ''`; logged once |
| pane id present but unknown to session.json | `space_id` set to the raw workspace id, name blank |

A space-bound rule simply fails to match when `space_id` is empty, so work falls through to
the cwd rules exactly as it does today.

## Testing

- **`internal/domain`** — table-driven cases for each `md-*` worktree, nested build
  directories, `packing-slip-default-assignee` and the repo root as negatives, literal
  prefixes unchanged, and rejection of a star before the last `/`. Space equality tests. A
  precedence test asserting a priority-50 space rule beats priority-100 rule 18.
- **`internal/herdr`** — fixture `session.json` covering a named space, a renamed space, an
  unnamed space (`custom_name: null`), a pane with no `agent_session`, and a missing file.
- **`internal/macos`** — env parsing via the fake bridge; no real processes.
- **Migrations** — modeled on `migrate_invoice_credit_test.go`: build a pre-migration
  database, migrate, assert rows and ids survive and the new UNIQUE is in force, and assert
  idempotency on a second run.
- **Parity test** — the Go matcher and the SQL clause must agree across a fixture corpus of
  cwds. This is the test that would have caught the `LIKE p || '%'` divergence.

## Rollout

After the change ships:

1. Add `match_space_id = wN` → `MD-Tracker`, priority 50.
2. Add `.../daas-back-end/.claude/worktrees/md-*` and
   `.../daas-front-end/.claude/worktrees/md-*` globs → `MD-Tracker`, priority 50.
3. Delete the seven per-worktree rules, four of which already point at deleted directories.
4. `kill -HUP` the daemon to reload the rule cache.

Historical ticks are untouched; new observations fork by `space_id`.

## Risks

- **Coupling to herdr internals.** `session.json`'s shape and the `HERDR_PANE_ID` variable
  are undocumented and may change on a herdr update. Mitigated by failing closed and by
  confining all parsing to `internal/herdr`.
- **Space id churn.** Deleting and recreating a space produces a new id and silently stops
  matching. `atl spaces` makes the current ids inspectable; detecting a stale binding
  automatically is deliberately out of scope.
- **Observation fingerprint fork.** Adding `space_id` to the UNIQUE key means a signal that
  previously produced one observation may now produce two. Rules matching on cwd alone are
  unaffected, since they do not constrain the new column.
