// Package lock provides ref-lock primitives for concurrent ref updates using atomic lock file creation (link(2) of a fully-written temporary sibling) and exponential backoff polling.
// PID liveness checks detect and clean up stale locks left by crashed processes.
package lock

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/procutil"
)

// The tool-owned pseudo-refs safegit locks under. They are not git refs and
// nothing ever resolves them as such: they are names in the same namespace as
// a ref so that one lock implementation serves both, and `safegit unlock`
// addresses them by these exact strings.
const (
	// RewriteRef is the repository-wide history-rewrite lock. It lives in the
	// SHARED safegit directory, so every worktree of the repository contends on
	// the same file: a rewrite changes object names for all of them. Taken by
	// scrub file, scrub match, scrub run and author rewrite.
	RewriteRef = "safegit/rewrite"

	// OperationRef is the worktree operation lock. It lives in the
	// WORKTREE-LOCAL safegit directory, so two worktrees of the same repository
	// operate independently while two processes in one worktree serialize.
	// Taken by every command that mutates this worktree: the guarded
	// passthroughs (checkout, pull, merge, rebase, reset, bisect, cherry-pick,
	// revert) and the commit pipeline's three entry points plus undo.
	//
	// It is what makes a commit's in-flight-operation check meaningful. Without
	// it a passthrough could create sequencer state (a conflicted merge, a
	// stopped cherry-pick) in the window between a commit reading that state and
	// updating the ref, and the commit would build its tree against a repository
	// git considers mid-operation.
	OperationRef = "safegit/operation"
)

// Lock ordering, declared once for the whole tool.
//
// safegit takes at most two locks at a time, and always in this order:
//
//  1. OperationRef -- the worktree operation lock, OUTERMOST. A command that
//     takes it takes it first and holds it for its whole operation.
//  2. a per-ref lock (the commit pipeline's CAS lock on refs/heads/<branch>,
//     undo's lock on the same) or RewriteRef -- INSIDE.
//
// Nothing ever takes them the other way round, and nothing takes two locks at
// the same level. That is the entire deadlock argument: a total order over the
// two levels, with no cycle to close.
//
// The one consequence worth stating out loud: a passthrough holds the operation
// lock for the FULL duration of the git command it wraps, including an
// interactive `rebase -i`'s editor session. A second safegit process in the same
// worktree waits (lock.acquireTimeoutSeconds) and then refuses with
// exitcode.LockTimeout rather than running concurrently with a rebase.
//
// That editor does run -- verified, not assumed, by
// testdata/experiments/exp-passthrough-editor-stdin.sh. The passthroughs that go
// through the effects handle (checkout, pull, merge, rebase, reset, bisect) give
// their git child /dev/null on stdin, so an editor that opens /dev/tty -- vim,
// nano, emacs -nw, every terminal editor -- works, while anything reading stdin
// sees EOF at once. The passthroughs that exec git directly (cherry-pick,
// revert) inherit stdin whole and have neither limitation.

// RefLock represents an acquired lock on a git ref.
type RefLock struct {
	Ref      string
	LockPath string
}

// TimeoutError is what Acquire returns when the timeout expired with a live
// holder still owning the lock. It is a distinct type because the exit code
// safegit reports for that outcome (exitcode.LockTimeout) is distinct: a
// caller decides between "someone else is working here, wait or investigate"
// and every other reason a lock could not be taken by asking errors.As for
// this type, never by matching the message text.
//
// The message names the ref and the holder record, so a caller that surfaces
// the error verbatim tells the operator which process to look at.
type TimeoutError struct {
	// Ref is the ref (or the tool-owned pseudo-ref, e.g. safegit/rewrite)
	// whose lock could not be taken.
	Ref string
	// Holder is the lock file's owner record as describeHolder renders it.
	Holder string
	// Timeout is how long Acquire waited before giving up.
	Timeout time.Duration
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("timeout acquiring lock on %s (held by %s)", e.Ref, e.Holder)
}

// IsTimeout reports whether err is, or wraps, a lock acquisition timeout.
func IsTimeout(err error) bool {
	var te *TimeoutError
	return errors.As(err, &te)
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

// lockSuffix is the extension every lock file carries. A file under a locks/
// subtree is a lock if and only if its name ends in this.
const lockSuffix = ".lock"

// lockPath returns the full path to the lock file for a ref.
// e.g. refs/heads/main -> <locksBaseDir>/locks/refs/heads/main.lock
func lockPath(locksBaseDir, ref string) string {
	dir := lockDir(locksBaseDir, ref)
	base := filepath.Base(ref) + lockSuffix
	return filepath.Join(dir, base)
}

// Path returns where the lock file for ref lives under locksBaseDir. Callers
// that inspect or remove a lock by name -- `safegit unlock`, doctor -- resolve
// it here rather than rebuilding the path themselves.
func Path(locksBaseDir, ref string) string { return lockPath(locksBaseDir, ref) }

// NameFromPath is Path's inverse: it turns an absolute lock-file path under
// locksBaseDir back into the ref (or pseudo-ref) it locks, for display. A path
// outside that subtree, or one that is not a lock file, yields "".
func NameFromPath(locksBaseDir, path string) string {
	rel, err := filepath.Rel(filepath.Join(locksBaseDir, "locks"), path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	if !IsLockFile(filepath.Base(rel)) {
		return ""
	}
	return filepath.ToSlash(strings.TrimSuffix(rel, lockSuffix))
}

// Acquire attempts to acquire a lock on the given ref.
// locksBaseDir is the safegit directory whose "locks/" subtree holds lock files;
// for worktrees this should be the shared (common) safegit dir so that all
// worktrees serialize on the same lock. safegitDir is the worktree-local
// safegit dir used for oplog writes (stale-lock recovery events).
// Creation is atomic, so exactly one caller wins it. If the lock is held by a
// dead process, it is automatically replaced -- see the reclamation rules in
// reclaim.go, which are what keep two contenders facing the same stale lock
// from both deciding they reclaimed it. Uses exponential backoff polling
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

		// Lock file exists -- reclaim it only if its holder is genuinely gone.
		//
		// IsStale here is a cheap pre-filter that keeps the common contended
		// case (a live holder) off the flock path entirely. It is NOT what
		// authorizes the removal: two contenders can both pass it on the same
		// stale lock, and if both then removed the path, the second would
		// delete the fresh lock the first had already created and both would
		// believe they held the ref. The judgement that authorizes removal is
		// re-made inside reclaimLocked, under the lock file's own flock and
		// against the descriptor's inode.
		if IsStale(lp) {
			f, outcome := openForReclaim(lp)
			if f != nil {
				var stalePid int
				outcome, stalePid = reclaimLocked(f, lp)
				if outcome == reclaimDone {
					_ = oplog.Append(safegitDir, oplog.Entry{
						Op: "lock_recovered",
						Extra: map[string]interface{}{
							"ref":      ref,
							"stalePid": stalePid,
						},
					})
					continue
				}
			}
			// Nothing was removed. A restart is worth taking immediately only
			// while there is still time; past the deadline fall through to the
			// timeout below rather than looping.
			if outcome == reclaimRestart && time.Now().Before(deadline) {
				continue
			}
		}

		// Not stale -- wait with backoff
		if time.Now().After(deadline) {
			return nil, &TimeoutError{Ref: ref, Holder: describeHolder(lp), Timeout: timeout}
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

// tryCreate creates the lock file with its owner record already in it, failing
// with os.ErrExist when someone else holds the lock.
//
// The record is written to a temporary sibling and published with link(2)
// rather than written in place after an O_CREAT|O_EXCL open. Both give the same
// atomic "one winner" property against a competing creator, but only the link
// makes the file's CONTENT atomic too: with the in-place form the file exists,
// empty, for the instant between the open and the write, and a contender that
// reads it in that instant finds no pid= line, judges the lock a crashed
// holder's leftover, and removes a lock whose owner is very much alive. link(2)
// publishes a file that is already complete, so no reader ever sees a half-made
// lock.
//
// The owner record includes start=, the holder's process start identity, which
// is what lets a later staleness check tell the original holder apart from an
// unrelated process that inherited its PID. When the platform cannot report a
// start time the field is omitted and reuse detection is simply absent (see
// IsStale). started= is the same instant in human-readable form and carries no
// decision.
func tryCreate(path, op string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), publicationTempPrefix(filepath.Base(path)))
	if err != nil {
		return fmt.Errorf("creating temporary lock file: %w", err)
	}
	tmpName := tmp.Name()
	// The temp name is unlinked whichever way this goes: on success the lock
	// lives at path under its own link, on failure nothing is left behind.
	defer os.Remove(tmpName)

	if err := writeLockContent(tmp, op); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	// link(2) fails with EEXIST when path already exists, which is the same
	// atomic "one winner" property O_CREAT|O_EXCL gives, and Acquire reads that
	// error as "someone else holds it".
	return os.Link(tmpName, path)
}

// publicationTempInfix is what tryCreate appends to a lock's own base name to
// build the temporary sibling it publishes that lock from. os.CreateTemp then
// appends random digits, so a temp name can never end in lockSuffix.
const publicationTempInfix = ".tmp-"

// publicationTempPrefix is the name prefix tryCreate gives that temporary
// sibling: a leading dot (so the file is hidden and is not a lock by name)
// followed by the lock's own base name and the infix.
//
// It exists so the writer and every reader agree on one spelling: doctor's lock
// walk must never mistake one of these for a lock, and doctor's cleanup must be
// able to recognize one that a kill between creation and publication left
// behind.
func publicationTempPrefix(lockBase string) string {
	return "." + lockBase + publicationTempInfix
}

// IsLockFile reports whether name (a bare file name, not a path) names a lock.
// It is the predicate every scan of a locks/ subtree uses.
func IsLockFile(name string) bool {
	return strings.HasSuffix(name, lockSuffix) && !IsPublicationTemp(name)
}

// IsPublicationTemp reports whether name (a bare file name, not a path) is a
// lock-publication temporary sibling rather than a lock.
func IsPublicationTemp(name string) bool {
	return strings.HasPrefix(name, ".") && strings.Contains(name, lockSuffix+publicationTempInfix)
}

// writeLockContent writes the owner record into an open lock file.
func writeLockContent(f *os.File, op string) error {
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
	_, err := f.WriteString(content)
	return err
}

// Release removes the lock file.
func (l *RefLock) Release() error {
	unregisterCleanup(l.LockPath)
	return os.Remove(l.LockPath)
}

// IsStale reports whether the process that holds the lock file is dead, which
// is the only condition under which the lock may be reclaimed. An unreadable,
// corrupt or zero-length lock file (no parseable PID) is stale: it is what a
// crash mid-create leaves behind.
//
// There is no error return. Every condition this function can meet is already
// a verdict -- a lock it cannot read is stale, a comparison it cannot make
// fails closed and the lock is left alone -- so a caller has nothing to decide
// from an error that the boolean does not already say.
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
func IsStale(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		// Unreadable lock -- treat as stale
		return true
	}
	defer f.Close()

	stale, _ := staleFile(f, path)
	return stale
}

// staleFile is IsStale's judgement applied to an already-open lock file,
// returning the verdict and the holder's pid (0 when the file names none).
//
// Reading through the descriptor instead of re-opening path is what lets a
// reclaimer judge the exact file it holds open: between opening a lock file and
// deciding to remove it, another contender may have unlinked that file and
// created a different one at the same path, and a fresh read of the path would
// then judge -- and condemn -- the wrong file. See reclaimLocked.
func staleFile(f *os.File, path string) (bool, int) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return true, 0
	}
	fields, err := readLockFields(f)
	if err != nil {
		// Unreadable lock -- treat as stale
		return true, 0
	}
	pid, err := pidFromFields(fields, path)
	if err != nil {
		// Corrupt lock (e.g. zero-length from a crash mid-create) -- treat as stale
		return true, 0
	}
	return staleFromFields(fields, pid), pid
}

// staleFromFields decides staleness from already-parsed lock fields and the
// pid they name.
func staleFromFields(fields map[string]string, pid int) bool {
	// If lock has a host= field and it doesn't match this machine, the PID
	// check is meaningless (different PID namespace on NFS/shared FS).
	if lockHost := fields["host"]; lockHost != "" {
		localHost, hostErr := os.Hostname()
		if hostErr == nil && lockHost != localHost {
			return false
		}
	}

	if !procutil.ProcessAlive(pid) {
		return true // process does not exist
	}

	return pidWasReused(pid, fields["start"])
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
	return readLockFields(f)
}

// readLockFields parses lock-file lines from an already-open source, so a
// reader that holds a descriptor can parse the file it holds rather than
// whatever the path names by the time it looks again.
func readLockFields(r io.Reader) (map[string]string, error) {
	fields := make(map[string]string)
	scanner := bufio.NewScanner(r)
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
