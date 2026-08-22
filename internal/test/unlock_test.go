package test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUnlockStaleLock verifies that `safegit unlock` removes a lock held by a
// dead process (PID that doesn't exist).
func TestUnlockStaleLock(t *testing.T) {
	dir := newRepo(t)

	// Ensure .git/safegit/ is initialized (config set in newRepo triggers this).
	gitDir := filepath.Join(dir, ".git")
	lockDir := filepath.Join(gitDir, "safegit", "locks", "refs", "heads")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatalf("creating lock dir: %v", err)
	}

	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}

	// Plant a stale lock file with a PID that doesn't exist.
	lockFile := filepath.Join(lockDir, "main.lock")
	content := fmt.Sprintf("pid=999999999\nts=2026-01-01T00:00:00Z\nop=commit\nhost=%s\n", hostname)
	if err := os.WriteFile(lockFile, []byte(content), 0644); err != nil {
		t.Fatalf("writing lock file: %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir, "unlock", "main")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "lock on main released") {
		t.Errorf("expected stdout to contain 'lock on main released', got %q", stdout)
	}

	// Lock file should no longer exist.
	if _, err := os.Stat(lockFile); !os.IsNotExist(err) {
		t.Errorf("lock file still exists after unlock (err=%v)", err)
	}
}

// TestUnlockLiveProcessRefused verifies that `safegit unlock` refuses to remove
// a lock held by a live process (the test process itself).
func TestUnlockLiveProcessRefused(t *testing.T) {
	dir := newRepo(t)

	gitDir := filepath.Join(dir, ".git")
	lockDir := filepath.Join(gitDir, "safegit", "locks", "refs", "heads")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatalf("creating lock dir: %v", err)
	}

	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}

	// Plant a lock file with the test process's PID (alive).
	lockFile := filepath.Join(lockDir, "main.lock")
	content := fmt.Sprintf("pid=%d\nts=2026-01-01T00:00:00Z\nop=commit\nhost=%s\n", os.Getpid(), hostname)
	if err := os.WriteFile(lockFile, []byte(content), 0644); err != nil {
		t.Fatalf("writing lock file: %v", err)
	}

	_, stderr, code := runSafegit(t, dir, "unlock", "main")
	if code == 0 {
		t.Fatal("expected non-zero exit code for live process lock, got 0")
	}
	if !strings.Contains(stderr, "held by a live process") {
		t.Errorf("expected stderr to contain 'held by a live process', got %q", stderr)
	}

	// Lock file should still exist (not removed).
	if _, err := os.Stat(lockFile); err != nil {
		t.Errorf("lock file was removed despite live process: %v", err)
	}
}

// TestUnlockDryRun verifies that `safegit --dry-run unlock` reports what it
// would do but does not actually release the lock.
func TestUnlockDryRun(t *testing.T) {
	dir := newRepo(t)

	gitDir := filepath.Join(dir, ".git")
	lockDir := filepath.Join(gitDir, "safegit", "locks", "refs", "heads")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatalf("creating lock dir: %v", err)
	}

	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}

	// Plant a stale lock file with a PID that doesn't exist.
	lockFile := filepath.Join(lockDir, "main.lock")
	content := fmt.Sprintf("pid=999999999\nts=2026-01-01T00:00:00Z\nop=commit\nhost=%s\n", hostname)
	if err := os.WriteFile(lockFile, []byte(content), 0644); err != nil {
		t.Fatalf("writing lock file: %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "unlock", "main")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "would release") {
		t.Errorf("expected stdout to contain 'would release', got %q", stdout)
	}

	// Lock file should still exist (not removed in dry-run).
	if _, err := os.Stat(lockFile); err != nil {
		t.Errorf("lock file was removed during dry-run: %v", err)
	}
}

// TestUnlockNonexistentLock verifies that `safegit unlock` fails when no lock
// exists for the given ref.
func TestUnlockNonexistentLock(t *testing.T) {
	dir := newRepo(t)

	_, stderr, code := runSafegit(t, dir, "unlock", "main")
	if code == 0 {
		t.Fatal("expected non-zero exit code for nonexistent lock, got 0")
	}
	if !strings.Contains(stderr, "no lock held") {
		t.Errorf("expected stderr to contain 'no lock held', got %q", stderr)
	}
}

// The full-ref arm of the naming grammar: an argument that already starts with
// refs/ is taken as written rather than prefixed again, so `unlock
// refs/heads/main` reaches the same lock as `unlock main`, and a ref outside
// refs/heads (which has no shorthand at all) is reachable by its full name.
func TestUnlockAcceptsFullRefNames(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}

	for _, tc := range []struct {
		name        string
		arg         string
		lockRelDir  []string
		lockBase    string
		wantDisplay string
	}{
		{"branch by full ref", "refs/heads/main", []string{"refs", "heads"}, "main.lock", "main"},
		{"a ref with no shorthand", "refs/notes/commits", []string{"refs", "notes"}, "commits.lock", "refs/notes/commits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			lockDir := filepath.Join(append([]string{dir, ".git", "safegit", "locks"}, tc.lockRelDir...)...)
			if err := os.MkdirAll(lockDir, 0755); err != nil {
				t.Fatalf("creating lock dir: %v", err)
			}
			lockFile := filepath.Join(lockDir, tc.lockBase)
			content := fmt.Sprintf("pid=999999999\nts=2026-01-01T00:00:00Z\nop=commit\nhost=%s\n", hostname)
			if err := os.WriteFile(lockFile, []byte(content), 0644); err != nil {
				t.Fatalf("writing lock file: %v", err)
			}

			stdout, stderr, code := runSafegit(t, dir, "unlock", tc.arg)
			if code != 0 {
				t.Fatalf("unlock %s exited %d; stdout=%q stderr=%q", tc.arg, code, stdout, stderr)
			}
			if !strings.Contains(stdout, "lock on "+tc.wantDisplay+" released") {
				t.Errorf("unlock %s must report the released lock as %q, got %q", tc.arg, tc.wantDisplay, stdout)
			}
			if _, err := os.Stat(lockFile); !os.IsNotExist(err) {
				t.Errorf("the lock file survived unlock %s (err=%v)", tc.arg, err)
			}
		})
	}
}
