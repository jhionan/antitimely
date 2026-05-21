package cli

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
)

func cmdInvoice(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: antitimely invoice <send|list|delete> ...")
		return 64
	}
	switch args[0] {
	case "send":
		return invoiceSend(args[1:])
	case "list":
		return invoiceList(args[1:])
	case "delete":
		return invoiceDelete(args[1:])
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
