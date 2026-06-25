# Live `status` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn `atl status` into a live, self-refreshing terminal view (redraw every 5s) that exits on **Esc** (returning to the menu when launched from it) or Ctrl-C, while keeping a single-shot mode for pipes and `--once`.

**Architecture:** Split the monolithic `cmdStatus` into a pure renderer (`renderStatus`), a thin RPC fetch (`fetchStatus`), and a live loop (`runStatusLive`). The live loop puts the terminal into cbreak mode via `golang.org/x/sys/unix` (already an indirect dep) and uses a single timed `Read` as both the 5s refresh clock and the key listener — no goroutine, so nothing leaks into the menu's input afterward.

**Tech Stack:** Go 1.26, `net/rpc` over unix socket, `golang.org/x/sys/unix` (termios), ANSI escape codes. No new module.

## Global Constraints

- No new Go module dependency. Use `golang.org/x/sys/unix` (already in `go.mod` as indirect; this promotes it to direct — expected). Spec ref: memory `cli-no-cobra-2026-06-25`.
- macOS only (consistent with the tool). Termios ioctls use `unix.TIOCGETA`/`unix.TIOCSETA` (the BSD/darwin names).
- Cbreak mode clears `ICANON|ECHO` but **keeps `ISIG`** so Ctrl-C still raises SIGINT.
- Refresh interval: **5 seconds** (`VTIME = 50` deciseconds).
- No daemon, RPC, schema, or `rpcapi.StatusReply` changes.
- Commit messages: no Claude/Anthropic attribution, no Co-Authored-By, no "Generated with" footer.

---

### Task 1: Extract pure renderer + one-shot path + `--once`

Refactor `cmdStatus` so the formatting becomes a pure, testable function and the one-shot/piped behavior is preserved. No live mode yet.

**Files:**
- Modify: `internal/cli/status.go` (rewrite `cmdStatus`; add `fetchStatus`, `renderStatus`, `renderWarning`)
- Modify: `internal/cli/menu.go` (add `IsStdoutTerminal` next to `IsStdinTerminal:13`)
- Test: `internal/cli/status_test.go` (new)

**Interfaces:**
- Produces:
  - `fetchStatus(client *rpc.Client) (rpcapi.StatusReply, error)`
  - `renderStatus(w io.Writer, reply rpcapi.StatusReply)` — renders header + today + companies + unassigned. **No accessibility warning, no footer, no time.Now()** (kept deterministic).
  - `renderWarning(w io.Writer, reply rpcapi.StatusReply)` — writes the 4-line accessibility warning iff `reply.PermissionState == "accessibility_denied"`, else nothing.
  - `IsStdoutTerminal() bool`
- Consumes: `dialOrExit() (*rpc.Client, int)` (existing, `internal/cli/watch.go:122`), `fmtDuration` (existing, `status.go:139`).

- [ ] **Step 1: Write the failing test**

Create `internal/cli/status_test.go`:

```go
package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rian/antitimely/internal/rpcapi"
)

func sampleReply() rpcapi.StatusReply {
	return rpcapi.StatusReply{
		UserIdleSeconds:     65,
		TickIntervalSeconds: 5,
		PermissionState:     "accessibility_denied",
		DaemonUptimeSeconds: 3600,
		TodayTotalSeconds:   7200,
		Companies: []rpcapi.CompanyTotals{
			{
				Name:            "BClouder",
				LastInvoiceUnix: 0,
				BillableSeconds: 3600,
				Projects: []rpcapi.ProjectTotals{
					{Name: "Daas", BillableSeconds: 3600, TodaySeconds: 3600},
					{Name: "Rumo", BillableSeconds: 0, TodaySeconds: 0, Paused: true},
					{Name: "VCNA", BillableSeconds: 120, TodaySeconds: 0, Armed: true, SuppressedSeconds: 300},
				},
			},
			{
				Name:            "(no company)",
				BillableSeconds: 600,
				Projects:        []rpcapi.ProjectTotals{{Name: "Solo", BillableSeconds: 600, TodaySeconds: 60}},
			},
		},
		UnassignedBillableSeconds: 900,
		UnassignedTodaySeconds:    90,
		UnassignedSignaturesCount: 3,
	}
}

func TestRenderStatusCoversBranches(t *testing.T) {
	var buf bytes.Buffer
	renderStatus(&buf, sampleReply())
	out := buf.String()
	for _, want := range []string{
		"Idle: 1m5s", "Tick: 5s", "Uptime: 1h0m0s",
		"Today: 2h0m0s total tracked",
		"BClouder", "Daas", "(paused)", "(armed: needs focus — 5m0s NOT counted!)",
		"(no company)", "Solo",
		"(unassigned)", "3 signature(s), run `antitimely review`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderStatus output missing %q\n---\n%s", want, out)
		}
	}
	// renderStatus must NOT emit the warning (that is renderWarning's job).
	if strings.Contains(out, "Window-title capture disabled") {
		t.Error("renderStatus should not contain the accessibility warning")
	}
}

func TestRenderStatusEmpty(t *testing.T) {
	var buf bytes.Buffer
	renderStatus(&buf, rpcapi.StatusReply{TickIntervalSeconds: 5})
	if !strings.Contains(buf.String(), "(no time tracked yet)") {
		t.Errorf("expected empty-state line, got:\n%s", buf.String())
	}
}

func TestRenderWarningOnlyWhenDenied(t *testing.T) {
	var denied, ok bytes.Buffer
	renderWarning(&denied, rpcapi.StatusReply{PermissionState: "accessibility_denied"})
	renderWarning(&ok, rpcapi.StatusReply{PermissionState: "ok"})
	if !strings.Contains(denied.String(), "Window-title capture disabled") {
		t.Error("expected warning when accessibility_denied")
	}
	if ok.String() != "" {
		t.Errorf("expected no warning when ok, got: %q", ok.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run 'TestRenderStatus|TestRenderWarning' -v`
Expected: FAIL — `undefined: renderStatus` / `undefined: renderWarning`.

- [ ] **Step 3: Rewrite `status.go`**

Replace the entire body of `internal/cli/status.go` with:

```go
package cli

import (
	"flag"
	"fmt"
	"io"
	"net/rpc"
	"os"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
)

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	once := fs.Bool("once", false, "print a single snapshot and exit (no live view)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 64
	}

	client, code := dialOrExit()
	if client == nil {
		return code
	}
	defer client.Close()

	if !*once && IsStdoutTerminal() {
		return runStatusLive(client)
	}

	reply, err := fetchStatus(client)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	renderStatus(os.Stdout, reply)
	renderWarning(os.Stderr, reply)
	return 0
}

// fetchStatus performs the single Status RPC.
func fetchStatus(client *rpc.Client) (rpcapi.StatusReply, error) {
	var reply rpcapi.StatusReply
	err := client.Call(rpcapi.ServiceName+".Status", rpcapi.StatusArgs{}, &reply)
	return reply, err
}

// renderStatus writes one status frame (header, today total, grouped billables,
// unassigned bucket) to w. Pure: no globals, no time.Now, no accessibility
// warning (see renderWarning), no exit.
func renderStatus(w io.Writer, reply rpcapi.StatusReply) {
	fmt.Fprintf(w, "Idle: %s   |   Tick: %ds   |   Permission: %s   |   Uptime: %s\n",
		fmtDuration(int64(reply.UserIdleSeconds)),
		reply.TickIntervalSeconds,
		reply.PermissionState,
		fmtDuration(reply.DaemonUptimeSeconds),
	)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Today: %s total tracked\n", fmtDuration(reply.TodayTotalSeconds))
	fmt.Fprintln(w)

	if len(reply.Companies) == 0 && reply.UnassignedBillableSeconds == 0 {
		fmt.Fprintln(w, "(no time tracked yet)")
		return
	}

	fmt.Fprintln(w, "Billable (since last invoice per company):")
	fmt.Fprintln(w)

	for _, co := range reply.Companies {
		if co.Name == "(no company)" {
			continue
		}
		since := "never"
		if co.LastInvoiceUnix != 0 {
			since = time.Unix(co.LastInvoiceUnix, 0).Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "  %-38s %s   (since: %s)\n", co.Name, fmtDuration(co.BillableSeconds), since)
		renderProjects(w, co.Projects)
		fmt.Fprintln(w)
	}

	for _, co := range reply.Companies {
		if co.Name != "(no company)" {
			continue
		}
		fmt.Fprintf(w, "  %-38s %s\n", "(no company)", fmtDuration(co.BillableSeconds))
		renderProjects(w, co.Projects)
		fmt.Fprintln(w)
	}

	if reply.UnassignedBillableSeconds > 0 || reply.UnassignedTodaySeconds > 0 {
		sigNote := ""
		if reply.UnassignedSignaturesCount > 0 {
			sigNote = fmt.Sprintf(", %d signature(s), run `antitimely review`", reply.UnassignedSignaturesCount)
		}
		fmt.Fprintf(w, "  %-38s %s   (today: %s%s)\n",
			"(unassigned)",
			fmtDuration(reply.UnassignedBillableSeconds),
			fmtDuration(reply.UnassignedTodaySeconds),
			sigNote,
		)
	}
}

func renderProjects(w io.Writer, projects []rpcapi.ProjectTotals) {
	for _, pr := range projects {
		pausedNote := ""
		if pr.Paused {
			pausedNote = "  (paused)"
		}
		armedNote := ""
		if pr.Armed {
			armedNote = "  (armed: needs focus)"
			if pr.SuppressedSeconds > 0 {
				armedNote = fmt.Sprintf("  (armed: needs focus — %s NOT counted!)", fmtDuration(pr.SuppressedSeconds))
			}
		}
		fmt.Fprintf(w, "    %-36s %s   (today: %s)%s%s\n",
			pr.Name,
			fmtDuration(pr.BillableSeconds),
			fmtDuration(pr.TodaySeconds),
			pausedNote,
			armedNote,
		)
	}
}

// renderWarning writes the accessibility-permission warning to w when
// window-title capture is disabled; otherwise writes nothing.
func renderWarning(w io.Writer, reply rpcapi.StatusReply) {
	if reply.PermissionState != "accessibility_denied" {
		return
	}
	fmt.Fprintln(w,
		"  Warning: Window-title capture disabled. Grant antitimely BOTH:\n"+
			"    - Privacy & Security -> Accessibility (required for Electron/JVM apps: VS Code, Antigravity, JetBrains, ...)\n"+
			"    - Privacy & Security -> Automation -> antitimely -> System Events\n"+
			"  Then restart the daemon (make rebuild). A rebuild can reset these grants.")
}

// fmtDuration formats a duration in seconds as "1h2m3s", or "0s" for zero.
func fmtDuration(seconds int64) string {
	if seconds <= 0 {
		return "0s"
	}
	return time.Duration(seconds * int64(time.Second)).String()
}
```

- [ ] **Step 4: Add `IsStdoutTerminal` to `menu.go`**

After `IsStdinTerminal` (ends `menu.go:19`), add:

```go
// IsStdoutTerminal reports whether stdout appears to be an interactive terminal.
// Returns false if stdout is a pipe, file, or other non-tty.
func IsStdoutTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
```

- [ ] **Step 5: Run tests to verify they pass + build**

Run: `go test ./internal/cli/ -run 'TestRenderStatus|TestRenderWarning' -v && go build ./...`
Expected: PASS (3 tests) and a clean build. (Note: `runStatusLive` does not exist yet, so the build will FAIL with `undefined: runStatusLive`. That is expected — proceed to Step 6 to temporarily stub it, OR implement Task 2+3 before building. To keep this task independently green, add the stub below.)

Add a temporary stub at the end of `status.go` so the package builds:

```go
// runStatusLive is implemented in Task 3. Temporary stub to keep the package
// compiling after Task 1; replaced in Task 3.
func runStatusLive(client *rpc.Client) int {
	reply, err := fetchStatus(client)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	renderStatus(os.Stdout, reply)
	renderWarning(os.Stderr, reply)
	return 0
}
```

Re-run: `go test ./internal/cli/ -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/status.go internal/cli/status_test.go internal/cli/menu.go
git commit -m "refactor(cli): split status into fetch/render, add --once and stdout-tty check"
```

---

### Task 2: Terminal helpers (cbreak / restore / ANSI)

Add the macOS termios + ANSI helpers in their own file. These are I/O glue; the one testable behavior is that entering cbreak on a non-tty fd returns an error (so the live loop can fall back to one-shot).

**Files:**
- Create: `internal/cli/terminal.go`
- Test: `internal/cli/terminal_test.go`

**Interfaces:**
- Produces:
  - `type termState struct { fd int; orig *unix.Termios }`
  - `enterCbreak() (*termState, error)` — cbreak on stdin; `VMIN=0`, `VTIME=50`, keeps `ISIG`.
  - `enterCbreakFd(fd int) (*termState, error)` — same, explicit fd (for tests).
  - `(*termState) restore()` — drains pending input then restores the saved termios.
  - `(*termState) readEvent() statusEvent` — one timed read; returns `evtEsc`, `evtOther`, or `evtTimeout`.
  - ANSI writers: `altScreenEnter(w)`, `altScreenLeave(w)`, `clearScreen(w)`, `hideCursor(w)`, `showCursor(w)`.
  - `type statusEvent int` with `evtTimeout`, `evtEsc`, `evtOther`.
- Consumes: `golang.org/x/sys/unix`.

- [ ] **Step 1: Write the failing test**

Create `internal/cli/terminal_test.go`:

```go
package cli

import (
	"os"
	"testing"
)

func TestEnterCbreakFdRejectsNonTTY(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	if st, err := enterCbreakFd(int(r.Fd())); err == nil {
		st.restore()
		t.Fatal("expected error entering cbreak on a non-tty pipe, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestEnterCbreakFd -v`
Expected: FAIL — `undefined: enterCbreakFd`.

- [ ] **Step 3: Implement `terminal.go`**

```go
package cli

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const (
	ansiAltEnter   = "\x1b[?1047h"
	ansiAltLeave   = "\x1b[?1047l"
	ansiClearHome  = "\x1b[H\x1b[J"
	ansiHideCursor = "\x1b[?25l"
	ansiShowCursor = "\x1b[?25h"
)

func altScreenEnter(w io.Writer) { fmt.Fprint(w, ansiAltEnter) }
func altScreenLeave(w io.Writer) { fmt.Fprint(w, ansiAltLeave) }
func clearScreen(w io.Writer)    { fmt.Fprint(w, ansiClearHome) }
func hideCursor(w io.Writer)     { fmt.Fprint(w, ansiHideCursor) }
func showCursor(w io.Writer)     { fmt.Fprint(w, ansiShowCursor) }

type statusEvent int

const (
	evtTimeout statusEvent = iota // VTIME elapsed, no key
	evtEsc                        // bare Escape pressed
	evtOther                      // any other key / escape sequence
)

type termState struct {
	fd   int
	orig *unix.Termios
}

// enterCbreak switches stdin to cbreak mode (non-canonical, no echo, ISIG kept,
// VMIN=0/VTIME=50 → reads block up to 5s).
func enterCbreak() (*termState, error) {
	return enterCbreakFd(int(os.Stdin.Fd()))
}

func enterCbreakFd(fd int) (*termState, error) {
	orig, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return nil, err
	}
	raw := *orig
	raw.Lflag &^= unix.ICANON | unix.ECHO // keep ISIG so Ctrl-C still signals
	raw.Cc[unix.VMIN] = 0
	raw.Cc[unix.VTIME] = 50 // 5.0s
	if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, &raw); err != nil {
		return nil, err
	}
	return &termState{fd: fd, orig: orig}, nil
}

// setVTime adjusts the inter-read timeout (deciseconds) in-place. Used to do a
// short follow-up read when disambiguating a bare Esc from an escape sequence.
func (s *termState) setVTime(d uint8) {
	t, err := unix.IoctlGetTermios(s.fd, unix.TIOCGETA)
	if err != nil {
		return
	}
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = d
	_ = unix.IoctlSetTermios(s.fd, unix.TIOCSETA, t)
}

// readEvent does one timed read. With VMIN=0/VTIME=50 it returns after a
// keypress or after 5s. A lone 0x1b is disambiguated from an escape sequence
// (arrow keys etc.) with a 0.1s follow-up read.
func (s *termState) readEvent() statusEvent {
	var b [16]byte
	n, _ := unix.Read(s.fd, b[:])
	if n == 0 {
		return evtTimeout
	}
	if b[0] != 0x1b {
		return evtOther
	}
	if n > 1 {
		return evtOther // Esc followed by more bytes in one read = sequence
	}
	// Lone Esc byte: a real sequence's tail is already in the OS buffer, so a
	// 0.1s read returns it immediately; a bare Esc times out empty.
	s.setVTime(1)
	n2, _ := unix.Read(s.fd, b[:])
	s.setVTime(50)
	if n2 == 0 {
		return evtEsc
	}
	return evtOther
}

// restore drains any pending input (e.g. an escape-sequence tail) then puts the
// terminal back into its original mode. Safe to call once on teardown.
func (s *termState) restore() {
	drain := *s.orig
	drain.Lflag &^= unix.ICANON | unix.ECHO
	drain.Cc[unix.VMIN] = 0
	drain.Cc[unix.VTIME] = 0 // fully non-blocking
	if err := unix.IoctlSetTermios(s.fd, unix.TIOCSETA, &drain); err == nil {
		var b [64]byte
		for {
			n, err := unix.Read(s.fd, b[:])
			if n <= 0 || err != nil {
				break
			}
		}
	}
	_ = unix.IoctlSetTermios(s.fd, unix.TIOCSETA, s.orig)
}
```

- [ ] **Step 4: Run test + tidy modules + build**

Run: `go mod tidy && go test ./internal/cli/ -run TestEnterCbreakFd -v && go build ./...`
Expected: PASS. `go mod tidy` moves `golang.org/x/sys` from `// indirect` to a direct require. No other new modules appear in `go.sum`.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/terminal.go internal/cli/terminal_test.go go.mod go.sum
git commit -m "feat(cli): macOS cbreak terminal + ANSI helpers for live view"
```

---

### Task 3: Live redraw loop (`runStatusLive`)

Replace the Task 1 stub with the real live loop: alt-screen, redraw every 5s, Esc/Ctrl-C exit with guaranteed terminal restore.

**Files:**
- Modify: `internal/cli/status.go` (replace the `runStatusLive` stub; add `renderFooter`; new imports `os/signal`, `time` already imported)

**Interfaces:**
- Consumes: `enterCbreak`, `(*termState).readEvent`, `(*termState).restore`, `altScreenEnter/Leave`, `clearScreen`, `hideCursor`, `showCursor`, `evtEsc`, `fetchStatus`, `renderStatus`, `renderWarning` (Tasks 1–2).
- Produces: `runStatusLive(client *rpc.Client) int`, `renderFooter(w io.Writer, now time.Time)`.

- [ ] **Step 1: Write the failing test**

`renderFooter` is the one deterministic piece; test it. Append to `internal/cli/status_test.go`:

```go
func TestRenderFooter(t *testing.T) {
	var buf bytes.Buffer
	renderFooter(&buf, time.Date(2026, 6, 25, 20, 36, 1, 0, time.Local))
	out := buf.String()
	for _, want := range []string{"live", "every 5s", "Esc to exit", "20:36:01"} {
		if !strings.Contains(out, want) {
			t.Errorf("footer missing %q, got: %q", want, out)
		}
	}
}
```

Add `"time"` to the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestRenderFooter -v`
Expected: FAIL — `undefined: renderFooter`.

- [ ] **Step 3: Replace the stub with the real loop**

In `internal/cli/status.go`: add `"os/signal"` to imports, remove the temporary `runStatusLive` stub, and add:

```go
// runStatusLive renders the status frame on the alternate screen, refreshing
// every 5s, until the user presses Esc (returns) or Ctrl-C (clean exit). The
// terminal is always restored, including on SIGINT.
func runStatusLive(client *rpc.Client) int {
	st, err := enterCbreak()
	if err != nil {
		// Not a real tty after all — fall back to a single snapshot.
		reply, ferr := fetchStatus(client)
		if ferr != nil {
			fmt.Fprintln(os.Stderr, ferr)
			return 1
		}
		renderStatus(os.Stdout, reply)
		renderWarning(os.Stderr, reply)
		return 0
	}

	out := os.Stdout
	var restoreOnce sync.Once
	cleanup := func() {
		restoreOnce.Do(func() {
			altScreenLeave(out)
			showCursor(out)
			st.restore()
		})
	}
	defer cleanup()

	// Guarantee restore on Ctrl-C (ISIG is kept, so Ctrl-C raises SIGINT).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		cleanup()
		os.Exit(130)
	}()

	altScreenEnter(out)
	hideCursor(out)

	for {
		reply, err := fetchStatus(client)
		if err != nil {
			cleanup()
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		clearScreen(out)
		renderStatus(out, reply)
		renderWarning(out, reply)
		renderFooter(out, time.Now())

		if st.readEvent() == evtEsc {
			return 0 // cleanup runs via defer
		}
	}
}

// renderFooter writes the live-mode footer line.
func renderFooter(w io.Writer, now time.Time) {
	fmt.Fprintf(w, "\nlive · every 5s · Esc to exit · %s\n", now.Format("15:04:05"))
}
```

Add `"sync"` to the import block of `status.go`.

- [ ] **Step 4: Run tests + build + vet**

Run: `go test ./internal/cli/ -v && go build ./... && go vet ./internal/cli/`
Expected: PASS (all status/terminal tests), clean build, no vet complaints.

- [ ] **Step 5: Manual verification**

Ensure the daemon is running (`pgrep -fl 'antitimely daemon'`), build the binary (`go build -o antitimely .`), then verify each:

1. `./antitimely status` → live view appears on a cleared screen, footer clock advances every 5s. Press **Esc** → returns to the shell prompt, terminal normal (cursor visible, echo works). ✅
2. `./antitimely status` again → press **Ctrl-C** → clean exit, terminal normal. ✅
3. `./antitimely status --once` → prints one frame and exits immediately (no alt-screen, no clock). ✅
4. `./antitimely status | cat` → prints one frame (piped = non-tty → one-shot); warning, if any, on stderr not in the piped text. ✅
5. `./antitimely` → menu → `1` (Enter) → live view → **Esc** → the menu reprints. ✅
6. In the live view, press an **arrow key** → it is ignored (does NOT exit); only Esc/Ctrl-C leave. ✅

- [ ] **Step 6: Commit**

```bash
git add internal/cli/status.go internal/cli/status_test.go
git commit -m "feat(cli): live self-refreshing status view with Esc/Ctrl-C exit"
```

---

## Self-Review

**Spec coverage:**
- Activation (live/`--once`/piped) → Task 1 (`cmdStatus` dispatch, `IsStdoutTerminal`) + Task 3 (loop). ✅
- 5s refresh → Task 2 (`VTIME=50`) + Task 3 (loop). ✅
- Esc exit / return-to-menu → Task 2 (`readEvent`→`evtEsc`) + Task 3 (`return 0`); menu redraw is automatic via `menu.go:51`. ✅
- Ctrl-C clean exit → Task 3 (signal handler + `cleanup`). ✅
- Terminal handling (alt-screen, cbreak keep-ISIG, flush) → Task 2. ✅
- Bare-Esc vs sequence disambiguation → Task 2 (`readEvent` follow-up read). ✅
- Code structure (`fetchStatus`/`renderStatus`/`runStatusLive`, `terminal.go`) → Tasks 1–3. ✅
- Content tweaks (warning into frame in live; footer) → Task 1 (`renderWarning`) + Task 3 (`renderFooter`, called inline). ✅
- No new module → Task 2 (`go mod tidy` keeps `x/sys` only). ✅
- Testing (`renderStatus` branches) → Task 1 tests. ✅

**Placeholder scan:** none — the Task 1 stub is explicitly temporary and replaced in Task 3 Step 3.

**Type consistency:** `fetchStatus`, `renderStatus`, `renderWarning`, `renderFooter`, `runStatusLive`, `termState`, `readEvent`/`evtEsc`/`evtTimeout`/`evtOther`, `enterCbreak`/`enterCbreakFd`/`restore`, `IsStdoutTerminal`, ANSI writers — names match across all tasks. `runStatusLive(client *rpc.Client) int` signature identical in stub (Task 1) and final (Task 3).

**Note on imports:** `status.go` final import set = `flag`, `fmt`, `io`, `net/rpc`, `os`, `os/signal`, `sync`, `time`, `rpcapi`. Verified against usage.
