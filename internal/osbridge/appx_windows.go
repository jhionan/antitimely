package osbridge

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	enumChildWindows          = user32.NewProc("EnumChildWindows")
	getApplicationUserModelID = kernel32.NewProc("GetApplicationUserModelId")
	appWindowCallback         = windows.NewCallback(collectAppWindow)
)

type hostedApp struct {
	hostPID   uint32
	id        string
	ambiguous bool
}

func hostedApplicationID(hwnd uintptr, hostPID uint32) string {
	app := hostedApp{hostPID: hostPID}
	enumChildWindows.Call(hwnd, appWindowCallback, uintptr(unsafe.Pointer(&app)))
	if app.ambiguous {
		return ""
	}
	return app.id
}

func collectAppWindow(hwnd uintptr, app *hostedApp) uintptr {
	pid := windowPID(hwnd)
	if pid == 0 || pid == app.hostPID {
		return 1
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 1
	}
	defer windows.CloseHandle(h)

	var buf [256]uint16
	n := uint32(len(buf))
	status, _, _ := getApplicationUserModelID.Call(uintptr(h), uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&buf[0])))
	if status != 0 || n == 0 || n > uint32(len(buf)) {
		return 1
	}
	id := strings.ToLower(windows.UTF16ToString(buf[:n]))
	if app.id != "" && app.id != id {
		app.ambiguous = true
		return 0
	}
	app.id = id
	return 1
}
