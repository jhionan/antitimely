package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/rian/antitimely/internal/daemon"
)

func cmdConfig(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: antitimely config <init|show|path>")
		return 64
	}
	switch args[0] {
	case "init":
		return configInit()
	case "show":
		return configShow()
	case "path":
		return configPath()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: config %s\n", args[0])
		return 64
	}
}

func configPath() int {
	p, err := daemon.DefaultConfigFilePath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(p)
	return 0
}

func configInit() int {
	p, err := daemon.DefaultConfigFilePath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if _, err := os.Stat(p); err == nil {
		fmt.Fprintf(os.Stderr, "%s already exists; refusing to overwrite\n", p)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(p, []byte(daemon.DefaultConfigYAML()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Wrote default config to %s\n", p)
	fmt.Println("Edit it and restart the daemon (uninstall+install launch agent, or kill -TERM + restart).")
	return 0
}

func configShow() int {
	p, err := daemon.DefaultConfigFilePath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "no config file at %s (run `antitimely config init` to create one)\n", p)
			return 1
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Print(string(data))
	return 0
}
