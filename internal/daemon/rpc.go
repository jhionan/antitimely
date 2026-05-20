package daemon

import (
	"context"
	"database/sql"
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

// ProjectAdd creates a new project and returns its ID.
func (s *AntitimelyService) ProjectAdd(args rpcapi.ProjectAddArgs, reply *rpcapi.ProjectAddReply) error {
	ctx := context.Background()
	id, err := s.Q.AddProject(ctx, store.AddProjectParams{
		Name: args.Name, CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		return err
	}
	reply.ID = id
	return nil
}

// ProjectList returns all projects ordered by name.
func (s *AntitimelyService) ProjectList(args rpcapi.ProjectListArgs, reply *rpcapi.ProjectListReply) error {
	ctx := context.Background()
	rows, err := s.Q.ListProjects(ctx)
	if err != nil {
		return err
	}
	reply.Items = make([]rpcapi.Project, 0, len(rows))
	for _, r := range rows {
		reply.Items = append(reply.Items, rpcapi.Project{ID: r.ID, Name: r.Name})
	}
	return nil
}

// ProjectDelete removes a project by name and refreshes the cache.
func (s *AntitimelyService) ProjectDelete(args rpcapi.ProjectDeleteArgs, reply *rpcapi.ProjectDeleteReply) error {
	ctx := context.Background()
	if err := s.Q.DeleteProjectByName(ctx, args.Name); err != nil {
		return err
	}
	return s.ReloadCache()
}

// PendingReview returns observations that have unassigned ticks and need tagging.
func (s *AntitimelyService) PendingReview(args rpcapi.PendingReviewArgs, reply *rpcapi.PendingReviewReply) error {
	ctx := context.Background()
	limit := args.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.Q.PendingReviewSignatures(ctx, int64(limit))
	if err != nil {
		return err
	}
	reply.Signatures = make([]rpcapi.Signature, 0, len(rows))
	for _, r := range rows {
		lastSeen, _ := r.LastSeen.(int64)
		reply.Signatures = append(reply.Signatures, rpcapi.Signature{
			ObservationID: r.ID,
			Source:        r.Source,
			BundleID:      r.BundleID,
			WindowTitle:   r.WindowTitle,
			BinaryName:    r.BinaryName,
			CWD:           r.Cwd,
			Ticks:         r.Ticks,
			LastSeenUnix:  lastSeen,
		})
	}
	return nil
}

// TagSignature assigns ticks for an observation to a project, optionally
// creating a rule to retag all matching unassigned ticks retroactively.
func (s *AntitimelyService) TagSignature(args rpcapi.TagSignatureArgs, reply *rpcapi.TagSignatureReply) error {
	ctx := context.Background()
	now := time.Now().Unix()

	proj, err := s.Q.GetProjectByName(ctx, args.ProjectName)
	if err != nil {
		if !args.CreateProject {
			return err
		}
		id, err2 := s.Q.AddProject(ctx, store.AddProjectParams{Name: args.ProjectName, CreatedAt: now})
		if err2 != nil {
			return err2
		}
		proj.ID = id
		proj.Name = args.ProjectName
	}

	if args.Rule == nil {
		if err := s.Q.RetagSingleObservation(ctx, store.RetagSingleObservationParams{
			ProjectID:     sql.NullInt64{Int64: proj.ID, Valid: true},
			ObservationID: args.ObservationID,
		}); err != nil {
			return err
		}
		reply.RuleCreated = false
		reply.TicksRetagged = -1
		return nil
	}

	rid, err := s.Q.AddRule(ctx, store.AddRuleParams{
		ProjectID:        proj.ID,
		Priority:         args.Rule.Priority,
		MatchBundleID:    nullStr(args.Rule.MatchBundleID),
		MatchTitleSubstr: nullStr(args.Rule.MatchTitleSubstr),
		MatchBinaryName:  nullStr(args.Rule.MatchBinaryName),
		MatchCwdPrefix:   nullStr(args.Rule.MatchCWDPrefix),
		CreatedAt:        now,
	})
	if err != nil {
		return err
	}

	// Build params for ApplyRuleRetroactivelyCounted.
	// sqlc generated Column2/Column4/Column6/Column8 as the IS NULL sentinels
	// because the query uses bare "?" placeholders for the null checks.
	// We pass the NullString itself as Column2/4/6/8 (nil when not valid) and
	// the plain string as the equality operand Column3/5/7/9.
	bundleNull := nullStr(args.Rule.MatchBundleID)
	titleNull := nullStr(args.Rule.MatchTitleSubstr)
	binaryNull := nullStr(args.Rule.MatchBinaryName)
	cwdNull := nullStr(args.Rule.MatchCWDPrefix)

	var bundleCol2 interface{}
	if bundleNull.Valid {
		bundleCol2 = bundleNull.String
	}
	var titleCol4 interface{}
	if titleNull.Valid {
		titleCol4 = titleNull.String
	}
	var binaryCol6 interface{}
	if binaryNull.Valid {
		binaryCol6 = binaryNull.String
	}
	var cwdCol8 interface{}
	if cwdNull.Valid {
		cwdCol8 = cwdNull.String
	}

	count, err := s.Q.ApplyRuleRetroactivelyCounted(ctx, store.ApplyRuleRetroactivelyCountedParams{
		ProjectID:  sql.NullInt64{Int64: proj.ID, Valid: true},
		Column2:    bundleCol2,
		BundleID:   args.Rule.MatchBundleID,
		Column4:    titleCol4,
		Column5:    titleNull,
		Column6:    binaryCol6,
		BinaryName: args.Rule.MatchBinaryName,
		Column8:    cwdCol8,
		Column9:    cwdNull,
	})
	if err != nil {
		return err
	}

	reply.RuleCreated = true
	reply.RuleID = rid
	reply.TicksRetagged = count
	return s.ReloadCache()
}

// IgnoreSignature marks an observation so it won't appear in the review queue.
func (s *AntitimelyService) IgnoreSignature(args rpcapi.IgnoreSignatureArgs, reply *rpcapi.IgnoreSignatureReply) error {
	ctx := context.Background()
	return s.Q.IgnoreObservation(ctx, store.IgnoreObservationParams{
		ObservationID: args.ObservationID,
		IgnoredAt:     time.Now().Unix(),
	})
}

// RulesList returns all rules joined with their project name.
func (s *AntitimelyService) RulesList(args rpcapi.RulesListArgs, reply *rpcapi.RulesListReply) error {
	ctx := context.Background()
	rows, err := s.Q.ListRules(ctx)
	if err != nil {
		return err
	}
	reply.Items = make([]rpcapi.Rule, 0, len(rows))
	for _, r := range rows {
		reply.Items = append(reply.Items, rpcapi.Rule{
			ID:               r.ID,
			ProjectName:      r.ProjectName,
			Priority:         r.Priority,
			MatchBundleID:    r.MatchBundleID.String,
			MatchTitleSubstr: r.MatchTitleSubstr.String,
			MatchBinaryName:  r.MatchBinaryName.String,
			MatchCWDPrefix:   r.MatchCwdPrefix.String,
		})
	}
	return nil
}

// RuleDelete removes a rule by ID and refreshes the cache.
func (s *AntitimelyService) RuleDelete(args rpcapi.RuleDeleteArgs, reply *rpcapi.RuleDeleteReply) error {
	ctx := context.Background()
	if err := s.Q.DeleteRule(ctx, args.ID); err != nil {
		return err
	}
	return s.ReloadCache()
}

// Report returns per-project tick totals and unassigned tick count for a time range.
func (s *AntitimelyService) Report(args rpcapi.ReportArgs, reply *rpcapi.ReportReply) error {
	ctx := context.Background()
	rows, err := s.Q.TotalsByProject(ctx, store.TotalsByProjectParams{Ts: args.FromUnix, Ts_2: args.ToUnix})
	if err != nil {
		return err
	}
	reply.Totals = make(map[string]int64, len(rows))
	for _, r := range rows {
		reply.Totals[r.Name] = r.TickCount * int64(s.TickIntervalSeconds)
	}
	unassigned, err := s.Q.UnassignedTicksInRange(ctx, store.UnassignedTicksInRangeParams{Ts: args.FromUnix, Ts_2: args.ToUnix})
	if err != nil {
		return err
	}
	reply.Unassigned = unassigned * int64(s.TickIntervalSeconds)
	return nil
}

// nullStr converts a non-empty string to a valid NullString; empty -> invalid.
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
