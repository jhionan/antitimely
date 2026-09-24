package daemon

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/rian/antitimely/internal/macos"
)

// A slow write phase has two causes that call for opposite fixes: the single
// connection held elsewhere (an RPC, the checkpointer) or a statement that is
// itself slow. The report must tell them apart, so holding the connection
// while a tick writes has to show up as conn-wait, not just as write=.
func TestSlowTickReportSeparatesConnectionWait(t *testing.T) {
	p, br, cache, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec: 120,
		TickBudget:       time.Nanosecond, // report every tick
	})
	defer db.Close()
	db.SetMaxOpenConns(1) // as in production (openDB)
	p.SetDBStats(db.Stats)
	buf := captureLog(t)

	cache.Store(&CacheSnapshot{
		AllowedBundles: map[string]bool{"com.google.antigravity": true},
	})
	br.IdleSecondsVal = 5
	br.FrontmostInfoVal = macos.FrontmostInfo{BundleID: "com.google.antigravity", PID: 1234}
	br.FocusedTitle = "foca-api — main — Antigravity"

	ctx := context.Background()
	conn, err := db.Conn(ctx) // the only connection, as a busy RPC would hold it
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	const held = 300 * time.Millisecond
	go func() {
		time.Sleep(held)
		conn.Close()
	}()

	if err := p.RunTick(ctx, 1000); err != nil {
		t.Fatalf("RunTick: %v", err)
	}

	m := regexp.MustCompile(`conn-wait=(\S+) stmts=(\d+) slowest=(\S+)\)`).FindStringSubmatch(buf.String())
	if m == nil {
		t.Fatalf("slow-tick line lacks the write breakdown:\n%s", buf.String())
	}
	wait, err := time.ParseDuration(m[1])
	if err != nil {
		t.Fatalf("parse conn-wait %q: %v", m[1], err)
	}
	if wait < held/2 {
		t.Errorf("conn-wait=%s, want about %s: the held connection was not attributed", wait, held)
	}
	if m[2] != "3" { // upsert observation, ignored check, insert tick
		t.Errorf("stmts=%s, want 3", m[2])
	}
}
