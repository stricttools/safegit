package sequencer_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// The fixtures in this file build REAL in-flight git states by running the
// commands that produce them, never by writing state files by hand. The whole
// point of the package under test is that it reads what git actually writes, so
// a fixture that fabricates the files would test the fixture.
//
// Every fixture returns the repository's working directory; the git directory
// is always <repoDir>/.git, which is what gitDir() returns.

// newRepo creates a repository with one committed file, "f.txt", on main.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := testutil.InitBareRepo(t)
	testutil.WriteFile(t, dir, "f.txt", "base\n")
	testutil.Git(t, dir, "add", "f.txt")
	testutil.Git(t, dir, "commit", "-m", "base")
	return dir
}

// gitDir returns the git directory of a repository built by these fixtures.
func gitDir(repoDir string) string { return filepath.Join(repoDir, ".git") }

// divergeOnFile builds a branch whose single commit rewrites f.txt one way and
// a main commit that rewrites it another, so any attempt to combine them
// conflicts. It leaves HEAD on main and returns the branch commit's SHA.
func divergeOnFile(t *testing.T, dir, branch string) string {
	t.Helper()
	testutil.Git(t, dir, "checkout", "-q", "-b", branch)
	testutil.WriteFile(t, dir, "f.txt", branch+"\n")
	testutil.Git(t, dir, "commit", "-q", "-am", branch+" change")
	sha := testutil.Rev(t, dir, "HEAD")
	testutil.Git(t, dir, "checkout", "-q", "main")
	testutil.WriteFile(t, dir, "f.txt", "main\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "main change")
	return sha
}

// conflictedMerge leaves the repository stopped in a two-parent merge.
func conflictedMerge(t *testing.T) (repoDir, mergedSHA string) {
	t.Helper()
	dir := newRepo(t)
	side := divergeOnFile(t, dir, "side")
	mustConflict(t, dir, "merge", "side")
	return dir, side
}

// octopusMerge leaves the repository stopped in a merge of three branches, so
// MERGE_HEAD carries three lines. Two of the branches touch files main never
// touched and merge cleanly; the third conflicts, which is what stops the
// octopus with its state written.
func octopusMerge(t *testing.T) (repoDir string, heads []string) {
	t.Helper()
	dir := newRepo(t)
	base := testutil.Rev(t, dir, "HEAD")

	for _, name := range []string{"a", "b"} {
		testutil.Git(t, dir, "checkout", "-q", "-b", name, base)
		testutil.WriteFile(t, dir, name+".txt", name+"\n")
		testutil.Git(t, dir, "add", name+".txt")
		testutil.Git(t, dir, "commit", "-q", "-m", name)
		heads = append(heads, testutil.Rev(t, dir, "HEAD"))
	}

	testutil.Git(t, dir, "checkout", "-q", "-b", "c", base)
	testutil.WriteFile(t, dir, "f.txt", "c\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "c")
	heads = append(heads, testutil.Rev(t, dir, "HEAD"))

	testutil.Git(t, dir, "checkout", "-q", "main")
	testutil.WriteFile(t, dir, "f.txt", "main\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "main change")

	mustConflict(t, dir, "merge", "--no-commit", "--no-ff", "a", "b", "c")
	return dir, heads
}

// singleCherryPick leaves the repository stopped in a one-commit cherry-pick:
// CHERRY_PICK_HEAD and AUTO_MERGE, and no sequencer directory.
func singleCherryPick(t *testing.T) (repoDir, picked string) {
	t.Helper()
	dir := newRepo(t)
	side := divergeOnFile(t, dir, "side")
	mustConflict(t, dir, "cherry-pick", side)
	return dir, side
}

// queuedCherryPick leaves the repository stopped part-way through a two-commit
// cherry-pick, so git's sequencer holds a queue. The first commit touches a
// file main never touched and applies cleanly; the second conflicts.
func queuedCherryPick(t *testing.T) (repoDir, stopped string) {
	t.Helper()
	dir := newRepo(t)
	testutil.Git(t, dir, "checkout", "-q", "-b", "side")
	testutil.WriteFile(t, dir, "g.txt", "one\n")
	testutil.Git(t, dir, "add", "g.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "one")
	first := testutil.Rev(t, dir, "HEAD")
	testutil.WriteFile(t, dir, "f.txt", "side\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "two")
	second := testutil.Rev(t, dir, "HEAD")

	testutil.Git(t, dir, "checkout", "-q", "main")
	testutil.WriteFile(t, dir, "f.txt", "main\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "main change")

	mustConflict(t, dir, "cherry-pick", first, second)
	return dir, second
}

// queuedCherryPickStoppingOnFirst leaves the repository stopped on the FIRST
// commit of a two-commit cherry-pick. The queue exists from the moment the
// sequence starts, not only once a step has succeeded, and this fixture is what
// proves the discriminator does not secretly depend on progress.
func queuedCherryPickStoppingOnFirst(t *testing.T) (repoDir, stopped string) {
	t.Helper()
	dir := newRepo(t)
	testutil.Git(t, dir, "checkout", "-q", "-b", "side")
	testutil.WriteFile(t, dir, "f.txt", "side\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "one")
	first := testutil.Rev(t, dir, "HEAD")
	testutil.WriteFile(t, dir, "g.txt", "two\n")
	testutil.Git(t, dir, "add", "g.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "two")
	second := testutil.Rev(t, dir, "HEAD")

	testutil.Git(t, dir, "checkout", "-q", "main")
	testutil.WriteFile(t, dir, "f.txt", "main\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "main change")

	mustConflict(t, dir, "cherry-pick", first, second)
	return dir, first
}

// queuedCherryPickWithMoreToDo leaves the repository stopped part-way through a
// THREE-commit cherry-pick, with a further commit still queued behind the one
// it stopped on.
//
// The extra commit is what makes the fixture different from queuedCherryPick:
// git's own `git commit` calls its sequencer's post-commit cleanup, which
// removes the whole sequencer state when the commit just made was the last item
// in the todo. With something still queued the state survives, which is the
// only way to reach a queue that names no current step.
func queuedCherryPickWithMoreToDo(t *testing.T) (repoDir string) {
	t.Helper()
	dir := newRepo(t)
	testutil.Git(t, dir, "checkout", "-q", "-b", "side")
	testutil.WriteFile(t, dir, "g.txt", "one\n")
	testutil.Git(t, dir, "add", "g.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "one")
	first := testutil.Rev(t, dir, "HEAD")
	testutil.WriteFile(t, dir, "f.txt", "side\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "two")
	second := testutil.Rev(t, dir, "HEAD")
	testutil.WriteFile(t, dir, "h.txt", "three\n")
	testutil.Git(t, dir, "add", "h.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "three")
	third := testutil.Rev(t, dir, "HEAD")

	testutil.Git(t, dir, "checkout", "-q", "main")
	testutil.WriteFile(t, dir, "f.txt", "main\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "main change")

	mustConflict(t, dir, "cherry-pick", first, second, third)
	return dir
}

// singleRevert leaves the repository stopped in a one-commit revert.
func singleRevert(t *testing.T) (repoDir, reverted string) {
	t.Helper()
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "f.txt", "second\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "second")
	target := testutil.Rev(t, dir, "HEAD")
	testutil.WriteFile(t, dir, "f.txt", "third\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "third")
	mustConflict(t, dir, "revert", target)
	return dir, target
}

// queuedRevert leaves the repository stopped part-way through a two-commit
// revert, so the sequencer holds a queue alongside REVERT_HEAD.
func queuedRevert(t *testing.T) (repoDir, stopped string) {
	t.Helper()
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "g.txt", "g\n")
	testutil.Git(t, dir, "add", "g.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "add g")
	addG := testutil.Rev(t, dir, "HEAD")
	testutil.WriteFile(t, dir, "f.txt", "second\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "second")
	second := testutil.Rev(t, dir, "HEAD")
	testutil.WriteFile(t, dir, "f.txt", "third\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "third")

	// Reverting "add g" applies cleanly; reverting "second" conflicts with
	// "third", which stops the sequence with a queue behind it.
	mustConflict(t, dir, "revert", addG, second)
	return dir, second
}

// rebaseMergeBackend leaves the repository stopped in a rebase running under
// the merge backend, whose state lives in .git/rebase-merge. `git rebase
// --merge` names that backend explicitly; it is also git's default and the
// backend every interactive rebase uses.
func rebaseMergeBackend(t *testing.T) (repoDir, branch string) {
	t.Helper()
	dir := newRepo(t)
	divergeOnFile(t, dir, "side")
	testutil.Git(t, dir, "checkout", "-q", "side")
	mustConflict(t, dir, "rebase", "--merge", "main")
	return dir, "side"
}

// rebaseApplyBackend leaves the repository stopped in a rebase running under
// the apply backend, whose state lives in .git/rebase-apply. `git rebase
// --apply` is the only way to select it; it is not the default and an
// interactive rebase can never use it.
func rebaseApplyBackend(t *testing.T) (repoDir, branch string) {
	t.Helper()
	dir := newRepo(t)
	divergeOnFile(t, dir, "side")
	testutil.Git(t, dir, "checkout", "-q", "side")
	mustConflict(t, dir, "rebase", "--apply", "main")
	return dir, "side"
}

// mailboxApplication leaves the repository mid-`git am`, which stores its state
// in the SAME .git/rebase-apply directory the apply-backend rebase uses and is
// told apart from it only by the marker file git writes there.
func mailboxApplication(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	divergeOnFile(t, dir, "side")
	patch := testutil.GitOut(t, dir, "format-patch", "-1", "--stdout", "side")
	patchPath := filepath.Join(dir, "side.patch")
	testutil.WriteFileAt(t, patchPath, patch)
	mustConflict(t, dir, "am", patchPath)
	return dir
}

// mustConflict runs a git command that is expected to stop with a conflict and
// fails the test when it succeeds instead -- a fixture that quietly completed
// would leave the state under test absent and turn every assertion into a
// tautology.
func mustConflict(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, code := testutil.GitTry(t, dir, args...)
	if code == 0 {
		t.Fatalf("git %v was expected to stop with a conflict but succeeded:\n%s", args, out)
	}
}

// present reports whether a state file or directory exists under gitDir.
func present(t *testing.T, gitDirPath, name string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(gitDirPath, name))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", filepath.Join(gitDirPath, name), err)
	}
	return err == nil
}

// mustBePresent fails unless every named state file or directory exists.
func mustBePresent(t *testing.T, gitDirPath string, names ...string) {
	t.Helper()
	for _, name := range names {
		if !present(t, gitDirPath, name) {
			t.Fatalf("%s was expected to exist in %s but does not", name, gitDirPath)
		}
	}
}

// mustBeAbsent fails unless every named state file or directory is gone.
func mustBeAbsent(t *testing.T, gitDirPath string, names ...string) {
	t.Helper()
	for _, name := range names {
		if present(t, gitDirPath, name) {
			t.Fatalf("%s was expected to be gone from %s but is still there", name, gitDirPath)
		}
	}
}
