package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/rian/antitimely/internal/domain"
	"github.com/rian/antitimely/internal/herdr"
	"github.com/rian/antitimely/internal/macos"
	"github.com/rian/antitimely/internal/store"
)

type PipelineConfig struct {
	IdleThresholdSec   int
	CPUDeltaThresh     uint64 // applied when user is non-idle
	CPUDeltaThreshIdle uint64 // applied when user has been idle past IdleThresholdSec
	// AutoDisarmAgentTicks is how many ticks of matching agent activity (while
	// the user is present) an armed project must accumulate before it
	// auto-disarms and starts counting. 0 disables auto-disarm (the project
	// then stays armed until a focus signal disarms it). This is the self-heal
	// that stops arming from permanently dropping billable time when window
	// title capture — the normal focus-disarm path — is unavailable.
	AutoDisarmAgentTicks int
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
	// TranscriptTracking enables the Claude Code transcript signal source.
	TranscriptTracking bool
	// TranscriptRoot is the dir holding per-cwd session subdirs
	// (~/.claude/projects). Empty ⇒ transcript tracking is inert.
	TranscriptRoot string
	// TranscriptGraceSec: a session counts as actively worked for this many
	// seconds after its newest turn, stitching gaps between turns into one
	// continuous billable block.
	TranscriptGraceSec int
	// TickBudget is how long one tick may take before it is reported as slow.
	// Set it to the poll interval: past that the tracker is skipping billable
	// seconds outright (a stall on 2026-08-24 recorded 2 ticks in 6 minutes
	// and left nothing in the log to explain it). Zero disables the report.
	TickBudget time.Duration
}

// tickPhases records how long each stage of one tick took. Every stage is a
// separate failure mode in production - osascript hanging, lsof being killed,
// a transcript re-read, the single SQLite connection being contended - so the
// slow-tick report names them individually rather than quoting one total.
type tickPhases struct {
	idle       time.Duration
	focus      time.Duration
	agent      time.Duration
	transcript time.Duration
	write      time.Duration
}

func (t tickPhases) String() string {
	return fmt.Sprintf("idle=%s focus=%s agent=%s transcript=%s write=%s",
		t.idle.Round(time.Millisecond),
		t.focus.Round(time.Millisecond),
		t.agent.Round(time.Millisecond),
		t.transcript.Round(time.Millisecond),
		t.write.Round(time.Millisecond))
}

// procClass is what the agent loop remembers about a PID after its first
// CPU-threshold crossing, so the (relatively expensive) cwd lookup +
// allowlist/cwd-rule evaluation happens once per process rather than once
// per tick.
type procClass struct {
	name  string // binary name captured at classification; mismatch ⇒ PID reuse
	cwd   string // looked up via lsof; reused on subsequent emits
	track bool   // false ⇒ skip this PID (until trackFinal, see below)
	// trackFinal reports whether track was decided from complete inputs. It
	// is false only when the herdr space could not be determined on the
	// classifying tick AND the space is the one thing that could still flip
	// track to true (i.e. neither the binary allowlist nor a cwd pattern
	// matched). track is then recomputed on later ticks until the space
	// resolves — the expensive part, the lsof cwd lookup, stays cached.
	// Without this, one transient `ps -Eww` failure permanently untracked a
	// process whose only route to a project is its space binding, losing
	// every subsequent tick rather than merely its space.
	trackFinal bool
}

// activityState is the per-PID hysteresis state for the busy classifier. A
// tracked process emits agent signals only while working==true. aboveStreak /
// belowStreak count consecutive ticks the CPU delta has been at/above or below
// the regime's busy bar; whichever side accumulates enough flips working.
type activityState struct {
	working     bool
	aboveStreak int
	belowStreak int
}

// transcriptSession is per-session-file bookkeeping so each tick only reads new
// tail bytes rather than re-parsing the whole transcript.
type transcriptSession struct {
	offset       int64  // bytes consumed so far
	lastActivity int64  // unix seconds of newest entry seen
	cwd          string // authoritative cwd from the transcript body
}

type Pipeline struct {
	q       *store.Queries
	bridge  macos.Bridge
	cache   *Cache
	cfg     PipelineConfig
	prevCPU map[int]uint64
	// procClass caches each PID's classification (track-or-ignore) so we
	// don't re-lsof and re-evaluate rules every tick. Invalidated when the
	// cache snapshot pointer changes (rule/watch mutation) or when PID
	// reuse is detected via binary-name change or CPU regression.
	procClass map[int]procClass
	// procActivity is the per-PID busy/idle hysteresis state. Pruned for dead
	// PIDs in the same deferred sweep as prevCPU/procClass, and reset (deleted)
	// on PID reuse / CPU regression alongside procClass. Deliberately NOT
	// cleared on a cache-snapshot swap: busy/idle is a function of observed CPU,
	// independent of rule changes, so hysteresis state should survive a reload.
	procActivity map[int]activityState
	// herdr resolves a HERDR_PANE_ID / Claude session uuid to its herdr
	// workspace ("space"). NewPipeline defaults this to a Resolver with an
	// empty path, which resolves nothing but is safe to call — so tests that
	// never touch herdr don't need to wire one up. daemon.go overwrites this
	// with a resolver pointed at the real session.json.
	herdr *herdr.Resolver
	// procPane caches pid -> HERDR_PANE_ID (the value of the env var, "" for
	// a process not running under herdr). A process's environment is
	// immutable after exec, so one `ps -Eww` probe per pid suffices. The
	// pane -> space mapping is deliberately NOT cached here: it is read from
	// herdr's session.json, which fails closed while that file is missing,
	// mid-write, or not yet flushed for a freshly created space, so caching
	// a failed resolution would pin a long-lived agent to "no space" for its
	// entire life. Resolving the pane per tick keeps attribution
	// self-healing at the cost of one map lookup.
	// Entries are swept by the same livePIDs pass that clears prevCPU and
	// procClass, and are also deleted alongside procClass/procActivity on
	// PID-reuse detection (binary-name change or CPU-counter regression) — a
	// recycled pid never leaves livePIDs, so the sweep alone can't catch a
	// stale binding there; without this a recycled pid would keep billing
	// the old process's space.
	procPane map[int]string
	lastSnap *CacheSnapshot
	perm     *PermissionTracker
	// armedAgentStreak counts ticks of matching agent activity (user present)
	// accumulated by an armed project toward the AutoDisarmAgentTicks
	// threshold. Reset to zero whenever the project disarms.
	armedAgentStreak map[int64]int
	// titleRetryAt is the earliest unix-second at which the window-title
	// osascript may be spawned again after a denial or a run of timeouts.
	// Spawning it every tick while broken churns (and leaks) osascript
	// processes; we back off and only re-probe periodically. 0 = no backoff.
	titleRetryAt int64
	// titleBackoffSec is the current backoff length, doubled on each successive
	// failed re-probe up to titleBackoffMaxSec and reset to zero on success.
	// Fixed-interval retries re-probed 1440x/day while denied; growing the gap
	// keeps a long outage cheap without delaying recovery much.
	titleBackoffSec int64
	// titleTimeoutStreak counts consecutive timeouts. Unlike a denial (which is
	// unambiguous and backs off at once), a single timeout can be transient
	// system load, so we tolerate a few before backing off.
	titleTimeoutStreak int
	// transcriptState is per-session-file tail/offset + last-activity state,
	// keyed by absolute .jsonl path.
	transcriptState map[string]transcriptSession
	// slowTickRetryAt is the earliest unix-second at which another slow-tick
	// line may be logged; slowTickBackoffSec is the current quiet window,
	// doubled per report and cleared by the first tick back inside budget.
	// Same shape as titleRetryAt/titleBackoffSec above, and for the same
	// reason: daemon.err is append-only.
	slowTickRetryAt    int64
	slowTickBackoffSec int64
}

// Backoff policy for the window-title osascript — the only remaining call that
// goes through System Events and can therefore hang or balloon.
const (
	// titleDenyBackoffSec is the initial backoff after a failure, and the step
	// the exponential growth starts from.
	titleDenyBackoffSec int64 = 60
	// titleBackoffMaxSec caps the growth. Five minutes bounds a long denial to
	// ~288 probes/day while still noticing a re-grant reasonably promptly.
	titleBackoffMaxSec int64 = 300
	// osascriptTimeoutStreak is how many consecutive timeouts trigger a
	// backoff. Timeouts, not denials, drove the production leak (2707 killed
	// osascripts vs 85 title denials), so they must back off too — but a lone
	// timeout under momentary load shouldn't blind title capture for a minute.
	osascriptTimeoutStreak = 3
)

// Backoff policy for the slow-tick report, mirroring the title constants
// above: the first repeat is quiet for a minute, growing to five.
const (
	slowTickBackoffStartSec int64 = 60
	slowTickBackoffMaxSec   int64 = 300
)

func NewPipeline(q *store.Queries, b macos.Bridge, cache *Cache, cfg PipelineConfig) *Pipeline {
	return &Pipeline{
		q: q, bridge: b, cache: cache, cfg: cfg,
		prevCPU:          map[int]uint64{},
		procClass:        map[int]procClass{},
		procActivity:     map[int]activityState{},
		herdr:            herdr.NewResolver(""),
		procPane:         map[int]string{},
		armedAgentStreak: map[int64]int{},
		transcriptState:  map[string]transcriptSession{},
	}
}

// SetPermissionTracker wires a shared PermissionTracker into the pipeline so
// that accessibility denials detected during collectFocusSignal are propagated
// to the RPC Status response. If not called, permission state is not updated.
func (p *Pipeline) SetPermissionTracker(pt *PermissionTracker) {
	p.perm = pt
}

// RunTick executes one observation cycle. now is the unix-epoch seconds of
// this tick.
func (p *Pipeline) RunTick(ctx context.Context, now int64) error {
	var ph tickPhases
	tickStart := time.Now()
	defer func() { p.reportSlowTick(ph, time.Since(tickStart), now) }()

	snap := p.cache.Snapshot()
	// Snapshot is swapped wholesale on every ReloadCache, so a pointer
	// change is sufficient evidence that rules or the allowlist may have
	// moved underneath us; classifications based on the old view could
	// be wrong.
	if snap != p.lastSnap {
		clear(p.procClass)
		p.lastSnap = snap
	}

	phaseStart := time.Now()
	idle, err := p.bridge.IdleSeconds(ctx)
	ph.idle = time.Since(phaseStart)
	// Fail open: if we can't read idle state, assume the user is present.
	// The previous "userPresent = false on error" default silently flipped
	// the agent CPU threshold to the much stricter idle bar, so a broken
	// ioreg dropped most agent ticks with no user-visible signal beyond
	// a log line. Better to keep tracking (and surface the failure) than
	// to silently degrade.
	userPresent := true
	if err != nil {
		log.Printf("idle: %v", err)
		if p.perm != nil {
			p.perm.Set("idle_detection_failed", now)
		}
	} else {
		userPresent = idle < p.cfg.IdleThresholdSec
	}

	var signals []domain.Signal

	if userPresent {
		phaseStart = time.Now()
		if sig, ok := p.collectFocusSignal(ctx, snap, now); ok {
			signals = append(signals, sig)
		}
		ph.focus = time.Since(phaseStart)
	}
	phaseStart = time.Now()
	signals = append(signals, p.collectAgentSignals(ctx, snap, userPresent)...)
	ph.agent = time.Since(phaseStart)
	phaseStart = time.Now()
	signals = append(signals, p.collectTranscriptSignals(snap, now)...)
	ph.transcript = time.Since(phaseStart)

	if len(signals) == 0 {
		return nil
	}
	phaseStart = time.Now()
	defer func() { ph.write = time.Since(phaseStart) }()

	// Per-tick local state for the arming gate. snap.ArmedProjects is the
	// start-of-tick view; once we auto-disarm a project mid-tick we must treat
	// it as disarmed for the rest of this tick (the local snap won't reflect
	// the cache mutation). armedCountedThisTick dedups the streak/suppressed
	// bookkeeping when several PIDs map to the same armed project in one tick.
	disarmedThisTick := map[int64]bool{}
	armedCountedThisTick := map[int64]bool{}
	// tickedThisTick prevents a transcript signal from double-ticking a project
	// already counted via focus or agent in this cycle. Transcript signals are
	// appended last so focus/agent ticks register first.
	// Row-count hygiene only; ultimate per-project dedup is COUNT(DISTINCT ts) in the totals queries.
	tickedThisTick := map[int64]bool{}

	for _, sig := range signals {
		obsID, err := p.q.UpsertObservation(ctx, store.UpsertObservationParams{
			Source:      string(sig.Source),
			BundleID:    sig.BundleID,
			WindowTitle: sig.WindowTitle,
			BinaryName:  sig.BinaryName,
			Cwd:         sig.Cwd,
			SpaceID:     sig.SpaceID,
			FirstSeen:   now,
		})
		if err != nil {
			log.Printf("upsert obs: %v", err)
			continue
		}
		ignored, err := p.q.IsObservationIgnored(ctx, obsID)
		if err != nil {
			log.Printf("check ignored: %v", err)
			continue
		}
		if ignored != 0 {
			continue
		}

		pid := domain.MatchRules(sig, snap.Rules)

		if (sig.IsFocus() || sig.IsTranscript()) && pid != nil {
			p.cache.DisarmProject(*pid)
			delete(p.armedAgentStreak, *pid)
			disarmedThisTick[*pid] = true
		}

		// Paused projects:
		//   * Transcript signal ⇒ real human-directed work captured: auto-resume
		//     and count this tick immediately (fall through to InsertTick).
		//   * Agent signal while the user is present ⇒ fresh CPU activity in a
		//     tracked dir is a strong "user is back at work" signal: auto-resume,
		//     arm, and drop this tick (next tick counts after disarm).
		//   * Agent signal while the user is idle ⇒ unattended background CPU
		//     (dev servers, language servers, AI agents, builds) that must NOT
		//     resurrect a paused project. Without this gate a paused project
		//     billed around the clock whenever a process churned in its dir.
		//   * Focus signal ⇒ a window left open in the foreground is too weak
		//     a signal; still skip the tick. The observation is already upserted
		//     above so it survives in review history.
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

		// Arming gate for agent signals. An armed project's background CPU
		// doesn't count until the project is disarmed — normally by focusing a
		// matching window. But focus-disarm depends on window-title capture,
		// which can fail silently (e.g. Accessibility revoked after a rebuild),
		// leaving the project permanently armed and silently dropping billable
		// time. Two safeguards: count the suppressed ticks so Status can show
		// them, and auto-disarm after sustained matching activity while the
		// user is present (sustained CPU in the project's dir is real presence).
		if sig.IsAgent() && pid != nil && snap.ArmedProjects[*pid] && !disarmedThisTick[*pid] {
			if armedCountedThisTick[*pid] {
				continue // this project's gate already handled this tick
			}
			armedCountedThisTick[*pid] = true

			if userPresent && p.cfg.AutoDisarmAgentTicks > 0 {
				p.armedAgentStreak[*pid]++
				if p.armedAgentStreak[*pid] >= p.cfg.AutoDisarmAgentTicks {
					p.cache.DisarmProject(*pid)
					delete(p.armedAgentStreak, *pid)
					disarmedThisTick[*pid] = true
					log.Printf("auto-disarmed project %d: %d ticks of sustained agent activity (binary=%q cwd=%q)",
						*pid, p.cfg.AutoDisarmAgentTicks, sig.BinaryName, sig.Cwd)
					// fall through: this tick now counts.
				} else {
					if n := p.cache.AddSuppressed(*pid); n == 1 {
						log.Printf("project %d armed: suppressing agent ticks pending focus or sustained activity (binary=%q cwd=%q)",
							*pid, sig.BinaryName, sig.Cwd)
					}
					continue
				}
			} else {
				// User idle (or auto-disarm disabled): never auto-disarm —
				// idle background work is exactly what arming gates. Still
				// record the suppression for visibility.
				if n := p.cache.AddSuppressed(*pid); n == 1 {
					log.Printf("project %d armed: suppressing agent ticks (user idle) (binary=%q cwd=%q)",
						*pid, sig.BinaryName, sig.Cwd)
				}
				continue
			}
		}

		// Dedup: transcript must not double-tick a project already counted by
		// focus or agent this cycle. Transcript signals are appended last so
		// focus/agent ticks register first into tickedThisTick.
		if pid != nil && sig.IsTranscript() && tickedThisTick[*pid] {
			continue
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
	}

	return nil
}

// backOffTitle arms (or extends) the window-title backoff and returns the
// number of seconds capture is suppressed for, so callers can log it. The
// interval starts at titleDenyBackoffSec and doubles per successive failure up
// to titleBackoffMaxSec; a successful probe resets it.
// reportSlowTick logs one line when a tick overran its budget, naming where
// the time went. A tick that outlives the poll interval is billable seconds
// going unrecorded, and until now that happened silently: the 2026-08-24 stall
// (2 ticks in 6 minutes, every RPC hitting the 10s handler deadline) left
// nothing in daemon.err but the downstream lsof/osascript kills it caused.
//
// Repeats back off exactly like the window-title probe (see backOffTitle):
// daemon.err is append-only and already megabytes, so a lasting pathology -
// every tick overrunning - must not add a line every 5s forever. A tick that
// finishes inside its budget ends the incident and clears the backoff, so the
// next problem is reported at once rather than swallowed by an open window.
func (p *Pipeline) reportSlowTick(ph tickPhases, took time.Duration, now int64) {
	if p.cfg.TickBudget <= 0 {
		return
	}
	if took <= p.cfg.TickBudget {
		p.slowTickBackoffSec = 0
		p.slowTickRetryAt = 0
		return
	}
	if now < p.slowTickRetryAt {
		return
	}
	log.Printf("slow tick: took %s (budget %s) %s",
		took.Round(time.Millisecond), p.cfg.TickBudget, ph)
	p.backOffSlowTick(now)
}

// backOffSlowTick grows the quiet window after a reported slow tick: 60s, then
// doubling to a 5-minute cap.
func (p *Pipeline) backOffSlowTick(now int64) {
	switch {
	case p.slowTickBackoffSec == 0:
		p.slowTickBackoffSec = slowTickBackoffStartSec
	case p.slowTickBackoffSec < slowTickBackoffMaxSec:
		p.slowTickBackoffSec *= 2
	}
	if p.slowTickBackoffSec > slowTickBackoffMaxSec {
		p.slowTickBackoffSec = slowTickBackoffMaxSec
	}
	p.slowTickRetryAt = now + p.slowTickBackoffSec
}

func (p *Pipeline) backOffTitle(now int64) int64 {
	switch {
	case p.titleBackoffSec == 0:
		p.titleBackoffSec = titleDenyBackoffSec
	case p.titleBackoffSec < titleBackoffMaxSec:
		p.titleBackoffSec *= 2
	}
	if p.titleBackoffSec > titleBackoffMaxSec {
		p.titleBackoffSec = titleBackoffMaxSec
	}
	p.titleRetryAt = now + p.titleBackoffSec
	return p.titleBackoffSec
}

func (p *Pipeline) collectFocusSignal(ctx context.Context, snap *CacheSnapshot, now int64) (domain.Signal, bool) {
	fm, err := p.bridge.Frontmost(ctx)
	if err != nil {
		if errors.Is(err, macos.ErrAccessibilityDenied) {
			log.Printf("frontmost denied: %v", err)
			if p.perm != nil {
				p.perm.Set("accessibility_denied", now)
			}
		} else {
			log.Printf("frontmost: %v", err)
		}
		return domain.Signal{}, false
	}
	if !snap.AllowedBundles[fm.BundleID] {
		return domain.Signal{}, false
	}

	// Title capture is the expensive, leak-prone osascript (it introspects the
	// focused window's accessibility tree). When it's been denied, back off:
	// re-probing every tick spawned an osascript every 5s that — if System
	// Events hangs — orphans at GB scale and accumulates. A permission grant
	// just takes up to titleDenyBackoffSec to be noticed; bundle-only rules
	// still match in the meantime.
	title := ""
	if now >= p.titleRetryAt {
		t, terr := p.bridge.FocusedWindowTitle(ctx)
		switch {
		case terr == nil:
			title = t
			p.titleRetryAt = 0
			p.titleBackoffSec = 0
			p.titleTimeoutStreak = 0
			if p.perm != nil {
				p.perm.Set("ok", now)
			}
		case errors.Is(terr, macos.ErrAccessibilityDenied):
			if p.perm != nil {
				p.perm.Set("accessibility_denied", now)
			}
			p.titleTimeoutStreak = 0
			log.Printf("title denied; backing off osascript for %ds", p.backOffTitle(now))
		case errors.Is(terr, macos.ErrOsascriptTimeout):
			// A hung osascript is the leak vector: the process can outlive our
			// SIGKILL (WaitDelay lets us stop waiting on one stuck in an
			// uninterruptible mach call), so respawning every tick stacks up
			// multi-GB orphans. Tolerate a couple, then back off like a denial.
			p.titleTimeoutStreak++
			if p.titleTimeoutStreak >= osascriptTimeoutStreak {
				log.Printf("title timed out %dx; backing off osascript for %ds",
					p.titleTimeoutStreak, p.backOffTitle(now))
				p.titleTimeoutStreak = 0
			} else {
				log.Printf("title: %v", terr)
			}
		default:
			// Genuinely transient (bad output, script error): keep the title
			// empty but don't back off — the next tick retries.
			p.titleTimeoutStreak = 0
			log.Printf("title: %v", terr)
		}
	}
	return domain.Signal{
		Source:      domain.SourceFocus,
		BundleID:    fm.BundleID,
		WindowTitle: title,
	}, true
}

func (p *Pipeline) collectAgentSignals(ctx context.Context, snap *CacheSnapshot, userPresent bool) []domain.Signal {
	livePIDs := make(map[int]bool)
	defer func() {
		for pid := range p.prevCPU {
			if !livePIDs[pid] {
				delete(p.prevCPU, pid)
			}
		}
		for pid := range p.procClass {
			if !livePIDs[pid] {
				delete(p.procClass, pid)
			}
		}
		for pid := range p.procActivity {
			if !livePIDs[pid] {
				delete(p.procActivity, pid)
			}
		}
		for pid := range p.procPane {
			if !livePIDs[pid] {
				delete(p.procPane, pid)
			}
		}
	}()

	procs, err := p.bridge.ListProcesses(ctx)
	if err != nil {
		log.Printf("ps: %v", err)
		return nil
	}

	threshold := p.cfg.CPUDeltaThresh
	if !userPresent {
		threshold = p.cfg.CPUDeltaThreshIdle
	}

	var out []domain.Signal

	for _, proc := range procs {
		livePIDs[proc.PID] = true

		// Track CPU for every PID regardless of allowlist so that processes
		// reached via the cwd-rule path (not pre-registered as binaries) still
		// have a delta to measure on their second tick.
		prev, seen := p.prevCPU[proc.PID]
		p.prevCPU[proc.PID] = proc.CPUTicks
		if !seen {
			continue
		}

		// PID reuse via binary-name change: definitively a different process,
		// drop the cached classification before any further check.
		if cached, ok := p.procClass[proc.PID]; ok && cached.name != proc.Name {
			delete(p.procClass, proc.PID)
			delete(p.procActivity, proc.PID)
			delete(p.procPane, proc.PID)
		}

		// CPU counters are monotonic within a process; a regression means
		// either PID reuse or a counter glitch. Drop the cache and skip the
		// tick — the next tick will reclassify against the new process.
		if proc.CPUTicks < prev {
			delete(p.procClass, proc.PID)
			delete(p.procActivity, proc.PID)
			delete(p.procPane, proc.PID)
			continue
		}

		delta := proc.CPUTicks - prev
		rise := max(p.cfg.AgentBusyRiseTicks, 1)
		fall := max(p.cfg.AgentBusyFallTicks, 1)
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

		// Resolve the herdr space ahead of the track decision below: a
		// process in a rule-bound space must be tracked even when its cwd
		// matches no pattern, so track can't be computed without it.
		//
		// Two lookups, cached differently on purpose. The pane id comes from
		// the process environment, which is immutable after exec, so one
		// probe per pid is both correct and enough. The pane -> space
		// mapping comes from herdr's session.json, which fails closed while
		// that file is missing, mid-write, or not yet flushed for a
		// just-created space; it is therefore re-resolved every tick, so a
		// process that started before herdr persisted its space picks that
		// space up as soon as the file lands instead of carrying "no space"
		// (and billing whatever its cwd rule says) for its whole life.
		paneID, paneKnown := p.procPane[proc.PID]
		if !paneKnown {
			v, err := p.bridge.ProcessEnvVar(ctx, proc.PID, "HERDR_PANE_ID")
			if err != nil {
				// A failed probe must not drop the signal, and must not be
				// cached either — caching would wrongly pin this pid to
				// cwd-only matching for its entire life over one transient
				// failure. Log and retry the probe next tick, mirroring the
				// "don't cache an empty cwd" precedent just below.
				log.Printf("env pid=%d: %v", proc.PID, err)
			} else {
				// A successful probe finding no variable is a real answer
				// (the process is not under herdr) and is cached, so
				// non-herdr processes aren't re-probed every tick.
				paneID, paneKnown = v, true
				p.procPane[proc.PID] = v
			}
		}
		// spaceKnown distinguishes "this process has no space" (a fact) from
		// "the space could not be determined right now" (a transient), which
		// is what the classification below must not cache.
		spaceID := ""
		spaceKnown := paneKnown && paneID == ""
		if paneID != "" {
			if s, resolved := p.herdr.SpaceForPane(paneID); resolved {
				spaceID, spaceKnown = s.ID, true
			}
		}

		cached, ok := p.procClass[proc.PID]
		if !ok {
			cwd, err := p.bridge.ProcessCWD(ctx, proc.PID)
			if err != nil {
				log.Printf("cwd pid=%d: %v", proc.PID, err)
				continue
			}
			if cwd == "" {
				// Sandboxed or transient — retry rather than cache an empty cwd.
				continue
			}
			cached = procClass{name: proc.Name, cwd: cwd}
		}
		if !cached.trackFinal {
			// Everything except the space binding; if any of it matches, the
			// space cannot change the answer and the verdict is final.
			trackWithoutSpace := snap.AllowedBinaries[proc.Name] ||
				cwdMatchesAnyPattern(cached.cwd, snap.CwdPatterns)
			cached.track = trackWithoutSpace || (spaceID != "" && snap.BoundSpaceIDs[spaceID])
			cached.trackFinal = trackWithoutSpace || spaceKnown
			p.procClass[proc.PID] = cached
		}

		if !cached.track {
			continue
		}

		out = append(out, domain.Signal{
			Source:     domain.SourceAgent,
			BinaryName: proc.Name,
			Cwd:        cached.cwd,
			SpaceID:    spaceID,
		})
	}

	return out
}

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
