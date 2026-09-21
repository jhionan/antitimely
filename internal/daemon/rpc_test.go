package daemon

import (
	"context"
	"database/sql"
	"net"
	"net/rpc"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/rian/antitimely/internal/macos"
	"github.com/rian/antitimely/internal/rpcapi"
	"github.com/rian/antitimely/internal/store"
)

func setupRPCServer(t *testing.T) (*rpc.Client, *sql.DB, *Cache) {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("../../schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	cache := NewCache()
	svc := &AntitimelyService{
		DB:                  db,
		Q:                   store.New(db),
		Cache:               cache,
		Bridge:              &macos.FakeBridge{},
		TickIntervalSeconds: 5,
	}

	srv := rpc.NewServer()
	if err := srv.RegisterName(rpcapi.ServiceName, svc); err != nil {
		t.Fatal(err)
	}

	clientConn, serverConn := net.Pipe()
	go srv.ServeConn(serverConn)
	t.Cleanup(func() {
		clientConn.Close()
		db.Close()
	})

	return rpc.NewClient(clientConn), db, cache
}

func TestRPC_Status(t *testing.T) {
	client, _, _ := setupRPCServer(t)

	var reply rpcapi.StatusReply
	if err := client.Call(rpcapi.ServiceName+".Status", rpcapi.StatusArgs{}, &reply); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if reply.TickIntervalSeconds != 5 {
		t.Errorf("TickIntervalSeconds = %d, want 5", reply.TickIntervalSeconds)
	}
}

func TestRPC_WatchAddListRemove(t *testing.T) {
	client, _, _ := setupRPCServer(t)

	if err := client.Call(rpcapi.ServiceName+".WatchAdd",
		rpcapi.WatchAddArgs{Kind: "bundle", Identifier: "com.google.antigravity"},
		&rpcapi.WatchAddReply{}); err != nil {
		t.Fatalf("WatchAdd: %v", err)
	}
	if err := client.Call(rpcapi.ServiceName+".WatchAdd",
		rpcapi.WatchAddArgs{Kind: "binary", Identifier: "claude"},
		&rpcapi.WatchAddReply{}); err != nil {
		t.Fatalf("WatchAdd #2: %v", err)
	}

	var list rpcapi.WatchListReply
	if err := client.Call(rpcapi.ServiceName+".WatchList", rpcapi.WatchListArgs{}, &list); err != nil {
		t.Fatalf("WatchList: %v", err)
	}
	if len(list.Items) != 2 {
		t.Errorf("expected 2 watched, got %d", len(list.Items))
	}

	if err := client.Call(rpcapi.ServiceName+".WatchRemove",
		rpcapi.WatchRemoveArgs{Kind: "binary", Identifier: "claude"},
		&rpcapi.WatchRemoveReply{}); err != nil {
		t.Fatalf("WatchRemove: %v", err)
	}

	var listAfterRemove rpcapi.WatchListReply
	_ = client.Call(rpcapi.ServiceName+".WatchList", rpcapi.WatchListArgs{}, &listAfterRemove)
	if len(listAfterRemove.Items) != 1 || listAfterRemove.Items[0].Identifier != "com.google.antigravity" {
		t.Errorf("after remove, got %+v", listAfterRemove.Items)
	}
}

func TestRPC_WatchAdd_InvalidatesCache(t *testing.T) {
	client, _, cache := setupRPCServer(t)

	if err := client.Call(rpcapi.ServiceName+".WatchAdd",
		rpcapi.WatchAddArgs{Kind: "binary", Identifier: "claude"},
		&rpcapi.WatchAddReply{}); err != nil {
		t.Fatal(err)
	}
	if !cache.Snapshot().AllowedBinaries["claude"] {
		t.Errorf("cache not refreshed after WatchAdd")
	}
}

func TestRPC_ProjectsAddListDelete(t *testing.T) {
	client, _, _ := setupRPCServer(t)

	var addReply rpcapi.ProjectAddReply
	if err := client.Call(rpcapi.ServiceName+".ProjectAdd",
		rpcapi.ProjectAddArgs{Name: "foca-api"}, &addReply); err != nil {
		t.Fatal(err)
	}
	if addReply.ID == 0 {
		t.Error("expected non-zero project id")
	}

	var list rpcapi.ProjectListReply
	if err := client.Call(rpcapi.ServiceName+".ProjectList", rpcapi.ProjectListArgs{}, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Name != "foca-api" {
		t.Errorf("got %+v", list.Items)
	}

	if err := client.Call(rpcapi.ServiceName+".ProjectDelete",
		rpcapi.ProjectDeleteArgs{Name: "foca-api"},
		&rpcapi.ProjectDeleteReply{}); err != nil {
		t.Fatal(err)
	}

	var listAfterDelete rpcapi.ProjectListReply
	_ = client.Call(rpcapi.ServiceName+".ProjectList", rpcapi.ProjectListArgs{}, &listAfterDelete)
	if len(listAfterDelete.Items) != 0 {
		t.Errorf("after delete, got %d", len(listAfterDelete.Items))
	}
}

func TestRPC_TagSignature_CreatesRuleAndRetagsTicks(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	ctx := context.Background()

	q := store.New(db)
	projID, _ := q.AddProject(ctx, store.AddProjectParams{Name: "foca-api", CreatedAt: 1000})
	_ = projID
	obsID, _ := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "claude", Cwd: "/Users/rian/work/foca-api/src", FirstSeen: 1000,
	})
	for _, ts := range []int64{2000, 2005, 2010} {
		_ = q.InsertTick(ctx, store.InsertTickParams{Ts: ts, ObservationID: obsID})
	}

	args := rpcapi.TagSignatureArgs{
		ObservationID: obsID,
		ProjectName:   "foca-api",
		Rule: &rpcapi.ProposedRule{
			Priority:        100,
			MatchBinaryName: "claude",
			MatchCWDPrefix:  "/Users/rian/work/foca-api/",
		},
	}
	var reply rpcapi.TagSignatureReply
	if err := client.Call(rpcapi.ServiceName+".TagSignature", args, &reply); err != nil {
		t.Fatalf("TagSignature: %v", err)
	}
	if !reply.RuleCreated {
		t.Error("expected RuleCreated=true")
	}
	if reply.TicksRetagged != 3 {
		t.Errorf("TicksRetagged = %d, want 3", reply.TicksRetagged)
	}

	rows, _ := q.TotalsByProject(ctx, store.TotalsByProjectParams{Ts: 0, Ts_2: 9999})
	if len(rows) != 1 || rows[0].TickCount != 3 {
		t.Errorf("totals = %+v", rows)
	}
}

func TestRPC_IgnoreSignature(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	ctx := context.Background()
	q := store.New(db)
	obsID, _ := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "focus", BundleID: "com.spotify.client", FirstSeen: 1000,
	})

	if err := client.Call(rpcapi.ServiceName+".IgnoreSignature",
		rpcapi.IgnoreSignatureArgs{ObservationID: obsID},
		&rpcapi.IgnoreSignatureReply{}); err != nil {
		t.Fatal(err)
	}

	var ignored int64
	row := db.QueryRow(`SELECT COUNT(*) FROM ignored_observations WHERE observation_id = ?`, obsID)
	if err := row.Scan(&ignored); err != nil {
		t.Fatal(err)
	}
	if ignored != 1 {
		t.Errorf("expected ignored=1, got %d", ignored)
	}
}

func TestRPC_RulesListDelete(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	ctx := context.Background()
	q := store.New(db)
	projID, _ := q.AddProject(ctx, store.AddProjectParams{Name: "foca-api", CreatedAt: 1000})

	rid, _ := q.AddRule(ctx, store.AddRuleParams{
		ProjectID: projID, Priority: 100,
		MatchBinaryName: sql.NullString{String: "claude", Valid: true},
		CreatedAt:       1000,
	})

	var list rpcapi.RulesListReply
	if err := client.Call(rpcapi.ServiceName+".RulesList", rpcapi.RulesListArgs{}, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != rid {
		t.Errorf("got %+v", list.Items)
	}

	if err := client.Call(rpcapi.ServiceName+".RuleDelete",
		rpcapi.RuleDeleteArgs{ID: rid}, &rpcapi.RuleDeleteReply{}); err != nil {
		t.Fatal(err)
	}

	var listAfterDelete rpcapi.RulesListReply
	_ = client.Call(rpcapi.ServiceName+".RulesList", rpcapi.RulesListArgs{}, &listAfterDelete)
	if len(listAfterDelete.Items) != 0 {
		t.Errorf("after delete: %d items", len(listAfterDelete.Items))
	}
}

// TestRPC_RuleAdd covers the RuleAdd handler added for `atl rules add`:
// direct rule creation (unlike TagSignature, not tied to an observation).
func TestRPC_RuleAdd(t *testing.T) {
	client, db, cache := setupRPCServer(t)
	ctx := context.Background()
	q := store.New(db)
	if _, err := q.AddProject(ctx, store.AddProjectParams{Name: "MD-Tracker", CreatedAt: 1000}); err != nil {
		t.Fatal(err)
	}

	// A space-only rule (no cwd/bundle/title/binary) must be accepted: the
	// rules table's CHECK constraint was widened specifically to permit a
	// rule whose only match field is match_space_id, and nothing else in
	// this suite proves that end to end through the RPC layer.
	var reply rpcapi.RuleAddReply
	if err := client.Call(rpcapi.ServiceName+".RuleAdd", rpcapi.RuleAddArgs{
		ProjectName:  "MD-Tracker",
		Priority:     100,
		MatchSpaceID: "wN",
	}, &reply); err != nil {
		t.Fatalf("RuleAdd (space-only): %v", err)
	}
	if reply.ID == 0 {
		t.Fatal("expected a nonzero rule id")
	}

	var storedSpace sql.NullString
	row := db.QueryRow(`SELECT match_space_id FROM rules WHERE id = ?`, reply.ID)
	if err := row.Scan(&storedSpace); err != nil {
		t.Fatal(err)
	}
	if !storedSpace.Valid || storedSpace.String != "wN" {
		t.Errorf("stored match_space_id = %+v, want wN", storedSpace)
	}

	// The new rule must be visible in the cache snapshot immediately, with
	// no SIGHUP / separate ReloadCache call — that's what RuleAdd calling
	// ReloadCache itself before returning is for. Assert against the
	// snapshot, not just the database: that's the behaviour users depend on.
	found := false
	for _, r := range cache.Snapshot().Rules {
		if r.ID == reply.ID {
			found = true
			if r.MatchSpaceID == nil || *r.MatchSpaceID != "wN" {
				t.Errorf("cached rule MatchSpaceID = %v, want wN", r.MatchSpaceID)
			}
		}
	}
	if !found {
		t.Error("new rule not present in cache snapshot right after RuleAdd (ReloadCache did not run)")
	}

	// An unknown project must error and create nothing.
	var badReply rpcapi.RuleAddReply
	err := client.Call(rpcapi.ServiceName+".RuleAdd", rpcapi.RuleAddArgs{
		ProjectName:   "NoSuchProject",
		Priority:      100,
		MatchBundleID: "com.test.foo",
	}, &badReply)
	if err == nil {
		t.Fatalf("expected an error for an unknown project, got nil (id=%d)", badReply.ID)
	}

	var count int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM rules WHERE match_bundle_id = 'com.test.foo'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected no rule created for the unknown project, found %d", count)
	}
}

func TestRPC_Report(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	ctx := context.Background()
	q := store.New(db)
	projID, _ := q.AddProject(ctx, store.AddProjectParams{Name: "foca-api", CreatedAt: 0})
	obsID, _ := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "claude", Cwd: "/x", FirstSeen: 0,
	})
	for _, ts := range []int64{100, 105, 110} {
		_ = q.InsertTick(ctx, store.InsertTickParams{Ts: ts, ObservationID: obsID,
			ProjectID: sql.NullInt64{Int64: projID, Valid: true}})
	}
	for _, ts := range []int64{200, 205} {
		_ = q.InsertTick(ctx, store.InsertTickParams{Ts: ts, ObservationID: obsID})
	}

	var rep rpcapi.ReportReply
	args := rpcapi.ReportArgs{FromUnix: 0, ToUnix: 9999}
	if err := client.Call(rpcapi.ServiceName+".Report", args, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Totals["foca-api"] != 15 { // 3 ticks * 5s
		t.Errorf("foca-api total = %d, want 15", rep.Totals["foca-api"])
	}
	if rep.Unassigned != 10 { // 2 ticks * 5s
		t.Errorf("Unassigned = %d, want 10", rep.Unassigned)
	}

	// Company dedup: two AlphaCo projects sharing seconds bill that second
	// once per company; the company-less foca-api rolls into "(no company)",
	// sorted last (Status's convention).
	coID, _ := q.AddCompany(ctx, store.AddCompanyParams{Name: "AlphaCo", CreatedAt: 0})
	alpha1 := id(t, db, `INSERT INTO projects (name, company_id, created_at) VALUES ('alpha-1', ?, 0) RETURNING id`, coID)
	alpha2 := id(t, db, `INSERT INTO projects (name, company_id, created_at) VALUES ('alpha-2', ?, 0) RETURNING id`, coID)
	obsA, _ := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "claude", Cwd: "/a", FirstSeen: 0,
	})
	obsB, _ := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "claude", Cwd: "/b", FirstSeen: 0,
	})
	for _, tick := range []struct{ ts, obs, proj int64 }{
		{100, obsA, alpha1}, {100, obsB, alpha2}, // shared second across AlphaCo projects
		{105, obsB, alpha2},
		{400, obsA, alpha1},
	} {
		_ = q.InsertTick(ctx, store.InsertTickParams{Ts: tick.ts, ObservationID: tick.obs,
			ProjectID: sql.NullInt64{Int64: tick.proj, Valid: true}})
	}

	var rep2 rpcapi.ReportReply
	if err := client.Call(rpcapi.ServiceName+".Report", args, &rep2); err != nil {
		t.Fatal(err)
	}
	// AlphaCo: distinct ts {100, 105, 400} = 3 * 5s. "(no company)": foca-api's
	// distinct ts {100, 105, 110} = 3 * 5s; ts 100/105 are shared with AlphaCo
	// work but billed per company, so each company counts its own.
	if len(rep2.Companies) != 2 ||
		rep2.Companies[0].Name != "AlphaCo" || rep2.Companies[0].BillableSeconds != 15 ||
		rep2.Companies[1].Name != "(no company)" || rep2.Companies[1].BillableSeconds != 15 {
		t.Errorf("Companies = %+v, want [AlphaCo 15 (no company) 15]", rep2.Companies)
	}
}

func id(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestRPC_InvoiceSendListDelete(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	ctx := context.Background()
	q := store.New(db)

	// Create two companies.
	coAID, _ := q.AddCompany(ctx, store.AddCompanyParams{Name: "Acme", CreatedAt: 1000})
	coBID, _ := q.AddCompany(ctx, store.AddCompanyParams{Name: "BetaCorp", CreatedAt: 1000})

	// Send an invoice for Acme.
	var sendReply rpcapi.InvoiceSendReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceSend",
		rpcapi.InvoiceSendArgs{CompanyName: "Acme", SentAtUnix: 2000, Note: "May invoice"},
		&sendReply); err != nil {
		t.Fatalf("InvoiceSend: %v", err)
	}
	if sendReply.ID == 0 {
		t.Error("expected non-zero invoice id")
	}

	// Send another invoice for Acme.
	var sendReply2 rpcapi.InvoiceSendReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceSend",
		rpcapi.InvoiceSendArgs{CompanyName: "Acme", SentAtUnix: 3000, Note: "June invoice"},
		&sendReply2); err != nil {
		t.Fatalf("InvoiceSend #2: %v", err)
	}

	// Send invoice for BetaCorp.
	var sendReply3 rpcapi.InvoiceSendReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceSend",
		rpcapi.InvoiceSendArgs{CompanyName: "BetaCorp", SentAtUnix: 2500},
		&sendReply3); err != nil {
		t.Fatalf("InvoiceSend BetaCorp: %v", err)
	}

	// List all invoices.
	var listAll rpcapi.InvoiceListReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceList",
		rpcapi.InvoiceListArgs{}, &listAll); err != nil {
		t.Fatal(err)
	}
	if len(listAll.Items) != 3 {
		t.Errorf("expected 3 invoices, got %d", len(listAll.Items))
	}

	// List Acme only.
	var listAcme rpcapi.InvoiceListReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceList",
		rpcapi.InvoiceListArgs{CompanyName: "Acme"}, &listAcme); err != nil {
		t.Fatal(err)
	}
	if len(listAcme.Items) != 2 {
		t.Errorf("expected 2 Acme invoices, got %d", len(listAcme.Items))
	}
	// Should be ordered sent_at DESC, so June (3000) first.
	if listAcme.Items[0].Note != "June invoice" {
		t.Errorf("first item should be June invoice, got %q", listAcme.Items[0].Note)
	}

	// List unknown company returns error.
	var listNone rpcapi.InvoiceListReply
	err := client.Call(rpcapi.ServiceName+".InvoiceList",
		rpcapi.InvoiceListArgs{CompanyName: "NoSuchCo"}, &listNone)
	if err == nil {
		t.Error("expected error for unknown company, got nil")
	}

	// Delete an invoice.
	if err := client.Call(rpcapi.ServiceName+".InvoiceDelete",
		rpcapi.InvoiceDeleteArgs{ID: sendReply.ID}, &rpcapi.InvoiceDeleteReply{}); err != nil {
		t.Fatalf("InvoiceDelete: %v", err)
	}

	var listAfterDelete rpcapi.InvoiceListReply
	_ = client.Call(rpcapi.ServiceName+".InvoiceList",
		rpcapi.InvoiceListArgs{CompanyName: "Acme"}, &listAfterDelete)
	if len(listAfterDelete.Items) != 1 {
		t.Errorf("expected 1 Acme invoice after delete, got %d", len(listAfterDelete.Items))
	}

	// Suppress "declared and not used" for coBID since it is only used implicitly.
	_ = coAID
	_ = coBID
}

func TestRPC_Status_CompanyGrouping(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	ctx := context.Background()
	q := store.New(db)

	// Setup: 2 companies, 4 projects.
	coAID, _ := q.AddCompany(ctx, store.AddCompanyParams{Name: "AlphaCo", CreatedAt: 1})
	coBID, _ := q.AddCompany(ctx, store.AddCompanyParams{Name: "BetaCo", CreatedAt: 1})

	p1ID, _ := q.AddProject(ctx, store.AddProjectParams{
		Name: "alpha-p1", CompanyID: sql.NullInt64{Int64: coAID, Valid: true}, CreatedAt: 1,
	})
	p2ID, _ := q.AddProject(ctx, store.AddProjectParams{
		Name: "alpha-p2", CompanyID: sql.NullInt64{Int64: coAID, Valid: true}, CreatedAt: 1,
	})
	p3ID, _ := q.AddProject(ctx, store.AddProjectParams{
		Name: "beta-p1", CompanyID: sql.NullInt64{Int64: coBID, Valid: true}, CreatedAt: 1,
	})
	_ = p3ID

	// p4 has no company.
	p4ID, _ := q.AddProject(ctx, store.AddProjectParams{
		Name: "orphan", CompanyID: sql.NullInt64{}, CreatedAt: 1,
	})

	// Single observation shared by all ticks (doesn't matter for totals).
	obsID, _ := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "test", FirstSeen: 1,
	})

	// Timestamps:
	//   yesterday = now - 86400  (before the invoice anchor for AlphaCo)
	//   today = now - 60         (within today's window)
	now := int64(1748700000) // fixed fake "now" — we need ticks relative to startOfDay
	// We can't easily control "now" inside the daemon, so instead use
	// small absolute timestamps that will likely be "in the past" from the
	// perspective of the daemon's startOfDay computation. The key thing we test
	// is that after an invoice, only ticks after sentAt count as billable.

	tsYesterday := int64(1000) // old ticks, before invoice anchor
	tsToday := int64(2000)     // newer ticks, after invoice anchor
	_ = now

	// Ticks for alpha-p1: 3 old (pre-invoice) + 2 new (post-invoice).
	for _, ts := range []int64{tsYesterday, tsYesterday + 5, tsYesterday + 10} {
		_ = q.InsertTick(ctx, store.InsertTickParams{
			Ts: ts, ObservationID: obsID,
			ProjectID: sql.NullInt64{Int64: p1ID, Valid: true},
		})
	}
	for _, ts := range []int64{tsToday, tsToday + 5} {
		_ = q.InsertTick(ctx, store.InsertTickParams{
			Ts: ts, ObservationID: obsID,
			ProjectID: sql.NullInt64{Int64: p1ID, Valid: true},
		})
	}

	// Ticks for alpha-p2: 1 old + 1 new.
	_ = q.InsertTick(ctx, store.InsertTickParams{
		Ts: tsYesterday + 1, ObservationID: obsID,
		ProjectID: sql.NullInt64{Int64: p2ID, Valid: true},
	})
	_ = q.InsertTick(ctx, store.InsertTickParams{
		Ts: tsToday + 1, ObservationID: obsID,
		ProjectID: sql.NullInt64{Int64: p2ID, Valid: true},
	})

	// Ticks for orphan project (no company): 2 ticks.
	for _, ts := range []int64{tsYesterday + 2, tsToday + 2} {
		_ = q.InsertTick(ctx, store.InsertTickParams{
			Ts: ts, ObservationID: obsID,
			ProjectID: sql.NullInt64{Int64: p4ID, Valid: true},
		})
	}

	// Unassigned ticks: 2.
	for _, ts := range []int64{tsYesterday + 3, tsToday + 3} {
		_ = q.InsertTick(ctx, store.InsertTickParams{
			Ts: ts, ObservationID: obsID,
		})
	}

	// Invoice for AlphaCo sent at sentAt = tsToday - 1 (so only ticks >= tsToday count as billable).
	sentAt := tsToday - 1
	_, err := q.AddInvoice(ctx, store.AddInvoiceParams{
		CompanyID: coAID, SentAt: sentAt, Note: "test", CreatedAt: sentAt,
	})
	if err != nil {
		t.Fatalf("AddInvoice: %v", err)
	}

	// Call Status.
	var reply rpcapi.StatusReply
	if err := client.Call(rpcapi.ServiceName+".Status", rpcapi.StatusArgs{}, &reply); err != nil {
		t.Fatalf("Status: %v", err)
	}

	// Find AlphaCo in the reply.
	var alphaCo *rpcapi.CompanyTotals
	for i := range reply.Companies {
		if reply.Companies[i].Name == "AlphaCo" {
			alphaCo = &reply.Companies[i]
			break
		}
	}
	if alphaCo == nil {
		t.Fatalf("AlphaCo not found in Companies; got: %+v", reply.Companies)
	}

	// AlphaCo invoice anchor = sentAt; billable = ticks with ts >= sentAt.
	// alpha-p1: tsToday (2000) and tsToday+5 (2005) -> 2 ticks * 5s = 10s
	// alpha-p2: tsToday+1 (2001) -> 1 tick * 5s = 5s
	// Total billable for AlphaCo = 15s.
	if alphaCo.BillableSeconds != 15 {
		t.Errorf("AlphaCo billable = %d, want 15", alphaCo.BillableSeconds)
	}
	if alphaCo.LastInvoiceUnix != sentAt {
		t.Errorf("AlphaCo LastInvoiceUnix = %d, want %d", alphaCo.LastInvoiceUnix, sentAt)
	}

	// Find BetaCo — no invoice, so billable = all ticks for its projects.
	// beta-p1 has no ticks in our setup, billable = 0.
	var betaCo *rpcapi.CompanyTotals
	for i := range reply.Companies {
		if reply.Companies[i].Name == "BetaCo" {
			betaCo = &reply.Companies[i]
			break
		}
	}
	if betaCo == nil {
		t.Fatalf("BetaCo not found in Companies; got: %+v", reply.Companies)
	}
	if betaCo.LastInvoiceUnix != 0 {
		t.Errorf("BetaCo LastInvoiceUnix = %d, want 0", betaCo.LastInvoiceUnix)
	}

	// Unassigned all-time = 2 ticks * 5s = 10s.
	if reply.UnassignedBillableSeconds != 10 {
		t.Errorf("UnassignedBillableSeconds = %d, want 10", reply.UnassignedBillableSeconds)
	}

	// Companies should be ordered alphabetically: AlphaCo, BetaCo, then (no company) last.
	if len(reply.Companies) < 2 {
		t.Fatalf("expected at least 2 companies, got %d", len(reply.Companies))
	}
	if reply.Companies[0].Name != "AlphaCo" {
		t.Errorf("Companies[0].Name = %q, want AlphaCo", reply.Companies[0].Name)
	}
	if reply.Companies[1].Name != "BetaCo" {
		t.Errorf("Companies[1].Name = %q, want BetaCo", reply.Companies[1].Name)
	}
}

// When two projects of the same company tick at the same second (worked
// simultaneously), the company's billable rollup must count that second once —
// not once per project. Each project row still keeps its own count.
func TestRPC_Status_CompanyRollup_DedupsSharedSecond(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	ctx := context.Background()
	q := store.New(db)

	coID, _ := q.AddCompany(ctx, store.AddCompanyParams{Name: "DedupCo", CreatedAt: 1})
	d1, _ := q.AddProject(ctx, store.AddProjectParams{
		Name: "d1", CompanyID: sql.NullInt64{Int64: coID, Valid: true}, CreatedAt: 1,
	})
	d2, _ := q.AddProject(ctx, store.AddProjectParams{
		Name: "d2", CompanyID: sql.NullInt64{Int64: coID, Valid: true}, CreatedAt: 1,
	})
	obs1, _ := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "claude", Cwd: "/work/d1", FirstSeen: 1,
	})
	obs2, _ := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "claude", Cwd: "/work/d2", FirstSeen: 1,
	})

	// No invoice → all-time billable. Old timestamps keep them out of "today".
	// d1: 2000, 2005   d2: 2000 (shared with d1), 2010
	// Distinct company seconds = {2000, 2005, 2010} = 3 → 15s.
	insert := func(ts, obs, proj int64) {
		_ = q.InsertTick(ctx, store.InsertTickParams{
			Ts: ts, ObservationID: obs, ProjectID: sql.NullInt64{Int64: proj, Valid: true},
		})
	}
	insert(2000, obs1, d1)
	insert(2005, obs1, d1)
	insert(2000, obs2, d2)
	insert(2010, obs2, d2)

	var reply rpcapi.StatusReply
	if err := client.Call(rpcapi.ServiceName+".Status", rpcapi.StatusArgs{}, &reply); err != nil {
		t.Fatalf("Status: %v", err)
	}

	var co *rpcapi.CompanyTotals
	for i := range reply.Companies {
		if reply.Companies[i].Name == "DedupCo" {
			co = &reply.Companies[i]
		}
	}
	if co == nil {
		t.Fatalf("DedupCo not found; got %+v", reply.Companies)
	}
	if co.BillableSeconds != 15 {
		t.Errorf("DedupCo billable = %d, want 15 (shared second counted once)", co.BillableSeconds)
	}
	// Per-project rows are informational and keep their own counts: 10s each.
	byName := map[string]int64{}
	for _, p := range co.Projects {
		byName[p.Name] = p.BillableSeconds
	}
	if byName["d1"] != 10 || byName["d2"] != 10 {
		t.Errorf("per-project billable = %v, want d1=10 d2=10", byName)
	}
}

func TestRPC_ProjectPauseResume(t *testing.T) {
	client, _, cache := setupRPCServer(t)

	// Create a project.
	if err := client.Call(rpcapi.ServiceName+".ProjectAdd",
		rpcapi.ProjectAddArgs{Name: "test-pause"}, &rpcapi.ProjectAddReply{}); err != nil {
		t.Fatal(err)
	}

	// Pause it.
	if err := client.Call(rpcapi.ServiceName+".ProjectPause",
		rpcapi.ProjectPauseArgs{Name: "test-pause"}, &rpcapi.ProjectPauseReply{}); err != nil {
		t.Fatal(err)
	}

	// Verify ProjectList shows it as paused, and capture its ID.
	var list rpcapi.ProjectListReply
	if err := client.Call(rpcapi.ServiceName+".ProjectList", rpcapi.ProjectListArgs{}, &list); err != nil {
		t.Fatal(err)
	}
	var projID int64
	var found bool
	for _, p := range list.Items {
		if p.Name == "test-pause" {
			found = true
			projID = p.ID
			if !p.Paused {
				t.Errorf("expected paused=true after ProjectPause, got false")
			}
		}
	}
	if !found {
		t.Fatal("project test-pause not found in list")
	}

	// Verify cache reflects the paused project.
	snap := cache.Snapshot()
	if !snap.PausedProjectIDs[projID] {
		t.Errorf("cache.PausedProjectIDs[%d] = false, want true", projID)
	}

	// Calling pause again on an already-paused project should be a no-op success.
	if err := client.Call(rpcapi.ServiceName+".ProjectPause",
		rpcapi.ProjectPauseArgs{Name: "test-pause"}, &rpcapi.ProjectPauseReply{}); err != nil {
		t.Errorf("second pause should be no-op: %v", err)
	}

	// Resume it.
	if err := client.Call(rpcapi.ServiceName+".ProjectResume",
		rpcapi.ProjectResumeArgs{Name: "test-pause"}, &rpcapi.ProjectResumeReply{}); err != nil {
		t.Fatal(err)
	}

	// Verify ProjectList shows it as active.
	var list2 rpcapi.ProjectListReply
	if err := client.Call(rpcapi.ServiceName+".ProjectList", rpcapi.ProjectListArgs{}, &list2); err != nil {
		t.Fatal(err)
	}
	for _, p := range list2.Items {
		if p.Name == "test-pause" && p.Paused {
			t.Errorf("expected paused=false after ProjectResume, got true")
		}
	}

	// Verify cache no longer has the project in PausedProjectIDs.
	snap2 := cache.Snapshot()
	if snap2.PausedProjectIDs[projID] {
		t.Errorf("cache.PausedProjectIDs[%d] = true after resume, want false", projID)
	}
}

func TestRPC_ProjectPauseAllResumeAll(t *testing.T) {
	client, _, cache := setupRPCServer(t)

	// Pause-all on an empty project set should succeed with Count=0.
	var emptyReply rpcapi.ProjectPauseAllReply
	if err := client.Call(rpcapi.ServiceName+".ProjectPauseAll",
		rpcapi.ProjectPauseAllArgs{}, &emptyReply); err != nil {
		t.Fatalf("PauseAll on empty set: %v", err)
	}
	if emptyReply.Count != 0 {
		t.Errorf("PauseAll empty Count = %d, want 0", emptyReply.Count)
	}

	// Create three projects; pause one up-front so we can verify pause-all
	// treats already-paused as a no-op success without flipping anything.
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		if err := client.Call(rpcapi.ServiceName+".ProjectAdd",
			rpcapi.ProjectAddArgs{Name: name}, &rpcapi.ProjectAddReply{}); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}
	if err := client.Call(rpcapi.ServiceName+".ProjectPause",
		rpcapi.ProjectPauseArgs{Name: "bravo"}, &rpcapi.ProjectPauseReply{}); err != nil {
		t.Fatal(err)
	}

	// PauseAll should touch all 3 rows and end with every project paused.
	var pAll rpcapi.ProjectPauseAllReply
	if err := client.Call(rpcapi.ServiceName+".ProjectPauseAll",
		rpcapi.ProjectPauseAllArgs{}, &pAll); err != nil {
		t.Fatalf("PauseAll: %v", err)
	}
	if pAll.Count != 3 {
		t.Errorf("PauseAll Count = %d, want 3", pAll.Count)
	}

	var listAfterPause rpcapi.ProjectListReply
	if err := client.Call(rpcapi.ServiceName+".ProjectList",
		rpcapi.ProjectListArgs{}, &listAfterPause); err != nil {
		t.Fatal(err)
	}
	if len(listAfterPause.Items) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(listAfterPause.Items))
	}
	pausedIDs := map[int64]bool{}
	for _, p := range listAfterPause.Items {
		if !p.Paused {
			t.Errorf("project %q paused=false after PauseAll", p.Name)
		}
		pausedIDs[p.ID] = true
	}

	// Cache should hold all three IDs as paused.
	snap := cache.Snapshot()
	for id := range pausedIDs {
		if !snap.PausedProjectIDs[id] {
			t.Errorf("cache.PausedProjectIDs[%d] missing after PauseAll", id)
		}
	}

	// ResumeAll clears them all.
	var rAll rpcapi.ProjectResumeAllReply
	if err := client.Call(rpcapi.ServiceName+".ProjectResumeAll",
		rpcapi.ProjectResumeAllArgs{}, &rAll); err != nil {
		t.Fatalf("ResumeAll: %v", err)
	}
	if rAll.Count != 3 {
		t.Errorf("ResumeAll Count = %d, want 3", rAll.Count)
	}

	var listAfterResume rpcapi.ProjectListReply
	if err := client.Call(rpcapi.ServiceName+".ProjectList",
		rpcapi.ProjectListArgs{}, &listAfterResume); err != nil {
		t.Fatal(err)
	}
	for _, p := range listAfterResume.Items {
		if p.Paused {
			t.Errorf("project %q paused=true after ResumeAll", p.Name)
		}
	}

	snap2 := cache.Snapshot()
	if len(snap2.PausedProjectIDs) != 0 {
		t.Errorf("cache.PausedProjectIDs has %d entries after ResumeAll, want 0", len(snap2.PausedProjectIDs))
	}
}

func TestRPC_ProjectResumeAll_ArmsAllProjects(t *testing.T) {
	client, db, cache := setupRPCServer(t)
	q := store.New(db)
	ctx := context.Background()

	for _, name := range []string{"alpha", "beta", "gamma"} {
		if _, err := q.AddProject(ctx, store.AddProjectParams{Name: name, CreatedAt: 1000}); err != nil {
			t.Fatalf("AddProject %s: %v", name, err)
		}
	}

	var reply rpcapi.ProjectResumeAllReply
	if err := client.Call(rpcapi.ServiceName+".ProjectResumeAll",
		rpcapi.ProjectResumeAllArgs{}, &reply); err != nil {
		t.Fatalf("ProjectResumeAll: %v", err)
	}

	snap := cache.Snapshot()
	if len(snap.ArmedProjects) != 3 {
		t.Errorf("expected 3 armed projects, got %d (%v)", len(snap.ArmedProjects), snap.ArmedProjects)
	}
}

func TestRPC_ProjectResumeAll_NoProjects_NoPanic(t *testing.T) {
	client, _, cache := setupRPCServer(t)

	var reply rpcapi.ProjectResumeAllReply
	if err := client.Call(rpcapi.ServiceName+".ProjectResumeAll",
		rpcapi.ProjectResumeAllArgs{}, &reply); err != nil {
		t.Fatalf("ProjectResumeAll: %v", err)
	}

	if len(cache.Snapshot().ArmedProjects) != 0 {
		t.Errorf("expected empty arm map, got %v", cache.Snapshot().ArmedProjects)
	}
}

func TestRPC_ProjectResume_Single_DoesNotArm(t *testing.T) {
	client, db, cache := setupRPCServer(t)
	q := store.New(db)
	ctx := context.Background()

	if _, err := q.AddProject(ctx, store.AddProjectParams{Name: "solo", CreatedAt: 1000}); err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	if err := client.Call(rpcapi.ServiceName+".ProjectResume",
		rpcapi.ProjectResumeArgs{Name: "solo"},
		&rpcapi.ProjectResumeReply{}); err != nil {
		t.Fatalf("ProjectResume: %v", err)
	}

	if len(cache.Snapshot().ArmedProjects) != 0 {
		t.Errorf("single resume should not arm anything, got %v", cache.Snapshot().ArmedProjects)
	}
}

func TestRPC_ReloadCache_PreservesArmedProjects(t *testing.T) {
	client, _, cache := setupRPCServer(t)

	// Pre-arm two project IDs.
	cache.ArmAllProjects([]int64{100, 200})

	// Trigger a cache reload (the simplest way from a test is via a no-op
	// watched-program add, which calls ReloadCache).
	if err := client.Call(rpcapi.ServiceName+".WatchAdd",
		rpcapi.WatchAddArgs{Kind: "bundle", Identifier: "com.preserve.test"},
		&rpcapi.WatchAddReply{}); err != nil {
		t.Fatalf("WatchAdd: %v", err)
	}

	snap := cache.Snapshot()
	if !snap.ArmedProjects[100] || !snap.ArmedProjects[200] {
		t.Errorf("ArmedProjects lost across ReloadCache, got %v", snap.ArmedProjects)
	}
}

// TestRPC_ReloadCache_PopulatesSpaceID guards the wiring in ReloadCache that
// turns a rule's stored match_space_id column into both
// domain.RuleSpec.MatchSpaceID (so MatchRules can evaluate the clause) and
// CacheSnapshot.BoundSpaceIDs (so the agent/transcript pipelines can track a
// process in a bound space even without a cwd match). Deleting either half of
// that wiring leaves the rest of the suite green while the whole space
// attribution feature goes silently inert — this is the regression test for
// exactly that failure mode.
func TestRPC_ReloadCache_PopulatesSpaceID(t *testing.T) {
	client, db, cache := setupRPCServer(t)
	ctx := context.Background()
	q := store.New(db)

	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "space-bound", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	const wantSpace = "wN"
	if _, err := q.AddRule(ctx, store.AddRuleParams{
		ProjectID:    projID,
		Priority:     50,
		MatchSpaceID: sql.NullString{String: wantSpace, Valid: true},
		CreatedAt:    1000,
	}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	// Trigger a cache reload (WatchAdd calls ReloadCache as a side effect;
	// same pattern as TestRPC_ReloadCache_PreservesArmedProjects above).
	if err := client.Call(rpcapi.ServiceName+".WatchAdd",
		rpcapi.WatchAddArgs{Kind: "bundle", Identifier: "com.space.test"},
		&rpcapi.WatchAddReply{}); err != nil {
		t.Fatalf("WatchAdd: %v", err)
	}

	snap := cache.Snapshot()

	var found bool
	for _, r := range snap.Rules {
		if r.ProjectID != projID {
			continue
		}
		found = true
		if r.MatchSpaceID == nil || *r.MatchSpaceID != wantSpace {
			t.Errorf("rule.MatchSpaceID = %v, want %q", r.MatchSpaceID, wantSpace)
		}
	}
	if !found {
		t.Fatalf("rule for project %d not found in snapshot.Rules: %+v", projID, snap.Rules)
	}

	if !snap.BoundSpaceIDs[wantSpace] {
		t.Errorf("BoundSpaceIDs = %v, want it to contain %q", snap.BoundSpaceIDs, wantSpace)
	}
}

func TestRPC_ProjectAdd_ArmsNewProject(t *testing.T) {
	client, _, cache := setupRPCServer(t)

	var reply rpcapi.ProjectAddReply
	if err := client.Call(rpcapi.ServiceName+".ProjectAdd",
		rpcapi.ProjectAddArgs{Name: "fresh"},
		&reply); err != nil {
		t.Fatalf("ProjectAdd: %v", err)
	}
	if reply.ID == 0 {
		t.Fatalf("expected non-zero new project id, got 0")
	}
	if !cache.Snapshot().ArmedProjects[reply.ID] {
		t.Errorf("expected new project %d armed, got %v", reply.ID, cache.Snapshot().ArmedProjects)
	}
}

func TestRPC_TagSignature_CreateProject_Arms(t *testing.T) {
	client, db, cache := setupRPCServer(t)
	q := store.New(db)
	ctx := context.Background()

	// Seed a single observation row that the tag will reference.
	obsID, err := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "claude", Cwd: "/Users/rian/work/onfly", FirstSeen: 1000,
	})
	if err != nil {
		t.Fatalf("UpsertObservation: %v", err)
	}

	var reply rpcapi.TagSignatureReply
	if err := client.Call(rpcapi.ServiceName+".TagSignature",
		rpcapi.TagSignatureArgs{
			ProjectName:   "onfly-new",
			ObservationID: obsID,
			CreateProject: true,
			// Rule is nil — just create-project + retag single observation.
		},
		&reply); err != nil {
		t.Fatalf("TagSignature: %v", err)
	}

	// Read back the newly-created project id.
	row := db.QueryRow(`SELECT id FROM projects WHERE name=?`, "onfly-new")
	var newID int64
	if err := row.Scan(&newID); err != nil {
		t.Fatalf("scan new project id: %v", err)
	}
	if !cache.Snapshot().ArmedProjects[newID] {
		t.Errorf("expected create-on-the-fly project %d armed, got %v", newID, cache.Snapshot().ArmedProjects)
	}
}

func TestRPC_LatestTick(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	ctx := context.Background()
	q := store.New(db)

	// No ticks yet → 0; Perm is nil in the test service, and an unwired
	// tracker has probed nothing, so it must not claim a state.
	var empty rpcapi.LatestTickReply
	if err := client.Call(rpcapi.ServiceName+".LatestTick", rpcapi.LatestTickArgs{}, &empty); err != nil {
		t.Fatalf("LatestTick (empty): %v", err)
	}
	if empty.LatestTickUnix != 0 {
		t.Errorf("LatestTickUnix with no ticks = %d, want 0", empty.LatestTickUnix)
	}
	if empty.PermissionState != "unknown" {
		t.Errorf("PermissionState = %q, want \"unknown\"", empty.PermissionState)
	}

	obsID, _ := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "claude", Cwd: "/x", FirstSeen: 1000,
	})
	// Insert out of order; the probe must report the MAX, not the last.
	for _, ts := range []int64{2000, 2010, 2005} {
		_ = q.InsertTick(ctx, store.InsertTickParams{Ts: ts, ObservationID: obsID})
	}

	var reply rpcapi.LatestTickReply
	if err := client.Call(rpcapi.ServiceName+".LatestTick", rpcapi.LatestTickArgs{}, &reply); err != nil {
		t.Fatalf("LatestTick: %v", err)
	}
	if reply.LatestTickUnix != 2010 {
		t.Errorf("LatestTickUnix = %d, want 2010 (max of inserted ts)", reply.LatestTickUnix)
	}
}

// TestRPC_TagSignature_RetroSweepCannotCrossSpaces is the regression test for
// `atl review` being space-blind. Observations fork by space, so two rows
// that look identical in the review list can exist for the same cwd in two
// herdr spaces — which is exactly the case this feature exists to separate
// (two spaces, same working directory, two different clients). Tagging one of
// them creates a cwd-only rule; with the retroactive sweep's space clause
// hard-wired to don't-care, that rule swept BOTH spaces' unassigned ticks
// into one project and silently billed the other client's time to it.
func TestRPC_TagSignature_RetroSweepCannotCrossSpaces(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	ctx := context.Background()
	q := store.New(db)

	const cwd = "/Users/rian/work/shared-repo"
	if _, err := q.AddProject(ctx, store.AddProjectParams{Name: "client-a", CreatedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	obsA, err := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "claude", Cwd: cwd, SpaceID: "wN", FirstSeen: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	obsB, err := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "claude", Cwd: cwd, SpaceID: "wM", FirstSeen: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if obsA == obsB {
		t.Fatal("same cwd in two spaces must be two observations")
	}
	for _, ts := range []int64{2000, 2005} {
		if err := q.InsertTick(ctx, store.InsertTickParams{Ts: ts, ObservationID: obsA}); err != nil {
			t.Fatal(err)
		}
	}
	for _, ts := range []int64{3000, 3005, 3010} {
		if err := q.InsertTick(ctx, store.InsertTickParams{Ts: ts, ObservationID: obsB}); err != nil {
			t.Fatal(err)
		}
	}

	var reply rpcapi.TagSignatureReply
	if err := client.Call(rpcapi.ServiceName+".TagSignature", rpcapi.TagSignatureArgs{
		ObservationID: obsA,
		ProjectName:   "client-a",
		Rule: &rpcapi.ProposedRule{
			Priority:        100,
			MatchBinaryName: "claude",
			MatchCWDPrefix:  cwd,
		},
	}, &reply); err != nil {
		t.Fatalf("TagSignature: %v", err)
	}
	if reply.TicksRetagged != 2 {
		t.Errorf("TicksRetagged = %d, want 2 (only the tagged space's ticks)", reply.TicksRetagged)
	}

	var stillUnassigned int
	row := db.QueryRow(`SELECT COUNT(*) FROM ticks WHERE observation_id = ? AND project_id IS NULL`, obsB)
	if err := row.Scan(&stillUnassigned); err != nil {
		t.Fatal(err)
	}
	if stillUnassigned != 3 {
		t.Fatalf("the other space's ticks must stay unassigned, got %d of 3 still unassigned - "+
			"the retroactive sweep crossed spaces and billed another client's time", stillUnassigned)
	}
}

// TestRPC_TagSignature_SpacelessObservationStillSweepsEverything pins the
// other half of the constraint: work outside herdr has an empty space_id,
// and for those the sweep must stay space-agnostic, exactly as it behaved before
// spaces existed. A too-eager space clause here would quietly stop retagging
// history for every non-herdr signature.
func TestRPC_TagSignature_SpacelessObservationStillSweepsEverything(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	ctx := context.Background()
	q := store.New(db)

	const cwd = "/Users/rian/work/plain-repo"
	if _, err := q.AddProject(ctx, store.AddProjectParams{Name: "plain", CreatedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	obs1, err := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "claude", Cwd: cwd, FirstSeen: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	obs2, err := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "claude", Cwd: cwd + "/sub", FirstSeen: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.InsertTick(ctx, store.InsertTickParams{Ts: 2000, ObservationID: obs1}); err != nil {
		t.Fatal(err)
	}
	if err := q.InsertTick(ctx, store.InsertTickParams{Ts: 2005, ObservationID: obs2}); err != nil {
		t.Fatal(err)
	}

	var reply rpcapi.TagSignatureReply
	if err := client.Call(rpcapi.ServiceName+".TagSignature", rpcapi.TagSignatureArgs{
		ObservationID: obs1,
		ProjectName:   "plain",
		Rule: &rpcapi.ProposedRule{
			Priority:        100,
			MatchBinaryName: "claude",
			MatchCWDPrefix:  cwd,
		},
	}, &reply); err != nil {
		t.Fatalf("TagSignature: %v", err)
	}
	if reply.TicksRetagged != 2 {
		t.Fatalf("TicksRetagged = %d, want 2: a spaceless observation must keep the "+
			"space clause don't-care and retag all matching history", reply.TicksRetagged)
	}
}

// TestRPC_PendingReview_CarriesSpaceID pins the review queue's space column
// end to end: without it two rows for the same cwd in different spaces are
// indistinguishable in `atl review`, and the user cannot tell which client
// they are tagging.
func TestRPC_PendingReview_CarriesSpaceID(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	ctx := context.Background()
	q := store.New(db)

	obsID, err := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "claude", Cwd: "/Users/rian/work/x", SpaceID: "wN", FirstSeen: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.InsertTick(ctx, store.InsertTickParams{Ts: 2000, ObservationID: obsID}); err != nil {
		t.Fatal(err)
	}

	var reply rpcapi.PendingReviewReply
	if err := client.Call(rpcapi.ServiceName+".PendingReview", rpcapi.PendingReviewArgs{Limit: 10}, &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Signatures) != 1 {
		t.Fatalf("want 1 pending signature, got %d", len(reply.Signatures))
	}
	if reply.Signatures[0].SpaceID != "wN" {
		t.Fatalf("SpaceID = %q, want %q", reply.Signatures[0].SpaceID, "wN")
	}
}

// TestRPC_RuleAdd_RejectsInvalidCwdPattern pins that cwd-pattern validation
// lives at the RPC boundary, not only in the CLI: any other caller could
// otherwise store a pattern whose live matcher and retroactive SQL disagree.
func TestRPC_RuleAdd_RejectsInvalidCwdPattern(t *testing.T) {
	client, db, _ := setupRPCServer(t)
	ctx := context.Background()
	q := store.New(db)
	if _, err := q.AddProject(ctx, store.AddProjectParams{Name: "MD-Tracker", CreatedAt: 1000}); err != nil {
		t.Fatal(err)
	}

	var reply rpcapi.RuleAddReply
	err := client.Call(rpcapi.ServiceName+".RuleAdd", rpcapi.RuleAddArgs{
		ProjectName:    "MD-Tracker",
		Priority:       100,
		MatchCWDPrefix: "/a/*/md-x",
	}, &reply)
	if err == nil {
		t.Fatal("RuleAdd must reject a '*' that is not the final character of the pattern")
	}
	if !strings.Contains(err.Error(), "final character") {
		t.Fatalf("expected the ValidateCwdPattern error, got %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rules`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a rejected rule must not be stored, found %d rules", n)
	}
}
