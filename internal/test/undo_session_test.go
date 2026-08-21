package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// runSafegitCleanEnv runs safegit with the controlled test environment, which
// never carries CLAUDE_CODE_SESSION_ID unless a caller passes it explicitly.
// It passes argv through untouched; nothing it dispatches is consequential.
func runSafegitCleanEnv(t *testing.T, repoDir string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	return runSafegitNoConsent(t, repoDir, nil, args...)
}

// TestUndoSessionScoped verifies that session A's undo finds session A's commit,
// not session B's, even when B committed more recently.
func TestUndoSessionScoped(t *testing.T) {
	dir := newRepo(t)
	envA := []string{"CLAUDE_CODE_SESSION_ID=session-A"}
	envB := []string{"CLAUDE_CODE_SESSION_ID=session-B"}

	// Session A commits
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("from A\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, dir, envA, "commit", "-m", "commit from A", "--", "a.txt")
	if code != 0 {
		t.Fatalf("session A commit failed (code %d): %s", code, stderr)
	}
	shaAfterA := testutil.Rev(t, dir, "HEAD")

	// Session B commits on top
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("from B\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegitEnv(t, dir, envB, "commit", "-m", "commit from B", "--", "b.txt")
	if code != 0 {
		t.Fatalf("session B commit failed (code %d): %s", code, stderr)
	}
	shaAfterB := testutil.Rev(t, dir, "HEAD")

	if shaAfterA == shaAfterB {
		t.Fatal("HEAD did not advance after session B commit")
	}

	// Session B undoes -- should undo B's commit, not A's
	_, stderr, code = runSafegitEnv(t, dir, envB, "undo")
	if code != 0 {
		t.Fatalf("session B undo failed (code %d): %s", code, stderr)
	}

	// HEAD should now be at session A's commit
	shaAfterUndo := testutil.Rev(t, dir, "HEAD")
	if shaAfterUndo != shaAfterA {
		t.Errorf("after session B undo, HEAD = %s, want %s (session A's commit)", shaAfterUndo, shaAfterA)
	}

	// b.txt should be gone from tree, a.txt should remain
	treeCmd := exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
	treeCmd.Dir = dir
	treeOut, err := treeCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	tree := string(treeOut)
	if !strings.Contains(tree, "a.txt") {
		t.Error("a.txt missing from HEAD tree after session B undo")
	}
	if strings.Contains(tree, "b.txt") {
		t.Error("b.txt still in HEAD tree after session B undo")
	}
}

// TestUndoSessionScopedBranchMoved verifies that when session A tries to undo
// after session B committed on top, the CAS detects the branch moved and fails.
func TestUndoSessionScopedBranchMoved(t *testing.T) {
	dir := newRepo(t)
	envA := []string{"CLAUDE_CODE_SESSION_ID=session-A"}
	envB := []string{"CLAUDE_CODE_SESSION_ID=session-B"}

	// Session A commits
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("from A\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, dir, envA, "commit", "-m", "commit from A", "--", "a.txt")
	if code != 0 {
		t.Fatalf("session A commit failed (code %d): %s", code, stderr)
	}

	// Session B commits on top
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("from B\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegitEnv(t, dir, envB, "commit", "-m", "commit from B", "--", "b.txt")
	if code != 0 {
		t.Fatalf("session B commit failed (code %d): %s", code, stderr)
	}
	shaBeforeUndo := testutil.Rev(t, dir, "HEAD")

	// Session A tries to undo -- should fail because the branch moved past A's tip
	_, stderr, code = runSafegitEnv(t, dir, envA, "undo")
	if code == 0 {
		t.Fatal("session A undo should have failed (branch moved), but exited 0")
	}
	if !strings.Contains(stderr, "update-ref failed") {
		t.Errorf("expected update-ref failure in stderr, got: %s", stderr)
	}

	// HEAD should not have changed
	shaAfterUndo := testutil.Rev(t, dir, "HEAD")
	if shaAfterUndo != shaBeforeUndo {
		t.Errorf("HEAD changed despite failed undo: %s -> %s", shaBeforeUndo, shaAfterUndo)
	}
}

// TestUndoBypassSession verifies that --bypass-session undoes the latest
// operation regardless of session ID.
func TestUndoBypassSession(t *testing.T) {
	dir := newRepo(t)
	envA := []string{"CLAUDE_CODE_SESSION_ID=session-A"}
	envB := []string{"CLAUDE_CODE_SESSION_ID=session-B"}

	// Session A commits
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("from A\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, dir, envA, "commit", "-m", "commit from A", "--", "a.txt")
	if code != 0 {
		t.Fatalf("session A commit failed (code %d): %s", code, stderr)
	}
	shaAfterA := testutil.Rev(t, dir, "HEAD")

	// Session B commits on top
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("from B\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegitEnv(t, dir, envB, "commit", "-m", "commit from B", "--", "b.txt")
	if code != 0 {
		t.Fatalf("session B commit failed (code %d): %s", code, stderr)
	}

	// Use --bypass-session with session A's env -- should undo B's commit (the latest)
	_, stderr, code = runSafegitEnv(t, dir, envA, "undo", "--bypass-session")
	if code != 0 {
		t.Fatalf("bypass-session undo failed (code %d): %s", code, stderr)
	}

	// HEAD should be at session A's commit
	shaAfterUndo := testutil.Rev(t, dir, "HEAD")
	if shaAfterUndo != shaAfterA {
		t.Errorf("after bypass-session undo, HEAD = %s, want %s", shaAfterUndo, shaAfterA)
	}
}

// TestUndoNoSessionIDErrors verifies that without CLAUDE_CODE_SESSION_ID set
// and without --bypass-session, undo errors with a clear message.
func TestUndoNoSessionIDErrors(t *testing.T) {
	dir := newRepo(t)

	// Commit with a clean env (no session ID) -- commit doesn't require session ID
	if err := os.WriteFile(filepath.Join(dir, "nosid.txt"), []byte("no session\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitCleanEnv(t, dir, "commit", "-m", "commit without session", "--", "nosid.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	// Undo without session ID and without --bypass-session should fail
	_, stderr, code = runSafegitCleanEnv(t, dir, "undo")
	if code == 0 {
		t.Fatal("undo without session ID should have failed, but exited 0")
	}
	if !strings.Contains(stderr, "CLAUDE_CODE_SESSION_ID") {
		t.Errorf("error message should mention CLAUDE_CODE_SESSION_ID, got: %s", stderr)
	}
	if !strings.Contains(stderr, "--bypass-session") {
		t.Errorf("error message should mention --bypass-session, got: %s", stderr)
	}
}

// TestUndoNoSessionIDWithBypass verifies that without CLAUDE_CODE_SESSION_ID
// but with --bypass-session, undo works (falls back to unscoped).
func TestUndoNoSessionIDWithBypass(t *testing.T) {
	dir := newRepo(t)

	preSHA := testutil.Rev(t, dir, "HEAD")

	// Commit without session ID
	if err := os.WriteFile(filepath.Join(dir, "bypass.txt"), []byte("bypass\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitCleanEnv(t, dir, "commit", "-m", "commit for bypass", "--", "bypass.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	postSHA := testutil.Rev(t, dir, "HEAD")
	if preSHA == postSHA {
		t.Fatal("HEAD did not advance after commit")
	}

	// Undo with --bypass-session should work even without session ID
	_, stderr, code = runSafegitCleanEnv(t, dir, "undo", "--bypass-session")
	if code != 0 {
		t.Fatalf("undo with --bypass-session failed (code %d): %s", code, stderr)
	}

	undoneSHA := testutil.Rev(t, dir, "HEAD")
	if undoneSHA != preSHA {
		t.Errorf("after undo HEAD = %s, want %s (pre-commit)", undoneSHA, preSHA)
	}
}
