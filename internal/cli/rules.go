package cli

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/rian/antitimely/internal/domain"
	"github.com/rian/antitimely/internal/herdr"
	"github.com/rian/antitimely/internal/rpcapi"
)

func cmdRules(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: antitimely rules <list|add|delete> ...")
		return 64
	}
	switch args[0] {
	case "list":
		return rulesList()
	case "add":
		return rulesAdd(args[1:])
	case "delete":
		return rulesDelete(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: rules %s\n", args[0])
		return 64
	}
}

// rulesAdd creates a rule directly, without going through `atl review`'s
// unassigned-signature flow. All validation that can be done without the
// daemon (missing project, no match field, an invalid --cwd glob) happens
// before dialing, so a validation failure always exits 64 rather than 2
// (which would otherwise mask it whenever no daemon is reachable).
func rulesAdd(args []string) int {
	fs := flag.NewFlagSet("rules add", flag.ExitOnError)
	project := fs.String("project", "", "project name (required)")
	priority := fs.Int64("priority", 100, "lower number wins")
	bundle := fs.String("bundle", "", "match bundle id exactly")
	title := fs.String("title", "", "match window title substring")
	binary := fs.String("binary", "", "match binary name exactly")
	cwd := fs.String("cwd", "", "match cwd prefix, or a glob with * in the final segment")
	space := fs.String("space", "", "match herdr workspace id (see: antitimely spaces)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 64
	}
	if *project == "" {
		fmt.Fprintln(os.Stderr, "usage: antitimely rules add --project=<name> [--priority=N] [--bundle=..] [--title=..] [--binary=..] [--cwd=..] [--space=..]")
		return 64
	}
	if *bundle == "" && *title == "" && *binary == "" && *cwd == "" && *space == "" {
		fmt.Fprintln(os.Stderr, "at least one match field is required")
		return 64
	}
	if *cwd != "" {
		if err := domain.ValidateCwdPattern(*cwd); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 64
		}
	}
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	var reply rpcapi.RuleAddReply
	err := client.Call(rpcapi.ServiceName+".RuleAdd", rpcapi.RuleAddArgs{
		ProjectName:      *project,
		Priority:         *priority,
		MatchBundleID:    *bundle,
		MatchTitleSubstr: *title,
		MatchBinaryName:  *binary,
		MatchCWDPrefix:   *cwd,
		MatchSpaceID:     *space,
	}, &reply)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Added rule %d\n", reply.ID)
	return 0
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
	// A bare workspace id ("wN") says nothing about which client a rule
	// binds; resolve it to "name (id)" whenever herdr still knows the space.
	spaces := herdr.NewResolver(herdr.DefaultSessionPath())
	fmt.Printf("%-4s %-3s %-15s %-30s %-30s %-15s %-30s %s\n",
		"ID", "PRI", "PROJECT", "BUNDLE", "TITLE", "BINARY", "CWD-PREFIX", "SPACE")
	for _, r := range reply.Items {
		fmt.Printf("%-4d %-3d %-15s %-30s %-30s %-15s %-30s %s\n",
			r.ID, r.Priority, r.ProjectName,
			r.MatchBundleID, r.MatchTitleSubstr,
			r.MatchBinaryName, r.MatchCWDPrefix, spaceLabel(spaces, r.MatchSpaceID))
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
