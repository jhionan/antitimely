package cli

import (
	"fmt"
	"os"
)

func cmdReview(args []string) int {
	fmt.Fprintln(os.Stderr, "review: not implemented yet")
	return 1
}
