package test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// assertCommittedIndexIsClean is the shared verdict of the tests below: after
// safegit reports a commit, the shared index must hold the bytes the commit
// recorded, so the repository reports nothing pending at all.
//
// All three readings are asserted because each alone passes for the wrong
// reason: `git diff HEAD` compares the WORKING TREE against the commit and was
// empty even while the index was wrong; `git status --porcelain` is the reading
// that exposes the index; and `git diff --cached` names the exact blob the
// index kept.
func assertCommittedIndexIsClean(t *testing.T, dir string) {
	t.Helper()
	if diff := testutil.Git(t, dir, "diff", "HEAD"); diff != "" {
		t.Errorf("the working tree does not match HEAD after the commit:\n%s", diff)
	}
	if cached := testutil.Git(t, dir, "diff", "--cached"); cached != "" {
		t.Errorf("the shared index does not match HEAD after the commit; "+
			"`git diff --cached` says:\n%s", cached)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); status != "" {
		t.Errorf("the commit left paths pending in the shared index; "+
			"`git status --porcelain` says:\n%s", status)
	}
}

// assertRecommitFindsNothing performs the remedy a caller reaches for when the
// index looks wrong -- committing the same path again -- and asserts safegit
// refuses it as nothing to commit rather than failing some other way. With the
// index left stale the caller is stuck: HEAD is already right, so this refusal
// is all they can get, and nothing they can type repairs the index.
func assertRecommitFindsNothing(t *testing.T, dir string, paths ...string) {
	t.Helper()
	args := append([]string{"commit", "-m", "re-commit", "--"}, paths...)
	stdout, stderr, code := runSafegit(t, dir, args...)
	if code != exitcode.PathMatchedNothing {
		t.Errorf("re-committing %s after the commit exited %d, want %d (PathMatchedNothing)\nstdout: %s\nstderr: %s",
			strings.Join(paths, ", "), code, exitcode.PathMatchedNothing, stdout, stderr)
	}
	if !strings.Contains(stderr, "nothing to commit") {
		t.Errorf("re-committing %s did not report nothing to commit; stderr: %s",
			strings.Join(paths, ", "), stderr)
	}
}

// A tracked file is renamed with `git mv`, which stages the rename in the
// shared index, and the file is then edited in place before being committed.
// The index therefore holds the PRE-EDIT blob at the new path while the working
// tree holds the edited one.
//
// safegit recorded the commit correctly -- HEAD carried the edited bytes at the
// new path -- and then replayed that pre-edit index slot on top of the tree it
// had just synced, because the reconciler preserved any index slot differing
// from the pre-operation tip as another session's staged work. It was not
// another session's: it was the very path this invocation had just committed,
// and the repository was left reporting `MM docs/b.md` for a file safegit had
// reported as committed. No later safegit commit repairs it, because HEAD is
// already right and only the index is wrong.
func TestCommitClearsStagedRenameOfEditedPath(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "docs/a.md", "original\n")
	safegitCommit(t, dir, "add docs/a.md", "docs/a.md")

	testutil.GitRaw(t, dir, "mv", "docs/a.md", "docs/b.md")
	testutil.WriteFile(t, dir, "docs/b.md", "edited\n")

	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "rename and edit",
		"--", "docs/a.md", "docs/b.md"); code != 0 {
		t.Fatalf("commit of the renamed and edited path failed (code %d): %s", code, stderr)
	}

	if got := testutil.MustShow(t, dir, "HEAD", "docs/b.md"); got != "edited\n" {
		t.Fatalf("HEAD does not carry the edited content at the new path: %q", got)
	}
	assertCommittedIndexIsClean(t, dir)
	assertRecommitFindsNothing(t, dir, "docs/b.md")
}

// The directory form of the same shape: a whole directory is renamed with
// `git mv`, several files under it are edited, and every path is named on one
// commit. Each edited file carries its own stale slot, so the defect scales
// with the rename rather than appearing once.
func TestCommitClearsStagedDirectoryRenameOfEditedPaths(t *testing.T) {
	dir := newRepo(t)
	const edited = 3
	for i := 0; i < edited; i++ {
		testutil.WriteFile(t, dir, fmt.Sprintf("examples/x/f%d.txt", i), fmt.Sprintf("original %d\n", i))
	}
	testutil.WriteFile(t, dir, "examples/x/untouched.txt", "untouched\n")
	safegitCommit(t, dir, "add examples/x", "examples/x")

	testutil.GitRaw(t, dir, "mv", "examples/x", "examples/y")
	for i := 0; i < edited; i++ {
		testutil.WriteFile(t, dir, fmt.Sprintf("examples/y/f%d.txt", i), fmt.Sprintf("edited %d\n", i))
	}

	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "rename the directory and edit its files",
		"--", "examples/x", "examples/y"); code != 0 {
		t.Fatalf("commit of the renamed directory failed (code %d): %s", code, stderr)
	}

	for i := 0; i < edited; i++ {
		path := fmt.Sprintf("examples/y/f%d.txt", i)
		want := fmt.Sprintf("edited %d\n", i)
		if got := testutil.MustShow(t, dir, "HEAD", path); got != want {
			t.Fatalf("HEAD does not carry the edited content at %s: %q", path, got)
		}
	}
	if got := testutil.MustShow(t, dir, "HEAD", "examples/y/untouched.txt"); got != "untouched\n" {
		t.Fatalf("the unedited file did not move with the directory: %q", got)
	}
	assertCommittedIndexIsClean(t, dir)
	assertRecommitFindsNothing(t, dir, "examples/y")
}

// A rename with NO edit: `git mv` stages the move and nothing else touches the
// file. The index slot at the new path then already agrees with what the commit
// records, so this shape is the control -- it must stay clean for the same
// reason the edited one must become clean.
func TestCommitClearsStagedRenameWithoutEdit(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "docs/a.md", "original\n")
	safegitCommit(t, dir, "add docs/a.md", "docs/a.md")

	testutil.GitRaw(t, dir, "mv", "docs/a.md", "docs/b.md")

	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "rename",
		"--", "docs/a.md", "docs/b.md"); code != 0 {
		t.Fatalf("commit of the renamed path failed (code %d): %s", code, stderr)
	}

	if got := testutil.MustShow(t, dir, "HEAD", "docs/b.md"); got != "original\n" {
		t.Fatalf("HEAD does not carry the content at the new path: %q", got)
	}
	assertCommittedIndexIsClean(t, dir)
}

// A staged COPY plus an edit: the new path is staged from an existing blob with
// `git add` while the source stays where it is, and the copy is then edited
// before the commit names it. The index holds the pre-edit blob at a path that
// the pre-operation tip does not have at all -- the staged-ADDITION half of the
// same defect, rather than the staged-modification half a rename produces.
func TestCommitClearsStagedCopyOfEditedPath(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "docs/a.md", "original\n")
	safegitCommit(t, dir, "add docs/a.md", "docs/a.md")

	testutil.WriteFile(t, dir, "docs/b.md", "original\n")
	testutil.GitRaw(t, dir, "add", "docs/b.md")
	testutil.WriteFile(t, dir, "docs/b.md", "edited\n")

	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "copy and edit",
		"--", "docs/b.md"); code != 0 {
		t.Fatalf("commit of the staged copy failed (code %d): %s", code, stderr)
	}

	if got := testutil.MustShow(t, dir, "HEAD", "docs/b.md"); got != "edited\n" {
		t.Fatalf("HEAD does not carry the edited content at the copied path: %q", got)
	}
	if got := testutil.MustShow(t, dir, "HEAD", "docs/a.md"); got != "original\n" {
		t.Fatalf("the copy's source did not stay as it was: %q", got)
	}
	assertCommittedIndexIsClean(t, dir)
	assertRecommitFindsNothing(t, dir, "docs/b.md")
}

// A staged MODE change plus an edit: the executable bit is staged with
// `git update-index --chmod`, the file is then edited, and the commit names it.
// Mode and content are separate halves of an index slot, so this asserts the
// reconciled slot agrees with the commit on BOTH.
func TestCommitClearsStagedModeChangeOfEditedPath(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "tool.sh", "original\n")
	safegitCommit(t, dir, "add tool.sh", "tool.sh")

	testutil.GitRaw(t, dir, "update-index", "--chmod=+x", "tool.sh")
	testutil.WriteFile(t, dir, "tool.sh", "edited\n")

	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "make executable and edit",
		"--", "tool.sh"); code != 0 {
		t.Fatalf("commit of the mode change failed (code %d): %s", code, stderr)
	}

	if got := testutil.MustShow(t, dir, "HEAD", "tool.sh"); got != "edited\n" {
		t.Fatalf("HEAD does not carry the edited content: %q", got)
	}
	assertCommittedIndexIsClean(t, dir)
	assertRecommitFindsNothing(t, dir, "tool.sh")
}

// The fix must not reach past the paths the invocation named. Another session's
// staged modification of a path this commit says nothing about is real work and
// still has to outlive the commit, exactly as a staged deletion of an unnamed
// path does.
func TestCommitKeepsForeignStagedModificationThroughRename(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "docs/a.md", "original\n")
	testutil.WriteFile(t, dir, "theirs.txt", "theirs\n")
	safegitCommit(t, dir, "seed", "docs/a.md", "theirs.txt")

	// Another session stages a change to a path this commit never names.
	testutil.WriteFile(t, dir, "theirs.txt", "their staged work\n")
	testutil.GitRaw(t, dir, "add", "theirs.txt")

	testutil.GitRaw(t, dir, "mv", "docs/a.md", "docs/b.md")
	testutil.WriteFile(t, dir, "docs/b.md", "edited\n")

	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "rename and edit",
		"--", "docs/a.md", "docs/b.md"); code != 0 {
		t.Fatalf("commit of the renamed and edited path failed (code %d): %s", code, stderr)
	}

	if staged := stagedStatusOf(undoSyncStagedStatus(t, dir), "theirs.txt"); staged != "M" {
		t.Errorf("the other session's staged modification of theirs.txt was lost (staged status %q)", staged)
	}
	if got := testutil.MustShow(t, dir, "HEAD", "theirs.txt"); got != "theirs\n" {
		t.Errorf("the other session's staged work reached the commit: %q", got)
	}
	if staged := stagedStatusOf(undoSyncStagedStatus(t, dir), "docs/b.md"); staged != "" {
		t.Errorf("docs/b.md is still staged as %q after being committed", staged)
	}
	if diff := testutil.Git(t, dir, "diff", "HEAD", "--", "docs/b.md"); diff != "" {
		t.Errorf("the working tree does not match HEAD at the committed path:\n%s", diff)
	}
}
