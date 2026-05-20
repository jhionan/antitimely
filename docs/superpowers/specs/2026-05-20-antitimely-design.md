# Antitimely — Phase 1 Design

**Date:** 2026-05-20
**Status:** Draft for review
**Scope:** Phase 1 only (daemon + CLI for project time tracking on macOS). Phase 2 (invoice generator) is out of scope and is referenced only where it shapes Phase 1 decisions.

---

## 1. Summary

Antitimely is a personal time tracker for a macOS-based freelancer who works on multiple client projects in parallel — often with AI coding agents (`claude`, `opencode`, etc.) running concurrently across projects. The daemon observes a user-declared allowlist of programs, deduces which project each observation belongs to, and writes a tick-stream into a local SQLite database. Multiple projects active in the same wall-clock window are billed independently (parallel agent work is real work). A short-lived CLI talks to the daemon over a Unix-domain `net/rpc` socket to drive setup, review unassigned observations, and read reports.

Phase 1 ships a working, daily-usable tracker. Phase 2 (a `net/http` web UI for invoicing) will read the same SQLite database directly and call the daemon's RPC for live state.

## 2. Goals & Non-Goals

### Goals

- **Track time per project with no manual start/stop.** Project attribution is inferred from focus and from allowlisted background processes' working directories.
- **Multi-project parallelism is a first-class concept.** Three agents running for three different clients in the same hour count as three billable hours.
- **No-noise observation.** Only programs the user has explicitly allowlisted are ever observed.
- **Pure Go using only the stdlib for application code.** Third-party tooling is limited to the SQLite driver (`modernc.org/sqlite`, pure-Go) and the `sqlc` code generator (compile-time only).
- **Strong unit-testability.** ~80% of the code path (matching, rule generalization, tick pipeline) must be testable without macOS or system calls.
- **Phase 2 must plug in cleanly** without refactoring Phase 1 storage or domain types.

### Non-Goals (Phase 1)

- Manual `start <project>` / `stop` commands. Auto-detection is the model.
- Client / hourly-rate / currency metadata on projects. Added in Phase 2.
- Invoice generation, PDF export, or a web UI.
- Cross-platform support. macOS only.
- Background-process activity detection by I/O bytes (CPU-delta only for v1).
- Tab-level introspection inside terminal emulators (e.g. distinguishing two tabs inside one iTerm2 window). Out of scope; users with this need can rely on per-process cwd of allowlisted binaries.
- launchd auto-start at login. The user runs `antitimely daemon` manually in v1; a `.plist` is a follow-up.

## 3. Architecture Overview

```
┌──────────────────────────────────────────────────────────┐
│              antitimely daemon  (long-running)           │
│  ────────────────────────────────────────────────────    │
│  • polling goroutine — every TICK_INTERVAL (default 5s)  │
│    collects focus + agent signals, writes ticks          │
│  • net/rpc server on Unix socket                         │
│    (~/.antitimely/antitimely.sock, gob encoding)         │
│  • macOS bridge: osascript / ioreg / ps / lsof           │
│                                                          │
│         owns ──►  ~/.antitimely/db.sqlite (WAL mode)     │
└──────────────────────────────────────────────────────────┘
                          ▲
                          │ net/rpc + gob
                          │
       ┌──────────────────┼──────────────────┐
       │                  │                  │
 antitimely status   antitimely watch ...   antitimely review
 (each is a short-lived process: dial socket, one Call, exit)
```

**Single binary** (`antitimely`) with subcommand dispatch:
- `antitimely daemon` → runs the long-running daemon.
- Anything else → short-lived CLI that opens an RPC client, makes one call, prints, exits.

**No HTTP in Phase 1.** HTTP enters in Phase 2 as a separate localhost listener for the web invoicer.

## 4. Folder Layout

```
antitimely/
├── go.mod
├── go.sum
├── main.go                          # subcommand dispatch only
├── schema.sql                       # CREATE TABLEs; consumed by sqlc & runtime migrator
├── queries.sql                      # all SQL queries; consumed by sqlc
├── sqlc.yaml                        # sqlc generator config
├── internal/
│   ├── store/                       # sqlc-generated; treat as read-only
│   │   ├── db.go                    # (generated) DB type, Queries struct
│   │   ├── models.go                # (generated) row structs
│   │   └── queries.sql.go           # (generated) typed query funcs
│   ├── domain/                      # plain types & pure logic; no DB, no macOS
│   │   ├── types.go                 # Signal, Observation, Rule, Project
│   │   ├── match.go                 # rule evaluation
│   │   └── generalize.go            # rule-from-observation proposal logic
│   ├── macos/                       # only place macOS leaks in
│   │   ├── bridge.go                # Bridge interface (Frontmost, FocusedWindowTitle,
│   │   │                            #   IdleSeconds, ListProcesses, ProcessCWD)
│   │   ├── osascript.go             # Frontmost + FocusedWindowTitle impls
│   │   ├── ioreg.go                 # IdleSeconds impl
│   │   ├── ps.go                    # ListProcesses impl
│   │   └── lsof.go                  # ProcessCWD impl
│   ├── daemon/
│   │   ├── daemon.go                # lifecycle: Run, Shutdown
│   │   ├── poll.go                  # tick loop
│   │   ├── pipeline.go              # one tick: signals → projects → rows
│   │   └── rpc.go                   # net/rpc service registration
│   ├── rpcapi/                      # shared by daemon + cli
│   │   └── api.go                   # Args/Reply types; service & method names
│   └── cli/
│       ├── dispatch.go              # parse os.Args, route to subcommand
│       ├── client.go                # rpc.Dial helper, error formatting
│       ├── status.go                # `status`
│       ├── watch.go                 # `watch add|list|remove`
│       ├── project.go               # `project add|list|delete`
│       ├── review.go                # interactive `review`
│       ├── rules.go                 # `rules list|delete`
│       └── report.go                # `report`
└── docs/
    └── superpowers/specs/2026-05-20-antitimely-design.md
```

### Layering rationale

- `domain` has zero external dependencies. All matching and generalization logic is here, table-test friendly.
- `macos` is the only package that runs subprocesses. Tests inject a fake `Bridge`.
- `store` is regenerated from `schema.sql` + `queries.sql` by `sqlc generate`. Never edited by hand.
- `daemon` and `cli` are thin glue: they wire `macos` + `domain` + `store` into runnable processes.
- `rpcapi` is the contract between daemon and CLI — both import it, neither imports the other.

## 5. Data Model

All tables use SQLite's `STRICT` mode for type safety. Schema lives in `schema.sql`; sqlc reads it to generate Go types.

```sql
-- A project = a billable bucket.
-- Phase 2 will extend with client, hourly_rate, currency on this table.
CREATE TABLE projects (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    created_at  INTEGER NOT NULL              -- unix epoch seconds
) STRICT;

-- The allowlist: what the daemon may observe.
--   kind='bundle' → matched when frontmost (e.g. com.google.antigravity)
--   kind='binary' → matched when running with CPU-delta activity (e.g. claude)
CREATE TABLE watched_programs (
    id          INTEGER PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('bundle', 'binary')),
    identifier  TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    UNIQUE (kind, identifier)
) STRICT;

-- Mapping rules: observation → project. Evaluated in priority order (low first),
-- first match wins. At least one match column must be non-NULL.
CREATE TABLE rules (
    id                  INTEGER PRIMARY KEY,
    project_id          INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    priority            INTEGER NOT NULL DEFAULT 100,
    match_bundle_id     TEXT,
    match_title_substr  TEXT,
    match_binary_name   TEXT,
    match_cwd_prefix    TEXT,
    created_at          INTEGER NOT NULL,
    CHECK (
        match_bundle_id IS NOT NULL OR
        match_title_substr IS NOT NULL OR
        match_binary_name IS NOT NULL OR
        match_cwd_prefix IS NOT NULL
    )
) STRICT;
CREATE INDEX idx_rules_priority ON rules(priority);

-- Distinct signal signatures we've ever seen. One row per unique tuple.
-- Lets us GROUP BY observation_id for review instead of GROUP BY across tick columns.
--
-- Note: SQLite UNIQUE treats NULL ≠ NULL, which would defeat dedup if any of
-- these columns were nullable. We use NOT NULL DEFAULT '' instead. Empty string
-- means "not applicable for this source" (e.g. binary_name='' for focus signals,
-- bundle_id='' for agent signals).
CREATE TABLE observations (
    id              INTEGER PRIMARY KEY,
    source          TEXT NOT NULL CHECK (source IN ('focus', 'agent')),
    bundle_id       TEXT NOT NULL DEFAULT '',
    window_title    TEXT NOT NULL DEFAULT '',
    binary_name     TEXT NOT NULL DEFAULT '',
    cwd             TEXT NOT NULL DEFAULT '',
    first_seen      INTEGER NOT NULL,
    UNIQUE (source, bundle_id, window_title, binary_name, cwd)
) STRICT;

-- Observations the user said "ignore forever" during review.
-- Ticks whose observation is here are filtered out of all rollups.
CREATE TABLE ignored_observations (
    observation_id  INTEGER PRIMARY KEY REFERENCES observations(id) ON DELETE CASCADE,
    ignored_at      INTEGER NOT NULL
) STRICT;

-- The time series. One row per (tick, observation).
-- project_id NULL = unassigned. Composite PK enforces no duplicates.
CREATE TABLE ticks (
    ts              INTEGER NOT NULL,             -- unix epoch seconds
    observation_id  INTEGER NOT NULL REFERENCES observations(id),
    project_id      INTEGER REFERENCES projects(id),
    PRIMARY KEY (ts, observation_id)
) STRICT;
CREATE INDEX idx_ticks_project_ts ON ticks(project_id, ts);
CREATE INDEX idx_ticks_unassigned ON ticks(observation_id) WHERE project_id IS NULL;
```

### Sizing

At a 5s tick interval, 10 active hours, 3 concurrent projects on average:
- ~21,600 ticks/day, each row ~32 bytes → **~700 KB/day**, ~250 MB/year.
- `observations` grows only when novel signatures appear (typically <100 rows total per user).
- All queries are index-backed; no full scans.

### Key queries (in `queries.sql`, sqlc generates typed Go functions)

```sql
-- name: TotalsByProject :many
-- Caller multiplies tick_count × tick_interval_seconds for duration.
-- COUNT(DISTINCT t.ts) implements "same project from N sources in the same tick
-- = 1 credit"; different projects in the same tick each get their own row and
-- each contribute their own distinct-ts count.
SELECT p.name, COUNT(DISTINCT t.ts) AS tick_count
FROM ticks t
JOIN projects p ON p.id = t.project_id
WHERE t.ts BETWEEN ? AND ?
GROUP BY p.id;

-- name: PendingReviewSignatures :many
SELECT o.id, o.source, o.bundle_id, o.window_title, o.binary_name, o.cwd,
       COUNT(t.ts) AS ticks, MAX(t.ts) AS last_seen
FROM observations o
JOIN ticks t ON t.observation_id = o.id
WHERE t.project_id IS NULL
  AND o.id NOT IN (SELECT observation_id FROM ignored_observations)
GROUP BY o.id
ORDER BY ticks DESC
LIMIT ?;

-- name: ApplyRuleRetroactively :exec
UPDATE ticks
SET project_id = ?1
WHERE project_id IS NULL
  AND observation_id IN (
      SELECT id FROM observations
      WHERE (?2 IS NULL OR bundle_id = ?2)
        AND (?3 IS NULL OR window_title LIKE '%' || ?3 || '%')
        AND (?4 IS NULL OR binary_name = ?4)
        AND (?5 IS NULL OR cwd LIKE ?5 || '%')
  );

-- name: UpsertObservation :one
INSERT INTO observations (source, bundle_id, window_title, binary_name, cwd, first_seen)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (source, bundle_id, window_title, binary_name, cwd) DO UPDATE SET id = id
RETURNING id;

-- name: InsertTick :exec
INSERT OR IGNORE INTO ticks (ts, observation_id, project_id) VALUES (?, ?, ?);
```

## 6. The Polling Loop

### Per-tick algorithm

Run every `TICK_INTERVAL` (default `5s`):

1. **Idle check** — read system idle seconds via `ioreg`. If `idle >= IDLE_THRESHOLD` (default 120s), `userPresent := false`; foreground signal is skipped this tick.
2. **Foreground signal** (only if `userPresent`):
   - Read frontmost app's bundle id and pid via `osascript`.
   - If its bundle is not in the allowlist, skip.
   - Read focused window title via `osascript` (AX route).
   - Emit `Signal{Source: focus, BundleID, Title}` (no cwd on focus signals — `lsof` on the frontmost terminal *app's* pid gives the app's launch directory, not the focused shell's working directory, so it would be misleading. Per-shell cwd comes from agent signals, which run on the actual shell/agent process and read its cwd directly).
3. **Agent signals** (runs regardless of `userPresent` — that's the whole point):
   - Read all processes via `ps -A -o pid=,comm=,time=`.
   - For each process whose `comm` is in the allowlist:
     - Look up its previous CPU-ticks reading in the in-memory `prevCPU` map. Store the new reading.
     - If this is the process's first sighting (no prev reading), skip this tick — no delta yet.
     - If `delta < CPU_DELTA_THRESHOLD` (default 5 centiseconds per 5s tick = ~1% of one core), skip — process is alive but idle.
     - Read its cwd via `lsof -a -p PID -d cwd -Fn`. Skip if unreadable.
     - Emit `Signal{Source: agent, BinaryName, CWD}`.
4. **No signals**: write nothing. The tick is a true idle gap.
5. **Match each signal** to a project via the in-memory rules cache (`domain.Match`). Rules are evaluated in `priority` order, ascending; first match wins. A rule matches a signal when every non-NULL `match_*` column on the rule equals (or, for `match_title_substr`, is a substring of; for `match_cwd_prefix`, is a path-prefix of) the corresponding field on the signal. NULL `match_*` columns are "don't care." This is the runtime equivalent of the `ApplyRuleRetroactively` query in Section 5. Result is a `project_id` or `NULL` (unassigned).
6. **Write**: for each signal:
   - Upsert the observation, get its id.
   - Skip if the observation is in `ignored_observations`.
   - `INSERT OR IGNORE INTO ticks (ts, observation_id, project_id)`. The composite primary key `(ts, observation_id)` plus `OR IGNORE` makes accidental duplicate signals a silent no-op. Distinct-observation-but-same-project rows at the same `ts` are intentional — they preserve which signature triggered the credit, and the `COUNT(DISTINCT ts)` in `TotalsByProject` ensures they collapse to one credit at read time.
7. **Stale pid cleanup**: drop entries from `prevCPU` for pids no longer in this iteration's process list.

### CPU-delta calibration

`ps` reports CPU time in centiseconds. At a 5s tick:
- A fully idle process accumulates ~0 centiseconds.
- A process averaging 1% of one core accumulates ~5 centiseconds.
- Default threshold: `5` centiseconds delta → "active enough to count".
- Override via `--agent-cpu-thresh=N` daemon flag.

### Cache freshness

- `allowlist` (watched_programs) and `rules` are read once at daemon startup, cached in memory.
- The cache is invalidated and re-read on:
  - `SIGHUP` (manual reload, useful when editing DB directly).
  - Successful commit of any RPC mutation that touched these tables.
- Hot path never queries `watched_programs` or `rules` from the DB.

### Memory footprint

In-memory state of the polling loop: `prevCPU map[int]uint64` bounded by `(allowlisted binaries) × (concurrent matching processes)`. In practice <100 entries. Trivial.

## 7. macOS Bridging

The `internal/macos` package is the *only* place that shells out or could ever contain CGO. Everything else speaks plain Go types.

### Bridge interface

```go
type Bridge interface {
    Frontmost() (FrontmostInfo, error)
    FocusedWindowTitle() (string, error)
    IdleSeconds() (int, error)
    ListProcesses() ([]ProcessSample, error)
    ProcessCWD(pid int) (string, error)
}

type FrontmostInfo struct {
    BundleID string
    PID      int
    Name     string
}

type ProcessSample struct {
    PID      int
    Name     string  // executable basename ("claude")
    CPUTicks uint64  // monotonic, in centiseconds
}
```

### Implementation strategy (v1, pure Go, no CGO)

| Bridge method        | Backing command                                                                          | Approx latency |
|----------------------|------------------------------------------------------------------------------------------|----------------|
| `Frontmost`          | `osascript -e 'tell application "System Events" to get bundle identifier ...'`           | ~30 ms         |
| `FocusedWindowTitle` | `osascript -e 'tell application "System Events" ... get title of front window ...'`      | ~30 ms         |
| `IdleSeconds`        | `ioreg -c IOHIDSystem` then awk on `HIDIdleTime` (nanoseconds → seconds)                 | ~15 ms         |
| `ListProcesses`      | `ps -A -o pid=,comm=,time=`                                                              | ~20 ms         |
| `ProcessCWD`         | `lsof -a -p PID -d cwd -Fn`                                                              | ~15 ms         |

Total per-tick overhead under a normal load (1 frontmost call + 1 title call + 1 ioreg + 1 ps + a handful of lsof calls): well under 200 ms, run in a single goroutine, far inside the 5-second budget.

### Future CGO opt-in (out of scope for Phase 1)

If any single command becomes a bottleneck, its `Bridge` method can be replaced with a CGO implementation against the equivalent macOS framework (`AppKit`, `ApplicationServices`, `libproc`) without touching any other package. The interface is the seam.

## 8. RPC API

Service registered as `Antitimely` on the Unix socket. All methods take an `Args` struct and a `*Reply` pointer, returning `error` (the `net/rpc` convention).

### Method surface

```go
// internal/rpcapi/api.go

const ServiceName = "Antitimely"
const SocketPath  = "~/.antitimely/antitimely.sock"   // expanded at runtime

// --- Status ---
type StatusArgs struct{}
type StatusReply struct {
    ActiveProjects            []string             // currently being credited (this tick)
    TodayTotalsSeconds        map[string]int64     // project name → seconds today
    UnassignedTodaySeconds    int64
    UnassignedSignaturesCount int
    UserIdleSeconds           int
    TickIntervalSeconds       int
    PermissionState           string               // "ok" | "accessibility_denied" | "unknown"
}

// --- Allowlist management ---
type WatchAddArgs struct {
    Kind       string  // "bundle" or "binary"
    Identifier string
}
type WatchAddReply struct{}

type WatchRemoveArgs struct {
    Kind       string
    Identifier string
}
type WatchRemoveReply struct{}

type WatchListArgs struct{}
type WatchListReply struct {
    Items []WatchedItem
}
type WatchedItem struct {
    Kind       string
    Identifier string
}

// --- Projects ---
type ProjectAddArgs struct { Name string }
type ProjectAddReply struct { ID int64 }

type ProjectListArgs struct{}
type ProjectListReply struct { Items []Project }
type Project struct { ID int64; Name string }

type ProjectDeleteArgs struct { Name string }
type ProjectDeleteReply struct{}

// --- Review ---
type PendingReviewArgs struct { Limit int }
type PendingReviewReply struct { Signatures []Signature }
type Signature struct {
    ObservationID int64
    Source        string
    BundleID      string
    WindowTitle   string
    BinaryName    string
    CWD           string
    Ticks         int64
    LastSeenUnix  int64
}

type TagSignatureArgs struct {
    ObservationID int64
    ProjectName   string         // existing or new
    CreateProject bool           // if true, ProjectName must not exist
    Rule          *ProposedRule  // nil = tag this observation only, no rule
}
type ProposedRule struct {
    Priority         int
    MatchBundleID    string  // empty = NULL
    MatchTitleSubstr string  // empty = NULL
    MatchBinaryName  string  // empty = NULL
    MatchCWDPrefix   string  // empty = NULL
}
type TagSignatureReply struct {
    RuleCreated   bool
    RuleID        int64
    TicksRetagged int64
}

type IgnoreSignatureArgs struct { ObservationID int64 }
type IgnoreSignatureReply struct{}

// --- Rules ---
type RulesListArgs struct{}
type RulesListReply struct { Items []Rule }
type Rule struct {
    ID               int64
    ProjectName      string
    Priority         int
    MatchBundleID    string
    MatchTitleSubstr string
    MatchBinaryName  string
    MatchCWDPrefix   string
}

type RuleDeleteArgs struct { ID int64 }
type RuleDeleteReply struct{}

// --- Reporting ---
type ReportArgs struct {
    FromUnix int64   // inclusive
    ToUnix   int64   // exclusive
}
type ReportReply struct {
    Totals     map[string]int64  // project name → seconds in range
    Unassigned int64
}
```

### Method/handler pairing

```go
// internal/daemon/rpc.go
type AntitimelyService struct {
    deps *Daemon  // access to store, cache, etc.
}

func (s *AntitimelyService) Status(args rpcapi.StatusArgs, reply *rpcapi.StatusReply) error { ... }
func (s *AntitimelyService) WatchAdd(args rpcapi.WatchAddArgs, reply *rpcapi.WatchAddReply) error { ... }
// ... etc.
```

Registration:

```go
rpc.RegisterName(rpcapi.ServiceName, &AntitimelyService{deps: d})
listener, _ := net.Listen("unix", expandedSocketPath)
for {
    conn, err := listener.Accept()
    if err != nil { /* shutdown */ }
    go rpc.ServeConn(conn)
}
```

### Mutation handlers invalidate cache

Every handler that writes to `watched_programs` or `rules` must, after a successful commit, call `d.invalidateCache()` so the polling loop picks up changes within one tick.

## 9. CLI Command Surface

Dispatch is a single `switch` on `os.Args[1]` in `internal/cli/dispatch.go`. Each subcommand uses a `flag.FlagSet`. No third-party CLI framework.

```
antitimely daemon  [--interval=5s] [--idle-thresh=120s]
                   [--agent-cpu-thresh=5] [--socket=PATH] [--db=PATH]
    Start the long-running daemon. Foreground; Ctrl-C to stop.

antitimely status
    One-shot status snapshot: active projects, today's totals,
    unassigned-time count, idle state.

antitimely watch add app <bundle-id>           # e.g. com.google.antigravity
antitimely watch add binary <name>             # e.g. claude
antitimely watch list
antitimely watch remove <bundle-id-or-name>

antitimely project add <name>
antitimely project list
antitimely project delete <name>

antitimely review
    Interactive loop: enumerate unassigned signatures, prompt to tag
    or ignore, propose generalized rules.

antitimely rules list
antitimely rules delete <id>

antitimely report  [--from=YYYY-MM-DD] [--to=YYYY-MM-DD]
    Default range: today. Outputs per-project totals + unassigned.
```

### Exit codes

- `0` — success.
- `1` — RPC call returned an error (printed to stderr).
- `2` — daemon not reachable (socket missing or connection refused). Stderr hint: `Is 'antitimely daemon' running?`
- `64` — usage error (bad flags / unknown subcommand).

## 10. Review Flow UX

The interactive `antitimely review` loop runs in the CLI process and chats with the daemon via RPC. Pseudocode:

```
Loop {
    reply = rpc.PendingReview(Limit: 10)
    if len(reply.Signatures) == 0 {
        print("Nothing to review.")
        return
    }
    Print numbered list:
        [1] 187 ticks (15m 35s)  binary=claude, cwd=/Users/rian/work/foca-api
        [2] 156 ticks (13m 00s)  bundle=com.google.antigravity, title="antitimely — main — Antigravity"
        ...
    Prompt: "Select [1-N, q to quit]: "
    if 'q': return
    sig = reply.Signatures[choice]

    Prompt: "Tag as project — pick:"
        existing projects (numbered)
        n) new project
        i) ignore forever
        s) skip
    if 'i': rpc.IgnoreSignature(sig.ObservationID); continue
    if 's': continue
    project = ... (existing or newly-created via ProjectAdd RPC)

    proposed = generalize(sig, project)   // pure function in domain.Generalize
    Print proposed rule
    Prompt: "[y] accept  [e] edit  [n] this row only, no rule"
    rule := ...
    rpc.TagSignature(sig.ObservationID, project.Name, rule)
    Print "Retroactively tagged N ticks."
}
```

### Rule generalization logic (`domain.Generalize`)

A pure function `func Generalize(o Observation, projectName string) ProposedRule`:

- **For `source = agent`** (we have cwd + binary_name):
  - `MatchBinaryName = o.BinaryName`.
  - `MatchCWDPrefix`: find the longest path prefix of `o.CWD` that contains `projectName` as a literal substring. Walk from leaf to root:
    - `/Users/rian/work/foca-api/src/handlers` with project `foca-api` → propose `/Users/rian/work/foca-api/`.
    - If `projectName` never appears in the cwd, propose the immediate parent directory of `o.CWD` (one level up) and emit an `Notice: project name not found in cwd; defaulting to parent dir.` in the CLI.
- **For `source = focus`** (we have bundle + title, possibly cwd):
  - `MatchBundleID = o.BundleID`.
  - `MatchTitleSubstr`: if `projectName` appears as a substring of `o.WindowTitle` (case-insensitive), use it. Otherwise propose the most distinctive token of the title (the part of the title that isn't the app name, trimmed) and emit a notice.

This logic is unit-tested table-style with no external dependencies.

## 11. Daemon Lifecycle

### Startup sequence (`antitimely daemon`)

1. Parse flags.
2. Expand paths (`~/.antitimely/db.sqlite`, `~/.antitimely/antitimely.sock`); `mkdir -p` the parent.
3. Open SQLite with `_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=2000`.
4. Run schema migration: read `schema.sql` (embedded via `//go:embed`); execute its statements inside a transaction. Use `CREATE TABLE IF NOT EXISTS` so re-running is idempotent.
5. Construct `internal/macos.Bridge` (concrete osascript-backed impl).
6. Construct `Daemon` struct with: bridge, store (sqlc Queries), in-memory caches.
7. Load allowlist + rules into caches.
8. Acquire socket file: if `~/.antitimely/antitimely.sock` exists, attempt to connect to it; if a daemon answers, exit with error. Otherwise unlink the stale socket and proceed.
9. Write PID file (`~/.antitimely/antitimely.pid`).
10. Start polling goroutine (`go d.poll(ctx)`).
11. Start RPC listener goroutine (`go d.serveRPC(ctx, listener)`).
12. Block on signal channel for `SIGINT` / `SIGTERM` / `SIGHUP`.
13. On `SIGHUP`: re-read caches, continue.
14. On `SIGINT` / `SIGTERM`: `cancel(ctx)`, wait for goroutines via `sync.WaitGroup`, close DB, remove PID + socket files, exit 0.

### Shutdown is best-effort

If the process is killed (`SIGKILL`), the next start will:
- Find a stale socket and stale PID file.
- Check whether the PID is still alive (`syscall.Kill(pid, 0)`); if not, remove both and continue.

## 12. Permissions Handling

The first time the daemon calls `osascript` to query System Events, macOS shows the *Automation* permission dialog. We handle both grant and denial gracefully.

### Behavior on denial

- `osascript` returns exit code `1` with stderr containing `not authorized`.
- The macos bridge wraps this in a typed error: `ErrAccessibilityDenied`.
- The polling loop, on receiving this error from `FocusedWindowTitle` or `Frontmost`:
  - Logs once (rate-limited) to stderr:
    ```
    Accessibility permission denied. Window-title and frontmost-app capture disabled.
    To enable: System Settings → Privacy & Security → Automation → antitimely → System Events.
    Then send SIGHUP or restart the daemon.
    ```
  - Skips the focus signal; agent signals continue working (they don't need permissions).
- `antitimely status` includes a `PermissionState` field in `StatusReply` so the CLI can surface this prominently.

### Bundle-only fallback

When window title is unavailable but the bundle id is, the focus signal still records `Source: focus, BundleID: ..., Title: ""`. Rules with no `MatchTitleSubstr` can still match. Practically: useful for bundles that map 1:1 to a project (e.g. a project-specific app), less useful for multi-window IDEs.

## 13. Concurrency Model

- **Main goroutine**: process lifecycle (flag parsing, signal waits, shutdown).
- **Polling goroutine** (1): `time.NewTicker(d.interval)`. Each tick calls `d.runTick(ctx, now)`. Stops on `ctx.Done()`.
- **RPC accept goroutine** (1): `for { conn := listener.Accept(); go rpc.ServeConn(conn) }`.
- **RPC connection goroutines** (N, one per connected client): spawned by `net/rpc` automatically; live for the duration of one connection.

### Database concurrency

- SQLite in WAL mode permits unlimited concurrent readers + one writer.
- Writer: only the polling goroutine. Its writes are sub-millisecond.
- Readers: RPC handlers (status, list, report).
- Mutating RPC handlers (watch add, project add, tag signature) hold the writer briefly — measured in microseconds.
- The `_busy_timeout=2000` setting handles the rare collision (writer-vs-writer when an RPC mutation races the polling loop) by retrying for up to 2s.

### Cache invalidation

`Daemon` keeps an `atomic.Pointer[Caches]` to the immutable `Caches` struct (allowlist + rules). On mutation:

1. Mutation RPC handler writes to DB.
2. On success, it loads fresh data and `atomic.Store`s a new `Caches`.
3. The polling loop reads `atomic.Load` at the start of every tick.

No mutex needed; the pointer swap is atomic and ticks always see a consistent snapshot.

## 14. Error Handling Principles

- **Bridge errors are recoverable.** Any `macos.Bridge` method may return an error (system call failed, command timed out). The polling loop logs and skips that signal for the current tick; the next tick retries. The daemon never crashes on a transient bridge error.
- **DB errors are fatal-on-write, log-on-read.** A failed write means we'd lose tick data — better to crash and let the user notice than to silently drop. A failed read in an RPC handler returns the error to the client; the client prints and exits with code 1.
- **No silent fallbacks.** When something unexpected happens (bridge error, schema mismatch, parse failure), it surfaces — either via stderr log or RPC error. No try/except blanket pass-throughs.
- **No retry loops in the hot path.** If a per-tick step fails, this tick is incomplete and we move on. There is *no* per-step retry; the next tick is the retry.

## 15. Testing Strategy

### Unit tests

- **`internal/domain`**: heavy table-driven tests. Every rule-matcher branch, every generalization input/output. Target: 100% line coverage of this package.
- **`internal/store`**: integration tests using `:memory:` SQLite. Verify schema migrations, indexes, and key queries (totals, retroactive update).
- **`internal/daemon/pipeline`**: inject a fake `Bridge` returning canned process lists, frontmost values, idle seconds. Run one tick. Assert correct rows written.

### Integration tests

- **End-to-end black box**: spin up the daemon in a temp `HOME`, RPC-call it from a test, verify status output and table state. Uses real net/rpc over a temp unix socket. No real macOS calls — bridge is a fake.

### What we will not test (out of scope)

- The actual `osascript` / `lsof` / `ps` / `ioreg` integration. These are thin wrappers around shell commands. Manual smoke-test on the developer's machine is sufficient for v1.
- Performance under load. v1 has trivial load (5s tick, <100 PIDs).

### Test layout

```
internal/domain/match_test.go
internal/domain/generalize_test.go
internal/store/store_test.go             # uses sqlite3 :memory:
internal/daemon/pipeline_test.go         # uses fake Bridge
test/integration/end_to_end_test.go      # full daemon, fake Bridge
```

## 16. Open Questions

These were considered and resolved during brainstorming. Listed here so the rationale is preserved.

| Question | Decision | Rationale |
|---|---|---|
| Tick-based vs session-based storage? | Tick-based. | Parallelism, dedup, and "agent ran while I was AFK" all fall out trivially. Session intervals don't compose with multi-project parallelism. |
| HTTP-over-Unix-socket vs `net/rpc`? | `net/rpc` with gob. | User requested stdlib-only and "low-level." Phase 2 web layer is a separate listener, not the same socket. |
| Tick interval? | 5s default, daemon flag. | Balance of accuracy and per-tick overhead. 1s gives no real benefit; 60s loses precision for short context switches. |
| CGO or shell-outs for macOS? | Shell-outs (osascript/ps/lsof/ioreg). | Keeps build pure-Go. Latency is well within budget. Bridge interface allows per-method CGO upgrade later. |
| Window vs tab introspection in terminals? | Window-level only. | Tab-level requires per-terminal AppleScript dialects; out of scope for v1. Users wanting tab-level can rely on per-process cwd of allowlisted agents. |
| Manual start/stop commands? | None in v1. | Auto-detect from allowlist + rules covers the design. Can add `session_intents` table later if needed. |
| Rule generalization: auto or confirm? | Confirm with smart defaults. | Mismatched rules silently pollute weeks of data; one keystroke prevents that. The default proposal includes the project-name discriminator, so confirmation is usually `y`. |

## 17. Future Work (Phase 2 hints)

Phase 1 has been shaped so that the following all become incremental, non-refactoring additions:

- **`clients` table + project columns** (hourly_rate, currency, tax_rate): pure `ALTER TABLE` + new RPC methods.
- **`antitimely web`** subcommand: a second listener on the daemon, `net/http` on `127.0.0.1:PORT`, serving an invoice UI. Reads SQLite directly for historical totals; calls back into the local `AntitimelyService` (or just shares the same store) for live state.
- **Invoice generation**: a new table `invoices` referencing `clients` + a snapshot of tick aggregates for a date range. PDF rendering via stdlib HTML templates → headless Chrome or a third-party PDF lib (Phase 2 dependency decision).
- **Manual time entries**: a `manual_entries(ts_start, ts_end, project_id, note)` table whose rows are merged into reports alongside `ticks`. Lets the user bill phone calls or away-from-keyboard work.
- **launchd auto-start**: ship a `~/Library/LaunchAgents/com.rian.antitimely.plist`. Subcommand `antitimely install-launch-agent` writes and loads it.

None of these requires changes to `domain`, `store`, the polling loop, or the existing RPC methods.
