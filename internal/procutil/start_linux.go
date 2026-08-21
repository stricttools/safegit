//go:build linux

package procutil

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// userHZ is the clock-tick rate the kernel uses when reporting times in
// /proc/<pid>/stat. It is the ABI-fixed USER_HZ (100) on every architecture
// safegit targets, NOT the kernel's internal CONFIG_HZ. sysconf(_SC_CLK_TCK)
// would report the same value, but safegit builds with CGO_ENABLED=0 and
// cannot call it.
const userHZ = 100

// StartTime reads the start time of the process with the given PID from
// /proc/<pid>/stat. It returns ErrNoStartTime when /proc is unavailable, the
// process is gone, or the kernel data cannot be parsed.
func StartTime(pid int) (Start, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return Start{}, fmt.Errorf("%w: reading /proc/%d/stat: %v", ErrNoStartTime, pid, err)
	}
	ticks, err := parseStartTicks(string(raw))
	if err != nil {
		return Start{}, fmt.Errorf("%w: %v", ErrNoStartTime, err)
	}

	s := Start{Ticks: ticks}
	if boot, err := bootTime(); err == nil {
		s.Wall = boot.Add(time.Duration(ticks) * time.Second / userHZ)
	}
	return s, nil
}

// parseStartTicks extracts field 22 (starttime, in USER_HZ ticks since boot)
// from a /proc/<pid>/stat line.
//
// Field 2 is the executable name in parentheses and the kernel does NOT escape
// it: a process named "sg (weird) name" produces "(sg (weird) name)", so
// splitting the line on whitespace mis-indexes every field after it. The only
// correct split point is the LAST ')' in the line -- everything after it is
// field 3 onward, in fixed positions.
func parseStartTicks(line string) (uint64, error) {
	commEnd := strings.LastIndex(line, ")")
	if commEnd < 0 {
		return 0, fmt.Errorf("malformed stat line: no ')' terminating the comm field")
	}
	fields := strings.Fields(line[commEnd+1:])
	// fields[0] is stat field 3 (state), so field N lives at fields[N-3].
	const startTimeField = 22
	if len(fields) < startTimeField-2 {
		return 0, fmt.Errorf("malformed stat line: %d fields after comm, need at least %d", len(fields), startTimeField-2)
	}
	ticks, err := strconv.ParseUint(fields[startTimeField-3], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("malformed stat line: unparseable starttime %q", fields[startTimeField-3])
	}
	return ticks, nil
}

// bootTime reads the system boot time from /proc/stat's btime line.
func bootTime() (time.Time, error) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		rest, ok := strings.CutPrefix(line, "btime ")
		if !ok {
			continue
		}
		secs, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("unparseable btime %q", strings.TrimSpace(rest))
		}
		return time.Unix(secs, 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("no btime line in /proc/stat")
}
