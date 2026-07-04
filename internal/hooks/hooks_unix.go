//go:build !windows

package hooks

import (
	"os/exec"
	"syscall"
)

// setProcGroup puts the command in its own process group so we can
// signal the entire group on timeout.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup sends a signal to the process group (negative PID).
func killGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	return syscall.Kill(-cmd.Process.Pid, sig)
}
