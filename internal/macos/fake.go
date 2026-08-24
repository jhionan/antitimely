package macos

import (
	"context"
	"time"
)

// FakeBridge is an in-memory test implementation of Bridge. All fields are
// public so tests can mutate state between calls.
type FakeBridge struct {
	FrontmostInfoVal FrontmostInfo
	FrontmostErr     error
	// FrontmostCalls counts Frontmost invocations so tests can assert it is
	// NOT suppressed by the title backoff.
	FrontmostCalls int

	FocusedTitle    string
	FocusedTitleErr error
	// FocusedTitleCalls counts FocusedWindowTitle invocations so tests can
	// assert the daemon backs off (stops spawning osascript) when denied.
	FocusedTitleCalls int

	IdleSecondsVal int
	IdleErr        error
	// IdleDelay makes IdleSeconds block before returning, so tests can build
	// a tick that overruns its budget without depending on machine speed.
	IdleDelay time.Duration

	Processes    []ProcessSample
	ProcessesErr error

	// CWDByPID maps pid -> cwd. PIDs absent from the map and absent from
	// CWDErrByPID return ("", nil) — matching the real implementation's
	// behavior when lsof exits 1 (no cwd available).
	CWDByPID map[int]string

	// CWDErrByPID lets tests inject per-pid errors. Takes precedence over
	// CWDErr. Use this when you need a mixed-permission scenario (e.g.
	// lookup succeeds for one pid but is denied for another on the same tick).
	CWDErrByPID map[int]error

	// CWDErr applies to every pid when set, unless overridden by CWDErrByPID.
	CWDErr error

	// EnvByPID maps pid -> env key -> value. PIDs and keys absent from the map
	// return ("", nil), matching the real implementation when the variable is
	// unset or the process is gone.
	EnvByPID map[int]map[string]string

	// EnvErr applies to every pid when set.
	EnvErr error

	// EnvCalls counts ProcessEnvVar invocations so tests can assert the
	// daemon probes a pid's environment once rather than every tick.
	EnvCalls int
}

func (f *FakeBridge) Frontmost(ctx context.Context) (FrontmostInfo, error) {
	f.FrontmostCalls++
	return f.FrontmostInfoVal, f.FrontmostErr
}
func (f *FakeBridge) FocusedWindowTitle(ctx context.Context) (string, error) {
	f.FocusedTitleCalls++
	return f.FocusedTitle, f.FocusedTitleErr
}
func (f *FakeBridge) IdleSeconds(ctx context.Context) (int, error) {
	if f.IdleDelay > 0 {
		time.Sleep(f.IdleDelay)
	}
	return f.IdleSecondsVal, f.IdleErr
}
func (f *FakeBridge) ListProcesses(ctx context.Context) ([]ProcessSample, error) {
	return f.Processes, f.ProcessesErr
}
func (f *FakeBridge) ProcessCWD(ctx context.Context, pid int) (string, error) {
	if err, ok := f.CWDErrByPID[pid]; ok {
		return "", err
	}
	if f.CWDErr != nil {
		return "", f.CWDErr
	}
	return f.CWDByPID[pid], nil
}
func (f *FakeBridge) ProcessEnvVar(ctx context.Context, pid int, key string) (string, error) {
	f.EnvCalls++
	if f.EnvErr != nil {
		return "", f.EnvErr
	}
	return f.EnvByPID[pid][key], nil
}

// Compile-time assertion that *FakeBridge satisfies Bridge.
var _ Bridge = (*FakeBridge)(nil)
