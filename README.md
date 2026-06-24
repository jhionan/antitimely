# antitimely

> Automatic, project-aware time tracking for the agentic era — runs as a tiny macOS daemon, credits **parallel work**, and never asks you to start a timer.

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](./LICENSE)
[![Platform: macOS](https://img.shields.io/badge/platform-macOS-lightgrey.svg)](#install)
[![Made with Go](https://img.shields.io/badge/Go-1.26-00ADD8.svg?logo=go&logoColor=white)](https://go.dev)
[![No CGO](https://img.shields.io/badge/CGO-none-success.svg)](#layered-architecture)

A macOS time tracker that observes an allowlisted set of programs (apps + binaries) and attributes work to projects via rule matching. It captures **parallel work** — three AI agents on three clients during one hour count as three billable hours.

Built around the observation that modern software work is increasingly *agentic*: you launch a claude/opencode/aider session in one directory, switch to another window, run a build, take a call. antitimely tracks all of it automatically by matching processes' working directories to projects you defined.

See `docs/superpowers/specs/2026-05-20-antitimely-design.md` for the full design.

## Features

- **Multi-project parallel tracking.** Multiple agents in different project directories all get credited simultaneously.
- **Sustained-CPU busy detection.** A tracked process must stay genuinely busy for a couple of polls before its time counts — an idle agent at a prompt or an idle watch-server no longer bills. At the keyboard the bar is low (~3% of a core); when you're idle only real compute (>20%) counts. No more overnight TUI noise.
- **Companies → Projects.** One company can have many projects; each project belongs to one company (or none).
- **Invoice anchors.** Record when you send an invoice to a company; the daemon then shows time billable for the *next* invoice.
- **macOS launch-agent integration.** Boots at login, restarts on crash.
- **YAML config** at `~/.antitimely/config.yaml`.
- **Pure-Go stdlib + sqlite.** No CGO. Single binary, single SQLite file.

## Install

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
atl status                             # grouped by company → project
atl review                             # tag any unassigned signatures
atl report --from=2026-05-13 --to=2026-05-21

# When you send an invoice, mark the cutoff so the next billing cycle starts
atl invoice send --at=2026-05-20 --note="May invoice" BClouder
```

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

A project rule says *"if an allowlisted agent's working directory begins with this path, the work belongs to this project."* The match is case-insensitive (so `Antitimely` the project matches `~/work/antitimely` the folder).

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
- **Transcript signal** — Claude Code sessions actively writing transcripts to a tracked project directory (captures remote work and planning sessions that generate no local CPU or window focus)

Transcript activity **overrides pause** — if a project is paused but you're actively working on it via Claude Code, the daemon resumes ticking automatically. Transcript counting stops when the session is idle for longer than the grace period (default 10 minutes).

Configure via:

```yaml
transcript_tracking: true         # enable transcript watching
transcript_grace: 10m             # session counts this long after last turn
transcript_root: ~/.claude/projects
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
| `atl status` | current grouped totals + daemon uptime, idle, permission state |
| `atl review` | walk through unassigned observations, tag them, build rules |
| `atl report [--from --to]` | date-range totals |
| `atl summary [--from --to] [--project --company] [--all-authors] [--txt]` | markdown report: hours + git commits per project |
| `atl company add\|list\|delete <name>` | manage companies |
| `atl project add [--company=<c>] <name>` | manage projects |
| `atl project list\|delete\|set-company` | … |
| `atl watch add app\|binary <id>` | allowlist a program |
| `atl watch list\|remove` | … |
| `atl rules list\|delete <id>` | manage match rules |
| `atl invoice send [--at=<date>] [--note=<...>] <company>` | record an invoice cutoff |
| `atl invoice list [<company>]` | invoice history |
| `atl invoice delete <id>` | remove an invoice |
| `atl reset all\|ticks [--yes]` | wipe data (`ticks` keeps projects/rules; `all` wipes everything) |
| `atl config init\|show\|path` | manage `~/.antitimely/config.yaml` |
| `atl install-launch-agent` | install launchd plist + start daemon |
| `atl uninstall-launch-agent` | stop daemon + remove launchd plist |
| `atl restart` | restart the running daemon (`launchctl kickstart -k`) |
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
├── macos/       the only place subprocesses live (osascript/ps/lsof/ioreg)
├── daemon/      pipeline, polling, cache, RPC handlers, lifecycle
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
2. Keep system calls behind the `internal/macos` bridge so the rest of the code stays testable with the fake bridge.
3. SQL changes go in `schema.sql` / `queries.sql`, then `make sqlc` to regenerate `internal/store`.

Open an issue to discuss larger changes before sending a PR.

## License

[MIT](./LICENSE) © Jhionan
