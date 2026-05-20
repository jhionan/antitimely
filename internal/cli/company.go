package cli

import (
	"fmt"
	"os"

	"github.com/rian/antitimely/internal/rpcapi"
)

func cmdCompany(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: antitimely company <add|list|delete> ...")
		return 64
	}
	switch args[0] {
	case "add":
		return companyAdd(args[1:])
	case "list":
		return companyList()
	case "delete":
		return companyDelete(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: company %s\n", args[0])
		return 64
	}
}

func companyAdd(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: antitimely company add <name>")
		return 64
	}
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	var reply rpcapi.CompanyAddReply
	if err := client.Call(rpcapi.ServiceName+".CompanyAdd",
		rpcapi.CompanyAddArgs{Name: args[0]}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Created company %q (id=%d)\n", args[0], reply.ID)
	return 0
}

func companyList() int {
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	var reply rpcapi.CompanyListReply
	if err := client.Call(rpcapi.ServiceName+".CompanyList", rpcapi.CompanyListArgs{}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(reply.Items) == 0 {
		fmt.Println("(no companies)")
		return 0
	}
	for _, c := range reply.Items {
		fmt.Printf("%4d  %s\n", c.ID, c.Name)
	}
	return 0
}

func companyDelete(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: antitimely company delete <name>")
		return 64
	}
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	if err := client.Call(rpcapi.ServiceName+".CompanyDelete",
		rpcapi.CompanyDeleteArgs{Name: args[0]}, &rpcapi.CompanyDeleteReply{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Deleted company %q\n", args[0])
	return 0
}
