package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A linked worktree has its own git dir (.git/worktrees/<name>) but shares the
// repository: the same refs, the same objects, the same git hook directory. The
// hook stores follow that split exactly once, and these tests pin which side of
// it each store is on.
//
//   - The LIVE store is repository-level policy, like the ref locks: it lives
//     under the COMMON git dir, so a hook installed from a linked worktree is
//     the same hook a push from the main worktree runs.
//   - The LEGACY location is git's own hook directory, which is common in a
//     linked worktree, so migration and the refusal that precedes it are
//     repository-wide facts that every worktree agrees on.
//
// The TRACKED store is deliberately not here: it is checkout content, so it is
// per-worktree by nature and each worktree runs the hooks its own checkout has.

// addLinkedWorktree creates a linked worktree of repoDir on a new branch and
// returns its path.
func addLinkedWorktree(t *testing.T, repoDir, branch string) string {
	t.Helper()
	wt := filepath.Join(evalTempDir(t), "linked-"+branch)
	cmd := exec.Command("git", "worktree", "add", "-b", branch, wt)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	return wt
}

// TestHookInstalledFromLinkedWorktreeRunsInTheMainWorktree: `hook install` in a
// linked worktree writes into the repository's live store, so the main
// worktree's push runs it. Keying the live store on the per-worktree git dir
// made the two worktrees disagree about which checks the repository has.
func TestHookInstalledFromLinkedWorktreeRunsInTheMainWorktree(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	wt := addLinkedWorktree(t, dir, "side")

	marker := filepath.Join(evalTempDir(t), "worktree-installed-hook-ran.txt")
	src := filepath.Join(wt, "hooksrc", "pre-pre-push")
	writeHookScript(t, src, "printf ran > "+marker)

	if _, stderr, code := runSafegit(t, wt, "hook", "install", src); code != 0 {
		t.Fatalf("hook install from the linked worktree failed (%d): %s", code, stderr)
	}

	// The repository's live store, which is the common git dir's.
	installed := filepath.Join(dir, ".git", "safegit", "hooks", "pre-pre-push")
	if _, err := os.Stat(installed); err != nil {
		t.Errorf("install from a linked worktree did not write the repository's live store %s: %v", installed, err)
	}

	listOut, listErr, listCode := runSafegit(t, dir, "hook", "list")
	if listCode != 0 {
		t.Fatalf("hook list in the main worktree failed (%d): %s", listCode, listErr)
	}
	if !strings.Contains(listOut, "pre-pre-push") {
		t.Errorf("the main worktree cannot see the hook installed from the linked one:\n%s", listOut)
	}

	if _, stderr, code := runSafegit(t, dir, "push", "--refs", "head", "origin"); code != 0 {
		t.Fatalf("push from the main worktree failed (%d): %s", code, stderr)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the push did not run the hook installed from the linked worktree (marker %s absent: %v)", marker, err)
	}
}

// TestHookMigrateFromLinkedWorktreeMovesTheCommonHooks: git's hook directory is
// COMMON, so a hook in the pre-migration location is one repository-wide fact.
// Migration run from a linked worktree must find and move it; looking for it
// under the worktree's own git dir found an empty directory and reported
// nothing to migrate while every push kept refusing.
func TestHookMigrateFromLinkedWorktreeMovesTheCommonHooks(t *testing.T) {
	dir := newRepo(t)
	wt := addLinkedWorktree(t, dir, "side")

	marker := filepath.Join(evalTempDir(t), "migrated-hook-ran.txt")
	legacy := filepath.Join(dir, ".git", "hooks", "pre-pre-push")
	appendingHook(t, legacy, marker, "legacy")
	legacyNested := filepath.Join(dir, ".git", "hooks", "pre-pre-push.d", "20-nested")
	appendingHook(t, legacyNested, marker, "nested")

	stdout, stderr, code := runSafegit(t, wt, "hook", "migrate")
	if code != 0 {
		t.Fatalf("hook migrate from the linked worktree failed (%d): %s", code, stderr)
	}
	if strings.Contains(stdout, "nothing to migrate") {
		t.Fatalf("migrate from a linked worktree did not see the repository's legacy hooks: %q", stdout)
	}

	for _, rel := range []string{"pre-pre-push", filepath.Join("pre-pre-push.d", "20-nested")} {
		moved := filepath.Join(dir, ".git", "safegit", "hooks", rel)
		if _, err := os.Stat(moved); err != nil {
			t.Errorf("%s did not arrive in the repository's live store (%s): %v", rel, moved, err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".git", "hooks", rel)); !os.IsNotExist(err) {
			t.Errorf("%s is still in the legacy location (err=%v)", rel, err)
		}
	}

	// The migrated hooks now run from either worktree.
	if _, stderr, code := runSafegit(t, wt, "hook", "run"); code != 0 {
		t.Fatalf("hook run in the linked worktree after migrate failed (%d): %s", code, stderr)
	}
	if got := markerLines(t, marker); len(got) != 2 {
		t.Errorf("the migrated hooks did not both run from the linked worktree: %v", got)
	}
}

// TestDoctorFromLinkedWorktreeReportsUnmigratedHooks: the refusal is
// repository-wide, so doctor must report it from any worktree. Reporting
// hooks_migrated OK in a linked worktree while every push in the repository
// exits 24 is the false negative this pins.
func TestDoctorFromLinkedWorktreeReportsUnmigratedHooks(t *testing.T) {
	dir := newRepo(t)
	wt := addLinkedWorktree(t, dir, "side")

	// A linked worktree has its own state directory, and an uninitialized one
	// is an error-severity finding of its own. Initializing it first keeps the
	// exit code below attributable to the hook finding alone.
	if _, stderr, code := runSafegit(t, wt, "config", "set", "commit.casMaxAttempts", "200"); code != 0 {
		t.Fatalf("initializing the linked worktree failed (%d): %s", code, stderr)
	}

	legacy := filepath.Join(dir, ".git", "hooks", "pre-pre-push")
	writeHookScript(t, legacy, "true")

	stdout, _, code := runSafegit(t, wt, "doctor", "--action", "diagnose")
	if code != 50 {
		t.Errorf("doctor in a linked worktree over an unmigrated repository exited %d, want 50:\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "hooks_migrated") || !strings.Contains(stdout, "hook migrate") {
		t.Errorf("doctor did not report the unmigrated hooks from the linked worktree:\n%s", stdout)
	}

	// The same fact from the command that refuses on it.
	if _, stderr, runCode := runSafegit(t, wt, "hook", "run"); runCode != 24 {
		t.Errorf("hook run in the linked worktree exited %d, want 24: %s", runCode, stderr)
	}
}
