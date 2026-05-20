package cli

import (
	"fmt"
	"os"
)

func cmdStatus(args []string) int {
	fmt.Fprintln(os.Stderr, "status: not implemented yet")
	return 1
}
