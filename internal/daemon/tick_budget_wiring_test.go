package daemon

import (
	"testing"
	"time"
)

// TestPipelineConfigBudgetsATickAtThePollInterval pins the line that makes the
// slow-tick report live in production. PipelineConfig.TickBudget defaults to
// zero (silent), so a refactor that dropped this wiring would turn the report
// off with every other test still green - and the next stall would again leave
// nothing in daemon.err.
func TestPipelineConfigBudgetsATickAtThePollInterval(t *testing.T) {
	pc := pipelineConfigFor(Config{IntervalSeconds: 5})
	if pc.TickBudget != 5*time.Second {
		t.Errorf("TickBudget = %s, want the 5s poll interval", pc.TickBudget)
	}
}

// A tick has no meaningful budget without a poll interval, and a zero budget
// is the documented "off" value - so an unset interval must stay silent rather
// than report every tick as slow.
func TestPipelineConfigLeavesTickBudgetOffWithoutAnInterval(t *testing.T) {
	for _, interval := range []int{0, -1} {
		if pc := pipelineConfigFor(Config{IntervalSeconds: interval}); pc.TickBudget != 0 {
			t.Errorf("IntervalSeconds=%d: TickBudget = %s, want 0 (off)", interval, pc.TickBudget)
		}
	}
}

// The extraction must carry the whole config across, not just the new field.
func TestPipelineConfigCarriesTheTrackingSettings(t *testing.T) {
	pc := pipelineConfigFor(Config{
		IntervalSeconds:    5,
		IdleThresholdSec:   120,
		AgentCPUThresh:     15,
		AgentCPUThreshIdle: 100,
		AgentBusyRiseTicks: 2,
		AgentBusyFallTicks: 3,
		TranscriptTracking: true,
		TranscriptGraceSec: 600,
		TranscriptRoot:     "/tmp/projects",
	})
	switch {
	case pc.IdleThresholdSec != 120:
		t.Errorf("IdleThresholdSec = %d, want 120", pc.IdleThresholdSec)
	case pc.CPUDeltaThresh != 15:
		t.Errorf("CPUDeltaThresh = %d, want 15", pc.CPUDeltaThresh)
	case pc.CPUDeltaThreshIdle != 100:
		t.Errorf("CPUDeltaThreshIdle = %d, want 100", pc.CPUDeltaThreshIdle)
	case pc.AgentBusyRiseTicks != 2:
		t.Errorf("AgentBusyRiseTicks = %d, want 2", pc.AgentBusyRiseTicks)
	case pc.AgentBusyFallTicks != 3:
		t.Errorf("AgentBusyFallTicks = %d, want 3", pc.AgentBusyFallTicks)
	case !pc.TranscriptTracking:
		t.Error("TranscriptTracking = false, want true")
	case pc.TranscriptGraceSec != 600:
		t.Errorf("TranscriptGraceSec = %d, want 600", pc.TranscriptGraceSec)
	case pc.TranscriptRoot != "/tmp/projects":
		t.Errorf("TranscriptRoot = %q, want /tmp/projects", pc.TranscriptRoot)
	}
}

// The auto-disarm threshold is derived from the interval (~60s of sustained
// agent activity), so the extraction has to keep deriving it.
func TestPipelineConfigDerivesAutoDisarmFromTheInterval(t *testing.T) {
	if pc := pipelineConfigFor(Config{IntervalSeconds: 5}); pc.AutoDisarmAgentTicks != 12 {
		t.Errorf("AutoDisarmAgentTicks = %d, want 12 (60s / 5s)", pc.AutoDisarmAgentTicks)
	}
	if pc := pipelineConfigFor(Config{IntervalSeconds: 0}); pc.AutoDisarmAgentTicks != 12 {
		t.Errorf("AutoDisarmAgentTicks = %d, want the 12-tick fallback", pc.AutoDisarmAgentTicks)
	}
}
