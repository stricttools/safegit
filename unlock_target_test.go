package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// unlockGrammarRepo builds a repository plus a LINKED WORKTREE and returns the
// worktree's git directory, the shared (main) safegit directory and the
// worktree-local one.
//
// The linked worktree is what makes the two lock trees distinguishable at all:
// in an ordinary repository the shared and worktree-local safegit directories
// are the same path, so a test there cannot tell "this lock lives with the
// repository" from "this lock lives with the worktree".
func unlockGrammarRepo(t *testing.T) (wtGitDir, sharedSafegit, localSafegit string) {
	t.Helper()

	base := t.TempDir()
	// Temporary directories can sit behind a symlink; git reports resolved
	// paths, so the expectations must be resolved too.
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	main := filepath.Join(base, "main")
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatalf("creating %s: %v", main, err)
	}
	run(main, "init", "--initial-branch=main")
	run(main, "config", "user.name", "Test")
	run(main, "config", "user.email", "test@test.com")
	run(main, "commit", "--allow-empty", "-m", "initial")

	wt := filepath.Join(base, "wt")
	run(main, "worktree", "add", wt, "-b", "side")

	wtGitDir = run(wt, "rev-parse", "--absolute-git-dir")
	if !strings.Contains(wtGitDir, "worktrees") {
		t.Fatalf("fixture: %s is not a linked worktree git dir", wtGitDir)
	}
	return wtGitDir, filepath.Join(main, ".git", "safegit"), filepath.Join(wtGitDir, "safegit")
}

// The unlock naming grammar has three arms, and each one exists because the
// alternative was wrong in a specific way:
//
//   - "safegit/..." is a tool-owned pseudo-ref looked up in a table, because
//     before the table any argument without a refs/ prefix was read as a branch
//     and `safegit unlock safegit/rewrite` went looking for a lock on the
//     branch refs/heads/safegit/rewrite -- the one lock an operator needs most
//     after a crashed rewrite was unreachable.
//   - "refs/..." is taken as written, so a full ref name is not prefixed again.
//   - anything else is a branch shorthand.
//
// Which lock TREE each target resolves to is the other half of the grammar: a
// rewrite changes object names for every worktree and locks in the shared
// directory, an operation locks only the worktree it runs in, and a ref lock is
// shared because every worktree of the repository contends on the same ref.
func TestResolveLockTargetGrammar(t *testing.T) {
	wtGitDir, sharedSafegit, localSafegit := unlockGrammarRepo(t)
	flags := globalFlags{}

	for _, tc := range []struct {
		arg         string
		wantName    string
		wantBase    string
		wantDisplay string
	}{
		{"safegit/rewrite", "safegit/rewrite", sharedSafegit, "safegit/rewrite"},
		{"safegit/operation", "safegit/operation", localSafegit, "safegit/operation"},
		{"refs/heads/side", "refs/heads/side", sharedSafegit, "side"},
		{"refs/notes/commits", "refs/notes/commits", sharedSafegit, "refs/notes/commits"},
		{"side", "refs/heads/side", sharedSafegit, "side"},
		{"feature/x", "refs/heads/feature/x", sharedSafegit, "feature/x"},
	} {
		t.Run(tc.arg, func(t *testing.T) {
			got, err := resolveLockTarget(flags, wtGitDir, tc.arg)
			if err != nil {
				t.Fatalf("resolveLockTarget(%q): %v", tc.arg, err)
			}
			if got.name != tc.wantName {
				t.Errorf("name = %q, want %q", got.name, tc.wantName)
			}
			if got.base != tc.wantBase {
				t.Errorf("base = %q, want %q", got.base, tc.wantBase)
			}
			if got.display != tc.wantDisplay {
				t.Errorf("display = %q, want %q", got.display, tc.wantDisplay)
			}
		})
	}
}

// An unknown safegit/... name is an error that lists the known ones, never a
// silent reinterpretation as a branch: the reinterpretation is what made the
// rewrite lock unreachable, and it reported "no lock held" instead of saying
// the name was wrong.
func TestResolveLockTargetRefusesUnknownPseudoRef(t *testing.T) {
	wtGitDir, _, _ := unlockGrammarRepo(t)

	got, err := resolveLockTarget(globalFlags{}, wtGitDir, "safegit/nonesuch")
	if err == nil {
		t.Fatalf("an unknown tool-owned lock name must be refused, got target %+v", got)
	}
	for _, want := range []string{"safegit/nonesuch", "safegit/rewrite", "safegit/operation"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q so the operator can correct it, got: %v", want, err)
		}
	}
}
