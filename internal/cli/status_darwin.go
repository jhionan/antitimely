package cli

import (
	"os"
	"syscall"
)

var statusSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGTSTP}

func enterStatusTerminal() (*termState, error) {
	return enterCbreak()
}
