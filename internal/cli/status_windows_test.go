package cli

import (
	"testing"
	"unsafe"
)

func TestWindowsStatusEscape(t *testing.T) {
	if got := unsafe.Sizeof(consoleInputRecord{}); got != 20 {
		t.Fatalf("INPUT_RECORD size = %d", got)
	}
	tests := []struct {
		event  consoleInputRecord
		escape bool
	}{
		{consoleInputRecord{eventType: 1, keyDown: 1, key: 0x1b}, true},
		{consoleInputRecord{eventType: 1, keyDown: 0, key: 0x1b}, false},
		{consoleInputRecord{eventType: 1, keyDown: 1, key: 0x25}, false},
		{consoleInputRecord{eventType: 2, keyDown: 1, key: 0x1b}, false},
	}
	for _, test := range tests {
		if got := test.event.isEscape(); got != test.escape {
			t.Errorf("%+v = %v, want %v", test.event, got, test.escape)
		}
	}
}
