package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests cover committing pre-staged deletions when the caller names a
// containing DIRECTORY instead of the individual files. An external deletion
// tool removes the files from the working tree and stages the deletions in the
// index (`git rm -r dir/`); the caller then commits `dir/`.
//
// The directory form must behave identically to the file-path form. It does
// not: a directory pathspec is removed from the temp index wholesale by
// stageFile, and move detection then independently re-runs `git rm --cached`
// on an individual path inside that directory that is already gone from the
// index, which git rejects with exit 128. The file-path form never hits this,
// because the individual paths are in the explicit set that move detection
// skips.

// runGitIn runs a raw git command inside a test's scratch repo and fails the
// test if it errors. Raw git is fine here: the repo is a throwaway created by
// newRepo, and the point is to simulate a third-party deletion tool.
func runGitIn(t *testing.T, repoDir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// seedDeletedDir creates dir/ holding the given name->content files, commits
// it via safegit, then simulates the deletion tool with `git rm -r dir`, which
// removes the files from the working tree AND stages the deletions.
func seedDeletedDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := newRepo(t)

	if err := os.MkdirAll(filepath.Join(dir, "dir"), 0o755); err != nil {
		t.Fatalf("mkdir dir: %v", err)
	}
	var paths []string
	for name, content := range files {
		writeFile(t, dir, "dir/"+name, content)
		paths = append(paths, "dir/"+name)
	}

	args := append([]string{"commit", "-m", "add dir", "--"}, paths...)
	_, stderr, code := runSafegit(t, dir, args...)
	if code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}

	runGitIn(t, dir, "rm", "-r", "dir")
	if _, err := os.Stat(filepath.Join(dir, "dir")); !os.IsNotExist(err) {
		t.Fatalf("expected dir/ to be gone from the working tree, stat err = %v", err)
	}
	return dir
}

// assertDeletedInHead verifies the tip commit deletes each named path relative
// to its parent and that none of them survive in the HEAD tree.
func assertDeletedInHead(t *testing.T, dir string, paths ...string) {
	t.Helper()

	diff := runGitIn(t, dir, "diff-tree", "--no-commit-id", "-r", "--name-status", "HEAD")
	tree := runGitIn(t, dir, "ls-tree", "-r", "--name-only", "HEAD")
	for _, p := range paths {
		if !strings.Contains(diff, "D\t"+p) {
			t.Errorf("expected %q deleted in HEAD diff-tree, got:\n%s", p, diff)
		}
		if strings.Contains(tree, p) {
			t.Errorf("%s should be absent from HEAD tree, got:\n%s", p, tree)
		}
	}
}

// assertPresentInHead verifies each named path is in the HEAD tree.
func assertPresentInHead(t *testing.T, dir string, paths ...string) {
	t.Helper()
	tree := runGitIn(t, dir, "ls-tree", "-r", "--name-only", "HEAD")
	for _, p := range paths {
		if !strings.Contains(tree, p) {
			t.Errorf("%s missing from HEAD tree, got:\n%s", p, tree)
		}
	}
}

// TestCommitStagedDeletions_DirectoryPath is the baseline: a directory whose
// every file was deleted and pre-staged, committed by naming the directory.
// Nothing else is in the commit, so move detection finds no new file and never
// runs. This is the part that already works.
func TestCommitStagedDeletions_DirectoryPath(t *testing.T) {
	dir := seedDeletedDir(t, map[string]string{"a.txt": "alpha\n", "b.txt": "beta\n"})

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "remove dir", "--", "dir/")
	if code != 0 {
		t.Fatalf("commit of staged deletions by directory failed (code %d): %s", code, stderr)
	}

	assertDeletedInHead(t, dir, "dir/a.txt", "dir/b.txt")
	if status := gitStatusPorcelain(t, dir); status != "" {
		t.Errorf("expected clean working tree after commit, got: %s", status)
	}
}

// TestCommitStagedDeletions_DirectoryPathWithMovedFile is the regression case.
// One of the deleted files reappears elsewhere with identical content, so move
// detection fires. stageFile has already dropped the whole directory from the
// temp index; move detection then re-runs `git rm --cached` on dir/a.txt,
// which is no longer in that index, and git fails with exit 128.
func TestCommitStagedDeletions_DirectoryPathWithMovedFile(t *testing.T) {
	dir := seedDeletedDir(t, map[string]string{"a.txt": "alpha\n", "b.txt": "beta\n"})

	// The deleted dir/a.txt reappears at the repo root with the same content.
	writeFile(t, dir, "new.txt", "alpha\n")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "move a out of dir", "--", "dir/", "new.txt")
	if code != 0 {
		t.Fatalf("commit of staged deletions by directory alongside a moved file failed (code %d): %s", code, stderr)
	}

	assertDeletedInHead(t, dir, "dir/a.txt", "dir/b.txt")
	assertPresentInHead(t, dir, "new.txt")
	if status := gitStatusPorcelain(t, dir); status != "" {
		t.Errorf("expected clean working tree after commit, got: %s", status)
	}
}

// TestCommitStagedDeletions_DirectoryPathWithUnrelatedEmptyFile shows how
// little it takes to trigger the same failure: no deliberate move at all. An
// empty file was deleted with the directory, an unrelated new empty file is in
// the same commit, and the two share the empty blob, so move detection treats
// the new file as the destination of a move.
func TestCommitStagedDeletions_DirectoryPathWithUnrelatedEmptyFile(t *testing.T) {
	dir := seedDeletedDir(t, map[string]string{"empty.txt": "", "b.txt": "beta\n"})

	writeFile(t, dir, "notes.md", "")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "remove dir, add notes", "--", "dir/", "notes.md")
	if code != 0 {
		t.Fatalf("commit of staged deletions by directory alongside an unrelated empty file failed (code %d): %s", code, stderr)
	}

	assertDeletedInHead(t, dir, "dir/empty.txt", "dir/b.txt")
	assertPresentInHead(t, dir, "notes.md")
}

// TestCommitStagedDeletions_FilePathsWithMovedFile is the companion case that
// documents the asymmetry reported in the field: the identical scenario
// succeeds when the caller names the individual files instead of the
// directory, because move detection skips paths in the explicit set. It must
// keep working after any fix to the directory form.
func TestCommitStagedDeletions_FilePathsWithMovedFile(t *testing.T) {
	dir := seedDeletedDir(t, map[string]string{"a.txt": "alpha\n", "b.txt": "beta\n"})

	writeFile(t, dir, "new.txt", "alpha\n")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "move a out of dir", "--",
		"dir/a.txt", "dir/b.txt", "new.txt")
	if code != 0 {
		t.Fatalf("commit of staged deletions by file paths failed (code %d): %s", code, stderr)
	}

	assertDeletedInHead(t, dir, "dir/a.txt", "dir/b.txt")
	assertPresentInHead(t, dir, "new.txt")
	if status := gitStatusPorcelain(t, dir); status != "" {
		t.Errorf("expected clean working tree after commit, got: %s", status)
	}
}
