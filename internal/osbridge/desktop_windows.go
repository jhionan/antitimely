package osbridge

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	openInputDesktop  = user32.NewProc("OpenInputDesktop")
	closeDesktop      = user32.NewProc("CloseDesktop")
	getThreadDesktop  = user32.NewProc("GetThreadDesktop")
	getUserObjectInfo = user32.NewProc("GetUserObjectInformationW")
	querySessionInfo  = windows.NewLazySystemDLL("wtsapi32.dll").NewProc("WTSQuerySessionInformationW")
)

func interactiveDesktop() bool {
	// OpenInputDesktop can report the desktop that will receive input on
	// reconnect, so check the session before looking at its desktop.
	if !sessionConnected() {
		return false
	}
	const desktopReadObjects = 0x0001
	input, _, _ := openInputDesktop.Call(0, 0, desktopReadObjects)
	if input == 0 {
		return false
	}
	defer closeDesktop.Call(input)

	// The daemon stays on its startup desktop. A lock screen or secure
	// prompt can switch the input desktop while its window still exists.
	thread, _, _ := getThreadDesktop.Call(uintptr(windows.GetCurrentThreadId()))
	inputName := desktopName(input)
	return inputName != "" && strings.EqualFold(inputName, desktopName(thread))
}

func sessionConnected() bool {
	const (
		wtsCurrentSession = 0xffffffff
		wtsConnectState   = 8
	)
	var buffer *uint32
	var size uint32
	ok, _, _ := querySessionInfo.Call(0, wtsCurrentSession, wtsConnectState, uintptr(unsafe.Pointer(&buffer)), uintptr(unsafe.Pointer(&size)))
	if ok == 0 || buffer == nil {
		return false
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(buffer)))
	return size >= uint32(unsafe.Sizeof(*buffer)) && *buffer == windows.WTSActive
}

func desktopName(handle uintptr) string {
	if handle == 0 {
		return ""
	}
	const uoiName = 2
	var name [256]uint16
	ok, _, _ := getUserObjectInfo.Call(handle, uoiName, uintptr(unsafe.Pointer(&name[0])), unsafe.Sizeof(name), 0)
	if ok == 0 {
		return ""
	}
	return windows.UTF16ToString(name[:])
}
