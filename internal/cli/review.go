package cli

import (
	"bufio"
	"fmt"
	"net/rpc"
	"os"
	"strconv"
	"strings"

	"github.com/rian/antitimely/internal/domain"
	"github.com/rian/antitimely/internal/rpcapi"
)

func cmdReview(args []string) int {
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()

	stdin := bufio.NewScanner(os.Stdin)

	for {
		var pending rpcapi.PendingReviewReply
		if err := client.Call(rpcapi.ServiceName+".PendingReview",
			rpcapi.PendingReviewArgs{Limit: 10}, &pending); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if len(pending.Signatures) == 0 {
			fmt.Println("Nothing to review.")
			return 0
		}

		fmt.Printf("\n%d unassigned signatures:\n", len(pending.Signatures))
		for i, sig := range pending.Signatures {
			fmt.Printf("  [%d] %d ticks — %s\n", i+1, sig.Ticks, describeSignature(sig))
		}
		fmt.Printf("\nSelect [1-%d, q to quit]: ", len(pending.Signatures))
		if !stdin.Scan() {
			return 0
		}
		choice := strings.TrimSpace(stdin.Text())
		if choice == "q" || choice == "" {
			return 0
		}
		idx, err := strconv.Atoi(choice)
		if err != nil || idx < 1 || idx > len(pending.Signatures) {
			fmt.Println("  invalid choice")
			continue
		}
		sig := pending.Signatures[idx-1]
		if exit := handleOneSignature(client, stdin, sig); exit < 0 {
			return 0
		}
	}
}

func describeSignature(sig rpcapi.Signature) string {
	if sig.Source == "agent" {
		return fmt.Sprintf("binary=%q cwd=%q", sig.BinaryName, sig.CWD)
	}
	return fmt.Sprintf("bundle=%q title=%q", sig.BundleID, sig.WindowTitle)
}

func handleOneSignature(client *rpc.Client, stdin *bufio.Scanner, sig rpcapi.Signature) int {
	var projects rpcapi.ProjectListReply
	if err := client.Call(rpcapi.ServiceName+".ProjectList", rpcapi.ProjectListArgs{}, &projects); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 0
	}

	fmt.Printf("\nTag as which project?\n")
	for i, p := range projects.Items {
		fmt.Printf("  [%d] %s\n", i+1, p.Name)
	}
	fmt.Println("  [n] new project")
	fmt.Println("  [i] ignore forever")
	fmt.Println("  [s] skip")
	fmt.Print("Choice: ")
	if !stdin.Scan() {
		return -1
	}
	c := strings.TrimSpace(stdin.Text())

	switch c {
	case "s":
		return 0
	case "i":
		_ = client.Call(rpcapi.ServiceName+".IgnoreSignature",
			rpcapi.IgnoreSignatureArgs{ObservationID: sig.ObservationID},
			&rpcapi.IgnoreSignatureReply{})
		fmt.Println("  ignored.")
		return 0
	case "n":
		fmt.Print("New project name: ")
		if !stdin.Scan() {
			return -1
		}
		name := strings.TrimSpace(stdin.Text())
		return applyTag(client, stdin, sig, name, true)
	default:
		idx, err := strconv.Atoi(c)
		if err != nil || idx < 1 || idx > len(projects.Items) {
			fmt.Println("  invalid choice")
			return 0
		}
		return applyTag(client, stdin, sig, projects.Items[idx-1].Name, false)
	}
}

func applyTag(client *rpc.Client, stdin *bufio.Scanner, sig rpcapi.Signature, projectName string, create bool) int {
	obs := domain.Observation{
		Source:      domain.Source(sig.Source),
		BundleID:    sig.BundleID,
		WindowTitle: sig.WindowTitle,
		BinaryName:  sig.BinaryName,
		Cwd:         sig.CWD,
	}
	prop := domain.ProposeRule(obs, projectName)

	fmt.Println("\nProposed rule:")
	printProposed(prop)
	fmt.Print("[y] accept & retag past ticks  [n] tag only this signature, no rule: ")
	if !stdin.Scan() {
		return -1
	}
	c := strings.TrimSpace(stdin.Text())

	args := rpcapi.TagSignatureArgs{
		ObservationID: sig.ObservationID,
		ProjectName:   projectName,
		CreateProject: create,
	}
	if c == "y" || c == "" {
		args.Rule = &rpcapi.ProposedRule{
			Priority:         prop.Priority,
			MatchBundleID:    strDeref(prop.MatchBundleID),
			MatchTitleSubstr: strDeref(prop.MatchTitleSubstr),
			MatchBinaryName:  strDeref(prop.MatchBinaryName),
			MatchCWDPrefix:   strDeref(prop.MatchCwdPrefix),
		}
	}

	var reply rpcapi.TagSignatureReply
	if err := client.Call(rpcapi.ServiceName+".TagSignature", args, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 0
	}
	if reply.RuleCreated {
		fmt.Printf("  Created rule. Retroactively tagged %d ticks.\n", reply.TicksRetagged)
	} else {
		fmt.Println("  Tagged this signature only.")
	}
	return 0
}

func printProposed(p domain.ProposedRule) {
	if p.MatchBundleID != nil {
		fmt.Printf("    bundle = %q\n", *p.MatchBundleID)
	}
	if p.MatchTitleSubstr != nil {
		fmt.Printf("    title contains %q\n", *p.MatchTitleSubstr)
	}
	if p.MatchBinaryName != nil {
		fmt.Printf("    binary = %q\n", *p.MatchBinaryName)
	}
	if p.MatchCwdPrefix != nil {
		fmt.Printf("    cwd has prefix %q\n", *p.MatchCwdPrefix)
	}
}

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
