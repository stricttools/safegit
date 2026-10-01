//go:build linux

package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestLeftoverProcessRecordsItsState: on Linux every process a hook left behind
// is recorded with the state letter /proc reported when safegit found it.
func TestLeftoverProcessRecordsItsState(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not installed")
	}
	dir, _ := newRepoWithRemote(t)
	pidFile := filepath.Join(evalTempDir(t), "child.pid")
	installDirHook(t, dir, "10-leave", strings.Join([]string{
		"setsid sh -c 'echo $$ > " + pidFile + "; exec sleep 60' </dev/null >/dev/null 2>&1 &",
		"while [ ! -s " + pidFile + " ]; do sleep 0.05; done",
	}, "\n"))

	stdout, stderr, _ := runSafegit(t, dir, "--json", "hook", "run")
	if data, err := os.ReadFile(pidFile); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			defer syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	hooks := payloadHooks(t, stdout)
	if len(hooks) != 1 || len(hooks[0].LeftoverProcesses) == 0 {
		t.Fatalf("want one hook with a leftover process, got %+v; stderr:\n%s", hooks, stderr)
	}
	for _, l := range hooks[0].LeftoverProcesses {
		if l.State == nil || len(*l.State) != 1 {
			t.Errorf("leftover %d (%s) state = %v, want the one-letter state /proc reported", l.PID, l.Command, l.State)
		}
	}
}
