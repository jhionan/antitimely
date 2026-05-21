package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
)

func cmdStatus(args []string) int {
	sockPath, err := DefaultSocketPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	client, err := Dial(sockPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer client.Close()

	var reply rpcapi.StatusReply
	if err := client.Call(rpcapi.ServiceName+".Status", rpcapi.StatusArgs{}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// Header line.
	fmt.Printf("Idle: %s   |   Tick: %ds   |   Permission: %s   |   Uptime: %s\n",
		fmtDuration(int64(reply.UserIdleSeconds)),
		reply.TickIntervalSeconds,
		reply.PermissionState,
		fmtDuration(reply.DaemonUptimeSeconds),
	)
	if reply.PermissionState == "accessibility_denied" {
		fmt.Fprintln(os.Stderr,
			"  Warning: Window-title capture disabled. Enable in System Settings -> Privacy & Security -> Automation -> antitimely -> System Events.")
	}
	fmt.Println()

	// Today total.
	fmt.Printf("Today: %s total tracked\n", fmtDuration(reply.TodayTotalSeconds))
	fmt.Println()

	// Grouped companies block.
	if len(reply.Companies) == 0 && reply.UnassignedBillableSeconds == 0 {
		fmt.Println("(no time tracked yet)")
		return 0
	}

	fmt.Println("Billable (since last invoice per company):")
	fmt.Println()

	for _, co := range reply.Companies {
		if co.Name == "(no company)" {
			// Render under unassigned at end.
			continue
		}
		since := "never"
		if co.LastInvoiceUnix != 0 {
			since = time.Unix(co.LastInvoiceUnix, 0).Local().Format("2006-01-02 15:04")
		}
		fmt.Printf("  %-38s %s   (since: %s)\n", co.Name, fmtDuration(co.BillableSeconds), since)
		for _, pr := range co.Projects {
			pausedNote := ""
			if pr.Paused {
				pausedNote = "  (paused)"
			}
			fmt.Printf("    %-36s %s   (today: %s)%s\n",
				pr.Name,
				fmtDuration(pr.BillableSeconds),
				fmtDuration(pr.TodaySeconds),
				pausedNote,
			)
		}
		fmt.Println()
	}

	// No-company projects.
	for _, co := range reply.Companies {
		if co.Name != "(no company)" {
			continue
		}
		fmt.Printf("  %-38s %s\n", "(no company)", fmtDuration(co.BillableSeconds))
		for _, pr := range co.Projects {
			pausedNote := ""
			if pr.Paused {
				pausedNote = "  (paused)"
			}
			fmt.Printf("    %-36s %s   (today: %s)%s\n",
				pr.Name,
				fmtDuration(pr.BillableSeconds),
				fmtDuration(pr.TodaySeconds),
				pausedNote,
			)
		}
		fmt.Println()
	}

	// Unassigned bucket.
	if reply.UnassignedBillableSeconds > 0 || reply.UnassignedTodaySeconds > 0 {
		sigNote := ""
		if reply.UnassignedSignaturesCount > 0 {
			sigNote = fmt.Sprintf(", %d signature(s), run `antitimely review`", reply.UnassignedSignaturesCount)
		}
		fmt.Printf("  %-38s %s   (today: %s%s)\n",
			"(unassigned)",
			fmtDuration(reply.UnassignedBillableSeconds),
			fmtDuration(reply.UnassignedTodaySeconds),
			sigNote,
		)
	}

	return 0
}

// fmtDuration formats a duration in seconds as "1h2m3s", or "0s" for zero.
func fmtDuration(seconds int64) string {
	if seconds <= 0 {
		return "0s"
	}
	return time.Duration(seconds * int64(time.Second)).String()
}
