package cli

import (
	"flag"
	"fmt"
	"io"
	"net/rpc"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
)

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	once := fs.Bool("once", false, "print a single snapshot and exit (no live view)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 64
	}

	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()

	if !*once && IsStdoutTerminal() {
		return runStatusLive(client)
	}
	return renderOnce(client)
}

// fetchStatus performs the single Status RPC.
func fetchStatus(client *rpc.Client) (rpcapi.StatusReply, error) {
	var reply rpcapi.StatusReply
	err := client.Call(rpcapi.ServiceName+".Status", rpcapi.StatusArgs{}, &reply)
	return reply, err
}

// renderOnce fetches and prints a single status snapshot: body to stdout,
// accessibility warning to stderr. Returns the process exit code.
func renderOnce(client *rpc.Client) int {
	reply, err := fetchStatus(client)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	renderStatus(os.Stdout, reply)
	renderWarning(os.Stderr, reply)
	return 0
}

// renderStatus writes one status frame (header, today total, grouped billables,
// unassigned bucket) to w. Pure: no globals, no time.Now, no accessibility
// warning (see renderWarning), no exit.
func renderStatus(w io.Writer, reply rpcapi.StatusReply) {
	fmt.Fprintf(w, "Idle: %s   |   Tick: %ds   |   Permission: %s   |   Uptime: %s\n",
		fmtDuration(int64(reply.UserIdleSeconds)),
		reply.TickIntervalSeconds,
		reply.PermissionState,
		fmtDuration(reply.DaemonUptimeSeconds),
	)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Today: %s total tracked\n", fmtDuration(reply.TodayTotalSeconds))
	fmt.Fprintln(w)

	if len(reply.Companies) == 0 && reply.UnassignedBillableSeconds == 0 {
		fmt.Fprintln(w, "(no time tracked yet)")
		return
	}

	fmt.Fprintln(w, "Billable (since last invoice per company):")
	fmt.Fprintln(w)

	for _, co := range reply.Companies {
		if co.Name == "(no company)" {
			continue
		}
		since := "never"
		if co.LastInvoiceUnix != 0 {
			since = time.Unix(co.LastInvoiceUnix, 0).Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "  %-38s %s   (since: %s)\n", co.Name, fmtDuration(co.BillableSeconds), since)
		renderProjects(w, co.Projects)
		fmt.Fprintln(w)
	}

	for _, co := range reply.Companies {
		if co.Name != "(no company)" {
			continue
		}
		fmt.Fprintf(w, "  %-38s %s\n", "(no company)", fmtDuration(co.BillableSeconds))
		renderProjects(w, co.Projects)
		fmt.Fprintln(w)
	}

	if reply.UnassignedBillableSeconds > 0 || reply.UnassignedTodaySeconds > 0 {
		sigNote := ""
		if reply.UnassignedSignaturesCount > 0 {
			sigNote = fmt.Sprintf(", %d signature(s), run `antitimely review`", reply.UnassignedSignaturesCount)
		}
		fmt.Fprintf(w, "  %-38s %s   (today: %s%s)\n",
			"(unassigned)",
			fmtDuration(reply.UnassignedBillableSeconds),
			fmtDuration(reply.UnassignedTodaySeconds),
			sigNote,
		)
	}
}

func renderProjects(w io.Writer, projects []rpcapi.ProjectTotals) {
	for _, pr := range projects {
		pausedNote := ""
		if pr.Paused {
			pausedNote = "  (paused)"
		}
		armedNote := ""
		if pr.Armed {
			armedNote = "  (armed: needs focus)"
			if pr.SuppressedSeconds > 0 {
				armedNote = fmt.Sprintf("  (armed: needs focus — %s NOT counted!)", fmtDuration(pr.SuppressedSeconds))
			}
		}
		fmt.Fprintf(w, "    %-36s %s   (today: %s)%s%s\n",
			pr.Name,
			fmtDuration(pr.BillableSeconds),
			fmtDuration(pr.TodaySeconds),
			pausedNote,
			armedNote,
		)
	}
}

// renderWarning writes the accessibility-permission warning to w when
// window-title capture is disabled; otherwise writes nothing.
func renderWarning(w io.Writer, reply rpcapi.StatusReply) {
	if reply.PermissionState != "accessibility_denied" {
		return
	}
	fmt.Fprintln(w,
		"  Warning: Window-title capture disabled. Grant antitimely BOTH:\n"+
			"    - Privacy & Security -> Accessibility (required for Electron/JVM apps: VS Code, Antigravity, JetBrains, ...)\n"+
			"    - Privacy & Security -> Automation -> antitimely -> System Events\n"+
			"  Then restart the daemon (make rebuild). A rebuild can reset these grants.")
}

// fmtDuration formats a duration in seconds as "1h2m3s", or "0s" for zero.
func fmtDuration(seconds int64) string {
	if seconds <= 0 {
		return "0s"
	}
	return time.Duration(seconds * int64(time.Second)).String()
}

// runStatusLive renders the status frame on the alternate screen, refreshing
// every 5s, until the user presses Esc (returns) or a terminal/job-control
// signal arrives. The terminal is always restored: Esc returns to the caller;
// SIGINT/SIGQUIT/SIGTERM/SIGTSTP all clean-exit with terminal restored (so
// Ctrl-Z exits the view rather than suspending with alt-screen active).
func runStatusLive(client *rpc.Client) int {
	st, err := enterCbreak()
	if err != nil {
		// Not a real tty after all — fall back to a single snapshot.
		return renderOnce(client)
	}

	out := os.Stdout
	var restoreOnce sync.Once
	done := make(chan struct{})
	cleanup := func() {
		restoreOnce.Do(func() {
			altScreenLeave(out)
			showCursor(out)
			st.restore()
			close(done)
		})
	}
	defer cleanup()

	// Always restore the terminal on any terminal/job-control signal. Cbreak
	// keeps ISIG, so Ctrl-C (SIGINT), Ctrl-\ (SIGQUIT) and Ctrl-Z (SIGTSTP)
	// still fire; catching them here prevents leaving the terminal in raw +
	// alt-screen mode. The goroutine also exits on normal teardown (done) so
	// it does not leak across repeated menu visits.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGTSTP)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case sig := <-sigCh:
			cleanup()
			code := 130
			if s, ok := sig.(syscall.Signal); ok {
				code = 128 + int(s)
			}
			os.Exit(code)
		case <-done:
		}
	}()

	altScreenEnter(out)
	hideCursor(out)
	quietTerminalInput(out) // stop focus/paste escape sequences from closing the view

	// reply holds the last expensive Status snapshot; it is recomputed only when
	// the cheap LatestTick probe shows a new tick (or the day rolls / probe
	// fails). The header's cheap live fields (idle, uptime, permission) are
	// refreshed from the probe every cycle so the view still feels live.
	var reply rpcapi.StatusReply
	haveData := false
	var lastErr error
	lastTs := int64(-1)
	lastDay := 0
	for {
		probe, perr := fetchLatestTick(client)
		curDay := localDayKey(time.Now())
		if statusBodyChanged(lastTs, probe.LatestTickUnix, lastDay, curDay, perr != nil) {
			full, ferr := fetchStatus(client)
			if ferr != nil {
				// Transient RPC error (e.g. the daemon is slow and the query
				// timed out). Keep the view alive showing the last good frame
				// plus a notice, and retry next cycle — only Esc/Ctrl-C exit.
				lastErr = ferr
			} else {
				reply = full
				haveData = true
				lastErr = nil
				lastDay = curDay
				if perr == nil {
					lastTs = probe.LatestTickUnix
				}
			}
		}
		if perr == nil {
			reply.UserIdleSeconds = probe.UserIdleSeconds
			reply.DaemonUptimeSeconds = probe.DaemonUptimeSeconds
			reply.PermissionState = probe.PermissionState
		}
		clearScreen(out)
		if haveData {
			renderStatus(out, reply)
			renderWarning(out, reply)
		} else {
			fmt.Fprintln(out, "Connecting to daemon…")
		}
		if lastErr != nil {
			fmt.Fprintf(out, "\n⚠ daemon not responding, retrying: %v\n", lastErr)
		}
		renderFooter(out, time.Now())

		if st.readEvent() == evtEsc {
			return 0 // cleanup runs via defer
		}
	}
}

// fetchLatestTick performs the cheap LatestTick probe RPC.
func fetchLatestTick(client *rpc.Client) (rpcapi.LatestTickReply, error) {
	var reply rpcapi.LatestTickReply
	err := client.Call(rpcapi.ServiceName+".LatestTick", rpcapi.LatestTickArgs{}, &reply)
	return reply, err
}

// statusBodyChanged reports whether the expensive Status body must be
// recomputed: on the first cycle (lastTs < 0), when a new tick was recorded
// (curTs != lastTs), when the local day rolled over (the "Today" totals reset),
// or when the cheap probe failed (probeErr — fall back to a full fetch).
func statusBodyChanged(lastTs, curTs int64, lastDay, curDay int, probeErr bool) bool {
	return lastTs < 0 || probeErr || curTs != lastTs || curDay != lastDay
}

// localDayKey is a per-local-calendar-day integer (YYYYMMDD) used to detect
// midnight rollover so the "Today" totals reset even with no new ticks.
func localDayKey(now time.Time) int {
	y, m, d := now.Date()
	return y*10000 + int(m)*100 + d
}

// renderFooter writes the live-mode footer line.
func renderFooter(w io.Writer, now time.Time) {
	fmt.Fprintf(w, "\nlive · every 5s · Esc to exit · %s\n", now.Format("15:04:05"))
}
