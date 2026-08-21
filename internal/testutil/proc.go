package testutil

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// PathologicalCommName is an executable basename that reproduces the
// /proc/<pid>/stat parsing trap: the kernel writes the comm field unescaped
// between parentheses, so a name containing spaces and parentheses shifts
// every subsequent field for any parser that splits on whitespace. It is
// exactly 15 bytes, the largest comm Linux stores without truncation, so the
// spawned process's comm is this string verbatim.
const PathologicalCommName = "sg (weird) name"

// Process is a child process spawned by a test, used by suites that need a
// PID whose liveness they control.
type Process struct {
	// Pid is the child's process id.
	Pid int
	// Comm is the child's /proc/<pid>/comm contents, read after exec.
	Comm string

	cmd    *exec.Cmd
	reaped bool
}

// Kill terminates the process and reaps it, so the PID is genuinely dead (not
// a zombie) when Kill returns. It is safe to call more than once.
func (p *Process) Kill(t *testing.T) {
	t.Helper()
	if p.reaped {
		return
	}
	p.reaped = true
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("killing test process %d: %v", p.Pid, err)
	}
	// Wait reports the kill signal as an error; only the reaping is wanted.
	_ = p.cmd.Wait()
}

// SpawnSleeper starts a long-lived child process and returns a handle to it.
// The process is killed when the test ends unless the test kills it earlier.
func SpawnSleeper(t *testing.T) *Process {
	t.Helper()
	return spawnSleeperNamed(t, "safegit-test-sleeper")
}

// SpawnPathologicalNameSleeper starts a long-lived child process whose
// executable basename -- and therefore its /proc comm field -- is
// PathologicalCommName.
func SpawnPathologicalNameSleeper(t *testing.T) *Process {
	t.Helper()
	return spawnSleeperNamed(t, PathologicalCommName)
}

// spawnSleeperNamed execs the system `sleep` through a symlink named basename,
// which is what gives the child an arbitrary comm: Linux derives comm from the
// basename of the path passed to execve, not from the resolved target.
func spawnSleeperNamed(t *testing.T, basename string) *Process {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary available to spawn a controllable process: %v", err)
	}
	link := filepath.Join(t.TempDir(), basename)
	if err := os.Symlink(sleep, link); err != nil {
		t.Fatalf("symlinking %s to %s: %v", link, sleep, err)
	}

	cmd := exec.Command(link, "600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", link, err)
	}
	p := &Process{Pid: cmd.Process.Pid, cmd: cmd}
	t.Cleanup(func() { p.Kill(t) })

	if runtime.GOOS == "linux" {
		raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(p.Pid), "comm"))
		if err != nil {
			t.Fatalf("reading comm of pid %d: %v", p.Pid, err)
		}
		p.Comm = strings.TrimSuffix(string(raw), "\n")
	}
	return p
}
