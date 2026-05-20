package cli

import (
	"fmt"
	"os"
)

func cmdReport(args []string) int {
	fmt.Fprintln(os.Stderr, "report: not implemented yet")
	return 1
}
