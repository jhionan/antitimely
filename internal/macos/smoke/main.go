package main

import (
	"fmt"
	"os"

	"github.com/rian/antitimely/internal/macos"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: smoke <ps|lsof|ioreg|frontmost|title>")
		os.Exit(64)
	}
	switch os.Args[1] {
	case "ps":
		procs, err := macos.ListProcessesReal()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("got %d processes; first 5:\n", len(procs))
		for i, p := range procs {
			if i >= 5 {
				break
			}
			fmt.Printf("  pid=%d name=%q cpu_ticks=%d\n", p.PID, p.Name, p.CPUTicks)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown: %s\n", os.Args[1])
		os.Exit(64)
	}
}
