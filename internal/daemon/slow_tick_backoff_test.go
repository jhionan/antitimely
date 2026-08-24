package daemon

import (
	"strings"
	"testing"
	"time"
)

// newSlowTickReporter builds a bare pipeline that treats anything over 5s as a
// slow tick. Only reportSlowTick is exercised, so no bridge or DB is needed.
func newSlowTickReporter() *Pipeline {
	return &Pipeline{cfg: PipelineConfig{TickBudget: 5 * time.Second}}
}

func countLines(s, substr string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

// daemon.err is append-only and already 11.7MB. A pathology that makes every
// tick overrun - a wedged osascript, a contended DB - would otherwise add 12
// lines a minute forever, so the report backs off the same way the window-title
// probe does.
func TestSlowTickReportBacksOffWhileTicksKeepOverrunning(t *testing.T) {
	p := newSlowTickReporter()
	buf := captureLog(t)

	now := int64(1000)
	for i := int64(0); i < 10; i++ {
		p.reportSlowTick(tickPhases{}, 9*time.Second, now+i*5)
	}

	if got := countLines(buf.String(), "slow tick"); got != 1 {
		t.Errorf("10 consecutive overrunning ticks logged %d lines, want 1 (the rest inside the backoff window)", got)
	}
}

func TestSlowTickReportLogsAgainOnceTheBackoffWindowPasses(t *testing.T) {
	p := newSlowTickReporter()
	buf := captureLog(t)

	now := int64(1000)
	p.reportSlowTick(tickPhases{}, 9*time.Second, now)                             // logs, window = 60s
	p.reportSlowTick(tickPhases{}, 9*time.Second, now+slowTickBackoffStartSec-1)   // still inside
	p.reportSlowTick(tickPhases{}, 9*time.Second, now+slowTickBackoffStartSec)     // logs, window = 120s
	p.reportSlowTick(tickPhases{}, 9*time.Second, now+slowTickBackoffStartSec+119) // still inside

	if got := countLines(buf.String(), "slow tick"); got != 2 {
		t.Errorf("logged %d lines, want 2 (one per elapsed backoff window)\n%s", got, buf.String())
	}
}

// The backoff describes an ongoing problem. Once a tick comes in under budget
// the problem is over, so the next one has to be reported immediately rather
// than swallowed by a window opened minutes ago.
func TestSlowTickReportResetsAfterAHealthyTick(t *testing.T) {
	p := newSlowTickReporter()
	buf := captureLog(t)

	now := int64(1000)
	p.reportSlowTick(tickPhases{}, 9*time.Second, now)    // logs
	p.reportSlowTick(tickPhases{}, time.Second, now+5)    // healthy: clears the backoff
	p.reportSlowTick(tickPhases{}, 9*time.Second, now+10) // logs again despite the open window

	if got := countLines(buf.String(), "slow tick"); got != 2 {
		t.Errorf("logged %d lines, want 2 (a healthy tick must reset the backoff)\n%s", got, buf.String())
	}
}

func TestSlowTickBackoffGrowthIsCapped(t *testing.T) {
	p := newSlowTickReporter()
	captureLog(t)

	now := int64(1000)
	for i := 0; i < 20; i++ {
		now += p.slowTickBackoffSec // jump straight to the next allowed line
		p.reportSlowTick(tickPhases{}, 9*time.Second, now)
	}

	if p.slowTickBackoffSec != slowTickBackoffMaxSec {
		t.Errorf("backoff grew to %ds, want the %ds cap", p.slowTickBackoffSec, slowTickBackoffMaxSec)
	}
}

// A budget of zero means the report is off, and off must stay silent no matter
// how long a tick takes.
func TestSlowTickReportSilentWithoutBudget(t *testing.T) {
	p := &Pipeline{}
	buf := captureLog(t)
	p.reportSlowTick(tickPhases{}, time.Hour, 1000)
	if strings.Contains(buf.String(), "slow tick") {
		t.Errorf("logged with no budget configured:\n%s", buf.String())
	}
}
