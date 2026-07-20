package macos

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A hung osascript must surface as ErrOsascriptTimeout, not as an opaque
// "signal: killed" error. The daemon relies on this classification to back off
// instead of respawning a multi-GB osascript every tick — the accumulation
// observed in production (2707 kills in five days) happened because timeouts
// were indistinguishable from ordinary failures.
func TestRunOsascript_TimeoutIsClassified(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns osascript")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := runOsascript(ctx, `delay 10`)
	if err == nil {
		t.Fatal("expected an error from a script that outlives the deadline")
	}
	if !errors.Is(err, ErrOsascriptTimeout) {
		t.Errorf("expected ErrOsascriptTimeout, got %v", err)
	}
}

// A denial must still classify as ErrAccessibilityDenied, not as a timeout —
// the two drive different backoff behaviour.
func TestRunOsascript_DenialNotClassifiedAsTimeout(t *testing.T) {
	err := classifyOsascriptErr("System Events got an error: (-1743)")
	if !errors.Is(err, ErrAccessibilityDenied) {
		t.Fatalf("expected denial, got %v", err)
	}
	if errors.Is(err, ErrOsascriptTimeout) {
		t.Error("a denial must not be classified as a timeout")
	}
}
