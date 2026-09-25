//go:build linux

package test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// readPidFile reads a pid a hook recorded, failing the test when it is absent.
func readPidFile(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return pid
}

// processGone polls until no process has the pid, a zombie counting as gone.
func processGone(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return true
		}
		if i := strings.LastIndexByte(string(data), ')'); i >= 0 && strings.HasPrefix(strings.TrimSpace(string(data[i+1:])), "Z") {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// An interrupted safegit does not leave the hook it was running behind. The
// hook runs in its own process group, so a terminal's Ctrl-C never reaches it;
// the signal reaches safegit, which stops the hook and everything it started
// the same way a timeout does, names what the hook left behind, and exits the
// conventional 128 + the signal number.
func TestInterruptedSafegitStopsTheRunningHook(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not installed")
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		for _, args := range [][]string{
			{"push", "--refs", "head", "origin"},
			{"hook", "run"},
			{"hook", "run", "10-lint"},
		} {
			t.Run(sig.String()+" "+strings.Join(args, " "), func(t *testing.T) {
				dir, remote := newRepoWithRemote(t)
				scratch := evalTempDir(t)
				hookPid := filepath.Join(scratch, "hook.pid")
				childPid := filepath.Join(scratch, "child.pid")
				installDirHook(t, dir, "10-lint", strings.Join([]string{
					"echo $$ > " + hookPid + ".tmp && mv " + hookPid + ".tmp " + hookPid,
					"setsid sh -c 'echo $$ > " + childPid + ".tmp && mv " + childPid + ".tmp " + childPid + "; exec sleep 60' </dev/null >/dev/null 2>&1 &",
					"sleep 60",
				}, "\n"))

				cmd := exec.Command(safegitBin, args...)
				cmd.Dir = dir
				cmd.Env = controlledEnv(t)
				var stderr strings.Builder
				cmd.Stderr = &stderr
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				waitForFile(t, hookPid, 30*time.Second)
				waitForFile(t, childPid, 30*time.Second)
				hook, child := readPidFile(t, hookPid), readPidFile(t, childPid)
				t.Cleanup(func() {
					syscall.Kill(hook, syscall.SIGKILL)
					syscall.Kill(child, syscall.SIGKILL)
				})

				if err := cmd.Process.Signal(sig); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				var waitErr error
				select {
				case waitErr = <-done:
				case <-time.After(30 * time.Second):
					cmd.Process.Kill()
					t.Fatalf("safegit did not exit within 30s of %v; stderr:\n%s", sig, stderr.String())
				}

				var exitErr *exec.ExitError
				if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 128+int(sig) {
					t.Errorf("safegit ended with %v, want exit %d (128 + %v); stderr:\n%s", waitErr, 128+int(sig), sig, stderr.String())
				}
				if !processGone(hook, 2*time.Second) {
					t.Errorf("the hook process %d is still running after safegit exited", hook)
				}
				if !processGone(child, 2*time.Second) {
					t.Errorf("the hook's detached child %d is still running after safegit exited", child)
				}
				want := "hook 10-lint left process " + strconv.Itoa(child) + " (sleep) running after it ended; it was killed"
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr does not name the leftover as %q:\n%s", want, stderr.String())
				}
				if branches := remoteBranches(t, remote); len(branches) != 0 {
					t.Errorf("an interrupted push must push nothing; the remote has %v", branches)
				}
			})
		}
	}
}
