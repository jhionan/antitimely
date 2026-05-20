# antitimely

Personal macOS time tracker that observes an allowlisted set of programs (apps + binaries) and attributes work to projects via rule matching. Captures parallel work — three agents on three clients during one hour count as three billable hours.

See `docs/superpowers/specs/2026-05-20-antitimely-design.md` for the full design.

## Build

```bash
go build -o antitimely .
```

## Quick start

```bash
# Start the daemon (foreground; ^C to stop)
./antitimely daemon

# In another terminal:
./antitimely status                                # snapshot
./antitimely project add foca-api                  # create a project
./antitimely watch add app com.google.antigravity  # allowlist a GUI app by bundle id
./antitimely watch add binary claude               # allowlist a CLI agent by process name
./antitimely review                                # interactively tag unassigned observations
./antitimely report --from=2026-05-13 --to=2026-05-21
```

## Permissions

On first launch the daemon will trigger macOS Automation prompts for `osascript` → System Events. Allow these once. If you decline, window-title capture is disabled but the daemon continues running — see `antitimely status` for the current `Permission:` state.

## Where state lives

- Database: `~/.antitimely/db.sqlite` (WAL mode)
- Unix socket: `~/.antitimely/antitimely.sock`
- PID file: `~/.antitimely/antitimely.pid`

## Tests

```bash
go test ./...
```
