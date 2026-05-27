package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/rian/antitimely/internal/rpcapi"
)

func cmdProject(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: antitimely project <add|list|delete|set-company|pause|resume|pause-all|resume-all> ...")
		return 64
	}
	switch args[0] {
	case "add":
		return projectAdd(args[1:])
	case "list":
		return projectList()
	case "delete":
		return projectDelete(args[1:])
	case "set-company":
		return projectSetCompany(args[1:])
	case "pause":
		return projectPause(args[1:])
	case "resume":
		return projectResume(args[1:])
	case "pause-all":
		return projectPauseAll()
	case "resume-all":
		return projectResumeAll()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: project %s\n", args[0])
		return 64
	}
}

func projectAdd(args []string) int {
	fs := flag.NewFlagSet("project add", flag.ExitOnError)
	company := fs.String("company", "", "Assign to a company (optional)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: antitimely project add [--company=<name>] <name>")
		return 64
	}
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	var reply rpcapi.ProjectAddReply
	if err := client.Call(rpcapi.ServiceName+".ProjectAdd",
		rpcapi.ProjectAddArgs{Name: fs.Arg(0), CompanyName: *company}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *company != "" {
		fmt.Printf("Created project %q under company %q (id=%d)\n", fs.Arg(0), *company, reply.ID)
	} else {
		fmt.Printf("Created project %q (id=%d)\n", fs.Arg(0), reply.ID)
	}
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
	fmt.Printf("%4s  %-30s %-20s %s\n", "ID", "NAME", "COMPANY", "STATUS")
	for _, p := range reply.Items {
		company := p.CompanyName
		if company == "" {
			company = "—"
		}
		status := "active"
		if p.Paused {
			status = "PAUSED"
		}
		fmt.Printf("%4d  %-30s %-20s %s\n", p.ID, p.Name, company, status)
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

func projectSetCompany(args []string) int {
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: antitimely project set-company <project> [<company>]   (omit company to unassign)")
		return 64
	}
	projectName := args[0]
	companyName := ""
	if len(args) == 2 {
		companyName = args[1]
	}
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	if err := client.Call(rpcapi.ServiceName+".ProjectSetCompany",
		rpcapi.ProjectSetCompanyArgs{ProjectName: projectName, CompanyName: companyName},
		&rpcapi.ProjectSetCompanyReply{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if companyName == "" {
		fmt.Printf("Unassigned project %q from any company\n", projectName)
	} else {
		fmt.Printf("Assigned project %q to company %q\n", projectName, companyName)
	}
	return 0
}

func projectPause(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: antitimely project pause <name>")
		return 64
	}
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	if err := client.Call(rpcapi.ServiceName+".ProjectPause",
		rpcapi.ProjectPauseArgs{Name: args[0]},
		&rpcapi.ProjectPauseReply{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Paused project %q\n", args[0])
	return 0
}

func projectResume(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: antitimely project resume <name>")
		return 64
	}
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	if err := client.Call(rpcapi.ServiceName+".ProjectResume",
		rpcapi.ProjectResumeArgs{Name: args[0]},
		&rpcapi.ProjectResumeReply{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Resumed project %q\n", args[0])
	return 0
}

func projectPauseAll() int {
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	var reply rpcapi.ProjectPauseAllReply
	if err := client.Call(rpcapi.ServiceName+".ProjectPauseAll",
		rpcapi.ProjectPauseAllArgs{}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Paused all projects (%d)\n", reply.Count)
	return 0
}

func projectResumeAll() int {
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	var reply rpcapi.ProjectResumeAllReply
	if err := client.Call(rpcapi.ServiceName+".ProjectResumeAll",
		rpcapi.ProjectResumeAllArgs{}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Resumed all projects (%d)\n", reply.Count)
	return 0
}
