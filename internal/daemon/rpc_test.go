package daemon

import (
	"context"
	"database/sql"
	"net"
	"net/rpc"
	"os"
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
