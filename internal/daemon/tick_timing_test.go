package daemon

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"
)

// captureLog redirects the standard logger (which is what the daemon writes to
// daemon.err) into a buffer for the duration of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	flags := log.Flags()
	out := log.Writer()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(out)
		log.SetFlags(flags)
	})
	return &buf
}

// A tick that overruns the poll interval is the daemon silently skipping
// billable seconds. It has to say so, and say where the time went, or the only
// way to diagnose a stall is to catch it live with `sample`.
func TestRunTickLogsPhaseBreakdownWhenItOverrunsBudget(t *testing.T) {
	p, br, _, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec: 120,
		CPUDeltaThresh:   5,
		TickBudget:       10 * time.Millisecond,
	})
	defer db.Close()
	br.IdleDelay = 40 * time.Millisecond

	buf := captureLog(t)
	if err := p.RunTick(context.Background(), 1000); err != nil {
		t.Fatalf("RunTick: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "slow tick") {
		t.Fatalf("overrunning tick logged no warning; log was:\n%s", out)
	}
	for _, want := range []string{"idle=", "focus=", "agent=", "transcript=", "write="} {
		if !strings.Contains(out, want) {
			t.Errorf("slow-tick log is missing the %q phase; log was:\n%s", want, out)
		}
	}
}

func TestRunTickStaysQuietWithinBudget(t *testing.T) {
	p, _, _, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec: 120,
		CPUDeltaThresh:   5,
		TickBudget:       5 * time.Second,
	})
	defer db.Close()

	buf := captureLog(t)
	if err := p.RunTick(context.Background(), 1000); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if strings.Contains(buf.String(), "slow tick") {
		t.Errorf("a tick inside its budget must not log; log was:\n%s", buf.String())
	}
}

// A zero budget means "not configured" — every existing caller that builds a
// PipelineConfig without one must keep its silent behaviour.
func TestRunTickStaysQuietWithoutBudget(t *testing.T) {
	p, br, _, db := newTestPipelineWithCfg(t, PipelineConfig{
		IdleThresholdSec: 120,
		CPUDeltaThresh:   5,
	})
	defer db.Close()
	br.IdleDelay = 20 * time.Millisecond

	buf := captureLog(t)
	if err := p.RunTick(context.Background(), 1000); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if strings.Contains(buf.String(), "slow tick") {
		t.Errorf("no budget configured must mean no slow-tick logging; log was:\n%s", buf.String())
	}
}

func TestTickPhasesFormatsEveryPhase(t *testing.T) {
	ph := tickPhases{
		idle:       30 * time.Millisecond,
		focus:      283 * time.Millisecond,
		agent:      34 * time.Millisecond,
		transcript: 2281 * time.Millisecond,
		write:      12 * time.Millisecond,
	}
	got := ph.String()
	for _, want := range []string{"idle=30ms", "focus=283ms", "agent=34ms", "transcript=2.281s", "write=12ms"} {
		if !strings.Contains(got, want) {
			t.Errorf("tickPhases.String() = %q, missing %q", got, want)
		}
	}
}
