package cli

import (
	"fmt"
	"os"

	"github.com/rian/antitimely/internal/rpcapi"
)

func cmdProject(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: antitimely project <add|list|delete> ...")
		return 64
	}
	switch args[0] {
	case "add":
		return projectAdd(args[1:])
	case "list":
		return projectList()
	case "delete":
		return projectDelete(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: project %s\n", args[0])
		return 64
	}
}

func projectAdd(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: antitimely project add <name>")
		return 64
	}
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	var reply rpcapi.ProjectAddReply
	if err := client.Call(rpcapi.ServiceName+".ProjectAdd",
		rpcapi.ProjectAddArgs{Name: args[0]}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Created project %q (id=%d)\n", args[0], reply.ID)
	return 0
}

func projectList() int {
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	var reply rpcapi.ProjectListReply
	if err := client.Call(rpcapi.ServiceName+".ProjectList", rpcapi.ProjectListArgs{}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(reply.Items) == 0 {
		fmt.Println("(no projects)")
		return 0
	}
	for _, p := range reply.Items {
		fmt.Printf("%4d  %s\n", p.ID, p.Name)
	}
	return 0
}

func projectDelete(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: antitimely project delete <name>")
		return 64
	}
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	if err := client.Call(rpcapi.ServiceName+".ProjectDelete",
		rpcapi.ProjectDeleteArgs{Name: args[0]}, &rpcapi.ProjectDeleteReply{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Deleted project %q\n", args[0])
	return 0
}
