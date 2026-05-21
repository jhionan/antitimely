package cli

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/rian/antitimely/internal/daemon"
)

func cmdReset(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: antitimely reset <all|ticks> [--yes]")
		fmt.Fprintln(os.Stderr, "  all   — wipe ticks, observations, rules, projects, companies, watched_programs")
		fmt.Fprintln(os.Stderr, "  ticks — wipe only time-tracking data; preserve projects/companies/rules/watched")
		return 64
	}

	scope := args[0]
	switch scope {
	case "all", "ticks":
		// valid
	default:
		fmt.Fprintf(os.Stderr, "unknown scope: %q (expected 'all' or 'ticks')\n", scope)
		return 64
	}

	fs := flag.NewFlagSet("reset", flag.ExitOnError)
	yes := fs.Bool("yes", false, "Skip confirmation prompt")
	if err := fs.Parse(args[1:]); err != nil {
		return 64
	}

	cfg, err := daemon.DefaultConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// Refuse if daemon is alive. A stale socket file (no listener) is fine.
	if daemonAlive(cfg.SocketPath) {
		fmt.Fprintln(os.Stderr, "The daemon is currently running. Stop it first:")
		fmt.Fprintln(os.Stderr, "  antitimely uninstall-launch-agent     # if managed by launchd")
		fmt.Fprintf(os.Stderr, "  kill -TERM $(cat %s)  # if running standalone\n", cfg.PIDPath)
		fmt.Fprintln(os.Stderr, "Then re-run `antitimely reset`.")
		return 1
	}

	var summary string
	switch scope {
	case "all":
		summary = "ALL data (ticks, observations, rules, projects, companies, watched programs)"
	case "ticks":
		summary = "all time-tracking data (ticks + observations + ignored). Projects/companies/rules/watched preserved."
	}

	if !*yes {
		fmt.Printf("This will permanently delete %s\nfrom %s.\n\n", summary, cfg.DBPath)
		fmt.Print("Type 'yes' to confirm: ")
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			fmt.Println("aborted")
			return 1
		}
		if strings.TrimSpace(scanner.Text()) != "yes" {
			fmt.Println("aborted")
			return 1
		}
	}

	if err := daemon.Reset(cfg.DBPath, daemon.ResetScope(scope)); err != nil {
		fmt.Fprintln(os.Stderr, "reset failed:", err)
		return 1
	}
	fmt.Println("Reset complete.")
	if scope == "all" {
		fmt.Println("Restart the daemon to begin tracking again (you'll need to re-add projects, companies, and watched programs).")
	} else {
		fmt.Println("Restart the daemon to resume tracking.")
	}
	return 0
}

// daemonAlive reports whether something is actually listening on the socket
// (vs. just a stale socket file).
func daemonAlive(socketPath string) bool {
	if _, err := os.Stat(socketPath); err != nil {
		return false
	}
	conn, err := net.DialTimeout("unix", socketPath, 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
