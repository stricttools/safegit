package test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/smm-h/safegit/internal/procutil"
	"github.com/smm-h/safegit/internal/testutil"
)

// TestCommitDoesNotStealLockFromLiveHolder is the end-to-end form of the
// lock-staleness contract: when the branch lock is held by a process that is
// still running, a second safegit operation waits, times out, and leaves the
// lock file exactly where it found it. It must never reclaim the lock, because
// doing so would let two operations rewrite the same ref at once.
//
// The lock file is deliberately backdated: the previous staleness check
// compared the lock's mtime against the mtime of the holder's /proc directory,
// which meant an old-looking lock file from a live holder was reclaimed every
// time.
func TestCommitDoesNotStealLockFromLiveHolder(t *testing.T) {
	dir := newRepo(t)

	// Keep the wait short: the point is that the acquire fails, not how long
	// safegit is willing to wait for a live holder.
	if _, stderr, code := runSafegit(t, dir, "config", "set", "lock.acquireTimeoutSeconds", "1"); code != 0 {
		t.Fatalf("config set failed (code %d): %s", code, stderr)
	}

	holder := testutil.SpawnSleeper(t)
	start, err := procutil.StartTime(holder.Pid)
	if err != nil {
		t.Skipf("cannot read start time of pid %d: %v", holder.Pid, err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}

	lockDir := filepath.Join(dir, ".git", "safegit", "locks", "refs", "heads")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatalf("creating lock dir: %v", err)
	}
	lockFile := filepath.Join(lockDir, "main.lock")
	content := fmt.Sprintf("pid=%d\nts=2026-01-01T00:00:00Z\nop=commit\nhost=%s\nstart=%d\n",
		holder.Pid, hostname, start.Ticks)
	if err := os.WriteFile(lockFile, []byte(content), 0644); err != nil {
		t.Fatalf("writing lock file: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(lockFile, old, old); err != nil {
		t.Fatalf("backdating lock file: %v", err)
	}

	before := gitLog(t, dir, "HEAD")
	testutil.WriteFile(t, dir, "contended.txt", "content\n")

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "should not happen", "--", "contended.txt")
	if code == 0 {
		t.Fatalf("commit succeeded while the branch lock was held by live pid %d: stdout=%s", holder.Pid, stdout)
	}
	if !strings.Contains(stderr, "timeout acquiring lock") {
		t.Errorf("expected a lock timeout on stderr, got: %s", stderr)
	}
	if _, err := os.Stat(lockFile); err != nil {
		t.Errorf("the live holder's lock file was removed: %v", err)
	}
	if after := gitLog(t, dir, "HEAD"); after != before {
		t.Errorf("commit count went from %d to %d; the contended commit was made anyway", before, after)
	}
}
