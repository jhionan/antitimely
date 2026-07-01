# Menu-Driven Invoice Generation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the user generate a real invoice PDF from the `atl` interactive menu by picking a company from a numbered list, previewing period/hours/amount, confirming, and having the PDF + its Finder folder open automatically.

**Architecture:** A reusable numbered-list company picker (over the existing `CompanyList` RPC) feeds an invoice-generate flow that does a dry-run for a preview, confirms, then runs the real generate (which opens the PDF) and reveals its folder in Finder. A thin RPC wrapper + open/reveal helpers are shared by both the CLI `invoice generate` and the new menu flow. The `InvoiceGenerateReply` gains period/hours/mode so the preview is meaningful.

**Tech Stack:** Go 1.26, `net/rpc` over a Unix socket, stdlib `flag`, macOS `open` / `open -R`. No new modules.

## Global Constraints

- No new Go module dependency; stdlib only.
- macOS only: viewer via `exec.Command("open", pdf)`, Finder reveal via `exec.Command("open", "-R", pdf)`.
- The CLI is hand-rolled stdlib `flag` (no framework) — keep it that way (memory `cli-no-cobra-2026-06-25`).
- `internal/store/*.go` is sqlc-generated — do not hand-edit. (No store changes in this plan.)
- Company names are case-sensitive; the picker eliminates typing them.
- Commit messages: NO `Co-Authored-By`, NO "Generated with" footer. End each commit body with a `Claude-Session:` trailer line to match this repo's history.
- Spec: `docs/superpowers/specs/2026-07-01-menu-invoice-generation-design.md`.

---

### Task 1: Add period / hours / mode to `InvoiceGenerateReply`

The preview needs the resolved period, tick count, and billing mode. The daemon already computes all three while generating; expose them on the reply.

**Files:**
- Modify: `internal/rpcapi/api.go` (add 4 fields to `InvoiceGenerateReply`, ~line 97)
- Modify: `internal/daemon/rpc_invoice.go` (populate them in the reply block, ~line 226)
- Test: `internal/daemon/rpc_invoice_test.go` (extend `TestRPC_InvoiceGenerate_DryRun`)

**Interfaces:**
- Produces: `rpcapi.InvoiceGenerateReply` gains `FromUnix int64`, `ToUnix int64`, `Ticks int64`, `BillingMode string`.
- Consumes: existing handler locals `from, to time.Time` (`rpc_invoice.go:75`), `ticks int64` (`:86`), `co.BillingMode string`.

- [ ] **Step 1: Write the failing test**

In `internal/daemon/rpc_invoice_test.go`, inside `TestRPC_InvoiceGenerate_DryRun`, immediately after the `if reply.InvoiceID != 0 { ... }` block (~line 188), add:

```go
	if reply.BillingMode != "monthly_fixed" {
		t.Errorf("BillingMode = %q, want monthly_fixed", reply.BillingMode)
	}
	if reply.ToUnix <= reply.FromUnix {
		t.Errorf("period not set: from=%d to=%d", reply.FromUnix, reply.ToUnix)
	}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestRPC_InvoiceGenerate_DryRun -v`
Expected: FAIL — `reply.BillingMode` is `""` (field doesn't exist yet → compile error `unknown field BillingMode`).

- [ ] **Step 3: Add the reply fields**

In `internal/rpcapi/api.go`, change `InvoiceGenerateReply` to:

```go
type InvoiceGenerateReply struct {
	InvoiceID     int64 // 0 when DryRun=true
	Number        string
	PDFPath       string
	TotalCents    int64
	Currency      string
	SenderKey     string
	IssueDateUnix int64
	DueDateUnix   int64
	FromUnix      int64  // resolved billing-period start
	ToUnix        int64  // resolved billing-period end (exclusive)
	Ticks         int64  // billed tick count (0 for monthly_fixed)
	BillingMode   string // "hourly" | "monthly_fixed"
}
```

- [ ] **Step 4: Populate them in the handler**

In `internal/daemon/rpc_invoice.go`, in the reply block (just before `return nil` at ~line 234), add:

```go
	reply.FromUnix = from.Unix()
	reply.ToUnix = to.Unix()
	reply.Ticks = ticks
	reply.BillingMode = co.BillingMode
```

- [ ] **Step 5: Run tests to verify they pass + build**

Run: `go test ./internal/daemon/ -run TestRPC_InvoiceGenerate -v && go build ./...`
Expected: PASS (all InvoiceGenerate tests), clean build.

- [ ] **Step 6: Commit**

```bash
git add internal/rpcapi/api.go internal/daemon/rpc_invoice.go internal/daemon/rpc_invoice_test.go
git commit -m "feat(rpc): expose period/ticks/mode on InvoiceGenerateReply

Claude-Session: https://claude.ai/code/session_01E3veMELs6cpygGHnL27P1X"
```

---

### Task 2: Company picker (`pickCompany` + pure `parseCompanyChoice`)

A reusable numbered-list picker so the user never types a company name. The selection logic is a pure function; only the fetch + print + read is I/O.

**Files:**
- Create: `internal/cli/pick.go`
- Test: `internal/cli/pick_test.go`

**Interfaces:**
- Produces:
  - `parseCompanyChoice(items []rpcapi.Company, input string) (name string, ok bool)` — pure: maps a 1-based typed number to the company name; `ok=false` for blank, `b`, non-numeric, or out-of-range.
  - `pickCompany(stdin *bufio.Scanner) (name string, ok bool)` — fetches `CompanyList`, prints a numbered list, reads a line via `promptLine`, returns `parseCompanyChoice`.
- Consumes: `rpcapi.Company{ID int64; Name string}`, `dialOrExit()` (`watch.go`), `promptLine(stdin, label)` (`menu.go:100`), `CompanyList` RPC.

- [ ] **Step 1: Write the failing test**

Create `internal/cli/pick_test.go`:

```go
package cli

import (
	"testing"

	"github.com/rian/antitimely/internal/rpcapi"
)

func TestParseCompanyChoice(t *testing.T) {
	items := []rpcapi.Company{{ID: 7, Name: "BClouder"}, {ID: 3, Name: "Foca.app"}}
	cases := []struct {
		in       string
		wantName string
		wantOK   bool
	}{
		{"1", "BClouder", true},
		{"2", "Foca.app", true},
		{" 2 ", "Foca.app", true}, // trimmed
		{"3", "", false},          // out of range
		{"0", "", false},          // 1-based, 0 invalid
		{"", "", false},           // blank
		{"b", "", false},          // back
		{"x", "", false},          // non-numeric
	}
	for _, c := range cases {
		name, ok := parseCompanyChoice(items, c.in)
		if name != c.wantName || ok != c.wantOK {
			t.Errorf("parseCompanyChoice(%q) = (%q,%v), want (%q,%v)", c.in, name, ok, c.wantName, c.wantOK)
		}
	}
}

func TestParseCompanyChoiceEmptyList(t *testing.T) {
	if _, ok := parseCompanyChoice(nil, "1"); ok {
		t.Error("expected ok=false for empty company list")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestParseCompanyChoice -v`
Expected: FAIL — `undefined: parseCompanyChoice`.

- [ ] **Step 3: Implement `pick.go`**

Create `internal/cli/pick.go`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass + build**

Run: `go test ./internal/cli/ -run TestParseCompanyChoice -v && go build ./...`
Expected: PASS (2 tests), clean build.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/pick.go internal/cli/pick_test.go
git commit -m "feat(cli): reusable numbered company picker for the menu

Claude-Session: https://claude.ai/code/session_01E3veMELs6cpygGHnL27P1X"
```

---

### Task 3: Invoice preview formatter (`formatInvoicePreview`)

A pure function that turns a dry-run reply into a one-line confirmation preview, handling hourly and monthly_fixed.

**Files:**
- Create: `internal/cli/invoice_preview.go`
- Test: `internal/cli/invoice_preview_test.go`

**Interfaces:**
- Produces: `formatInvoicePreview(r rpcapi.InvoiceGenerateReply) string`.
- Consumes: `rpcapi.InvoiceGenerateReply` fields from Task 1 (`Number, FromUnix, ToUnix, Ticks, BillingMode, TotalCents, Currency`).

- [ ] **Step 1: Write the failing test**

Create `internal/cli/invoice_preview_test.go`:

```go
package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
)

func TestFormatInvoicePreviewHourly(t *testing.T) {
	from := time.Date(2026, 6, 16, 15, 52, 0, 0, time.Local)
	to := time.Date(2026, 7, 1, 18, 11, 0, 0, time.Local)
	got := formatInvoicePreview(rpcapi.InvoiceGenerateReply{
		Number: "ES-0004", BillingMode: "hourly",
		FromUnix: from.Unix(), ToUnix: to.Unix(),
		Ticks: 54706, TotalCents: 379900, Currency: "CAD",
	})
	for _, want := range []string{"ES-0004", "2026-06-16", "2026-07-01", "75.98h", "CAD 3799.00"} {
		if !strings.Contains(got, want) {
			t.Errorf("preview missing %q: %s", want, got)
		}
	}
}

func TestFormatInvoicePreviewMonthlyFixed(t *testing.T) {
	got := formatInvoicePreview(rpcapi.InvoiceGenerateReply{
		Number: "INV-014", BillingMode: "monthly_fixed",
		FromUnix: time.Date(2026, 6, 1, 0, 0, 0, 0, time.Local).Unix(),
		ToUnix:   time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local).Unix(),
		Ticks:    0, TotalCents: 300000, Currency: "EUR",
	})
	if !strings.Contains(got, "EUR 3000.00") || !strings.Contains(got, "INV-014") {
		t.Errorf("fixed preview wrong: %s", got)
	}
	if strings.Contains(got, "h ") || strings.Contains(got, "h·") || strings.Contains(got, "0.00h") {
		t.Errorf("monthly_fixed preview must not show hours: %s", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestFormatInvoicePreview -v`
Expected: FAIL — `undefined: formatInvoicePreview`.

- [ ] **Step 3: Implement `invoice_preview.go`**

Create `internal/cli/invoice_preview.go`:

```go
package cli

import (
	"fmt"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
)

// formatInvoicePreview renders a one-line confirmation summary of a dry-run
// invoice. Hourly invoices include the billed hours; monthly_fixed ones omit
// them (there is no per-hour component).
func formatInvoicePreview(r rpcapi.InvoiceGenerateReply) string {
	period := fmt.Sprintf("%s→%s",
		time.Unix(r.FromUnix, 0).Local().Format("2006-01-02"),
		time.Unix(r.ToUnix, 0).Local().Format("2006-01-02"))
	amount := fmt.Sprintf("%s %.2f", r.Currency, float64(r.TotalCents)/100)
	if r.BillingMode == "hourly" {
		hours := float64(r.Ticks) * 5 / 3600
		return fmt.Sprintf("%s · %s · %.2fh · %s", r.Number, period, hours, amount)
	}
	return fmt.Sprintf("%s · %s · %s", r.Number, period, amount)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cli/ -run TestFormatInvoicePreview -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/cli/invoice_preview.go internal/cli/invoice_preview_test.go
git commit -m "feat(cli): invoice dry-run preview formatter (hourly + monthly_fixed)

Claude-Session: https://claude.ai/code/session_01E3veMELs6cpygGHnL27P1X"
```

---

### Task 4: Shared RPC wrapper + open/reveal + retry; refactor CLI `invoice generate`

Extract a thin RPC wrapper and open/reveal helpers so both the CLI and the menu flow share them, and add Finder-reveal to the CLI path too (satisfies "open the folder"). Adds a small retry for the daemon-stall timeout.

**Files:**
- Create: `internal/cli/invoice_run.go`
- Modify: `internal/cli/invoice.go` (`invoiceGenerate`, ~lines 191-218)

**Interfaces:**
- Produces:
  - `invoiceGenerateRPC(args rpcapi.InvoiceGenerateArgs) (rpcapi.InvoiceGenerateReply, error)` — dials, calls `InvoiceGenerate`, returns the reply; retries up to 3× on `context deadline exceeded`.
  - `openAndReveal(pdfPath string)` — `open <pdf>` then `open -R <pdf>`; warns (does not fail) if either errors.
- Consumes: `dialOrExit()`, `rpcapi.InvoiceGenerateArgs/Reply`.

- [ ] **Step 1: Write the failing test**

Create `internal/cli/invoice_run_test.go`:

```go
package cli

import (
	"errors"
	"testing"
)

func TestIsDaemonStall(t *testing.T) {
	if !isDaemonStall(errors.New("context deadline exceeded")) {
		t.Error("should classify context-deadline as a stall")
	}
	if isDaemonStall(errors.New("company not found")) {
		t.Error("should not classify a real error as a stall")
	}
	if isDaemonStall(nil) {
		t.Error("nil is not a stall")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestIsDaemonStall -v`
Expected: FAIL — `undefined: isDaemonStall`.

- [ ] **Step 3: Implement `invoice_run.go`**

Create `internal/cli/invoice_run.go`:

```go
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
```

- [ ] **Step 4: Refactor `invoiceGenerate` to use them**

In `internal/cli/invoice.go`, replace the block from `client, code := dialOrExit()` (line 191) through the final `return 0` (line 218) with:

```go
	reply, err := invoiceGenerateRPC(rpcapi.InvoiceGenerateArgs{
		CompanyName:   company,
		FromUnix:      fromUnix,
		ToUnix:        toUnix,
		IssueDateUnix: issueUnix,
		Note:          *note,
		DryRun:        *dryRun,
		AllowEmpty:    *allowEmpty,
		DiscountCents: discountCents,
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
	openAndReveal(reply.PDFPath)
	return 0
```

Then remove now-unused imports from `invoice.go` if the compiler flags them (`net/rpc` may become unused; `os/exec` is now only in `invoice_run.go` — delete `"os/exec"` from `invoice.go`'s import block if `go build` reports it unused).

- [ ] **Step 5: Run tests + build + vet**

Run: `go test ./internal/cli/ -run TestIsDaemonStall -v && go build ./... && go vet ./internal/cli/`
Expected: PASS, clean build, no vet complaints (fix any unused-import error surfaced by the refactor).

- [ ] **Step 6: Commit**

```bash
git add internal/cli/invoice_run.go internal/cli/invoice_run_test.go internal/cli/invoice.go
git commit -m "refactor(cli): shared invoice-generate RPC wrapper + open/reveal + stall retry

Claude-Session: https://claude.ai/code/session_01E3veMELs6cpygGHnL27P1X"
```

---

### Task 5: Invoice-generate flow + rework the Invoices menu

Wire it together: the new `[2] Generate invoice (PDF)` runs pick → dry-run preview → confirm → real generate → open + reveal; the old anchor-only "send" moves to `[4] Record anchor only (advanced)`.

**Files:**
- Modify: `internal/cli/menu.go` (`invoiceMenu`, lines 257-299; add `invoiceGenerateFlow`)

**Interfaces:**
- Consumes: `pickCompany` (Task 2), `formatInvoicePreview` (Task 3), `invoiceGenerateRPC` + `openAndReveal` (Task 4), `promptLine` (`menu.go:100`), `invoiceSend`/`invoiceDelete`/`invoiceList` (existing).
- Produces: `invoiceGenerateFlow(stdin *bufio.Scanner)`.

- [ ] **Step 1: Replace `invoiceMenu` and add the flow**

In `internal/cli/menu.go`, replace the whole `invoiceMenu` function (lines 257-299) with:

```go
func invoiceMenu(stdin *bufio.Scanner) {
	for {
		fmt.Println()
		fmt.Println("Invoices:")
		fmt.Println("  [1] List all invoices")
		fmt.Println("  [2] Generate invoice (PDF)")
		fmt.Println("  [3] Delete invoice")
		fmt.Println("  [4] Record anchor only (advanced)")
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
			idStr, ok := promptLine(stdin, "Invoice ID to delete: ")
			if !ok || idStr == "" {
				continue
			}
			invoiceDelete([]string{idStr})
		case "4":
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
```

- [ ] **Step 2: Add the `rpcapi` import to `menu.go`**

`menu.go` now references `rpcapi.InvoiceGenerateArgs`. Add `"github.com/rian/antitimely/internal/rpcapi"` to `menu.go`'s import block (it currently imports only `bufio`, `fmt`, `os`, `strconv`, `strings`).

- [ ] **Step 3: Build + vet + full test suite**

Run: `go build ./... && go vet ./internal/cli/ && go test ./... -count=1`
Expected: clean build, no vet issues, all tests green.

- [ ] **Step 4: Manual verification**

Build and drive the menu (daemon must be running):

```bash
go build -o antitimely . && ./antitimely
```
1. `N` → Invoices → `2` → the company list appears numbered; pick a number → a preview line `NUMBER · period · [hours] · CUR amount` prints → `n` → "cancelled", nothing generated. ✅
2. Repeat → `y` → the real PDF generates, Preview opens it, Finder reveals it, and the invoice number advanced (check `[1] List all invoices`). ✅
3. Invoices → `4` (advanced) → pick a company from the list → records an anchor only (no PDF), as before. ✅
4. `atl invoice generate BClouder --dry-run` (CLI) → still opens the PDF **and** now reveals the folder. ✅

- [ ] **Step 5: Commit**

```bash
git add internal/cli/menu.go
git commit -m "feat(cli): menu invoice generation — pick company, preview, confirm, open+reveal

Claude-Session: https://claude.ai/code/session_01E3veMELs6cpygGHnL27P1X"
```

---

## Self-Review

**Spec coverage:**
- Company picker (numbered, no typing) → Task 2 (`pickCompany`/`parseCompanyChoice`), used in Task 5 menu. ✅
- Menu generates a real PDF → Task 5 (`[2]` → `invoiceGenerateFlow`). ✅
- Preview + confirm before burning the number → Task 5 (dry-run + `formatInvoicePreview` + `[y/N]`); dry-run never advances the cursor (existing handler behavior). ✅
- Open PDF + reveal folder → Task 4 (`openAndReveal`), called by both CLI and menu. ✅
- Demote anchor-only "send" → Task 5 (`[4] Record anchor only (advanced)`). ✅
- Preview shows period/hours/amount, both billing modes → Task 1 (reply fields) + Task 3 (`formatInvoicePreview` hourly vs monthly_fixed). ✅
- Retry on `context deadline exceeded` → Task 4 (`invoiceGenerateRPC` + `isDaemonStall`). ✅
- Timesheet out of scope → no task; unchanged. ✅

**Placeholder scan:** none — every step has concrete code and commands.

**Type consistency:** `parseCompanyChoice(items []rpcapi.Company, input string) (string, bool)`, `pickCompany(*bufio.Scanner) (string, bool)`, `formatInvoicePreview(rpcapi.InvoiceGenerateReply) string`, `invoiceGenerateRPC(rpcapi.InvoiceGenerateArgs) (rpcapi.InvoiceGenerateReply, error)`, `openAndReveal(string)`, `isDaemonStall(error) bool`, `invoiceGenerateFlow(*bufio.Scanner)` — names/signatures match across tasks. New reply fields (`FromUnix/ToUnix/Ticks/BillingMode`) defined in Task 1 and consumed in Task 3. `menu.go` gains the `rpcapi` import (Task 5 Step 2).
