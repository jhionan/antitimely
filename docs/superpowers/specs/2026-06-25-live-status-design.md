# Live `status` with Esc-to-exit — design

**Date:** 2026-06-25
**Status:** approved (brainstorming) — ready for implementation plan

## Problem

`atl status` (`internal/cli/status.go`) does a single RPC call and prints a
static snapshot. The user wants `status` to be a live, self-refreshing view
(like `top`), reachable from the interactive menu, and dismissable with **Esc**
to return to the menu.

## Goals

- `status` redraws live in a terminal; counters/totals stay current without
  re-running the command.
- **Esc** leaves the live view. From the menu, the menu redraws; from a direct
  shell invocation, it exits to the shell.
- No new Go module dependency (honor the lean-CLI decision; see memory
  `cli-no-cobra-2026-06-25`).
- Scripting/piping keeps working unchanged.

## Non-goals

- No daemon, RPC, schema, or `StatusReply` changes.
- No single-key navigation for the rest of the menu (only the live status view
  uses raw mode). YAGNI.
- No `q`-to-quit, arrow-key navigation, scrolling, or interactivity beyond
  Esc / Ctrl-C.

## Activation

| Invocation | Behavior |
|---|---|
| Terminal: menu `[1]` or `atl status` | Live view, redraw every 5s |
| `atl status --once` | Single print, exit 0 |
| Output piped / non-TTY (stdout not a char device) | Single print, exit 0 |

TTY detection: stdout is a character device (mirror of existing
`IsStdinTerminal` in `menu.go`, applied to stdout). `--once` forces one-shot
even in a terminal.

Refresh interval: **5s**, matching the tick grid (new billable data can only
appear on the 5s grid). `Idle`/`Uptime` therefore advance in 5s steps.

## Exit keys (live view)

- **Esc** (byte `0x1b`) → restore terminal, return. Caller (menu loop or
  `main`) continues naturally; the menu reprints itself.
- **Ctrl-C** → same clean exit. `ISIG` is kept enabled in cbreak mode so Ctrl-C
  still raises SIGINT; a signal handler runs the same teardown.

## Terminal handling

Library: `golang.org/x/sys/unix` (already an indirect dependency — no new
go.sum module). macOS-only, consistent with the tool.

- **Enter:** write alt-screen enter (`\033[?1047h`); `IoctlGetTermios` to stash
  original; clear `ICANON|ECHO`, **keep `ISIG`**; set `VMIN=0`, `VTIME=50`
  (5.0s read timeout); `IoctlSetTermios`.
- **Loop:** `Read` one byte from stdin.
  - Returns a byte → if `0x1b` (Esc): exit. Else: ignore, redraw.
  - Returns 0 bytes (5s timeout) → fetch + redraw.
  - Each redraw: cursor home + clear (`\033[H\033[J`), then `renderStatus`.
- **Teardown (always — normal exit AND SIGINT handler):** restore original
  termios → flush pending input (`TCIFLUSH`, discards stray escape-sequence
  tails such as an arrow key's `[ A`) → alt-screen leave (`\033[?1047l`) → show
  cursor.

No key-reader goroutine: the timed `Read` is both the refresh clock and the key
listener, which avoids a goroutine leaking and stealing the menu's next byte.

### Bare-Esc vs escape-sequence disambiguation

Arrow keys and similar send `0x1b` followed by more bytes. The core rule is "a
read returning `0x1b` exits." To avoid an arrow key being misread as Esc, on
reading `0x1b` do one additional very-short-timeout read: if more bytes follow,
treat it as a sequence (ignore + redraw); if nothing follows, it is a bare Esc
(exit). The `TCIFLUSH` on teardown is the backstop for any leftover bytes.

## Code structure

Split the current monolithic `cmdStatus` into focused units:

| Unit | Responsibility | Depends on |
|---|---|---|
| `fetchStatus(client) (rpcapi.StatusReply, error)` | one RPC call | rpcapi |
| `renderStatus(w io.Writer, reply)` | pure formatting of one frame; no exit, no globals | reply only |
| `runStatusLive(client) int` | termios setup, redraw loop, key handling, guaranteed teardown | fetchStatus, renderStatus, terminal helpers |
| `cmdStatus(args)` | parse `--once`, TTY check, dispatch one-shot vs live | the above |

New file `internal/cli/terminal.go`: the cbreak-enter / restore / input-flush /
alt-screen helpers (macOS termios via `x/sys/unix`), isolated from `status.go`.

## Content adjustments (live mode only)

1. Accessibility warning: rendered **into the frame** (currently written to
   stderr in `cmdStatus`). One-shot/piped mode keeps writing it to stderr.
2. Footer line proving liveness even when data is unchanged:
   `live · every 5s · Esc to exit · HH:MM:SS`.

## Testing

- Unit-test `renderStatus` against a constructed `StatusReply` covering the
  branches: companies + projects, paused, armed (incl. `SuppressedSeconds`),
  no-company bucket, unassigned bucket with signature count, and the empty
  ("no time tracked yet") case.
- The termios/loop/ANSI glue in `runStatusLive` / `terminal.go` is I/O and is
  not unit-tested, consistent with the rest of the CLI.

## Risks / edge cases

- A leftover escape-sequence tail leaking into the menu — mitigated by the
  bare-Esc disambiguation read plus `TCIFLUSH` on teardown.
- Terminal left in raw mode if teardown is skipped — mitigated by running
  teardown from both the normal return path and a SIGINT handler (deferred).
- Non-TTY / `--once` path must never touch termios.
