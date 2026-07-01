package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
)

// isDaemonStall reports whether err is the transient "context deadline
// exceeded" the daemon returns when momentarily blocked (e.g. hung osascript
// under accessibility_denied). These are worth retrying; real errors are not.
func isDaemonStall(err error) bool {
	return err != nil && strings.Contains(err.Error(), "context deadline exceeded")
}

// invoiceGenerateRPC calls InvoiceGenerate, retrying up to 3 times on a
// transient daemon stall (a fresh dial each attempt).
func invoiceGenerateRPC(args rpcapi.InvoiceGenerateArgs) (rpcapi.InvoiceGenerateReply, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		client, code := dialOrExit()
		if client == nil {
			return rpcapi.InvoiceGenerateReply{}, fmt.Errorf("daemon unreachable (exit %d)", code)
		}
		var reply rpcapi.InvoiceGenerateReply
		err := client.Call(rpcapi.ServiceName+".InvoiceGenerate", args, &reply)
		client.Close()
		if err == nil {
			return reply, nil
		}
		lastErr = err
		if !isDaemonStall(err) {
			return rpcapi.InvoiceGenerateReply{}, err
		}
		time.Sleep(2 * time.Second)
	}
	return rpcapi.InvoiceGenerateReply{}, fmt.Errorf("%w (daemon busy — check Accessibility, then retry)", lastErr)
}

// openAndReveal opens the PDF in the default viewer and reveals it in Finder.
// Failures are warnings, not errors (the file is already written).
func openAndReveal(pdfPath string) {
	if err := exec.Command("open", pdfPath).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "(warning: could not open viewer:", err, ")")
	}
	if err := exec.Command("open", "-R", pdfPath).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "(warning: could not reveal in Finder:", err, ")")
	}
}
