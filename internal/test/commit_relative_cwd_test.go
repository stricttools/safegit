package test

import (
	"os"
	"path/filepath"
	"testing"
)

// mustShow returns the contents of path (repo-relative) at ref, failing the
// test when the path is absent from that commit. It wraps the package's
// gitShow helper, which reports absence instead of failing.
func mustShow(t *testing.T, repoDir, ref, path string) string {
	t.Helper()
	content, ok := gitShow(t, repoDir, ref, path)
	if !ok {
		t.Fatalf("%s is absent from %s", path, ref)
	}
	return content
}

// TestCommitFromSubdirRelativePathNoPendingChange reproduces the reported
// failure: invoked from a repository subdirectory with two cwd-relative path
// arguments, one of which has no pending change, safegit's move detection
// resolves the unchanged relative path against the repository ROOT rather than
// the invoking cwd. The mis-resolved path is absent from the index and the
// underlying `git rm --cached` fails, so the whole commit dies with exit 128.
//
// The path with the pending edit takes a different code path and is unaffected,
// which is why the same command without the unchanged file succeeds.
func TestCommitFromSubdirRelativePathNoPendingChange(t *testing.T) {
	dir := newRepo(t)

	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "unchanged.txt"), []byte("unchanged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "edited.txt"), []byte("before\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Track and commit both files, so both are clean in the index at the start.
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add sub files", "--", "sub/unchanged.txt", "sub/edited.txt")
	if code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}

	// Modify ONLY sub/edited.txt. sub/unchanged.txt has no pending change.
	if err := os.WriteFile(filepath.Join(sub, "edited.txt"), []byte("after\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Run from inside sub/ with cwd-relative paths for both files.
	stdout, stderr, code := runSafegit(t, sub, "commit", "-m", "edit from subdir", "--", "edited.txt", "unchanged.txt")
	if code != 0 {
		t.Fatalf("commit from subdir with an unchanged relative path failed (code %d)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	// The edit must actually be in the new HEAD commit.
	if got := mustShow(t, dir, "HEAD", "sub/edited.txt"); got != "after\n" {
		t.Fatalf("sub/edited.txt at HEAD = %q, want %q", got, "after\n")
	}

	// sub/unchanged.txt must still be tracked with its original contents --
	// naming a path with no pending change must never remove it from the tree.
	if got := mustShow(t, dir, "HEAD", "sub/unchanged.txt"); got != "unchanged\n" {
		t.Fatalf("sub/unchanged.txt at HEAD = %q, want %q", got, "unchanged\n")
	}

	// And the working tree must be clean afterwards.
	if status := gitStatusPorcelain(t, dir); status != "" {
		t.Fatalf("expected clean working tree, got: %s", status)
	}
}

// TestCommitFromSubdirRelativePathOnlyChanged is the control case named in the
// report: the same command WITHOUT the unchanged path argument succeeds. If
// this one ever goes red the failure is broader than the reported bug.
func TestCommitFromSubdirRelativePathOnlyChanged(t *testing.T) {
	dir := newRepo(t)

	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "unchanged.txt"), []byte("unchanged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "edited.txt"), []byte("before\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add sub files", "--", "sub/unchanged.txt", "sub/edited.txt")
	if code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}

	if err := os.WriteFile(filepath.Join(sub, "edited.txt"), []byte("after\n"), 0644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runSafegit(t, sub, "commit", "-m", "edit from subdir", "--", "edited.txt")
	if code != 0 {
		t.Fatalf("commit from subdir with only the changed path failed (code %d)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := mustShow(t, dir, "HEAD", "sub/edited.txt"); got != "after\n" {
		t.Fatalf("sub/edited.txt at HEAD = %q, want %q", got, "after\n")
	}
	if status := gitStatusPorcelain(t, dir); status != "" {
		t.Fatalf("expected clean working tree, got: %s", status)
	}
}

// TestCommitFromRepoRootUnchangedPath pins the same shape from the repository
// root, where relative and repo-relative spellings coincide. It isolates
// "a named path has no pending change" from "the path was mis-resolved": if
// this passes while the subdirectory case fails, the defect is purely in
// resolving relative paths against the invoking cwd.
func TestCommitFromRepoRootUnchangedPath(t *testing.T) {
	dir := newRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "unchanged.txt"), []byte("unchanged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "edited.txt"), []byte("before\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add root files", "--", "unchanged.txt", "edited.txt")
	if code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}

	if err := os.WriteFile(filepath.Join(dir, "edited.txt"), []byte("after\n"), 0644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "edit from root", "--", "edited.txt", "unchanged.txt")
	if code != 0 {
		t.Fatalf("commit from root with an unchanged path failed (code %d)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := mustShow(t, dir, "HEAD", "edited.txt"); got != "after\n" {
		t.Fatalf("edited.txt at HEAD = %q, want %q", got, "after\n")
	}
	if got := mustShow(t, dir, "HEAD", "unchanged.txt"); got != "unchanged\n" {
		t.Fatalf("unchanged.txt at HEAD = %q, want %q", got, "unchanged\n")
	}
	if status := gitStatusPorcelain(t, dir); status != "" {
		t.Fatalf("expected clean working tree, got: %s", status)
	}
}
