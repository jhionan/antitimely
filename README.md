# antitimely

> Automatic, project-aware time tracking for the agentic era — runs as a small background daemon on macOS and Windows, credits **parallel work**, and never asks you to start a timer.

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](./LICENSE)
[![Platforms: macOS and Windows](https://img.shields.io/badge/platform-macOS%20%7C%20Windows-lightgrey.svg)](#install)
[![Made with Go](https://img.shields.io/badge/Go-1.26-00ADD8.svg?logo=go&logoColor=white)](https://go.dev)
[![No CGO](https://img.shields.io/badge/CGO-none-success.svg)](#layered-architecture)

A local time tracker that observes an allowlisted set of programs (apps + binaries) and attributes work to projects via rule matching. It captures **parallel work** — three AI agents on three clients during one hour count as three billable hours.

Built around the observation that modern software work is increasingly *agentic*: you launch a claude/opencode/aider session in one directory, switch to another window, run a build, take a call. antitimely tracks all of it automatically by matching processes' working directories to projects you defined.

See [the original design](./docs/superpowers/specs/2026-05-20-antitimely-design.md) and [the roadmap](./tasks.md).

## Features

- **Multi-project parallel tracking.** Multiple agents in different project directories all get credited simultaneously.
- **Sustained-CPU busy detection.** A tracked process must stay genuinely busy for a couple of polls before its time counts — an idle agent at a prompt or an idle watch-server no longer bills. At the keyboard the bar is low (~3% of a core); when you're idle only real compute (>20%) counts. No more overnight TUI noise.
- **Companies → Projects.** One company can have many projects; each project belongs to one company (or none).
- **Invoice anchors.** Record when you send an invoice to a company; the daemon then shows time billable for the *next* invoice.
- **macOS launch-agent integration.** Boots at login, restarts on crash.
- **YAML config** at `~/.antitimely/config.yaml`.
- **Pure-Go stdlib + sqlite.** No CGO. Single binary, single SQLite file.
- **Invoice PDFs.** `atl invoice generate` numbers, prices and renders an invoice for a company's unbilled, deduped hours, with client prepayments (advances) drawn down automatically.

## Tech stack

| Layer | Choice | Why |
|---|---|---|
| Language | **Go 1.26**, no cgo | One binary that is both the daemon and the CLI; builds without a C toolchain |
| Storage | **SQLite** via [`modernc.org/sqlite`](https://gitlab.com/cznic/sqlite) (pure-Go transpile), WAL mode, one connection | A single file you can back up, query or hand-edit; no CGO |
| Data access | [**sqlc**](https://sqlc.dev) generates `internal/store` from `schema.sql` + `queries.sql` | Type-checked SQL without an ORM |
| IPC | stdlib **`net/rpc`** over a Unix-domain socket on macOS or a per-user named pipe on Windows | The CLI is a thin client of the running daemon |
| CLI | stdlib **`flag`**, hand-rolled dispatch + interactive menu; `golang.org/x/sys/unix` for raw terminal mode | No framework; the command surface is small |
| Config | YAML via [`gopkg.in/yaml.v3`](https://github.com/go-yaml/yaml) | `~/.antitimely/config.yaml`, all keys optional |
| PDF | [`maroto/v2`](https://github.com/johnfercher/maroto) to render invoices, [`ledongthuc/pdf`](https://github.com/ledongthuc/pdf) to read them back in tests | |
| OS integration | Stock macOS tools (`osascript`, `ps`, `lsof`, `ioreg`) and Windows Win32 APIs | All behind `internal/osbridge`, with a fake bridge for tests |
| Process supervision | **launchd** on macOS, **Task Scheduler** on Windows | Starts at login and can restart on failure |
| Signing | `make build` re-signs with a stable Developer ID via `codesign` | macOS keeps the Accessibility grant across rebuilds |

## Install

### macOS

```bash
# 1. Build
make build                          # → ./antitimely

# 2. (Optional but recommended) put it on PATH as `atl`
mkdir -p ~/.local/bin
ln -sf "$PWD/antitimely" ~/.local/bin/atl
# Make sure ~/.local/bin is on your PATH (add to ~/.zshrc if not)

# 3. Install the launch agent (daemon starts now, and at every login)
atl install-launch-agent
```

On first run, macOS may prompt for **Automation** permission (allow it). For accurate window-title capture in Electron-based apps (VS Code, Antigravity, Cursor, etc.), also add `antitimely` to **System Settings → Privacy & Security → Accessibility** and toggle it on. The daemon needs a restart after granting:

```bash
make rebuild
```

### Windows

Build the CLI and the console-free daemon from the same source in PowerShell.

```powershell
go build -o .\antitimely.exe .
go build -ldflags="-H=windowsgui" -o .\antitimely-daemon.exe .
```

Run the daemon from the Windows account whose desktop you want to track. To switch from the foreground daemon to login startup, stop the foreground instance with Ctrl+C first.

```powershell
.\antitimely.exe daemon
# Or install the background daemon and start it now:
.\scripts\windows-startup.ps1 -Action Install
```

Keep the background binary at the path used for installation. State and `daemon.log` live under `%USERPROFILE%\.antitimely`. Manage the scheduled task with `-Action Status`, `Stop`, `Start`, or `Uninstall`. Use `antitimely.exe restart` to restart it.

Add the app identifier shown as `bundle` by `debug frontmost` to the watchlist. The delay gives you time to switch from PowerShell to the app you want to identify. The example uses VS Code. Focus the watched app for at least one tick while active, then check the recorded time and unassigned observations.

```powershell
Start-Sleep -Seconds 3; .\antitimely.exe debug frontmost
.\antitimely.exe watch add app code.exe
.\antitimely.exe debug windows
.\antitimely.exe status --once
.\antitimely.exe review
```

Windows supports focus, idle, process CPU, and transcript tracking. Foreground tracking pauses when the session disconnects or a secure desktop is active. The startup task supervises the daemon and restarts it after an abnormal exit. A packaged app hosted by Application Frame Host can be watched by its `appx:<application-id>` identifier. Run `antitimely debug frontmost` to see the identifier. A protected process can be unavailable to the process and working-directory probes.

Run `antitimely status` in a Windows console for live status. Esc or Ctrl+C closes the view. Use `--once` for scripts.

Process watch identifiers use lowercase executable names such as `codex.exe`. Working-directory rules accept Windows drive letters, UNC shares, and WSL share paths. Drive letters and normal Windows shares compare case-insensitively, and `/mnt/c/…` is treated as `C:\…`. WSL distro paths preserve directory case. For transcripts stored under a configured `\\wsl.localhost\<distro>\…` root, native Linux paths in those files resolve within that distro.

To build the per-user MSI, install WiX 5 and run `scripts/build-windows-installer.ps1 -Version 0.1.0` on Windows. Release builds require a Windows code-signing certificate and SignTool. Supply `-CertificateThumbprint` to sign the executables and MSI, or use `-Unsigned` for a test build. After installation, open **Enable Antitimely tracking** from the Start menu to register startup under your account.

## Quick start

```bash
atl                                    # interactive menu (or run any command below directly)

# Set up companies + projects
atl company add Foca.app
atl company add BClouder
atl project add --company=Foca.app antitimely
atl project add --company=BClouder rumo

# Tell the daemon what to watch
atl watch add binary claude            # any CLI agent: claude, opencode, aider, ...
atl watch add app com.google.antigravity-ide   # any GUI app's bundle id

# After working a bit, see what's tracked
atl status                             # live view, grouped by company → project (Esc/Ctrl-C to exit; --once for a single print)
atl review                             # tag any unassigned signatures
atl report --from=2026-05-13 --to=2026-05-21

# Bill the unbilled hours: renders a numbered PDF and starts the next billing cycle
atl invoice generate --dry-run BClouder   # preview first
atl invoice generate BClouder
```

## How it works

A background daemon polls every 5 seconds, stores everything in a single SQLite file (`~/.antitimely/db.sqlite`), and answers the CLI over a local socket or named pipe. OS introspection is isolated in `internal/osbridge`.

### Each poll: three independent signals

On every tick the daemon gathers up to three kinds of **observation**, each a fingerprint of "something is happening":

| Signal | How the process / work is identified | Carries |
|---|---|---|
| **focus** | The foreground app and window title. macOS uses AppleScript and identifies apps by bundle ID. Windows uses Win32 and identifies apps by executable name. Collected while the user is active. | app ID + window title |
| **agent** | `ps -A` lists every process with its cumulative CPU time. The daemon diffs each PID's CPU against the previous poll; processes burning sustained CPU (the "busy bar", with rise/fall hysteresis — see [What counts as "active"](#what-counts-as-active)) qualify. For each, the working directory is read with `lsof -p <pid> -d cwd`. | binary name + cwd |
| **transcript** | Reads coding-agent session logs: tails Claude Code's `~/.claude*/projects/<dir>/*.jsonl` (every config dir, e.g. `~/.claude-work` used via `CLAUDE_CONFIG_DIR`; discovered at daemon start) and Codex's `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`, and queries opencode's `~/.local/share/opencode/opencode.db` read-only. Takes the `cwd` and last-activity time from the log itself. Needs no CPU, no focus, not even a local process. | cwd + session id |

The agent signal is what captures **parallel work**: `ps` sees three `claude` processes in three directories at once, and each becomes its own observation.

### Which processes count

Two allowlists keep the daemon from tracking your whole system:

- `atl watch add app <bundle-id>` gates the **focus** signal — only allowlisted foreground apps produce an observation.
- `atl watch add binary <name>` marks a binary as a tracked **agent**.

An agent process is tracked if **either** its binary is allowlisted **or** its working directory already falls under a known project prefix — so a build or test process running *inside* a tracked repo counts too, even if you never named it. (To avoid running `lsof` on every process on the machine, the cwd is looked up only for processes that cross the busy bar.)

### Turning an observation into a project

Each unique `(source, bundle id, window title, binary name, cwd, space id)` tuple is stored once as an observation; thousands of ticks then reference that single row. To attribute it, the daemon runs your **rules** in order (priority, then age) and takes the first whose every set field matches:

- **cwd prefix** — the process's working directory equals, or is a subdirectory of, the rule's path (matched literally — casing must match the real folder). A `*` as the pattern's **final character** makes it a glob over the last path segment, so `.../worktrees/md-*` matches every current and future `md-` worktree and anything nested inside it
- **herdr space** — the [herdr](https://github.com/) workspace the process was launched in, resolved from its `HERDR_PANE_ID` (herdr TUI, ids like `w29`) or `HC_TERMINAL_ID` (the Herdr GUI / herdr-core, ids like `sp6437…`; a terminal an agent spawned inherits its parent terminal's space). This is the only field that can separate two spaces sharing one working directory; see `atl spaces` for the ids
- **bundle id** / **binary name** — exact match
- **window title** — substring match

`atl review` builds these rules for you: it surfaces observations that ticked but matched no rule (their raw fingerprints, called *signatures*) and proposes a cwd prefix. Tagging a signature writes the rule **and** retroactively re-credits past ticks that match — in one transaction. The retroactive sweep only ever fills in ticks that are still unassigned, and it is scoped to the tagged observation's herdr space, so tagging one space's work cannot silently re-credit another's.

### Idle, pausing, and what finally gets stored

- **Idle gate.** The daemon reads keyboard/mouse idle time from the HID system (`ioreg`). Past `idle_threshold` (default 2m) the *focus* signal is dropped and the *agent* busy bar gets ~6× stricter — but the *transcript* signal still fires, which is how remote/agent work with no local input is still captured.
- **Pause.** A paused project ignores agent and focus ticks, but an active transcript on it auto-resumes counting — real work overrides a forgotten "end day".
- **Ticks.** Time lands on a 5-second grid: one row per `(timestamp, observation)`, so duplicate signals in the same second collapse. A project's hours are `COUNT(DISTINCT timestamp) × 5s`; within a company, a second worked on two projects at once bills once.

See [Layered architecture](#layered-architecture) for the code map and [For AI coding agents](#for-ai-coding-agents) for the agent-specific details.

## For AI coding agents

This app was designed with AI-agentic workflows as a first-class use case. **You don't need to integrate anything into your agent** — tracking is fully passive.

### How an agent gets tracked

When you allowlist an agent binary:

```bash
atl watch add binary claude
atl watch add binary opencode
atl watch add binary aider
```

…the daemon polls every 5 seconds for *any process matching that name*, reads its working directory via `lsof`, and matches the cwd against your project rules. So if you run:

```bash
cd ~/work/foca-api && claude
cd ~/work/rumo && claude
cd ~/work/antitimely && opencode
```

…the daemon sees three independent agent processes in three different directories, credits each tick to a different project, and your daily totals correctly reflect three parallel hours' worth of work-in-progress for three different clients.

### Project rules

A project rule says *"if an allowlisted agent's working directory begins with this path, the work belongs to this project."* The stored prefix is matched **literally** against each process's real working directory; when `atl review` *proposes* a prefix it finds the project name in the path case-insensitively (so project `Antitimely` yields the prefix `~/work/antitimely`, which then matches exactly).

You can create rules:
- **Interactively** via `atl review` — recommended; it proposes the right cwd prefix based on what it actually saw
- **From the menu** — `atl` → Rules / Projects sections
- **Directly** via SQL on `~/.antitimely/db.sqlite` if you want bulk setup

### What counts as "active"

Tracking only fires when the agent is *doing real work*, not just sitting at a prompt. A tracked process must stay above the CPU "busy bar" for a couple of consecutive polls (`agent_busy_rise_ticks`) before its time counts, and drop below it for a few polls (`agent_busy_fall_ticks`) before it stops — so a brief blip never starts the clock and a quiet gap between streamed tokens never stops it prematurely:

- **While you're at the keyboard:** sustained CPU above ~3% of a core counts (claude streaming tokens, opencode running tools, etc.); an idle prompt (~0.4%) does not.
- **While you're idle (> 2 minutes of no input):** only processes doing >20% CPU count, so a claude TUI waiting for your reply doesn't accumulate phantom time overnight

Tune both via `~/.antitimely/config.yaml`:

```yaml
agent_cpu_threshold: 15       # centiseconds/poll "busy bar" when user is active (~3% of a core)
agent_cpu_threshold_idle: 100 # centiseconds/poll busy bar when user is idle (~20%)
agent_busy_rise_ticks: 2      # consecutive busy polls before a process counts
agent_busy_fall_ticks: 3      # consecutive quiet polls before it stops (hysteresis)
idle_threshold: 2m
```

### Tracking signals: focus, agent CPU, and transcripts

The daemon combines three independent signals to track work:

- **Focus signal** — your foreground window's application and title (tells which project you're looking at)
- **Agent CPU signal** — processes matching your allowlist burning sustained CPU in a tracked project directory (picks up work the process is doing, and credits it even when you've switched windows)
- **Transcript signal** — Claude Code, Codex and opencode sessions writing to a tracked project directory (captures remote work and planning sessions that generate no local CPU or window focus). For Codex, which is usually opened on a parent folder, the repo is taken from the agent's command events rather than the session's own cwd

Transcript activity **overrides pause** — if a project is paused but you're actively working on it through an agent session, the daemon resumes ticking automatically. Transcript counting stops when the session is idle for longer than the grace period (default 10 minutes).

Configure via:

```yaml
transcript_tracking: true         # enable transcript watching
transcript_grace: 10m             # session counts this long after last turn
# transcript_roots: [~/.claude/projects, ~/.claude-work/projects]  # default: every ~/.claude*/projects
opencode_db: ~/.local/share/opencode/opencode.db   # "off" disables opencode
codex_root: ~/.codex/sessions                       # "off" disables Codex
```

### Generating work summaries

`atl summary` emits a markdown report combining tracked hours per project with the git commits in each project's directory for the same date range — perfect for piping into a coding agent that polishes it into an invoice description.

```bash
atl summary --from=2026-05-19 --to=2026-05-26
atl summary --from=2026-05-19 --to=2026-05-26 | claude "polish into invoice descriptions"
```

Flags:
- `--from`, `--to` — date range (default: today)
- `--project=<name>`, `--company=<name>` — filter scope
- `--all-authors` — include commits by all authors (default: only yours via `git config user.email`)
- `--txt` — plain text instead of markdown

### BClouder timesheets

The default billing workflow produces a timesheet with daily hours separated by
BClouder **modules and submodules**, such as Packing Slips, Deduction,
PS - Cargo Dockers, PS - Auto Matcher, and MD - Cleanse. These categories are
assigned from work evidence when preparing the report; shared repository names
and the tracker's project labels are only hints.

Module hours may overlap. The report shows **Invoice hours** separately, counting
simultaneous company work once, so it does not present a combined 30-hour day as
daily worked time. Unclear allocation remains shared/unallocated. The CSV and PDF
include module totals, invoice reconciliation and verified work descriptions.

See [the billing runbook](docs/billing-runbook.md) before generating an invoice or
timesheet. This is currently a manual reporting workflow; there is no
`atl timesheet` command. Antitimely still uses project records for collection,
but BClouder's buckets represent its **clients: Rumo and VCNA**. DaaS,
Master Data and Deduction are classified as modules/submodules inside the
timesheet, not separate tracking buckets. Reports can therefore follow
**client → module → submodule**, while the invoice remains company-wide.

### Agents driving the CLI

Agents themselves can use the CLI to query or update state. Useful patterns:

```bash
# An agent task script can record an explicit project/company assignment:
atl project add --company=Foca.app new-feature

# Record when an external "invoice sent" event happens (e.g. integration with billing):
atl invoice send --at=2026-05-20 --note="May" Foca.app

# Query current totals (parse the output):
atl status
atl report --from=2026-05-01 --to=2026-05-31
```

If you build something on top, exit codes are stable: `0` success, `1` runtime error, `2` daemon unreachable, `64` usage error.

## Optional device sync

The Go sync server exposes Connect RPCs defined in the [versioned protobuf schema](api/antitimely/sync/v1/sync.proto). The daemon can run locally without a server or network access.

```sh
go build -o antitimely-server ./cmd/antitimely-server
./antitimely-server create-account -db /var/lib/antitimely/sync.sqlite -name team -out account.token
./antitimely-server serve -db /var/lib/antitimely/sync.sqlite -listen 127.0.0.1:8080
./antitimely-server register-device -url https://sync.example.com -account-token-file account.token -name laptop -out device.json
```

Run the server on loopback behind an HTTPS reverse proxy, or supply `-tls-cert` and `-tls-key` to serve HTTPS directly. Keep the SQLite database in persistent storage and back it up with SQLite's online backup API or a stopped server. Protect the account token and copy only the device credential file to the tracker machine.

Set these keys in the daemon's YAML config and restart it.

```yaml
sync_endpoint: https://sync.example.com
sync_token_file: ~/.antitimely/device.json
```

The daemon queues project names and assigned time intervals recorded after opt-in. Observation details, rules, window titles, and file paths stay local. Upload retries are idempotent. Use `antitimely sync status` to see the queue and the most recent result. Set both keys to empty to pause sync. Each database is bound to its first endpoint and device to prevent sending history to another account.

Account-authenticated RPCs list device projects, link projects across devices to a shared group, and report the union of time intervals by project and across the account. Devices can only upload under their own identity. Projects with the same name remain distinct until explicitly linked.

### ClickUp and the Windows tray

Open **Antitimely tray** from the Windows Start menu. Choose **Start timer** for a standalone session, with an optional name, or choose **Start without a task** from the ClickUp task list. **Stop timer** saves the session to **History** on the device. The CLI offers the same controls through `antitimely timer start ["session name"]`, `antitimely timer stop`, and `antitimely timer status`.

Generate the server-side encryption key once and keep it beside the deployment secrets.

```sh
./antitimely-server generate-clickup-key -out clickup.key
./antitimely-server serve -db /var/lib/antitimely/sync.sqlite -clickup-key-file clickup.key
./antitimely-server clickup-connect -url https://sync.example.com -account-token-file account.token -personal-token-file clickup.token
```

For OAuth, also pass `-clickup-oauth-file` to the server. The JSON file contains `client_id`, `client_secret`, and `redirect_url`. Register the exact HTTPS `/clickup/oauth/callback` URL with ClickUp. Run `clickup-oauth` with the server URL and account token file, then open the authorization URL it prints. To enable the tray’s **Sign in with ClickUp** button for an enrolled device, include its device ID in the OAuth configuration’s `sign_in_device_ids` array and restart the server. Run `clickup-disconnect` with the same options to remove the stored credential.

When connected, use **Refresh** to update your assigned ClickUp tasks. Select a task's Play button to start tracking it. Standalone sessions and ClickUp tasks share one running timer. The timer measures elapsed wall time through idle, sleep, and daemon restart. Stopping or switching saves the completed interval in SQLite and queues it for submission if it belongs to a ClickUp task. **Assign tracked time** previews the exact duration from an existing date range before you confirm it. Submitted or reserved intervals are excluded from future allocations.

The tray shows queued, published, failed, and unconfirmed entries. Antitimely reconciles an unconfirmed ClickUp creation using its reference marker. It retries the lookup while the entry remains unconfirmed. Closing the panel hides it and leaves the tracker running.

## Configuration

The daemon reads `~/.antitimely/config.yaml` at startup. Defaults are reasonable; tweak only what you need.

```bash
atl config init       # write the default config (with all options commented)
atl config show       # print the current config
atl config path       # print the file path
```

Example:

```yaml
interval: 5s                      # polling interval
idle_threshold: 2m                # idle = no kbd/mouse for this long
agent_cpu_threshold: 15           # centi-seconds/poll busy bar for an active-user agent
agent_cpu_threshold_idle: 100     # centi-seconds/poll busy bar for an idle-user agent
agent_busy_rise_ticks: 2          # consecutive busy polls before counting
agent_busy_fall_ticks: 3          # consecutive quiet polls before stopping
```

Precedence: defaults → config file → CLI flags on `atl daemon`.

## Commands

| Command | What it does |
|---|---|
| `atl` | interactive menu (TTY-aware; piped invocations fall back to usage) |
| `atl status [--once]` | live self-refreshing view (every 5s) of grouped totals + daemon uptime, idle, permission state; Esc/Ctrl-C exits. In a terminal it refreshes in place; `--once` (or piped output) prints a single snapshot and exits |
| `atl review` | walk through unassigned observations, tag them, build rules |
| `atl rules list\|add\|delete` | list, create, or remove rules; `add` takes `--project --priority --bundle --title --binary --cwd --space` |
| `atl spaces` | list herdr spaces and their ids, for use with `rules add --space` |
| `atl report [--from --to]` | date-range totals, plus per-company deduped hours (parallel work on two projects of one company bills once) |
| `atl summary [--from --to] [--project --company] [--all-authors] [--txt]` | markdown report: hours + git commits per project |
| `atl company add\|list\|delete <name>` | manage companies |
| `atl project add [--company=<c>] <name>` | manage projects |
| `atl project list\|delete\|set-company` | … |
| `atl watch add app\|binary <id>` | allowlist a program |
| `atl watch list\|remove` | … |
| `atl rules list\|delete <id>` | manage match rules |
| `atl invoice generate <company> [--from --to --issue-date --note --discount --no-credit --dry-run]` | number and render an invoice PDF for the unbilled, deduped hours, applying any advance credit, and close the billing period |
| `atl invoice advance <company> --amount=N` | record a client prepayment; later invoices draw it down |
| `atl invoice balance <company>` | remaining advance credit |
| `atl invoice send [--at=<date>] [--note=<...>] <company>` | record an invoice cutoff **only** (no PDF, no number) |
| `atl invoice list [<company>]` | invoice history |
| `atl invoice delete <id>` | remove an invoice |
| `atl end-day` / `atl start-day` | pause / resume every project |
| `atl reset all\|ticks [--yes]` | wipe data (`ticks` keeps projects/rules; `all` wipes everything) |
| `atl config init\|show\|path` | manage `~/.antitimely/config.yaml` |
| `atl install-launch-agent` | install launchd plist + start daemon |
| `atl uninstall-launch-agent` | stop daemon + remove launchd plist |
| `atl restart` | restart the running daemon (`launchctl kickstart -k`) |
| `atl grant-accessibility` | open the macOS Accessibility pane for window-title capture |
| `atl debug frontmost\|windows\|idle` | inspect what the macOS bridge sees right now |
| `atl daemon [flags]` | run the daemon in the foreground (debugging) |
| `atl help` | full usage |

Flags must come *before* positional args (Go's stdlib `flag` package stops parsing at the first non-flag arg):

```bash
atl invoice send --at=2026-05-20 BClouder    # works
atl invoice send BClouder --at=2026-05-20    # fails — flags after positional
```

## Where state lives

| Path | Purpose |
|---|---|
| `~/.antitimely/db.sqlite` | all data (WAL mode) |
| `~/.antitimely/antitimely.sock` | Unix-domain `net/rpc` socket between CLI and daemon |
| `~/.antitimely/antitimely.pid` | daemon's PID |
| `~/.antitimely/config.yaml` | user-managed config (optional) |
| `~/.antitimely/daemon.log` | daemon stdout (when run via launchd) |
| `~/.antitimely/daemon.err` | daemon stderr (when run via launchd) |
| `~/Library/LaunchAgents/com.rian.antitimely.plist` | launchd auto-start |

## Development

```bash
make build       # go build -o ./antitimely .
make test        # go test ./... -count=1
make sqlc        # regenerate internal/store from schema.sql/queries.sql
make rebuild     # build + restart the running launch agent
make install     # build + install launch agent (first-time)
make uninstall   # stop + remove launch agent
make clean       # remove the local binary
make help        # list all targets
```

Or `./scripts/rebuild.sh` for the same rebuild+restart flow without `make`.

### Layered architecture

```
internal/
├── domain/      pure Go: types, rule matching, rule generalization (zero deps)
├── store/       sqlc-generated SQLite bindings (regenerate with `make sqlc`)
├── osbridge/    the only place OS calls live: the Bridge interface + fake (shared),
│                macOS implementation in *_darwin.go (osascript/ps/lsof/ioreg)
├── daemon/      pipeline, polling, cache, RPC handlers, WAL checkpointer, lifecycle
├── invoice/     invoice numbering, line items, advance-credit drawdown, PDF rendering
├── herdr/       resolves herdr TUI panes and Herdr GUI (herdr-core) terminals and Claude sessions to workspaces (fails closed)
├── rpcapi/      shared net/rpc types between daemon and CLI
└── cli/         subcommand dispatch + interactive menu
```

## Tests

```bash
go test ./...
```

Coverage: domain logic (matching + generalization), SQLite schema + queries, RPC round-trips via `net.Pipe`, an end-to-end integration test that boots the real daemon in a tmpdir.

## Design reference

`docs/superpowers/specs/2026-05-20-antitimely-design.md` — the canonical design, including:
- The tick-based vs. session-based accounting decision
- Why focus signals + agent signals are separate and combined
- The asymmetric idle threshold (added after a real overnight-overcounting bug)
- Phase 2 sketches (invoice PDFs, web UI)

## Contributing

Contributions are welcome. The codebase is small, pure-Go (no CGO), and layered for easy navigation (see [Layered architecture](#layered-architecture)).

1. `make test` should stay green — domain logic, store queries, and RPC round-trips are all covered.
2. Keep system calls behind the `internal/osbridge` bridge so the rest of the code stays testable with the fake bridge.
3. SQL changes go in `schema.sql` / `queries.sql`, then `make sqlc` to regenerate `internal/store`.

Open an issue to discuss larger changes before sending a PR.

## License

[MIT](./LICENSE) © Jhionan
