package cli

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/rian/antitimely/internal/rpcapi"
)

// parseCompanyChoice maps a user's typed selection (a 1-based index) to a
// company name. Returns ok=false for blank, "b" (back), non-numeric, or
// out-of-range input.
func parseCompanyChoice(items []rpcapi.Company, input string) (string, bool) {
	s := strings.TrimSpace(input)
	if s == "" || s == "b" {
		return "", false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > len(items) {
		return "", false
	}
	return items[n-1].Name, true
}

// pickCompany fetches the company list, prints it numbered, and reads a
// selection. Returns (name, true) on a valid pick, ("", false) otherwise
// (blank, "b", invalid, empty list, or RPC failure).
func pickCompany(stdin *bufio.Scanner) (string, bool) {
	client, code := dialOrExit()
	if client == nil {
		_ = code
		return "", false
	}
	defer client.Close()

	var reply rpcapi.CompanyListReply
	if err := client.Call(rpcapi.ServiceName+".CompanyList", rpcapi.CompanyListArgs{}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return "", false
	}
	if len(reply.Items) == 0 {
		fmt.Println("  (no companies — add one first)")
		return "", false
	}
	fmt.Println("Select a company:")
	for i, c := range reply.Items {
		fmt.Printf("  [%d] %s\n", i+1, c.Name)
	}
	choice, ok := promptLine(stdin, "Choice: ")
	if !ok {
		return "", false
	}
	return parseCompanyChoice(reply.Items, choice)
}
