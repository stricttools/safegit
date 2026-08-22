package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// Doctor's two hook findings, the exit code its findings now decide, and the
// one resolution both the commit pipeline and doctor ask git for.
//
// safegit runs three of git's hooks -- pre-commit, commit-msg, post-commit --
// and never opens an editor, so `prepare-commit-msg` and everything else in the
// repository's hook directory simply does not run under safegit. That is
// legitimate and it was also invisible; doctor states it.

// TestDoctorNamesGitHooksSafegitNeverRuns: the finding names the files and says
// which hooks safegit does run, and it is advisory -- a repository is free to
// keep hooks for the git commands it still uses directly.
func TestDoctorNamesGitHooksSafegitNeverRuns(t *testing.T) {
	dir := newRepo(t)

	writeHookScript(t, filepath.Join(dir, ".git", "hooks", "prepare-commit-msg"), "true")
	writeHookScript(t, filepath.Join(dir, ".git", "hooks", "pre-commit"), "true")

	stdout, stderr, code := runSafegit(t, dir, "doctor", "--action", "diagnose")
	if code != 0 {
		t.Fatalf("an advisory finding must not change the exit code, got %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "[WARN] native_hooks") {
		t.Fatalf("doctor did not report the never-executed hooks:\n%s", stdout)
	}
	if !strings.Contains(stdout, "prepare-commit-msg") {
		t.Errorf("the finding must name the hook, got:\n%s", stdout)
	}
	// pre-commit IS run by safegit, so it must not be listed as unused.
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, "native_hooks") && strings.Contains(line, "never runs: pre-commit") {
			t.Errorf("a hook safegit does run was reported as never run: %s", line)
		}
	}
}

// TestDoctorExitCodeFollowsErrorFindings: an error-severity finding makes
// doctor exit nonzero, warnings alone do not, and after --action fix the code
// reflects what the fix LEFT rather than what it found.
func TestDoctorExitCodeFollowsErrorFindings(t *testing.T) {
	dir := newRepo(t)

	// A warning on its own: a non-executable hook in the tool-owned store.
	nonExec := filepath.Join(localHookDir(dir), "pre-pre-push")
	if err := os.MkdirAll(filepath.Dir(nonExec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nonExec, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, code := runSafegit(t, dir, "doctor", "--action", "diagnose")
	if code != 0 {
		t.Fatalf("warnings alone must exit 0, got %d:\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "[WARN] hook_perms") {
		t.Fatalf("the warning was not reported at all:\n%s", stdout)
	}

	// An error-severity finding: a leftover scrub-policy file, which holds the
	// very secrets a scrub was run to remove.
	policyPath := filepath.Join(dir, ".git", "safegit", "scrub-policies.jsonl")
	line := `{"type":"match","pattern":"DOCTOR_EXIT_SECRET","reason":"old scrub","created_at":"2026-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(policyPath, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, code = runSafegit(t, dir, "doctor", "--action", "diagnose")
	if code != 50 {
		t.Fatalf("an error-severity finding must exit 50, got %d:\n%s", code, stdout)
	}

	// The fix removes it, so the run that repaired the repository succeeds.
	stdout, stderr, code := runSafegit(t, dir, "doctor", "--action", "fix")
	if code != 0 {
		t.Fatalf("--action fix repaired the finding but still exited %d:\n%s\n%s", code, stdout, stderr)
	}
	if _, err := os.Stat(policyPath); !os.IsNotExist(err) {
		t.Errorf("--action fix left %s behind (err=%v)", policyPath, err)
	}
}

// TestDoctorExitsNonzeroWhileHooksAreUnmigrated: every push refuses while hooks
// sit in the pre-migration location, so doctor says so as an error rather than
// reporting a repository that cannot push as healthy.
func TestDoctorExitsNonzeroWhileHooksAreUnmigrated(t *testing.T) {
	dir := newRepo(t)
	writeHookScript(t, filepath.Join(legacyHookDir(dir), "pre-pre-push"), "true")

	stdout, _, code := runSafegit(t, dir, "doctor", "--action", "diagnose")
	if code != 50 {
		t.Fatalf("unmigrated hooks must exit 50, got %d:\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "[FAIL] hooks_migrated") || !strings.Contains(stdout, "hook migrate") {
		t.Errorf("the finding must name the remedy:\n%s", stdout)
	}

	if _, stderr, mCode := runSafegit(t, dir, "hook", "migrate"); mCode != 0 {
		t.Fatalf("hook migrate failed (%d): %s", mCode, stderr)
	}
	if stdout, _, code := runSafegit(t, dir, "doctor", "--action", "diagnose"); code != 0 {
		t.Fatalf("doctor still exits %d after the migration:\n%s", code, stdout)
	}
}

// TestCoreHooksPathRedirectsSafegitsOwnHookRuns: git resolves its hook
// directory through core.hooksPath, and safegit asks git rather than joining
// .git/hooks itself. Before that, a repository that redirected its hooks ran
// them under every git command and none under safegit -- silently, since a hook
// that is not there and a hook that is never looked for read the same.
func TestCoreHooksPathRedirectsSafegitsOwnHookRuns(t *testing.T) {
	dir := newRepo(t)

	hooksPath := filepath.Join(evalTempDir(t), "custom-hooks")
	marker := filepath.Join(dir, "redirected-pre-commit-ran.txt")
	writeHookScript(t, filepath.Join(hooksPath, "pre-commit"), "printf ran > "+marker)
	writeHookScript(t, filepath.Join(hooksPath, "prepare-commit-msg"), "true")

	cmd := exec.Command("git", "config", "core.hooksPath", hooksPath)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config core.hooksPath: %v\n%s", err, out)
	}

	testutil.WriteFile(t, dir, "a.txt", "a\n")
	safegitCommit(t, dir, "commit under a redirected hook directory", "a.txt")
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("safegit did not run the redirected pre-commit hook (marker absent: %v)", err)
	}

	// Doctor reads the same resolution, so its report is about the files git
	// would actually run.
	stdout, _, _ := runSafegit(t, dir, "doctor", "--action", "diagnose")
	if !strings.Contains(stdout, "prepare-commit-msg") || !strings.Contains(stdout, hooksPath) {
		t.Errorf("doctor reported hooks from the wrong directory:\n%s", stdout)
	}
}

// TestLinkedWorktreeRunsTheRepositorysHooks: a linked worktree's git dir is
// .git/worktrees/<name>, which has no hooks/ of its own -- git runs the common
// git dir's hooks there. Joining "hooks" onto the worktree's git dir found
// nothing at all, so every hook silently stopped running inside a worktree.
func TestLinkedWorktreeRunsTheRepositorysHooks(t *testing.T) {
	dir := newRepo(t)

	marker := filepath.Join(dir, "worktree-pre-commit-ran.txt")
	writeHookScript(t, filepath.Join(dir, ".git", "hooks", "pre-commit"), "printf ran > "+marker)

	wt := filepath.Join(evalTempDir(t), "linked")
	cmd := exec.Command("git", "worktree", "add", "-b", "side", wt)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}

	testutil.WriteFile(t, wt, "b.txt", "b\n")
	safegitCommit(t, wt, "commit inside a linked worktree", "b.txt")
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("safegit did not run the repository's pre-commit hook inside a linked worktree (marker absent: %v)", err)
	}
}
