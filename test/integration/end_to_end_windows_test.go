package integration_test

import (
	"encoding/json"
	"fmt"
	"net/rpc"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rian/antitimely/internal/cli"
	"github.com/rian/antitimely/internal/daemon"
	"github.com/rian/antitimely/internal/localrpc"
	"github.com/rian/antitimely/internal/rpcapi"
)

func TestWindowsDaemonRespondsToRPC(t *testing.T) {
	dir := t.TempDir()
	endpoint, err := localrpc.DefaultPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	endpoint += fmt.Sprintf("-test-%d", os.Getpid())

	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	start := func() (*exec.Cmd, *rpc.Client) {
		t.Helper()
		cmd := exec.Command(binary, "-test.run=^TestWindowsDaemonProcess$")
		cmd.Env = append(os.Environ(), "ANTITIMELY_TEST_DAEMON_DIR="+dir, "ANTITIMELY_TEST_DAEMON_PIPE="+endpoint)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		deadline := time.Now().Add(15 * time.Second)
		for {
			client, err := cli.Dial(endpoint)
			if err == nil {
				t.Cleanup(func() { client.Close() })
				var status rpcapi.StatusReply
				if err := client.Call(rpcapi.ServiceName+".Status", rpcapi.StatusArgs{}, &status); err != nil {
					t.Fatal(err)
				}
				if status.TickIntervalSeconds != 1 {
					t.Fatalf("unexpected daemon status: %+v", status)
				}
				return cmd, client
			}
			if time.Now().After(deadline) {
				t.Fatalf("daemon did not start: %v", err)
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
	cmd, client := start()
	if listener, err := localrpc.Listen(endpoint, ""); err == nil {
		listener.Close()
		t.Fatal("launched a second listener on the same pipe")
	}
	var added rpcapi.WatchAddReply
	if err := client.Call(rpcapi.ServiceName+".WatchAdd", rpcapi.WatchAddArgs{
		Kind: "binary", Identifier: "test-agent.exe",
	}, &added); err != nil {
		t.Fatal(err)
	}
	task := rpcapi.ClickUpTask{ConnectionID: strings.Repeat("a", 32), WorkspaceID: "17", TaskID: "test", Name: "Test timer"}
	var timer rpcapi.ClickUpTimer
	if err := client.Call(rpcapi.ServiceName+".ClickUpSwitch", rpcapi.ClickUpSwitchArgs{Task: &task}, &timer); err != nil {
		t.Fatal(err)
	}
	if timer.StartedUnix == 0 {
		t.Fatal("timer did not start")
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait() // a forced termination leaves the SQLite WAL and PID file
	recoveredProcess, recovered := start()
	var list rpcapi.WatchListReply
	if err := recovered.Call(rpcapi.ServiceName+".WatchList", rpcapi.WatchListArgs{}, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Identifier != "test-agent.exe" {
		t.Fatalf("watch program was lost after restart: %+v", list)
	}
	var status rpcapi.ClickUpStatusReply
	if err := recovered.Call(rpcapi.ServiceName+".ClickUpStatus", rpcapi.ClickUpStatusArgs{}, &status); err != nil {
		t.Fatal(err)
	}
	if status.Timer.StartedUnix != timer.StartedUnix || status.Timer.TaskID != timer.TaskID {
		t.Fatalf("active timer was lost after restart: %+v", status.Timer)
	}

	profile := filepath.Join(dir, ".antitimely")
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "config.yaml"), []byte("socket_path: '"+endpoint+"'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runTimerCLI := func(input string, args ...string) rpcapi.Timer {
		t.Helper()
		args = append([]string{"-test.run=^TestWindowsTimerCLIProcess$", "--", "timer"}, args...)
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "USERPROFILE="+dir, "ANTITIMELY_TEST_TIMER_CLI=1")
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("timer CLI: %v", err)
		}
		var timer rpcapi.Timer
		if err := json.Unmarshal(out, &timer); err != nil {
			t.Fatalf("timer CLI returned %q: %v", out, err)
		}
		return timer
	}
	local := runTimerCLI(`{"Name":"Plan the week"}`, "start", "--stdin")
	if !local.Local || local.Name != "Plan the week" || local.TaskID != "" || local.StartedUnix == 0 {
		t.Fatalf("unexpected local timer: %+v", local)
	}
	if err := recoveredProcess.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = recoveredProcess.Wait()
	_, localRecovered := start()
	var localStatus rpcapi.TimerStatusReply
	if err := localRecovered.Call(rpcapi.ServiceName+".TimerStatus", rpcapi.TimerStatusArgs{}, &localStatus); err != nil {
		t.Fatal(err)
	}
	if localStatus.Timer != local {
		t.Fatalf("standalone timer was lost after restart: %+v", localStatus.Timer)
	}
	time.Sleep(time.Until(time.Unix(local.StartedUnix+1, 0)))
	stopped := runTimerCLI("", "stop")
	localStatus = rpcapi.TimerStatusReply{}
	if err := localRecovered.Call(rpcapi.ServiceName+".TimerStatus", rpcapi.TimerStatusArgs{}, &localStatus); err != nil {
		t.Fatal(err)
	}
	if stopped.StartedUnix != 0 || localStatus.Timer.StartedUnix != 0 {
		t.Fatalf("local timer is still running: %+v", localStatus.Timer)
	}
	for _, entry := range localStatus.Entries {
		if entry.State == "local" && entry.Name == local.Name && entry.Start == local.StartedUnix && entry.End > entry.Start {
			return
		}
	}
	t.Fatalf("stopped session missing from history: %+v", localStatus.Entries)
}

func TestWindowsTimerCLIProcess(t *testing.T) {
	if os.Getenv("ANTITIMELY_TEST_TIMER_CLI") == "" {
		t.Skip("timer CLI subprocess helper")
	}
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		t.Fatal("missing timer CLI arguments")
	}
	os.Exit(cli.Dispatch(os.Args[separator+1:]))
}

func TestWindowsDaemonProcess(t *testing.T) {
	dir := os.Getenv("ANTITIMELY_TEST_DAEMON_DIR")
	if dir == "" {
		t.Skip("daemon subprocess helper")
	}
	cfg, err := daemon.DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBPath = filepath.Join(dir, "db.sqlite")
	cfg.PIDPath = filepath.Join(dir, "antitimely.pid")
	cfg.SocketPath = os.Getenv("ANTITIMELY_TEST_DAEMON_PIPE")
	cfg.TranscriptTracking = false
	cfg.IntervalSeconds = 1
	schema, err := os.ReadFile(filepath.Join("..", "..", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := daemon.Run(cfg, string(schema)); err != nil {
		t.Fatal(err)
	}
}
