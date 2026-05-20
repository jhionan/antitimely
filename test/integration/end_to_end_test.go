package integration_test

import (
	"fmt"
	"net"
	"net/rpc"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
)

func TestEndToEnd_BootedDaemonRespondsToRPC(t *testing.T) {
	dir := t.TempDir()
	// macOS limits Unix socket paths to ~104 chars; use /tmp with a short name
	// instead of the deep t.TempDir() path which can exceed that limit.
	sockDir, err := os.MkdirTemp("/tmp", fmt.Sprintf("at%d", os.Getpid()))
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	socket := filepath.Join(sockDir, "a.sock")
	dbPath := filepath.Join(dir, "db.sqlite")

	// Build the binary into the temp dir to avoid colliding with the dev copy.
	bin := filepath.Join(dir, "antitimely")
	build := exec.Command("go", "build", "-o", bin, "github.com/rian/antitimely")
	build.Dir = projectRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, "daemon",
		"--socket="+socket,
		"--db="+dbPath,
		"--interval=1s")
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_, _ = cmd.Process.Wait()
	})

	// Wait for the socket to appear (up to 3 seconds).
	deadline := time.Now().Add(3 * time.Second)
	for {
		if fi, err := os.Stat(socket); err == nil && fi.Mode()&os.ModeSocket != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never appeared")
		}
		time.Sleep(50 * time.Millisecond)
	}

	client, err := dialUnix(socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	var reply rpcapi.StatusReply
	if err := client.Call(rpcapi.ServiceName+".Status", rpcapi.StatusArgs{}, &reply); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if reply.TickIntervalSeconds != 1 {
		t.Errorf("TickIntervalSeconds = %d, want 1", reply.TickIntervalSeconds)
	}

	// Round-trip: add a project, list it back.
	if err := client.Call(rpcapi.ServiceName+".ProjectAdd",
		rpcapi.ProjectAddArgs{Name: "test-project"},
		&rpcapi.ProjectAddReply{}); err != nil {
		t.Fatalf("ProjectAdd: %v", err)
	}
	var list rpcapi.ProjectListReply
	if err := client.Call(rpcapi.ServiceName+".ProjectList",
		rpcapi.ProjectListArgs{}, &list); err != nil {
		t.Fatalf("ProjectList: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].Name != "test-project" {
		t.Errorf("got %+v, want one item 'test-project'", list.Items)
	}
}

func dialUnix(path string) (*rpc.Client, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	return rpc.NewClient(conn), nil
}

func projectRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
