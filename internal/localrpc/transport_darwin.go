package localrpc

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func DefaultPath(stateDir string) (string, error) {
	return filepath.Join(stateDir, "antitimely.sock"), nil
}

func Dial(path string) (net.Conn, error) {
	return net.DialTimeout("unix", path, 2*time.Second)
}

func Listen(path, pidPath string) (net.Listener, error) {
	if _, err := os.Stat(path); err == nil {
		// Socket present. Try the PID file first — if it points to a live
		// process, refuse to start. Only treat the socket as stale when we
		// can prove nothing is listening (PID file missing/stale AND dial
		// fails).
		if running, pid := pidFileAlive(pidPath); running {
			return nil, fmt.Errorf("another antitimely daemon is running (pid=%d, socket=%s)", pid, path)
		}
		c, err := net.DialTimeout("unix", path, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return nil, fmt.Errorf("another antitimely daemon is running on %s", path)
		}
		fmt.Fprintf(os.Stderr, "removing stale socket %s (pid file missing or dead)\n", path)
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// pidFileAlive returns whether the PID file at path points to a live process.
func pidFileAlive(path string) (bool, int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false, 0
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, 0
	}
	// Signal 0 doesn't deliver but returns an error if the process is gone.
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false, pid
	}
	return true, pid
}
