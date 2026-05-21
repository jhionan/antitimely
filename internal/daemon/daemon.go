package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/rpc"
	"os"
	"os/signal"
	"path/filepath"
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

// Run boots the daemon and blocks until SIGINT/SIGTERM.
func Run(cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		return fmt.Errorf("mkdir state: %w", err)
	}

	db, q, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := applySchema(db); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}

	bridge := macos.RealBridge{}
	cache := NewCache()
	svc := &AntitimelyService{
		Q:                   q,
		Cache:               cache,
		Bridge:              bridge,
		TickIntervalSeconds: cfg.IntervalSeconds,
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
	poller := NewPoller(pipeline, time.Duration(cfg.IntervalSeconds)*time.Second)

	listener, err := acquireSocket(cfg.SocketPath)
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
		for {
			conn, err := listener.Accept()
			if err != nil {
				if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
					return
				}
				fmt.Fprintln(os.Stderr, "accept:", err)
				continue
			}
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
	_ = listener.Close()
	wg.Wait()
	_ = os.Remove(cfg.SocketPath)
	_ = os.Remove(cfg.PIDPath)
	return nil
}

func openDB(cfg Config) (*sql.DB, *store.Queries, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(2000)", cfg.DBPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open db: %w", err)
	}
	return db, store.New(db), nil
}

func applySchema(db *sql.DB) error {
	if SchemaSQL == "" {
		return fmt.Errorf("schema not loaded; call daemon.SetSchema first")
	}
	_, err := db.Exec(SchemaSQL)
	return err
}

func acquireSocket(path string) (net.Listener, error) {
	// If socket exists, attempt to connect; if a daemon answers, refuse to start.
	if _, err := os.Stat(path); err == nil {
		c, err := net.DialTimeout("unix", path, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return nil, fmt.Errorf("another antitimely daemon is running on %s", path)
		}
		// Stale: remove it.
		_ = os.Remove(path)
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

func writePIDFile(path string) error {
	pid := fmt.Sprintf("%d\n", os.Getpid())
	return os.WriteFile(path, []byte(pid), 0o600)
}
