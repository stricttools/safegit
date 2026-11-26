package testutil

import (
	"golang.org/x/sys/windows"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

const InterruptExitStatus = 130

// Give only the fixture a private hidden native console. The signaller attaches
// to it from a detached helper, never broadcasting into the user's console.
func PrepareInterruptible(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE, HideWindow: true}
}
func InterruptCommand(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	WriteExecutable(t, filepath.Join(t.TempDir(), "controller"), "#!/bin/sh\nexit 0\n")
	signal := exec.Command(shimBinary, "--ctrl-break-target", strconv.Itoa(cmd.Process.Pid))
	signal.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS, HideWindow: true}
	if out, err := signal.CombinedOutput(); err != nil {
		t.Fatalf("native console Ctrl-Break: %v %s", err, out)
	}
}
