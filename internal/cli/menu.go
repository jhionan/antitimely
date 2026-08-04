package cli

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/rian/antitimely/internal/invoice"
	"github.com/rian/antitimely/internal/rpcapi"
)

// IsStdinTerminal reports whether stdin appears to be an interactive terminal.
// Returns false if stdin is a pipe, file, or other non-tty.
func IsStdinTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// IsStdoutTerminal reports whether stdout appears to be an interactive terminal.
// Returns false if stdout is a pipe, file, or other non-tty.
func IsStdoutTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// RunMenu launches the top-level interactive menu and loops until the user quits.
func RunMenu() int {
	stdin := bufio.NewScanner(os.Stdin)
	for {
		fmt.Println()
		fmt.Println("antitimely — work time tracker")
		fmt.Println()
		fmt.Println("  [1] Status")
		fmt.Println("  [2] Review unassigned signatures")
		fmt.Println("  [3] Report (today)")
		fmt.Println("  [s] Summary (week with git commits)")
		fmt.Println("  [4] Watch programs")
		fmt.Println("  [5] Projects")
		fmt.Println("  [6] Companies")
		fmt.Println("  [N] Invoices")
		fmt.Println("  [7] Rules")
		fmt.Println("  [8] Config (init / show)")
		fmt.Println("  [9] Reset (wipe data)")
		fmt.Println("  [E] End day (pause all projects)")
		fmt.Println("  [R] Resume all projects")
		fmt.Println("  [D] Restart daemon")
		fmt.Println("  [A] Grant accessibility (open Settings)")
		fmt.Println("  [h] Help (full CLI usage)")
		fmt.Println("  [q] Quit")
		fmt.Print("\nChoice: ")
		if !stdin.Scan() {
			return 0
		}
		choice := strings.TrimSpace(stdin.Text())
		switch choice {
		case "1":
			cmdStatus(nil)
		case "2":
			cmdReview(nil)
		case "3":
			cmdReport(nil)
		case "s", "S":
			cmdSummary(nil)
		case "4":
			watchMenu(stdin)
		case "5":
			projectMenu(stdin)
		case "6":
			companyMenu(stdin)
		case "N", "n":
			invoiceMenu(stdin)
		case "7":
			rulesMenu(stdin)
		case "8":
			configMenu(stdin)
		case "9":
			resetMenu(stdin)
		case "E", "e":
			projectPauseAll()
		case "R", "r":
			projectResumeAll()
		case "D", "d":
			cmdRestartDaemon(nil)
		case "A", "a":
			cmdGrantAccessibility(nil)
		case "h", "help":
			printUsage(os.Stdout)
		case "q", "Q", "":
			return 0
		default:
			fmt.Println("  invalid choice")
		}
	}
}

// promptLine prints a prompt and reads one trimmed line. Returns (text, ok).
// ok is false on EOF.
func promptLine(stdin *bufio.Scanner, label string) (string, bool) {
	fmt.Print(label)
	if !stdin.Scan() {
		return "", false
	}
	return strings.TrimSpace(stdin.Text()), true
}

func watchMenu(stdin *bufio.Scanner) {
	for {
		fmt.Println()
		fmt.Println("Watch programs:")
		fmt.Println("  [1] List watched programs")
		fmt.Println("  [2] Add app (by bundle id)")
		fmt.Println("  [3] Add binary (by process name)")
		fmt.Println("  [4] Remove")
		fmt.Println("  [b] Back")
		choice, ok := promptLine(stdin, "Choice: ")
		if !ok {
			return
		}
		switch choice {
		case "1":
			watchList()
		case "2":
			id, ok := promptLine(stdin, "Bundle id (e.g. com.google.antigravity-ide): ")
			if !ok || id == "" {
				continue
			}
			watchAdd([]string{"app", id})
		case "3":
			name, ok := promptLine(stdin, "Binary name (e.g. claude): ")
			if !ok || name == "" {
				continue
			}
			watchAdd([]string{"binary", name})
		case "4":
			id, ok := promptLine(stdin, "Identifier to remove: ")
			if !ok || id == "" {
				continue
			}
			watchRemove([]string{id})
		case "b", "":
			return
		default:
			fmt.Println("  invalid choice")
		}
	}
}

func projectMenu(stdin *bufio.Scanner) {
	for {
		fmt.Println()
		fmt.Println("Projects:")
		fmt.Println("  [1] List projects")
		fmt.Println("  [2] Add project")
		fmt.Println("  [3] Delete project")
		fmt.Println("  [4] Assign company to project")
		fmt.Println("  [5] Pause project")
		fmt.Println("  [6] Resume project")
		fmt.Println("  [7] Pause all projects")
		fmt.Println("  [8] Resume all projects")
		fmt.Println("  [b] Back")
		choice, ok := promptLine(stdin, "Choice: ")
		if !ok {
			return
		}
		switch choice {
		case "1":
			projectList()
		case "2":
			name, ok := promptLine(stdin, "Project name: ")
			if !ok || name == "" {
				continue
			}
			company, _ := promptLine(stdin, "Company (blank for none): ")
			if company == "" {
				projectAdd([]string{name})
			} else {
				projectAdd([]string{"--company=" + company, name})
			}
		case "3":
			name, ok := promptLine(stdin, "Project name to delete: ")
			if !ok || name == "" {
				continue
			}
			projectDelete([]string{name})
		case "4":
			name, ok := promptLine(stdin, "Project name: ")
			if !ok || name == "" {
				continue
			}
			company, _ := promptLine(stdin, "Company (blank to unassign): ")
			if company == "" {
				projectSetCompany([]string{name})
			} else {
				projectSetCompany([]string{name, company})
			}
		case "5":
			name, ok := promptLine(stdin, "Project name to pause: ")
			if !ok || name == "" {
				continue
			}
			projectPause([]string{name})
		case "6":
			name, ok := promptLine(stdin, "Project name to resume: ")
			if !ok || name == "" {
				continue
			}
			projectResume([]string{name})
		case "7":
			projectPauseAll()
		case "8":
			projectResumeAll()
		case "b", "":
			return
		default:
			fmt.Println("  invalid choice")
		}
	}
}

func companyMenu(stdin *bufio.Scanner) {
	for {
		fmt.Println()
		fmt.Println("Companies:")
		fmt.Println("  [1] List companies")
		fmt.Println("  [2] Add company")
		fmt.Println("  [3] Delete company")
		fmt.Println("  [b] Back")
		choice, ok := promptLine(stdin, "Choice: ")
		if !ok {
			return
		}
		switch choice {
		case "1":
			companyList()
		case "2":
			name, ok := promptLine(stdin, "Company name: ")
			if !ok || name == "" {
				continue
			}
			companyAdd([]string{name})
		case "3":
			name, ok := promptLine(stdin, "Company name to delete: ")
			if !ok || name == "" {
				continue
			}
			companyDelete([]string{name})
		case "b", "":
			return
		default:
			fmt.Println("  invalid choice")
		}
	}
}

func invoiceMenu(stdin *bufio.Scanner) {
	for {
		fmt.Println()
		fmt.Println("Invoices:")
		fmt.Println("  [1] List all invoices")
		fmt.Println("  [2] Generate invoice (PDF)")
		fmt.Println("  [3] Issue advance (prepayment)")
		fmt.Println("  [4] Delete invoice")
		fmt.Println("  [5] Record anchor only (advanced)")
		fmt.Println("  [b] Back")
		choice, ok := promptLine(stdin, "Choice: ")
		if !ok {
			return
		}
		switch choice {
		case "1":
			invoiceList(nil)
		case "2":
			invoiceGenerateFlow(stdin)
		case "3":
			invoiceAdvanceFlow(stdin)
		case "4":
			idStr, ok := promptLine(stdin, "Invoice ID to delete: ")
			if !ok || idStr == "" {
				continue
			}
			invoiceDelete([]string{idStr})
		case "5":
			company, ok := pickCompany(stdin)
			if !ok {
				continue
			}
			at, _ := promptLine(stdin, "Date (YYYY-MM-DD, blank for now): ")
			note, _ := promptLine(stdin, "Note (blank for none): ")
			sendArgs := []string{company}
			if at != "" {
				sendArgs = append([]string{"--at=" + at}, sendArgs...)
			}
			if note != "" {
				sendArgs = append([]string{"--note=" + note}, sendArgs...)
			}
			invoiceSend(sendArgs)
		case "b", "":
			return
		default:
			fmt.Println("  invalid choice")
		}
	}
}

// invoiceGenerateFlow: pick a company, preview a dry-run, confirm, then
// generate the real PDF and open + reveal it.
func invoiceGenerateFlow(stdin *bufio.Scanner) {
	company, ok := pickCompany(stdin)
	if !ok {
		return
	}

	preview, err := invoiceGenerateRPC(rpcapi.InvoiceGenerateArgs{CompanyName: company, DryRun: true})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	// The menu dry-run exists only to build the preview; it never opens the
	// rendered PDF, so remove the daemon's throwaway temp file.
	if preview.PDFPath != "" {
		_ = os.Remove(preview.PDFPath)
	}
	fmt.Printf("\nAbout to generate:\n  %s\n", formatInvoicePreview(preview))

	confirm, ok := promptLine(stdin, "Generate this invoice? [y/N]: ")
	if !ok || strings.ToLower(strings.TrimSpace(confirm)) != "y" {
		fmt.Println("  cancelled — nothing generated")
		return
	}

	reply, err := invoiceGenerateRPC(rpcapi.InvoiceGenerateArgs{CompanyName: company})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	fmt.Printf("Generated %s — %s\n", reply.Number, reply.PDFPath)
	openAndReveal(reply.PDFPath)
}

// parseAdvanceAmount wraps parseMoneyCents with the advance-specific rule that
// the amount must be strictly positive.
func parseAdvanceAmount(s string) (int64, error) {
	cents, err := parseMoneyCents(s)
	if err != nil {
		return 0, err
	}
	if cents <= 0 {
		return 0, fmt.Errorf("advance amount must be positive")
	}
	return cents, nil
}

// companyBillableForAdvance reports whether a company's billing setup (as
// reported by InvoiceBalance) is complete enough to issue an advance.
// GetCompanyForInvoice returns a row for any company that exists by name —
// it only errors on an unknown name, not on missing billing config — so an
// existing-but-unconfigured company comes back with a zero-value currency
// and rate. SetCompanyBilling always sets billing_mode, currency, rate_cents
// and billed_from together in one statement, so either signal going missing
// reliably means billing was never (or only partially) configured.
func companyBillableForAdvance(currency string, rateCents int64) bool {
	return currency != "" && rateCents > 0
}

// invoiceAdvanceFlow: pick a company, look up its currency, prompt for a
// prepayment amount, preview and confirm, then issue the advance and open +
// reveal the PDF. Confirmation happens before the RPC call because issuing
// burns an invoice number that is never reclaimed.
//
// The billing-config check below happens right after the currency lookup
// and before the amount prompt: without it, a company that exists but has
// no currency/rate/sender configured would show a blank "Advance amount ():"
// prompt, let the operator sit through a full preview + confirm, and only
// fail once InvoiceAdvance rejects it at the very end.
func invoiceAdvanceFlow(stdin *bufio.Scanner) {
	company, ok := pickCompany(stdin)
	if !ok {
		return
	}

	client, code := dialOrExit()
	if client == nil {
		_ = code
		return
	}
	var balance rpcapi.InvoiceBalanceReply
	err := client.Call(rpcapi.ServiceName+".InvoiceBalance", rpcapi.InvoiceBalanceArgs{CompanyName: company}, &balance)
	client.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if !companyBillableForAdvance(balance.Currency, balance.RateCents) {
		fmt.Fprintf(os.Stderr,
			"company %q has no billing configured (currency, rate, and sender all need to be set) — cannot issue an advance\n",
			company)
		return
	}

	raw, ok := promptLine(stdin, "Advance amount ("+balance.Currency+"): ")
	if !ok {
		return
	}
	cents, err := parseAdvanceAmount(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}

	fmt.Printf("\nAbout to issue:\n  %s advance to %s\n", invoice.FormatMoney(cents, balance.Currency), company)
	confirm, ok := promptLine(stdin, "Issue this advance? This burns an invoice number. [y/N]: ")
	if !ok || strings.ToLower(strings.TrimSpace(confirm)) != "y" {
		fmt.Println("  cancelled — nothing issued")
		return
	}

	advClient, code := dialOrExit()
	if advClient == nil {
		_ = code
		return
	}
	defer advClient.Close()
	var reply rpcapi.InvoiceAdvanceReply
	if err := advClient.Call(rpcapi.ServiceName+".InvoiceAdvance", rpcapi.InvoiceAdvanceArgs{
		CompanyName: company,
		AmountCents: cents,
	}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	fmt.Printf("Recorded advance %s — %s (%s)\n", reply.Number, reply.PDFPath,
		invoice.FormatMoney(reply.TotalCents, reply.Currency))
	fmt.Printf("Credit remaining: %s\n", invoice.FormatMoney(reply.CreditRemainingCents, reply.Currency))
	openAndReveal(reply.PDFPath)
}

func rulesMenu(stdin *bufio.Scanner) {
	for {
		fmt.Println()
		fmt.Println("Rules:")
		fmt.Println("  [1] List rules")
		fmt.Println("  [2] Delete rule")
		fmt.Println("  [b] Back")
		choice, ok := promptLine(stdin, "Choice: ")
		if !ok {
			return
		}
		switch choice {
		case "1":
			rulesList()
		case "2":
			idStr, ok := promptLine(stdin, "Rule ID to delete: ")
			if !ok || idStr == "" {
				continue
			}
			if _, err := strconv.ParseInt(idStr, 10, 64); err != nil {
				fmt.Fprintln(os.Stderr, "invalid id")
				continue
			}
			rulesDelete([]string{idStr})
		case "b", "":
			return
		default:
			fmt.Println("  invalid choice")
		}
	}
}

func resetMenu(stdin *bufio.Scanner) {
	fmt.Println()
	fmt.Println("Reset:")
	fmt.Println("  [1] Wipe time-tracking data only (preserve projects/companies/rules/watched)")
	fmt.Println("  [2] Wipe EVERYTHING (full reset)")
	fmt.Println("  [b] Back")
	choice, ok := promptLine(stdin, "Choice: ")
	if !ok {
		return
	}
	switch choice {
	case "1":
		cmdReset([]string{"ticks"})
	case "2":
		cmdReset([]string{"all"})
	case "b", "":
		return
	default:
		fmt.Println("  invalid choice")
	}
}

func configMenu(stdin *bufio.Scanner) {
	for {
		fmt.Println()
		fmt.Println("Config:")
		fmt.Println("  [1] Show current config file")
		fmt.Println("  [2] Init default config file")
		fmt.Println("  [3] Print config file path")
		fmt.Println("  [b] Back")
		choice, ok := promptLine(stdin, "Choice: ")
		if !ok {
			return
		}
		switch choice {
		case "1":
			configShow()
		case "2":
			configInit()
		case "3":
			configPath()
		case "b", "":
			return
		default:
			fmt.Println("  invalid choice")
		}
	}
}
