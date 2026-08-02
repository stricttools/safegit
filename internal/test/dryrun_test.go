package test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plantLiveRewriteLock writes a rewrite lock file owned by the (live) test
// process, so any command that tries to acquire the rewrite lock blocks until
// its timeout and then fails. Returns the lock file path.
func plantLiveRewriteLock(t *testing.T, repoDir string) string {
	t.Helper()
	lockDir := filepath.Join(repoDir, ".git", "safegit", "locks", "safegit")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatalf("creating lock dir: %v", err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	lockFile := filepath.Join(lockDir, "rewrite.lock")
	content := fmt.Sprintf("pid=%d\nts=2026-01-01T00:00:00Z\nop=scrub-file\nhost=%s\n", os.Getpid(), hostname)
	if err := os.WriteFile(lockFile, []byte(content), 0644); err != nil {
		t.Fatalf("writing lock file: %v", err)
	}
	return lockFile
}

// TestAuthorRewriteDryRunSkipsRewriteLock: a dry-run preview is read-only, so
// it must not contend for the repo-wide rewrite lock the way the execute path
// does. With the lock held by a live process, the preview still succeeds.
func TestAuthorRewriteDryRunSkipsRewriteLock(t *testing.T) {
	dir := newRepo(t)

	// Keep the lock timeout short so the pre-fix behaviour fails fast.
	if _, stderr, code := runSafegit(t, dir, "config", "set", "lock.acquireTimeoutSeconds", "1"); code != 0 {
		t.Fatalf("config set failed (code %d): %s", code, stderr)
	}
	lockFile := plantLiveRewriteLock(t, dir)

	stdout, stderr, code := runSafegit(t, dir,
		"--dry-run", "author", "rewrite", "--old-name", "Test", "--new-name", "Renamed")
	if code != 0 {
		t.Fatalf("author rewrite --dry-run should not need the rewrite lock (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Would rewrite") {
		t.Errorf("expected a preview on stdout, got: %s", stdout)
	}

	// The foreign lock must be left exactly as it was found.
	if _, err := os.Stat(lockFile); err != nil {
		t.Errorf("dry-run disturbed the foreign rewrite lock: %v", err)
	}
}

// TestSubmoduleCommitDryRunDoesNotBumpParent: a dry-run commit inside a
// submodule must leave the parent repository completely untouched, even when
// commit.autoBumpParent is enabled there.
func TestSubmoduleCommitDryRunDoesNotBumpParent(t *testing.T) {
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)
	enableAutoBump(t, parentDir)

	// Land a real submodule commit while auto-bump is still disabled, so the
	// parent's gitlink is stale: this is exactly the state in which the parent
	// bump would produce a real commit.
	if err := os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("real content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegit(t, subDir, "commit", "-m", "real sub change", "--", "file.txt"); code != 0 {
		t.Fatalf("submodule commit failed (code %d): %s", code, stderr)
	}

	enableAutoBump(t, parentDir)

	parentCountBefore := gitLog(t, parentDir, "HEAD")
	parentHeadBefore := revParseHEAD(t, parentDir)
	subHeadBefore := revParseHEAD(t, subDir)

	if err := os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("dry content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, subDir, "--dry-run", "commit", "-m", "dry sub change", "--", "file.txt")
	if code != 0 {
		t.Fatalf("dry-run commit in submodule failed (code %d): %s", code, stderr)
	}

	if got := revParseHEAD(t, parentDir); got != parentHeadBefore {
		t.Errorf("parent HEAD moved during a dry run: %s -> %s", parentHeadBefore[:12], got[:12])
	}
	if got := gitLog(t, parentDir, "HEAD"); got != parentCountBefore {
		t.Errorf("parent commit count changed during a dry run: %d -> %d", parentCountBefore, got)
	}
	if got := revParseHEAD(t, subDir); got != subHeadBefore {
		t.Errorf("submodule HEAD moved during a dry run: %s -> %s", subHeadBefore[:12], got[:12])
	}
}

// TestSubmoduleRewordDryRunDoesNotBumpParent covers the amend/reword autobump
// call site: a dry-run message reword inside a submodule must not commit in
// the parent either.
func TestSubmoduleRewordDryRunDoesNotBumpParent(t *testing.T) {
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)

	if err := os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("real content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegit(t, subDir, "commit", "-m", "real sub change", "--", "file.txt"); code != 0 {
		t.Fatalf("submodule commit failed (code %d): %s", code, stderr)
	}

	enableAutoBump(t, parentDir)

	parentCountBefore := gitLog(t, parentDir, "HEAD")
	parentHeadBefore := revParseHEAD(t, parentDir)

	_, stderr, code := runSafegit(t, subDir, "--dry-run", "commit", "--amend", "-m", "reworded in dry run")
	if code != 0 {
		t.Fatalf("dry-run reword in submodule failed (code %d): %s", code, stderr)
	}

	if got := revParseHEAD(t, parentDir); got != parentHeadBefore {
		t.Errorf("parent HEAD moved during a dry-run reword: %s -> %s", parentHeadBefore[:12], got[:12])
	}
	if got := gitLog(t, parentDir, "HEAD"); got != parentCountBefore {
		t.Errorf("parent commit count changed during a dry-run reword: %d -> %d", parentCountBefore, got)
	}
}

// TestAuthorRewriteDryRunSkipsConfigLoad: the preview needs neither config nor
// lock, so an unreadable config file must not block it.
func TestAuthorRewriteDryRunSkipsConfigLoad(t *testing.T) {
	dir := newRepo(t)

	cfgPath := filepath.Join(dir, ".git", "safegit", "config.json")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("config file missing at %s: %v", cfgPath, err)
	}
	if err := os.WriteFile(cfgPath, []byte("{ this is not json"), 0644); err != nil {
		t.Fatalf("corrupting config: %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir,
		"--dry-run", "author", "rewrite", "--old-email", "test@test.com", "--new-email", "new@test.com")
	if code != 0 {
		t.Fatalf("author rewrite --dry-run should not load config (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Would rewrite") {
		t.Errorf("expected a preview on stdout, got: %s", stdout)
	}
}
