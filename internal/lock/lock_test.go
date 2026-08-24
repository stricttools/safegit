package lock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func setupSafegitDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	sgDir := filepath.Join(dir, "safegit")
	os.MkdirAll(filepath.Join(sgDir, "locks", "refs", "heads"), 0755)
	return sgDir
}

func TestAcquireAndRelease(t *testing.T) {
	sgDir := setupSafegitDir(t)

	lock, err := Acquire(sgDir, sgDir, "refs/heads/main", "commit", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	// Lock file should exist
	if _, err := os.Stat(lock.LockPath); os.IsNotExist(err) {
		t.Fatal("lock file should exist")
	}

	// Release should remove it
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lock.LockPath); !os.IsNotExist(err) {
		t.Error("lock file should be removed after Release")
	}
}

func TestAcquireConflict(t *testing.T) {
	sgDir := setupSafegitDir(t)

	lock1, err := Acquire(sgDir, sgDir, "refs/heads/main", "commit", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer lock1.Release()

	// Second acquire should time out quickly
	_, err = Acquire(sgDir, sgDir, "refs/heads/main", "commit", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error on conflicting acquire")
	}
}

// TestAcquireTimeoutIsTyped pins the shape callers depend on: a contended
// acquire fails with *TimeoutError carrying the ref and the holder record, so
// safegit can report it as its own exit code without matching message text.
func TestAcquireTimeoutIsTyped(t *testing.T) {
	sgDir := setupSafegitDir(t)

	held, err := Acquire(sgDir, sgDir, "safegit/rewrite", "scrub-file", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	_, err = Acquire(sgDir, sgDir, "safegit/rewrite", "scrub-match", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout on the contended acquire")
	}
	if !IsTimeout(err) {
		t.Fatalf("IsTimeout(%v) = false, want true", err)
	}
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("errors.As did not find a *TimeoutError in %v", err)
	}
	if te.Ref != "safegit/rewrite" {
		t.Errorf("TimeoutError.Ref = %q, want safegit/rewrite", te.Ref)
	}
	if !strings.Contains(te.Holder, "op=scrub-file") {
		t.Errorf("TimeoutError.Holder = %q, want it to name the holding operation", te.Holder)
	}
	if te.Timeout != 50*time.Millisecond {
		t.Errorf("TimeoutError.Timeout = %v, want 50ms", te.Timeout)
	}
	if !strings.Contains(err.Error(), "timeout acquiring lock on safegit/rewrite") {
		t.Errorf("error text = %q, want it to name the ref", err.Error())
	}
}

// TestNonTimeoutAcquireFailureIsNotTyped keeps IsTimeout honest: a lock that
// fails for a reason other than contention must not be reported as a timeout,
// or the exit code would lie about what happened.
func TestNonTimeoutAcquireFailureIsNotTyped(t *testing.T) {
	sgDir := setupSafegitDir(t)
	// A regular file where the ref's lock directory needs to be makes MkdirAll
	// fail, which is a lock failure that is not contention.
	blocker := filepath.Join(sgDir, "locks", "safegit")
	if err := os.WriteFile(blocker, []byte("not a directory\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Acquire(sgDir, sgDir, "safegit/rewrite", "scrub-file", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected an error when the locks directory cannot be created")
	}
	if IsTimeout(err) {
		t.Errorf("IsTimeout(%v) = true, but the failure was not contention", err)
	}
}

func TestStaleLockReclaimed(t *testing.T) {
	sgDir := setupSafegitDir(t)
	ref := "refs/heads/main"
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}

	// Manually create a lock file with a dead PID on the local host
	lp := lockPath(sgDir, ref)
	os.MkdirAll(filepath.Dir(lp), 0755)
	err = os.WriteFile(lp, []byte("pid=999999999\nts=2026-01-01T00:00:00Z\nop=commit\nhost="+hostname+"\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Acquire should reclaim the stale lock
	lock, err := Acquire(sgDir, sgDir, ref, "commit", 5*time.Second)
	if err != nil {
		t.Fatalf("should reclaim stale lock: %v", err)
	}
	defer lock.Release()
}

func TestIsStale(t *testing.T) {
	dir := t.TempDir()
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}

	// Create a lock file with a dead PID on the local host
	deadLock := filepath.Join(dir, "dead.lock")
	os.WriteFile(deadLock, []byte("pid=999999999\nts=2026-01-01T00:00:00Z\nop=test\nhost="+hostname+"\n"), 0644)

	if !IsStale(deadLock) {
		t.Error("lock with dead PID should be stale")
	}

	// Create a lock file with PID 1 (alive) on the local host
	aliveLock := filepath.Join(dir, "alive.lock")
	os.WriteFile(aliveLock, []byte("pid=1\nts=2026-01-01T00:00:00Z\nop=test\nhost="+hostname+"\n"), 0644)

	if IsStale(aliveLock) {
		t.Error("lock with PID 1 should not be stale")
	}
}

func TestParseLockFields(t *testing.T) {
	dir := t.TempDir()

	// Normal lock file with every field
	f := filepath.Join(dir, "withhost.lock")
	os.WriteFile(f, []byte("pid=42\nts=2026-01-01T00:00:00Z\nop=test\nhost=myhost\nstart=12345\n"), 0644)
	fields, err := parseLockFields(f)
	if err != nil {
		t.Fatalf("parseLockFields: %v", err)
	}
	for key, want := range map[string]string{"pid": "42", "op": "test", "host": "myhost", "start": "12345"} {
		if fields[key] != want {
			t.Errorf("fields[%q] = %q, want %q", key, fields[key], want)
		}
	}

	// Lock file without host= or start= fields
	noHost := filepath.Join(dir, "nohost.lock")
	os.WriteFile(noHost, []byte("pid=42\nts=2026-01-01T00:00:00Z\nop=test\n"), 0644)
	fields, err = parseLockFields(noHost)
	if err != nil {
		t.Fatalf("parseLockFields: %v", err)
	}
	if fields["host"] != "" {
		t.Errorf("host = %q, want empty string", fields["host"])
	}
	if fields["start"] != "" {
		t.Errorf("start = %q, want empty string", fields["start"])
	}

	// Nonexistent file
	if _, err := parseLockFields(filepath.Join(dir, "nope.lock")); err == nil {
		t.Error("expected an error reading a missing lock file")
	}
}

func TestIsStale_ForeignHost(t *testing.T) {
	dir := t.TempDir()

	// Create a lock file with a dead PID but from a different host.
	// The dead PID (999999999) would normally be stale, but the hostname
	// mismatch should prevent reclamation.
	foreignLock := filepath.Join(dir, "foreign.lock")
	os.WriteFile(foreignLock, []byte("pid=999999999\nts=2026-01-01T00:00:00Z\nop=test\nhost=some-other-machine-that-does-not-exist\n"), 0644)

	if IsStale(foreignLock) {
		t.Error("lock from a different host should NOT be considered stale (PID namespace is foreign)")
	}
}

func TestIsStale_EmptyHost(t *testing.T) {
	dir := t.TempDir()

	// Lock file with no host= field (old format): dead PID should still be stale
	oldFormatLock := filepath.Join(dir, "old.lock")
	os.WriteFile(oldFormatLock, []byte("pid=999999999\nts=2026-01-01T00:00:00Z\nop=test\n"), 0644)

	if !IsStale(oldFormatLock) {
		t.Error("lock with dead PID and no host field should be stale (backward compat)")
	}
}

func TestIsStale_SameHost(t *testing.T) {
	dir := t.TempDir()

	// Lock file with matching hostname and dead PID should be stale
	hostname, err := os.Hostname()
	if err != nil {
		t.Skip("cannot determine hostname")
	}

	sameHostLock := filepath.Join(dir, "samehost.lock")
	content := "pid=999999999\nts=2026-01-01T00:00:00Z\nop=test\nhost=" + hostname + "\n"
	os.WriteFile(sameHostLock, []byte(content), 0644)

	if !IsStale(sameHostLock) {
		t.Error("lock with dead PID on same host should be stale")
	}
}

func TestForceRelease(t *testing.T) {
	sgDir := setupSafegitDir(t)
	ref := "refs/heads/main"

	// Force release when no lock exists should error
	err := ForceRelease(sgDir, ref, os.Remove)
	if err == nil {
		t.Fatal("expected error when no lock held")
	}

	// Create and force-release
	lock, err := Acquire(sgDir, sgDir, ref, "commit", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	err = ForceRelease(sgDir, ref, os.Remove)
	if err != nil {
		t.Fatal(err)
	}

	// The lock struct still has the path, but the file is gone
	if _, err := os.Stat(lock.LockPath); !os.IsNotExist(err) {
		t.Error("lock file should be removed after ForceRelease")
	}
}

func TestConcurrentAcquire(t *testing.T) {
	sgDir := setupSafegitDir(t)
	ref := "refs/heads/main"

	const goroutines = 10
	var wg sync.WaitGroup
	acquired := make(chan int, goroutines)

	// Launch goroutines that all try to acquire the same lock
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			lock, err := Acquire(sgDir, sgDir, ref, "commit", 2*time.Second)
			if err != nil {
				return // timeout is expected for most goroutines
			}
			acquired <- id
			// Hold the lock briefly
			time.Sleep(10 * time.Millisecond)
			lock.Release()
		}(i)
	}

	wg.Wait()
	close(acquired)

	// At least one goroutine should have acquired the lock
	count := 0
	for range acquired {
		count++
	}
	if count == 0 {
		t.Error("at least one goroutine should have acquired the lock")
	}
}

func TestLockPath(t *testing.T) {
	got := lockPath("/repo/.git/safegit", "refs/heads/main")
	want := filepath.Join("/repo/.git/safegit", "locks", "refs", "heads", "main.lock")
	if got != want {
		t.Errorf("lockPath = %q, want %q", got, want)
	}
}

func TestParsePID(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.lock")

	os.WriteFile(f, []byte("pid=42\nts=2026-01-01T00:00:00Z\nop=test\nhost=test\n"), 0644)
	pid, err := ParsePID(f)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 42 {
		t.Errorf("ParsePID = %d, want 42", pid)
	}

	// Missing pid line
	noPID := filepath.Join(dir, "nopid.lock")
	os.WriteFile(noPID, []byte("ts=2026-01-01T00:00:00Z\n"), 0644)
	_, err = ParsePID(noPID)
	if err == nil {
		t.Error("expected error when no pid= line")
	}
}
