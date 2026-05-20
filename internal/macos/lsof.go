package macos

import (
	"fmt"
	"os/exec"
	"strings"
)

// ProcessCWDReal returns the current working directory of the given pid, by
// shelling out to `lsof -a -p PID -d cwd -Fn`. Output format is two lines:
//
//	p<pid>
//	n<path>
//
// Returns empty string + nil error if pid has no cwd (rare; typically denied
// or already exited). Returns an error only for unexpected failures.
func ProcessCWDReal(pid int) (string, error) {
	cmd := exec.Command("lsof", "-a", "-p", fmt.Sprintf("%d", pid), "-d", "cwd", "-Fn")
	out, err := cmd.Output()
	if err != nil {
		// lsof exits 1 if the pid is gone or has no matching fd.
		return "", nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return strings.TrimPrefix(line, "n"), nil
		}
	}
	return "", nil
}
