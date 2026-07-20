package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/rian/antitimely/internal/macos"
)

// When window-title capture is denied, the daemon must stop spawning the title
// osascript every tick (the process-leak vector) and only re-probe periodically.
func TestPipeline_TitleDenied_BacksOffOsascript(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	cache.Store(&CacheSnapshot{
		AllowedBundles: map[string]bool{"com.example.app": true},
	})
	br.IdleSecondsVal = 5 // user present → focus signal fires
	br.FrontmostInfoVal = macos.FrontmostInfo{BundleID: "com.example.app", PID: 1}
	br.FocusedTitleErr = macos.ErrAccessibilityDenied

	// Tick at 5s intervals for ~50s (10 ticks) — all within one backoff window.
	for ts := int64(1000); ts < 1050; ts += 5 {
		if err := p.RunTick(ctx, ts); err != nil {
			t.Fatalf("RunTick @%d: %v", ts, err)
		}
	}
	if br.FocusedTitleCalls != 1 {
		t.Fatalf("expected title osascript called once then backed off, got %d calls", br.FocusedTitleCalls)
	}

	// After the backoff window (>= 60s since the denial at ts=1000), it re-probes.
	if err := p.RunTick(ctx, 1065); err != nil {
		t.Fatalf("RunTick @1065: %v", err)
	}
	if br.FocusedTitleCalls != 2 {
		t.Errorf("expected a single re-probe after backoff, got %d total calls", br.FocusedTitleCalls)
	}
}

// Repeated title timeouts must also trigger the backoff. This is the case that
// actually bit in production: a wedged System Events made osascript hang until
// the deadline killed it, and because timeouts (unlike denials) did not back
// off, the daemon respawned a multi-GB osascript every 5s indefinitely.
func TestPipeline_TitleTimeouts_BackOffAfterStreak(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	cache.Store(&CacheSnapshot{
		AllowedBundles: map[string]bool{"com.example.app": true},
	})
	br.IdleSecondsVal = 5
	br.FrontmostInfoVal = macos.FrontmostInfo{BundleID: "com.example.app", PID: 1}
	br.FocusedTitleErr = macos.ErrOsascriptTimeout

	// Ten ticks of nothing but timeouts. Only the ticks up to the streak
	// threshold may spawn osascript; after that the backoff must hold.
	for ts := int64(1000); ts < 1050; ts += 5 {
		if err := p.RunTick(ctx, ts); err != nil {
			t.Fatalf("RunTick @%d: %v", ts, err)
		}
	}
	if br.FocusedTitleCalls != osascriptTimeoutStreak {
		t.Fatalf("expected osascript to stop after %d consecutive timeouts, got %d calls",
			osascriptTimeoutStreak, br.FocusedTitleCalls)
	}
}

// Frontmost must never be suppressed by the title backoff. It no longer goes
// through System Events (NSWorkspace instead), so it cannot hang or balloon —
// and keeping it live means bundle-only rules still match while titles are
// unavailable.
func TestPipeline_FrontmostNotSuppressedByTitleBackoff(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	cache.Store(&CacheSnapshot{
		AllowedBundles: map[string]bool{"com.example.app": true},
	})
	br.IdleSecondsVal = 5
	br.FrontmostInfoVal = macos.FrontmostInfo{BundleID: "com.example.app", PID: 1}
	br.FocusedTitleErr = macos.ErrAccessibilityDenied

	const ticks = 10
	for i := 0; i < ticks; i++ {
		if err := p.RunTick(ctx, 1000+int64(i)*5); err != nil {
			t.Fatalf("RunTick: %v", err)
		}
	}
	if br.FrontmostCalls != ticks {
		t.Errorf("expected frontmost on every tick despite title backoff, got %d/%d",
			br.FrontmostCalls, ticks)
	}
}

// A successful title call must NOT back off — every tick should still read the
// title so a focus change is picked up promptly.
func TestPipeline_TitleOK_NoBackoff(t *testing.T) {
	p, br, cache, db := newTestPipeline(t)
	defer db.Close()
	ctx := context.Background()

	cache.Store(&CacheSnapshot{
		AllowedBundles: map[string]bool{"com.example.app": true},
	})
	br.IdleSecondsVal = 5
	br.FrontmostInfoVal = macos.FrontmostInfo{BundleID: "com.example.app", PID: 1}
	br.FocusedTitle = "some window"
	br.FocusedTitleErr = nil

	for ts := int64(1000); ts < 1020; ts += 5 { // 4 ticks
		if err := p.RunTick(ctx, ts); err != nil {
			t.Fatalf("RunTick @%d: %v", ts, err)
		}
	}
	if br.FocusedTitleCalls != 4 {
		t.Errorf("expected a title read every tick when OK, got %d", br.FocusedTitleCalls)
	}
	if errors.Is(nil, macos.ErrAccessibilityDenied) { // keep import used if title path changes
		t.Fatal("unreachable")
	}
}
