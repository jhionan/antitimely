package cli

import (
	"fmt"
	"net"
	"net/rpc"
	"os"
	"path/filepath"
)

// DefaultSocketPath returns the default unix socket path inside ~/.antitimely/.
func DefaultSocketPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".antitimely", "antitimely.sock"), nil
}

// Dial connects to the daemon's net/rpc server over a unix socket.
func Dial(socketPath string) (*rpc.Client, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w (is `antitimely daemon` running?)", socketPath, err)
	}
	return rpc.NewClient(conn), nil
}
