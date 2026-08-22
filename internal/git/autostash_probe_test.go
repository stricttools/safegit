package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// RECORDED-FACT probes for what git itself does with MERGE_AUTOSTASH when a
// stopped merge is concluded with `git merge --continue`.
//
// safegit's own merge conclusion mirrors this, so the mirror needs a written-
// down original: what git prints, what it leaves on disk, where the work ends up
// when the apply cannot succeed, and -- the one place safegit deliberately
// differs -- what it exits with. If a future git changes any of it, one of these
// fails and the divergence is re-decided against fact rather than memory.

// autostashMerge builds a repository stopped in a conflicted merge that carries
// an autostash, and returns the repo directory and the file the autostash holds.
//
// sideOnBranch decides whether the merged branch also changes that file, which
// is what makes the difference between an autostash that applies cleanly
// afterwards and one that conflicts.
func autostashMerge(t *testing.T, sideOnBranch bool) (repoDir, stashedFile, stashedContent string) {
	t.Helper()
	dir := testutil.InitBareRepo(t)

	testutil.WriteFile(t, dir, "f.txt", "base\n")
	testutil.WriteFile(t, dir, "side.txt", "side base\n")
	testutil.Git(t, dir, "add", "f.txt", "side.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "base")

	testutil.Git(t, dir, "checkout", "-q", "-b", "side")
	testutil.WriteFile(t, dir, "f.txt", "side\n")
	if sideOnBranch {
		testutil.WriteFile(t, dir, "side.txt", "side from the branch\n")
	}
	testutil.Git(t, dir, "commit", "-q", "-am", "side change")

	testutil.Git(t, dir, "checkout", "-q", "main")
	testutil.WriteFile(t, dir, "f.txt", "main\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "main change")

	const uncommitted = "uncommitted work\n"
	testutil.WriteFile(t, dir, "side.txt", uncommitted)

	out, code := testutil.GitTry(t, dir, "merge", "--autostash", "side")
	if code == 0 {
		t.Fatalf("git merge --autostash was expected to conflict but succeeded:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_AUTOSTASH")); err != nil {
		t.Fatalf("git wrote no MERGE_AUTOSTASH for the autostashed merge:\n%s", out)
	}
	return dir, "side.txt", uncommitted
}

// resolveAndContinue resolves the conflicted file and runs `git merge
// --continue`, returning git's combined output and exit code.
func resolveAndContinue(t *testing.T, dir string) (string, int) {
	t.Helper()
	testutil.WriteFile(t, dir, "f.txt", "resolved\n")
	testutil.Git(t, dir, "add", "f.txt")
	return testutil.GitTryEnv(t, dir, []string{"GIT_EDITOR=true"}, "merge", "--continue")
}

// TestGitMergeContinueAppliesTheAutostash records the happy path: git puts the
// work back, says "Applied autostash.", and removes the file.
func TestGitMergeContinueAppliesTheAutostash(t *testing.T) {
	dir, stashedFile, stashedContent := autostashMerge(t, false)

	out, code := resolveAndContinue(t, dir)
	if code != 0 {
		t.Fatalf("git merge --continue exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "Applied autostash.") {
		t.Errorf("git did not report applying the autostash:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_AUTOSTASH")); !os.IsNotExist(err) {
		t.Errorf("git left MERGE_AUTOSTASH behind (stat err %v)", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, stashedFile))
	if err != nil || string(data) != stashedContent {
		t.Errorf("git did not restore the stashed work: %q (err %v)", data, err)
	}
}

// TestGitMergeContinueStoresAnUnappliableAutostash records the failure path, and
// with it the ONE thing safegit does differently.
//
// git stores the stash commit on refs/stash, removes MERGE_AUTOSTASH, tells the
// operator where the work is -- and exits ZERO. safegit mirrors everything here
// except the exit code: a conclusion that could not restore the operator's work
// exits nonzero, so a script cannot read the outcome as clean. See
// docs/divergences.md.
func TestGitMergeContinueStoresAnUnappliableAutostash(t *testing.T) {
	dir, stashedFile, _ := autostashMerge(t, true)

	out, code := resolveAndContinue(t, dir)
	if code != 0 {
		t.Errorf("git merge --continue exited %d after a conflicting autostash apply, want 0 "+
			"(safegit deliberately exits nonzero here; this probe is what that divergence is measured against):\n%s", code, out)
	}
	if !strings.Contains(out, "stash") {
		t.Errorf("git did not say where the work went:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_AUTOSTASH")); !os.IsNotExist(err) {
		t.Errorf("git left MERGE_AUTOSTASH behind alongside the stored stash (stat err %v)", err)
	}
	if list := testutil.Git(t, dir, "stash", "list"); strings.TrimSpace(list) == "" {
		t.Fatalf("git stored no stash entry, so the work would be unreachable:\n%s", out)
	}
	stashed := testutil.Git(t, dir, "show", "stash@{0}:"+stashedFile)
	if !strings.Contains(stashed, "uncommitted work") {
		t.Errorf("the stash entry does not hold the operator's work: %q", stashed)
	}
}
