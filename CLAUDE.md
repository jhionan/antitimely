# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`antitimely` is a macOS time tracker: one Go binary that runs both as a **background daemon** (launchd-managed) and as the **`atl` CLI**. It observes allowlisted processes/apps, attributes their working directories to projects via rules, and credits **parallel work** (three agents in three repos = three billable hours). Pure Go, no CGO (`modernc.org/sqlite`), single SQLite file.

### Key docs
- **README.md** — canonical user-facing reference (commands, config, signals).
- **`docs/superpowers/specs/2026-05-20-antitimely-design.md`** — design of record (tick vs session accounting, why the signals are combined, the asymmetric idle threshold).
- **`docs/billing-runbook.md`** — step-by-step for producing the monthly **invoice + timesheet**. **Read this before any billing / invoice / timesheet task.** It covers the deduped-vs-all-hours rule, the exact commands and SQL for hours per day, gathering client-facing descriptions from git commits + Claude Code transcripts, and the CSV/PDF output format & location (`~/Documents/Espanha/Autonomo/Invoices/`).

## Commands

```bash
make build          # go build -o ./antitimely .
make test           # go test ./... -count=1
go vet ./...
make sqlc           # regenerate internal/store from schema.sql + queries.sql (needs `sqlc`)
make rebuild        # build + restart the launchd daemon (uninstall + install)

# Single test / package:
go test ./internal/domain/ -run TestMatchRules -v
go test ./internal/daemon/ -run TestCheckpoint -v
```

The daemon runs under launchd (`com.rian.antitimely`). `make rebuild` cycles it; `atl restart` does `launchctl kickstart -k`. To run it in the foreground for debugging: `./antitimely daemon`. State lives in `~/.antitimely/` (`db.sqlite`, `antitimely.sock`, `config.yaml`, `daemon.{log,err}`).

## Architecture

**Two roles, one binary, split over a Unix socket.** `main.go` routes `daemon` → `daemon.Run`; everything else → `cli.Dispatch` (`internal/cli/dispatch.go`), which talks to the daemon via `net/rpc` over `~/.antitimely/antitimely.sock`. Shared RPC types live in `internal/rpcapi`. A dead socket means the daemon is down/restarting — CLI exit codes: `0` ok, `1` runtime, `2` daemon unreachable, `64` usage.

**The daemon poll loop** (`internal/daemon`): `Poller` (poll.go) calls `Pipeline.RunTick` (pipeline.go) every 5s. Each tick gathers up to three independent **signals**, upserts an **observation** per signal, matches it to a project, and writes **ticks**:
- **focus** — frontmost app + window title (via `osascript`), only while at the keyboard.
- **agent** — processes burning sustained CPU (`ps` diff with rise/fall hysteresis), cwd read via `lsof`. This is what captures parallel work.
- **transcript** — Claude Code `~/.claude/projects/*/*.jsonl` activity; needs no CPU/focus, and **overrides pause** (real work resumes a forgotten "end day").

**Data model & the dedup rule that matters most.** An `observation` is a unique `(source, bundle_id, window_title, binary_name, cwd)` fingerprint, stored once. A `tick` is `(ts, observation_id, project_id)` on a 5-second grid; PK `(ts, observation_id)`. A project's hours = `COUNT(DISTINCT ts) × 5s`. **Company-level billable dedups across projects** — a second worked on two projects at once bills once. This deduped total is what `atl invoice generate` charges; a plain per-project sum (all hours worked) is higher. Do not confuse the two (see gotchas).

**Rule matching** (`internal/domain/match.go`, pure/zero-dep): first rule (by priority, then age) whose every set field matches wins. cwd prefix is literal `cwd == prefix || cwd startsWith prefix+"/"` (case-sensitive); bundle id / binary exact; window title substring. `atl review` creates a rule **and** retroactively retags all matching past ticks in one transaction (`TagSignature` in rpc.go: `AddRule` + `ApplyRuleRetroactivelyCounted` + `ReloadCache`); `IgnoreSignature` is the sibling that marks an observation ignored instead of tagging it.

**Layers:**
```
internal/domain/   pure Go — types, rule matching, generalization (zero deps, unit-tested)
internal/store/    sqlc-GENERATED SQLite bindings — never hand-edit; edit schema.sql/queries.sql + `make sqlc`
internal/macos/    the ONLY place subprocesses live (osascript/ps/lsof/ioreg); fake.go for tests
internal/daemon/   pipeline, poller, cache, RPC handlers, WAL checkpointer, lifecycle
internal/invoice/  invoice numbering, line items, formatting, PDF rendering (`atl invoice generate`)
internal/rpcapi/   shared net/rpc request/reply types
internal/cli/      hand-rolled subcommand dispatch + interactive menu
```

**SQLite is single-connection** (`SetMaxOpenConns(1)` in daemon.go `openDB`) — all DB access serializes; WAL mode. `checkpoint.go` runs periodic `PRAGMA wal_checkpoint(TRUNCATE)` (on startup, every 5m, on shutdown) so the WAL can't bloat and slow reads.

## Non-obvious gotchas

- **Invoice hours ≠ timesheet hours.** Invoice = **deduped** company-level `COUNT(DISTINCT ts)` (`atl invoice generate`). Timesheet = **all hours worked** (per-project sums, always ≥ invoice). Full monthly procedure is in `docs/billing-runbook.md`.
- **Timesheet descriptions must be filtered by commit author.** The repos are shared — a period can hold more teammate commits than yours (ES-0006: 102 of 216 front-end commits were a teammate's). Describing them as your work bills someone else's output. Check `%an` before writing any line; see `docs/billing-runbook.md` Step 5.
- **Advance invoices are anchor-neutral and drive an automatic drawdown.** A `kind='advance'` row (a client prepayment, e.g. `ES-0007`) is excluded from `LastInvoiceSentForCompany`, so it closes no billing period and loses no hours. `atl invoice generate` then applies `min(remaining_credit, line_total)` automatically — `--no-credit` opts out — and `atl invoice balance <company>` reports what's left. The balance is **derived** (`SUM(advance totals) − SUM(credit_applied_cents)`), never stored, so deleting a credit-bearing invoice moves it: that needs `--force`. Create advances with `atl invoice advance <company> --amount=N` or the menu, never `generate` + a hand-edit — generate would auto-apply the existing credit first and under-state the new advance. Design: `docs/superpowers/specs/2026-08-03-invoice-credit-drawdown-design.md`.
- **Only `atl invoice generate <company>` produces a PDF** (+ number + anchor). The interactive menu's "Send invoice" and `atl invoice send` **only record a billing anchor** — using them thinking they "send the invoice" closes the period with no document.
- **`internal/store/*.go` is generated.** SQL changes go in `schema.sql` / `queries.sql`, then `make sqlc`. **Keep `queries.sql` ASCII-only:** sqlc v1.30.0 tracks query text by *byte* offset but comment length in *runes*, so a single multi-byte character (`—`, `…`, an accent) in a comment silently shifts every generated query string below it — the build still compiles and the corruption only surfaces at runtime.
- **Keep syscalls behind `internal/macos`.** The rest of the code stays testable via the fake bridge; don't shell out elsewhere.
- **The CLI is hand-rolled stdlib `flag` (no Cobra) by choice.** Flags must come *before* positional args; company names are case-sensitive. When adding a subcommand, update both the dispatch switch and `printUsage`.
- **After editing rules/ticks directly in SQLite, `kill -HUP` the daemon** (or restart) so it reloads the rule/allowlist cache (`ReloadCache`).
- **A new Accessibility grant only applies to a freshly started daemon — `atl restart`, not `make rebuild`.** Grants survive rebuilds now: `make build` re-signs with a stable Developer ID (`SIGN_ID` in the Makefile), so TCC keys the grant on the signing identity rather than the code hash. A plain `go build` bypasses that and reverts the binary to Go's ad-hoc signature, which *does* break the grant — always build via `make`. Verify with `codesign -dv --verbose=2 ./antitimely` (`Identifier=com.rian.antitimely`, `TeamIdentifier=5KNATBVY62`).
- **`Permission:` in `atl status` is a probe result, not a live check.** It is written only from `collectFocusSignal`, which runs solely while an *allowlisted* app is frontmost — so it goes stale whenever you work anywhere the daemon can't watch, and reports `unknown` past `permStaleAfterSec`. Don't diagnose from it: real proof of title capture is a `source='focus'` observation with a non-empty `window_title` and a fresh tick. Testing via the CLI proves nothing either — TCC attributes Accessibility to the *responsible* process, which for a terminal-launched `atl` is the terminal.
- **The allowlist is per-program, and switching editor/terminal silently kills a whole signal.** Focus signals are dropped for any bundle not in `watched_programs`, with no warning. Note focus carries **no cwd** (only bundle + title), so `match_cwd_prefix` rules can never attribute a focus tick — that's why a terminal running many agents at once (`herdr`) is deliberately not allowlisted: its title is constant, so its time is unattributable by construction. Agent + transcript signals carry that work via cwd instead.
