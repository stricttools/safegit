package procutil

import (
	"errors"
	"time"
)

// ErrNoStartTime is returned by StartTime when the process start time cannot
// be determined: the platform does not expose it, the PID is gone, or the
// kernel-provided data is unparseable. Callers must fail closed on this error
// (assume the recorded identity still holds) rather than guessing.
var ErrNoStartTime = errors.New("process start time unavailable")

// Start identifies one specific run of a process on the local machine.
//
// A PID alone is not an identity: PIDs are reused. Ticks pins the PID to the
// exact process instance that occupied it, so a recorded (pid, ticks) pair can
// be re-read later and compared to decide whether the original process is
// still the one holding that PID.
type Start struct {
	// Ticks is /proc/<pid>/stat field 22 verbatim: the process's start time in
	// USER_HZ clock ticks since boot. It is fixed for the life of the process
	// and is the ONLY field to compare when deciding whether two readings name
	// the same process instance.
	Ticks uint64

	// Wall is Ticks converted to wall-clock time using the boot time from
	// /proc/stat's btime line. It is informational only -- for humans reading a
	// lock file or an error message. btime is derived from the current clock
	// minus uptime and can shift by a second or more between reads (rounding,
	// NTP steps), so comparing two Wall values would occasionally declare one
	// live process to be two different ones. Never compare it.
	Wall time.Time
}
