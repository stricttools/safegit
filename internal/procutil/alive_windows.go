//go:build windows

// Package procutil provides cross-platform process liveness checks.
package procutil

import "golang.org/x/sys/windows"

// ProcessAlive checks if a process with the given PID exists.
// Uses OpenProcess with PROCESS_QUERY_LIMITED_INFORMATION (least-privilege
// access right) to probe whether the process handle is obtainable.
func ProcessAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	windows.CloseHandle(h)
	return true
}
