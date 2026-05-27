package daemon

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"strings"

	"github.com/rian/antitimely/internal/domain"
	"github.com/rian/antitimely/internal/macos"
	"github.com/rian/antitimely/internal/store"
)

type PipelineConfig struct {
	IdleThresholdSec   int
	CPUDeltaThresh     uint64 // applied when user is non-idle
	CPUDeltaThreshIdle uint64 // applied when user has been idle past IdleThresholdSec
}

// procClass is what the agent loop remembers about a PID after its first
// CPU-threshold crossing, so the (relatively expensive) cwd lookup +
// allowlist/cwd-rule evaluation happens once per process rather than once
// per tick.
type procClass struct {
	name  string // binary name captured at classification; mismatch ⇒ PID reuse
	cwd   string // looked up via lsof; reused on subsequent emits
	track bool   // false ⇒ skip this PID for the rest of its life
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
	lastSnap  *CacheSnapshot
	perm      *PermissionTracker
}

func NewPipeline(q *store.Queries, b macos.Bridge, cache *Cache, cfg PipelineConfig) *Pipeline {
	return &Pipeline{
		q: q, bridge: b, cache: cache, cfg: cfg,
		prevCPU:   map[int]uint64{},
		procClass: map[int]procClass{},
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
	snap := p.cache.Snapshot()
	// Snapshot is swapped wholesale on every ReloadCache, so a pointer
	// change is sufficient evidence that rules or the allowlist may have
	// moved underneath us; classifications based on the old view could
	// be wrong.
	if snap != p.lastSnap {
		clear(p.procClass)
		p.lastSnap = snap
	}

	idle, err := p.bridge.IdleSeconds(ctx)
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
			p.perm.Set("idle_detection_failed")
		}
	} else {
		userPresent = idle < p.cfg.IdleThresholdSec
	}

	var signals []domain.Signal

	if userPresent {
		if sig, ok := p.collectFocusSignal(ctx, snap); ok {
			signals = append(signals, sig)
		}
	}
	signals = append(signals, p.collectAgentSignals(ctx, snap, userPresent)...)

	if len(signals) == 0 {
		return nil
	}

	for _, sig := range signals {
		obsID, err := p.q.UpsertObservation(ctx, store.UpsertObservationParams{
			Source:      string(sig.Source),
			BundleID:    sig.BundleID,
			WindowTitle: sig.WindowTitle,
			BinaryName:  sig.BinaryName,
			Cwd:         sig.Cwd,
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

		if sig.IsFocus() && pid != nil {
			p.cache.DisarmProject(*pid)
		}

		// Paused projects:
		//   * Agent signal ⇒ fresh CPU activity in a tracked dir is a strong
		//     "user is back at work" signal: auto-resume and let the tick land.
		//   * Focus signal ⇒ a window left open in the foreground is too weak
		//     a signal; still skip the tick. The observation is already upserted
		//     above so it survives in review history.
		if pid != nil && snap.PausedProjectIDs[*pid] {
			if !sig.IsAgent() {
				continue
			}
			if err := p.q.ResumeProjectByID(ctx, *pid); err != nil {
				log.Printf("auto-resume project %d: %v", *pid, err)
				continue
			}
			p.cache.MarkProjectActive(*pid)
			log.Printf("auto-resumed project %d: agent activity (binary=%q cwd=%q)", *pid, sig.BinaryName, sig.Cwd)
		}

		if sig.IsAgent() && pid != nil && snap.ArmedProjects[*pid] {
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
		}
	}

	return nil
}

func (p *Pipeline) collectFocusSignal(ctx context.Context, snap *CacheSnapshot) (domain.Signal, bool) {
	fm, err := p.bridge.Frontmost(ctx)
	if err != nil {
		if errors.Is(err, macos.ErrAccessibilityDenied) {
			log.Printf("frontmost denied: %v", err)
			if p.perm != nil {
				p.perm.Set("accessibility_denied")
			}
		} else {
			log.Printf("frontmost: %v", err)
		}
		return domain.Signal{}, false
	}
	if p.perm != nil {
		p.perm.Set("ok")
	}
	if !snap.AllowedBundles[fm.BundleID] {
		return domain.Signal{}, false
	}
	title, err := p.bridge.FocusedWindowTitle(ctx)
	if err != nil {
		// ErrAccessibilityDenied here means Automation works for Frontmost
		// but not for window introspection. Surface it the same way so the
		// user sees a non-"ok" state in Status. Continue with an empty
		// title regardless — bundle-only rules can still match.
		if errors.Is(err, macos.ErrAccessibilityDenied) && p.perm != nil {
			p.perm.Set("accessibility_denied")
		}
		log.Printf("title: %v", err)
		title = ""
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
		}

		// CPU counters are monotonic within a process; a regression means
		// either PID reuse or a counter glitch. Drop the cache and skip the
		// tick — the next tick will reclassify against the new process.
		if proc.CPUTicks < prev {
			delete(p.procClass, proc.PID)
			continue
		}
		if proc.CPUTicks-prev < threshold {
			continue
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
			cached = procClass{
				name:  proc.Name,
				cwd:   cwd,
				track: snap.AllowedBinaries[proc.Name] || cwdUnderAnyPrefix(cwd, snap.CwdPrefixes),
			}
			p.procClass[proc.PID] = cached
		}

		if !cached.track {
			continue
		}

		out = append(out, domain.Signal{
			Source:     domain.SourceAgent,
			BinaryName: proc.Name,
			Cwd:        cached.cwd,
		})
	}

	return out
}

// cwdUnderAnyPrefix returns true when cwd equals or is a true subdirectory of
// any entry in prefixes (which must already be trailing-slash-stripped). Same
// semantics as the rule matcher's cwd check, kept consistent so the upstream
// "is this dir tracked?" decision and the downstream "does rule X apply?"
// decision can never disagree.
func cwdUnderAnyPrefix(cwd string, prefixes []string) bool {
	for _, p := range prefixes {
		if cwd == p || strings.HasPrefix(cwd, p+"/") {
			return true
		}
	}
	return false
}
