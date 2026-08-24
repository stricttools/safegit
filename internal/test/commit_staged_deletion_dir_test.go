package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// These tests cover committing pre-staged deletions when the caller names a
// containing DIRECTORY instead of the individual files. An external deletion
// tool removes the files from the working tree and stages the deletions in the
// index (`git rm -r dir/`); the caller then commits `dir/`.
//
// The directory form behaves identically to the file-path form, and these tests
// are what holds it there.
//
// It did not always. A directory pathspec is removed from the temp index
// wholesale by stageFile, and MOVE DETECTION -- which safegit no longer has --
// then independently re-ran `git rm --cached` on an individual path inside that
// directory that was already gone from the index, which git rejects with exit
// 128. The file-path form never hit it, because the individual paths were in
// the explicit set detection skipped. A move is now declared and never guessed
// (see internal/commit/moved.go), so nothing re-runs anything behind stageFile;
// the cases below stay because the directory form still has to reach the same
// commit as the file form, whatever the reason a past version did not.

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
		testutil.WriteFile(t, dir, "dir/"+name, content)
		paths = append(paths, "dir/"+name)
	}

	args := append([]string{"commit", "-m", "add dir", "--"}, paths...)
	_, stderr, code := runSafegit(t, dir, args...)
	if code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}

	testutil.GitRaw(t, dir, "rm", "-r", "dir")
	if _, err := os.Stat(filepath.Join(dir, "dir")); !os.IsNotExist(err) {
		t.Fatalf("expected dir/ to be gone from the working tree, stat err = %v", err)
	}
	return dir
}

// assertDeletedInHead verifies the tip commit deletes each named path relative
// to its parent and that none of them survive in the HEAD tree.
func assertDeletedInHead(t *testing.T, dir string, paths ...string) {
	t.Helper()

	diff := testutil.GitRaw(t, dir, "diff-tree", "--no-commit-id", "-r", "--name-status", "HEAD")
	tree := testutil.GitRaw(t, dir, "ls-tree", "-r", "--name-only", "HEAD")
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
	tree := testutil.GitRaw(t, dir, "ls-tree", "-r", "--name-only", "HEAD")
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
	if status := testutil.Git(t, dir, "status", "--porcelain"); status != "" {
		t.Errorf("expected clean working tree after commit, got: %s", status)
	}
}

// TestCommitStagedDeletions_DirectoryPathWithMovedFile is the regression case.
// One of the deleted files reappears elsewhere with identical content -- the
// shape that used to make move detection fire on top of a directory stageFile
// had already dropped from the temp index, producing git's exit 128. Nothing is
// STAGED from blob equality any more -- the commit is the ordinary one, two
// deletions and an addition -- and the record safegit may mint for what the
// delta witnesses changes no tree, so this path cannot come back.
func TestCommitStagedDeletions_DirectoryPathWithMovedFile(t *testing.T) {
	dir := seedDeletedDir(t, map[string]string{"a.txt": "alpha\n", "b.txt": "beta\n"})

	// The deleted dir/a.txt reappears at the repo root with the same content.
	testutil.WriteFile(t, dir, "new.txt", "alpha\n")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "move a out of dir", "--", "dir/", "new.txt")
	if code != 0 {
		t.Fatalf("commit of staged deletions by directory alongside a moved file failed (code %d): %s", code, stderr)
	}

	assertDeletedInHead(t, dir, "dir/a.txt", "dir/b.txt")
	assertPresentInHead(t, dir, "new.txt")
	if status := testutil.Git(t, dir, "status", "--porcelain"); status != "" {
		t.Errorf("expected clean working tree after commit, got: %s", status)
	}
}

// TestCommitStagedDeletions_DirectoryPathWithUnrelatedEmptyFile shows how
// little it took to trigger the same failure back when blob equality decided
// anything: no deliberate move at all. An empty file was deleted with the
// directory, an unrelated new empty file is in the same commit, and the two
// share the empty blob -- which detection read as one being the other moved.
// Two empty files are now two empty files: the empty blob is fenced out of
// safegit's own reading of a delta for this very reason, and no reading of a
// delta stages anything in any case.
func TestCommitStagedDeletions_DirectoryPathWithUnrelatedEmptyFile(t *testing.T) {
	dir := seedDeletedDir(t, map[string]string{"empty.txt": "", "b.txt": "beta\n"})

	testutil.WriteFile(t, dir, "notes.md", "")

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

	testutil.WriteFile(t, dir, "new.txt", "alpha\n")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "move a out of dir", "--",
		"dir/a.txt", "dir/b.txt", "new.txt")
	if code != 0 {
		t.Fatalf("commit of staged deletions by file paths failed (code %d): %s", code, stderr)
	}

	assertDeletedInHead(t, dir, "dir/a.txt", "dir/b.txt")
	assertPresentInHead(t, dir, "new.txt")
	if status := testutil.Git(t, dir, "status", "--porcelain"); status != "" {
		t.Errorf("expected clean working tree after commit, got: %s", status)
	}
}
