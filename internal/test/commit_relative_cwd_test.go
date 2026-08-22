package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// TestCommitFromSubdirRelativePathNoPendingChange: invoked from a repository
// subdirectory with two cwd-relative path arguments, one of which has no
// pending change.
//
// Naming a path is a statement that it belongs in the commit, so the unchanged
// one is refused -- by the name the caller typed, resolved against the INVOKING
// directory and not against the repository root. What the refusal must never do
// is act on the mis-resolved path: nothing is committed, and the unchanged file
// keeps its tracked content.
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

	head := testutil.Rev(t, dir, "HEAD")

	// Run from inside sub/ with cwd-relative paths for both files.
	stdout, stderr, code := runSafegit(t, sub, "commit", "-m", "edit from subdir", "--", "edited.txt", "unchanged.txt")
	if code != exitcode.PathMatchedNothing {
		t.Fatalf("commit from subdir with an unchanged relative path exited %d, want %d\nstdout:\n%s\nstderr:\n%s",
			code, exitcode.PathMatchedNothing, stdout, stderr)
	}
	// The refusal names the argument as typed, which is what proves the path
	// was resolved against sub/ and not against the repository root.
	if !strings.Contains(stderr, "unchanged.txt") {
		t.Errorf("the refusal does not name the argument it was decided about: %s", stderr)
	}

	// Nothing was committed, and the unchanged file keeps its tracked content.
	if now := testutil.Rev(t, dir, "HEAD"); now != head {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", head, now)
	}
	if got := testutil.MustShow(t, dir, "HEAD", "sub/unchanged.txt"); got != "unchanged\n" {
		t.Fatalf("sub/unchanged.txt at HEAD = %q, want %q", got, "unchanged\n")
	}

	// Naming only the changed path commits it, from the same directory.
	if _, stderr, code := runSafegit(t, sub, "commit", "-m", "edit from subdir", "--", "edited.txt"); code != 0 {
		t.Fatalf("commit of the changed path alone failed (code %d): %s", code, stderr)
	}
	if got := testutil.MustShow(t, dir, "HEAD", "sub/edited.txt"); got != "after\n" {
		t.Fatalf("sub/edited.txt at HEAD = %q, want %q", got, "after\n")
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); status != "" {
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
	if got := testutil.MustShow(t, dir, "HEAD", "sub/edited.txt"); got != "after\n" {
		t.Fatalf("sub/edited.txt at HEAD = %q, want %q", got, "after\n")
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); status != "" {
		t.Fatalf("expected clean working tree, got: %s", status)
	}
}

// TestCommitFromRepoRootUnchangedPath pins the same shape from the repository
// root, where relative and repo-relative spellings coincide. It isolates
// "a named path has no pending change" from "the path was mis-resolved": both
// directories must produce the same refusal, so a difference between the two is
// a path-resolution defect rather than the refusal itself.
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

	head := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "edit from root", "--", "edited.txt", "unchanged.txt")
	if code != exitcode.PathMatchedNothing {
		t.Fatalf("commit from root with an unchanged path exited %d, want %d\nstdout:\n%s\nstderr:\n%s",
			code, exitcode.PathMatchedNothing, stdout, stderr)
	}
	if !strings.Contains(stderr, "unchanged.txt") {
		t.Errorf("the refusal does not name the argument it was decided about: %s", stderr)
	}
	if now := testutil.Rev(t, dir, "HEAD"); now != head {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", head, now)
	}
	if got := testutil.MustShow(t, dir, "HEAD", "unchanged.txt"); got != "unchanged\n" {
		t.Fatalf("unchanged.txt at HEAD = %q, want %q", got, "unchanged\n")
	}

	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "edit from root", "--", "edited.txt"); code != 0 {
		t.Fatalf("commit of the changed path alone failed (code %d): %s", code, stderr)
	}
	if got := testutil.MustShow(t, dir, "HEAD", "edited.txt"); got != "after\n" {
		t.Fatalf("edited.txt at HEAD = %q, want %q", got, "after\n")
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); status != "" {
		t.Fatalf("expected clean working tree, got: %s", status)
	}
}
