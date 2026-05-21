package daemon

import (
	"fmt"
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
	SocketPath            string `yaml:"socket_path,omitempty"`
	DBPath                string `yaml:"db_path,omitempty"`
	PIDPath               string `yaml:"pid_path,omitempty"`
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
		if os.IsNotExist(err) {
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
// if a duration field can't be parsed.
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
	if fc.SocketPath != "" {
		cfg.SocketPath = expandHome(fc.SocketPath)
	}
	if fc.DBPath != "" {
		cfg.DBPath = expandHome(fc.DBPath)
	}
	if fc.PIDPath != "" {
		cfg.PIDPath = expandHome(fc.PIDPath)
	}
	return nil
}

func expandHome(p string) string {
	if len(p) > 0 && p[0] == '~' {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
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

# CPU threshold for agent processes when the user is ACTIVE. Lower = more
# permissive. 5 centi = ~1% of one core averaged over a 5s tick.
agent_cpu_threshold: 5

# CPU threshold for agent processes when the user has been IDLE past
# idle_threshold. Should be much higher than agent_cpu_threshold so that
# background TUI redraws don't get counted overnight. 100 centi = ~20% of
# one core. Real inference / heavy builds easily clear this; idle TUIs don't.
agent_cpu_threshold_idle: 100

# Override paths (defaults shown). Useful if you want state in a non-default
# location. Leave commented to use the defaults under ~/.antitimely/.
# socket_path: ~/.antitimely/antitimely.sock
# db_path:     ~/.antitimely/db.sqlite
# pid_path:    ~/.antitimely/antitimely.pid
`
}
