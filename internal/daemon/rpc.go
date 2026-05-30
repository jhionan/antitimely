package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/rian/antitimely/internal/domain"
	"github.com/rian/antitimely/internal/macos"
	"github.com/rian/antitimely/internal/rpcapi"
	"github.com/rian/antitimely/internal/store"
)

// AntitimelyService exposes all daemon operations over net/rpc.
type AntitimelyService struct {
	DB                  *sql.DB
	Q                   *store.Queries
	Cache               *Cache
	Bridge              macos.Bridge
	TickIntervalSeconds int
	Perm                *PermissionTracker
	StartedAtUnix       int64 // set when the daemon boots; used to report uptime
}

// rpcHandlerDeadline bounds any single RPC handler. SQLite on a slow filesystem
// or under contention can stall arbitrary queries; without a per-handler cap
// a hung query would pin the connection (which is also the only DB connection
// — see SetMaxOpenConns(1) in openDB).
const rpcHandlerDeadline = 10 * time.Second

// handlerCtx returns a context bounded by rpcHandlerDeadline. Callers must
// defer the returned cancel.
func handlerCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), rpcHandlerDeadline)
}

// asInt64 coerces an interface value returned by sqlc for aggregate columns
// (which can be int64, int32, or float64 depending on the driver) into int64.
// Returns 0 if the value is nil or an unsupported type.
func asInt64(v interface{}) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int32:
		return int64(x)
	case float64:
		return int64(x)
	case nil:
		return 0
	default:
		return 0
	}
}

// Status returns a live snapshot of daemon state.
func (s *AntitimelyService) Status(args rpcapi.StatusArgs, reply *rpcapi.StatusReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	endOfDay := startOfDay + 86400
	tickSec := int64(s.TickIntervalSeconds)

	// --- Legacy fields ---

	todayTotals, err := s.Q.TotalsByProject(ctx, store.TotalsByProjectParams{Ts: startOfDay, Ts_2: endOfDay})
	if err != nil {
		return err
	}
	reply.TodayTotalsSeconds = make(map[string]int64, len(todayTotals))
	for _, r := range todayTotals {
		reply.TodayTotalsSeconds[r.Name] = r.TickCount * tickSec
	}

	unassignedToday, err := s.Q.UnassignedTicksInRange(ctx, store.UnassignedTicksInRangeParams{Ts: startOfDay, Ts_2: endOfDay})
	if err != nil {
		return err
	}
	reply.UnassignedTodaySeconds = unassignedToday * tickSec

	pending, err := s.Q.CountPendingReviewSignatures(ctx)
	if err != nil {
		return err
	}
	reply.UnassignedSignaturesCount = int(pending)

	if idle, err := s.Bridge.IdleSeconds(ctx); err != nil {
		log.Printf("status idle: %v", err)
	} else {
		reply.UserIdleSeconds = idle
	}
	reply.TickIntervalSeconds = s.TickIntervalSeconds
	if s.Perm != nil {
		reply.PermissionState = s.Perm.Get()
	} else {
		reply.PermissionState = "ok"
	}
	if s.StartedAtUnix > 0 {
		reply.DaemonUptimeSeconds = time.Now().Unix() - s.StartedAtUnix
	}

	// --- New grouped fields ---

	// Today's grand total: COUNT(DISTINCT ts) of assigned ticks in the day
	// range. Computed via a dedicated query rather than summing per-project
	// counts because a single ts can produce ticks for multiple projects
	// (e.g. simultaneous focus + agent signals) and summation overcounts
	// those collisions.
	todayAssigned, err := s.Q.AssignedDistinctTicksInRange(ctx, store.AssignedDistinctTicksInRangeParams{Ts: startOfDay, Ts_2: endOfDay})
	if err != nil {
		return err
	}
	reply.TodayTotalSeconds = todayAssigned * tickSec

	// Last invoice timestamps per company_id.
	lastInvoiceRows, err := s.Q.LastInvoicePerCompany(ctx)
	if err != nil {
		return err
	}
	lastInvoice := map[int64]int64{} // company_id -> unix ts
	for _, r := range lastInvoiceRows {
		if r.LastSent != nil {
			lastInvoice[r.CompanyID] = asInt64(r.LastSent)
		}
	}

	// Projects with their company info.
	projRows, err := s.Q.ListProjectsWithCompany(ctx)
	if err != nil {
		return err
	}

	// Per-project today ticks (map project_id -> ticks).
	// TotalsByProjectSince(startOfDay) has no upper bound, but since we're running this
	// during "today", anything since startOfDay is today's ticks.
	todayByProjectID := map[int64]int64{}
	todaySinceRows, err := s.Q.TotalsByProjectSince(ctx, startOfDay)
	if err != nil {
		return err
	}
	for _, r := range todaySinceRows {
		todayByProjectID[r.ProjectID] = r.TickCount
	}

	// Per-project billable ticks (since company's last invoice, or all-time).
	// We need to call this per-project; since scale is small, do it simply.
	// Build a set of unique "since" timestamps needed.
	sinceNeeded := map[int64]bool{0: true}
	for _, pr := range projRows {
		if pr.CompanyID.Valid {
			since := lastInvoice[pr.CompanyID.Int64]
			sinceNeeded[since] = true
		}
	}
	// Fetch billable per project for each since value.
	billableByProjAndSince := map[int64]map[int64]int64{} // since -> project_id -> ticks
	for since := range sinceNeeded {
		rows, err := s.Q.TotalsByProjectSince(ctx, since)
		if err != nil {
			return err
		}
		m := map[int64]int64{}
		for _, r := range rows {
			m[r.ProjectID] = r.TickCount
		}
		billableByProjAndSince[since] = m
	}

	// Group projects under companies.
	type compKey struct {
		id   int64
		name string
	}
	compMap := map[compKey]*rpcapi.CompanyTotals{}
	var noCompany *rpcapi.CompanyTotals

	suppressedByProject := s.Cache.SuppressedAll()

	for _, pr := range projRows {
		since := int64(0)
		lastInvUnix := int64(0)
		if pr.CompanyID.Valid {
			lastInvUnix = lastInvoice[pr.CompanyID.Int64]
			since = lastInvUnix
		}

		billableTicks := billableByProjAndSince[since][pr.ID]
		todayTicks := todayByProjectID[pr.ID]
		isPaused := pr.Paused != 0

		pt := rpcapi.ProjectTotals{
			Name:              pr.Name,
			BillableSeconds:   billableTicks * tickSec,
			TodaySeconds:      todayTicks * tickSec,
			Paused:            isPaused,
			Armed:             s.Cache.Snapshot().ArmedProjects[pr.ID],
			SuppressedSeconds: int64(suppressedByProject[pr.ID]) * tickSec,
		}

		if !pr.CompanyID.Valid {
			if noCompany == nil {
				noCompany = &rpcapi.CompanyTotals{Name: "(no company)"}
			}
			noCompany.Projects = append(noCompany.Projects, pt)
			noCompany.BillableSeconds += pt.BillableSeconds
			noCompany.TodaySeconds += pt.TodaySeconds
		} else {
			ck := compKey{id: pr.CompanyID.Int64, name: pr.CompanyName.String}
			ct := compMap[ck]
			if ct == nil {
				ct = &rpcapi.CompanyTotals{
					Name:            pr.CompanyName.String,
					LastInvoiceUnix: lastInvUnix,
				}
				compMap[ck] = ct
			}
			ct.Projects = append(ct.Projects, pt)
			ct.BillableSeconds += pt.BillableSeconds
			ct.TodaySeconds += pt.TodaySeconds
		}
	}

	// Sort companies by name.
	compKeys := make([]compKey, 0, len(compMap))
	for k := range compMap {
		compKeys = append(compKeys, k)
	}
	sort.Slice(compKeys, func(i, j int) bool {
		return compKeys[i].name < compKeys[j].name
	})
	for _, k := range compKeys {
		reply.Companies = append(reply.Companies, *compMap[k])
	}
	if noCompany != nil {
		reply.Companies = append(reply.Companies, *noCompany)
	}

	// Unassigned all-time.
	unassignedAll, err := s.Q.UnassignedTicksAllTime(ctx)
	if err != nil {
		return err
	}
	reply.UnassignedBillableSeconds = unassignedAll * tickSec

	return nil
}

// ReloadCache rebuilds the cache snapshot from the DB and atomically swaps it.
// Called on daemon startup and after every mutation handler.
func (s *AntitimelyService) ReloadCache() error {
	ctx, cancel := handlerCtx()
	defer cancel()
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
	pausedIDs, err := s.Q.ListPausedProjectIDs(ctx)
	if err != nil {
		return err
	}
	snap.PausedProjectIDs = make(map[int64]bool, len(pausedIDs))
	for _, id := range pausedIDs {
		snap.PausedProjectIDs[id] = true
	}
	// Distinct, trailing-slash-normalized cwd prefixes for the agent pipeline's
	// directory-widening fast check.
	seenPrefix := map[string]struct{}{}
	for _, r := range snap.Rules {
		if r.MatchCwdPrefix == nil {
			continue
		}
		p := strings.TrimRight(*r.MatchCwdPrefix, "/")
		if p == "" {
			continue
		}
		if _, ok := seenPrefix[p]; ok {
			continue
		}
		seenPrefix[p] = struct{}{}
		snap.CwdPrefixes = append(snap.CwdPrefixes, p)
	}
	s.Cache.StorePreservingRuntime(snap)
	return nil
}

var errBadKind = errors.New("kind must be 'bundle' or 'binary'")

// WatchAdd adds a program to the allowlist and refreshes the cache.
func (s *AntitimelyService) WatchAdd(args rpcapi.WatchAddArgs, reply *rpcapi.WatchAddReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
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
	ctx, cancel := handlerCtx()
	defer cancel()
	if err := s.Q.RemoveWatchedProgram(ctx, store.RemoveWatchedProgramParams{
		Kind: args.Kind, Identifier: args.Identifier,
	}); err != nil {
		return err
	}
	return s.ReloadCache()
}

// WatchList returns all currently watched programs.
func (s *AntitimelyService) WatchList(args rpcapi.WatchListArgs, reply *rpcapi.WatchListReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
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
	ctx, cancel := handlerCtx()
	defer cancel()
	var companyID sql.NullInt64
	if args.CompanyName != "" {
		c, err := s.Q.GetCompanyByName(ctx, args.CompanyName)
		if err != nil {
			return fmt.Errorf("company %q not found: %w", args.CompanyName, err)
		}
		companyID = sql.NullInt64{Int64: c.ID, Valid: true}
	}
	id, err := s.Q.AddProject(ctx, store.AddProjectParams{
		Name:      args.Name,
		CompanyID: companyID,
		CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		return err
	}
	s.Cache.ArmProject(id)
	reply.ID = id
	return nil
}

// ProjectList returns all projects ordered by name.
func (s *AntitimelyService) ProjectList(args rpcapi.ProjectListArgs, reply *rpcapi.ProjectListReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	rows, err := s.Q.ListProjects(ctx)
	if err != nil {
		return err
	}
	reply.Items = make([]rpcapi.Project, 0, len(rows))
	for _, r := range rows {
		reply.Items = append(reply.Items, rpcapi.Project{
			ID:          r.ID,
			Name:        r.Name,
			CompanyName: r.CompanyName.String, // zero value of NullString is ""
			Paused:      r.Paused != 0,
		})
	}
	return nil
}

// ProjectDelete removes a project by name and refreshes the cache.
func (s *AntitimelyService) ProjectDelete(args rpcapi.ProjectDeleteArgs, reply *rpcapi.ProjectDeleteReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	if err := s.Q.DeleteProjectByName(ctx, args.Name); err != nil {
		return err
	}
	return s.ReloadCache()
}

// ProjectPause marks a project as paused so the pipeline does not credit it
// any ticks even when its rules match. Calling pause on an already-paused
// project is a no-op success.
func (s *AntitimelyService) ProjectPause(args rpcapi.ProjectPauseArgs, reply *rpcapi.ProjectPauseReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	if err := s.Q.SetProjectPaused(ctx, store.SetProjectPausedParams{
		Paused: 1, Name: args.Name,
	}); err != nil {
		return err
	}
	return s.ReloadCache()
}

// ProjectResume clears the paused flag on a project so the pipeline resumes
// crediting ticks. Calling resume on an already-active project is a no-op
// success.
func (s *AntitimelyService) ProjectResume(args rpcapi.ProjectResumeArgs, reply *rpcapi.ProjectResumeReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	if err := s.Q.SetProjectPaused(ctx, store.SetProjectPausedParams{
		Paused: 0, Name: args.Name,
	}); err != nil {
		return err
	}
	return s.ReloadCache()
}

// ProjectPauseAll marks every project as paused. The reply.Count is the number
// of rows the UPDATE touched (i.e., total project count) — not the number that
// changed state, because SQLite's RowsAffected for UPDATE returns matched, not
// modified, rows.
func (s *AntitimelyService) ProjectPauseAll(args rpcapi.ProjectPauseAllArgs, reply *rpcapi.ProjectPauseAllReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	n, err := s.Q.PauseAllProjects(ctx)
	if err != nil {
		return err
	}
	reply.Count = n
	return s.ReloadCache()
}

// ProjectResumeAll clears the paused flag on every project.
func (s *AntitimelyService) ProjectResumeAll(args rpcapi.ProjectResumeAllArgs, reply *rpcapi.ProjectResumeAllReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	n, err := s.Q.ResumeAllProjects(ctx)
	if err != nil {
		return err
	}
	reply.Count = n
	if err := s.ReloadCache(); err != nil {
		return err
	}
	rows, err := s.Q.ListProjects(ctx)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	s.Cache.ArmAllProjects(ids)
	return nil
}

// PendingReview returns observations that have unassigned ticks and need tagging.
func (s *AntitimelyService) PendingReview(args rpcapi.PendingReviewArgs, reply *rpcapi.PendingReviewReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
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
		lastSeen := asInt64(r.LastSeen)
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
//
// When a rule is created, the rule insert + retroactive retag run inside
// a single transaction: if either fails, neither commits. Without this,
// a crash between the two would leave an orphan rule with no historical
// ticks attributed, or — worse — a concurrent poll tick arriving between
// the two steps could insert new matching ticks that the retag would miss.
func (s *AntitimelyService) TagSignature(args rpcapi.TagSignatureArgs, reply *rpcapi.TagSignatureReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
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
		s.Cache.ArmProject(id)
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

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	qtx := s.Q.WithTx(tx)

	rid, err := qtx.AddRule(ctx, store.AddRuleParams{
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

	// Build params for ApplyRuleRetroactivelyCounted. sqlc generated
	// Column2/Column4/Column6/Column8 as the IS NULL sentinels because the
	// query uses bare "?" placeholders for the null checks; pass the
	// NullString itself for those (nil when not valid) and the plain string
	// for the equality operand.
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

	count, err := qtx.ApplyRuleRetroactivelyCounted(ctx, store.ApplyRuleRetroactivelyCountedParams{
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

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true

	reply.RuleCreated = true
	reply.RuleID = rid
	reply.TicksRetagged = count
	return s.ReloadCache()
}

// IgnoreSignature marks an observation so it won't appear in the review queue.
func (s *AntitimelyService) IgnoreSignature(args rpcapi.IgnoreSignatureArgs, reply *rpcapi.IgnoreSignatureReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	return s.Q.IgnoreObservation(ctx, store.IgnoreObservationParams{
		ObservationID: args.ObservationID,
		IgnoredAt:     time.Now().Unix(),
	})
}

// RulesList returns all rules joined with their project name.
func (s *AntitimelyService) RulesList(args rpcapi.RulesListArgs, reply *rpcapi.RulesListReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
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
	ctx, cancel := handlerCtx()
	defer cancel()
	if err := s.Q.DeleteRule(ctx, args.ID); err != nil {
		return err
	}
	return s.ReloadCache()
}

// Report returns per-project tick totals and unassigned tick count for a time range.
func (s *AntitimelyService) Report(args rpcapi.ReportArgs, reply *rpcapi.ReportReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
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

// Summary returns per-project tracked hours grouped by company for an arbitrary
// date range, along with unassigned seconds and the cwd prefixes known for each
// project (derived from its match_cwd_prefix rules).
func (s *AntitimelyService) Summary(args rpcapi.SummaryArgs, reply *rpcapi.SummaryReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	tickSec := int64(s.TickIntervalSeconds)

	// Per-project tick totals in range.
	totalRows, err := s.Q.TotalsByProject(ctx, store.TotalsByProjectParams{Ts: args.FromUnix, Ts_2: args.ToUnix})
	if err != nil {
		return err
	}
	secondsByName := map[string]int64{}
	for _, r := range totalRows {
		secondsByName[r.Name] = r.TickCount * tickSec
	}

	// Projects with company info.
	projRows, err := s.Q.ListProjectsWithCompany(ctx)
	if err != nil {
		return err
	}

	// Collect cwd prefixes per project ID from rules.
	ruleRows, err := s.Q.ListRulesForCache(ctx)
	if err != nil {
		return err
	}
	cwdByProjectID := map[int64][]string{}
	seen := map[int64]map[string]bool{}
	for _, r := range ruleRows {
		if !r.MatchCwdPrefix.Valid || r.MatchCwdPrefix.String == "" {
			continue
		}
		if seen[r.ProjectID] == nil {
			seen[r.ProjectID] = map[string]bool{}
		}
		prefix := r.MatchCwdPrefix.String
		if !seen[r.ProjectID][prefix] {
			seen[r.ProjectID][prefix] = true
			cwdByProjectID[r.ProjectID] = append(cwdByProjectID[r.ProjectID], prefix)
		}
	}

	// Group projects by company, applying optional filters.
	type compKey struct {
		id   int64
		name string
	}
	compMap := map[compKey]*rpcapi.SummaryCompany{}
	var noCompany *rpcapi.SummaryCompany

	for _, pr := range projRows {
		// Optional project / company filters.
		if args.ProjectName != "" && pr.Name != args.ProjectName {
			continue
		}
		if args.CompanyName != "" {
			if !pr.CompanyID.Valid || pr.CompanyName.String != args.CompanyName {
				continue
			}
		}

		sp := rpcapi.SummaryProject{
			Name:        pr.Name,
			Seconds:     secondsByName[pr.Name],
			CwdPrefixes: cwdByProjectID[pr.ID],
		}

		if !pr.CompanyID.Valid {
			if noCompany == nil {
				noCompany = &rpcapi.SummaryCompany{Name: "(unassigned)"}
			}
			noCompany.Projects = append(noCompany.Projects, sp)
		} else {
			ck := compKey{id: pr.CompanyID.Int64, name: pr.CompanyName.String}
			ct := compMap[ck]
			if ct == nil {
				ct = &rpcapi.SummaryCompany{Name: pr.CompanyName.String}
				compMap[ck] = ct
			}
			ct.Projects = append(ct.Projects, sp)
		}
	}

	// Sort companies by name.
	compKeys := make([]compKey, 0, len(compMap))
	for k := range compMap {
		compKeys = append(compKeys, k)
	}
	sort.Slice(compKeys, func(i, j int) bool {
		return compKeys[i].name < compKeys[j].name
	})
	for _, k := range compKeys {
		reply.Companies = append(reply.Companies, *compMap[k])
	}
	if noCompany != nil {
		reply.Companies = append(reply.Companies, *noCompany)
	}

	// Unassigned ticks in range.
	unassigned, err := s.Q.UnassignedTicksInRange(ctx, store.UnassignedTicksInRangeParams{Ts: args.FromUnix, Ts_2: args.ToUnix})
	if err != nil {
		return err
	}
	reply.UnassignedSeconds = unassigned * tickSec
	return nil
}

// CompanyAdd creates a new company and returns its ID.
func (s *AntitimelyService) CompanyAdd(args rpcapi.CompanyAddArgs, reply *rpcapi.CompanyAddReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	id, err := s.Q.AddCompany(ctx, store.AddCompanyParams{
		Name: args.Name, CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		return err
	}
	reply.ID = id
	return nil
}

// CompanyList returns all companies ordered by name.
func (s *AntitimelyService) CompanyList(args rpcapi.CompanyListArgs, reply *rpcapi.CompanyListReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	rows, err := s.Q.ListCompanies(ctx)
	if err != nil {
		return err
	}
	reply.Items = make([]rpcapi.Company, 0, len(rows))
	for _, r := range rows {
		reply.Items = append(reply.Items, rpcapi.Company{ID: r.ID, Name: r.Name})
	}
	return nil
}

// CompanyDelete removes a company by name.
func (s *AntitimelyService) CompanyDelete(args rpcapi.CompanyDeleteArgs, reply *rpcapi.CompanyDeleteReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	return s.Q.DeleteCompanyByName(ctx, args.Name)
}

// ProjectSetCompany assigns or unassigns a company from a project.
func (s *AntitimelyService) ProjectSetCompany(args rpcapi.ProjectSetCompanyArgs, reply *rpcapi.ProjectSetCompanyReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	var companyID sql.NullInt64
	if args.CompanyName != "" {
		c, err := s.Q.GetCompanyByName(ctx, args.CompanyName)
		if err != nil {
			return fmt.Errorf("company %q not found: %w", args.CompanyName, err)
		}
		companyID = sql.NullInt64{Int64: c.ID, Valid: true}
	}
	return s.Q.SetProjectCompany(ctx, store.SetProjectCompanyParams{
		CompanyID: companyID,
		Name:      args.ProjectName,
	})
}

// InvoiceSend records that an invoice was sent to a company.
func (s *AntitimelyService) InvoiceSend(args rpcapi.InvoiceSendArgs, reply *rpcapi.InvoiceSendReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	c, err := s.Q.GetCompanyByName(ctx, args.CompanyName)
	if err != nil {
		return fmt.Errorf("company %q not found: %w", args.CompanyName, err)
	}
	sentAt := args.SentAtUnix
	if sentAt == 0 {
		sentAt = time.Now().Unix()
	}
	id, err := s.Q.AddInvoice(ctx, store.AddInvoiceParams{
		CompanyID: c.ID,
		SentAt:    sentAt,
		Note:      args.Note,
		CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		return err
	}
	reply.ID = id
	return nil
}

// InvoiceList returns invoices, optionally filtered to a single company.
func (s *AntitimelyService) InvoiceList(args rpcapi.InvoiceListArgs, reply *rpcapi.InvoiceListReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	if args.CompanyName == "" {
		rows, err := s.Q.ListAllInvoices(ctx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			reply.Items = append(reply.Items, rpcapi.InvoiceEntry{
				ID: r.ID, CompanyName: r.CompanyName, SentAtUnix: r.SentAt, Note: r.Note,
			})
		}
		return nil
	}
	c, err := s.Q.GetCompanyByName(ctx, args.CompanyName)
	if err != nil {
		return fmt.Errorf("company %q not found: %w", args.CompanyName, err)
	}
	rows, err := s.Q.ListInvoicesByCompany(ctx, c.ID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		reply.Items = append(reply.Items, rpcapi.InvoiceEntry{
			ID: r.ID, CompanyName: r.CompanyName, SentAtUnix: r.SentAt, Note: r.Note,
		})
	}
	return nil
}

// InvoiceDelete removes an invoice by ID.
func (s *AntitimelyService) InvoiceDelete(args rpcapi.InvoiceDeleteArgs, reply *rpcapi.InvoiceDeleteReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	return s.Q.DeleteInvoice(ctx, args.ID)
}

// SetCompanyBilling updates a company's billing configuration.
func (s *AntitimelyService) SetCompanyBilling(args rpcapi.SetCompanyBillingArgs, reply *rpcapi.SetCompanyBillingReply) error {
	ctx, cancel := handlerCtx()
	defer cancel()
	return s.Q.SetCompanyBilling(ctx, store.SetCompanyBillingParams{
		Name:        args.Name,
		BillingMode: args.BillingMode,
		Currency:    nullStr(args.Currency),
		RateCents:   nullInt(args.RateCents),
		BilledFrom:  nullStr(args.BilledFrom),
	})
}

// nullStr converts a non-empty string to a valid NullString; empty -> invalid.
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// nullInt returns sql.NullInt64 with Valid=true iff n != 0.
func nullInt(n int64) sql.NullInt64 {
	if n == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: n, Valid: true}
}
