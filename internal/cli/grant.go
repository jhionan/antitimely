package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// paneAccessibility deep-links System Settings to Privacy & Security →
// Accessibility. macOS cannot grant the permission programmatically (it
// requires user consent in Settings), so this just removes the friction of
// finding the pane.
const paneAccessibility = "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"

// cmdGrantAccessibility prints what antitimely needs for window-title capture,
// the binary path to add, and opens the Accessibility settings pane.
func cmdGrantAccessibility(args []string) int {
	exe := "(the antitimely binary)"
	if p, err := os.Executable(); err == nil {
		exe = p
		if real, rerr := filepath.EvalSymlinks(p); rerr == nil {
			exe = real // TCC keys on the real binary, not the `atl` symlink
		}
	}

	fmt.Println("antitimely needs Accessibility to read the focused window's title.")
	fmt.Println()
	fmt.Println("In the Accessibility list that opens:")
	fmt.Println("  • if 'antitimely' is already listed, toggle it off then on;")
	fmt.Println("  • otherwise click +, press ⌘⇧G, and paste this path:")
	fmt.Printf("        %s\n", exe)
	fmt.Println()
	fmt.Println("Electron/JVM apps (VS Code, Antigravity, JetBrains) also need")
	fmt.Println("Automation → System Events (usually auto-prompted on first run).")
	fmt.Println()

	if err := exec.Command("open", paneAccessibility).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "could not open System Settings:", err)
		fmt.Println("Open manually: System Settings → Privacy & Security → Accessibility")
		return 1
	}
	fmt.Println("Opened System Settings → Accessibility. After enabling, run:  atl restart")
	return 0
}
