package cli

import (
	"fmt"
	"io"
)

const (
	ansiAltEnter   = "\x1b[?1049h" // ?1049 saves the cursor; ?1047 did not, so the menu overprinted scrollback
	ansiAltLeave   = "\x1b[?1049l" // restores the cursor saved on enter
	ansiClearHome  = "\x1b[H\x1b[J"
	ansiHideCursor = "\x1b[?25l"
	ansiShowCursor = "\x1b[?25h"
	// Disable terminal modes that emit escape sequences on their own. Focus
	// reporting (?1004) sends ESC[I / ESC[O when the window gains/loses focus;
	// bracketed paste (?2004) wraps pastes in ESC[200~ / ESC[201~. Either, if
	// left on, feeds stray ESC bytes into the live view and was closing it.
	ansiFocusOff = "\x1b[?1004l"
	ansiPasteOff = "\x1b[?2004l"
)

func altScreenEnter(w io.Writer) { fmt.Fprint(w, ansiAltEnter) }
func altScreenLeave(w io.Writer) { fmt.Fprint(w, ansiAltLeave) }
func clearScreen(w io.Writer)    { fmt.Fprint(w, ansiClearHome) }
func hideCursor(w io.Writer)     { fmt.Fprint(w, ansiHideCursor) }
func showCursor(w io.Writer)     { fmt.Fprint(w, ansiShowCursor) }

// quietTerminalInput disables focus reporting and bracketed paste so the
// terminal stops emitting escape sequences while the live view is open.
func quietTerminalInput(w io.Writer) { fmt.Fprint(w, ansiFocusOff+ansiPasteOff) }

type statusEvent int

const (
	evtTimeout statusEvent = iota // VTIME elapsed, no key
	evtEsc                        // bare Escape pressed
	evtOther                      // any other key / escape sequence
)
