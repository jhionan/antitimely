package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const launchAgentLabel = "com.rian.antitimely"

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist"), nil
}

func plistContent(binaryPath string) string {
	home, _ := os.UserHomeDir()
	logOut := filepath.Join(home, ".antitimely", "daemon.log")
	logErr := filepath.Join(home, ".antitimely", "daemon.err")
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTD/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>daemon</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`, launchAgentLabel, binaryPath, logOut, logErr)
}

func cmdInstallLaunchAgent(args []string) int {
	bin, err := resolveBinaryPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	plistPath, err := launchAgentPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Ensure log directory exists
	home, _ := os.UserHomeDir()
	if err := os.MkdirAll(filepath.Join(home, ".antitimely"), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(plistPath, []byte(plistContent(bin)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// Try to bootstrap. If it says "service already loaded", boot it out then retry.
	if err := runLaunchctl("bootstrap", guiDomain(), plistPath); err != nil {
		if strings.Contains(err.Error(), "already loaded") || strings.Contains(err.Error(), "Service already loaded") || strings.Contains(err.Error(), "Bootstrap failed: 37") {
			// Service exists — boot it out and try again.
			_ = runLaunchctl("bootout", guiDomain()+"/"+launchAgentLabel)
			if err := runLaunchctl("bootstrap", guiDomain(), plistPath); err != nil {
				fmt.Fprintln(os.Stderr, "bootstrap failed after bootout:", err)
				return 1
			}
		} else {
			fmt.Fprintln(os.Stderr, "bootstrap:", err)
			return 1
		}
	}

	fmt.Printf("Installed launch agent → %s\n", plistPath)
	fmt.Printf("  binary: %s\n", bin)
	fmt.Printf("  logs:   ~/.antitimely/daemon.{log,err}\n")
	fmt.Println()
	fmt.Println("The daemon should now be running and will restart at login.")
	fmt.Printf("Check status: launchctl print %s/%s\n", guiDomain(), launchAgentLabel)
	return 0
}

func cmdUninstallLaunchAgent(args []string) int {
	plistPath, err := launchAgentPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Try bootout regardless of plist file presence — service may be loaded.
	if err := runLaunchctl("bootout", guiDomain()+"/"+launchAgentLabel); err != nil {
		// "Could not find service" is fine (already unloaded).
		if !strings.Contains(err.Error(), "Could not find service") &&
			!strings.Contains(err.Error(), "No such process") {
			fmt.Fprintln(os.Stderr, "bootout (non-fatal):", err)
		}
	}
	if err := os.Remove(plistPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "remove plist:", err)
		return 1
	}
	fmt.Println("Launch agent uninstalled.")
	return 0
}

func cmdRestartDaemon(args []string) int {
	// kickstart -k kills the currently-loaded instance and immediately
	// relaunches it. This only works when the daemon is managed by the launch
	// agent (the normal install). If it isn't loaded, launchctl reports
	// "Could not find service" — point the user at install rather than dumping
	// a raw launchctl error.
	target := guiDomain() + "/" + launchAgentLabel
	if err := runLaunchctl("kickstart", "-k", target); err != nil {
		if strings.Contains(err.Error(), "Could not find service") ||
			strings.Contains(err.Error(), "No such process") {
			fmt.Fprintln(os.Stderr, "daemon is not running under launchd —")
			fmt.Fprintln(os.Stderr, "  run 'antitimely install-launch-agent' first, or restart your manual 'antitimely daemon' by hand.")
			return 1
		}
		fmt.Fprintln(os.Stderr, "restart:", err)
		return 1
	}
	// kickstart returns once launchd accepts the request; the new process then
	// takes a few seconds to spawn and rebind the socket. Say so, otherwise an
	// immediate 'status' looks like a failure.
	fmt.Println("Daemon restarting… (give it a few seconds before 'status')")
	return 0
}

func resolveBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Follow symlinks so the plist points to the real path.
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(real)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func guiDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func runLaunchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
