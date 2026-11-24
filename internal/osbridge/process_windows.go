package osbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

func (r *RealBridge) ListProcesses(ctx context.Context) ([]ProcessSample, error) {
	var session uint32
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &session); err != nil {
		return nil, err
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	var samples []ProcessSample
	entry := windows.ProcessEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pid := entry.ProcessID
		var otherSession uint32
		if pid == 0 || windows.ProcessIdToSessionId(pid, &otherSession) != nil || otherSession != session {
			continue
		}
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			continue
		}
		var started, exited, kernel, user windows.Filetime
		err = windows.GetProcessTimes(handle, &started, &exited, &kernel, &user)
		windows.CloseHandle(handle)
		if err == nil && filetimeValue(exited) == 0 {
			samples = append(samples, ProcessSample{
				PID: int(pid), Name: strings.ToLower(windows.UTF16ToString(entry.ExeFile[:])),
				Started: filetimeValue(started), CPUTicks: (filetimeValue(kernel) + filetimeValue(user)) / 100_000,
			})
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return samples, nil
}

func filetimeValue(t windows.Filetime) uint64 {
	return uint64(t.HighDateTime)<<32 | uint64(t.LowDateTime)
}

func (r *RealBridge) ProcessCWD(ctx context.Context, pid int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	process, err := openOwnProcess(pid)
	if err != nil || process == 0 {
		return "", err
	}
	defer windows.CloseHandle(process)

	params, layout, err := processParameters(process)
	if err != nil || params == 0 {
		return "", err
	}
	var directory [16]byte // UNICODE_STRING
	field := directory[:layout.ptrSize*2]
	if err := readProcess(process, params+layout.cwd, field); err != nil {
		return "", err
	}
	length := int(binary.LittleEndian.Uint16(field))
	address := pointerValue(field[layout.ptrSize:])
	if length == 0 || length%2 != 0 || length > int(binary.LittleEndian.Uint16(field[2:])) || address == 0 {
		return "", nil
	}
	data := make([]byte, length)
	if err := readProcess(process, address, data); err != nil {
		return "", err
	}
	return filepath.Clean(decodeProcessUTF16(data)), nil
}

func (r *RealBridge) ProcessEnvVars(ctx context.Context, pid int, keys ...string) (map[string]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	process, err := openOwnProcess(pid)
	if err != nil || process == 0 {
		return nil, err
	}
	defer windows.CloseHandle(process)

	params, layout, err := processParameters(process)
	if err != nil || params == 0 {
		return nil, err
	}
	var field [8]byte
	if err := readProcess(process, params+layout.env, field[:layout.ptrSize]); err != nil {
		return nil, err
	}
	cursor := pointerValue(field[:layout.ptrSize])
	if cursor == 0 {
		return nil, nil
	}
	requested := make(map[string]string, len(keys))
	for _, key := range keys {
		requested[strings.ToUpper(key)] = key
	}

	// Each UTF-16 string ends in NUL and the block ends in an extra NUL.
	// Read to the next page boundary so the final read stays in mapped memory.
	const pageSize, maxEnvironment = 4096, 1024 * 1024
	var page [pageSize]byte
	var entry []byte
	result := make(map[string]string)
	for scanned := 0; scanned < maxEnvironment; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := min(pageSize-int(cursor%pageSize), maxEnvironment-scanned)
		if err := readProcess(process, cursor, page[:n]); err != nil {
			return nil, err
		}
		for i := 0; i+1 < n; i += 2 {
			if page[i] != 0 || page[i+1] != 0 {
				entry = append(entry, page[i:i+2]...)
				continue
			}
			if len(entry) == 0 {
				return result, nil
			}
			name, value, _ := strings.Cut(decodeProcessUTF16(entry), "=")
			if key, ok := requested[strings.ToUpper(name)]; ok {
				result[key] = value
				if len(result) == len(requested) {
					return result, nil
				}
			}
			entry = entry[:0]
		}
		scanned += n
		cursor += uintptr(n)
	}
	return nil, errors.New("process environment exceeds the probe limit")
}

// openOwnProcess restricts memory probes to the current user and session.
// A protected or exited process is unavailable and can be skipped.
func openOwnProcess(pid int) (windows.Handle, error) {
	var ours, theirs uint32
	if pid <= 0 || windows.ProcessIdToSessionId(uint32(pid), &theirs) != nil {
		return 0, nil
	}
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &ours); err != nil || ours != theirs {
		return 0, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		windows.CloseHandle(process)
		return 0, nil
	}
	defer token.Close()
	other, err := token.GetTokenUser()
	if err != nil {
		windows.CloseHandle(process)
		return 0, err
	}
	self, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !self.User.Sid.Equals(other.User.Sid) {
		windows.CloseHandle(process)
		return 0, err
	}
	return process, nil
}

// Windows exposes no public API for another process's working directory.
// Keep the PEB layout here and bound each read. These Windows layouts are
// private and may change. A failed read simply leaves the process unclassified.
type parameterLayout struct {
	ptrSize          int
	params, cwd, env uintptr
}

func processParameters(process windows.Handle) (uintptr, parameterLayout, error) {
	layout32 := parameterLayout{ptrSize: 4, params: 0x10, cwd: 0x24, env: 0x48}
	layout64 := parameterLayout{ptrSize: 8, params: 0x20, cwd: 0x38, env: 0x80}
	layout := layout32
	var peb uintptr
	if unsafe.Sizeof(peb) == 8 {
		layout = layout64
		if err := windows.NtQueryInformationProcess(process, windows.ProcessWow64Information, unsafe.Pointer(&peb), uint32(unsafe.Sizeof(peb)), nil); err != nil {
			return 0, layout, err
		}
		if peb != 0 {
			layout = layout32
		}
	}
	if peb == 0 {
		var info windows.PROCESS_BASIC_INFORMATION
		if err := windows.NtQueryInformationProcess(process, windows.ProcessBasicInformation, unsafe.Pointer(&info), uint32(unsafe.Sizeof(info)), nil); err != nil {
			return 0, layout, err
		}
		peb = uintptr(unsafe.Pointer(info.PebBaseAddress))
	}
	if peb == 0 {
		return 0, layout, nil
	}
	var field [8]byte
	err := readProcess(process, peb+layout.params, field[:layout.ptrSize])
	return pointerValue(field[:layout.ptrSize]), layout, err
}

func readProcess(process windows.Handle, address uintptr, buf []byte) error {
	var n uintptr
	if err := windows.ReadProcessMemory(process, address, &buf[0], uintptr(len(buf)), &n); err != nil {
		return fmt.Errorf("read process parameters: %w", err)
	}
	if n != uintptr(len(buf)) {
		return errors.New("incomplete process parameter read")
	}
	return nil
}

func pointerValue(field []byte) uintptr {
	if len(field) == 4 {
		return uintptr(binary.LittleEndian.Uint32(field))
	}
	return uintptr(binary.LittleEndian.Uint64(field))
}

func decodeProcessUTF16(data []byte) string {
	code := make([]uint16, len(data)/2)
	for i := range code {
		code[i] = binary.LittleEndian.Uint16(data[i*2:])
	}
	return string(utf16.Decode(code))
}
