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
