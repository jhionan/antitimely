package cli

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
)

func cmdReport(args []string) int {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	fromStr := fs.String("from", "", "Inclusive start date YYYY-MM-DD (default today)")
	toStr := fs.String("to", "", "Exclusive end date YYYY-MM-DD (default tomorrow)")
	fs.Parse(args)

	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.Add(24 * time.Hour)
	if *fromStr != "" {
		t, err := time.ParseInLocation("2006-01-02", *fromStr, now.Location())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 64
		}
		start = t
	}
	if *toStr != "" {
		t, err := time.ParseInLocation("2006-01-02", *toStr, now.Location())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 64
		}
		end = t
	}

	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	var reply rpcapi.ReportReply
	if err := client.Call(rpcapi.ServiceName+".Report",
		rpcapi.ReportArgs{FromUnix: start.Unix(), ToUnix: end.Unix()}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	fmt.Printf("Report: %s → %s\n", start.Format("2006-01-02"), end.Format("2006-01-02"))
	names := make([]string, 0, len(reply.Totals))
	for k := range reply.Totals {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("  %-30s %s\n", n, time.Duration(reply.Totals[n])*time.Second)
	}
	if reply.Unassigned > 0 {
		fmt.Printf("  %-30s %s\n", "(unassigned)", time.Duration(reply.Unassigned)*time.Second)
	}
	return 0
}
