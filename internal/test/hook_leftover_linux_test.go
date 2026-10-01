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

	"github.com/stricttools/safegit/internal/exitcode"
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
			if left := hooks[0].LeftoverProcesses; len(left) != 1 ||
				left[0].PID != pid || left[0].Command != "sleep" || !left[0].Killed || left[0].State == nil || len(*left[0].State) != 1 {
				t.Errorf("leftover_processes = %+v, want the killed sleep %d with its one-letter state", left, pid)
			}
			assertNoVerdictMember(t, stdout)
		})
	}
}

// A process outside safegit's process tree that still holds a hook's output
// after the hook ended cannot be named by safegit. The run fails with exit 20,
// and the entry records that as a fact: unidentified_leftovers true and the
// reason identification failed in leftover_identification_error. The outside
// holder is this test process, which opens the hook's stdout pipe through
// /proc while the hook runs.
func TestHookUnidentifiedLeftoverIsRecordedInThePayload(t *testing.T) {
	dir, _ := newRepoWithRemote(t)

	tmp := t.TempDir()
	pidFile := filepath.Join(tmp, "hook.pid")
	readyFile := filepath.Join(tmp, "ready")
	installDirHook(t, dir, "10-lint", strings.Join([]string{
		"echo $$ > " + pidFile + ".tmp && mv " + pidFile + ".tmp " + pidFile,
		"while [ ! -e " + readyFile + " ]; do sleep 0.05; done",
	}, "\n"))

	for _, args := range [][]string{
		{"--json", "push", "--refs", "head", "origin"},
		{"--json", "hook", "run"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			for _, f := range []string{pidFile, readyFile} {
				if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			// Hold the hook's stdout pipe from outside safegit's tree, then let
			// the hook end.
			held := make(chan *os.File, 1)
			go func() {
				defer os.WriteFile(readyFile, nil, 0o644)
				deadline := time.Now().Add(30 * time.Second)
				for time.Now().Before(deadline) {
					data, err := os.ReadFile(pidFile)
					if err == nil {
						if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
							f, err := os.OpenFile("/proc/"+strconv.Itoa(pid)+"/fd/1", os.O_WRONLY, 0)
							if err == nil {
								held <- f
							}
							close(held)
							return
						}
					}
					time.Sleep(20 * time.Millisecond)
				}
				close(held)
			}()

			stdout, stderr, code := runSafegit(t, dir, args...)
			f, ok := <-held
			if !ok || f == nil {
				t.Fatalf("could not hold the hook's output pipe; stderr:\n%s", stderr)
			}
			defer f.Close()

			if code != exitcode.PushHookFailed {
				t.Errorf("exit code = %d, want %d; stderr:\n%s", code, exitcode.PushHookFailed, stderr)
			}
			hooks := payloadHooks(t, stdout)
			if len(hooks) != 1 {
				t.Fatalf("hooks = %d entries, want 1", len(hooks))
			}
			e := hooks[0]
			if e.ExitCode == nil || *e.ExitCode != 0 {
				t.Errorf("exit_code = %v, want 0: the hook itself exited 0", e.ExitCode)
			}
			if len(e.LeftoverProcesses) != 0 {
				t.Errorf("leftover_processes = %+v, want none: the holder could not be named", e.LeftoverProcesses)
			}
			if e.UnidentifiedLeftovers == nil || !*e.UnidentifiedLeftovers {
				t.Errorf("unidentified_leftovers = %v, want true", e.UnidentifiedLeftovers)
			}
			if e.LeftoverIdentificationError == nil || !strings.Contains(*e.LeftoverIdentificationError, "outside safegit's process tree") {
				t.Errorf("leftover_identification_error = %v, want the reason naming a holder outside safegit's process tree", e.LeftoverIdentificationError)
			}
			assertNoVerdictMember(t, stdout)
		})
	}
}
