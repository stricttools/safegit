//go:build linux

package hooks

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// On Linux safegit contains a hook completely: while the hook runs, safegit is
// a child subreaper (prctl PR_SET_CHILD_SUBREAPER), so any process the hook
// starts -- including one that leaves the hook's session with setsid, or is
// orphaned by a double fork -- is reparented to safegit rather than to init,
// and stays findable as safegit's descendant.

const (
	prSetChildSubreaper = 36
	prGetChildSubreaper = 37
)

// uncontainedNote ends the message for a process safegit found and could not
// stop.
const uncontainedNote = "safegit could not kill it"

type container struct {
	self int
	// wasSubreaper is the subreaper flag before this hook run, restored at end.
	wasSubreaper bool
	hookPid      int
	// hookTicks is the hook's start time. A process reparented to safegit is
	// attributed to the hook only if it started no earlier: safegit starts no
	// other process while a hook runs, so such a process descends from the hook.
	hookTicks uint64
}

func beginContainment() (*container, error) {
	c := &container{self: os.Getpid()}
	var flag int32
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prGetChildSubreaper, uintptr(unsafe.Pointer(&flag)), 0); errno != 0 {
		return nil, fmt.Errorf("reading the child-subreaper flag: %v", errno)
	}
	c.wasSubreaper = flag != 0
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0); errno != 0 {
		return nil, fmt.Errorf("becoming a child subreaper, which is how safegit keeps track of every process a hook starts: %v", errno)
	}
	return c, nil
}

func (c *container) end() {
	if !c.wasSubreaper {
		syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 0, 0)
	}
}

func (c *container) started(pid int) error {
	st, err := readStat(pid)
	if err != nil {
		return fmt.Errorf("reading the hook process's start time: %w", err)
	}
	c.hookPid = pid
	c.hookTicks = st.ticks
	return nil
}

// sweep stops every process the hook left behind and returns them, named.
//
// signalledGroup says the hook's process group was already signalled (the
// timeout or a cancellation): its members get the grace to act on that signal
// before whatever still runs is counted. Then every live descendant gets
// SIGTERM, SIGKILL after the grace, and is reaped when it is safegit's own
// child. A process that appears while this runs (a leftover forking) is found
// by the next scan and counted too.
func (c *container) sweep(signalledGroup bool) ([]LeftoverProcess, string) {
	if signalledGroup {
		deadline := time.Now().Add(killGrace)
		for time.Now().Before(deadline) {
			live := c.scan()
			if !anyInGroup(live, c.hookPid) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	found := map[int]*LeftoverProcess{}
	var order []int
	record := func(live map[int]procStat) {
		for pid, st := range live {
			if _, ok := found[pid]; !ok {
				found[pid] = &LeftoverProcess{PID: pid, Command: st.comm}
				order = append(order, pid)
			}
		}
	}
	signal := func(live map[int]procStat, sig syscall.Signal) {
		for pid := range live {
			syscall.Kill(pid, sig)
		}
	}

	live := c.scan()
	if len(live) == 0 {
		c.reapZombies()
		return nil, ""
	}
	record(live)
	signal(live, syscall.SIGTERM)

	deadline := time.Now().Add(killGrace)
	for len(live) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		live = c.scan()
		record(live)
		signal(live, syscall.SIGTERM)
	}
	deadline = time.Now().Add(killGrace)
	for len(live) > 0 && time.Now().Before(deadline) {
		signal(live, syscall.SIGKILL)
		time.Sleep(20 * time.Millisecond)
		live = c.scan()
		record(live)
	}

	sort.Ints(order)
	out := make([]LeftoverProcess, 0, len(order))
	for _, pid := range order {
		l := *found[pid]
		_, stillLive := live[pid]
		l.Killed = !stillLive && c.reap(pid)
		out = append(out, l)
	}
	c.reapZombies()
	return out, ""
}

// reap waits for a leftover the sweep stopped, and reports whether it is gone.
//
// A leftover whose parent was also a leftover is reparented to safegit only
// when that parent exits, which can come after the scan that reaped the parent:
// the scan reads each process at its own moment, so the leftover may have been
// read under its parent just before the parent's exit handed it over. Waiting
// for it by pid does not depend on that read. A leftover that is safegit's child
// and has not exited yet is killed here and waited for, up to the grace; one
// that is not safegit's child (ECHILD) was already reaped, or belongs to a
// parent outside safegit's tree that the scans would have reported as live.
func (c *container) reap(pid int) bool {
	deadline := time.Now().Add(killGrace)
	for {
		var ws syscall.WaitStatus
		wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
		if wpid == pid || err != nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		syscall.Kill(pid, syscall.SIGKILL)
		time.Sleep(20 * time.Millisecond)
	}
}

// reapZombies reaps, from a fresh read of the process table, every zombie
// child of safegit that the hook started -- descendants that exited before any
// scan named them and were handed to safegit when their parent exited -- until
// a read finds none left.
func (c *container) reapZombies() {
	for {
		reaped := false
		for pid, st := range readProcStats() {
			if st.ppid != c.self || st.state != "Z" || pid == c.hookPid || st.ticks < c.hookTicks {
				continue
			}
			var ws syscall.WaitStatus
			if wpid, _ := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); wpid == pid {
				reaped = true
			}
		}
		if !reaped {
			return
		}
	}
}

// outputHolders has nothing to name on Linux: every descendant was found by the
// sweep, so a pipe still held after it is held by a process outside safegit's
// tree, which one of the hook's processes handed the descriptor to.
func (c *container) outputHolders(readFds []int) ([]LeftoverProcess, string) {
	return nil, "a process outside safegit's process tree still held the hook's output after every process the hook left was stopped, so safegit stopped reading it"
}

// scan returns the live (not zombie) processes descending from the hook, and
// reaps the zombies among safegit's own children that belong to it.
//
// The roots are safegit's children that started no earlier than the hook: the
// hook's orphans, reparented here. The hook itself is reaped before any scan,
// and its surviving children are orphans by then too.
func (c *container) scan() map[int]procStat {
	all := readProcStats()
	children := map[int][]int{}
	for pid, st := range all {
		children[st.ppid] = append(children[st.ppid], pid)
	}
	var queue []int
	for _, pid := range children[c.self] {
		st := all[pid]
		if pid == c.hookPid || st.ticks < c.hookTicks {
			continue
		}
		if st.state == "Z" {
			var ws syscall.WaitStatus
			syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
			continue
		}
		queue = append(queue, pid)
	}
	live := map[int]procStat{}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		st := all[pid]
		if st.state == "Z" {
			continue
		}
		live[pid] = st
		queue = append(queue, children[pid]...)
	}
	return live
}

func anyInGroup(live map[int]procStat, pgid int) bool {
	for _, st := range live {
		if st.pgrp == pgid {
			return true
		}
	}
	return false
}

// procStat is what the sweep reads from /proc/<pid>/stat.
type procStat struct {
	comm  string
	state string
	ppid  int
	pgrp  int
	ticks uint64
}

// readProcStats is how a scan reads the process table; a test stands in for it
// to replay a stale read.
var readProcStats = readAllStats

func readAllStats() map[int]procStat {
	out := map[int]procStat{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if st, err := readStat(pid); err == nil {
			out[pid] = st
		}
	}
	return out
}

func readStat(pid int) (procStat, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procStat{}, err
	}
	return parseStat(string(raw))
}

// parseStat parses a /proc/<pid>/stat line. Field 2, the command name, is in
// parentheses and may itself contain parentheses and spaces, so the fields
// after it are split at the LAST ')'.
func parseStat(line string) (procStat, error) {
	open := strings.IndexByte(line, '(')
	end := strings.LastIndexByte(line, ')')
	if open < 0 || end < open {
		return procStat{}, fmt.Errorf("malformed stat line")
	}
	fields := strings.Fields(line[end+1:])
	// fields[0] is stat field 3 (state), so field N lives at fields[N-3].
	if len(fields) < 20 {
		return procStat{}, fmt.Errorf("malformed stat line: %d fields after the command name", len(fields))
	}
	ppid, err1 := strconv.Atoi(fields[1])
	pgrp, err2 := strconv.Atoi(fields[2])
	ticks, err3 := strconv.ParseUint(fields[19], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return procStat{}, fmt.Errorf("malformed stat line: unparseable fields")
	}
	return procStat{comm: line[open+1 : end], state: fields[0], ppid: ppid, pgrp: pgrp, ticks: ticks}, nil
}
