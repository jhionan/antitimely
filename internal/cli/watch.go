package cli

import (
	"fmt"
	"net/rpc"
	"os"
	"strings"

	"github.com/rian/antitimely/internal/rpcapi"
)

func cmdWatch(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: antitimely watch <add|list|remove> ...")
		return 64
	}
	switch args[0] {
	case "list":
		return watchList()
	case "add":
		return watchAdd(args[1:])
	case "remove":
		return watchRemove(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: watch %s\n", args[0])
		return 64
	}
}

func watchList() int {
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()

	var reply rpcapi.WatchListReply
	if err := client.Call(rpcapi.ServiceName+".WatchList", rpcapi.WatchListArgs{}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(reply.Items) == 0 {
		fmt.Println("(no programs watched)")
		return 0
	}
	for _, item := range reply.Items {
		fmt.Printf("%-8s %s\n", item.Kind, item.Identifier)
	}
	return 0
}

func watchAdd(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: antitimely watch add <app|binary> <identifier>")
		return 64
	}
	kind := args[0]
	switch kind {
	case "app", "bundle":
		kind = "bundle"
	case "binary":
		// canonical
	default:
		fmt.Fprintln(os.Stderr, "kind must be 'app' (bundle id) or 'binary' (process name)")
		return 64
	}
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()

	if err := client.Call(rpcapi.ServiceName+".WatchAdd",
		rpcapi.WatchAddArgs{Kind: kind, Identifier: args[1]},
		&rpcapi.WatchAddReply{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Added: %s %s\n", kind, args[1])
	return 0
}

func watchRemove(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: antitimely watch remove <identifier>")
		return 64
	}
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()

	// The identifier could be either kind; try both, track which (if any)
	// actually existed. Reporting "Removed" when both calls failed would
	// silently lie to the user.
	var (
		removed []string
		lastErr error
	)
	for _, kind := range []string{"bundle", "binary"} {
		err := client.Call(rpcapi.ServiceName+".WatchRemove",
			rpcapi.WatchRemoveArgs{Kind: kind, Identifier: args[0]},
			&rpcapi.WatchRemoveReply{})
		if err == nil {
			removed = append(removed, kind)
		} else {
			lastErr = err
		}
	}
	if len(removed) == 0 {
		fmt.Fprintln(os.Stderr, "remove failed:", lastErr)
		return 1
	}
	fmt.Printf("Removed: %s (%s)\n", args[0], strings.Join(removed, ", "))
	return 0
}

// dialOrExit dials the daemon socket. Returns (nil, exitCode) on failure.
// Lives in watch.go since it's the first command that needs it; later
// subcommands import it from the same package.
func dialOrExit() (*rpc.Client, int) {
	sockPath, err := DefaultSocketPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return nil, 1
	}
	c, err := Dial(sockPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return nil, 2
	}
	return c, 0
}

// redial opens a fresh daemon connection without printing or exiting on
// failure. The live status view uses it to reconnect after a daemon restart
// drops the socket (the old *rpc.Client then permanently returns ErrShutdown).
func redial() (*rpc.Client, error) {
	sockPath, err := DefaultSocketPath()
	if err != nil {
		return nil, err
	}
	return Dial(sockPath)
}
