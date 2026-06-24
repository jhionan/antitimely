# Transcript-activity signal — capturing remote & planning work

**Date:** 2026-06-24
**Status:** Design (awaiting review)
**Related:** `2026-06-10-agent-busy-classifier-design.md`, `2026-05-27-focus-arm-gate-design.md`

## Problem

Real, billable work is silently recording **zero ticks**. Confirmed 2026-06-24: an
after-midnight Daas planning session (01:55–02:30) produced no ticks, no commits,
and no file changes — it had to be reconstructed by hand from the Claude Code
transcript and back-filled with direct SQLite inserts.

### Root cause

The daemon infers "working" from two proxies, and both are blind to agent-driven work:

1. **Focus** (foreground window) — requires a human at this Mac. When you drive
   Claude Code **remotely**, there is no local foreground window, no SSH/login
   session, and HID idle climbs, so the idle gate suppresses focus entirely.
2. **CPU** (sustained local CPU in a tracked cwd) — **structurally wrong for
   Claude Code**, which is a *thin client*. The model runs on Anthropic's servers;
   while a turn generates/streams, the local `claude` process is blocked on a
   network read at **~0% CPU**. Local CPU only blips briefly during *tool* runs
   (bash/edit/build). An agent working hard for minutes shows no sustained CPU.

So planning sessions (agent idle, no commits, no focus) are invisible, and even
remote *coding* is only caught during brief tool blips. Loosening the idle/CPU
gate barely helps — **CPU is the wrong signal.**

### The signal that does track the work

Every Claude Code turn is appended to a session transcript on local disk:
`~/.claude/projects/<encoded-cwd>/<session-id>.jsonl`, one JSON object per entry,
each carrying a UTC `timestamp`. The file is written **as the turn streams**,
regardless of where compute runs or whether you drive locally or remotely. The
transcript is therefore a *direct* observation of work, not a proxy.

## Goal

Count active Claude Code work against the correct project **whenever a transcript
for a tracked cwd is being written**, independent of CPU, focus, or HID idle —
while preserving the existing anti-overnight-accrual guard (paused / End-day
projects never tick).

Non-goals: tracking non-Claude-Code tools; perfectly attributing concurrent
sessions; replacing focus/CPU (this is an additional source, not a replacement).

## Design

### A third signal source

Add `collectTranscriptSignals` alongside `collectFocusSignal` and
`collectAgentSignals` in `internal/daemon/pipeline.go`. It produces
`domain.Signal{ Source: SourceTranscript, Cwd: <session cwd>, ... }` values that
flow through the **existing** `MatchRules` (cwd-prefix) → tick path. No new
matching logic: a transcript signal's `Cwd` is matched by the same cwd-prefix
rules that already map a project's directories, reusing `cwdUnderAnyPrefix`
semantics.

### Per-tick algorithm (runs every interval, ~5s)

1. **Discover sessions.** Enumerate `~/.claude/projects/*/` dirs (root
   configurable; honor `CLAUDE_CONFIG_DIR`). The dir name is the cwd with `/`
   encoded as `-` (e.g. `-Users-rian-focaApp-bclouder-daas-daas-back-end`).
2. **Cheap change check.** Skip any session dir whose newest `*.jsonl` mtime is
   older than the grace window — no need to open it. (Avoids reading every
   transcript every tick.)
3. **Resolve cwd → project.** Decode the dir name to an absolute path. Determine
   the session cwd: prefer the `cwd` field recorded inside the transcript
   entries (authoritative, handles edge cases in the encoding); fall back to the
   decoded dir name. If the cwd is under no project's `CwdPrefixes`, ignore the
   session.
4. **Last-activity per session.** Track the newest entry `timestamp` seen per
   session file, using a stored byte offset / mtime so only new tail bytes are
   read each tick (never re-parse the whole file).
5. **Emit while active.** If `now − lastActivity < grace`, emit a transcript
   signal for that session's cwd. This makes the grace window fill the
   "thinking/reading" gaps between turns as one continuous billable session —
   exactly how the manual back-fills treated them.

### Signal semantics (how a transcript signal differs from agent CPU)

A transcript turn is **unambiguous active work**, stronger than agent CPU. So:

- **Bypass the arming gate.** Armed projects suppress *CPU* agent ticks (because
  background CPU is ambiguous). A transcript turn is not ambiguous → it counts
  immediately and **disarms** the project (same effect as a focus signal).
- **Do NOT auto-resume paused projects.** End-day / pause stays authoritative:
  the user explicitly said "stop counting." A transcript signal ticks an
  *active* project; for a *paused* one it is upserted as an observation (so it
  survives in review) but does not resurrect it. This is the key guard against
  an autonomous overnight agent loop re-billing a paused project.
- **Dedup within a project per tick.** If focus or CPU already produced a tick
  for this project this cycle, do not also emit a transcript tick — prevents
  per-project double counting. (Cross-project concurrency is unchanged and still
  reported via distinct-`ts` in the audit.)

### Schema

`observations.source` CHECK is currently `IN ('focus','agent')`. Add
`'transcript'` → `CHECK (source IN ('focus','agent','transcript'))`. Migration
rebuilds the table (SQLite can't alter a CHECK in place) preserving rows and the
UNIQUE constraint. A transcript observation row uses
`source='transcript'`, `cwd=<session cwd>`, and `window_title=<session-id>` (so
distinct sessions in the same cwd remain distinguishable and auditable, the way
the `manual entry` tag made back-fills identifiable).

### Config (`config_file.go`)

```yaml
transcript_tracking: true        # master enable (default true)
transcript_grace: 10m            # session considered active this long after last turn
transcript_root: ~/.claude/projects   # override; default per CLAUDE_CONFIG_DIR / ~/.claude
```

Defaults: enabled, 10-minute grace. Grace is the one tunable that trades
"counts reading/thinking gaps" against "keeps ticking after you walk away";
10m bounds the post-session over-count to ≤ one grace window.

### Status & audit

- `atl status` already groups by project; transcript ticks flow in naturally.
  Optionally annotate a project actively driven by a transcript session
  (`(live: claude-code)`), mirroring the `(armed: needs focus)` annotation.
- Because transcript ticks carry `source='transcript'`, the audit queries can
  attribute "how much of today came from remote/agent sessions vs focus vs CPU".

## Phase 2 (optional, separate PR): back-fill importer

A one-shot `atl import-transcripts [--from D --to D]` that replays historical
transcripts into ticks for any uncovered interval — automating exactly the manual
recovery done on 2026-06-24. Same cwd→project mapping and grace-window
session-stitching; skips intervals already ticked. Lets us recover past lost
windows without hand-written SQL. Kept out of the core daemon change to keep that
PR focused.

## Testing

- **cwd decoding / mapping:** encoded dir → path → project via `CwdPrefixes`,
  including a session whose cwd maps to no project (ignored) and a subdir under a
  prefix (matched).
- **grace stitching:** turns at t, t+3m, t+8m with grace=10m → continuous ticks
  across the gaps; a 20m gap → two separate sessions.
- **paused guard:** transcript activity for a paused project → observation
  upserted, no tick, project stays paused.
- **arming bypass:** transcript activity for an armed project → ticks immediately
  and disarms.
- **dedup:** focus+transcript (or CPU+transcript) in the same tick for one
  project → a single project-tick.
- **tail-read efficiency:** appending to a large transcript only reads new bytes
  (assert no full re-parse) — offset/mtime bookkeeping.
- **migration:** existing focus/agent rows survive the CHECK rebuild; UNIQUE
  dedup still holds.
- **daemon integration:** temp transcript dir with seeded `.jsonl` + a project
  whose `CwdPrefixes` covers it → `RunTick` emits transcript ticks while HID idle
  is high and CPU is zero (the exact failure case from this spec).

## Risks / open questions

- **Walk-away over-count:** bounded by grace (≤10m after last turn). Acceptable;
  End-day pause stops it entirely.
- **Autonomous overnight agent loops** (an agent that keeps taking turns
  unattended) *would* be counted on an active project — that is real compute, but
  may not be billable. Mitigation: End-day pause; optionally a future "max
  unattended transcript run" cap. Out of scope for v1.
- **Concurrent sessions, same cwd:** each session file is tracked independently;
  per-project per-tick dedup prevents double counting.
- **Transcript format drift:** depends on the `timestamp` + `cwd` fields in
  `.jsonl`. Parsing is defensive (skip unparseable lines); if the newest entry
  has no timestamp, fall back to file mtime.
