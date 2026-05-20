package daemon

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type counterPipeline struct{ n atomic.Int64 }

func (c *counterPipeline) RunTick(ctx context.Context, now int64) error {
	c.n.Add(1)
	return nil
}

func TestPoller_TicksAndStops(t *testing.T) {
	cp := &counterPipeline{}
	p := NewPoller(cp, 20*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	p.Run(ctx)

	got := cp.n.Load()
	if got < 3 || got > 8 {
		t.Errorf("expected ~5 ticks in 100ms with 20ms interval, got %d", got)
	}
}
