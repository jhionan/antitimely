package daemon

import (
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
