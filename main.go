package main

import (
	_ "embed"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	if handled, code := platformCommand(os.Args[1:]); handled {
		os.Exit(code)
	}
	os.Exit(cli.Dispatch(os.Args[1:]))
}

func runDaemon(args []string) int {
	cfg, err := daemon.DefaultConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	defaultFile, _ := daemon.DefaultConfigFilePath()
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	configFile := fs.String("config", defaultFile, "Daemon YAML config file")
	interval := fs.Duration("interval", time.Duration(cfg.IntervalSeconds)*time.Second, "Tick interval")
	idleThresh := fs.Duration("idle-thresh", time.Duration(cfg.IdleThresholdSec)*time.Second, "Idle threshold")
	cpuThresh := fs.Uint64("agent-cpu-thresh", cfg.AgentCPUThresh, "CPU centisecond \"busy bar\" per poll to count an agent active, sustained for agent_busy_rise_ticks polls (default 15)")
	cpuThreshIdle := fs.Uint64("agent-cpu-thresh-idle", cfg.AgentCPUThreshIdle,
		"CPU centisecond busy bar per poll when the USER is idle (default 100 = 20% of one core; default for active is 15)")
	socket := fs.String("socket", cfg.SocketPath, "Local RPC endpoint (Unix socket or Windows named pipe)")
	dbPath := fs.String("db", cfg.DBPath, "SQLite database path")
	pidPath := fs.String("pid", cfg.PIDPath, "Daemon PID file path")
	logPath := fs.String("log", "", "Append daemon output to this file")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 64
	}

	var configWarning string
	if *configFile != "" {
		fc, err := daemon.LoadFileConfig(*configFile)
		if err == nil {
			err = fc.ApplyTo(&cfg)
		}
		if err != nil {
			configWarning = fmt.Sprintf("warning: config %s: %v", *configFile, err)
		}
	}
	// Apply explicitly supplied CLI flags after the selected config file.
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "interval":
			cfg.IntervalSeconds = max(1, int(interval.Seconds()))
		case "idle-thresh":
			cfg.IdleThresholdSec = int(idleThresh.Seconds())
		case "agent-cpu-thresh":
			cfg.AgentCPUThresh = *cpuThresh
		case "agent-cpu-thresh-idle":
			cfg.AgentCPUThreshIdle = *cpuThreshIdle
		case "socket":
			cfg.SocketPath = *socket
		case "db":
			cfg.DBPath = *dbPath
		case "pid":
			cfg.PIDPath = *pidPath
		}
	})

	if *logPath != "" {
		if err := os.MkdirAll(filepath.Dir(*logPath), 0o700); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		file, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer file.Close()
		os.Stdout, os.Stderr = file, file
		log.SetOutput(file)
	}

	if configWarning != "" {
		fmt.Fprintln(os.Stderr, configWarning)
	}
	if err := daemon.Run(cfg, schemaSQL); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
