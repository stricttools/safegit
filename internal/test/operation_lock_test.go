package test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/testutil"
)

// operationLockPath is where the worktree operation lock lives for a repository
// built by newRepo: in the WORKTREE-LOCAL safegit directory, not the shared one.
func operationLockPath(dir string) string {
	return lock.Path(repo.SafegitDir(filepath.Join(dir, ".git")), lock.OperationRef)
}

// plantStaleLock writes a lock file naming a PID no process can have, which is
// what a crashed holder leaves behind.
func plantStaleLock(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("pid=999999999\nts=2026-01-01T00:00:00Z\nop=merge\nhost=%s\n", host)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// divergeIntoConflict builds a branch whose edit to c.txt conflicts with main's,
// leaving HEAD on main. It returns the branch name.
func divergeIntoConflict(t *testing.T, dir, branch, mainContent string) string {
	t.Helper()
	testutil.WriteFile(t, dir, "c.txt", "base\n")
	safegitCommit(t, dir, "base", "c.txt")
	testutil.Git(t, dir, "branch", branch)
	testutil.Git(t, dir, "switch", "-q", branch)
	testutil.WriteFile(t, dir, "c.txt", branch+"\n")
	safegitCommit(t, dir, branch+" edit", "c.txt")
	testutil.Git(t, dir, "switch", "-q", "main")
	testutil.WriteFile(t, dir, "c.txt", mainContent)
	safegitCommit(t, dir, "main edit", "c.txt")
	return branch
}

// Two passthroughs launched at once in one worktree must not run at once.
//
// The observable difference is in the loser's verdict. Serialized, the second
// merge starts only after the first has finished writing MERGE_HEAD and the
// conflict into the tree, so it meets a repository that is plainly mid-merge and
// is refused by the coordination guard with exit 5. Interleaved, both would pass
// the guard against a clean tree and the second would reach git, which fails
// with its own fatal ("You have not concluded your merge") and a code in the
// 128 range -- a git-level error where safegit owed a refusal.
func TestOperationLockSerializesConcurrentPassthroughs(t *testing.T) {
	dir := newRepo(t)
	divergeIntoConflict(t, dir, "feature", "main\n")

	// A second branch that also conflicts with main, so whichever merge wins
	// the lock conflicts and whichever loses meets a dirty tree.
	testutil.Git(t, dir, "branch", "-q", "other", "feature")

	type outcome struct {
		code   int
		stderr string
	}
	results := make([]outcome, 2)
	branches := []string{"feature", "other"}

	var wg sync.WaitGroup
	for i := range branches {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, stderr, code := runSafegit(t, dir, "merge", branches[i])
			results[i] = outcome{code: code, stderr: stderr}
		}(i)
	}
	wg.Wait()

	var refused, conflicted int
	for i, r := range results {
		switch {
		case r.code == exitcode.CoordinationBusy:
			refused++
			if !strings.Contains(strings.ToLower(r.stderr), "merge") {
				t.Errorf("merge %s was refused without naming the merge in flight: %s",
					branches[i], oneLine(r.stderr))
			}
		case r.code != 0:
			conflicted++
		default:
			t.Errorf("merge %s succeeded; both branches conflict with main", branches[i])
		}
		if r.code >= 128 {
			t.Errorf("merge %s exited %d -- a git fatal, which is what an interleaved "+
				"pair produces: %s", branches[i], r.code, oneLine(r.stderr))
		}
	}
	if conflicted != 1 || refused != 1 {
		t.Errorf("want exactly one conflicted merge and one coordination refusal, got %d and %d (%+v)",
			conflicted, refused, results)
	}

	// Exactly one merge is in flight, not two half-finished ones.
	if !testutil.FileExists(filepath.Join(dir, ".git", "MERGE_HEAD")) {
		t.Error("no merge is in flight after two concurrent merges")
	}
}

// commit takes the same lock the passthroughs take, so the two families
// serialize against each other and not merely within themselves. Held by a live
// holder, the lock makes every one of them wait and then refuse with the typed
// timeout rather than proceed.
func TestOperationLockIsSharedByCommitAndPassthroughs(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "f.txt", "content\n")
	shortLockTimeout(t, dir)
	holdLock(t, dir, lock.OperationRef, "test-holder")

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"commit", []string{"commit", "-m", "blocked", "--", "f.txt"}},
		{"amend", []string{"commit", "--amend", "-m", "blocked", "--", "f.txt"}},
		{"reword", []string{"commit", "--amend", "-m", "blocked"}},
		{"undo", []string{"undo", "--bypass-session"}},
		{"cherry-pick", []string{"cherry-pick", "HEAD"}},
		{"checkout", []string{"checkout", "main"}},
		{"merge", []string{"merge", "main"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, code := runSafegit(t, dir, tc.args...)
			assertLockTimeoutRefusal(t, tc.name, code, stderr)
			if !strings.Contains(stderr, lock.OperationRef) {
				t.Errorf("%s does not name the operation lock it timed out on: %s", tc.name, oneLine(stderr))
			}
		})
	}

	// Nothing was committed while the lock was held.
	if status := testutil.Git(t, dir, "status", "--porcelain"); !strings.Contains(status, "f.txt") {
		t.Errorf("f.txt is no longer untracked, so something committed under a held lock: %s", oneLine(status))
	}
}

// The TOCTOU the lock exists to close, exercised directly.
//
// A commit reads git's in-flight-operation state and then updates a ref. Without
// the operation lock those are two separate instants, and a passthrough running
// in between puts the repository mid-merge AFTER the check has already passed --
// so the commit proceeds against a repository git considers mid-merge, drops the
// merge's second parent and every path its pathspec does not name, and exits 0.
//
// Here the commit is made to wait on the lock, the merge state appears while it
// waits, and the lock is then released. If the check runs under the lock -- as
// it must -- the commit sees the merge and refuses.
func TestCommitRereadsSequencerStateUnderTheOperationLock(t *testing.T) {
	dir := newRepo(t)
	feature := divergeIntoConflict(t, dir, "feature", "main\n")
	before := testutil.Rev(t, dir, "HEAD")

	// An unrelated file, so the commit under test has something to commit that
	// the merge does not touch: the refusal must not depend on the pathspec.
	testutil.WriteFile(t, dir, "unrelated.txt", "unrelated\n")

	sgDir := repo.SafegitDir(filepath.Join(dir, ".git"))
	held, err := lock.Acquire(sgDir, sgDir, lock.OperationRef, "test-holder", 5*time.Second)
	if err != nil {
		t.Fatalf("the test could not take the operation lock it needs to hold: %v", err)
	}

	type result struct {
		stderr string
		code   int
	}
	done := make(chan result, 1)
	go func() {
		_, stderr, code := runSafegit(t, dir, "commit", "-m", "while waiting", "--", "unrelated.txt")
		done <- result{stderr: stderr, code: code}
	}()

	// Let the commit reach the lock and block on it. It cannot have read the
	// sequencer state yet if the read happens under the lock, which is the
	// property under test.
	time.Sleep(500 * time.Millisecond)
	select {
	case r := <-done:
		t.Fatalf("commit finished (code %d) while the operation lock was held: %s", r.code, oneLine(r.stderr))
	default:
	}

	// The state a concurrent passthrough would have created appears now, in the
	// window that used to be invisible to the waiting commit.
	if out, code := testutil.GitTry(t, dir, "merge", feature); code == 0 {
		t.Fatalf("the fixture needs a merge conflict; git merge succeeded:\n%s", out)
	}
	testutil.AssertMergeHead(t, dir, testutil.Rev(t, dir, feature), "the fixture must be mid-merge")

	if err := held.Release(); err != nil {
		t.Fatalf("releasing the held lock: %v", err)
	}

	select {
	case r := <-done:
		if r.code == 0 {
			t.Fatalf("the commit ran against a repository that went mid-merge while it waited "+
				"(HEAD %s -> %s); the in-flight check was made before the lock, not under it",
				before, testutil.Rev(t, dir, "HEAD"))
		}
		if r.code != exitcode.CoordinationBusy {
			t.Errorf("the commit exited %d, want %d (CoordinationBusy): %s",
				r.code, exitcode.CoordinationBusy, oneLine(r.stderr))
		}
		if !strings.Contains(strings.ToLower(r.stderr), "merge") {
			t.Errorf("the refusal does not name the merge: %s", oneLine(r.stderr))
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the commit never finished after the lock was released")
	}

	if head := testutil.Rev(t, dir, "HEAD"); head != before {
		t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, before)
	}
}

// A stale operation lock is recoverable exactly the way a stale ref lock is:
// doctor names it, and unlock releases it by that name. Before the naming
// grammar, `unlock safegit/operation` looked for a branch called
// refs/heads/safegit/operation and reported that no lock was held.
func TestStaleOperationLockIsListedByDoctorAndReleasedByUnlock(t *testing.T) {
	dir := newRepo(t)
	lockPath := operationLockPath(dir)
	plantStaleLock(t, lockPath)

	stdout, stderr, code := runSafegit(t, dir, "doctor", "--action", "diagnose")
	if code != 0 {
		t.Fatalf("doctor exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "stale lock") {
		t.Errorf("doctor did not report the stale operation lock:\n%s", stdout)
	}
	if !strings.Contains(stdout, lock.OperationRef) {
		t.Errorf("doctor did not NAME the stale lock, so the operator cannot act on it:\n%s", stdout)
	}

	stdout, stderr, code = runSafegit(t, dir, "unlock", lock.OperationRef)
	if code != 0 {
		t.Fatalf("unlock %s exited %d: %s", lock.OperationRef, code, stderr)
	}
	if !strings.Contains(stdout, "released") {
		t.Errorf("unlock said nothing about releasing the lock: %s", oneLine(stdout))
	}
	if testutil.FileExists(lockPath) {
		t.Error("the stale operation lock survived unlock")
	}

	// And the worktree works again.
	testutil.WriteFile(t, dir, "after.txt", "after\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "after the unlock", "--", "after.txt"); code != 0 {
		t.Fatalf("committing after the unlock failed (code %d): %s", code, stderr)
	}
}

// The repository-wide rewrite lock is addressable by the same grammar. It was
// not before: every argument without a refs/ prefix had refs/heads/ prepended,
// so the lock most likely to be left behind by a crashed history rewrite was the
// one lock unlock could not reach.
func TestStaleRewriteLockIsUnlockable(t *testing.T) {
	dir := newRepo(t)
	sharedDir := repo.SafegitDir(filepath.Join(dir, ".git"))
	lockPath := lock.Path(sharedDir, lock.RewriteRef)
	plantStaleLock(t, lockPath)

	stdout, stderr, code := runSafegit(t, dir, "unlock", lock.RewriteRef)
	if code != 0 {
		t.Fatalf("unlock %s exited %d: %s", lock.RewriteRef, code, stderr)
	}
	if !strings.Contains(stdout, "released") {
		t.Errorf("unlock said nothing about releasing the lock: %s", oneLine(stdout))
	}
	if testutil.FileExists(lockPath) {
		t.Error("the stale rewrite lock survived unlock")
	}

	// A name in the tool-owned namespace that is not a real lock is an error
	// listing the ones that are, never a silent reinterpretation as a branch.
	_, stderr, code = runSafegit(t, dir, "unlock", "safegit/not-a-lock")
	if code == 0 {
		t.Error("unlock accepted an unknown safegit/ lock name")
	}
	if !strings.Contains(stderr, lock.RewriteRef) || !strings.Contains(stderr, lock.OperationRef) {
		t.Errorf("the error does not list the tool-owned lock names: %s", oneLine(stderr))
	}
}

// doctor's lock walk must never mistake a publication temporary file for a
// lock. Reporting one as a stale lock would send an operator to `safegit unlock`
// with a name that is not a lock; cleaning one that a live process is publishing
// through would turn that process's link(2) into a spurious failure.
func TestDoctorDistinguishesPublicationTempsFromLocks(t *testing.T) {
	dir := newRepo(t)
	sgDir := repo.SafegitDir(filepath.Join(dir, ".git"))
	lockDir := filepath.Dir(lock.Path(sgDir, "refs/heads/main"))
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatal(err)
	}

	// A temp a live publication is in the middle of: recent, and naming this
	// very (live) test process.
	fresh := filepath.Join(lockDir, ".main.lock.tmp-000000")
	if err := os.WriteFile(fresh, []byte(fmt.Sprintf("pid=%d\nop=commit\n", os.Getpid())), 0644); err != nil {
		t.Fatal(err)
	}

	// A temp a kill left behind: dead holder, and old enough to be past any
	// publication in progress.
	orphan := filepath.Join(lockDir, ".main.lock.tmp-111111")
	if err := os.WriteFile(orphan, []byte("pid=999999999\nop=commit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runSafegit(t, dir, "doctor", "--action", "diagnose")
	if code != 0 {
		t.Fatalf("doctor exited %d: %s", code, stderr)
	}
	if strings.Contains(stdout, "stale lock") {
		t.Errorf("doctor counted a publication temp as a stale lock:\n%s", stdout)
	}
	if !strings.Contains(stdout, "temp file") {
		t.Errorf("doctor did not report the orphaned publication temp:\n%s", stdout)
	}

	if _, stderr, code := runSafegit(t, dir, "doctor", "--action", "fix"); code != 0 {
		t.Fatalf("doctor --action fix exited %d: %s", code, stderr)
	}
	if testutil.FileExists(orphan) {
		t.Error("doctor --action fix left the orphaned publication temp behind")
	}
	if !testutil.FileExists(fresh) {
		t.Error("doctor --action fix removed a temp a live process is publishing through")
	}
}
