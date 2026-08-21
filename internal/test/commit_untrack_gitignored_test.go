package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The cleanup workflow every git user knows -- add a pattern to .gitignore,
// `git rm -r --cached` the copies that are already tracked, commit both in one
// commit -- has no safegit-mediated form today. These two tests pin the two
// halves of that report: the commit that should express the whole cleanup, and
// the fallback attempt that commits only .gitignore.

// untrackGitIn runs a raw git command inside a test's scratch repo and fails
// the test if it errors. It stands in for the operator's own `git rm --cached`,
// which is what stages the removals safegit is then asked to commit.
func untrackGitIn(t *testing.T, repoDir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// untrackGitAllowFail runs a raw git command and returns its combined output
// plus exit code without failing the test, for observing state.
func untrackGitAllowFail(t *testing.T, repoDir string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}
	return string(out), code
}

// seedTrackedThenIgnoredFile builds the exact starting state of the report: a
// repo where dir/junk.txt is tracked and committed, .gitignore has just grown a
// `dir/` pattern (committed nowhere yet), and the operator has already run
// `git rm -r --cached dir` so the removal is staged while the file stays on
// disk.
func seedTrackedThenIgnoredFile(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)

	writeRepoFile(t, dir, "dir/junk.txt", "build artifact\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "add junk", "--", "dir/junk.txt"); code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}

	// The pattern that makes the tracked file ignored from now on.
	writeRepoFile(t, dir, ".gitignore", "dir/\n")

	// The operator's own step 2: stage the removal, keep the file on disk.
	untrackGitIn(t, dir, "rm", "-r", "--cached", "dir")

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

	diff := untrackGitIn(t, dir, "diff-tree", "--no-commit-id", "-r", "--name-status", "HEAD")
	if !strings.Contains(diff, "\t.gitignore") {
		t.Errorf("expected the .gitignore change in the commit, got:\n%s", diff)
	}
	if !strings.Contains(diff, "D\tdir/junk.txt") {
		t.Errorf("expected dir/junk.txt to be deleted from the tree by the commit, got:\n%s", diff)
	}

	tree := untrackGitIn(t, dir, "ls-tree", "-r", "--name-only", "HEAD")
	if strings.Contains(tree, "dir/junk.txt") {
		t.Errorf("dir/junk.txt should no longer be tracked, HEAD tree:\n%s", tree)
	}

	if _, err := os.Stat(filepath.Join(dir, "dir", "junk.txt")); err != nil {
		t.Errorf("dir/junk.txt must remain on disk after being untracked: %v", err)
	}
}

// TestCommitGitignoreOnlyDropsPreStagedRemoval is the fallback the report tried
// after the case above was refused: commit only the non-ignored path and hope
// the pre-staged removals survive for a later commit. It asserts the CURRENT
// behavior of the shared index after that commit, whatever that behavior is, so
// any change to it is visible.
func TestCommitGitignoreOnlyDropsPreStagedRemoval(t *testing.T) {
	dir := seedTrackedThenIgnoredFile(t)

	before := untrackGitIn(t, dir, "diff", "--cached", "--name-status")
	t.Logf("staged before safegit commit:\n%s", before)
	if !strings.Contains(before, "D\tdir/junk.txt") {
		t.Fatalf("precondition: the removal must be staged before the commit, got:\n%s", before)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "ignore dir", "--", ".gitignore")
	if code != 0 {
		t.Fatalf("safegit commit of .gitignore alone failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	after := untrackGitIn(t, dir, "diff", "--cached", "--name-status")
	status := gitStatusPorcelain(t, dir)
	lsFiles, _ := untrackGitAllowFail(t, dir, "ls-files", "--", "dir/junk.txt")
	t.Logf("staged after safegit commit: %q", after)
	t.Logf("git status --porcelain after safegit commit: %q", status)
	t.Logf("git ls-files dir/junk.txt after safegit commit: %q", lsFiles)

	// Current behavior, recorded so a change is loud: the commit records only
	// .gitignore, and the shared index is rebuilt from the new HEAD -- so the
	// operator's staged removal is gone and dir/junk.txt is tracked again.
	if strings.Contains(after, "D\tdir/junk.txt") {
		t.Errorf("behavior changed: the pre-staged removal survived the commit (staged: %q)", after)
	}
	if strings.TrimSpace(lsFiles) != "dir/junk.txt" {
		t.Errorf("behavior changed: dir/junk.txt is no longer tracked after the commit (ls-files: %q)", lsFiles)
	}

	diff := untrackGitIn(t, dir, "diff-tree", "--no-commit-id", "-r", "--name-status", "HEAD")
	if strings.Contains(diff, "dir/junk.txt") {
		t.Errorf("the .gitignore-only commit should not touch dir/junk.txt, got:\n%s", diff)
	}
}
