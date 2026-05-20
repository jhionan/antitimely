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
