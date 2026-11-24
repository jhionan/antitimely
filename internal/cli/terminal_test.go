package cli

import "testing"

// The live view must hand the cursor back where the menu left it. ?1047 swaps
// buffers but shares one cursor, so leaving it put the cursor on the live
// view's footer row and the next menu overprinted the restored scrollback.
// ?1049 saves the cursor on enter and restores it on leave.
func TestAltScreenRestoresCursor(t *testing.T) {
	if ansiAltEnter != "\x1b[?1049h" || ansiAltLeave != "\x1b[?1049l" {
		t.Fatalf("alt screen must use ?1049 (save/restore cursor), got enter=%q leave=%q",
			ansiAltEnter, ansiAltLeave)
	}
}
