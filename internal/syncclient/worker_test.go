package syncclient_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/rian/antitimely/internal/syncapi/v1"
	"github.com/rian/antitimely/internal/syncapi/v1/syncv1connect"
	"github.com/rian/antitimely/internal/syncclient"
	"github.com/rian/antitimely/internal/syncserver"
)

func TestOutageReplayAndProjectEdits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := t.TempDir()
	remote, err := syncserver.Open(ctx, filepath.Join(dir, "remote.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	svc := syncserver.New(remote)
	token, err := svc.CreateAccount(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	handler := svc.Handler()
	var mode atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != syncv1connect.SyncServicePushProcedure || mode.Load() == 0 {
			handler.ServeHTTP(w, r)
			return
		}
		if mode.Load() == 2 {
			// Commit the write, then drop the acknowledgement.
			handler.ServeHTTP(httptest.NewRecorder(), r)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := syncclient.NewClient(server.URL)
	device, err := client.RegisterDevice(ctx, syncclient.Authorize(connect.NewRequest(&v1.RegisterDeviceRequest{Name: "laptop"}), token))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := json.Marshal(syncclient.DeviceCredential{DeviceID: device.Msg.DeviceId, Token: device.Msg.Token})
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(dir, "device.json")
	if err := os.WriteFile(tokenFile, credential, 0o600); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(dir, "local.sqlite")
	openLocal := func() *sql.DB {
		t.Helper()
		db, err := sql.Open("sqlite", filename)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		return db
	}
	db := openLocal()
	defer func() { db.Close() }()
	schema, err := os.ReadFile("../../schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	exec(string(schema))
	exec(`INSERT INTO projects (id, name, created_at) VALUES (1,'Test',1);
		INSERT INTO observations (id, source, cwd, window_title, first_seen) VALUES
			(1, 'focus', 'PRIVATE DIRECTORY', 'PRIVATE TITLE', 1),
			(2, 'agent', 'PRIVATE DIRECTORY', 'PRIVATE TITLE', 1);
		INSERT INTO ticks VALUES (90,1,1)`)
	cfg := syncclient.Config{Endpoint: server.URL, TokenFile: tokenFile}
	worker, err := syncclient.Configure(ctx, db, cfg, 5)
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT OR IGNORE INTO ticks VALUES (100,1,1), (100,1,1), (100,2,1), (105,1,NULL)`)
	mode.Store(1)
	if _, err := worker.Flush(ctx); err == nil {
		t.Fatal("outage should fail")
	}
	exec(`INSERT INTO ticks VALUES (110,1,1); UPDATE ticks SET project_id = 1 WHERE ts = 105`)
	mode.Store(2)
	if _, err := worker.Flush(ctx); err == nil {
		t.Fatal("lost acknowledgement should fail")
	}
	assertTotals := func(seconds int64) {
		t.Helper()
		res, err := client.Totals(ctx, syncclient.Authorize(connect.NewRequest(&v1.TotalsRequest{StartUnix: 1, EndUnix: 200}), token))
		if err != nil || res.Msg.TotalSeconds != seconds || len(res.Msg.Projects) != 1 || res.Msg.Projects[0].Seconds != seconds {
			t.Fatalf("want %ds, got %v (%v)", seconds, res, err)
		}
	}
	assertTotals(15)
	db.Close()
	db = openLocal()
	worker, err = syncclient.Configure(ctx, db, cfg, 2)
	if err != nil {
		t.Fatal(err)
	}
	mode.Store(0)
	if n, err := worker.Flush(ctx); err != nil || n != 5 {
		t.Fatalf("replayed %d records, err %v", n, err)
	}
	assertTotals(15)
	exec(`UPDATE projects SET name = 'Renamed' WHERE id = 1; DELETE FROM ticks WHERE ts = 105;
		UPDATE ticks SET project_id = NULL WHERE ts = 110; INSERT INTO ticks VALUES (117,1,1)`)
	if n, err := worker.Flush(ctx); err != nil || n != 4 {
		t.Fatalf("flushed %d edits, err %v", n, err)
	}
	assertTotals(7)
	projects, err := client.ListProjects(ctx, syncclient.Authorize(connect.NewRequest(&v1.ListProjectsRequest{}), token))
	if err != nil || len(projects.Msg.Projects) != 1 || projects.Msg.Projects[0].Name != "Renamed" {
		t.Fatalf("unexpected projects %v (%v)", projects, err)
	}
	var private int
	if err := remote.QueryRowContext(ctx, `SELECT count(*) FROM records WHERE name LIKE '%PRIVATE%'`).Scan(&private); err != nil || private != 0 {
		t.Fatalf("raw signal data left the device: %d, %v", private, err)
	}
}

func TestSyncEndpointRequiresTLSOutsideLoopback(t *testing.T) {
	for _, value := range []string{"http://example.com", "http://localhost:9000", "https://u:p@test.example", "https://test.example/?a=1"} {
		if _, err := syncclient.ValidateEndpoint(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	for _, value := range []string{"https://test.example/sync", "http://127.0.0.1:9000/", "http://[::1]:9000"} {
		if _, err := syncclient.ValidateEndpoint(value); err != nil {
			t.Errorf("rejected %q: %v", value, err)
		}
	}
}
