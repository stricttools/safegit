// Package lock provides ref-lock primitives for concurrent ref updates using O_CREAT|O_EXCL for atomic lock file creation and exponential backoff polling.
// PID liveness checks detect and clean up stale locks left by crashed processes.
package lock

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/procutil"
)

// RefLock represents an acquired lock on a git ref.
type RefLock struct {
	Ref      string
	LockPath string
}

// backoff steps for polling: 10ms, 20ms, 50ms, 100ms, 200ms, 500ms, capped at 1s.
var backoffSteps = []time.Duration{
	10 * time.Millisecond,
	20 * time.Millisecond,
	50 * time.Millisecond,
	100 * time.Millisecond,
	200 * time.Millisecond,
	500 * time.Millisecond,
	1 * time.Second,
}

// lockDir returns the path to the lock directory for a given ref.
// ref is expected to be like "refs/heads/main" -> locks dir is locks/refs/heads/.
// locksBaseDir is the safegit directory that contains the "locks/" subtree;
// for worktrees this should be the shared (common) safegit dir.
func lockDir(locksBaseDir, ref string) string {
	return filepath.Join(locksBaseDir, "locks", filepath.Dir(ref))
}

// lockPath returns the full path to the lock file for a ref.
// e.g. refs/heads/main -> <locksBaseDir>/locks/refs/heads/main.lock
func lockPath(locksBaseDir, ref string) string {
	dir := lockDir(locksBaseDir, ref)
	base := filepath.Base(ref) + ".lock"
	return filepath.Join(dir, base)
}

// Acquire attempts to acquire a lock on the given ref.
// locksBaseDir is the safegit directory whose "locks/" subtree holds lock files;
// for worktrees this should be the shared (common) safegit dir so that all
// worktrees serialize on the same lock. safegitDir is the worktree-local
// safegit dir used for oplog writes (stale-lock recovery events).
// It uses O_CREAT|O_EXCL for atomic creation. If the lock is held by a dead
// process, it is automatically replaced. Uses exponential backoff polling
// bounded by timeout.
func Acquire(locksBaseDir, safegitDir, ref, op string, timeout time.Duration) (*RefLock, error) {
	lp := lockPath(locksBaseDir, ref)

	// Ensure the lock directory exists
	if err := os.MkdirAll(filepath.Dir(lp), 0755); err != nil {
		return nil, fmt.Errorf("creating lock dir: %w", err)
	}

	deadline := time.Now().Add(timeout)
	step := 0

	for {
		err := tryCreate(lp, op)
		if err == nil {
			registerCleanup(lp)
			return &RefLock{Ref: ref, LockPath: lp}, nil
		}

		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("creating lock file: %w", err)
		}

		// Lock file exists -- check if it's stale
		stale, staleErr := IsStale(lp)
		if staleErr == nil && stale {
			// Capture stale holder's PID before removing the lock file
			stalePid, _ := ParsePID(lp)
			os.Remove(lp)
			_ = oplog.Append(safegitDir, oplog.Entry{
				Op: "lock_recovered",
				Extra: map[string]interface{}{
					"ref":      ref,
					"stalePid": stalePid,
				},
			})
			continue
		}

		// Not stale -- wait with backoff
		if time.Now().After(deadline) {
			holder := describeHolder(lp)
			return nil, fmt.Errorf("timeout acquiring lock on %s (held by %s)", ref, holder)
		}

		delay := backoffSteps[step]
		if step < len(backoffSteps)-1 {
			step++
		}
		// Don't sleep past deadline
		remaining := time.Until(deadline)
		if delay > remaining {
			delay = remaining
		}
		time.Sleep(delay)
	}
}

// tryCreate atomically creates the lock file with O_CREAT|O_EXCL and writes owner info.
//
// The owner info includes start=, the holder's process start identity, which is
// what lets a later staleness check tell the original holder apart from an
// unrelated process that inherited its PID. When the platform cannot report a
// start time the field is omitted and reuse detection is simply absent (see
// IsStale). started= is the same instant in human-readable form and carries no
// decision.
func tryCreate(path, op string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	hostname, _ := os.Hostname()
	content := fmt.Sprintf("pid=%d\nts=%s\nop=%s\nhost=%s\n",
		os.Getpid(),
		time.Now().UTC().Format(time.RFC3339Nano),
		op,
		hostname,
	)
	if start, startErr := procutil.StartTime(os.Getpid()); startErr == nil {
		content += fmt.Sprintf("start=%d\n", start.Ticks)
		if !start.Wall.IsZero() {
			content += fmt.Sprintf("started=%s\n", start.Wall.Format(time.RFC3339Nano))
		}
	}
	_, err = f.WriteString(content)
	return err
}

// Release removes the lock file.
func (l *RefLock) Release() error {
	unregisterCleanup(l.LockPath)
	return os.Remove(l.LockPath)
}

// IsStale checks whether the process that holds the lock file is dead.
// Returns (true, nil) if the lock is stale and can be reclaimed.
// A corrupt or zero-length lock file (no parseable PID) is treated as stale.
//
// Reclaiming a lock whose holder is still running lets two operations mutate
// the same ref at once, so every check beyond plain PID liveness must have
// positive evidence before it declares a lock stale:
//   - If the lock contains a host= field that differs from the local hostname,
//     refuse to reclaim (the PID belongs to a different machine's namespace).
//   - PID reuse is decided by comparing the start identity recorded at acquire
//     time against the current start time of whatever now holds that PID: a
//     mismatch means the recorded holder is gone and an unrelated process
//     inherited its PID. Nothing else -- and in particular no file timestamp --
//     is evidence of reuse. When either side of the comparison is missing
//     (no start= field, or a platform that cannot report start times) the check
//     fails closed and the lock is left alone.
func IsStale(path string) (bool, error) {
	fields, err := parseLockFields(path)
	if err != nil {
		// Unreadable lock -- treat as stale
		return true, nil
	}
	pid, err := pidFromFields(fields, path)
	if err != nil {
		// Corrupt lock (e.g. zero-length from a crash mid-create) -- treat as stale
		return true, nil
	}

	// If lock has a host= field and it doesn't match this machine, the PID
	// check is meaningless (different PID namespace on NFS/shared FS).
	if lockHost := fields["host"]; lockHost != "" {
		localHost, hostErr := os.Hostname()
		if hostErr == nil && lockHost != localHost {
			return false, nil
		}
	}

	if !procutil.ProcessAlive(pid) {
		return true, nil // process does not exist
	}

	return pidWasReused(pid, fields["start"]), nil
}

// pidWasReused reports whether the live process now occupying pid is a
// different process instance than the one that recorded recordedStart when it
// took the lock. Any inability to compare returns false: without evidence of
// reuse the holder is assumed to be alive and the lock stays.
func pidWasReused(pid int, recordedStart string) bool {
	if recordedStart == "" {
		return false
	}
	recorded, err := strconv.ParseUint(recordedStart, 10, 64)
	if err != nil {
		return false
	}
	current, err := procutil.StartTime(pid)
	if err != nil {
		return false
	}
	return current.Ticks != recorded
}

// ForceRelease unconditionally removes the lock file for a ref.
// locksBaseDir is the safegit directory whose "locks/" subtree holds lock files;
// for worktrees this should be the shared (common) safegit dir.
func ForceRelease(locksBaseDir, ref string) error {
	lp := lockPath(locksBaseDir, ref)
	err := os.Remove(lp)
	if os.IsNotExist(err) {
		return fmt.Errorf("no lock held on %s", ref)
	}
	return err
}

// parseLockFields reads a lock file's key=value lines. Every reader of a lock
// file goes through it, so the file's format is described in exactly one place.
// Unknown keys are kept; a line without '=' is ignored.
func parseLockFields(lockPath string) (map[string]string, error) {
	f, err := os.Open(lockPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fields := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if ok {
			fields[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return fields, nil
}

// pidFromFields extracts the pid from already-parsed lock fields.
func pidFromFields(fields map[string]string, lockPath string) (int, error) {
	pidStr, ok := fields["pid"]
	if !ok {
		return 0, fmt.Errorf("no pid= line in lock file %s", lockPath)
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return 0, fmt.Errorf("invalid pid in lock file: %q", pidStr)
	}
	return pid, nil
}

// ParsePID reads the lock file and extracts the pid= value.
func ParsePID(lockPath string) (int, error) {
	fields, err := parseLockFields(lockPath)
	if err != nil {
		return 0, err
	}
	return pidFromFields(fields, lockPath)
}

// describeHolder returns a human-readable description of the lock holder.
func describeHolder(lockPath string) string {
	fields, err := parseLockFields(lockPath)
	if err != nil {
		return "unknown"
	}
	return fmt.Sprintf("pid=%s op=%s host=%s started=%s",
		fields["pid"], fields["op"], fields["host"], fields["started"])
}
