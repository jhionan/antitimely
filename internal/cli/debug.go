package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/rian/antitimely/internal/macos"
)

func cmdDebug(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: antitimely debug <windows|frontmost|idle>")
		return 64
	}
	ctx := context.Background()
	switch args[0] {
	case "windows":
		out, err := macos.FocusedWindowDebugReal(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Print(out)
		return 0
	case "frontmost":
		fi, err := macos.FrontmostReal(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("bundle=%q name=%q pid=%d\n", fi.BundleID, fi.Name, fi.PID)
		return 0
	case "idle":
		s, err := macos.IdleSecondsReal(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("idle_seconds=%d\n", s)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: debug %s\n", args[0])
		return 64
	}
}
