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
	"time"

	"github.com/smm-h/safegit/internal/exitcode"
)

// A pre-pre-push hook that exits 0 but leaves a process running fails the push
// with the hook-failure exit code, names the process, and kills it; nothing is
// pushed.
func TestPushFailsWhenAHookLeavesADetachedProcessBehind(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not installed")
	}
	dir, remote := newRepoWithRemote(t)

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	src := filepath.Join(t.TempDir(), "pre-pre-push")
	writeHookScript(t, src, strings.Join([]string{
		"setsid sh -c 'echo $$ > " + pidFile + "; exec sleep 60' </dev/null >/dev/null 2>&1 &",
		"while [ ! -s " + pidFile + " ]; do sleep 0.05; done",
	}, "\n"))
	if _, stderr, code := runSafegit(t, dir, "hook", "install", src); code != 0 {
		t.Fatalf("hook install failed (code %d): %s", code, stderr)
	}

	for _, args := range [][]string{
		{"push", "--refs", "head", "origin"},
		{"hook", "run"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			_, stderr, code := runSafegit(t, dir, args...)
			data, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatalf("the hook did not record its child: %v (stderr: %s)", err, stderr)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.Kill(pid, syscall.SIGKILL)

			if code != exitcode.PushHookFailed {
				t.Errorf("exit code = %d, want %d (PushHookFailed); stderr:\n%s", code, exitcode.PushHookFailed, stderr)
			}
			want := "hook pre-pre-push left process " + strconv.Itoa(pid) + " (sleep) running after it ended; it was killed"
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr does not say %q:\n%s", want, stderr)
			}
			if err := syscall.Kill(pid, 0); err == nil {
				// Reaped by safegit before it exited, so it must be gone now.
				time.Sleep(200 * time.Millisecond)
				if err := syscall.Kill(pid, 0); err == nil {
					t.Errorf("the leftover process %d is still running", pid)
				}
			}
		})
	}
	if branches := remoteBranches(t, remote); len(branches) != 0 {
		t.Errorf("a failed hook run must push nothing, the remote has %v", branches)
	}
}

// The same run in machine mode: the payload is emitted, and the hook's entry
// names the process it left behind as a fact, with the verdict carried by exit
// 20 alone.
func TestHookLeftoverProcessIsRecordedInThePayload(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not installed")
	}
	dir, _ := newRepoWithRemote(t)

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	installDirHook(t, dir, "10-lint", strings.Join([]string{
		"setsid sh -c 'echo $$ > " + pidFile + "; exec sleep 60' </dev/null >/dev/null 2>&1 &",
		"while [ ! -s " + pidFile + " ]; do sleep 0.05; done",
	}, "\n"))

	for _, args := range [][]string{
		{"--json", "push", "--refs", "head", "origin"},
		{"--json", "hook", "run"},
		{"--json", "hook", "run", "10-lint"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			stdout, stderr, code := runSafegit(t, dir, args...)
			data, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatalf("the hook did not record its child: %v (stderr: %s)", err, stderr)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.Kill(pid, syscall.SIGKILL)

			if code != exitcode.PushHookFailed {
				t.Errorf("exit code = %d, want %d; stderr:\n%s", code, exitcode.PushHookFailed, stderr)
			}
			hooks := payloadHooks(t, stdout)
			if len(hooks) != 1 {
				t.Fatalf("hooks = %d entries, want 1", len(hooks))
			}
			checkEntry(t, hooks[0], "10-lint", 0, false)
			want := leftoverRecord{PID: pid, Command: "sleep", Killed: true}
			if len(hooks[0].LeftoverProcesses) != 1 || hooks[0].LeftoverProcesses[0] != want {
				t.Errorf("leftover_processes = %+v, want [%+v]", hooks[0].LeftoverProcesses, want)
			}
			assertNoVerdictMember(t, stdout)
		})
	}
}
