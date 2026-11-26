//go:build !windows

package testutil

import (
	"os/exec"
	"syscall"
	"testing"
)

const InterruptExitStatus = 128 + int(syscall.SIGTERM)

func PrepareInterruptible(cmd *exec.Cmd) {}
func InterruptCommand(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
}
