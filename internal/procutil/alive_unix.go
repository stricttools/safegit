//go:build !windows

// Package procutil provides cross-platform process liveness checks.
package procutil

import "syscall"

// ProcessAlive checks if a process with the given PID exists.
// Returns true if the process is alive (including cases where we lack
// permission to signal it).
func ProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	// ESRCH = no such process; EPERM = exists but we can't signal it (still alive)
	return err == nil || err == syscall.EPERM
}
