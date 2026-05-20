package daemon

import (
	"context"
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
			_ = p.runner.RunTick(ctx, now.Unix())
		}
	}
}
