//go:build !linux

package hooks

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Off Linux (macOS) there is no child subreaper, so containment is partial: a
// process that leaves the hook's process group (setsid, a double fork into a
// new group) is reparented to init and safegit cannot find it by ancestry.
// What safegit does instead: it stops whatever is still in the hook's process
// group, and names every process still holding the hook's output pipes, which
// it cannot stop, from lsof.

// uncontainedNote ends the message for a process safegit could not stop.
const uncontainedNote = "safegit cannot contain detached processes on macOS"

// runLsof and runPs are the seams the tests of the parsers stand in for.
var (
	runLsof = func() ([]byte, error) { return exec.Command("lsof", "-n", "-P", "-F", "pcfdDn").Output() }
	runPs   = func() ([]byte, error) {
		return exec.Command("ps", "-A", "-o", "pid=", "-o", "pgid=", "-o", "comm=").Output()
	}
)

type container struct {
	self int
	pgid int
}

func beginContainment() (*container, error) {
	return &container{self: os.Getpid()}, nil
}

func (c *container) end() {}

func (c *container) started(pid int) error {
	c.pgid = pid
	return nil
}

// groupAlive reports whether any process is still in the hook's group.
func (c *container) groupAlive() bool {
	return syscall.Kill(-c.pgid, 0) == nil
}

// sweep stops what is left in the hook's process group and names it.
func (c *container) sweep(signalledGroup bool) ([]LeftoverProcess, string) {
	if signalledGroup {
		deadline := time.Now().Add(killGrace)
		for c.groupAlive() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !c.groupAlive() {
		return nil, ""
	}

	var members []LeftoverProcess
	unknown := ""
	out, err := runPs()
	if err != nil {
		unknown = "ps, which lists the hook's process group, failed: " + err.Error()
	} else {
		members = parsePsGroup(out, c.pgid)
	}

	syscall.Kill(-c.pgid, syscall.SIGTERM)
	deadline := time.Now().Add(killGrace)
	for c.groupAlive() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	deadline = time.Now().Add(killGrace)
	for c.groupAlive() && time.Now().Before(deadline) {
		syscall.Kill(-c.pgid, syscall.SIGKILL)
		time.Sleep(20 * time.Millisecond)
	}

	for i := range members {
		members[i].Killed = errors.Is(syscall.Kill(members[i].PID, 0), syscall.ESRCH)
	}
	if len(members) == 0 && unknown == "" && c.groupAlive() {
		unknown = "processes remained in the hook's process group and ps did not list them"
	}
	return members, unknown
}

// outputHolders names the processes still holding the hook's output pipes.
func (c *container) outputHolders(readFds []int) ([]LeftoverProcess, string) {
	return pipeHolders(runLsof, c.self, readFds)
}
