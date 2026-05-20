package macos

// FakeBridge is an in-memory test implementation of Bridge. All fields are
// public so tests can mutate state between calls.
type FakeBridge struct {
	FrontmostInfoVal FrontmostInfo
	FrontmostErr     error

	FocusedTitle    string
	FocusedTitleErr error

	IdleSecondsVal int
	IdleErr        error

	Processes    []ProcessSample
	ProcessesErr error

	CWDByPID map[int]string
	CWDErr   error
}

func (f *FakeBridge) Frontmost() (FrontmostInfo, error) {
	return f.FrontmostInfoVal, f.FrontmostErr
}
func (f *FakeBridge) FocusedWindowTitle() (string, error) {
	return f.FocusedTitle, f.FocusedTitleErr
}
func (f *FakeBridge) IdleSeconds() (int, error) {
	return f.IdleSecondsVal, f.IdleErr
}
func (f *FakeBridge) ListProcesses() ([]ProcessSample, error) {
	return f.Processes, f.ProcessesErr
}
func (f *FakeBridge) ProcessCWD(pid int) (string, error) {
	if f.CWDErr != nil {
		return "", f.CWDErr
	}
	return f.CWDByPID[pid], nil
}

// Compile-time assertion that *FakeBridge satisfies Bridge.
var _ Bridge = (*FakeBridge)(nil)
