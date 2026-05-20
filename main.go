package main

import (
	"fmt"
	"os"

	"github.com/rian/antitimely/internal/cli"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: antitimely <command> [...]")
		os.Exit(64)
	}
	if os.Args[1] == "daemon" {
		fmt.Fprintln(os.Stderr, "daemon: not implemented yet")
		os.Exit(1)
	}
	os.Exit(cli.Dispatch(os.Args[1:]))
}
