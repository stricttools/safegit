//go:build linux

package hooks

import (
	"bytes"
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A process that outlives SIGKILL -- one in uninterruptible sleep, state D --
// cannot be produced on demand, so these tests play one: the seams that send
// SIGKILL drop it, and the process ignores SIGTERM, so nothing safegit sends
// can end it. The stop must still end at its cap, name the process as not
// stoppable with its state, and record it as not killed.

const testStopCap = 11 * time.Second

// dropSIGKILL makes every SIGKILL safegit sends, to a process or to a process
// group, go nowhere, for the rest of the test.
func dropSIGKILL(t *testing.T) {
	prevProcess, prevGroup := signalProcess, signalGroup
	signalProcess = func(pid, fd int, sig syscall.Signal) {
		if sig != syscall.SIGKILL {
			prevProcess(pid, fd, sig)
		}
	}
	signalGroup = func(pgid int, sig syscall.Signal) error {
		if sig == syscall.SIGKILL {
			return nil
		}
		return prevGroup(pgid, sig)
	}
	t.Cleanup(func() { signalProcess, signalGroup = prevProcess, prevGroup })
}

// killForReal ends a process the test left unkillable, and reaps it when it is
// this process's child.
func killForReal(pid int) {
	syscall.Kill(pid, syscall.SIGKILL)
	var ws syscall.WaitStatus
	for i := 0; i < 100; i++ {
		if wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); wpid == pid || err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// runWithin runs the hooks in a goroutine and fails the test when the run does
// not return within limit, so a stop that ignores its cap fails instead of
// hanging the suite.
func runWithin(t *testing.T, limit time.Duration, run func() ([]HookResult, error)) ([]HookResult, time.Duration) {
	t.Helper()
	type outcome struct {
		results []HookResult
		err     error
	}
	done := make(chan outcome, 1)
	start := time.Now()
	go func() {
		r, err := run()
		done <- outcome{r, err}
	}()
	select {
	case o := <-done:
		if o.err != nil {
			t.Fatal(o.err)
		}
		return o.results, time.Since(start)
	case <-time.After(limit):
		t.Fatalf("the run did not return within %v", limit)
		return nil, 0
	}
}

// findLeftover returns the leftover at pid, failing the test when there is none.
func findLeftover(t *testing.T, r HookResult, pid int) LeftoverProcess {
	t.Helper()
	for _, l := range r.Leftovers {
		if l.PID == pid {
			return l
		}
	}
	t.Fatalf("the result does not name process %d: %+v", pid, r.Leftovers)
	return LeftoverProcess{}
}

// A hook exits 0 and leaves behind a process nothing can kill. The sweep gives
// it the SIGTERM grace, then SIGKILL, and at the cap names it as still running,
// with its state, and returns.
func TestLeftoverThatOutlivesSIGKILLIsNamedAtTheCap(t *testing.T) {
	dropSIGKILL(t)
	var errBuf bytes.Buffer
	restore := SetOutput(&bytes.Buffer{}, &errBuf)
	defer restore()

	gitDir := setupGitDir(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	writeHook(t, filepath.Join(LocalDir(gitDir), "pre-pre-push"), "#!/bin/sh\n"+
		"sh -c \"trap '' TERM; echo \\$\\$ > '"+pidFile+"'; exec sleep 60\" </dev/null >/dev/null 2>&1 &\n"+
		"while [ ! -s '"+pidFile+"' ]; do sleep 0.05; done\nexit 0\n")

	results, elapsed := runWithin(t, testStopCap+10*time.Second, func() ([]HookResult, error) {
		return Run(context.Background(), store(gitDir), nil, 30, testStopCap, nil)
	})
	pid := readPid(t, pidFile)
	t.Cleanup(func() { killForReal(pid) })

	if elapsed > testStopCap+time.Second {
		t.Errorf("the run took %v; the stop is capped at %v", elapsed, testStopCap)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %+v", results)
	}
	r := results[0]
	l := findLeftover(t, r, pid)
	if l.Killed {
		t.Errorf("leftover %d is reported as killed; it outlived SIGKILL", pid)
	}
	if !r.Failed() {
		t.Errorf("a hook that left a process behind must be a failed run: %+v", r)
	}
	msgs := strings.Join(r.LeftoverMessages(), "\n")
	want := "hook pre-pre-push left process " + strconv.Itoa(pid) + " (sleep) running after it ended; " +
		"it is still running: safegit could not stop it within the 11s cap on stopping a hook (process state S) and exits without it"
	if !strings.Contains(msgs, want) {
		t.Errorf("the messages do not say %q:\n%s", want, msgs)
	}
}

// A hook that ignores SIGTERM times out, and SIGKILL does not end it either.
// The timeout's stop runs under the same cap: at the cap the hook itself is
// named as still running, with its state, and the run returns.
func TestTimedOutHookThatOutlivesSIGKILLIsNamedAtTheCap(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timeout test in short mode")
	}
	dropSIGKILL(t)
	restore := SetOutput(&bytes.Buffer{}, &bytes.Buffer{})
	defer restore()

	gitDir := setupGitDir(t)
	pidFile := filepath.Join(t.TempDir(), "hook.pid")
	writeHook(t, filepath.Join(LocalDir(gitDir), "pre-pre-push"),
		"#!/bin/sh\ntrap '' TERM\necho $$ > '"+pidFile+"'\nexec sleep 60\n")

	const timeoutSec = 1
	results, elapsed := runWithin(t, timeoutSec*time.Second+testStopCap+10*time.Second, func() ([]HookResult, error) {
		return Run(context.Background(), store(gitDir), nil, timeoutSec, testStopCap, nil)
	})
	pid := readPid(t, pidFile)
	t.Cleanup(func() { killForReal(pid) })

	if elapsed > timeoutSec*time.Second+testStopCap+time.Second {
		t.Errorf("the run took %v; the timeout is %ds and the stop after it is capped at %v", elapsed, timeoutSec, testStopCap)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %+v", results)
	}
	r := results[0]
	if !r.TimedOut {
		t.Errorf("expected a timed-out result, got %+v", r)
	}
	l := findLeftover(t, r, pid)
	if l.Killed {
		t.Errorf("the hook process %d is reported as killed; it outlived SIGKILL", pid)
	}
	msgs := strings.Join(r.LeftoverMessages(), "\n")
	want := "hook pre-pre-push (process " + strconv.Itoa(pid) + ") is still running: " +
		"safegit could not stop it within the 11s cap on stopping a hook (process state S) and exits without it"
	if !strings.Contains(msgs, want) {
		t.Errorf("the messages do not say %q:\n%s", want, msgs)
	}
}
