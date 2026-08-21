package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/smm-h/safegit/internal/procutil"
	"github.com/smm-h/safegit/internal/testutil"
)

// plantLock writes a lock file in the format tryCreate produces. start is the
// recorded start identity; the empty string omits the field entirely, which is
// what a lock written on a platform without /proc looks like.
func plantLock(t *testing.T, path string, pid int, start string) {
	t.Helper()
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	content := fmt.Sprintf("pid=%d\nts=%s\nop=test\nhost=%s\n",
		pid, time.Now().UTC().Format(time.RFC3339Nano), host)
	if start != "" {
		content += "start=" + start + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing lock file: %v", err)
	}
}

// trueStart reads the running process's real start identity, the value a lock
// file records for its holder.
func trueStart(t *testing.T, pid int) string {
	t.Helper()
	s, err := procutil.StartTime(pid)
	if err != nil {
		t.Skipf("cannot read start time of pid %d: %v", pid, err)
	}
	return strconv.FormatUint(s.Ticks, 10)
}

func setMtime(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("setting mtime of %s: %v", path, err)
	}
}

// TestIsStaleLiveHolderNeverStale is the core regression pin for the
// lock-stealing bug: staleness must depend on the holder's identity alone, so
// no arrangement of file timestamps can make a live holder's lock reclaimable.
// The previous implementation compared the lock's mtime against the mtime of
// the /proc/<pid> directory, which advances during a process's life, and
// therefore deleted live holders' locks.
func TestIsStaleLiveHolderNeverStale(t *testing.T) {
	proc := testutil.SpawnSleeper(t)
	lp := filepath.Join(t.TempDir(), "main.lock")
	plantLock(t, lp, proc.Pid, trueStart(t, proc.Pid))

	ages := map[string]time.Duration{
		"just written":        0,
		"an hour old":         time.Hour,
		"thirty days old":     30 * 24 * time.Hour,
		"dated in the future": -time.Hour,
	}
	for name, age := range ages {
		t.Run(name, func(t *testing.T) {
			setMtime(t, lp, time.Now().Add(-age))
			stale, err := IsStale(lp)
			if err != nil {
				t.Fatalf("IsStale: %v", err)
			}
			if stale {
				t.Errorf("lock held by live pid %d reported stale (mtime %s)", proc.Pid, name)
			}
		})
	}
}

// TestIsStaleDeadHolderStale keeps the reclamation path working: a holder that
// really exited leaves a lock anyone may take.
func TestIsStaleDeadHolderStale(t *testing.T) {
	proc := testutil.SpawnSleeper(t)
	lp := filepath.Join(t.TempDir(), "main.lock")
	plantLock(t, lp, proc.Pid, trueStart(t, proc.Pid))

	proc.Kill(t)

	stale, err := IsStale(lp)
	if err != nil {
		t.Fatalf("IsStale: %v", err)
	}
	if !stale {
		t.Errorf("lock held by dead pid %d should be stale", proc.Pid)
	}
}

// TestIsStalePidReuseStale simulates PID reuse: the recorded PID is alive, but
// it is a different process instance than the one that took the lock, which
// the recorded start identity reveals.
func TestIsStalePidReuseStale(t *testing.T) {
	proc := testutil.SpawnSleeper(t)
	live := trueStart(t, proc.Pid)
	ticks, err := strconv.ParseUint(live, 10, 64)
	if err != nil {
		t.Fatalf("parsing start ticks %q: %v", live, err)
	}

	lp := filepath.Join(t.TempDir(), "main.lock")
	// The holder started one tick earlier than whatever occupies the PID now:
	// same PID, different process.
	plantLock(t, lp, proc.Pid, strconv.FormatUint(ticks-1, 10))

	stale, err := IsStale(lp)
	if err != nil {
		t.Fatalf("IsStale: %v", err)
	}
	if !stale {
		t.Errorf("lock whose recorded start does not match live pid %d should be stale (PID reuse)", proc.Pid)
	}
}

// TestIsStaleMissingStartFailsClosed pins the fail-closed stance: with no
// recorded identity there is no evidence of reuse, and a live PID keeps its
// lock rather than losing it to a guess.
func TestIsStaleMissingStartFailsClosed(t *testing.T) {
	proc := testutil.SpawnSleeper(t)
	lp := filepath.Join(t.TempDir(), "main.lock")
	plantLock(t, lp, proc.Pid, "")
	setMtime(t, lp, time.Now().Add(-24*time.Hour))

	stale, err := IsStale(lp)
	if err != nil {
		t.Fatalf("IsStale: %v", err)
	}
	if stale {
		t.Error("lock without a recorded start identity must not be stale while its PID is alive")
	}
}

func TestIsStaleUnparseableStartFailsClosed(t *testing.T) {
	proc := testutil.SpawnSleeper(t)
	lp := filepath.Join(t.TempDir(), "main.lock")
	plantLock(t, lp, proc.Pid, "not-a-number")

	stale, err := IsStale(lp)
	if err != nil {
		t.Fatalf("IsStale: %v", err)
	}
	if stale {
		t.Error("lock with an unparseable start identity must not be stale while its PID is alive")
	}
}

// The pathological-comm holder case lives in staleness_linux_test.go: it reads
// a child's /proc comm, which only linux reports.

// TestAcquireRecordsStartIdentity checks the other half of the contract: the
// identity a staleness check needs is written when the lock is taken.
func TestAcquireRecordsStartIdentity(t *testing.T) {
	sgDir := setupSafegitDir(t)
	lk, err := Acquire(sgDir, sgDir, "refs/heads/main", "commit", 5*time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lk.Release()

	raw, err := os.ReadFile(lk.LockPath)
	if err != nil {
		t.Fatalf("reading lock file: %v", err)
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			fields[k] = v
		}
	}

	want := trueStart(t, os.Getpid())
	if fields["start"] != want {
		t.Errorf("start = %q, want %q", fields["start"], want)
	}
	if _, err := time.Parse(time.RFC3339Nano, fields["started"]); err != nil {
		t.Errorf("started = %q, which is not an RFC3339 time: %v", fields["started"], err)
	}
}

// TestAcquireDoesNotStealLiveLock is the end-to-end form of the regression: a
// second acquirer facing an old-looking lock file whose holder is alive must
// wait and time out, never reclaim.
func TestAcquireDoesNotStealLiveLock(t *testing.T) {
	sgDir := setupSafegitDir(t)
	ref := "refs/heads/main"
	proc := testutil.SpawnSleeper(t)

	lp := lockPath(sgDir, ref)
	if err := os.MkdirAll(filepath.Dir(lp), 0755); err != nil {
		t.Fatalf("creating lock dir: %v", err)
	}
	plantLock(t, lp, proc.Pid, trueStart(t, proc.Pid))
	setMtime(t, lp, time.Now().Add(-2*time.Hour))

	if _, err := Acquire(sgDir, sgDir, ref, "commit", 200*time.Millisecond); err == nil {
		t.Fatal("Acquire took a lock held by a live process")
	}
	if _, err := os.Stat(lp); err != nil {
		t.Errorf("live holder's lock file was removed: %v", err)
	}
}
