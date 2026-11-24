package main

import (
	"fmt"
	"os"
	"time"

	"github.com/rian/antitimely/internal/osbridge"
)

func platformCommand(args []string) (bool, int) {
	if args[0] != "supervise" {
		return false, 0
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return true, 1
	}
	if err := osbridge.Supervise(exe, append([]string{"daemon"}, args[1:]...), time.Second); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return true, 1
	}
	return true, 0
}
