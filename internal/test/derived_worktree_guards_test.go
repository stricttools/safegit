package test

import (
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// The worktree guard is DERIVED from internal/gitexec's classification table
// rather than re-scanned per handler. Two hand-written approximations of git's
// own vocabulary lived in coord_cmd.go and both were wrong:
//
//   - reset guarded only `--hard`, on a comment claiming it is the one mode
//     that mutates the working tree. `--merge` and `--keep` write working-tree
//     files too, and ran unguarded.
//   - bisect guarded a hand-kept subcommand list that omitted `skip`, `run` and
//     `replay`, every one of which moves HEAD and checks a commit out.
//
// These tests pin the guard from the operator's side: a dirty tree refuses the
// forms that write to it, and does not refuse the forms that do not.

// dirtyGuardRepo builds a repo with two commits and an untracked file, so the
// coordination check has uncommitted work to refuse over and `HEAD~1` exists.
func dirtyGuardRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "one\n")
	safegitCommit(t, dir, "second", "a.txt")
	// Refresh the index's stat cache, so `reset --merge` and `reset --keep` see
	// a tree whose tracked files are all up to date and proceed. Without it git
	// refuses them for its own reason and the guard's absence would be invisible.
	testutil.Git(t, dir, "status", "--porcelain")
	testutil.WriteFile(t, dir, "stray.txt", "uncommitted work\n")
	return dir
}

// TestResetWorktreeWritingModesRefuseOnADirtyTree: --merge and --keep write
// working-tree files exactly as --hard does, so the guard that refuses --hard
// over uncommitted work has to refuse them too.
func TestResetWorktreeWritingModesRefuseOnADirtyTree(t *testing.T) {
	for _, mode := range []string{"--merge", "--keep"} {
		t.Run(mode, func(t *testing.T) {
			dir := dirtyGuardRepo(t)
			tip := testutil.Rev(t, dir, "HEAD")

			stdout, stderr, code := runSafegit(t, dir, "reset", mode, "HEAD~1")
			if code != exitcode.CoordinationBusy {
				t.Fatalf("`safegit reset %s` on a dirty tree: exit %d, want %d (CoordinationBusy)\nstdout=%s\nstderr=%s",
					mode, code, exitcode.CoordinationBusy, stdout, stderr)
			}
			if got := testutil.Rev(t, dir, "HEAD"); got != tip {
				t.Errorf("HEAD moved to %s despite the refusal (was %s)", got, tip)
			}
		})
	}
}

// TestResetRefOnlyModesAreNotGuarded is the other end of the same rule: --soft
// and --mixed touch the ref and the index and never the working tree, so
// uncommitted work is no reason to refuse them. Without this the easy "guard
// every reset" fix would look correct.
func TestResetRefOnlyModesAreNotGuarded(t *testing.T) {
	for _, mode := range []string{"--soft", "--mixed"} {
		t.Run(mode, func(t *testing.T) {
			dir := dirtyGuardRepo(t)

			stdout, stderr, code := runSafegit(t, dir, "reset", mode, "HEAD~1")
			if code == exitcode.CoordinationBusy {
				t.Fatalf("`safegit reset %s` was refused over uncommitted work it cannot touch\nstdout=%s\nstderr=%s",
					mode, stdout, stderr)
			}
			if code != 0 {
				t.Fatalf("`safegit reset %s`: exit %d\nstdout=%s\nstderr=%s", mode, code, stdout, stderr)
			}
		})
	}
}

// TestBisectSteppingSubcommandRefusesOnADirtyTree: `skip` is one of bisect's
// stepping subcommands -- it checks another commit out -- and the hand-kept list
// it was missing from let it run over uncommitted work.
func TestBisectSteppingSubcommandRefusesOnADirtyTree(t *testing.T) {
	dir := dirtyGuardRepo(t)

	stdout, stderr, code := runSafegit(t, dir, "bisect", "skip")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("`safegit bisect skip` on a dirty tree: exit %d, want %d (CoordinationBusy)\nstdout=%s\nstderr=%s",
			code, exitcode.CoordinationBusy, stdout, stderr)
	}
}

// TestBisectReportingSubcommandIsNotGuarded is its control: `log` reports and
// checks nothing out, so it is not the guard's business. It fails for git's own
// reason (no bisect is in progress), which is a different verdict from the
// guard's.
func TestBisectReportingSubcommandIsNotGuarded(t *testing.T) {
	dir := dirtyGuardRepo(t)

	stdout, stderr, code := runSafegit(t, dir, "bisect", "log")
	if code == exitcode.CoordinationBusy {
		t.Fatalf("`safegit bisect log` was refused over uncommitted work it cannot touch\nstdout=%s\nstderr=%s",
			stdout, stderr)
	}
}
