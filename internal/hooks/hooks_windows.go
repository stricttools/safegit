//go:build windows

package hooks

import (
	"os/exec"
	"syscall"
)

// setProcGroup is a no-op on Windows (no process groups).
func setProcGroup(_ *exec.Cmd) {}

// killGroup kills the process directly on Windows. The signal parameter
// is ignored -- Windows has no process groups or POSIX signals.
func killGroup(cmd *exec.Cmd, _ syscall.Signal) error {
	return cmd.Process.Kill()
}
