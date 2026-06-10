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
	cpuThresh := fs.Uint64("agent-cpu-thresh", cfg.AgentCPUThresh, "CPU centisecond \"busy bar\" per poll to count an agent active, sustained for agent_busy_rise_ticks polls (default 15)")
	cpuThreshIdle := fs.Uint64("agent-cpu-thresh-idle", cfg.AgentCPUThreshIdle,
		"CPU centisecond busy bar per poll when the USER is idle (default 100 = 20% of one core; default for active is 15)")
	socket := fs.String("socket", cfg.SocketPath, "Unix socket path")
	dbPath := fs.String("db", cfg.DBPath, "SQLite database path")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 64
	}

	cfg.IntervalSeconds = int(interval.Seconds())
	if cfg.IntervalSeconds < 1 {
		cfg.IntervalSeconds = 1
	}
	cfg.IdleThresholdSec = int(idleThresh.Seconds())
	cfg.AgentCPUThresh = *cpuThresh
	cfg.AgentCPUThreshIdle = *cpuThreshIdle
	cfg.SocketPath = *socket
	cfg.DBPath = *dbPath

	if err := daemon.Run(cfg, schemaSQL); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
