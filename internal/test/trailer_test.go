package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runSafegitEnv executes the safegit binary with extra environment variables
// layered on the controlled test environment (nothing is inherited from the
// parent process beyond the allowlist in controlledEnv).
// It passes argv through untouched; runSafegitNoConsent is the identical
// helper that tests about consent name explicitly.
func runSafegitEnv(t *testing.T, repoDir string, env []string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	return runSafegitNoConsent(t, repoDir, env, args...)
}

// commitMessage returns the full commit message of the given ref.
func commitMessage(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "log", "-1", "--format=%B", ref)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log -1 --format=%%B %s: %v", ref, err)
	}
	return string(out)
}

func TestSessionTrailer_CommitWithEnv(t *testing.T) {
	dir := newRepo(t)

	// Create a file
	if err := os.WriteFile(filepath.Join(dir, "trailer.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Commit with CLAUDE_CODE_SESSION_ID set
	env := []string{"CLAUDE_CODE_SESSION_ID=test-session-abc"}
	stdout, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "test commit", "--", "trailer.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify the commit message contains the trailer
	msg := commitMessage(t, dir, "HEAD")
	if !strings.Contains(msg, "Claude-Code-Session-Id: test-session-abc") {
		t.Errorf("expected session trailer in commit message, got:\n%s", msg)
	}
}

func TestSessionTrailer_CommitWithoutEnv(t *testing.T) {
	dir := newRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "notrailer.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Commit with no CLAUDE_CODE_SESSION_ID anywhere in the environment.
	// controlledEnv is a stronger guarantee than filtering the parent env: the
	// child sees only the allowlist, so nothing ambient can supply a session id.
	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "no trailer", "--", "notrailer.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	msg := commitMessage(t, dir, "HEAD")
	if strings.Contains(msg, "Claude-Code-Session-Id") {
		t.Errorf("expected no session trailer without env var, got:\n%s", msg)
	}
}

func TestCustomTrailer_SingleTrailer(t *testing.T) {
	dir := newRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "custom.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "--trailer", "Agent: test123", "-m", "test commit", "--", "custom.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	msg := commitMessage(t, dir, "HEAD")
	if !strings.Contains(msg, "Agent: test123") {
		t.Errorf("expected custom trailer in commit message, got:\n%s", msg)
	}
}

func TestCustomTrailer_MultipleTrailers(t *testing.T) {
	dir := newRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "multi.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "--trailer", "A: 1", "--trailer", "B: 2", "-m", "test", "--", "multi.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	msg := commitMessage(t, dir, "HEAD")
	if !strings.Contains(msg, "A: 1") {
		t.Errorf("expected trailer A: 1 in commit message, got:\n%s", msg)
	}
	if !strings.Contains(msg, "B: 2") {
		t.Errorf("expected trailer B: 2 in commit message, got:\n%s", msg)
	}
}

func TestCustomTrailer_WithSessionTrailer(t *testing.T) {
	dir := newRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "both.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Commit with both --trailer and CLAUDE_CODE_SESSION_ID
	env := []string{"CLAUDE_CODE_SESSION_ID=session-xyz"}
	stdout, stderr, code := runSafegitEnv(t, dir, env, "commit", "--trailer", "Agent: bot1", "-m", "test", "--", "both.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	msg := commitMessage(t, dir, "HEAD")
	if !strings.Contains(msg, "Agent: bot1") {
		t.Errorf("expected custom trailer in commit message, got:\n%s", msg)
	}
	if !strings.Contains(msg, "Claude-Code-Session-Id: session-xyz") {
		t.Errorf("expected session trailer in commit message, got:\n%s", msg)
	}

	// Custom trailer should come before the session trailer
	customIdx := strings.Index(msg, "Agent: bot1")
	sessionIdx := strings.Index(msg, "Claude-Code-Session-Id: session-xyz")
	if customIdx >= sessionIdx {
		t.Errorf("expected custom trailer before session trailer, got custom at %d, session at %d in:\n%s", customIdx, sessionIdx, msg)
	}
}

func TestSessionTrailer_AmendDifferentSession(t *testing.T) {
	dir := newRepo(t)

	// Create and commit a file with session A
	if err := os.WriteFile(filepath.Join(dir, "amend.txt"), []byte("v1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	env := []string{"CLAUDE_CODE_SESSION_ID=session-aaa"}
	stdout, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "original commit", "--", "amend.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify original trailer
	msg := commitMessage(t, dir, "HEAD")
	if !strings.Contains(msg, "Claude-Code-Session-Id: session-aaa") {
		t.Fatalf("expected session-aaa trailer, got:\n%s", msg)
	}

	// Amend with session B
	if err := os.WriteFile(filepath.Join(dir, "amend.txt"), []byte("v2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	env2 := []string{"CLAUDE_CODE_SESSION_ID=session-bbb"}
	stdout, stderr, code = runSafegitEnv(t, dir, env2, "commit", "--amend", "-m", "amended commit", "--", "amend.txt")
	if code != 0 {
		t.Fatalf("amend failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Both trailers should be present: the new message gets session-bbb injected,
	// but since it's a new message (-m), it won't have session-aaa unless
	// the original message was reused. With -m the user provides a fresh message,
	// so only the new session trailer will be present.
	msg = commitMessage(t, dir, "HEAD")
	if !strings.Contains(msg, "Claude-Code-Session-Id: session-bbb") {
		t.Errorf("expected session-bbb trailer in amended commit, got:\n%s", msg)
	}
}

func TestSessionTrailer_AmendKeepMessage_DifferentSession(t *testing.T) {
	dir := newRepo(t)

	// Create and commit a file with session A
	if err := os.WriteFile(filepath.Join(dir, "amend2.txt"), []byte("v1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	env := []string{"CLAUDE_CODE_SESSION_ID=session-aaa"}
	stdout, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "keep this message", "--", "amend2.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Amend without -m (keep message) with session B
	if err := os.WriteFile(filepath.Join(dir, "amend2.txt"), []byte("v2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	env2 := []string{"CLAUDE_CODE_SESSION_ID=session-bbb"}
	stdout, stderr, code = runSafegitEnv(t, dir, env2, "commit", "--amend", "--", "amend2.txt")
	if code != 0 {
		t.Fatalf("amend failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Both session trailers should be present (original message reused + new session)
	msg := commitMessage(t, dir, "HEAD")
	if !strings.Contains(msg, "Claude-Code-Session-Id: session-aaa") {
		t.Errorf("expected session-aaa trailer preserved, got:\n%s", msg)
	}
	if !strings.Contains(msg, "Claude-Code-Session-Id: session-bbb") {
		t.Errorf("expected session-bbb trailer added, got:\n%s", msg)
	}
}

func TestSessionTrailer_AmendKeepMessage_SameSession(t *testing.T) {
	dir := newRepo(t)

	// Create and commit a file with session A
	if err := os.WriteFile(filepath.Join(dir, "dedup.txt"), []byte("v1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	env := []string{"CLAUDE_CODE_SESSION_ID=session-same"}
	stdout, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "dedup test", "--", "dedup.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Amend without -m (keep message) with the same session
	if err := os.WriteFile(filepath.Join(dir, "dedup.txt"), []byte("v2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = runSafegitEnv(t, dir, env, "commit", "--amend", "--", "dedup.txt")
	if code != 0 {
		t.Fatalf("amend failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Should have exactly one trailer (dedup)
	msg := commitMessage(t, dir, "HEAD")
	count := strings.Count(msg, "Claude-Code-Session-Id: session-same")
	if count != 1 {
		t.Errorf("expected exactly 1 session trailer (dedup), got %d in:\n%s", count, msg)
	}
}
