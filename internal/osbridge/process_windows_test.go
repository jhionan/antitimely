package osbridge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsProcessProbe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "agent Café")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestWindowsProcessHelper$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ANTITIMELY_TEST_PROCESS=1", "HERDR_PANE_ID=pané-42")
	ready, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	var marker [1]byte
	if _, err := ready.Read(marker[:]); err != nil || marker[0] != '1' {
		t.Fatalf("helper did not start: %q, %v", marker, err)
	}
	bridge := &RealBridge{}
	pid := cmd.Process.Pid
	cwd, err := bridge.ProcessCWD(ctx, pid)
	if err != nil || !strings.EqualFold(dir, cwd) {
		t.Fatalf("cwd = %q, %v; want %q", cwd, err, dir)
	}
	env, err := bridge.ProcessEnvVars(ctx, pid, "herdr_pane_id", "ABSENT_TEST_KEY")
	if err != nil || env["herdr_pane_id"] != "pané-42" || len(env) != 1 {
		t.Fatalf("env = %#v, %v", env, err)
	}
	samples, err := bridge.ListProcesses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range samples {
		if sample.PID == pid {
			if !strings.EqualFold(sample.Name, filepath.Base(exe)) {
				t.Fatalf("wrong process name: %+v", sample)
			}
			return
		}
	}
	t.Fatal("same-user process is absent from snapshot")
}

func TestWindowsProcessHelper(t *testing.T) {
	if os.Getenv("ANTITIMELY_TEST_PROCESS") != "1" {
		return
	}
	_, _ = os.Stdout.Write([]byte("1"))
	time.Sleep(15 * time.Second)
}
