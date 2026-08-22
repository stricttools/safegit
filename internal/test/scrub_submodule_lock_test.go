package test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/testutil"
)

// A submodule scrub rewrites TWO repositories, so it contends for two
// repository-wide rewrite locks: the parent's and the submodule's own. The
// tests here pin that both are taken, that either one being held elsewhere
// stops the operation before it rewrites anything, and that both are held at
// the moment the rewrite is writing objects.

var submoduleLockEnv = []string{"CLAUDE_CODE_SESSION_ID=submodule-lock-test"}

// rewriteLockPath is where the repository-wide rewrite lock lives for a given
// safegit directory.
func rewriteLockPath(sgDir string) string {
	return lock.Path(sgDir, lock.RewriteRef)
}

// plantLiveRewriteLockAt writes a rewrite-lock file owned by the (live) test
// process into a specific safegit directory, so safegit's staleness check
// cannot reclaim it. It stands in for another session mid-rewrite in that
// repository -- including a submodule, whose safegit directory is not under the
// parent's working tree.
func plantLiveRewriteLockAt(t *testing.T, sgDir string) string {
	t.Helper()
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	lockFile := rewriteLockPath(sgDir)
	if err := os.MkdirAll(filepath.Dir(lockFile), 0755); err != nil {
		t.Fatalf("creating lock dir: %v", err)
	}
	content := fmt.Sprintf("pid=%d\nts=2026-01-01T00:00:00Z\nop=scrub-file\nhost=%s\n", os.Getpid(), hostname)
	if err := os.WriteFile(lockFile, []byte(content), 0644); err != nil {
		t.Fatalf("writing lock file: %v", err)
	}
	return lockFile
}

// shortenLockTimeout keeps the contention tests quick: the point is that the
// acquire fails, not how long safegit is willing to wait.
func shortenLockTimeout(t *testing.T, dir string) {
	t.Helper()
	if _, stderr, code := runSafegitEnv(t, dir, submoduleLockEnv, "config", "set", "lock.acquireTimeoutSeconds", "1"); code != 0 {
		t.Fatalf("config set in %s failed (code %d): %s", dir, code, stderr)
	}
}

// TestScrubFileInSubmoduleWaitsForTheSubmodulesRewriteLock: the submodule is a
// repository of its own, and a scrub that rewrites its history must contend on
// its rewrite lock. Another session holding it means this one refuses with the
// contention exit code instead of rewriting the submodule underneath it.
func TestScrubFileInSubmoduleWaitsForTheSubmodulesRewriteLock(t *testing.T) {
	parentDir, subDir, _, subSgDir, _ := submoduleScrubFixture(t)
	shortenLockTimeout(t, parentDir)
	shortenLockTimeout(t, subDir)

	parentBefore := snapshotRefs(t, parentDir, "parent-v1")
	subBefore := snapshotRefs(t, subDir, "sub-v1")

	lockFile := plantLiveRewriteLockAt(t, subSgDir)

	stdout, stderr, code := scrubSubmoduleFile(t, parentDir, submoduleLockEnv)
	if code != exitcode.LockTimeout {
		t.Fatalf("a held submodule rewrite lock must refuse with %d (LockTimeout), got %d\nstdout:\n%s\nstderr:\n%s",
			exitcode.LockTimeout, code, stdout, stderr)
	}
	if !strings.Contains(stderr, "timeout acquiring lock") {
		t.Errorf("the refusal must name the lock timeout; stderr: %s", stderr)
	}
	if _, err := os.Stat(lockFile); err != nil {
		t.Errorf("the live holder's lock file was removed: %v", err)
	}

	requireUntouched(t, "submodule", subDir, "sub-v1", subBefore)
	requireUntouched(t, "parent", parentDir, "parent-v1", parentBefore)
}

// TestScrubFileInSubmoduleWaitsForTheParentsRewriteLock: the parent's own
// history is rewritten too (its gitlinks follow the submodule), so the parent's
// rewrite lock is taken BEFORE the operation delegates into the submodule.
func TestScrubFileInSubmoduleWaitsForTheParentsRewriteLock(t *testing.T) {
	parentDir, subDir, _, _, _ := submoduleScrubFixture(t)
	shortenLockTimeout(t, parentDir)
	shortenLockTimeout(t, subDir)

	parentBefore := snapshotRefs(t, parentDir, "parent-v1")
	subBefore := snapshotRefs(t, subDir, "sub-v1")

	parentSgDir := filepath.Join(parentDir, ".git", "safegit")
	lockFile := plantLiveRewriteLockAt(t, parentSgDir)

	stdout, stderr, code := scrubSubmoduleFile(t, parentDir, submoduleLockEnv)
	if code != exitcode.LockTimeout {
		t.Fatalf("a held parent rewrite lock must refuse with %d (LockTimeout), got %d\nstdout:\n%s\nstderr:\n%s",
			exitcode.LockTimeout, code, stdout, stderr)
	}
	if _, err := os.Stat(lockFile); err != nil {
		t.Errorf("the live holder's lock file was removed: %v", err)
	}

	requireUntouched(t, "submodule", subDir, "sub-v1", subBefore)
	requireUntouched(t, "parent", parentDir, "parent-v1", parentBefore)
}

// TestScrubFileInSubmoduleHoldsBothRewriteLocks observes the locks from inside
// the operation: at the moment the submodule's commits are being written, both
// lock files exist, each in its own repository's safegit directory. Holding
// them at that instant is what makes the whole two-repository rewrite -- objects,
// verification, refs -- exclusive against another session in either repository.
func TestScrubFileInSubmoduleHoldsBothRewriteLocks(t *testing.T) {
	parentDir, _, _, subSgDir, _ := submoduleScrubFixture(t)

	probe := filepath.Join(t.TempDir(), "locks-seen")
	parentLock := rewriteLockPath(filepath.Join(parentDir, ".git", "safegit"))
	subLock := rewriteLockPath(subSgDir)

	// The submodule's calls carry a GIT_DIR; the parent's do not. Writing a
	// commit into the submodule is therefore an instant that is unambiguously
	// inside the delegated flow.
	env, _ := gitShim(t, "commit-tree",
		`if [ -n "$GIT_DIR" ]; then`+
			` { [ -e `+parentLock+` ] && echo parent; [ -e `+subLock+` ] && echo submodule; } >> `+probe+`;`+
			` fi`)

	stdout, stderr, code := scrubSubmoduleFile(t, parentDir, env)
	if code != 0 {
		t.Fatalf("scrub failed (code %d)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	data, err := os.ReadFile(probe)
	if err != nil {
		t.Fatalf("the probe never ran, so no submodule commit was written: %v", err)
	}
	seen := string(data)
	if !strings.Contains(seen, "parent") {
		t.Errorf("the parent's rewrite lock (%s) was not held while the submodule was being rewritten; probe: %q", parentLock, seen)
	}
	if !strings.Contains(seen, "submodule") {
		t.Errorf("the submodule's rewrite lock (%s) was not held while it was being rewritten; probe: %q", subLock, seen)
	}
}

// TestScrubMatchWaitsForTheSubmodulesRewriteLock is the same contract for
// `scrub match`, whose submodule recursion rewrites every submodule that holds
// the pattern. The parent's lock is taken at the command's entry; each
// submodule's own lock is taken before its history is rewritten.
func TestScrubMatchWaitsForTheSubmodulesRewriteLock(t *testing.T) {
	parentDir, _, subDir := newRepoWithSubmoduleSecret(t, "MATCHLOCK_SECRET", "secret.txt")
	shortenLockTimeout(t, parentDir)
	shortenLockTimeout(t, subDir)

	subSgDir := filepath.Join(submoduleGitDir(t, subDir), "safegit")
	subHeadBefore := testutil.Rev(t, subDir, "HEAD")
	parentHeadBefore := testutil.Rev(t, parentDir, "HEAD")

	lockFile := plantLiveRewriteLockAt(t, subSgDir)

	stdout, stderr, code := runSafegitEnv(t, parentDir, submoduleLockEnv, "--approve-consequential",
		"scrub", "match", "--pattern", "MATCHLOCK_SECRET", "--replace", "REDACTED",
		"--entire-history", "--reason", "submodule lock contention")
	if code != exitcode.LockTimeout {
		t.Fatalf("a held submodule rewrite lock must refuse with %d (LockTimeout), got %d\nstdout:\n%s\nstderr:\n%s",
			exitcode.LockTimeout, code, stdout, stderr)
	}
	if _, err := os.Stat(lockFile); err != nil {
		t.Errorf("the live holder's lock file was removed: %v", err)
	}
	if got := testutil.Rev(t, subDir, "HEAD"); got != subHeadBefore {
		t.Errorf("the submodule was rewritten while its lock was held: %s -> %s", subHeadBefore, got)
	}
	if got := testutil.Rev(t, parentDir, "HEAD"); got != parentHeadBefore {
		t.Errorf("the parent was rewritten while the submodule's lock was held: %s -> %s", parentHeadBefore, got)
	}
}
