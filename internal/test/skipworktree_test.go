package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkipWorktreePreserved verifies that skip-worktree flags survive a
// safegit commit, which reconciles the shared index through
// git.ReconcileMainIndex -- a `git read-tree` that clears every flag, followed
// by a replay that re-sets the ones git.ListSkipWorktreeFiles collected first.
func TestSkipWorktreePreserved(t *testing.T) {
	dir := newRepo(t)

	// Create a file and commit it via git so it's tracked.
	trackedPath := filepath.Join(dir, "config.local")
	if err := os.WriteFile(trackedPath, []byte("local config\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "config.local"},
		{"git", "commit", "-m", "add config.local"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Set skip-worktree on config.local.
	cmd := exec.Command("git", "update-index", "--skip-worktree", "config.local")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git update-index --skip-worktree: %v\n%s", err, out)
	}

	// Verify skip-worktree is set before the safegit commit.
	assertSkipWorktree(t, dir, "config.local", true, "before safegit commit")

	// Create another file and commit it via safegit.
	newFile := filepath.Join(dir, "feature.txt")
	if err := os.WriteFile(newFile, []byte("feature\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "add feature", "--", "feature.txt")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify skip-worktree is still set after the safegit commit.
	assertSkipWorktree(t, dir, "config.local", true, "after safegit commit")
}

// TestSkipWorktreeMultipleFiles verifies preservation when multiple files
// have skip-worktree set.
func TestSkipWorktreeMultipleFiles(t *testing.T) {
	dir := newRepo(t)

	// Create and commit several files.
	skipFiles := []string{"local-a.cfg", "local-b.cfg", "local-c.cfg"}
	for _, name := range skipFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	addArgs := append([]string{"add"}, skipFiles...)
	cmd := exec.Command("git", addArgs...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "add config files")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	// Set skip-worktree on all of them.
	for _, name := range skipFiles {
		cmd := exec.Command("git", "update-index", "--skip-worktree", name)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git update-index --skip-worktree %s: %v\n%s", name, err, out)
		}
	}

	// Commit a new file via safegit.
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("other\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "add other", "--", "other.txt")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// All skip-worktree flags should be preserved.
	for _, name := range skipFiles {
		assertSkipWorktree(t, dir, name, true, "after safegit commit")
	}
}

// assertSkipWorktree checks whether a file has the skip-worktree flag set.
func assertSkipWorktree(t *testing.T, dir, file string, expected bool, context string) {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-v")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files -v failed: %v", err)
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(line, "S ") && strings.TrimSpace(line[2:]) == file {
			found = true
			break
		}
	}
	if expected && !found {
		t.Errorf("%s: expected skip-worktree on %s but flag is not set\nls-files -v output:\n%s", context, file, string(out))
	}
	if !expected && found {
		t.Errorf("%s: expected no skip-worktree on %s but flag is set", context, file)
	}
}
