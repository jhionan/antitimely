package daemon

import (
	"context"
	"database/sql"
	"errors"
	"log"

	"github.com/rian/antitimely/internal/domain"
	"github.com/rian/antitimely/internal/macos"
	"github.com/rian/antitimely/internal/store"
)

type PipelineConfig struct {
	IdleThresholdSec int
	CPUDeltaThresh   uint64
}

type Pipeline struct {
	q       *store.Queries
	bridge  macos.Bridge
	cache   *Cache
	cfg     PipelineConfig
	prevCPU map[int]uint64
}

func NewPipeline(q *store.Queries, b macos.Bridge, cache *Cache, cfg PipelineConfig) *Pipeline {
	return &Pipeline{
		q: q, bridge: b, cache: cache, cfg: cfg,
		prevCPU: map[int]uint64{},
	}
}

// RunTick executes one observation cycle. now is the unix-epoch seconds of
// this tick.
func (p *Pipeline) RunTick(ctx context.Context, now int64) error {
	snap := p.cache.Snapshot()

	idle, err := p.bridge.IdleSeconds()
	if err != nil {
		log.Printf("idle: %v", err)
		idle = 0
	}
	userPresent := idle < p.cfg.IdleThresholdSec

	var signals []domain.Signal

	if userPresent {
		if sig, ok := p.collectFocusSignal(snap); ok {
			signals = append(signals, sig)
		}
	}
	signals = append(signals, p.collectAgentSignals(snap)...)

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

func (p *Pipeline) collectFocusSignal(snap *CacheSnapshot) (domain.Signal, bool) {
	fm, err := p.bridge.Frontmost()
	if err != nil {
		if errors.Is(err, macos.ErrAccessibilityDenied) {
			log.Printf("frontmost denied: %v", err)
		} else {
			log.Printf("frontmost: %v", err)
		}
		return domain.Signal{}, false
	}
	if !snap.AllowedBundles[fm.BundleID] {
		return domain.Signal{}, false
	}
	title, err := p.bridge.FocusedWindowTitle()
	if err != nil {
		// Continue with empty title; bundle-only rules can still match.
		log.Printf("title: %v", err)
		title = ""
	}
	return domain.Signal{
		Source:      domain.SourceFocus,
		BundleID:    fm.BundleID,
		WindowTitle: title,
	}, true
}

func (p *Pipeline) collectAgentSignals(snap *CacheSnapshot) []domain.Signal {
	procs, err := p.bridge.ListProcesses()
	if err != nil {
		log.Printf("ps: %v", err)
		return nil
	}

	livePIDs := make(map[int]bool, len(procs))
	var out []domain.Signal

	for _, proc := range procs {
		livePIDs[proc.PID] = true
		if !snap.AllowedBinaries[proc.Name] {
			continue
		}
		prev, seen := p.prevCPU[proc.PID]
		p.prevCPU[proc.PID] = proc.CPUTicks
		if !seen {
			continue
		}
		if proc.CPUTicks-prev < p.cfg.CPUDeltaThresh {
			continue
		}
		cwd, err := p.bridge.ProcessCWD(proc.PID)
		if err != nil || cwd == "" {
			continue
		}
		out = append(out, domain.Signal{
			Source:     domain.SourceAgent,
			BinaryName: proc.Name,
			Cwd:        cwd,
		})
	}

	// Drop stale pids so prevCPU stays bounded.
	for pid := range p.prevCPU {
		if !livePIDs[pid] {
			delete(p.prevCPU, pid)
		}
	}

	return out
}
