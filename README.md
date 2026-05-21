# antitimely

Personal macOS time tracker that observes an allowlisted set of programs (apps + binaries) and attributes work to projects via rule matching. Captures **parallel work** — three AI agents on three clients during one hour count as three billable hours.

Built around the observation that modern software work is increasingly *agentic*: you launch a claude/opencode/aider session in one directory, switch to another window, run a build, take a call. antitimely tracks all of it automatically by matching processes' working directories to projects you defined.

See `docs/superpowers/specs/2026-05-20-antitimely-design.md` for the full design.

## Features

- **Multi-project parallel tracking.** Multiple agents in different project directories all get credited simultaneously.
- **Asymmetric idle thresholds.** When you're at the keyboard, low-CPU work counts. When you're idle, only processes doing real work (>20% of a core) count. No more overnight TUI noise.
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

Tracking only fires when the agent is *doing real work*, not just sitting at a prompt:

- **While you're at the keyboard:** any CPU activity above 1% counts (claude streaming tokens, opencode running tools, etc.)
- **While you're idle (> 2 minutes of no input):** only processes doing >20% CPU count, so a claude TUI waiting for your reply doesn't accumulate phantom time overnight

Tune both via `~/.antitimely/config.yaml`:

```yaml
agent_cpu_threshold: 5        # centiseconds/tick when user is active
agent_cpu_threshold_idle: 100 # centiseconds/tick when user is idle
idle_threshold: 2m
```

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
agent_cpu_threshold: 5            # centi-seconds/tick to count an active-user agent
agent_cpu_threshold_idle: 100     # centi-seconds/tick to count an idle-user agent
```

Precedence: defaults → config file → CLI flags on `atl daemon`.

## Commands

| Command | What it does |
|---|---|
| `atl` | interactive menu (TTY-aware; piped invocations fall back to usage) |
| `atl status` | current grouped totals + daemon uptime, idle, permission state |
| `atl review` | walk through unassigned observations, tag them, build rules |
| `atl report [--from --to]` | date-range totals |
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
