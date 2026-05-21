package daemon

import (
	"context"
	"log"
	"time"
)

// TickRunner is the contract the Poller needs; Pipeline satisfies it.
type TickRunner interface {
	RunTick(ctx context.Context, now int64) error
}

type Poller struct {
	runner   TickRunner
	interval time.Duration
}

func NewPoller(r TickRunner, interval time.Duration) *Poller {
	return &Poller{runner: r, interval: interval}
}

// Run blocks until ctx is cancelled, invoking runner.RunTick at every interval.
// Returns when ctx is done.
func (p *Poller) Run(ctx context.Context) {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if err := p.runner.RunTick(ctx, now.Unix()); err != nil {
				// The pipeline already best-effort-logs per-call failures
				// internally; surfacing a returned error means something
				// it considered fatal escaped. Log it loud so the user
				// sees it in daemon.err — the alternative is a silent
				// stopped tracker.
				log.Printf("tick error: %v", err)
			}
		}
	}
}
