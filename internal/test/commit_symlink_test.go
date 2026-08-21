package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// lsTreeHEAD returns `git ls-tree -r HEAD` output, which carries the mode of
// every entry ("120000" for a symlink, "100644" for a regular file). The
// testutil.TreePaths listing drops the modes, which are the whole point here.
func lsTreeHEAD(t *testing.T, repoDir string) string {
	t.Helper()
	return testutil.GitRaw(t, repoDir, "ls-tree", "-r", "HEAD")
}

// treeEntryMode returns the mode of path in the HEAD tree, or "" if absent.
func treeEntryMode(t *testing.T, repoDir, path string) string {
	t.Helper()
	for _, line := range strings.Split(lsTreeHEAD(t, repoDir), "\n") {
		// Format: "<mode> <type> <sha>\t<path>"
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		if line[tab+1:] != path {
			continue
		}
		fields := strings.Fields(line[:tab])
		if len(fields) == 0 {
			continue
		}
		return fields[0]
	}
	return ""
}

// catFileBlob returns the content of the blob at path in the HEAD tree. For a
// symlink entry the blob content IS the link target. It reads the blob rather
// than going through testutil.MustShow so that no path-based interpretation
// (or filter) can stand between the object store and the assertion.
func catFileBlob(t *testing.T, repoDir, path string) string {
	t.Helper()
	return testutil.GitRaw(t, repoDir, "cat-file", "blob", "HEAD:"+path)
}

// TestCommitSymlink_LinkToCommittedFile checks that a symlink passed to
// `safegit commit` is committed as the symlink object itself (mode 120000),
// not resolved away to its target.
//
// Regression: internal/commit/commit.go resolveFiles() runs resolveSymlinks()
// (filepath.EvalSymlinks) on the full path including the final component, so
// the argument "link" collapses to the target "file.txt". Because file.txt is
// already committed unchanged, the resulting tree equals the parent tree and
// the commit is refused with "nothing to commit (tree unchanged)" -- making
// symlinks uncommittable through safegit.
func TestCommitSymlink_LinkToCommittedFile(t *testing.T) {
	dir := newRepo(t)

	// Commit the symlink's future target first, so its content is already in
	// the tree and resolving the link away produces no tree change at all.
	testutil.WriteFile(t, dir, "file.txt", "target content\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "add file.txt", "--", "file.txt"); code != 0 {
		t.Fatalf("committing file.txt failed (code %d): %s", code, stderr)
	}

	if err := os.Symlink("file.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "add link", "--", "link")
	if code != 0 {
		t.Fatalf("committing symlink failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	if mode := treeEntryMode(t, dir, "link"); mode != "120000" {
		t.Fatalf("expected HEAD entry \"link\" with mode 120000, got mode %q; tree:\n%s",
			mode, lsTreeHEAD(t, dir))
	}

	if target := catFileBlob(t, dir, "link"); target != "file.txt" {
		t.Fatalf("expected symlink blob to hold the target path %q, got %q", "file.txt", target)
	}

	// The symlink must still be a symlink on disk -- committing it must not
	// have replaced it with its target's content.
	info, err := os.Lstat(filepath.Join(dir, "link"))
	if err != nil {
		t.Fatalf("lstat link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected link to remain a symlink on disk, got mode %v", info.Mode())
	}
}

// TestCommitSymlink_MixedWithRegularFile checks a commit that mixes one new
// regular file with one new symlink: both must be in the commit, and the
// reported file count must match what was actually committed.
//
// Regression: the symlink resolves away to seed.txt (already committed
// unchanged), so only the regular file is staged -- yet commit.go:113 prints
// len(files)+len(result.AutoStagedDeletions), i.e. the INPUT spec count, so
// the command reports "2 file(s) committed" for a commit containing 1. The
// caller gets no signal that anything was dropped.
func TestCommitSymlink_MixedWithRegularFile(t *testing.T) {
	dir := newRepo(t)

	// seed.txt is created and committed by newRepo, so a link to it collapses
	// to an already-committed, unchanged path.
	testutil.WriteFile(t, dir, "regular.txt", "regular content\n")
	if err := os.Symlink("seed.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "add regular and link", "--", "regular.txt", "link")
	if code != 0 {
		t.Fatalf("mixed commit failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// Errorf, not Fatalf: the tree assertion and the count assertion are the
	// two halves of the same defect, and seeing both in one run is the point.
	if mode := treeEntryMode(t, dir, "regular.txt"); mode != "100644" {
		t.Errorf("expected HEAD entry \"regular.txt\" with mode 100644, got mode %q; tree:\n%s",
			mode, lsTreeHEAD(t, dir))
	}
	if mode := treeEntryMode(t, dir, "link"); mode != "120000" {
		t.Errorf("expected HEAD entry \"link\" with mode 120000, got mode %q; tree:\n%s",
			mode, lsTreeHEAD(t, dir))
	} else if target := catFileBlob(t, dir, "link"); target != "seed.txt" {
		t.Errorf("expected symlink blob to hold the target path %q, got %q", "seed.txt", target)
	}

	// Both entries must have been introduced by THIS commit, not merely be
	// present from an earlier one.
	introduced := diffTreeAgainstParent(t, dir)
	for _, want := range []string{"regular.txt", "link"} {
		if !strings.Contains(introduced, want) {
			t.Errorf("expected %q to be changed by HEAD, got diff-tree:\n%s", want, introduced)
		}
	}

	// The reported count must equal what the commit actually contains. Today
	// it is derived from the input spec list, so it says 2 regardless.
	changed := len(strings.Fields(introduced))
	if !strings.Contains(stdout, "2 file(s) committed") {
		t.Errorf("expected stdout to report \"2 file(s) committed\", got:\n%s", stdout)
	}
	if changed != 2 {
		t.Errorf("reported count and reality disagree: HEAD changed %d path(s) (%v) but safegit printed:\n%s",
			changed, strings.Fields(introduced), stdout)
	}
}

// diffTreeAgainstParent lists the paths HEAD changed relative to its parent.
func diffTreeAgainstParent(t *testing.T, repoDir string) string {
	t.Helper()
	cmd := exec.Command("git", "diff-tree", "--no-commit-id", "-r", "--name-only", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git diff-tree failed: %v\n%s", err, out)
	}
	return string(out)
}
