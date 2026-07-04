//go:build windows

// Package procutil provides cross-platform process liveness checks.
package procutil

import (
	"os"
	"syscall"
)

// ProcessAlive checks if a process with the given PID exists.
// On Windows, os.FindProcess always succeeds, so we probe with Signal(0)
// for a real liveness check.
func ProcessAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal(0) checks liveness without actually sending a signal.
	err = p.Signal(syscall.Signal(0))
	return err == nil
}
