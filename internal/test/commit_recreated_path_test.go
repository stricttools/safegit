package test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// An archiving deletion tool takes a tracked file out of the working tree and
// stages its removal with `git rm --cached`, so the next commit cannot
// resurrect it. Another tool then writes a NEW file at the same path, and the
// caller commits that path.
//
// safegit built and recorded that commit correctly -- HEAD carried the new
// bytes -- and then put the staged deletion back into the shared index,
// because the reconciler read "the pre-operation tip holds this path and the
// index has no slot for it" as another session's staged deletion worth
// preserving. It was not another session's: it was the very path this
// invocation had just committed. The repository was left reporting `D <path>`
// alongside `?? <path>` for a file safegit had reported as committed, which is
// enough for any clean-working-tree check to refuse.
//
// The index and the working tree are both asserted, because either alone would
// pass for the wrong reason: the file never left disk, and HEAD was right all
// along.
func TestCommitClearsStagedDeletionOfRecreatedPath(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "releases/unreleased.toml", "original\n")
	safegitCommit(t, dir, "add unreleased.toml", "releases/unreleased.toml")

	// The deletion tool: the file leaves the working tree and its removal is
	// staged in the shared index.
	if err := os.Remove(filepath.Join(dir, "releases/unreleased.toml")); err != nil {
		t.Fatalf("removing the file the deletion tool archives: %v", err)
	}
	testutil.GitRaw(t, dir, "rm", "--cached", "releases/unreleased.toml")

	// A tool writes a new file at the same path.
	testutil.WriteFile(t, dir, "releases/unreleased.toml", "regenerated\n")

	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "regenerate unreleased.toml",
		"--", "releases/unreleased.toml"); code != 0 {
		t.Fatalf("commit of the recreated path failed (code %d): %s", code, stderr)
	}

	if got := testutil.MustShow(t, dir, "HEAD", "releases/unreleased.toml"); got != "regenerated\n" {
		t.Fatalf("HEAD does not carry the new content: %q", got)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); status != "" {
		t.Errorf("the commit left the path pending in the shared index; "+
			"`git status --porcelain` says:\n%s", status)
	}
	if staged := testutil.Git(t, dir, "ls-files", "--stage", "releases/unreleased.toml"); staged == "" {
		t.Errorf("releases/unreleased.toml has no slot in the shared index after being committed")
	}
}

// The fix must not reach past the paths the invocation named: a staged deletion
// of a path this commit says nothing about is another session's work and still
// has to outlive the commit.
func TestCommitClearsOnlyItsOwnStagedDeletion(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "mine.txt", "original\n")
	testutil.WriteFile(t, dir, "theirs.txt", "theirs\n")
	safegitCommit(t, dir, "seed", "mine.txt", "theirs.txt")

	// One deletion belongs to this commit's path, the other to nobody's.
	if err := os.Remove(filepath.Join(dir, "mine.txt")); err != nil {
		t.Fatalf("removing mine.txt: %v", err)
	}
	testutil.GitRaw(t, dir, "rm", "--cached", "mine.txt")
	testutil.GitRaw(t, dir, "rm", "--cached", "theirs.txt")

	testutil.WriteFile(t, dir, "mine.txt", "regenerated\n")

	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "regenerate mine.txt",
		"--", "mine.txt"); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	if staged := stagedStatusOf(undoSyncStagedStatus(t, dir), "theirs.txt"); staged != "D" {
		t.Errorf("the other session's staged deletion of theirs.txt was lost (staged status %q)", staged)
	}
	if staged := stagedStatusOf(undoSyncStagedStatus(t, dir), "mine.txt"); staged != "" {
		t.Errorf("mine.txt is still staged as %q after being committed", staged)
	}
	if !testutil.FileExists(filepath.Join(dir, "theirs.txt")) {
		t.Errorf("theirs.txt left the working tree; the staged deletion was cache-only")
	}
}
