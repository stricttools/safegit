//go:build linux

package hooks

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// A hook that leaves any process running when it ends is a failed hook run. On
// Linux safegit is a child subreaper while a hook runs, so a process that
// detaches with setsid or a double fork stays safegit's descendant, and is found,
// named, killed and reaped once the hook ends.

// runLeftoverHook runs one hook whose body records a detached child's pid in
// pidFile before the hook exits, and returns the result and that pid.
func runLeftoverHook(t *testing.T, body string, timeoutSec int) (HookResult, int, time.Duration) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	restore := SetOutput(&outBuf, &errBuf)
	defer restore()

	gitDir := setupGitDir(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	hookPath := filepath.Join(LocalDir(gitDir), "pre-pre-push")
	writeHook(t, hookPath, "#!/bin/sh\n"+strings.ReplaceAll(body, "PIDFILE", "'"+pidFile+"'")+
		"\nwhile [ ! -s '"+pidFile+"' ]; do sleep 0.05; done\necho started\nexit 0\n")

	start := time.Now()
	results, err := Run(context.Background(), store(gitDir), nil, timeoutSec, nil)
	elapsed := time.Since(start)
	pid := readPid(t, pidFile)
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %+v", results)
	}
	if got := outBuf.String(); got != "started\n" {
		t.Errorf("hook stdout = %q, want %q", got, "started\n")
	}
	return results[0], pid, elapsed
}

// assertLeftoverKilled requires the result to name pid as a leftover `sleep`
// that was killed, and the process to be gone.
func assertLeftoverKilled(t *testing.T, r HookResult, pid int) {
	t.Helper()
	found := false
	for _, l := range r.Leftovers {
		if l.PID == pid {
			found = true
			if l.Command != "sleep" {
				t.Errorf("leftover %d is named %q, want %q", pid, l.Command, "sleep")
			}
			if !l.Killed {
				t.Errorf("leftover %d is reported as not killed", pid)
			}
		}
	}
	if !found {
		t.Errorf("the result does not name the leftover process %d: %+v", pid, r.Leftovers)
	}
	if !r.Failed() {
		t.Errorf("a hook that left a process behind must be a failed run: %+v", r)
	}
	if !waitGone(pid, 2*time.Second) {
		t.Errorf("the leftover process %d is still running", pid)
	}
	msgs := strings.Join(r.LeftoverMessages(), "\n")
	want := "hook pre-pre-push left process " + strconv.Itoa(pid) + " (sleep) running after it ended; it was killed"
	if !strings.Contains(msgs, want) {
		t.Errorf("the messages do not say %q:\n%s", want, msgs)
	}
}

// TestHookThatSetsidsASleepAndExitsZeroFails: the child leaves the hook's
// session and process group, so the group signal cannot reach it.
func TestHookThatSetsidsASleepAndExitsZeroFails(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not installed")
	}
	r, pid, elapsed := runLeftoverHook(t,
		"setsid sh -c 'echo $$ > PIDFILE; exec sleep 60' </dev/null >/dev/null 2>&1 &", 30)
	if r.TimedOut {
		t.Fatalf("the hook exited on its own and must not be reported as timed out: %+v", r)
	}
	if !exitedWith(r, 0) {
		t.Errorf("ExitCode is the hook's own status, want 0, got %v", r.ExitCode)
	}
	if elapsed > 10*time.Second {
		t.Errorf("the run took %v; a leftover must be killed, not waited for", elapsed)
	}
	assertLeftoverKilled(t, r, pid)
}

// TestHookThatDoubleForksASleepAndExitsZeroFails: the classic daemon shape --
// the intermediate parent exits at once, so the grandchild is orphaned and
// reparented while the hook is still running.
func TestHookThatDoubleForksASleepAndExitsZeroFails(t *testing.T) {
	r, pid, elapsed := runLeftoverHook(t,
		"( sh -c 'echo $$ > PIDFILE; exec sleep 60' & ) </dev/null >/dev/null 2>&1", 30)
	if r.TimedOut {
		t.Fatalf("the hook exited on its own and must not be reported as timed out: %+v", r)
	}
	if elapsed > 10*time.Second {
		t.Errorf("the run took %v; a leftover must be killed, not waited for", elapsed)
	}
	assertLeftoverKilled(t, r, pid)
}

// TestTimeoutStopsHookWhoseEscapedChildHoldsStdout: a child that left the
// hook's process group (setsid) is out of reach of the group signal and still
// holds stdout. The timeout ends the hook, and the child is then found as a
// leftover, named, killed and reaped -- which is what ends the read.
func TestTimeoutStopsHookWhoseEscapedChildHoldsStdout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timeout test in short mode")
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not installed")
	}
	r, pid, elapsed := runLeftoverHook(t,
		"setsid sh -c 'echo $$ > PIDFILE; exec sleep 60' 2>/dev/null &\n"+
			"while [ ! -s PIDFILE ]; do sleep 0.05; done\necho started\nsleep 60", 1)
	if !r.TimedOut {
		t.Fatalf("expected a timed-out result, got %+v", r)
	}
	// timeout + grace + margin
	if elapsed > 10*time.Second {
		t.Errorf("timeout took too long: %v", elapsed)
	}
	assertLeftoverKilled(t, r, pid)
}

// TestHookThatLeavesNothingHasNoLeftovers: the control -- an ordinary hook
// passes with no leftover named, so the assertions above are not satisfied by
// a sweep that reports everything.
func TestHookThatLeavesNothingHasNoLeftovers(t *testing.T) {
	gitDir := setupGitDir(t)
	restore := SetOutput(&bytes.Buffer{}, &bytes.Buffer{})
	defer restore()
	writeHook(t, filepath.Join(LocalDir(gitDir), "pre-pre-push"), "#!/bin/sh\nsleep 0.1 &\nwait\necho ok\n")

	results, err := Run(context.Background(), store(gitDir), nil, 30, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Failed() || len(results[0].Leftovers) != 0 {
		t.Fatalf("an ordinary hook must pass with nothing left behind: %+v", results)
	}
}

// TestNoZombieRemainsAfterADoubleForkHook: a leftover whose parent was also a
// leftover is reparented to safegit only when that parent exits, so it can
// become safegit's zombie after the scan that reaped the parent -- a /proc
// snapshot reads each process at its own moment, and one read before its
// parent's exit still shows it under that parent. The seam here reports every
// process under the parent it was first seen with, which is that stale read
// made permanent; every leftover must still be reaped before the run returns.
func TestNoZombieRemainsAfterADoubleForkHook(t *testing.T) {
	var mu sync.Mutex
	firstParent := map[int]int{}
	prev := readProcStats
	readProcStats = func() map[int]procStat {
		all := prev()
		mu.Lock()
		defer mu.Unlock()
		for pid, st := range all {
			if ppid, seen := firstParent[pid]; seen {
				st.ppid = ppid
				all[pid] = st
			} else {
				firstParent[pid] = st.ppid
			}
		}
		return all
	}
	defer func() { readProcStats = prev }()

	r, pid, _ := runLeftoverHook(t,
		"sh -c 'sleep 60 & echo $! > PIDFILE; wait' </dev/null >/dev/null 2>&1 &", 30)
	if !r.Failed() || len(r.Leftovers) != 2 {
		t.Errorf("expected the run to fail naming both leftovers (sh and sleep): %+v", r)
	}
	if st, err := readStat(pid); err == nil {
		if st.state == "Z" && st.ppid == os.Getpid() {
			var ws syscall.WaitStatus
			syscall.Wait4(pid, &ws, 0, nil)
			t.Fatalf("leftover %d (the grandchild) is a zombie of this process: it was killed and never reaped", pid)
		}
		t.Errorf("leftover %d is still there in state %s", pid, st.state)
	}
}

// TestAReusedPidIsNeverSignalled: between the scan that finds a leftover and
// the signal that stops it, the leftover can exit and its pid can be given to
// an unrelated process. The seam replays that: the first scan reports a
// leftover at the pid of a process this test started before the hook, with a
// start time that is not that process's. The unrelated process must still be running:
// safegit signals the process it found, never whatever holds the number now.
func TestAReusedPidIsNeverSignalled(t *testing.T) {
	bystander := exec.Command("sleep", "60")
	if err := bystander.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { bystander.Wait(); close(exited) }()
	t.Cleanup(func() { bystander.Process.Kill(); <-exited })
	// Start times count in clock ticks, and a child of this process that
	// started in the same tick as the hook would be attributed to the hook by
	// a real scan too; safegit starts no other process while a hook runs.
	time.Sleep(50 * time.Millisecond)

	prev := readProcStats
	injected := false
	readProcStats = func() map[int]procStat {
		all := prev()
		if !injected {
			injected = true
			all[bystander.Process.Pid] = procStat{comm: "leftover", state: "S", ppid: os.Getpid(), pgrp: 1, ticks: ^uint64(0)}
		}
		return all
	}
	defer func() { readProcStats = prev }()

	gitDir := setupGitDir(t)
	restore := SetOutput(&bytes.Buffer{}, &bytes.Buffer{})
	defer restore()
	writeHook(t, filepath.Join(LocalDir(gitDir), "pre-pre-push"), "#!/bin/sh\nexit 0\n")
	if _, err := Run(context.Background(), store(gitDir), nil, 30, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
		t.Fatalf("the process now holding pid %d was signalled; it is not the leftover the scan found", bystander.Process.Pid)
	case <-time.After(200 * time.Millisecond):
	}
}
