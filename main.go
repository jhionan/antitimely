package main

import (
	_ "embed"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/rian/antitimely/internal/cli"
	"github.com/rian/antitimely/internal/daemon"
)

//go:embed schema.sql
var schemaSQL string

func main() {
	daemon.SetSchema(schemaSQL)

	if len(os.Args) < 2 {
		os.Exit(cli.Dispatch(nil))
	}
	if os.Args[1] == "daemon" {
		os.Exit(runDaemon(os.Args[2:]))
	}
	os.Exit(cli.Dispatch(os.Args[1:]))
}

func runDaemon(args []string) int {
	cfg, err := daemon.DefaultConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// Load YAML config (~/.antitimely/config.yaml). Missing is fine.
	if path, err := daemon.DefaultConfigFilePath(); err == nil {
		fc, err := daemon.LoadFileConfig(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v (continuing with defaults)\n", err)
		} else if err := fc.ApplyTo(&cfg); err != nil {
			fmt.Fprintf(os.Stderr, "warning: config %s invalid: %v\n", path, err)
		}
	}

	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	interval := fs.Duration("interval", time.Duration(cfg.IntervalSeconds)*time.Second, "Tick interval")
	idleThresh := fs.Duration("idle-thresh", time.Duration(cfg.IdleThresholdSec)*time.Second, "Idle threshold")
	cpuThresh := fs.Uint64("agent-cpu-thresh", cfg.AgentCPUThresh, "Minimum CPU centisecond delta per tick to count an agent as active")
	socket := fs.String("socket", cfg.SocketPath, "Unix socket path")
	dbPath := fs.String("db", cfg.DBPath, "SQLite database path")
	fs.Parse(args)

	cfg.IntervalSeconds = int(interval.Seconds())
	if cfg.IntervalSeconds < 1 {
		cfg.IntervalSeconds = 1
	}
	cfg.IdleThresholdSec = int(idleThresh.Seconds())
	cfg.AgentCPUThresh = *cpuThresh
	cfg.SocketPath = *socket
	cfg.DBPath = *dbPath

	if err := daemon.Run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
