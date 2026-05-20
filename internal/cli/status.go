package cli

import (
	"fmt"
	"os"
	"sort"
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

	fmt.Printf("Idle: %s\n", time.Duration(reply.UserIdleSeconds)*time.Second)
	fmt.Printf("Tick interval: %ds\n", reply.TickIntervalSeconds)
	fmt.Printf("Permission: %s\n", reply.PermissionState)
	if reply.PermissionState == "accessibility_denied" {
		fmt.Fprintln(os.Stderr,
			"  ⚠ Window-title capture disabled. Enable in System Settings → Privacy & Security → Automation → antitimely → System Events.")
	}
	fmt.Println()
	fmt.Println("Today:")
	if len(reply.TodayTotalsSeconds) == 0 {
		fmt.Println("  (no project time tracked yet)")
	} else {
		names := make([]string, 0, len(reply.TodayTotalsSeconds))
		for k := range reply.TodayTotalsSeconds {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Printf("  %-30s %s\n", n, time.Duration(reply.TodayTotalsSeconds[n])*time.Second)
		}
	}
	if reply.UnassignedTodaySeconds > 0 {
		fmt.Printf("  %-30s %s  (%d signatures, run `antitimely review`)\n",
			"(unassigned)", time.Duration(reply.UnassignedTodaySeconds)*time.Second,
			reply.UnassignedSignaturesCount)
	}
	return 0
}
