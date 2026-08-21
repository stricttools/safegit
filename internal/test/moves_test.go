package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

func TestMoveDetection_BasicRename(t *testing.T) {
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

	// stderr should mention auto-staged deletion with rename detected
	if !strings.Contains(stderr, "auto-staged deletion: foo.txt (rename detected)") {
		t.Fatalf("expected stderr to contain auto-staged deletion message, got: %s", stderr)
	}

	// git diff-tree should show a rename
	diffTree := testutil.GitRaw(t, dir, "diff-tree", "--no-commit-id", "-r", "-M", "HEAD")
	if !strings.Contains(diffTree, "foo.txt") || !strings.Contains(diffTree, "bar.txt") {
		t.Fatalf("expected diff-tree to mention both foo.txt and bar.txt, got: %s", diffTree)
	}
	if !strings.Contains(diffTree, "R") {
		t.Fatalf("expected diff-tree to show rename (R), got: %s", diffTree)
	}

	// Working tree should be clean
	status := testutil.Git(t, dir, "status", "--porcelain")
	if status != "" {
		t.Fatalf("expected clean working tree, got: %s", status)
	}
}

func TestMoveDetection_MoveAndEdit(t *testing.T) {
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

	// No rename detection when content differs
	if strings.Contains(stderr, "rename detected") {
		t.Fatalf("expected no rename detection when content changed, got: %s", stderr)
	}

	// foo.txt deletion should NOT be auto-staged
	status := testutil.Git(t, dir, "status", "--porcelain")
	if !strings.Contains(status, "D foo.txt") {
		t.Fatalf("expected 'D foo.txt' in status, got: %s", status)
	}
}

func TestMoveDetection_ExplicitBothPaths(t *testing.T) {
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

	// Commit both paths explicitly
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "rename", "--", "bar.txt", "foo.txt")
	if code != 0 {
		t.Fatalf("explicit-both-paths commit failed (code %d): %s", code, stderr)
	}

	// No auto-staging needed when user lists both paths
	if strings.Contains(stderr, "rename detected") {
		t.Fatalf("expected no rename detection when both paths explicit, got: %s", stderr)
	}

	// Working tree should be clean
	status := testutil.Git(t, dir, "status", "--porcelain")
	if status != "" {
		t.Fatalf("expected clean working tree, got: %s", status)
	}
}

func TestMoveDetection_UnrelatedDeletion(t *testing.T) {
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

	// Should auto-stage b.txt deletion
	if !strings.Contains(stderr, "auto-staged deletion: b.txt (rename detected)") {
		t.Fatalf("expected auto-staged deletion of b.txt, got: %s", stderr)
	}

	// Should NOT mention a.txt (unrelated deletion)
	if strings.Contains(stderr, "a.txt") {
		t.Fatalf("expected no mention of a.txt in stderr, got: %s", stderr)
	}

	// a.txt should still show as deleted in status
	status := testutil.Git(t, dir, "status", "--porcelain")
	if !strings.Contains(status, "D a.txt") {
		t.Fatalf("expected 'D a.txt' in status, got: %s", status)
	}
}

func TestMoveDetection_Amend(t *testing.T) {
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

	// Should detect the rename
	if !strings.Contains(stderr, "auto-staged deletion: foo.txt (rename detected)") {
		t.Fatalf("expected auto-staged deletion message for amend, got: %s", stderr)
	}

	// Working tree should be clean
	status := testutil.Git(t, dir, "status", "--porcelain")
	if status != "" {
		t.Fatalf("expected clean working tree, got: %s", status)
	}
}

func TestMoveDetection_QuietSuppresses(t *testing.T) {
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

	// Commit with --quiet
	_, stderr, code = runSafegit(t, dir, "--quiet", "commit", "-m", "rename", "--", "bar.txt")
	if code != 0 {
		t.Fatalf("quiet commit failed (code %d): %s", code, stderr)
	}

	// Should NOT mention rename in output (suppressed by --quiet)
	if strings.Contains(stderr, "rename detected") {
		t.Fatalf("expected no rename message with --quiet, got: %s", stderr)
	}

	// But move detection should still have run -- working tree should be clean
	status := testutil.Git(t, dir, "status", "--porcelain")
	if status != "" {
		t.Fatalf("expected clean working tree (move detection should still run), got: %s", status)
	}
}

func TestMoveDetection_MoveToSubdirectory(t *testing.T) {
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

	// Should auto-stage deletion of foo.txt
	if !strings.Contains(stderr, "auto-staged deletion: foo.txt (rename detected)") {
		t.Fatalf("expected auto-staged deletion message for foo.txt, got: %s", stderr)
	}

	// Working tree should be clean
	status := testutil.Git(t, dir, "status", "--porcelain")
	if status != "" {
		t.Fatalf("expected clean working tree, got: %s", status)
	}

	// git diff-tree should show a rename
	diffTree := testutil.GitRaw(t, dir, "diff-tree", "--no-commit-id", "-r", "-M", "HEAD")
	if !strings.Contains(diffTree, "R") {
		t.Fatalf("expected diff-tree to show rename (R), got: %s", diffTree)
	}
	if !strings.Contains(diffTree, "foo.txt") || !strings.Contains(diffTree, "subdir/foo.txt") {
		t.Fatalf("expected diff-tree to mention both paths, got: %s", diffTree)
	}
}

func TestMoveDetection_MultipleMoves(t *testing.T) {
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

	// Should auto-stage deletion of both a.txt and b.txt
	if !strings.Contains(stderr, "auto-staged deletion: a.txt (rename detected)") {
		t.Fatalf("expected stderr to mention a.txt deletion, got: %s", stderr)
	}
	if !strings.Contains(stderr, "auto-staged deletion: b.txt (rename detected)") {
		t.Fatalf("expected stderr to mention b.txt deletion, got: %s", stderr)
	}

	// Working tree should be clean
	status := testutil.Git(t, dir, "status", "--porcelain")
	if status != "" {
		t.Fatalf("expected clean working tree, got: %s", status)
	}
}

func TestMoveDetection_PathSimilarityTiebreak(t *testing.T) {
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

	// Commit only the new file
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "rename helper", "--", "src/util/renamed.txt")
	if code != 0 {
		t.Fatalf("tiebreak commit failed (code %d): %s", code, stderr)
	}

	// Should auto-stage deletion of src/util/helper.txt (same directory = higher similarity)
	if !strings.Contains(stderr, "auto-staged deletion: src/util/helper.txt (rename detected)") {
		t.Fatalf("expected auto-staged deletion of src/util/helper.txt, got: %s", stderr)
	}

	// lib/helper.txt should still be deleted in working tree (unstaged)
	status := testutil.Git(t, dir, "status", "--porcelain")
	if !strings.Contains(status, "D lib/helper.txt") {
		t.Fatalf("expected lib/helper.txt to remain as unstaged deletion, got: %s", status)
	}
}

func TestMoveDetection_OriginalPathRecreated(t *testing.T) {
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

	// No auto-staged deletion: old path config.txt still exists on disk with different content
	if strings.Contains(stderr, "auto-staged deletion:") {
		t.Fatalf("expected no auto-staged deletion when original path recreated, got: %s", stderr)
	}

	// Working tree should be clean (both files explicitly listed)
	status := testutil.Git(t, dir, "status", "--porcelain")
	if status != "" {
		t.Fatalf("expected clean working tree, got: %s", status)
	}
}

func TestMoveDetection_EmptyFile(t *testing.T) {
	dir := newRepo(t)

	// Create and commit an empty file
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

	// Should auto-stage deletion of empty.txt
	if !strings.Contains(stderr, "auto-staged deletion: empty.txt (rename detected)") {
		t.Fatalf("expected auto-staged deletion message for empty.txt, got: %s", stderr)
	}

	// Working tree should be clean
	status := testutil.Git(t, dir, "status", "--porcelain")
	if status != "" {
		t.Fatalf("expected clean working tree, got: %s", status)
	}

	// git diff-tree should show a rename
	diffTree := testutil.GitRaw(t, dir, "diff-tree", "--no-commit-id", "-r", "-M", "HEAD")
	if !strings.Contains(diffTree, "R") {
		t.Fatalf("expected diff-tree to show rename (R), got: %s", diffTree)
	}
}
