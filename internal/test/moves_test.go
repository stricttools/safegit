package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// safegit used to guess. When a commit added a file whose blob already existed
// in the parent tree at a path that was gone from disk, it read the pair as a
// rename and staged that other path's DELETION into the commit -- a path the
// caller never named. The guess is deleted, and these tests are what keeps it
// deleted: same fixtures as before, opposite expectations.
//
// The rule now is the whole of it: a commit contains the paths the caller
// named, and nothing else. Committing the new half of a move records an
// addition; the old half stays a pending deletion in the working tree until
// someone commits it, which is what naming both paths in one command does.

// assertNoRenameNotice fails when a run announced a rename it should no longer
// be detecting.
func assertNoRenameNotice(t *testing.T, stderr string) {
	t.Helper()
	if strings.Contains(stderr, "rename detected") || strings.Contains(stderr, "auto-staged deletion") {
		t.Errorf("a commit announced a rename; move detection is deleted: %s", stderr)
	}
}

// assertUnstagedDeletion fails unless the path is still present in HEAD and
// reported by git status as a deletion nobody has committed yet.
func assertUnstagedDeletion(t *testing.T, dir, path string) {
	t.Helper()
	if _, ok := testutil.Show(t, dir, "HEAD", path); !ok {
		t.Errorf("%s was removed from HEAD by a commit that never named it", path)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); !strings.Contains(status, "D "+path) {
		t.Errorf("expected %s to remain a pending deletion in the working tree, got status:\n%s", path, status)
	}
}

func TestNoMoveDetection_BasicRename(t *testing.T) {
	dir := newRepo(t)

	// Write foo.txt and commit it
	testutil.WriteFile(t, dir, "foo.txt", "hello world")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add foo", "--", "foo.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Rename foo.txt -> bar.txt
	if err := os.Rename(filepath.Join(dir, "foo.txt"), filepath.Join(dir, "bar.txt")); err != nil {
		t.Fatalf("rename failed: %v", err)
	}

	// Commit only the new path
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "rename foo to bar", "--", "bar.txt")
	if code != 0 {
		t.Fatalf("rename commit failed (code %d): %s", code, stderr)
	}

	assertNoRenameNotice(t, stderr)

	// The commit adds bar.txt and nothing else.
	diffTree := testutil.GitRaw(t, dir, "diff-tree", "--no-commit-id", "--no-renames", "-r", "--name-status", "HEAD")
	if strings.TrimSpace(diffTree) != "A\tbar.txt" {
		t.Errorf("expected the commit to contain exactly the added bar.txt, got:\n%s", diffTree)
	}

	// foo.txt is still the caller's to delete.
	assertUnstagedDeletion(t, dir, "foo.txt")
}

func TestNoMoveDetection_MoveAndEdit(t *testing.T) {
	dir := newRepo(t)

	// Write foo.txt and commit
	testutil.WriteFile(t, dir, "foo.txt", "hello world")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add foo", "--", "foo.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Rename foo.txt -> bar.txt, then modify content
	if err := os.Rename(filepath.Join(dir, "foo.txt"), filepath.Join(dir, "bar.txt")); err != nil {
		t.Fatalf("rename failed: %v", err)
	}
	testutil.WriteFile(t, dir, "bar.txt", "goodbye world")

	// Commit only the new path
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "move and edit", "--", "bar.txt")
	if code != 0 {
		t.Fatalf("move-and-edit commit failed (code %d): %s", code, stderr)
	}

	assertNoRenameNotice(t, stderr)
	assertUnstagedDeletion(t, dir, "foo.txt")
}

func TestNoMoveDetection_ExplicitBothPaths(t *testing.T) {
	dir := newRepo(t)

	// Write foo.txt and commit
	testutil.WriteFile(t, dir, "foo.txt", "hello world")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add foo", "--", "foo.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Rename foo.txt -> bar.txt
	if err := os.Rename(filepath.Join(dir, "foo.txt"), filepath.Join(dir, "bar.txt")); err != nil {
		t.Fatalf("rename failed: %v", err)
	}

	// Commit both paths explicitly -- the supported way to record a move.
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "rename", "--", "bar.txt", "foo.txt")
	if code != 0 {
		t.Fatalf("explicit-both-paths commit failed (code %d): %s", code, stderr)
	}

	assertNoRenameNotice(t, stderr)

	// Working tree should be clean: both halves of the move were named.
	status := testutil.Git(t, dir, "status", "--porcelain")
	if status != "" {
		t.Fatalf("expected clean working tree, got: %s", status)
	}
	if _, ok := testutil.Show(t, dir, "HEAD", "foo.txt"); ok {
		t.Error("foo.txt should be gone from HEAD: its deletion was named")
	}
}

func TestNoMoveDetection_UnrelatedDeletion(t *testing.T) {
	dir := newRepo(t)

	// Write a.txt and b.txt, commit both
	testutil.WriteFile(t, dir, "a.txt", "content A")
	testutil.WriteFile(t, dir, "b.txt", "content B")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add a and b", "--", "a.txt", "b.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Delete a.txt (unrelated) and rename b.txt -> c.txt
	if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatalf("remove a.txt failed: %v", err)
	}
	if err := os.Rename(filepath.Join(dir, "b.txt"), filepath.Join(dir, "c.txt")); err != nil {
		t.Fatalf("rename b.txt failed: %v", err)
	}

	// Commit only the new path of b
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "rename b", "--", "c.txt")
	if code != 0 {
		t.Fatalf("rename commit failed (code %d): %s", code, stderr)
	}

	assertNoRenameNotice(t, stderr)

	// Neither deletion was swept into the commit: not the blob-matching one,
	// and not the unrelated one.
	assertUnstagedDeletion(t, dir, "b.txt")
	assertUnstagedDeletion(t, dir, "a.txt")
}

func TestNoMoveDetection_Amend(t *testing.T) {
	dir := newRepo(t)

	// Write foo.txt and commit
	testutil.WriteFile(t, dir, "foo.txt", "hello world")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add foo", "--", "foo.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Rename foo.txt -> bar.txt
	if err := os.Rename(filepath.Join(dir, "foo.txt"), filepath.Join(dir, "bar.txt")); err != nil {
		t.Fatalf("rename failed: %v", err)
	}

	// Amend with only the new path
	_, stderr, code = runSafegit(t, dir, "commit", "--amend", "-m", "amend with rename", "--", "bar.txt")
	if code != 0 {
		t.Fatalf("amend commit failed (code %d): %s", code, stderr)
	}

	assertNoRenameNotice(t, stderr)
	assertUnstagedDeletion(t, dir, "foo.txt")
}

func TestNoMoveDetection_QuietIsNotASilentGuess(t *testing.T) {
	dir := newRepo(t)

	// Write foo.txt and commit
	testutil.WriteFile(t, dir, "foo.txt", "hello world")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add foo", "--", "foo.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Rename foo.txt -> bar.txt
	if err := os.Rename(filepath.Join(dir, "foo.txt"), filepath.Join(dir, "bar.txt")); err != nil {
		t.Fatalf("rename failed: %v", err)
	}

	// Commit with --quiet. There is nothing to suppress any more: the notice
	// existed to disclose a guess, and with the guess gone the quiet run and
	// the loud one produce the same commit.
	_, stderr, code = runSafegit(t, dir, "--quiet", "commit", "-m", "rename", "--", "bar.txt")
	if code != 0 {
		t.Fatalf("quiet commit failed (code %d): %s", code, stderr)
	}

	assertNoRenameNotice(t, stderr)
	assertUnstagedDeletion(t, dir, "foo.txt")
}

func TestNoMoveDetection_MoveToSubdirectory(t *testing.T) {
	dir := newRepo(t)

	// Create and commit foo.txt
	testutil.WriteFile(t, dir, "foo.txt", "subdirectory test content")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add foo", "--", "foo.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Create subdir and move foo.txt into it
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir subdir failed: %v", err)
	}
	if err := os.Rename(filepath.Join(dir, "foo.txt"), filepath.Join(dir, "subdir", "foo.txt")); err != nil {
		t.Fatalf("rename failed: %v", err)
	}

	// Commit only the new path
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "move to subdir", "--", "subdir/foo.txt")
	if code != 0 {
		t.Fatalf("move commit failed (code %d): %s", code, stderr)
	}

	assertNoRenameNotice(t, stderr)
	assertUnstagedDeletion(t, dir, "foo.txt")

	diffTree := testutil.GitRaw(t, dir, "diff-tree", "--no-commit-id", "--no-renames", "-r", "--name-status", "HEAD")
	if strings.TrimSpace(diffTree) != "A\tsubdir/foo.txt" {
		t.Errorf("expected the commit to contain exactly the added subdir/foo.txt, got:\n%s", diffTree)
	}
}

func TestNoMoveDetection_MultipleMoves(t *testing.T) {
	dir := newRepo(t)

	// Create and commit two files
	testutil.WriteFile(t, dir, "a.txt", "alpha")
	testutil.WriteFile(t, dir, "b.txt", "beta")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add a and b", "--", "a.txt", "b.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Move both files
	if err := os.Rename(filepath.Join(dir, "a.txt"), filepath.Join(dir, "x.txt")); err != nil {
		t.Fatalf("rename a.txt failed: %v", err)
	}
	if err := os.Rename(filepath.Join(dir, "b.txt"), filepath.Join(dir, "y.txt")); err != nil {
		t.Fatalf("rename b.txt failed: %v", err)
	}

	// Commit both new paths in one command
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "rename both", "--", "x.txt", "y.txt")
	if code != 0 {
		t.Fatalf("multi-move commit failed (code %d): %s", code, stderr)
	}

	assertNoRenameNotice(t, stderr)
	assertUnstagedDeletion(t, dir, "a.txt")
	assertUnstagedDeletion(t, dir, "b.txt")
}

func TestNoMoveDetection_PathSimilarityIsNotConsulted(t *testing.T) {
	dir := newRepo(t)

	// Create two files with identical content in different directories
	if err := os.MkdirAll(filepath.Join(dir, "src", "util"), 0o755); err != nil {
		t.Fatalf("mkdir src/util failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o755); err != nil {
		t.Fatalf("mkdir lib failed: %v", err)
	}
	testutil.WriteFile(t, dir, "src/util/helper.txt", "shared helper content")
	testutil.WriteFile(t, dir, "lib/helper.txt", "shared helper content")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add helpers", "--", "src/util/helper.txt", "lib/helper.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Delete both, create renamed file in same directory as src/util/helper.txt
	if err := os.Remove(filepath.Join(dir, "src", "util", "helper.txt")); err != nil {
		t.Fatalf("remove src/util/helper.txt failed: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "lib", "helper.txt")); err != nil {
		t.Fatalf("remove lib/helper.txt failed: %v", err)
	}
	testutil.WriteFile(t, dir, "src/util/renamed.txt", "shared helper content")

	// Commit only the new file. There is no tie to break: neither deletion is
	// a candidate for anything, because the commit contains what was named.
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "rename helper", "--", "src/util/renamed.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	assertNoRenameNotice(t, stderr)
	assertUnstagedDeletion(t, dir, "src/util/helper.txt")
	assertUnstagedDeletion(t, dir, "lib/helper.txt")
}

func TestNoMoveDetection_OriginalPathRecreated(t *testing.T) {
	dir := newRepo(t)

	// Create and commit config.txt
	testutil.WriteFile(t, dir, "config.txt", "original")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add config", "--", "config.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Move config.txt to config.bak, then recreate config.txt with different content
	if err := os.Rename(filepath.Join(dir, "config.txt"), filepath.Join(dir, "config.bak")); err != nil {
		t.Fatalf("rename failed: %v", err)
	}
	testutil.WriteFile(t, dir, "config.txt", "updated")

	// Commit both files explicitly
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "backup and update config", "--", "config.bak", "config.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	assertNoRenameNotice(t, stderr)

	// Working tree should be clean (both files explicitly listed)
	status := testutil.Git(t, dir, "status", "--porcelain")
	if status != "" {
		t.Fatalf("expected clean working tree, got: %s", status)
	}
}

func TestNoMoveDetection_EmptyFile(t *testing.T) {
	dir := newRepo(t)

	// Create and commit an empty file. Every empty file in a repository shares
	// one blob, which is what made the old guess fire on unrelated paths.
	testutil.WriteFile(t, dir, "empty.txt", "")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add empty", "--", "empty.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Move empty file
	if err := os.Rename(filepath.Join(dir, "empty.txt"), filepath.Join(dir, "renamed_empty.txt")); err != nil {
		t.Fatalf("rename failed: %v", err)
	}

	// Commit only the new path
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "rename empty", "--", "renamed_empty.txt")
	if code != 0 {
		t.Fatalf("empty file rename commit failed (code %d): %s", code, stderr)
	}

	assertNoRenameNotice(t, stderr)
	assertUnstagedDeletion(t, dir, "empty.txt")
}
