package daemon

import (
	"context"
	"time"

	"github.com/rian/antitimely/internal/macos"
	"github.com/rian/antitimely/internal/rpcapi"
	"github.com/rian/antitimely/internal/store"
)

// AntitimelyService exposes all daemon operations over net/rpc.
type AntitimelyService struct {
	Q                   *store.Queries
	Cache               *Cache
	Bridge              macos.Bridge
	TickIntervalSeconds int
	PermissionState     string
}

// Status returns a live snapshot of daemon state.
func (s *AntitimelyService) Status(args rpcapi.StatusArgs, reply *rpcapi.StatusReply) error {
	ctx := context.Background()
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	endOfDay := startOfDay + 86400

	totals, err := s.Q.TotalsByProject(ctx, store.TotalsByProjectParams{Ts: startOfDay, Ts_2: endOfDay})
	if err != nil {
		return err
	}
	reply.TodayTotalsSeconds = make(map[string]int64, len(totals))
	for _, r := range totals {
		reply.TodayTotalsSeconds[r.Name] = r.TickCount * int64(s.TickIntervalSeconds)
	}

	unassigned, err := s.Q.UnassignedTicksInRange(ctx, store.UnassignedTicksInRangeParams{Ts: startOfDay, Ts_2: endOfDay})
	if err != nil {
		return err
	}
	reply.UnassignedTodaySeconds = unassigned * int64(s.TickIntervalSeconds)

	pending, err := s.Q.CountPendingReviewSignatures(ctx)
	if err != nil {
		return err
	}
	reply.UnassignedSignaturesCount = int(pending)

	idle, _ := s.Bridge.IdleSeconds()
	reply.UserIdleSeconds = idle
	reply.TickIntervalSeconds = s.TickIntervalSeconds

	if s.PermissionState == "" {
		reply.PermissionState = "ok"
	} else {
		reply.PermissionState = s.PermissionState
	}
	return nil
}
