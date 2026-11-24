package osbridge

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	user32                   = windows.NewLazySystemDLL("user32.dll")
	getForegroundWindow      = user32.NewProc("GetForegroundWindow")
	getWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	getWindowText            = user32.NewProc("GetWindowTextW")
	getLastInputInfo         = user32.NewProc("GetLastInputInfo")
	getTickCount             = kernel32.NewProc("GetTickCount")
)

type RealBridge struct{}

var _ Bridge = (*RealBridge)(nil)

func (r *RealBridge) Frontmost(ctx context.Context) (FrontmostInfo, error) {
	return FrontmostReal(ctx)
}

func FrontmostReal(ctx context.Context) (FrontmostInfo, error) {
	if err := ctx.Err(); err != nil {
		return FrontmostInfo{}, err
	}
	if !interactiveDesktop() {
		return FrontmostInfo{}, nil
	}
	hwnd, _, _ := getForegroundWindow.Call()
	if hwnd == 0 {
		return FrontmostInfo{}, nil
	}
	pid := windowPID(hwnd)
	if pid == 0 {
		return FrontmostInfo{}, nil
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		// A protected process or a window that closed during the query.
		return FrontmostInfo{}, nil
	}
	if err != nil {
		return FrontmostInfo{}, fmt.Errorf("open foreground process: %w", err)
	}
	defer windows.CloseHandle(handle)
	path := make([]uint16, 32768)
	size := uint32(len(path))
	if err := windows.QueryFullProcessImageName(handle, 0, &path[0], &size); err != nil {
		return FrontmostInfo{}, fmt.Errorf("foreground executable: %w", err)
	}
	name := filepath.Base(windows.UTF16ToString(path[:size]))
	focused := FrontmostInfo{BundleID: strings.ToLower(name), PID: int(pid), Name: name, window: hwnd}
	if focused.BundleID == "applicationframehost.exe" {
		if id := hostedApplicationID(hwnd, pid); id != "" {
			focused.BundleID = "appx:" + id
		}
	}
	if !stillForeground(focused) || !interactiveDesktop() {
		return FrontmostInfo{}, nil
	}
	return focused, nil
}

func windowPID(hwnd uintptr) uint32 {
	var pid uint32
	getWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

func (r *RealBridge) FocusedWindowTitle(ctx context.Context, focused FrontmostInfo) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !stillForeground(focused) {
		return "", ErrForegroundChanged
	}
	var title [4096]uint16
	length, _, err := getWindowText.Call(focused.window, uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
	if !stillForeground(focused) || !interactiveDesktop() ||
		(strings.HasPrefix(focused.BundleID, "appx:") &&
			focused.BundleID != "appx:"+hostedApplicationID(focused.window, uint32(focused.PID))) {
		return "", ErrForegroundChanged
	}
	if length == 0 && !errors.Is(err, windows.ERROR_SUCCESS) {
		return "", fmt.Errorf("foreground window title: %w", err)
	}
	return windows.UTF16ToString(title[:length]), nil
}

func stillForeground(focused FrontmostInfo) bool {
	hwnd, _, _ := getForegroundWindow.Call()
	return hwnd != 0 && hwnd == focused.window && int(windowPID(hwnd)) == focused.PID
}

func FocusedWindowDebugReal(ctx context.Context) (string, error) {
	focused, err := FrontmostReal(ctx)
	if err != nil {
		return "", err
	}
	if focused.window == 0 {
		return "No accessible foreground window.\n", nil
	}
	title, err := (&RealBridge{}).FocusedWindowTitle(ctx, focused)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("app=%q pid=%d title=%q\n", focused.BundleID, focused.PID, title), nil
}

func (r *RealBridge) IdleSeconds(ctx context.Context) (int, error) {
	return IdleSecondsReal(ctx)
}

func IdleSecondsReal(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var info struct {
		size uint32
		tick uint32
	}
	info.size = uint32(unsafe.Sizeof(info))
	ok, _, err := getLastInputInfo.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return 0, fmt.Errorf("get last input time: %w", err)
	}
	ticks, _, _ := getTickCount.Call()
	// Unsigned subtraction handles wraparound of the 32-bit millisecond clock.
	return int((uint32(ticks) - info.tick) / 1000), nil
}
