// Package testutil provides shared test helpers for creating temporary git repos, used across internal/*_test.go packages to avoid duplicating boilerplate.
//
// This package intentionally does NOT import internal/repo (or any other
// internal/* package) to avoid import cycles -- internal/git is at the
// bottom of the dependency graph and its tests need these helpers too.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// InitRepo creates a temp git repo with a seed file ("seed.txt") and initial
// commit, runs the provided safegitInit function to set up .git/safegit/, and
// returns (repoDir, gitDir, safegitDir).
//
// Callers pass repo.Init as safegitInit:
//
//	dir, gitDir, sgDir := testutil.InitRepo(t, repo.Init)
func InitRepo(t *testing.T, safegitInit func(gitDir string) error) (repoDir, gitDir, safegitDir string) {
	t.Helper()
	dir := evalTempDir(t)

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

	// Seed file so the initial commit has a non-empty tree
	seedPath := filepath.Join(dir, "seed.txt")
	if err := os.WriteFile(seedPath, []byte("seed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "seed.txt"},
		{"git", "commit", "-m", "initial"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	gd := filepath.Join(dir, ".git")
	if err := safegitInit(gd); err != nil {
		t.Fatalf("safegit init: %v", err)
	}
	sgDir := filepath.Join(gd, "safegit")
	return dir, gd, sgDir
}

// InitBareRepo creates a temp git repo with an allow-empty initial commit
// (no seed file, no safegit init). Returns the repo directory. Suitable for
// packages like git and index that don't need safegit infrastructure.
func InitBareRepo(t *testing.T) string {
	t.Helper()
	dir := evalTempDir(t)

	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "initial"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	return dir
}

// evalTempDir creates a temp directory and resolves symlinks in the path.
// On macOS, t.TempDir() returns paths like /var/folders/... which is a
// symlink to /private/var/folders/...; git resolves symlinks, causing
// path comparison mismatches in test assertions.
func evalTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", dir, err)
	}
	return resolved
}

// Chdir changes into dir for the duration of the test, restoring the
// original working directory on cleanup.
func Chdir(t *testing.T, dir string) {
	t.Helper()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}
