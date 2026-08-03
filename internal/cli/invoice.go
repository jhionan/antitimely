package cli

import (
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rian/antitimely/internal/invoice"
	"github.com/rian/antitimely/internal/rpcapi"
)

func cmdInvoice(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: antitimely invoice <send|list|delete|generate|advance|show-senders|setup> ...")
		return 64
	}
	switch args[0] {
	case "send":
		return invoiceSend(args[1:])
	case "list":
		return invoiceList(args[1:])
	case "delete":
		return invoiceDelete(args[1:])
	case "generate":
		return invoiceGenerate(args[1:])
	case "advance":
		return invoiceAdvance(args[1:])
	case "show-senders":
		return invoiceShowSenders(args[1:])
	case "setup":
		return invoiceSetup(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: invoice %s\n", args[0])
		return 64
	}
}

func invoiceSend(args []string) int {
	fs := flag.NewFlagSet("invoice send", flag.ExitOnError)
	at := fs.String("at", "", "Backdate: YYYY-MM-DD or YYYY-MM-DD HH:MM (default: now)")
	note := fs.String("note", "", "Optional note")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: antitimely invoice send [--at=YYYY-MM-DD] [--note=...] <company>")
		return 64
	}
	company := fs.Arg(0)

	var sentAt int64
	if *at != "" {
		formats := []string{"2006-01-02", "2006-01-02 15:04", "2006-01-02T15:04:05"}
		var t time.Time
		var parseErr error
		for _, f := range formats {
			t, parseErr = time.ParseInLocation(f, *at, time.Local)
			if parseErr == nil {
				break
			}
		}
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "invalid --at value %q (try YYYY-MM-DD)\n", *at)
			return 64
		}
		sentAt = t.Unix()
	}

	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	var reply rpcapi.InvoiceSendReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceSend",
		rpcapi.InvoiceSendArgs{CompanyName: company, SentAtUnix: sentAt, Note: *note}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	when := "now"
	if *at != "" {
		when = *at
	}
	fmt.Printf("Recorded invoice #%d for company %q at %s\n", reply.ID, company, when)
	return 0
}

func invoiceList(args []string) int {
	fs := flag.NewFlagSet("invoice list", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 64
	}
	company := ""
	if fs.NArg() == 1 {
		company = fs.Arg(0)
	}

	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	var reply rpcapi.InvoiceListReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceList",
		rpcapi.InvoiceListArgs{CompanyName: company}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(reply.Items) == 0 {
		fmt.Println("(no invoices)")
		return 0
	}
	fmt.Printf("%-4s  %-20s  %-20s  %s\n", "ID", "COMPANY", "SENT", "NOTE")
	for _, i := range reply.Items {
		fmt.Printf("%-4d  %-20s  %-20s  %s\n", i.ID, i.CompanyName,
			time.Unix(i.SentAtUnix, 0).Local().Format("2006-01-02 15:04"),
			i.Note)
	}
	return 0
}

func invoiceDelete(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: antitimely invoice delete <id>")
		return 64
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid id")
		return 64
	}
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	if err := client.Call(rpcapi.ServiceName+".InvoiceDelete",
		rpcapi.InvoiceDeleteArgs{ID: id}, &rpcapi.InvoiceDeleteReply{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Deleted invoice #%d\n", id)
	return 0
}

func invoiceGenerate(args []string) int {
	fs := flag.NewFlagSet("invoice generate", flag.ExitOnError)
	from := fs.String("from", "", "Period start: YYYY-MM-DD (defaults per billing mode)")
	to := fs.String("to", "", "Period end: YYYY-MM-DD (defaults per billing mode)")
	issueDate := fs.String("issue-date", "", "Date on the invoice: YYYY-MM-DD (default: today)")
	note := fs.String("note", "", "Stored in invoices.note (not printed on PDF)")
	dryRun := fs.Bool("dry-run", false, "Render PDF to a temp file without DB writes")
	allowEmpty := fs.Bool("allow-empty", false, "For hourly mode, allow 0-tick periods")
	discount := fs.String("discount", "", "Flat discount in the company's currency, e.g. 50 or 50.00")
	noCredit := fs.Bool("no-credit", false, "Do not apply any outstanding advance credit")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 64
	}
	discountCents, err := parseMoneyCents(*discount)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid --discount:", err)
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: antitimely invoice generate <company> [--from=YYYY-MM-DD] [--to=YYYY-MM-DD] [--issue-date=YYYY-MM-DD] [--note=...] [--discount=AMOUNT] [--no-credit] [--dry-run] [--allow-empty]")
		return 64
	}
	company := fs.Arg(0)
	fromUnix, err := parseOptionalDate(*from)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid --from:", err)
		return 64
	}
	toUnix, err := parseOptionalDate(*to)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid --to:", err)
		return 64
	}
	issueUnix, err := parseOptionalDate(*issueDate)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid --issue-date:", err)
		return 64
	}

	reply, err := invoiceGenerateRPC(rpcapi.InvoiceGenerateArgs{
		CompanyName:   company,
		FromUnix:      fromUnix,
		ToUnix:        toUnix,
		IssueDateUnix: issueUnix,
		Note:          *note,
		DryRun:        *dryRun,
		AllowEmpty:    *allowEmpty,
		DiscountCents: discountCents,
		NoCredit:      *noCredit,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	tag := ""
	if *dryRun {
		tag = " (dry-run)"
	}
	fmt.Printf("Generated %s%s — %s\n", reply.Number, tag, reply.PDFPath)
	if reply.CreditAppliedCents > 0 {
		fmt.Printf("Advance applied: %s (remaining %s)\n",
			invoice.FormatMoney(reply.CreditAppliedCents, reply.Currency),
			invoice.FormatMoney(reply.CreditRemainingCents, reply.Currency))
	}
	openAndReveal(reply.PDFPath)
	return 0
}

func invoiceAdvance(args []string) int {
	fs := flag.NewFlagSet("invoice advance", flag.ExitOnError)
	amount := fs.String("amount", "", "Prepayment amount in the company's currency, e.g. 20000 or 20000.00 (required)")
	note := fs.String("note", "", "Stored in invoices.note (not printed on PDF)")
	issueDate := fs.String("issue-date", "", "Date on the invoice: YYYY-MM-DD (default: today)")
	dryRun := fs.Bool("dry-run", false, "Render PDF to a temp file without DB writes")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: antitimely invoice advance <company> --amount=AMOUNT [--note=...] [--issue-date=YYYY-MM-DD] [--dry-run]")
		return 64
	}
	company := fs.Arg(0)
	amountCents, err := parseMoneyCents(*amount)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid --amount:", err)
		return 64
	}
	if amountCents <= 0 {
		fmt.Fprintln(os.Stderr, "--amount is required and must be positive")
		return 64
	}
	issueUnix, err := parseOptionalDate(*issueDate)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid --issue-date:", err)
		return 64
	}

	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()
	var reply rpcapi.InvoiceAdvanceReply
	if err := client.Call(rpcapi.ServiceName+".InvoiceAdvance", rpcapi.InvoiceAdvanceArgs{
		CompanyName:   company,
		AmountCents:   amountCents,
		Note:          *note,
		IssueDateUnix: issueUnix,
		DryRun:        *dryRun,
	}, &reply); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	tag := ""
	if *dryRun {
		tag = " (dry-run)"
	}
	fmt.Printf("Recorded advance %s%s — %s (%s)\n", reply.Number, tag, reply.PDFPath,
		invoice.FormatMoney(reply.TotalCents, reply.Currency))
	fmt.Printf("Credit remaining: %s\n", invoice.FormatMoney(reply.CreditRemainingCents, reply.Currency))
	openAndReveal(reply.PDFPath)
	return 0
}

func invoiceShowSenders(args []string) int {
	_ = args
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cfgPath := filepath.Join(home, ".antitimely", "config.yaml")
	if env := os.Getenv("ANTITIMELY_CONFIG"); env != "" {
		cfgPath = env
	}
	cfg, err := invoice.LoadSendersConfig(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	issues := cfg.Validate()
	fmt.Printf("Config: %s\n\n", cfgPath)
	for key, s := range cfg.Senders {
		fmt.Printf("[%s] %s\n  %s %s\n  %s\n  Invoice cursor (config seed): %s%0*d\n",
			key, s.LegalName, s.TaxIDLabel, s.TaxID,
			strings.Join(s.AddressLines, ", "),
			s.Invoice.NumberPrefix, s.Invoice.NumberPad, s.Invoice.NextNumber)
		for ccy, bk := range s.BankAccounts {
			extra := ""
			if len(bk.AlsoAccepts) > 0 {
				extra = " (also accepts: " + strings.Join(bk.AlsoAccepts, ", ") + ")"
			}
			fmt.Printf("  Bank for %s%s: %d field(s)\n", ccy, extra, len(bk.Fields))
		}
		fmt.Println()
	}
	if len(issues) > 0 {
		fmt.Println("Issues:")
		for _, iss := range issues {
			fmt.Println("  -", iss)
		}
		return 1
	}
	fmt.Println("Config OK.")
	return 0
}

// parseMoneyCents parses a decimal amount ("50", "50.00", "50.5") into integer
// cents. Empty string ⇒ 0 (no amount). Rejects negatives and >2 decimal places
// so we never silently drop sub-cent precision.
func parseMoneyCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	if f < 0 {
		return 0, fmt.Errorf("must not be negative")
	}
	if dot := strings.IndexByte(s, '.'); dot >= 0 && len(s)-dot-1 > 2 {
		return 0, fmt.Errorf("%q has more than 2 decimal places", s)
	}
	return int64(math.Round(f * 100)), nil
}

func parseOptionalDate(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return 0, err
	}
	return t.Unix(), nil
}

func invoiceSetup(args []string) int {
	_ = args
	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()

	// Step 1: Ensure Dentix company exists.
	if err := client.Call(rpcapi.ServiceName+".CompanyAdd",
		rpcapi.CompanyAddArgs{Name: "Dentix"}, &rpcapi.CompanyAddReply{}); err != nil {
		// Ignore "UNIQUE constraint" errors — company already exists.
		if !strings.Contains(err.Error(), "UNIQUE") && !strings.Contains(err.Error(), "already exists") {
			fmt.Fprintln(os.Stderr, "CompanyAdd Dentix:", err)
			return 1
		}
	}

	// Step 2: Move project Dentix → company Dentix.
	if err := client.Call(rpcapi.ServiceName+".ProjectSetCompany",
		rpcapi.ProjectSetCompanyArgs{ProjectName: "Dentix", CompanyName: "Dentix"},
		&rpcapi.ProjectSetCompanyReply{}); err != nil {
		fmt.Fprintln(os.Stderr, "ProjectSetCompany Dentix → Dentix:", err)
		return 1
	}

	// Step 3: Configure billing for BClouder and Dentix.
	plans := []rpcapi.SetCompanyBillingArgs{
		{Name: "BClouder", BillingMode: "hourly", Currency: "CAD", RateCents: 5000, BilledFrom: "es"},
		{Name: "Dentix", BillingMode: "monthly_fixed", Currency: "EUR", RateCents: 300000, BilledFrom: "br"},
	}
	for _, p := range plans {
		if err := client.Call(rpcapi.ServiceName+".SetCompanyBilling",
			p, &rpcapi.SetCompanyBillingReply{}); err != nil {
			fmt.Fprintf(os.Stderr, "SetCompanyBilling %s: %v\n", p.Name, err)
			return 1
		}
		fmt.Printf("  set %s → mode=%s currency=%s rate=%d billed_from=%s\n",
			p.Name, p.BillingMode, p.Currency, p.RateCents, p.BilledFrom)
	}

	fmt.Println("\nSetup complete. Next steps:")
	fmt.Println("  1. Edit ~/.antitimely/config.yaml to add `senders:` block (see docs/superpowers/specs/2026-05-27-invoice-pdf-design.md)")
	fmt.Println("  2. Run `atl invoice show-senders` to validate.")
	fmt.Println("  3. Run `atl invoice generate <company>` to produce your first PDF.")
	return 0
}
