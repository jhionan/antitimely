package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFileConfig_Missing(t *testing.T) {
	fc, err := LoadFileConfig(filepath.Join(t.TempDir(), "no-such.yaml"))
	if err != nil {
		t.Fatalf("missing should be silent: %v", err)
	}
	if (fc != FileConfig{}) {
		t.Errorf("expected zero-value, got %+v", fc)
	}
}

func TestLoadFileConfig_Parse(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("interval: 10s\nidle_threshold: 30m\nagent_cpu_threshold: 42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fc, err := LoadFileConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if fc.Interval != "10s" || fc.IdleThreshold != "30m" || fc.AgentCPUThreshold != 42 {
		t.Errorf("got %+v", fc)
	}
}

func TestFileConfig_ApplyTo(t *testing.T) {
	cfg := Config{
		IntervalSeconds:  5,
		IdleThresholdSec: 120,
		AgentCPUThresh:   5,
	}
	fc := FileConfig{
		Interval:          "10s",
		IdleThreshold:     "30m",
		AgentCPUThreshold: 42,
	}
	if err := fc.ApplyTo(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.IntervalSeconds != 10 || cfg.IdleThresholdSec != 1800 || cfg.AgentCPUThresh != 42 {
		t.Errorf("got %+v", cfg)
	}
}

func TestFileConfig_ApplyTo_BadDuration(t *testing.T) {
	cfg := Config{}
	fc := FileConfig{Interval: "not-a-duration"}
	if err := fc.ApplyTo(&cfg); err == nil {
		t.Fatal("expected error")
	}
}

func TestFileConfig_ApplyTo_AgentBusyTicks(t *testing.T) {
	cfg := Config{}
	fc := FileConfig{AgentBusyRiseTicks: 4, AgentBusyFallTicks: 6}
	if err := fc.ApplyTo(&cfg); err != nil {
		t.Fatalf("ApplyTo: %v", err)
	}
	if cfg.AgentBusyRiseTicks != 4 {
		t.Errorf("AgentBusyRiseTicks = %d, want 4", cfg.AgentBusyRiseTicks)
	}
	if cfg.AgentBusyFallTicks != 6 {
		t.Errorf("AgentBusyFallTicks = %d, want 6", cfg.AgentBusyFallTicks)
	}
}

// defaultFileConfigForTest builds a Config with defaults applied and (if yaml
// is non-empty) overrides from that YAML applied via LoadFileConfig + ApplyTo.
func defaultFileConfigForTest(t *testing.T, yaml string) Config {
	t.Helper()
	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	if yaml == "" {
		return cfg
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	fc, err := LoadFileConfig(p)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if err := fc.ApplyTo(&cfg); err != nil {
		t.Fatalf("ApplyTo: %v", err)
	}
	return cfg
}

func TestParseConfig_TranscriptDefaultsAndOverrides(t *testing.T) {
	// Defaults: tracking on, 600s grace, non-empty root.
	def := defaultFileConfigForTest(t, "")
	if !def.TranscriptTracking {
		t.Fatal("transcript tracking should default on")
	}
	if def.TranscriptGraceSec != 600 {
		t.Fatalf("grace default = %d, want 600", def.TranscriptGraceSec)
	}
	if def.TranscriptRoot == "" {
		t.Fatal("transcript root should default to a path")
	}

	// Overrides.
	yaml := "transcript_tracking: false\ntranscript_grace: 5m\ntranscript_root: /tmp/x\n"
	cfg := defaultFileConfigForTest(t, yaml)
	if cfg.TranscriptTracking {
		t.Fatal("override should disable tracking")
	}
	if cfg.TranscriptGraceSec != 300 {
		t.Fatalf("grace = %d, want 300", cfg.TranscriptGraceSec)
	}
	if cfg.TranscriptRoot != "/tmp/x" {
		t.Fatalf("root = %q, want /tmp/x", cfg.TranscriptRoot)
	}
}
