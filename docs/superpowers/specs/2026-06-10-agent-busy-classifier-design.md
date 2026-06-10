# Agent "busy" classifier — sustained-CPU work detection

**Date:** 2026-06-10
**Status:** Approved, pending implementation plan

## Problem

The daemon auto-logs billable time per project from two signals: a **focus**
signal (frontmost allowlisted app) and an **agent** signal (a tracked process
burning CPU in a tracked directory). The agent path classifies a process as
"working" when its CPU time grows past a flat bar in a *single* 5s poll:
`agent_cpu_threshold = 5` centiseconds while the user is present (≈1% of one
core), `agent_cpu_threshold_idle = 100` while idle.

That 1% bar is a *liveness* check, not a *work* check. It cannot distinguish an
agent actively doing work from a process that is merely alive. The observed
failures, in the user's words:

1. **Idle agent at prompt** — a `claude`/`node` session waiting for input still
   trickles past 1% and keeps its project counting.
2. **Idle watch-mode build server** — a dev server / language server / watcher
   in a tracked dir crosses 1% and bills time the user isn't working.
3. **"Forgot to kill it before sleep"** — a process left running keeps the
   project accruing because presence is global (machine idle < 120s), not
   per-project, and the bar is trivially cleared.

The unifying truth: **CPU activity ≠ the user working on that project.** The fix
must classify a process by whether it is *genuinely doing work*, not whether it
is alive.

## Empirical calibration

Measured CPU delta over one 5s poll on the user's live sessions (centiseconds;
500 cs = one core fully busy for 5s):

| Session | Δ over 5s | ≈ % of one core | State |
|---|---|---|---|
| dentix (`claude`, idle at prompt) | 2 cs | ~0.4% | idle |
| daas (`claude`, lightly active) | 34 cs | ~6.8% | active |
| antitimely (`claude`, actively replying) | 70 cs | ~14% | active |

Key finding: **LLM work is network-bound, not CPU-bound.** A genuinely active
agent is only ~30–70 cs; it spikes to hundreds of cs *only* when running a tool
(build/test/grep). So a high bar (e.g. 150 cs) would count only tool-execution
bursts and mark all thinking/streaming as idle — a large undercount. The real
idle-vs-active gap lives at **~2 cs vs ~30–70 cs**, so the bar belongs near
**15 cs**.

## Decisions (from brainstorming)

- **Approach:** sustained-CPU "busy" classifier (per-process state machine with
  hysteresis). No focus-recency gating; no agent heartbeat for now.
- **Unattended agent runs count indefinitely while the user is present** — as
  long as the process is genuinely busy. When the user *leaves* (machine idle),
  the high idle bar takes over so forgotten low-CPU processes stop counting.
- **IDE editing is out of scope of this change** — it counts via the untouched
  **focus** path whenever the IDE is the frontmost allowlisted, ruled app. The
  user confirmed they always edit with the IDE focused.
- **Claude heartbeat** (a hook-written turn-active marker) is explicitly
  **deferred** to a future iteration as the eventual cure for the low-CPU
  thinking blind spot.

## Design

### Core mechanism — per-PID activity state machine

Replace the single-tick threshold test in `collectAgentSignals`
(`internal/daemon/pipeline.go`) with a small state machine kept per tracked PID
in the `Pipeline`, alongside `prevCPU` and `procClass`:

```
activityState { working bool; aboveStreak int; belowStreak int }
```

Each tick, after computing the CPU delta (unchanged):

- `delta >= busyBar` → `aboveStreak++`, `belowStreak = 0`.
  If `!working && aboveStreak >= riseTicks` → `working = true`.
- `delta < busyBar` → `belowStreak++`, `aboveStreak = 0`.
  If `working && belowStreak >= fallTicks` → `working = false`.

**A PID emits an agent signal only while `working`.** Idle prompts (~2 cs),
idle watchers (sporadic), and forgotten low-CPU daemons never reach `working`;
genuine sustained work does.

### Two CPU regimes

`busyBar` switches on machine-idle, as today:

- **Present** (`agent_cpu_threshold`): `5 → 15` cs. Low enough that thinking /
  streaming counts, high enough to clear idle's ~2 cs.
- **Idle** (`agent_cpu_threshold_idle`): `100` cs (unchanged). When the user has
  stepped away, only heavy compute counts — idle watch-servers and idle agents
  sit well under 100, killing the "forgot it before sleep" over-bill.

This split is the resolution of the apparent tension between "count agent runs
indefinitely while busy" and "don't bill what I forgot before sleep": the
discriminator is **whether the machine is idle**, not focus.

### New config keys

```yaml
agent_busy_rise_ticks: 2   # consecutive busy ticks to start counting (~10s)
agent_busy_fall_ticks: 3   # consecutive quiet ticks to stop  (~15s hysteresis)
```

`agent_cpu_threshold` and `agent_cpu_threshold_idle` keep their names (no config
breakage); only the present default and their meaning (now the state-machine
busy bar) change. `PipelineConfig` gains `AgentBusyRiseTicks` /
`AgentBusyFallTicks`; `Config` and `DefaultConfig()` and `config_file.go`
plumbing follow.

### State lifecycle / correctness

- Activity state is pruned for dead PIDs in the same deferred sweep that prunes
  `prevCPU` / `procClass` in `collectAgentSignals`.
- PID reuse (binary-name change) and CPU regression already drop `procClass`;
  reset activity state at the same points so a recycled PID starts `idle`.
- A process not yet seen twice (no prior CPU sample) cannot have a delta, so it
  stays `idle` — unchanged from today's "skip on first sight."

### What does not change

- **Focus path** (`collectFocusSignal`) — untouched. IDE editing while focused
  keeps counting every tick regardless of CPU.
- **Arming gate** and **auto-disarm** (`RunTick`) — unchanged logic; they simply
  receive cleaner agent signals. The "sustained matching activity" auto-disarm
  now means the process is genuinely `working`.
- **Paused-project auto-resume** — unchanged code path, but because it only sees
  signals from `working` processes, an idle watcher can no longer resurrect a
  paused project (tightens the 2026-06-05 paused-accrual fix for free).

## Error handling

- `ps` failure → return `nil` (existing fail-safe); no state transitions occur
  that tick.
- Idle-read failure → fail open to "present" (existing behavior), so the low
  present bar applies — consistent with current "keep tracking, surface the
  failure" philosophy.

## Testing

Table-driven tests against the fake `macos.Bridge` driving synthetic CPU
sequences:

1. Idle process (~2 cs/tick) never reaches `working`.
2. Sustained-high process becomes `working` after exactly `riseTicks`.
3. A single above-bar blip (1 tick) never flips `working`.
4. A mid-work dip below bar shorter than `fallTicks` does not drop `working`
   (hysteresis).
5. Present→idle regime change drops a moderate (15–100 cs) process to `idle`.
6. PID reuse (binary-name change) and CPU regression reset state to `idle`.
7. Paused-project: an idle watcher no longer auto-resumes; a genuinely working
   process still does.

## Precondition to verify during implementation

Confirm the user's IDE bundle is in `AllowedBundles` and has a rule mapping its
window to the project, so the "IDE editing counts via focus" guarantee actually
holds for their config.

## Out of scope / deferred

- Claude (and other agent) **heartbeat** signal — the precise cure for low-CPU
  thinking; revisit if the background-daemon residual or undercount bites.
- Per-project focus-recency gating — explicitly rejected in favor of the
  busy/idle CPU classifier.
- Descendant/process-tree activity detection.
