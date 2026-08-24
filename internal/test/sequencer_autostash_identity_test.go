package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// The OTHER half of "whose autostash is this".
//
// A conclusion consumes MERGE_AUTOSTASH only when the commit it names is the
// stash git made for THE MERGE BEING CONCLUDED, and two facts answer that
// together: the stash commit's first parent is the tip the conclusion commits
// onto, and its message carries git's autostash shape ("On <branch>:
// autostash"). The message half is what refuses a plain `git stash create`
// somebody planted, and it is recorded by the probes in
// internal/git/autostash_probe_test.go.
//
// This file covers the FIRST-PARENT half, which no probe can reach: the stash
// here is one git itself made, with git's own message on it, left behind by an
// autostashed merge that was ABANDONED. Only the parent tells it apart from the
// current merge's own -- and applying it would put a stranger's uncommitted work
// into the working tree and then delete the one file naming it.

// abandonedAutostashFixture is a repository parked in a NEW conflicted merge
// whose git directory carries the genuine MERGE_AUTOSTASH of an EARLIER merge,
// abandoned, with the branch moved on since.
type abandonedAutostashFixture struct {
	dir string
	// stale is the genuine autostash commit of the abandoned merge.
	stale string
	// stashed is the path that commit holds uncommitted work for, and committed
	// is what it holds in the commit the fixture ends on -- so any change to it
	// came from the stale stash being applied.
	stashed   string
	committed string
}

func newAbandonedAutostashRepo(t *testing.T) abandonedAutostashFixture {
	t.Helper()
	dir := newRepo(t)

	const committed = "the committed content\n"
	testutil.WriteFile(t, dir, "conflicted.txt", "l1\nbase\nl3\n")
	testutil.WriteFile(t, dir, "unrelated.txt", committed)
	safegitCommitEnv(t, dir, conclusionSession, "base", "conflicted.txt", "unrelated.txt")

	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "-q", "feature")
	testutil.WriteFile(t, dir, "conflicted.txt", "l1\nfeature\nl3\n")
	safegitCommitEnv(t, dir, conclusionSession, "feature edit", "conflicted.txt")

	testutil.Git(t, dir, "switch", "-q", "main")
	testutil.WriteFile(t, dir, "conflicted.txt", "l1\nmain\nl3\n")
	safegitCommitEnv(t, dir, conclusionSession, "main edit", "conflicted.txt")

	// The FIRST merge: autostashed, conflicted, and then abandoned. Raw git,
	// because safegit's merge refuses a dirty working tree outright -- which is
	// why an autostash only ever arrives through git.
	testutil.WriteFile(t, dir, "unrelated.txt", "work from a merge that was abandoned\n")
	if out, code := testutil.GitTry(t, dir, "merge", "--autostash", "feature"); code == 0 {
		t.Fatalf("the first merge was expected to conflict:\n%s", out)
	}
	stale := strings.TrimSpace(testutil.GitOut(t, dir, "rev-parse", "MERGE_AUTOSTASH"))
	if stale == "" {
		t.Fatal("git wrote no MERGE_AUTOSTASH for the autostashed merge")
	}
	// git's own shape, asserted rather than assumed: this fixture's whole point
	// is a stash the MESSAGE check cannot refuse.
	if subject := strings.TrimSpace(testutil.Git(t, dir, "log", "-1", "--format=%s", stale)); !strings.HasSuffix(subject, ": autostash") {
		t.Fatalf("the stale stash's subject is %q; the fixture needs git's genuine autostash shape", subject)
	}
	testutil.Git(t, dir, "merge", "--abort")

	// The branch moves on, so the abandoned stash's first parent is no longer
	// the tip anything is built on. The stashed file goes back to its committed
	// content, so the working tree is clean for the next merge.
	testutil.WriteFile(t, dir, "unrelated.txt", committed)
	testutil.WriteFile(t, dir, "later.txt", "later work\n")
	safegitCommitEnv(t, dir, conclusionSession, "a later commit", "later.txt")
	if status := testutil.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Fatalf("the working tree must be clean before the new merge:\n%s", status)
	}

	// The SECOND merge, which conflicts and never had an autostash of its own.
	stdout, stderr, code := runSafegitEnv(t, dir, conclusionSession, "merge", "feature")
	if code == 0 {
		t.Fatalf("safegit merge feature succeeded; the fixture needs a conflict\nstdout=%s stderr=%s", stdout, stderr)
	}
	testutil.WriteFileAt(t, filepath.Join(dir, ".git", "MERGE_AUTOSTASH"), stale+"\n")
	testutil.WriteFile(t, dir, "conflicted.txt", "l1\nresolved\nl3\n")

	return abandonedAutostashFixture{dir: dir, stale: stale, stashed: "unrelated.txt", committed: committed}
}

// TestConclusionDoesNotConsumeAnAbandonedMergesAutostash: the stash is genuine,
// so only its first parent says it belongs to a merge that is over.
func TestConclusionDoesNotConsumeAnAbandonedMergesAutostash(t *testing.T) {
	fx := newAbandonedAutostashRepo(t)

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=worktree")
	combined := stdout + stderr

	if got := readWorktree(t, fx.dir, fx.stashed); got != fx.committed {
		t.Errorf("%s = %q, want the committed content %q: the conclusion applied the abandoned merge's autostash (exit %d)\n%s",
			fx.stashed, got, fx.committed, code, combined)
	}
	if strings.Contains(combined, "Applied autostash") {
		t.Errorf("the conclusion announced applying an autostash that belongs to an abandoned merge:\n%s", combined)
	}

	// Not consumed is not the same as thrown away: the file names work held
	// nowhere else, so it stays, and the conclusion says it is there.
	if !testutil.FileExists(filepath.Join(fx.dir, ".git", "MERGE_AUTOSTASH")) {
		t.Error("the conclusion removed MERGE_AUTOSTASH without applying it, so the work it named is unreachable")
	}
	if !strings.Contains(combined, "MERGE_AUTOSTASH") {
		t.Errorf("the conclusion says nothing about the autostash it left behind:\n%s", combined)
	}
	if !strings.Contains(combined, fx.stale) {
		t.Errorf("the conclusion does not name the commit %s the file points at:\n%s", fx.stale, combined)
	}

	// The commit itself stands -- this is aftercare, not a refusal -- and it is
	// the merge commit it was asked for.
	if parents := testutil.Parents(t, fx.dir, testutil.Rev(t, fx.dir, "HEAD")); len(parents) != 2 {
		t.Errorf("HEAD has parents %v, want the two-parent merge commit the conclusion created", parents)
	}
	if code == 0 {
		t.Errorf("the conclusion exited 0 while leaving an unconsumed autostash behind:\n%s", combined)
	}
}

// blockTheWorktreeWrite makes the working-tree write a conclusion owes fail, by
// putting a non-empty DIRECTORY where the resolved file has to be written.
//
// It is the one way to stop the aftercare BEFORE the autostash step without
// touching the autostash itself: the commit is made, the state files go, and
// the chain stops at the last step of finishConclusion.
func blockTheWorktreeWrite(t *testing.T, dir, rel string) {
	t.Helper()
	abs := filepath.Join(dir, rel)
	if err := os.Remove(abs); err != nil {
		t.Fatalf("removing %s: %v", rel, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatalf("putting a directory in place of %s: %v", rel, err)
	}
	testutil.WriteFileAt(t, filepath.Join(abs, "occupant.txt"), "this directory is not empty\n")
}

// TestUnreachedAutostashDoesNotClaimWorkItNeverChecked: when the aftercare
// stops before the autostash step, the line about MERGE_AUTOSTASH must not call
// what it names "your uncommitted work" without having asked whose it is.
//
// The ownership question is answerable right there -- the tip the conclusion
// committed onto is in hand -- and a foreign stash is exactly the file this
// arm is most likely to be looking at, since a merge whose aftercare failed is
// a repository something else already went wrong in.
func TestUnreachedAutostashDoesNotClaimWorkItNeverChecked(t *testing.T) {
	fx := newAbandonedAutostashRepo(t)
	blockTheWorktreeWrite(t, fx.dir, "conflicted.txt")

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=ours")
	combined := stdout + stderr
	if code == 0 {
		t.Fatalf("the blocked working-tree write must make the run exit nonzero:\n%s", combined)
	}
	if !strings.Contains(combined, "MERGE_AUTOSTASH") {
		t.Fatalf("the run says nothing about the autostash it did not reach; the fixture no longer produces the arm:\n%s", combined)
	}
	if strings.Contains(combined, "your uncommitted work") {
		t.Errorf("the run calls a stash this merge did not set aside 'your uncommitted work'; it names %s, whose first parent is not the tip this merge was built on:\n%s",
			fx.stale, combined)
	}
	if !strings.Contains(combined, "untouched") {
		t.Errorf("the run does not say the file was left untouched:\n%s", combined)
	}
	// Untouched means untouched: nothing was applied and nothing removed.
	if !testutil.FileExists(filepath.Join(fx.dir, ".git", "MERGE_AUTOSTASH")) {
		t.Error("MERGE_AUTOSTASH is gone; an unreached autostash is neither applied nor removed")
	}
	if got := readWorktree(t, fx.dir, fx.stashed); got != fx.committed {
		t.Errorf("%s = %q, want the committed content %q: the stale stash was applied", fx.stashed, got, fx.committed)
	}
}

// TestUnreachedAutostashStillClaimsTheMergesOwn is the other half of the same
// wording: where the ownership key PASSES, the line says whose work it is,
// because that is the fact an operator needs to go and get it back.
func TestUnreachedAutostashStillClaimsTheMergesOwn(t *testing.T) {
	fx := newAutostashMergeRepo(t, false)
	blockTheWorktreeWrite(t, fx.dir, "conflicted.txt")

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=ours")
	combined := stdout + stderr
	if code == 0 {
		t.Fatalf("the blocked working-tree write must make the run exit nonzero:\n%s", combined)
	}
	if !strings.Contains(combined, "your uncommitted work") {
		t.Errorf("the autostash here IS this merge's own, and the run does not say so:\n%s", combined)
	}
	if !testutil.FileExists(filepath.Join(fx.dir, ".git", "MERGE_AUTOSTASH")) {
		t.Error("MERGE_AUTOSTASH is gone; the aftercare stopped before the step that consumes it")
	}
}
