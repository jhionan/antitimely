package macos

// RealBridge implements Bridge by delegating to the *Real functions in this
// package. Used by the daemon in production.
type RealBridge struct{}

func (RealBridge) Frontmost() (FrontmostInfo, error)       { return FrontmostReal() }
func (RealBridge) FocusedWindowTitle() (string, error)     { return FocusedWindowTitleReal() }
func (RealBridge) IdleSeconds() (int, error)               { return IdleSecondsReal() }
func (RealBridge) ListProcesses() ([]ProcessSample, error) { return ListProcessesReal() }
func (RealBridge) ProcessCWD(pid int) (string, error)      { return ProcessCWDReal(pid) }

// Compile-time assertion that RealBridge implements Bridge.
var _ Bridge = RealBridge{}
