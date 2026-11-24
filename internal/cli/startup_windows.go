package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows"
)

func cmdInstallLaunchAgent(_ []string) int {
	fmt.Fprintln(os.Stderr, "Install Windows startup with scripts/windows-startup.ps1 -Action Install.")
	return 64
}

func cmdUninstallLaunchAgent(_ []string) int {
	fmt.Fprintln(os.Stderr, "Remove Windows startup with scripts/windows-startup.ps1 -Action Uninstall.")
	return 64
}

func cmdGrantAccessibility(_ []string) int {
	fmt.Fprintln(os.Stderr, "Windows window capture uses the signed-in desktop session.")
	return 64
}

func cmdRestartDaemon(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: antitimely restart")
		return 64
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	task := "Antitimely-" + user.User.Sid.String()
	if _, err := exec.Command("schtasks.exe", "/Query", "/TN", task).CombinedOutput(); err != nil {
		fmt.Fprintln(os.Stderr, "Could not find the Antitimely startup task. Use scripts/windows-startup.ps1 -Action Install or restart the foreground daemon.")
		return 1
	}
	if out, err := exec.Command("schtasks.exe", "/End", "/TN", task).CombinedOutput(); err != nil {
		fmt.Fprintln(os.Stderr, "Stop scheduled daemon:", strings.TrimSpace(string(out)), err)
		return 1
	}
	if out, err := exec.Command("schtasks.exe", "/Run", "/TN", task).CombinedOutput(); err != nil {
		fmt.Fprintln(os.Stderr, "Start scheduled daemon:", strings.TrimSpace(string(out)), err)
		return 1
	}
	fmt.Println("Daemon restart requested.")
	return 0
}

func printStopDaemonHint(w io.Writer, _ string) {
	fmt.Fprintln(w, "  scripts/windows-startup.ps1 -Action Stop  (scheduled daemon)")
	fmt.Fprintln(w, "  Ctrl+C in the daemon's terminal          (foreground daemon)")
}

const startupUsage = `  antitimely restart                  restart the Windows startup task
  Windows login startup is managed with scripts/windows-startup.ps1.`
