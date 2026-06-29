package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newEmptyRepo creates a git repo with no commits (empty HEAD).
func newEmptyRepo(t *testing.T) string {
	t.Helper()
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
	return dir
}

func TestAmendRootCommit(t *testing.T) {
	dir := newEmptyRepo(t)

	// Create a file and make the root commit via safegit.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("v1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "root commit", "--", "file.txt")
	if code != 0 {
		t.Fatalf("root commit failed (code %d): %s", code, stderr)
	}

	// Verify there is exactly 1 commit.
	if n := gitLog(t, dir, "HEAD"); n != 1 {
		t.Fatalf("expected 1 commit after root, got %d", n)
	}

	// Record the original SHA.
	origCmd := exec.Command("git", "rev-parse", "HEAD")
	origCmd.Dir = dir
	origOut, err := origCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	origSHA := strings.TrimSpace(string(origOut))

	// Modify the file and amend the root commit.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("v2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "amended root", "--", "file.txt")
	if code != 0 {
		t.Fatalf("amend root failed (code %d): %s", code, stderr)
	}

	// Output should mention "amended".
	if !strings.Contains(stdout, "amended") {
		t.Fatalf("amend output missing 'amended': %s", stdout)
	}

	// Still exactly 1 commit (amend replaces, doesn't add).
	if n := gitLog(t, dir, "HEAD"); n != 1 {
		t.Fatalf("expected 1 commit after amend, got %d", n)
	}

	// HEAD should have changed.
	newCmd := exec.Command("git", "rev-parse", "HEAD")
	newCmd.Dir = dir
	newOut, err := newCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	newSHA := strings.TrimSpace(string(newOut))
	if newSHA == origSHA {
		t.Fatal("HEAD did not change after amend")
	}

	// Verify the commit message was updated.
	msg := commitMessage(t, dir, "HEAD")
	if !strings.Contains(msg, "amended root") {
		t.Fatalf("expected commit message to contain 'amended root', got: %s", msg)
	}

	// Verify the file content is the amended version.
	showCmd := exec.Command("git", "show", "HEAD:file.txt")
	showCmd.Dir = dir
	showOut, err := showCmd.Output()
	if err != nil {
		t.Fatalf("git show HEAD:file.txt: %v", err)
	}
	if strings.TrimSpace(string(showOut)) != "v2" {
		t.Fatalf("expected file content 'v2', got: %s", strings.TrimSpace(string(showOut)))
	}
}
