package cli

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/rian/antitimely/internal/rpcapi"
)

func cmdRules(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: antitimely rules <list|delete> ...")
		return 64
	}
	switch args[0] {
	case "list":
		return rulesList()
	case "delete":
		return rulesDelete(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: rules %s\n", args[0])
		return 64
	}
}

func rulesList() int {
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	var reply rpcapi.RulesListReply
	if err := client.Call(rpcapi.ServiceName+".RulesList", rpcapi.RulesListArgs{}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(reply.Items) == 0 {
		fmt.Println("(no rules)")
		return 0
	}
	fmt.Printf("%-4s %-3s %-15s %-30s %-30s %-15s %s\n",
		"ID", "PRI", "PROJECT", "BUNDLE", "TITLE", "BINARY", "CWD-PREFIX")
	for _, r := range reply.Items {
		fmt.Printf("%-4d %-3d %-15s %-30s %-30s %-15s %s\n",
			r.ID, r.Priority, r.ProjectName,
			r.MatchBundleID, r.MatchTitleSubstr,
			r.MatchBinaryName, r.MatchCWDPrefix)
	}
	return 0
}

func rulesDelete(args []string) int {
	fs := flag.NewFlagSet("rules delete", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: antitimely rules delete <id>")
		return 64
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 64
	}
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	if err := client.Call(rpcapi.ServiceName+".RuleDelete",
		rpcapi.RuleDeleteArgs{ID: id}, &rpcapi.RuleDeleteReply{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Deleted rule %d\n", id)
	return 0
}
