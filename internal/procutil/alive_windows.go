package procutil

import "golang.org/x/sys/windows"

// Access denied is not evidence of death. Fail closed for protected processes.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return err != windows.ERROR_INVALID_PARAMETER
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil {
		return true
	}
	return code == 259 // STILL_ACTIVE
}
