package test

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// What `safegit undo` does when commits it did NOT create sit between the
// rollback target and where the branch actually stands.
//
// undo's arithmetic is oplog arithmetic: it counts back N of its own recorded
// operations and moves the ref to what the Nth one was made on top of. Nothing
// in that arithmetic looks at the branch, so a commit somebody else put there
// -- a plain `git commit`, a passthrough cherry-pick, a conclusion git authored
// on safegit's behalf -- is simply in the way, and moving the ref past it
// removes it from the branch's history.
//
// The rule the tests below pin: every commit in the range the ref would move
// back over must be one the oplog says this undo is reversing. Anything else is
// a refusal that NAMES the commits, in the preview exactly as in the real run.

// undoForeignSession is the handshake these tests spawn safegit with.
var undoForeignSession = []string{"CLAUDE_CODE_SESSION_ID=undo-foreign-test"}

// gitCommit makes a commit with git itself, so nothing about it is in safegit's
// oplog. It is the plainest possible foreign commit.
func gitCommit(t *testing.T, dir, path, content, message string) string {
	t.Helper()
	testutil.WriteFile(t, dir, path, content)
	testutil.Git(t, dir, "add", path)
	testutil.Git(t, dir, "commit", "-q", "-m", message)
	return testutil.Rev(t, dir, "HEAD")
}

// assertNamedForeignRefusal is the shape every refusal in this file has: it
// names the commit that is in the way, says why undo cannot reverse it, and is
// not the raw plumbing error the compare-and-swap would have produced.
func assertNamedForeignRefusal(t *testing.T, stderr string, foreign ...string) {
	t.Helper()
	for _, sha := range foreign {
		if !strings.Contains(stderr, sha[:7]) {
			t.Errorf("the refusal does not name the foreign commit %s:\n%s", sha[:7], stderr)
		}
	}
	if !strings.Contains(stderr, "did not create") {
		t.Errorf("the refusal does not say the commits are not safegit's:\n%s", stderr)
	}
	if strings.Contains(stderr, "update-ref failed") {
		t.Errorf("the refusal is the raw plumbing error rather than the named one:\n%s", stderr)
	}
}

// TestUndoRefusesAGitCommitOnTop is the --count 1 case. The compare-and-swap
// already refused it, but with a plumbing message that named nothing and
// explained nothing.
func TestUndoRefusesAGitCommitOnTop(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "a.txt", "a\n")
	safegitCommitEnv(t, dir, undoForeignSession, "safegit's commit", "a.txt")
	foreign := gitCommit(t, dir, "g.txt", "g\n", "git's own commit")

	_, stderr, code := runSafegitEnv(t, dir, undoForeignSession, "undo")
	if code == 0 {
		t.Fatalf("undo moved the branch past a commit safegit did not make: %s", stderr)
	}
	assertNamedForeignRefusal(t, stderr, foreign)
	if head := testutil.Rev(t, dir, "HEAD"); head != foreign {
		t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, foreign)
	}
}

// TestUndoCountRefusesRatherThanDestroyingAnInterleavedGitCommit is the case
// that was silently destructive.
//
// The compare-and-swap pins the NEWEST recorded tip, so it only ever notices a
// commit on top. A foreign commit with a safegit commit above it leaves the pin
// matching exactly, and `--count 2` walked the ref straight past it: the
// git-authored commit left the branch's history at exit 0, with the report
// saying two operations were undone.
func TestUndoCountRefusesRatherThanDestroyingAnInterleavedGitCommit(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "a.txt", "a\n")
	safegitCommitEnv(t, dir, undoForeignSession, "first", "a.txt")
	foreign := gitCommit(t, dir, "g.txt", "g\n", "git's own commit")
	testutil.WriteFile(t, dir, "b.txt", "b\n")
	tip := safegitCommitEnv(t, dir, undoForeignSession, "second", "b.txt")

	_, stderr, code := runSafegitEnv(t, dir, undoForeignSession, "undo", "--count", "2")
	if code == 0 {
		t.Fatalf("undo --count 2 destroyed a commit safegit did not make: %s", stderr)
	}
	assertNamedForeignRefusal(t, stderr, foreign)
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Fatalf("HEAD moved to %s despite the refusal (was %s)", head, tip)
	}
	if !testutil.Contains(testutil.TreePaths(t, dir, "HEAD"), "g.txt") {
		t.Error("the git-authored commit's content is gone from the branch")
	}
}

// TestUndoCountRefusesRatherThanDestroyingAQueueGitFinished is the second
// flavor of the same destruction, and the one an operator meets by accident: a
// cherry-pick SEQUENCE is git's from end to end -- safegit's own cherry-pick
// applies one commit, and its conclusion refuses a queue -- so the commits it
// makes are git-authored and carry no oplog entry undo can see. One safegit
// commit on top of them puts the compare-and-swap's pin back in agreement with
// the branch, and `--count 2` then rolls the ref back over the whole sequence.
func TestUndoCountRefusesRatherThanDestroyingAQueueGitFinished(t *testing.T) {
	fx := newQueuedPickRepo(t, "cherry-pick")

	// git's own conclusion, because a queue is git's: resolve the conflict and
	// hand the rest of the sequence back to git.
	testutil.WriteFile(t, fx.dir, fx.conflicted, "resolved by hand\n")
	testutil.Git(t, fx.dir, "add", fx.conflicted)
	if out, code := testutil.GitTry(t, fx.dir, "-c", "core.editor=true", "cherry-pick", "--continue"); code != 0 {
		t.Fatalf("git's own cherry-pick --continue failed: %s", out)
	}
	queued := testutil.Rev(t, fx.dir, "HEAD")
	if queued == fx.tip {
		t.Fatal("the queue created nothing, so there is nothing to protect")
	}

	testutil.WriteFile(t, fx.dir, "after.txt", "after\n")
	tip := safegitCommitEnv(t, fx.dir, conclusionSession, "after the queue", "after.txt")

	_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession, "undo", "--count", "2")
	if code == 0 {
		t.Fatalf("undo --count 2 destroyed the commits git authored: %s", stderr)
	}
	assertNamedForeignRefusal(t, stderr, queued)
	if head := testutil.Rev(t, fx.dir, "HEAD"); head != tip {
		t.Fatalf("HEAD moved to %s despite the refusal (was %s)", head, tip)
	}
	if !testutil.Contains(testutil.TreePaths(t, fx.dir, "HEAD"), fx.clean) {
		t.Errorf("the queue's commits' content is gone from the branch")
	}
}

// TestUndoDryRunRefusesWhatTheRealRunRefuses: the preview used to announce a
// rollback the real run would not perform, because it never looked at the ref
// at all. Same check, same verdict, same message.
func TestUndoDryRunRefusesWhatTheRealRunRefuses(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "a.txt", "a\n")
	safegitCommitEnv(t, dir, undoForeignSession, "safegit's commit", "a.txt")
	foreign := gitCommit(t, dir, "g.txt", "g\n", "git's own commit")

	stdout, stderr, code := runSafegitEnv(t, dir, undoForeignSession, "undo", "--dry-run")
	if code == 0 {
		t.Fatalf("the preview announced a rollback the real run refuses:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	assertNamedForeignRefusal(t, stderr, foreign)
	if strings.Contains(stdout, "would undo") {
		t.Errorf("the preview still says it would undo something:\n%s", stdout)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != foreign {
		t.Errorf("the preview moved HEAD to %s", head)
	}
}

// TestUndoOfAMergeConclusionIsNotRefusedForTheMergedSide is the boundary the
// range check has to respect: undoing a merge commit takes the branch back to
// its first parent, and the commits that came in on the OTHER side were never
// created by the operation being reversed. They are not in the range this check
// walks, and a merge conclusion stays undoable.
func TestUndoOfAMergeConclusionIsNotRefusedForTheMergedSide(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "base.txt", "base\n")
	safegitCommitEnv(t, dir, undoForeignSession, "base", "base.txt")

	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "feature")
	gitCommit(t, dir, "side.txt", "side\n", "the side's own commit")
	testutil.Git(t, dir, "switch", "main")

	testutil.WriteFile(t, dir, "main.txt", "main\n")
	before := safegitCommitEnv(t, dir, undoForeignSession, "main moves on", "main.txt")

	if _, stderr, code := runSafegitEnv(t, dir, undoForeignSession, "merge", "--no-ff", "feature"); code != 0 {
		t.Fatalf("the fixture needs a clean merge (code %d): %s", code, stderr)
	}
	if testutil.Rev(t, dir, "HEAD") == before {
		t.Fatal("the merge did not move HEAD")
	}

	// Whether undo reverses the merge commit itself or reaches the safegit
	// commit underneath it, the assertion is the same and it is about the
	// RANGE: the commits that came in on the merge's other side were never
	// created by any operation being reversed, they are not in the first-parent
	// range, and no refusal may name them.
	_, stderr, _ := runSafegitEnv(t, dir, undoForeignSession, "undo")
	sideSHA := testutil.Rev(t, dir, "feature")
	if strings.Contains(stderr, sideSHA[:7]) {
		t.Errorf("the refusal names a commit that came in on the merge's other side:\n%s", stderr)
	}
}
