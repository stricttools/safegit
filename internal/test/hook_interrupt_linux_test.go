//go:build linux

package test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/smm-h/safegit/internal/hooks"
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
				if !strings.Contains(stderr.String(), stoppingLine("10-lint")) {
					t.Errorf("stderr does not say %q:\n%s", stoppingLine("10-lint"), stderr.String())
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

// syncBuffer is a strings.Builder a subprocess writes into while the test
// reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// stoppingLine is what safegit prints on stderr when an interruption makes it
// begin stopping a hook: the hook's name, and the longest the stop can take,
// taken from the grace and kill budget the stop itself runs under.
func stoppingLine(name string) string {
	return "stopping hook " + name + " and the processes it started; this can take up to " + hooks.InterruptStopBudget().String()
}

// An interrupted machine-mode run still answers with its payload. The hooks run
// so far are facts all the same: the one that passed, and the one the
// interruption stopped, which has no exit status of its own (null) and records
// what it left behind. The exit code stays the signal's, 128 + its number, and
// is the envelope's too. A second signal while the hook is being stopped is
// ignored, and the line saying safegit is stopping the hook is why.
func TestInterruptedJSONRunEmitsThePayload(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not installed")
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		for _, args := range [][]string{
			{"--json", "push", "--refs", "head", "origin"},
			{"--json", "hook", "run"},
		} {
			t.Run(sig.String()+" "+strings.Join(args, " "), func(t *testing.T) {
				dir, remote := newRepoWithRemote(t)
				scratch := evalTempDir(t)
				hookPid := filepath.Join(scratch, "hook.pid")
				childPid := filepath.Join(scratch, "child.pid")
				installDirHook(t, dir, "10-first", "true")
				installDirHook(t, dir, "20-test", strings.Join([]string{
					"echo $$ > " + hookPid + ".tmp && mv " + hookPid + ".tmp " + hookPid,
					"setsid sh -c 'echo $$ > " + childPid + ".tmp && mv " + childPid + ".tmp " + childPid + "; exec sleep 60' </dev/null >/dev/null 2>&1 &",
					// The hook ignores SIGTERM, so the stop waits out the grace
					// before SIGKILL: that is the window the second signal is
					// sent in.
					"trap '' TERM",
					"sleep 60",
				}, "\n"))

				cmd := exec.Command(safegitBin, args...)
				cmd.Dir = dir
				cmd.Env = controlledEnv(t)
				var stdout strings.Builder
				stderr := &syncBuffer{}
				cmd.Stdout = &stdout
				cmd.Stderr = stderr
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
				// The second signal goes in once safegit has said it is stopping
				// the hook, which is the window it must be ignored in.
				deadline := time.Now().Add(10 * time.Second)
				for !strings.Contains(stderr.String(), stoppingLine("20-test")) && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if !strings.Contains(stderr.String(), stoppingLine("20-test")) {
					t.Errorf("stderr does not say %q:\n%s", stoppingLine("20-test"), stderr.String())
				}
				cmd.Process.Signal(sig)

				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				var waitErr error
				select {
				case waitErr = <-done:
				case <-time.After(30 * time.Second):
					cmd.Process.Kill()
					t.Fatalf("safegit did not exit within 30s of %v; stderr:\n%s", sig, stderr.String())
				}

				sigExit := 128 + int(sig)
				var exitErr *exec.ExitError
				if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != sigExit {
					t.Fatalf("safegit ended with %v, want exit %d (128 + %v); stderr:\n%s", waitErr, sigExit, sig, stderr.String())
				}
				if env := decodeEnvelope(t, stdout.String()); env.ExitCode != sigExit {
					t.Errorf("envelope exit_code = %d, want %d", env.ExitCode, sigExit)
				}
				entries := payloadHooks(t, stdout.String())
				if len(entries) != 2 {
					t.Fatalf("hooks = %d entries, want 2 (the one that passed and the interrupted one):\n%s", len(entries), stdout.String())
				}
				checkEntry(t, entries[0], "10-first", 0, false)
				stopped := entries[1]
				if stopped.Name == nil || *stopped.Name != "20-test" {
					t.Errorf("second entry name = %v, want 20-test", stopped.Name)
				}
				if stopped.ExitCode != nil {
					t.Errorf("the interrupted hook's exit_code = %d, want null: safegit killed it", *stopped.ExitCode)
				}
				if stopped.TimedOut == nil || *stopped.TimedOut {
					t.Errorf("the interrupted hook's timed_out = %v, want false", stopped.TimedOut)
				}
				found := false
				for _, l := range stopped.LeftoverProcesses {
					if l.PID == child && l.Killed {
						found = true
					}
				}
				if !found {
					t.Errorf("the interrupted hook's leftover_processes = %+v, want the killed child %d", stopped.LeftoverProcesses, child)
				}
				assertNoVerdictMember(t, stdout.String())
				if args[1] == "push" {
					var p struct {
						Refs []json.RawMessage `json:"refs"`
					}
					if err := json.Unmarshal(decodeEnvelope(t, stdout.String()).Payload, &p); err != nil {
						t.Fatal(err)
					}
					if p.Refs == nil || len(p.Refs) != 0 {
						t.Errorf("an interrupted push's refs = %v, want an empty list", p.Refs)
					}
				}
				if !processGone(child, 2*time.Second) {
					t.Errorf("the hook's detached child %d is still running after safegit exited", child)
				}
				if branches := remoteBranches(t, remote); len(branches) != 0 {
					t.Errorf("an interrupted push must push nothing; the remote has %v", branches)
				}
			})
		}
	}
}
