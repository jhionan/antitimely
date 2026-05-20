package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/rian/antitimely/internal/domain"
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

// ReloadCache rebuilds the cache snapshot from the DB and atomically swaps it.
// Called on daemon startup and after every mutation handler.
func (s *AntitimelyService) ReloadCache() error {
	ctx := context.Background()
	watched, err := s.Q.ListWatchedPrograms(ctx)
	if err != nil {
		return err
	}
	snap := &CacheSnapshot{
		AllowedBundles:  map[string]bool{},
		AllowedBinaries: map[string]bool{},
	}
	for _, w := range watched {
		switch w.Kind {
		case "bundle":
			snap.AllowedBundles[w.Identifier] = true
		case "binary":
			snap.AllowedBinaries[w.Identifier] = true
		}
	}
	rules, err := s.Q.ListRulesForCache(ctx)
	if err != nil {
		return err
	}
	for _, r := range rules {
		spec := domain.RuleSpec{
			ID:        r.ID,
			ProjectID: r.ProjectID,
			Priority:  r.Priority,
		}
		if r.MatchBundleID.Valid {
			v := r.MatchBundleID.String
			spec.MatchBundleID = &v
		}
		if r.MatchTitleSubstr.Valid {
			v := r.MatchTitleSubstr.String
			spec.MatchTitleSubstr = &v
		}
		if r.MatchBinaryName.Valid {
			v := r.MatchBinaryName.String
			spec.MatchBinaryName = &v
		}
		if r.MatchCwdPrefix.Valid {
			v := r.MatchCwdPrefix.String
			spec.MatchCwdPrefix = &v
		}
		snap.Rules = append(snap.Rules, spec)
	}
	s.Cache.Store(snap)
	return nil
}

var errBadKind = errors.New("kind must be 'bundle' or 'binary'")

// WatchAdd adds a program to the allowlist and refreshes the cache.
func (s *AntitimelyService) WatchAdd(args rpcapi.WatchAddArgs, reply *rpcapi.WatchAddReply) error {
	ctx := context.Background()
	if args.Kind != "bundle" && args.Kind != "binary" {
		return errBadKind
	}
	if err := s.Q.AddWatchedProgram(ctx, store.AddWatchedProgramParams{
		Kind: args.Kind, Identifier: args.Identifier, CreatedAt: time.Now().Unix(),
	}); err != nil {
		return err
	}
	return s.ReloadCache()
}

// WatchRemove removes a program from the allowlist and refreshes the cache.
func (s *AntitimelyService) WatchRemove(args rpcapi.WatchRemoveArgs, reply *rpcapi.WatchRemoveReply) error {
	ctx := context.Background()
	if err := s.Q.RemoveWatchedProgram(ctx, store.RemoveWatchedProgramParams{
		Kind: args.Kind, Identifier: args.Identifier,
	}); err != nil {
		return err
	}
	return s.ReloadCache()
}

// WatchList returns all currently watched programs.
func (s *AntitimelyService) WatchList(args rpcapi.WatchListArgs, reply *rpcapi.WatchListReply) error {
	ctx := context.Background()
	rows, err := s.Q.ListWatchedPrograms(ctx)
	if err != nil {
		return err
	}
	reply.Items = make([]rpcapi.WatchedItem, 0, len(rows))
	for _, r := range rows {
		reply.Items = append(reply.Items, rpcapi.WatchedItem{Kind: r.Kind, Identifier: r.Identifier})
	}
	return nil
}
