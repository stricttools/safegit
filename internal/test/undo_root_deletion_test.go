package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Undoing the only commit on a branch DELETES the ref -- there is no earlier
// position to move it back to. safegit did that on purpose, and the oplog says
// so, so the readers of that log stop there instead of walking on to the commit
// the undo reversed and reporting the deletion as work done behind safegit's
// back.

// TestBypassDetectIsQuietAfterARootUndo: undoing the only commit on a branch
// deletes the ref, which safegit did deliberately. doctor read past the undo
// entry to the commit it reversed, found a tip the ref no longer resolves to,
// and reported an error over safegit's own work.
func TestBypassDetectIsQuietAfterARootUndo(t *testing.T) {
	dir := evalTempDir(t)
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// The ROOT commit, made by safegit: undoing it deletes refs/heads/main.
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "root", "--", "a.txt"); code != 0 {
		t.Fatalf("the root commit failed (code %d): %s", code, stderr)
	}
	if _, stderr, code := runSafegit(t, dir, "undo", "--bypass-session"); code != 0 {
		t.Fatalf("the root undo failed (code %d): %s", code, stderr)
	}

	stdout, stderr, code := runSafegit(t, dir, "doctor", "--action", "diagnose")
	line := doctorLine(stdout, "bypass_detect")
	if strings.HasPrefix(line, "[error]") || strings.HasPrefix(line, "[FAIL]") || strings.HasPrefix(line, "[WARN]") {
		t.Errorf("doctor reported a bypass over a ref safegit's own undo deleted: %q\nfull output:\n%s", line, stdout)
	}
	if code != 0 {
		t.Errorf("doctor exited %d after safegit's own root undo\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
}
