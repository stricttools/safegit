package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// TestUndoCount2 creates two commits and undoes both with --count 2.
func TestUndoCount2(t *testing.T) {
	dir := newRepo(t)
	env := []string{"CLAUDE_CODE_SESSION_ID=test-undo-count"}

	initialSHA := testutil.Rev(t, dir, "HEAD")

	// First commit
	if err := os.WriteFile(filepath.Join(dir, "one.txt"), []byte("one\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "first", "--", "one.txt")
	if code != 0 {
		t.Fatalf("first commit failed (code %d): %s", code, stderr)
	}

	// Second commit
	if err := os.WriteFile(filepath.Join(dir, "two.txt"), []byte("two\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegitEnv(t, dir, env, "commit", "-m", "second", "--", "two.txt")
	if code != 0 {
		t.Fatalf("second commit failed (code %d): %s", code, stderr)
	}

	// Undo both at once
	_, stderr, code = runSafegitEnv(t, dir, env, "undo", "--count", "2")
	if code != 0 {
		t.Fatalf("undo --count 2 failed (code %d): %s", code, stderr)
	}

	// HEAD should be back to initial
	afterSHA := testutil.Rev(t, dir, "HEAD")
	if afterSHA != initialSHA {
		t.Errorf("after undo --count 2, HEAD = %s, want %s (initial)", afterSHA, initialSHA)
	}

	// Neither file should be in HEAD's tree
	treeCmd := exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
	treeCmd.Dir = dir
	treeOut, err := treeCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	tree := string(treeOut)
	if strings.Contains(tree, "one.txt") {
		t.Error("one.txt still in HEAD tree after undo --count 2")
	}
	if strings.Contains(tree, "two.txt") {
		t.Error("two.txt still in HEAD tree after undo --count 2")
	}

	// Verify the index is clean (matches HEAD)
	statusCmd := exec.Command("git", "diff", "--cached", "--name-only")
	statusCmd.Dir = dir
	statusOut, err := statusCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		t.Errorf("index is not clean after undo: staged files = %s", strings.TrimSpace(string(statusOut)))
	}
}

// TestUndoCountExceedsAvailable tries to undo more operations than exist.
func TestUndoCountExceedsAvailable(t *testing.T) {
	dir := newRepo(t)
	env := []string{"CLAUDE_CODE_SESSION_ID=test-undo-exceeds"}

	// Make one commit
	if err := os.WriteFile(filepath.Join(dir, "only.txt"), []byte("only\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "only commit", "--", "only.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	// Try to undo 3 (only 1 available)
	_, stderr, code = runSafegitEnv(t, dir, env, "undo", "--count", "3")
	if code == 0 {
		t.Fatal("undo --count 3 should have failed (only 1 available), but exited 0")
	}
	if !strings.Contains(stderr, "only 1 undoable operations available, requested 3") {
		t.Errorf("expected 'only 1 undoable operations available, requested 3' in stderr, got: %s", stderr)
	}
}

// TestUndoCountAfterPreviousUndo creates 3 commits, undoes once, then undoes
// again with --count 1. The second undo should skip the undo entry and undo
// the next live commit.
func TestUndoCountAfterPreviousUndo(t *testing.T) {
	dir := newRepo(t)
	env := []string{"CLAUDE_CODE_SESSION_ID=test-undo-after-undo"}

	// Commit A
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "commit A", "--", "a.txt")
	if code != 0 {
		t.Fatalf("commit A failed (code %d): %s", code, stderr)
	}
	shaA := testutil.Rev(t, dir, "HEAD")

	// Commit B
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegitEnv(t, dir, env, "commit", "-m", "commit B", "--", "b.txt")
	if code != 0 {
		t.Fatalf("commit B failed (code %d): %s", code, stderr)
	}

	// Commit C
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("c\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegitEnv(t, dir, env, "commit", "-m", "commit C", "--", "c.txt")
	if code != 0 {
		t.Fatalf("commit C failed (code %d): %s", code, stderr)
	}

	// Undo once (undoes C)
	_, stderr, code = runSafegitEnv(t, dir, env, "undo")
	if code != 0 {
		t.Fatalf("first undo failed (code %d): %s", code, stderr)
	}

	// Verify we're now at B
	shaAfterFirstUndo := testutil.Rev(t, dir, "HEAD")
	// B is the commit after A, check that c.txt is gone
	treeCmd := exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
	treeCmd.Dir = dir
	treeOut, _ := treeCmd.Output()
	if strings.Contains(string(treeOut), "c.txt") {
		t.Fatal("c.txt still in tree after first undo")
	}
	_ = shaAfterFirstUndo

	// Undo again with --count 1 (should undo B, skipping the undo entry)
	_, stderr, code = runSafegitEnv(t, dir, env, "undo", "--count", "1")
	if code != 0 {
		t.Fatalf("second undo failed (code %d): %s", code, stderr)
	}

	// HEAD should now be at A
	shaAfterSecondUndo := testutil.Rev(t, dir, "HEAD")
	if shaAfterSecondUndo != shaA {
		t.Errorf("after second undo, HEAD = %s, want %s (commit A)", shaAfterSecondUndo, shaA)
	}

	// b.txt and c.txt should be gone, a.txt should remain
	treeCmd = exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
	treeCmd.Dir = dir
	treeOut, err := treeCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	tree := string(treeOut)
	if !strings.Contains(tree, "a.txt") {
		t.Error("a.txt missing from HEAD tree after second undo")
	}
	if strings.Contains(tree, "b.txt") {
		t.Error("b.txt still in HEAD tree after second undo")
	}
	if strings.Contains(tree, "c.txt") {
		t.Error("c.txt still in HEAD tree after second undo")
	}
}

// TestUndoCountZero verifies that --count 0 errors.
func TestUndoCountZero(t *testing.T) {
	dir := newRepo(t)
	env := []string{"CLAUDE_CODE_SESSION_ID=test-undo-zero"}

	// Make a commit so there's something to potentially undo
	if err := os.WriteFile(filepath.Join(dir, "z.txt"), []byte("z\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "commit z", "--", "z.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	// Try --count 0
	_, stderr, code = runSafegitEnv(t, dir, env, "undo", "--count", "0")
	if code == 0 {
		t.Fatal("undo --count 0 should have failed, but exited 0")
	}
	if !strings.Contains(stderr, "--count must be positive") {
		t.Errorf("expected '--count must be positive' in stderr, got: %s", stderr)
	}
}

// TestUndoRootCommit undoes the only safegit commit, leaving the branch with
// no commits (orphan state).
func TestUndoRootCommit(t *testing.T) {
	dir := evalTempDir(t)
	env := []string{"CLAUDE_CODE_SESSION_ID=test-undo-root"}

	// Create a fresh repo with NO initial commit (unlike newRepo which seeds one)
	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Make exactly one commit via safegit (this is the root commit)
	if err := os.WriteFile(filepath.Join(dir, "root.txt"), []byte("root content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "root commit", "--", "root.txt")
	if code != 0 {
		t.Fatalf("root commit failed (code %d): %s", code, stderr)
	}

	// Verify the commit exists
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	if _, err := cmd.Output(); err != nil {
		t.Fatal("HEAD should resolve after root commit")
	}

	// Undo the root commit
	_, stderr, code = runSafegitEnv(t, dir, env, "undo", "--count", "1")
	if code != 0 {
		t.Fatalf("undo root commit failed (code %d): %s", code, stderr)
	}

	// The branch ref should be deleted (git rev-parse HEAD should fail)
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("expected git rev-parse HEAD to fail after root undo, but got: %s", strings.TrimSpace(string(out)))
	}

	// git symbolic-ref HEAD should still work (still on branch main)
	cmd = exec.Command("git", "symbolic-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("symbolic-ref HEAD failed: %v", err)
	}
	if strings.TrimSpace(string(out)) != "refs/heads/main" {
		t.Errorf("expected symbolic-ref HEAD = refs/heads/main, got %s", strings.TrimSpace(string(out)))
	}

	// The index should be empty
	cmd = exec.Command("git", "ls-files")
	cmd.Dir = dir
	out, err = cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("index should be empty after root undo, got: %s", strings.TrimSpace(string(out)))
	}

	// The working tree should still have the file
	content, err := os.ReadFile(filepath.Join(dir, "root.txt"))
	if err != nil {
		t.Fatalf("root.txt should still exist in working tree: %v", err)
	}
	if string(content) != "root content\n" {
		t.Errorf("root.txt content = %q, want %q", string(content), "root content\n")
	}
}
