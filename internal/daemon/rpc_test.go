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
