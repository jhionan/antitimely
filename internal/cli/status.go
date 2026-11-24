package cli

import (
	"flag"
	"fmt"
	"io"
	"net/rpc"
	"os"
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
		fmt.Fprintf(w, "  %-38s %s   (today dedup: %s, since: %s)\n",
			co.Name, fmtDuration(co.BillableSeconds), fmtDuration(co.TodaySeconds), since)
		renderProjects(w, co.Projects)
		fmt.Fprintln(w)
	}

	for _, co := range reply.Companies {
		if co.Name != "(no company)" {
			continue
		}
		fmt.Fprintf(w, "  %-38s %s   (today dedup: %s)\n",
			"(no company)", fmtDuration(co.BillableSeconds), fmtDuration(co.TodaySeconds))
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
			"  Then run `atl restart` — a grant only applies to a freshly started\n"+
			"  daemon, and restarting is enough (no rebuild required).")
}

// fmtDuration formats a duration in seconds as "1h2m3s", or "0s" for zero.
func fmtDuration(seconds int64) string {
	if seconds <= 0 {
		return "0s"
	}
	return time.Duration(seconds * int64(time.Second)).String()
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
