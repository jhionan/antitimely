package cli

import (
	"fmt"
	"net/rpc"

	"github.com/rian/antitimely/internal/daemon"
	"github.com/rian/antitimely/internal/localrpc"
)

// DefaultSocketPath returns the daemon's configured local RPC endpoint.
func DefaultSocketPath() (string, error) {
	cfg, err := configuredDaemon()
	return cfg.SocketPath, err
}

func configuredDaemon() (daemon.Config, error) {
	cfg, err := daemon.DefaultConfig()
	if err != nil {
		return cfg, err
	}
	path, err := daemon.DefaultConfigFilePath()
	if err != nil {
		return cfg, err
	}
	fc, err := daemon.LoadFileConfig(path)
	if err == nil {
		// Match daemon startup, which retains applied settings if a later
		// field is invalid.
		_ = fc.ApplyTo(&cfg)
	}
	return cfg, nil
}

// Dial connects to the daemon's local net/rpc server.
func Dial(socketPath string) (*rpc.Client, error) {
	conn, err := localrpc.Dial(socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w (is `antitimely daemon` running?)", socketPath, err)
	}
	return rpc.NewClient(conn), nil
}
