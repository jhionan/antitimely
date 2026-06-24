package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// FileConfig is the YAML config file shape. All fields are optional; empty/zero
// means "use whatever was already in Config".
type FileConfig struct {
	Interval              string `yaml:"interval,omitempty"`
	IdleThreshold         string `yaml:"idle_threshold,omitempty"`
	AgentCPUThreshold     uint64 `yaml:"agent_cpu_threshold,omitempty"`
	AgentCPUThresholdIdle uint64 `yaml:"agent_cpu_threshold_idle,omitempty"`
	AgentBusyRiseTicks    int    `yaml:"agent_busy_rise_ticks,omitempty"`
	AgentBusyFallTicks    int    `yaml:"agent_busy_fall_ticks,omitempty"`
	SocketPath            string `yaml:"socket_path,omitempty"`
	DBPath                string `yaml:"db_path,omitempty"`
	PIDPath               string `yaml:"pid_path,omitempty"`
	TranscriptTracking    *bool  `yaml:"transcript_tracking,omitempty"`
	TranscriptGrace       string `yaml:"transcript_grace,omitempty"`
	TranscriptRoot        string `yaml:"transcript_root,omitempty"`
}

// DefaultConfigFilePath returns ~/.antitimely/config.yaml.
func DefaultConfigFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".antitimely", "config.yaml"), nil
}

// LoadFileConfig reads the YAML config at path. Returns a zero-valued
// FileConfig (with nil error) if the file doesn't exist. Returns a non-nil
// error only for parse failures or I/O errors other than NotExist.
func LoadFileConfig(path string) (FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return FileConfig{}, nil
		}
		return FileConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	var fc FileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return FileConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return fc, nil
}

// ApplyTo merges non-empty values from fc into cfg in place. Returns an error
// if a duration field can't be parsed or a ~-prefixed path can't be expanded.
func (fc FileConfig) ApplyTo(cfg *Config) error {
	if fc.Interval != "" {
		d, err := time.ParseDuration(fc.Interval)
		if err != nil {
			return fmt.Errorf("interval: %w", err)
		}
		cfg.IntervalSeconds = int(d.Seconds())
		if cfg.IntervalSeconds < 1 {
			cfg.IntervalSeconds = 1
		}
	}
	if fc.IdleThreshold != "" {
		d, err := time.ParseDuration(fc.IdleThreshold)
		if err != nil {
			return fmt.Errorf("idle_threshold: %w", err)
		}
		cfg.IdleThresholdSec = int(d.Seconds())
		if cfg.IdleThresholdSec < 1 {
			cfg.IdleThresholdSec = 1
		}
	}
	if fc.AgentCPUThreshold != 0 {
		cfg.AgentCPUThresh = fc.AgentCPUThreshold
	}
	if fc.AgentCPUThresholdIdle != 0 {
		cfg.AgentCPUThreshIdle = fc.AgentCPUThresholdIdle
	}
	if fc.AgentBusyRiseTicks != 0 {
		cfg.AgentBusyRiseTicks = fc.AgentBusyRiseTicks
	}
	if fc.AgentBusyFallTicks != 0 {
		cfg.AgentBusyFallTicks = fc.AgentBusyFallTicks
	}
	if fc.SocketPath != "" {
		p, err := expandHome(fc.SocketPath)
		if err != nil {
			return fmt.Errorf("socket_path: %w", err)
		}
		cfg.SocketPath = p
	}
	if fc.DBPath != "" {
		p, err := expandHome(fc.DBPath)
		if err != nil {
			return fmt.Errorf("db_path: %w", err)
		}
		cfg.DBPath = p
	}
	if fc.PIDPath != "" {
		p, err := expandHome(fc.PIDPath)
		if err != nil {
			return fmt.Errorf("pid_path: %w", err)
		}
		cfg.PIDPath = p
	}
	if fc.TranscriptTracking != nil {
		cfg.TranscriptTracking = *fc.TranscriptTracking
	}
	if fc.TranscriptGrace != "" {
		if d, err := time.ParseDuration(fc.TranscriptGrace); err == nil && d > 0 {
			cfg.TranscriptGraceSec = int(d.Seconds())
		}
	}
	if fc.TranscriptRoot != "" {
		if p, err := expandHome(fc.TranscriptRoot); err == nil {
			cfg.TranscriptRoot = p
		}
	}
	return nil
}

// defaultTranscriptRoot is $CLAUDE_CONFIG_DIR/projects when set, else
// ~/.claude/projects.
func defaultTranscriptRoot() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// expandHome resolves a leading "~" to the current user's home directory.
// Returns an error if UserHomeDir fails — silently leaving the literal "~"
// in the path would cause the daemon to create state directories named "~"
// relative to its CWD, which is hard to debug.
func expandHome(p string) (string, error) {
	if len(p) > 0 && p[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %q: %w", p, err)
		}
		return filepath.Join(home, p[1:]), nil
	}
	return p, nil
}

// DefaultConfigYAML returns a commented example config matching the current
// in-binary defaults. Used by `config init`.
func DefaultConfigYAML() string {
	return `# Antitimely daemon configuration.
# Any line can be commented out to use the default.

# Polling interval. Lower = more accurate but more CPU. Go duration syntax.
interval: 5s

# How long the user must be inactive (no keyboard/mouse) before focus signals
# stop being counted. Agent signals (background processes burning CPU) keep
# counting regardless of user idle.
idle_threshold: 2m

# CPU "busy bar" for agent processes when the user is ACTIVE (centiseconds of
# CPU per poll). A process must stay at/above this for agent_busy_rise_ticks
# polls before its time counts. ~15 ≈ 3% of one core; low enough that an LLM
# agent thinking/streaming counts, high enough to ignore an idle prompt (~2cs).
agent_cpu_threshold: 15

# CPU busy bar when the user has been IDLE past idle_threshold. Higher so that
# only genuine heavy compute counts once you've stepped away — idle dev servers
# and idle agents fall below it and stop billing.
agent_cpu_threshold_idle: 100

# Consecutive above-bar polls before a process is counted as working (~rise*interval seconds).
agent_busy_rise_ticks: 2

# Consecutive below-bar polls before a working process stops counting (hysteresis).
agent_busy_fall_ticks: 3

# Override paths (defaults shown). Useful if you want state in a non-default
# location. Leave commented to use the defaults under ~/.antitimely/.
# socket_path: ~/.antitimely/antitimely.sock
# db_path:     ~/.antitimely/db.sqlite
# pid_path:    ~/.antitimely/antitimely.pid
`
}
