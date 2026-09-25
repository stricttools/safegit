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

	// The pidfd system calls have one number on every Linux architecture.
	sysPidfdSendSignal = 424
	sysPidfdOpen       = 434
)

// uncontainedNote ends the message for a process safegit found and could not
// stop.
const uncontainedNote = "safegit could not kill it"

// containmentPartial is false: every process a hook starts stays findable.
const containmentPartial = false

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
	// Every signal the sweep sends goes through a pidfd, so the kernel has to
	// have them (Linux 5.3); asked of safegit's own process before the hook
	// starts, so a kernel without them refuses the run instead of leaving the
	// sweep unable to stop anything. There is no fallback to bare pids: a pid
	// can be reused by an unrelated process between a scan and a signal.
	fd, errno := openPidfd(c.self)
	if errno != 0 {
		return nil, fmt.Errorf("this system has no pidfd support (pidfd_open: %v); safegit signals the processes a hook leaves behind only through pidfds, "+
			"so running a pre-pre-push hook needs Linux 5.3 or later", errno)
	}
	syscall.Close(fd)
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
//
// Every signal goes through a pidfd opened when the process is found and
// checked against the start time the scan read, never through the bare pid: a
// leftover that exits between a scan and a signal can have its pid handed to an
// unrelated process, and a pidfd can only ever reach the process it was opened
// on.
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
	handles := map[int]pidHandle{}
	defer func() {
		for _, h := range handles {
			syscall.Close(h.fd)
		}
	}()
	var order []int
	record := func(live map[int]procStat) {
		for pid, st := range live {
			if _, ok := found[pid]; !ok {
				found[pid] = &LeftoverProcess{PID: pid, Command: st.comm}
				order = append(order, pid)
			}
			if h, ok := handles[pid]; ok {
				if h.ticks == st.ticks {
					continue
				}
				syscall.Close(h.fd)
				delete(handles, pid)
			}
			if fd, ok := openHandle(pid, st.ticks); ok {
				handles[pid] = pidHandle{fd: fd, ticks: st.ticks}
			}
		}
	}
	signal := func(live map[int]procStat, sig syscall.Signal) {
		for pid, st := range live {
			if h, ok := handles[pid]; ok && h.ticks == st.ticks {
				pidfdSendSignal(h.fd, sig)
			}
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
		h, hasHandle := handles[pid]
		l.Killed = !stillLive && c.reap(pid, h, hasHandle)
		out = append(out, l)
	}
	c.reapZombies()
	return out, ""
}

// pidHandle is a pidfd on one process the sweep found, with the start time the
// scan read for it.
type pidHandle struct {
	fd    int
	ticks uint64
}

// openHandle opens a pidfd on the process a scan found at pid with start time
// ticks. It reports false when that process is gone: the open found no process
// at pid, or found one that started at another time -- the pid was reused --
// in which case the pidfd is closed without ever being used. A pidfd opened
// before the start-time check refers to the checked process for good, so a
// check that passes leaves no window for reuse.
func openHandle(pid int, ticks uint64) (int, bool) {
	fd, errno := pidfdOpen(pid)
	if errno != 0 {
		return -1, false
	}
	if st, err := readStat(pid); err != nil || st.ticks != ticks {
		syscall.Close(fd)
		return -1, false
	}
	return fd, true
}

// openPidfd is how beginContainment asks for pidfd support; a test stands in
// for it to play a kernel that has none.
var openPidfd = pidfdOpen

func pidfdOpen(pid int) (int, syscall.Errno) {
	fd, _, errno := syscall.Syscall(sysPidfdOpen, uintptr(pid), 0, 0)
	return int(fd), errno
}

func pidfdSendSignal(fd int, sig syscall.Signal) syscall.Errno {
	_, _, errno := syscall.Syscall6(sysPidfdSendSignal, uintptr(fd), uintptr(sig), 0, 0, 0, 0)
	return errno
}

// reap waits for a leftover the sweep stopped, and reports whether it is gone.
//
// A leftover whose parent was also a leftover is reparented to safegit only
// when that parent exits, which can come after the scan that reaped the parent:
// the scan reads each process at its own moment, so the leftover may have been
// read under its parent just before the parent's exit handed it over. Waiting
// for it by pid does not depend on that read, and is safe from reuse: a pid
// wait4 answers for is safegit's own child, whose pid is not free until it is
// reaped. A leftover that is safegit's child and has not exited yet is killed
// through its pidfd and waited for, up to the grace; one that is not safegit's
// child (ECHILD) was already reaped, or belongs to a parent outside safegit's
// tree that the scans would have reported as live. Without a pidfd -- the
// process was gone by the time it was found -- nothing is signalled.
func (c *container) reap(pid int, h pidHandle, hasHandle bool) bool {
	deadline := time.Now().Add(killGrace)
	for {
		var ws syscall.WaitStatus
		wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
		if wpid == pid || err != nil {
			return true
		}
		if !hasHandle || time.Now().After(deadline) {
			return false
		}
		pidfdSendSignal(h.fd, syscall.SIGKILL)
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
