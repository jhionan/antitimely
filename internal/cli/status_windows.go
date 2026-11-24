package cli

import (
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var statusSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

var readConsoleInput = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReadConsoleInputW")

type statusConsole struct {
	in, out         windows.Handle
	inMode, outMode uint32
}

// INPUT_RECORD consists of a WORD event type and a 16-byte union aligned on
// four bytes. For a key event the first DWORD is bKeyDown and the virtual key
// is the WORD following the repeat count.
type consoleInputRecord struct {
	eventType uint16
	_         uint16
	keyDown   uint32
	repeat    uint16
	key       uint16
	_         [8]byte
}

func enterStatusTerminal() (*statusConsole, error) {
	s := &statusConsole{in: windows.Handle(os.Stdin.Fd()), out: windows.Handle(os.Stdout.Fd())}
	if err := windows.GetConsoleMode(s.in, &s.inMode); err != nil {
		return nil, err
	}
	if err := windows.GetConsoleMode(s.out, &s.outMode); err != nil {
		return nil, err
	}
	if err := windows.SetConsoleMode(s.out, s.outMode|windows.ENABLE_PROCESSED_OUTPUT|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		return nil, err
	}
	mode := (s.inMode | windows.ENABLE_PROCESSED_INPUT) &
		^uint32(windows.ENABLE_LINE_INPUT|windows.ENABLE_ECHO_INPUT|windows.ENABLE_VIRTUAL_TERMINAL_INPUT)
	if err := windows.SetConsoleMode(s.in, mode); err != nil {
		_ = windows.SetConsoleMode(s.out, s.outMode)
		return nil, err
	}
	return s, nil
}

func (s *statusConsole) readEvent() statusEvent {
	deadline := time.Now().Add(5 * time.Second)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return evtTimeout
		}
		event, err := windows.WaitForSingleObject(s.in, uint32(remaining.Round(time.Millisecond).Milliseconds()))
		if err != nil {
			return evtEsc
		}
		if event != windows.WAIT_OBJECT_0 {
			return evtTimeout
		}
		var record consoleInputRecord
		var read uint32
		ok, _, _ := readConsoleInput.Call(uintptr(s.in), uintptr(unsafe.Pointer(&record)), 1, uintptr(unsafe.Pointer(&read)))
		if ok == 0 {
			return evtEsc
		}
		if read == 1 && record.isEscape() {
			return evtEsc
		}
	}
}

func (r consoleInputRecord) isEscape() bool {
	const keyEvent, escape = 1, 0x1b
	return r.eventType == keyEvent && r.keyDown != 0 && r.key == escape
}

func (s *statusConsole) restore() {
	_ = windows.SetConsoleMode(s.in, s.inMode)
	_ = windows.SetConsoleMode(s.out, s.outMode)
}
