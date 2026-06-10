# Agent Busy Classifier Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the single-tick CPU threshold in the agent-signal path with a per-PID sustained-CPU "busy" state machine (hysteresis), so idle agents/watchers stop billing while genuinely-working processes still count.

**Architecture:** A per-PID `activityState{working, aboveStreak, belowStreak}` map lives in `Pipeline` alongside `prevCPU`/`procClass`. Each tick, the CPU delta is compared to a regime-dependent busy bar (15cs present / 100cs idle); a PID must stay above for `riseTicks` to become `working` and below for `fallTicks` to stop. Agent signals are emitted only while `working`. The focus path, arming gate, and paused-resume logic are unchanged — they simply receive fewer, cleaner agent signals.

**Tech Stack:** Go, `modernc.org/sqlite`, table-driven tests against `macos.FakeBridge`.

**Spec:** `docs/superpowers/specs/2026-06-10-agent-busy-classifier-design.md`

---

## File Structure

- **Modify** `internal/daemon/pipeline.go` — add `activityState` type + `procActivity` map field; add `riseTicks`/`fallTicks` to `PipelineConfig`; replace the single-tick threshold test in `collectAgentSignals` with the state machine; reset activity state at the existing PID-reuse / CPU-regression / cache-swap points.
- **Modify** `internal/daemon/daemon.go` — add `AgentBusyRiseTicks`/`AgentBusyFallTicks` to `Config`; set defaults in `DefaultConfig()` (rise 2, fall 3) and bump `AgentCPUThresh` 5→15; wire the two new fields into `PipelineConfig` in `Run`.
- **Modify** `internal/daemon/config_file.go` — add `agent_busy_rise_ticks` / `agent_busy_fall_ticks` YAML keys + parsing; update the sample-config comment for `agent_cpu_threshold` (now 15, "busy bar").
- **Modify** `internal/daemon/pipeline_test.go` — update one existing test whose assumptions change (single-tick emit), and add the state-machine tests.

---

## Task 1: Add the activity state machine to the pipeline (counting changes)

**Files:**
- Modify: `internal/daemon/pipeline.go` (PipelineConfig ~15-26; Pipeline struct ~38-60; NewPipeline ~66-73; collectAgentSignals ~297-385)
- Test: `internal/daemon/pipeline_test.go`

This is the core. We work test-first against the fake bridge.

- [ ] **Step 1: Add the new config fields and state type (no behavior change yet)**

In `internal/daemon/pipeline.go`, add two fields to `PipelineConfig` (after `AutoDisarmAgentTicks`):

```go
	// AgentBusyRiseTicks is how many consecutive ticks a tracked process's CPU
	// delta must stay at/above the busy bar before it is considered "working"
	// and starts emitting agent signals. 0 or 1 ⇒ a single above-bar tick
	// counts (legacy behavior). Default 2.
	AgentBusyRiseTicks int
	// AgentBusyFallTicks is how many consecutive ticks below the busy bar a
	// "working" process must accumulate before it stops emitting (hysteresis,
	// so brief dips between streamed tokens / build phases don't flicker it
	// off). 0 or 1 ⇒ a single below-bar tick stops it. Default 3.
	AgentBusyFallTicks int
```

Add the `activityState` type near `procClass` (after the `procClass` struct, ~line 36):

```go
// activityState is the per-PID hysteresis state for the busy classifier. A
// tracked process emits agent signals only while working==true. aboveStreak /
// belowStreak count consecutive ticks the CPU delta has been at/above or below
// the regime's busy bar; whichever side accumulates enough flips working.
type activityState struct {
	working     bool
	aboveStreak int
	belowStreak int
}
```

Add the map field to `Pipeline` (after `procClass map[int]procClass`, ~line 48):

```go
	// procActivity is the per-PID busy/idle hysteresis state. Pruned for dead
	// PIDs in the same deferred sweep as prevCPU/procClass, and reset (deleted)
	// on PID reuse / CPU regression / cache-snapshot swap alongside procClass.
	procActivity map[int]activityState
```

Initialize it in `NewPipeline` (alongside the other maps):

```go
		procActivity:     map[int]activityState{},
```

- [ ] **Step 2: Run build + existing tests to confirm no behavior change yet**

Run: `go build ./... && go test ./internal/daemon/...`
Expected: PASS (fields added but unused; `activityState`/`procActivity` referenced only by initialization — Go will complain `procActivity` is unused only if never read; it is written in NewPipeline so it compiles. If the unused-write triggers nothing, build passes.)

- [ ] **Step 3: Write the failing hysteresis tests**

Add to `internal/daemon/pipeline_test.go`. These use the existing `newTestPipelineWithCfg` helper. Note `CPUDeltaThresh`/`CPUDeltaThreshIdle` are the busy bars.

```go
func TestPipeline_Busy_RiseRequiresSustainedTicks(t *testing.T) {
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     15,
		CPUDeltaThreshIdle: 100,
		AgentBusyRiseTicks: 2,
		AgentBusyFallTicks: 3,
	})
	defer db.Close()
	ctx := context.Background()
	cache.Store(&CacheSnapshot{AllowedBinaries: map[string]bool{"claude": true}})
	br.IdleSecondsVal = 5 // present → busy bar 15
	br.CWDByPID = map[int]string{999: "/work/x"}

	// Tick 1: establish prev (no delta yet).
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 0}}
	_ = p.RunTick(ctx, 1000)
	// Tick 2: delta 30 ≥ 15 → aboveStreak=1, not yet working (riseTicks=2).
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 30}}
	_ = p.RunTick(ctx, 1005)
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 0 {
		t.Fatalf("after 1 above-bar tick, expected 0 emits (rise not met), got %d", n)
	}
	// Tick 3: delta 30 ≥ 15 → aboveStreak=2 → working → emit.
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 60}}
	_ = p.RunTick(ctx, 1010)
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 1 {
		t.Fatalf("after 2 sustained above-bar ticks, expected 1 emit, got %d", n)
	}
}

func TestPipeline_Busy_SingleBlipNeverFlips(t *testing.T) {
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     15,
		CPUDeltaThreshIdle: 100,
		AgentBusyRiseTicks: 2,
		AgentBusyFallTicks: 3,
	})
	defer db.Close()
	ctx := context.Background()
	cache.Store(&CacheSnapshot{AllowedBinaries: map[string]bool{"claude": true}})
	br.IdleSecondsVal = 5
	br.CWDByPID = map[int]string{999: "/work/x"}

	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 0}}
	_ = p.RunTick(ctx, 1000) // prev
	// One blip above, then quiet.
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 40}}
	_ = p.RunTick(ctx, 1005) // aboveStreak=1
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 42}}
	_ = p.RunTick(ctx, 1010) // delta 2 < 15 → belowStreak resets above
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 44}}
	_ = p.RunTick(ctx, 1015) // delta 2 < 15

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 0 {
		t.Errorf("a single above-bar blip must never reach working; got %d emits", n)
	}
}

func TestPipeline_Busy_FallHysteresisBridgesDips(t *testing.T) {
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     15,
		CPUDeltaThreshIdle: 100,
		AgentBusyRiseTicks: 2,
		AgentBusyFallTicks: 3,
	})
	defer db.Close()
	ctx := context.Background()
	cache.Store(&CacheSnapshot{AllowedBinaries: map[string]bool{"claude": true}})
	br.IdleSecondsVal = 5
	br.CWDByPID = map[int]string{999: "/work/x"}

	// Drive to working: prev + 2 sustained above-bar ticks.
	seq := []uint64{0, 30, 60} // deltas: -, 30, 30
	ts := int64(1000)
	for _, c := range seq {
		br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: c}}
		_ = p.RunTick(ctx, ts)
		ts += 5
	}
	var afterRise int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&afterRise)
	if afterRise != 1 {
		t.Fatalf("expected working+1 emit after rise, got %d", afterRise)
	}

	// One dip below bar (delta 2) then back above (delta 30). fallTicks=3, so a
	// single dip must NOT drop working — the dip tick itself still emits.
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 62}}
	_ = p.RunTick(ctx, ts) // delta 2 < 15 → belowStreak=1, still working → emit
	ts += 5
	br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: 92}}
	_ = p.RunTick(ctx, ts) // delta 30 → above resets belowStreak → emit
	ts += 5

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	if n != 3 {
		t.Errorf("expected 3 emits (rise tick + dip tick + recovery tick), got %d", n)
	}
}

func TestPipeline_Busy_FallStopsAfterSustainedQuiet(t *testing.T) {
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     15,
		CPUDeltaThreshIdle: 100,
		AgentBusyRiseTicks: 2,
		AgentBusyFallTicks: 3,
	})
	defer db.Close()
	ctx := context.Background()
	cache.Store(&CacheSnapshot{AllowedBinaries: map[string]bool{"claude": true}})
	br.IdleSecondsVal = 5
	br.CWDByPID = map[int]string{999: "/work/x"}

	// Rise to working: prev + 2 sustained above-bar ticks.
	ts := int64(1000)
	for _, c := range []uint64{0, 30, 60} {
		br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: c}}
		_ = p.RunTick(ctx, ts)
		ts += 5
	}
	// Now quiet ticks (delta 1 each, below bar). With fall=3: belowStreak 1,2
	// keep working (emit), belowStreak 3 flips working off in the same tick
	// BEFORE the emit gate (no emit), and subsequent quiet ticks don't emit.
	base := uint64(60)
	for i := 0; i < 5; i++ {
		base += 1 // delta 1 < 15
		br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: base}}
		_ = p.RunTick(ctx, ts)
		ts += 5
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&n)
	// Rise emitted 1. Quiet ticks: belowStreak 1 (emit), 2 (emit), 3→stop (no
	// emit), then no emits. Total = 1 + 2 = 3.
	if n != 3 {
		t.Errorf("expected 3 total emits (1 rise + 2 quiet-while-working), got %d", n)
	}
}
```

```go
func TestPipeline_Busy_PresentToIdleRegimeDropsModerate(t *testing.T) {
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     15,  // present bar
		CPUDeltaThreshIdle: 100, // idle bar
		AgentBusyRiseTicks: 2,
		AgentBusyFallTicks: 3,
	})
	defer db.Close()
	ctx := context.Background()
	cache.Store(&CacheSnapshot{AllowedBinaries: map[string]bool{"claude": true}})
	br.CWDByPID = map[int]string{999: "/work/x"}

	// Present: a moderate ~50cs/poll process rises to working (above 15).
	br.IdleSecondsVal = 5
	c := uint64(0)
	ts := int64(1000)
	for i := 0; i < 3; i++ { // prev + 2 above-bar ⇒ working, last tick emits
		br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: c}}
		_ = p.RunTick(ctx, ts)
		c += 50
		ts += 5
	}
	var afterRise int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&afterRise)
	if afterRise != 1 {
		t.Fatalf("present moderate process should rise to working (1 emit), got %d", afterRise)
	}

	// User leaves: bar jumps to 100. The same ~50cs/poll is now below bar →
	// belowStreak accrues; ticks 1,2 still emit (working), tick 3 flips off.
	br.IdleSecondsVal = 200
	for i := 0; i < 4; i++ {
		br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: c}}
		_ = p.RunTick(ctx, ts)
		c += 50
		ts += 5
	}
	var total int
	db.QueryRow(`SELECT COUNT(*) FROM ticks`).Scan(&total)
	// 1 (rise) + 2 (belowStreak 1,2 while still working) = 3; tick 3 drops it.
	if total != 3 {
		t.Errorf("moderate process should stop counting after going idle; got %d emits, want 3", total)
	}
}
```

- [ ] **Step 4: Run the new tests to verify they fail**

Run: `go test ./internal/daemon/ -run 'TestPipeline_Busy_' -v`
Expected: FAIL — current code emits on every above-`5cs` tick (no rise/fall accounting), so the counts won't match (e.g. `RiseRequiresSustainedTicks` will see an emit after the first above-bar tick).

- [ ] **Step 5: Implement the state machine in `collectAgentSignals`**

In `internal/daemon/pipeline.go`, extend the deferred cleanup sweep in `collectAgentSignals` to also prune `procActivity` (add inside the existing `defer func()` next to the `prevCPU`/`procClass` loops):

```go
		for pid := range p.procActivity {
			if !livePIDs[pid] {
				delete(p.procActivity, pid)
			}
		}
```

At the PID-reuse (binary-name change) drop point — currently:

```go
		if cached, ok := p.procClass[proc.PID]; ok && cached.name != proc.Name {
			delete(p.procClass, proc.PID)
		}
```

add a sibling reset:

```go
			delete(p.procActivity, proc.PID)
```

At the CPU-regression drop point — currently:

```go
		if proc.CPUTicks < prev {
			delete(p.procClass, proc.PID)
			continue
		}
```

add `delete(p.procActivity, proc.PID)` before the `continue`.

Now replace the single-tick threshold gate. The current code is:

```go
		if proc.CPUTicks-prev < threshold {
			continue
		}
```

Replace it with the state-machine update + working gate. Insert helper resolution of rise/fall (treat <1 as 1 so the loop always has a meaningful streak target):

```go
		delta := proc.CPUTicks - prev
		rise := p.cfg.AgentBusyRiseTicks
		if rise < 1 {
			rise = 1
		}
		fall := p.cfg.AgentBusyFallTicks
		if fall < 1 {
			fall = 1
		}
		st := p.procActivity[proc.PID]
		if delta >= threshold {
			st.aboveStreak++
			st.belowStreak = 0
			if !st.working && st.aboveStreak >= rise {
				st.working = true
			}
		} else {
			st.belowStreak++
			st.aboveStreak = 0
			if st.working && st.belowStreak >= fall {
				st.working = false
			}
		}
		p.procActivity[proc.PID] = st
		if !st.working {
			continue
		}
```

Note: the classification block that follows (`cached, ok := p.procClass[proc.PID]` … `if !cached.track { continue }`) is unchanged and still runs only for `working` PIDs, so the expensive cwd lookup happens lazily on the first working tick — same laziness as before, just gated by `working` instead of a one-tick threshold.

- [ ] **Step 6: Run the new tests to verify they pass**

Run: `go test ./internal/daemon/ -run 'TestPipeline_Busy_' -v`
Expected: PASS (all four).

- [ ] **Step 7: Run the full daemon suite to find regressions**

Run: `go test ./internal/daemon/...`
Expected: Some existing tests FAIL — they assume a single above-`5cs` tick emits immediately (e.g. `TestPipeline_AgentSignal_CountsOnlyWhenActive`, `TestPipeline_AgentSignal_IdleThresholdHigherWhenUserIdle`, the armed/paused/cwd-rule tests all do "tick to set prev, one big-delta tick → expect 1 emit"). Those break because the default test cfg has `AgentBusyRiseTicks: 0`→treated as 1, so a single tick SHOULD still emit. Confirm whether they pass — if `rise` defaults to 1, the legacy "one big tick emits" behavior is preserved and these should still PASS. Investigate any that fail in Task 2.

- [ ] **Step 8: Commit**

```bash
git add internal/daemon/pipeline.go internal/daemon/pipeline_test.go
git commit -m "feat(daemon): sustained-CPU busy classifier for agent signals"
```

---

## Task 2: Preserve legacy single-tick behavior for existing tests

**Files:**
- Modify: `internal/daemon/pipeline_test.go` (the `newTestPipeline` helper ~42-49)

The legacy tests drive "one big-delta tick → 1 emit". With `rise` defaulting to 1 (Task 1 Step 5 clamps `<1` to `1`), a single above-bar tick already reaches `working` and emits, so legacy tests should pass unchanged. This task verifies that and only adjusts the helper if a test genuinely depended on multi-tick accumulation.

- [ ] **Step 1: Run the full daemon suite**

Run: `go test ./internal/daemon/... -v 2>&1 | grep -E '^(=== RUN|--- FAIL|--- PASS|ok|FAIL)' | grep -i fail`
Expected: empty (no failures). The clamp `rise<1 → 1` makes the default `AgentBusyRiseTicks: 0` behave as single-tick, matching legacy expectations.

- [ ] **Step 2: If (and only if) a test fails because it needs explicit hysteresis config**

If a specific legacy test fails, set explicit rise/fall in the shared helper so the default is unambiguous. Edit `newTestPipeline`:

```go
func newTestPipeline(t *testing.T) (*Pipeline, *macos.FakeBridge, *Cache, *sql.DB) {
	t.Helper()
	return newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     5,
		CPUDeltaThreshIdle: 5, // same as active to keep existing test behaviour
		AgentBusyRiseTicks: 1, // single above-bar tick emits (legacy behavior)
		AgentBusyFallTicks: 1,
	})
}
```

- [ ] **Step 3: Re-run the full suite**

Run: `go test ./internal/daemon/...`
Expected: PASS.

- [ ] **Step 4: Commit (only if the helper changed)**

```bash
git add internal/daemon/pipeline_test.go
git commit -m "test(daemon): pin explicit busy rise/fall in shared test helper"
```

---

## Task 3: Wire config defaults and the new fields end-to-end

**Files:**
- Modify: `internal/daemon/daemon.go` (Config struct ~28-35; DefaultConfig ~37-52; Run wiring ~129-132)
- Modify: `internal/daemon/config_file.go` (FileConfig struct ~17-20; apply ~76-80; sample comment ~135-143)
- Test: `internal/daemon/config_file_test.go`

- [ ] **Step 1: Write a failing config-parse test**

Add to `internal/daemon/config_file_test.go`, matching the existing `TestFileConfig_ApplyTo` style (the apply entrypoint is the method `func (fc FileConfig) ApplyTo(cfg *Config) error`). The test asserts the two new keys land in `Config`:

```go
func TestFileConfig_ApplyTo_AgentBusyTicks(t *testing.T) {
	cfg := Config{}
	fc := FileConfig{AgentBusyRiseTicks: 4, AgentBusyFallTicks: 6}
	if err := fc.ApplyTo(&cfg); err != nil {
		t.Fatalf("ApplyTo: %v", err)
	}
	if cfg.AgentBusyRiseTicks != 4 {
		t.Errorf("AgentBusyRiseTicks = %d, want 4", cfg.AgentBusyRiseTicks)
	}
	if cfg.AgentBusyFallTicks != 6 {
		t.Errorf("AgentBusyFallTicks = %d, want 6", cfg.AgentBusyFallTicks)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/daemon/ -run TestApplyFileConfig_AgentBusyTicks -v`
Expected: FAIL — `FileConfig` has no `AgentBusyRiseTicks`/`AgentBusyFallTicks` fields, and `Config` has no such fields (compile error).

- [ ] **Step 3: Add fields to `Config` and `DefaultConfig` in `daemon.go`**

Add to the `Config` struct (after `AgentCPUThreshIdle uint64`):

```go
	AgentBusyRiseTicks int
	AgentBusyFallTicks int
```

In `DefaultConfig()`, change `AgentCPUThresh` and add the new defaults:

```go
		AgentCPUThresh:     15,
		AgentCPUThreshIdle: 100,
		AgentBusyRiseTicks: 2,
		AgentBusyFallTicks: 3,
```

In `Run`, where `PipelineConfig` is built (~129-132), add the two fields:

```go
		AgentBusyRiseTicks:   cfg.AgentBusyRiseTicks,
		AgentBusyFallTicks:   cfg.AgentBusyFallTicks,
```

- [ ] **Step 4: Add fields + parsing to `config_file.go`**

Add to `FileConfig` (after `AgentCPUThresholdIdle`):

```go
	AgentBusyRiseTicks    int    `yaml:"agent_busy_rise_ticks,omitempty"`
	AgentBusyFallTicks    int    `yaml:"agent_busy_fall_ticks,omitempty"`
```

In `ApplyTo` (near the `AgentCPUThreshold` handling ~76-80):

```go
	if fc.AgentBusyRiseTicks != 0 {
		cfg.AgentBusyRiseTicks = fc.AgentBusyRiseTicks
	}
	if fc.AgentBusyFallTicks != 0 {
		cfg.AgentBusyFallTicks = fc.AgentBusyFallTicks
	}
```

Update the sample YAML in `DefaultConfigYAML()` (~135-143): change `agent_cpu_threshold: 5` to `15` and revise its comment to describe the busy bar, then add the two new keys with comments:

```
# CPU "busy bar" for agent processes when the user is ACTIVE (centiseconds of
# CPU per poll). A process must stay at/above this for agent_busy_rise_ticks
# polls before its time counts. ~15 ≈ 3% of one core; low enough that an LLM
# agent thinking/streaming counts, high enough to ignore an idle prompt (~2cs).
agent_cpu_threshold: 15

# CPU busy bar when the user has been IDLE past idle_threshold. Higher so that
# only genuine heavy compute counts once you've stepped away — idle dev servers
# and idle agents fall below it and stop billing.
agent_cpu_threshold_idle: 100

# Consecutive above-bar polls before a process is counted as working (~rise*interval seconds).
agent_busy_rise_ticks: 2

# Consecutive below-bar polls before a working process stops counting (hysteresis).
agent_busy_fall_ticks: 3
```

- [ ] **Step 5: Run the config test + full suite**

Run: `go test ./internal/daemon/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/config_file.go internal/daemon/config_file_test.go
git commit -m "feat(daemon): config keys + defaults for busy classifier (bar 15cs, rise 2/fall 3)"
```

---

## Task 4: Paused-project regression — idle watcher no longer auto-resumes via low CPU

**Files:**
- Test: `internal/daemon/pipeline_test.go`

The paused-resume path consumes agent signals, which now come only from `working` PIDs. A low-CPU idle watcher in a paused project's dir must no longer auto-resume it. This is a behavior guarantee worth locking with a test (the existing `TestPipeline_PausedProject_AgentSignal_AutoResumes` uses a single big-delta tick which, with rise clamped to 1 in the default helper, still resumes — that stays valid).

- [ ] **Step 1: Write the test**

Add to `internal/daemon/pipeline_test.go`, using explicit hysteresis so the intent is unambiguous:

```go
func TestPipeline_PausedProject_LowCPUWatcher_DoesNotResume(t *testing.T) {
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec:   120,
		CPUDeltaThresh:     15,
		CPUDeltaThreshIdle: 100,
		AgentBusyRiseTicks: 2,
		AgentBusyFallTicks: 3,
	})
	defer db.Close()
	ctx := context.Background()

	q := store.New(db)
	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "paused-proj", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := q.SetProjectPaused(ctx, store.SetProjectPausedParams{Paused: 1, Name: "paused-proj"}); err != nil {
		t.Fatalf("pause: %v", err)
	}
	bin := "claude"
	cwd := "/work/paused-proj/"
	cache.Store(&CacheSnapshot{
		AllowedBinaries:  map[string]bool{"claude": true},
		Rules:            []domain.RuleSpec{{ID: 1, ProjectID: projID, Priority: 100, MatchBinaryName: &bin, MatchCwdPrefix: &cwd}},
		PausedProjectIDs: map[int64]bool{projID: true},
		ArmedProjects:    map[int64]bool{},
	})
	br.IdleSecondsVal = 5 // user present — but the watcher is below the busy bar
	br.CWDByPID = map[int]string{999: "/work/paused-proj/x"}

	// Idle watcher: ~2cs/poll, never reaches working.
	base := uint64(1000)
	for i := 0; i < 5; i++ {
		base += 2
		br.Processes = []macos.ProcessSample{{PID: 999, Name: "claude", CPUTicks: base}}
		_ = p.RunTick(ctx, 1000+int64(i)*5)
	}

	var paused int64
	db.QueryRow(`SELECT paused FROM projects WHERE id=?`, projID).Scan(&paused)
	if paused != 1 {
		t.Errorf("low-CPU watcher must not auto-resume a paused project; paused=%d, want 1", paused)
	}
	if !cache.Snapshot().PausedProjectIDs[projID] {
		t.Errorf("cache should still flag project paused")
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/daemon/ -run TestPipeline_PausedProject_LowCPUWatcher_DoesNotResume -v`
Expected: PASS (the low-CPU process never reaches `working`, so no agent signal, so no resume). If it FAILS, the resume path is seeing a signal it shouldn't — debug `collectAgentSignals` gating.

- [ ] **Step 3: Commit**

```bash
git add internal/daemon/pipeline_test.go
git commit -m "test(daemon): low-CPU watcher does not auto-resume paused project"
```

---

## Task 5: Verify the IDE-editing precondition (config audit, no code)

**Files:** none (runtime config inspection)

The spec guarantees "IDE editing counts via the untouched focus path" *only if* the user's IDE is allowlisted and ruled. Confirm against the live config.

- [ ] **Step 1: Inspect the running allowlist + rules**

Run: `atl status` and `atl rules` (or the equivalent list commands — check `internal/cli` for exact names, e.g. `atl watch`/`atl rules list`).
Expected: the user's IDE bundle id (e.g. `com.google.antigravity`, `com.microsoft.VSCode`, `com.todesktop.230313mzl4w4u92` for Cursor) appears in the allowed bundles, and a rule maps its window title to the relevant project.

- [ ] **Step 2: If the IDE is missing, report it**

Do NOT silently add rules. Surface to the user: "Your IDE `<bundle>` isn't allowlisted/ruled, so editing won't be attributed by the focus path — want me to add it?" and stop for their decision.

---

## Task 6: Build, full test, and final verification

- [ ] **Step 1: Build everything**

Run: `go build ./...`
Expected: no output (success).

- [ ] **Step 2: Run the entire test suite**

Run: `go test ./...`
Expected: all packages `ok`.

- [ ] **Step 3: Sanity-check the live daemon behavior (optional, manual)**

Restart the daemon (`atl restart`), then with an idle agent in one terminal and an active one in another, watch `atl status` over ~30s. Expected: the idle session's project does not accrue; the active one does. Report observed deltas.

- [ ] **Step 4: Final commit if anything is outstanding**

```bash
git status   # should be clean
```
