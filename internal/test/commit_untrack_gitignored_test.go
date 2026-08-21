package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// The cleanup workflow every git user knows -- add a pattern to .gitignore,
// `git rm -r --cached` the copies that are already tracked, commit both in one
// commit -- has no safegit-mediated form today. These two tests pin the two
// halves of that report: the commit that should express the whole cleanup, and
// the fallback attempt that commits only .gitignore.

// seedTrackedThenIgnoredFile builds the exact starting state of the report: a
// repo where dir/junk.txt is tracked and committed, .gitignore has just grown a
// `dir/` pattern (committed nowhere yet), and the operator has already run
// `git rm -r --cached dir` so the removal is staged while the file stays on
// disk.
func seedTrackedThenIgnoredFile(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "dir/junk.txt", "build artifact\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "add junk", "--", "dir/junk.txt"); code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}

	// The pattern that makes the tracked file ignored from now on.
	testutil.WriteFile(t, dir, ".gitignore", "dir/\n")

	// The operator's own step 2: stage the removal, keep the file on disk.
	testutil.GitRaw(t, dir, "rm", "-r", "--cached", "dir")

	if _, err := os.Stat(filepath.Join(dir, "dir", "junk.txt")); err != nil {
		t.Fatalf("dir/junk.txt must survive `git rm --cached` on disk: %v", err)
	}
	return dir
}

// TestCommitUntrackGitignoredPath is the headline case: one safegit commit that
// records both the new .gitignore pattern and the removal of the now-ignored
// file from tracking. The file is gitignored on purpose -- that is the point of
// the operation, not a mistake -- and it must stay on disk afterwards.
func TestCommitUntrackGitignoredPath(t *testing.T) {
	dir := seedTrackedThenIgnoredFile(t)

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "untrack", "--", ".gitignore", "dir/junk.txt")
	if code != 0 {
		t.Fatalf("safegit commit of the untrack cleanup failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	diff := testutil.GitRaw(t, dir, "diff-tree", "--no-commit-id", "-r", "--name-status", "HEAD")
	if !strings.Contains(diff, "\t.gitignore") {
		t.Errorf("expected the .gitignore change in the commit, got:\n%s", diff)
	}
	if !strings.Contains(diff, "D\tdir/junk.txt") {
		t.Errorf("expected dir/junk.txt to be deleted from the tree by the commit, got:\n%s", diff)
	}

	tree := testutil.GitRaw(t, dir, "ls-tree", "-r", "--name-only", "HEAD")
	if strings.Contains(tree, "dir/junk.txt") {
		t.Errorf("dir/junk.txt should no longer be tracked, HEAD tree:\n%s", tree)
	}

	if _, err := os.Stat(filepath.Join(dir, "dir", "junk.txt")); err != nil {
		t.Errorf("dir/junk.txt must remain on disk after being untracked: %v", err)
	}
}

// TestCommitGitignoreOnlyKeepsPreStagedRemoval is the fallback the report tried
// after the case above was refused: commit only the non-ignored path and hope
// the pre-staged removals survive for a later commit. They do -- the commit
// reconciles the shared index through the one authority that preserves every
// staged change the parent tip does not account for -- and this test pins that,
// so a regression to "the index is rebuilt from the new HEAD and the operator's
// staged removal is gone" is loud.
func TestCommitGitignoreOnlyKeepsPreStagedRemoval(t *testing.T) {
	dir := seedTrackedThenIgnoredFile(t)

	before := testutil.GitRaw(t, dir, "diff", "--cached", "--name-status")
	t.Logf("staged before safegit commit:\n%s", before)
	if !strings.Contains(before, "D\tdir/junk.txt") {
		t.Fatalf("precondition: the removal must be staged before the commit, got:\n%s", before)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "ignore dir", "--", ".gitignore")
	if code != 0 {
		t.Fatalf("safegit commit of .gitignore alone failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	after := testutil.GitRaw(t, dir, "diff", "--cached", "--name-status")
	status := testutil.Git(t, dir, "status", "--porcelain")
	lsFiles, _ := testutil.GitTry(t, dir, "ls-files", "--", "dir/junk.txt")
	t.Logf("staged after safegit commit: %q", after)
	t.Logf("git status --porcelain after safegit commit: %q", status)
	t.Logf("git ls-files dir/junk.txt after safegit commit: %q", lsFiles)

	// The commit records only .gitignore, and the operator's pre-staged removal
	// is still staged afterwards: the shared index is reconciled against the
	// commit's PARENT, so a staged change the parent does not account for is
	// replayed over the new HEAD instead of being erased by it. The operator
	// can commit the removal in a second commit, which is the whole point of
	// the fallback.
	if !strings.Contains(after, "D\tdir/junk.txt") {
		t.Errorf("the pre-staged removal did not survive the commit -- another session's staged work was destroyed (staged: %q)", after)
	}
	if strings.TrimSpace(lsFiles) != "" {
		t.Errorf("dir/junk.txt is in the index again, so the staged removal was undone (ls-files: %q)", lsFiles)
	}
	if _, err := os.Stat(filepath.Join(dir, "dir", "junk.txt")); err != nil {
		t.Errorf("dir/junk.txt must stay on disk: the removal was staged with --cached: %v", err)
	}

	diff := testutil.GitRaw(t, dir, "diff-tree", "--no-commit-id", "-r", "--name-status", "HEAD")
	if strings.Contains(diff, "dir/junk.txt") {
		t.Errorf("the .gitignore-only commit should not touch dir/junk.txt, got:\n%s", diff)
	}
}
