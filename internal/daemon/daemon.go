package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/rpc"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"github.com/rian/antitimely/internal/macos"
	"github.com/rian/antitimely/internal/rpcapi"
	"github.com/rian/antitimely/internal/store"
)

type Config struct {
	IntervalSeconds    int
	IdleThresholdSec   int
	AgentCPUThresh     uint64
	AgentCPUThreshIdle uint64
	SocketPath         string
	DBPath             string
	PIDPath            string
}

func DefaultConfig() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	dir := filepath.Join(home, ".antitimely")
	return Config{
		IntervalSeconds:    5,
		IdleThresholdSec:   120,
		AgentCPUThresh:     5,
		AgentCPUThreshIdle: 100,
		SocketPath:         filepath.Join(dir, "antitimely.sock"),
		DBPath:             filepath.Join(dir, "db.sqlite"),
		PIDPath:            filepath.Join(dir, "antitimely.pid"),
	}, nil
}

// rpcConnDeadline bounds total time spent on a single accepted RPC connection.
// CLI commands are one-shot (open → call → close) and finish in well under a
// second, so a 30-second cap is generous; the goal is to make sure a stuck or
// abandoned client cannot pin a goroutine + DB connection forever.
const rpcConnDeadline = 30 * time.Second

// Run boots the daemon and blocks until SIGINT/SIGTERM.
func Run(cfg Config, schemaSQL string) error {
	if schemaSQL == "" {
		return errors.New("schema is empty")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		return fmt.Errorf("mkdir state: %w", err)
	}

	db, q, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	// Idempotent column add for older DBs that predate the paused feature.
	if _, err := db.Exec("ALTER TABLE projects ADD COLUMN paused INTEGER NOT NULL DEFAULT 0"); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate projects.paused: %w", err)
		}
	}

	bridge := &macos.RealBridge{}
	cache := NewCache()
	pt := NewPermissionTracker()
	svc := &AntitimelyService{
		DB:                  db,
		Q:                   q,
		Cache:               cache,
		Bridge:              bridge,
		TickIntervalSeconds: cfg.IntervalSeconds,
		Perm:                pt,
		StartedAtUnix:       time.Now().Unix(),
	}
	if err := svc.ReloadCache(); err != nil {
		return fmt.Errorf("initial cache load: %w", err)
	}

	pipeline := NewPipeline(q, bridge, cache, PipelineConfig{
		IdleThresholdSec:   cfg.IdleThresholdSec,
		CPUDeltaThresh:     cfg.AgentCPUThresh,
		CPUDeltaThreshIdle: cfg.AgentCPUThreshIdle,
	})
	pipeline.SetPermissionTracker(pt)
	poller := NewPoller(pipeline, time.Duration(cfg.IntervalSeconds)*time.Second)

	listener, err := acquireSocket(cfg.SocketPath, cfg.PIDPath)
	if err != nil {
		return err
	}

	if err := writePIDFile(cfg.PIDPath); err != nil {
		return err
	}

	srv := rpc.NewServer()
	if err := srv.RegisterName(rpcapi.ServiceName, svc); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		poller.Run(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		// Exponential backoff on transient Accept errors so EMFILE / similar
		// don't put the loop into a tight printing spin.
		backoff := 5 * time.Millisecond
		const backoffMax = time.Second
		for {
			conn, err := listener.Accept()
			if err != nil {
				if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
					return
				}
				fmt.Fprintln(os.Stderr, "accept:", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < backoffMax {
					backoff *= 2
					if backoff > backoffMax {
						backoff = backoffMax
					}
				}
				continue
			}
			backoff = 5 * time.Millisecond
			_ = conn.SetDeadline(time.Now().Add(rpcConnDeadline))
			go srv.ServeConn(conn)
		}
	}()

	fmt.Printf("antitimely daemon ready (socket=%s db=%s)\n", cfg.SocketPath, cfg.DBPath)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	for {
		sig := <-sigCh
		if sig == syscall.SIGHUP {
			fmt.Println("SIGHUP: reloading cache")
			if err := svc.ReloadCache(); err != nil {
				fmt.Fprintln(os.Stderr, "reload:", err)
			}
			continue
		}
		fmt.Println("shutdown:", sig)
		break
	}
	cancel()
	if err := listener.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "listener close:", err)
	}
	wg.Wait()
	if err := os.Remove(cfg.SocketPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "remove socket:", err)
	}
	if err := os.Remove(cfg.PIDPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "remove pid file:", err)
	}
	return nil
}

func openDB(cfg Config) (*sql.DB, *store.Queries, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(2000)", cfg.DBPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open db: %w", err)
	}
	// Serialize all DB access. The pure-Go SQLite driver doesn't multiplex
	// writers across connections — letting database/sql open more than one
	// guarantees periodic SQLITE_BUSY under any concurrent RPC + poll load.
	db.SetMaxOpenConns(1)
	return db, store.New(db), nil
}

func acquireSocket(path, pidPath string) (net.Listener, error) {
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

func writePIDFile(path string) error {
	pid := fmt.Sprintf("%d\n", os.Getpid())
	return os.WriteFile(path, []byte(pid), 0o600)
}
