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
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"github.com/rian/antitimely/internal/herdr"
	"github.com/rian/antitimely/internal/localrpc"
	"github.com/rian/antitimely/internal/osbridge"
	"github.com/rian/antitimely/internal/rpcapi"
	"github.com/rian/antitimely/internal/store"
	"github.com/rian/antitimely/internal/syncclient"
	"github.com/rian/antitimely/internal/tasktimer"
)

type Config struct {
	Sync               syncclient.Config
	IntervalSeconds    int
	IdleThresholdSec   int
	AgentCPUThresh     uint64
	AgentCPUThreshIdle uint64
	AgentBusyRiseTicks int
	AgentBusyFallTicks int
	SocketPath         string
	DBPath             string
	PIDPath            string
	TranscriptTracking bool
	TranscriptGraceSec int
	// TranscriptRoots are Claude Code projects/ dirs, one per config dir
	// (~/.claude, ~/.claude-work, …). Empty disables Claude transcripts.
	TranscriptRoots []string
	// OpencodeDB is opencode's SQLite store, read as a transcript source.
	// Empty disables it.
	OpencodeDB string
	// CodexRoot is Codex's rollout directory, read as a transcript source.
	// Empty disables it.
	CodexRoot string
}

func DefaultConfig() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	dir := filepath.Join(home, ".antitimely")
	socketPath, err := localrpc.DefaultPath(dir)
	if err != nil {
		return Config{}, err
	}
	return Config{
		IntervalSeconds:    5,
		IdleThresholdSec:   120,
		AgentCPUThresh:     15,
		AgentCPUThreshIdle: 100,
		AgentBusyRiseTicks: 2,
		AgentBusyFallTicks: 3,
		SocketPath:         socketPath,
		DBPath:             filepath.Join(dir, "db.sqlite"),
		PIDPath:            filepath.Join(dir, "antitimely.pid"),
		TranscriptTracking: true,
		TranscriptGraceSec: 600,
		TranscriptRoots:    defaultTranscriptRoots(),
		OpencodeDB:         defaultOpencodeDB(),
		CodexRoot:          defaultCodexRoot(),
	}, nil
}

// rpcConnDeadline bounds total time spent on a single accepted RPC connection.
// CLI commands are one-shot (open → call → close) and finish in well under a
// second, so a 30-second cap is generous; the goal is to make sure a stuck or
// abandoned client cannot pin a goroutine + DB connection forever.
const rpcConnDeadline = 30 * time.Second

// invoiceCreditMigrations adds the advance-credit columns. The CHECK must ship
// in the same statement as the column: the loop below tolerates "duplicate
// column", so a later attempt to add the constraint silently no-ops and the
// only remaining route is a full table rebuild.
var invoiceCreditMigrations = []string{
	"ALTER TABLE invoices ADD COLUMN kind TEXT NOT NULL DEFAULT 'hourly' CHECK (kind IN ('hourly','advance'))",
	"ALTER TABLE invoices ADD COLUMN credit_applied_cents INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE invoices ADD COLUMN discount_cents INTEGER NOT NULL DEFAULT 0",
}

// Run boots the daemon and blocks until SIGINT/SIGTERM.
// pipelineConfigFor translates the daemon's on-disk config into the pipeline's
// tuning knobs. Extracted from Run so the derived values below — the
// auto-disarm threshold and the slow-tick budget, neither of which is a
// straight copy of a config field — can be pinned by tests.
func pipelineConfigFor(cfg Config) PipelineConfig {
	// Auto-disarm an armed project after ~60s of sustained, user-present agent
	// activity in its directory — long enough to ignore a transient blip, short
	// enough that a broken focus-disarm path never silently eats much time.
	autoDisarmTicks := 12
	if cfg.IntervalSeconds > 0 {
		if n := 60 / cfg.IntervalSeconds; n > 0 {
			autoDisarmTicks = n
		}
	}
	// A tick is over budget once it outlives the interval it is polled at:
	// past that point the next tick is already due and seconds go unrecorded.
	// No interval ⇒ no budget ⇒ the report stays off.
	var tickBudget time.Duration
	if cfg.IntervalSeconds > 0 {
		tickBudget = time.Duration(cfg.IntervalSeconds) * time.Second
	}
	// Claude Code keeps its per-process session registry beside the
	// transcripts, in the same config dir.
	var claudeRoots []ClaudeRoot
	for _, r := range cfg.TranscriptRoots {
		claudeRoots = append(claudeRoots, ClaudeRoot{Projects: r, Sessions: filepath.Join(filepath.Dir(r), "sessions")})
	}
	return PipelineConfig{
		IdleThresholdSec:     cfg.IdleThresholdSec,
		CPUDeltaThresh:       cfg.AgentCPUThresh,
		CPUDeltaThreshIdle:   cfg.AgentCPUThreshIdle,
		AutoDisarmAgentTicks: autoDisarmTicks,
		AgentBusyRiseTicks:   cfg.AgentBusyRiseTicks,
		AgentBusyFallTicks:   cfg.AgentBusyFallTicks,
		TranscriptTracking:   cfg.TranscriptTracking,
		ClaudeRoots:          claudeRoots,
		TranscriptGraceSec:   cfg.TranscriptGraceSec,
		OpencodeDB:           cfg.OpencodeDB,
		CodexRoot:            cfg.CodexRoot,
		TickBudget:           tickBudget,
	}
}

// newDaemonPipeline builds the Pipeline the daemon actually runs. It exists
// so that the one line switching herdr space attribution ON in production —
// pointing the resolver at the real session.json — is reachable from a test.
// NewPipeline deliberately defaults to a resolver with an empty path (it
// resolves nothing, so tests need no herdr state), which means dropping this
// wiring would silently disable space attribution with every test still
// green. TestDaemonPipelineUsesRealHerdrSessionPath is that assertion.
func newDaemonPipeline(q *store.Queries, bridge osbridge.Bridge, cache *Cache, cfg PipelineConfig) *Pipeline {
	p := NewPipeline(q, bridge, cache, cfg)
	p.herdr = herdr.NewDefaultResolver()
	return p
}

func Run(cfg Config, schemaSQL string) error {
	if schemaSQL == "" {
		return errors.New("schema is empty")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		return fmt.Errorf("mkdir state: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(cfg.PIDPath), 0o700); err != nil {
		return fmt.Errorf("mkdir pid directory: %w", err)
	}
	listener, err := localrpc.Listen(cfg.SocketPath, cfg.PIDPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.SocketPath, err)
	}
	defer listener.Close()
	if err := writePIDFile(cfg.PIDPath); err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(cfg.PIDPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(os.Stderr, "remove pid file:", err)
		}
	}()

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

	// Idempotent column adds for older DBs that predate the invoice feature.
	invoiceMigrations := []string{
		"ALTER TABLE companies ADD COLUMN billing_mode TEXT NOT NULL DEFAULT 'none'",
		"ALTER TABLE companies ADD COLUMN currency TEXT",
		"ALTER TABLE companies ADD COLUMN rate_cents INTEGER",
		"ALTER TABLE companies ADD COLUMN billed_from TEXT",
		"ALTER TABLE invoices ADD COLUMN number TEXT",
		"ALTER TABLE invoices ADD COLUMN pdf_path TEXT",
		"ALTER TABLE invoices ADD COLUMN total_cents INTEGER",
		"ALTER TABLE invoices ADD COLUMN currency TEXT",
		"ALTER TABLE invoices ADD COLUMN sender_key TEXT",
	}
	for _, q := range append(invoiceMigrations, invoiceCreditMigrations...) {
		if _, err := db.Exec(q); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate %q: %w", q, err)
		}
	}
	if err := migrateObservationsSourceCheck(db); err != nil {
		return fmt.Errorf("migrate observations source: %w", err)
	}
	if err := migrateObservationsSpaceID(db); err != nil {
		return fmt.Errorf("migrate observations space_id: %w", err)
	}
	if err := migrateRulesSpaceID(db); err != nil {
		return fmt.Errorf("migrate rules match_space_id: %w", err)
	}

	syncWorker, syncErr := syncclient.Configure(context.Background(), db, cfg.Sync, cfg.IntervalSeconds)
	if syncErr != nil {
		fmt.Fprintln(os.Stderr, "sync disabled:", syncErr)
	}

	tasks, taskErr := tasktimer.Open(context.Background(), db, cfg.IntervalSeconds)
	if taskErr != nil {
		fmt.Fprintln(os.Stderr, "task timer unavailable:", taskErr)
	}
	var clickUp *tasktimer.Remote
	if syncWorker != nil && tasks != nil {
		clickUp, taskErr = tasktimer.NewRemote(tasks, cfg.Sync)
		if taskErr != nil {
			fmt.Fprintln(os.Stderr, "ClickUp unavailable:", taskErr)
		}
	}
	bridge := &osbridge.RealBridge{}
	cache := NewCache()
	pt := NewPermissionTracker()
	svc := &AntitimelyService{
		SyncWorker:          syncWorker,
		Tasks:               tasks,
		ClickUp:             clickUp,
		SyncError:           syncErr,
		DB:                  db,
		Q:                   q,
		Cache:               cache,
		Bridge:              bridge,
		TickIntervalSeconds: cfg.IntervalSeconds,
		Perm:                pt,
		StartedAtUnix:       time.Now().Unix(),
		TranscriptRoots:     cfg.TranscriptRoots,
		TranscriptGraceSec:  cfg.TranscriptGraceSec,
	}
	if err := svc.ReloadCache(); err != nil {
		return fmt.Errorf("initial cache load: %w", err)
	}

	pipeline := newDaemonPipeline(q, bridge, cache, pipelineConfigFor(cfg))
	pipeline.SetPermissionTracker(pt)
	pipeline.SetDBStats(db.Stats)
	poller := NewPoller(pipeline, time.Duration(cfg.IntervalSeconds)*time.Second)

	srv := rpc.NewServer()
	if err := srv.RegisterName(rpcapi.ServiceName, svc); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	if syncWorker != nil {
		wg.Go(func() { syncWorker.Run(ctx) })
	}
	if clickUp != nil {
		wg.Go(func() { clickUp.Run(ctx) })
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		poller.Run(ctx)
	}()

	// Periodically truncate the WAL. Passive auto-checkpoint alone let it grow
	// unbounded (see checkpointWAL); a 5-minute TRUNCATE pass keeps it bounded
	// and runs a final checkpoint on shutdown.
	wg.Add(1)
	go func() {
		defer wg.Done()
		runCheckpointer(ctx, db, 5*time.Minute)
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

	fmt.Printf("antitimely daemon ready (endpoint=%s db=%s)\n", cfg.SocketPath, cfg.DBPath)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)
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

func writePIDFile(path string) error {
	pid := fmt.Sprintf("%d\n", os.Getpid())
	return os.WriteFile(path, []byte(pid), 0o600)
}
