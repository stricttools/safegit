//go:build !linux

package procutil

import "fmt"

// StartTime is unavailable off Linux: there is no /proc/<pid>/stat to read a
// process start time from. Callers fail closed on ErrNoStartTime, so PID-reuse
// detection is simply absent on these platforms rather than guessed at.
func StartTime(pid int) (Start, error) {
	return Start{}, fmt.Errorf("%w: no /proc on this platform", ErrNoStartTime)
}
