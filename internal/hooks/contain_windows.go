package hooks

import (
	"fmt"
	"github.com/stricttools/safegit/internal/procutil"
	"golang.org/x/sys/windows"
	"time"
	"unsafe"
)

const containmentPartial = false
const uncontainedNote = "Windows job containment failed"

type container struct {
	job       windows.Handle
	root      int
	stopped   []LeftoverProcess
	stopError string
}

func beginContainment() (*container, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("creating hook job: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return &container{job: h}, nil
}
func (c *container) end() { windows.CloseHandle(c.job) }
func (c *container) started(pid int) error {
	c.root = pid
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	if err = windows.AssignProcessToJobObject(c.job, h); err != nil {
		return fmt.Errorf("assigning suspended hook to job: %w", err)
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snap, &entry); err == nil; err = windows.Thread32Next(snap, &entry) {
		if entry.OwnerProcessID != uint32(pid) {
			continue
		}
		thread, e := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if e != nil {
			return e
		}
		_, e = windows.ResumeThread(thread)
		windows.CloseHandle(thread)
		return e
	}
	return fmt.Errorf("cannot locate suspended hook thread: %w", err)
}
func (c *container) terminate() {
	pids, err := c.members()
	if err != nil {
		c.stopError = "cannot enumerate hook job before termination: " + err.Error()
	}
	for _, pid := range pids {
		if pid != c.root && procutil.ProcessAlive(pid) {
			comm, state := describe(pid)
			c.stopped = append(c.stopped, LeftoverProcess{PID: pid, Command: comm, State: state})
		}
	}
	if err := windows.TerminateJobObject(c.job, 1); err != nil {
		c.stopError = "cannot terminate hook job: " + err.Error()
	}
}
func describe(pid int) (string, string) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", ""
	}
	defer windows.CloseHandle(h)
	var buf [32768]uint16
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return "", ""
	}
	return windows.UTF16ToString(buf[:n]), ""
}
func (c *container) members() ([]int, error) {
	// Query a dynamically sized process-ID list, never silently truncate it.
	size := 1024
	for size <= 16*1024*1024 {
		buf := make([]uintptr, size)
		err := windows.QueryInformationJobObject(c.job, windows.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(&buf[0])), uint32(len(buf)*int(unsafe.Sizeof(buf[0]))), nil)
		if err == windows.ERROR_MORE_DATA {
			size *= 2
			continue
		}
		if err != nil {
			return nil, err
		}
		count := *(*uint32)(unsafe.Add(unsafe.Pointer(&buf[0]), 4))
		offset := 8 / int(unsafe.Sizeof(buf[0]))
		if uint64(count)+uint64(offset) > uint64(len(buf)) {
			return nil, fmt.Errorf("invalid job member count")
		}
		pids := make([]int, 0, count)
		for _, p := range buf[offset : offset+int(count)] {
			pids = append(pids, int(p))
		}
		return pids, nil
	}
	return nil, fmt.Errorf("hook job member list exceeds 128 MiB")
}
func (c *container) sweep(clock stopClock, signalled bool) ([]LeftoverProcess, string) {
	pids, err := c.members()
	if err != nil {
		c.terminate()
		return nil, "cannot enumerate hook job: " + err.Error()
	}
	leftovers := append([]LeftoverProcess(nil), c.stopped...)
	seen := map[int]bool{}
	for _, p := range leftovers {
		seen[p.PID] = true
	}
	for _, pid := range pids {
		if pid == c.root || seen[pid] || !procutil.ProcessAlive(pid) {
			continue
		}
		comm, state := describe(pid)
		leftovers = append(leftovers, LeftoverProcess{PID: pid, Command: comm, State: state})
	}
	if len(pids) > 0 {
		if err := windows.TerminateJobObject(c.job, 1); err != nil {
			return leftovers, "cannot terminate hook job: " + err.Error()
		}
	}
	for time.Now().Before(clock.kill) {
		live, e := c.members()
		if e != nil {
			return leftovers, e.Error()
		}
		if len(live) == 0 {
			for i := range leftovers {
				leftovers[i].Killed = true
			}
			return leftovers, c.stopError
		}
		time.Sleep(10 * time.Millisecond)
	}
	return leftovers, "hook job still has active processes at stop deadline"
}
func (c *container) outputHolders(fds []int) ([]LeftoverProcess, string) {
	return nil, "hook output remained open after job termination"
}
