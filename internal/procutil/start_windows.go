package procutil

import (
	"golang.org/x/sys/windows"
	"time"
)

// The creation FILETIME identifies a process instance, independently of PID reuse.
func StartTime(pid int) (Start, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return Start{}, err
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return Start{}, err
	}
	return Start{Ticks: uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), Wall: time.Unix(0, created.Nanoseconds()).UTC()}, nil
}
