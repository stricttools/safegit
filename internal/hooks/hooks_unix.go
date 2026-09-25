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
	return signalGroup(cmd.Process.Pid, sig)
}

// signalGroup is how killGroup signals a process group; a test stands in for
// it to play a process that survives SIGKILL.
var signalGroup = func(pgid int, sig syscall.Signal) error {
	return syscall.Kill(-pgid, sig)
}
