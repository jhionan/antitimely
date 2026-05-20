package macos

// Bridge is the only seam between the daemon and macOS. All system calls,
// subprocess invocations, and platform-specific code go through this interface.
// Tests inject FakeBridge; production wires the Real* implementations.
type Bridge interface {
	Frontmost() (FrontmostInfo, error)
	FocusedWindowTitle() (string, error)
	IdleSeconds() (int, error)
	ListProcesses() ([]ProcessSample, error)
	ProcessCWD(pid int) (string, error)
}

type FrontmostInfo struct {
	BundleID string
	PID      int
	Name     string
}

type ProcessSample struct {
	PID      int
	Name     string // executable basename, e.g. "claude"
	CPUTicks uint64 // monotonic counter, units = centiseconds
}
