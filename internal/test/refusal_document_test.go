package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lastError returns the message of the last error diagnostic in env, or "".
func lastError(env machineEnvelope) string {
	msg := ""
	for _, d := range env.Diagnostics {
		if d["level"] == "error" {
			msg = d["message"]
		}
	}
	return msg
}

// TestRefusalUnderJSONEmitsTheDocument: a refusal ends the command through the
// framework's exit step, so under --json stdout still carries the one document,
// with the refusal's exit code and its reason as the last error diagnostic --
// and nothing on stderr repeats it.
func TestRefusalUnderJSONEmitsTheDocument(t *testing.T) {
	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runSafegit(t, dir, "--json", "commit", "--", "a.txt")
	if code != 2 {
		t.Fatalf("commit without a message exited %d, want 2 (usage): %s", code, stderr)
	}
	env := decodeEnvelope(t, stdout)
	if env.ExitCode != 2 {
		t.Errorf("document exit_code = %d, want 2", env.ExitCode)
	}
	if got := lastError(env); !strings.Contains(got, "commit message required") {
		t.Errorf("last error diagnostic = %q, want the refusal's reason; diagnostics: %v", got, env.Diagnostics)
	}
	if strings.Contains(stderr, "commit message required") {
		t.Errorf("under --json the refusal belongs in diagnostics, not on stderr: %s", stderr)
	}
}

// TestRefusalInHumanModePrintsOneErrorLine: the same refusal in human mode is
// one "error: " line on stderr and nothing on stdout.
func TestRefusalInHumanModePrintsOneErrorLine(t *testing.T) {
	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runSafegit(t, dir, "commit", "--", "a.txt")
	if code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, stderr)
	}
	if stdout != "" {
		t.Errorf("a refusal wrote to stdout: %q", stdout)
	}
	if n := strings.Count(stderr, "error: "); n != 1 || !strings.Contains(stderr, "error: commit message required") {
		t.Errorf("want one 'error: commit message required' line, got %d error lines:\n%s", n, stderr)
	}
}

// TestRefusalAfterTheOperationLockReleasesIt: a refusal raised while the
// worktree operation lock is held ends the command through the unwind, so the
// lock is released and the next command does not wait for it.
func TestRefusalAfterTheOperationLockReleasesIt(t *testing.T) {
	dir := newRepo(t)
	// No such file: intake refuses after the commit has taken its lock.
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "x", "--", "missing.txt"); code == 0 {
		t.Fatalf("committing a missing file succeeded: %s", stderr)
	}
	locks := filepath.Join(dir, ".git", "safegit", "locks")
	filepath.WalkDir(locks, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".lock") {
			t.Errorf("lock file %s left behind after a refusal", path)
		}
		return nil
	})
}
