package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// `safegit cherry-pick` is single-form and pipeline-authored.
//
// It used to hand the operator's whole command line to git, which authored
// whatever commits came out of it: no safegit trailers, no commit-msg handling,
// nothing `safegit undo` could reverse. The restructure splits the operation
// where git itself splits it -- `git cherry-pick --no-commit` COMPUTES the pick
// and stages its result, and the conclusion engine (the one behind `safegit
// cherry-pick-continue`) turns that staged result into a commit.
//
// One thing is safegit's own rather than git's, and it is the reason this
// restructure needed more than the revert's did: `git cherry-pick --no-commit`
// records NOTHING about the commit it is applying -- no CHERRY_PICK_HEAD, on
// either the clean or the conflicted path. The conclusion machinery, author
// preservation, the marker labels, `git status` and `git cherry-pick --abort`
// all key on that file, so safegit writes it itself after the compute step. The
// parked state is then exactly what a conflicted pick looks like to git.
//
// The command line is narrower than git's, under the subset law: exactly one
// commit, named as a commit and not as a range.

var pickSession = []string{"CLAUDE_CODE_SESSION_ID=cherry-pick-restructure-test"}

// pickAuthor is the identity of the commits these fixtures put on the side
// branch. It is somebody OTHER than whoever runs the test, which is what makes
// author preservation observable at all.
var pickAuthorEnv = append([]string{
	"GIT_AUTHOR_NAME=Original Author",
	"GIT_AUTHOR_EMAIL=original@example.com",
}, pickSession...)

// newPickableRepo builds a repository whose `side` branch carries two commits
// that touch files main never touches, so picking either of them is clean.
func newPickableRepo(t *testing.T) (dir, first, second string) {
	t.Helper()
	dir = newRepo(t)

	testutil.WriteFile(t, dir, "base.txt", "base\n")
	safegitCommitEnv(t, dir, pickSession, "base", "base.txt")

	testutil.Git(t, dir, "branch", "side")
	testutil.Git(t, dir, "switch", "side")
	testutil.WriteFile(t, dir, "one.txt", "one\n")
	first = safegitCommitEnv(t, dir, pickAuthorEnv, "the first side change", "one.txt")
	testutil.WriteFile(t, dir, "two.txt", "two\n")
	second = safegitCommitEnv(t, dir, pickAuthorEnv, "the second side change", "two.txt")

	testutil.Git(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "main.txt", "main\n")
	safegitCommitEnv(t, dir, pickSession, "the main change", "main.txt")
	return dir, first, second
}

// newConflictingPickRepo builds a repository where picking the side commit
// collides with main's own edit of the same line.
func newConflictingPickRepo(t *testing.T) (dir, source string) {
	t.Helper()
	dir = newRepo(t)

	testutil.WriteFile(t, dir, "c.txt", "l1\nbase\nl3\n")
	safegitCommitEnv(t, dir, pickSession, "base", "c.txt")

	testutil.Git(t, dir, "branch", "side")
	testutil.Git(t, dir, "switch", "side")
	testutil.WriteFile(t, dir, "c.txt", "l1\nside\nl3\n")
	source = safegitCommitEnv(t, dir, pickAuthorEnv, "the side change", "c.txt")

	testutil.Git(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "c.txt", "l1\nmain\nl3\n")
	safegitCommitEnv(t, dir, pickSession, "the main change", "c.txt")
	return dir, source
}

// TestCleanCherryPickIsPipelineAuthored is the headline: a clean pick produces
// ONE safegit commit that preserves the picked commit's author, records the
// operator as committer, carries safegit's trailers, leaves no operation state
// behind, writes exactly one oplog entry under the command's own op name, and
// is reversible with `safegit undo`.
func TestCleanCherryPickIsPipelineAuthored(t *testing.T) {
	dir, first, _ := newPickableRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, pickSession, "cherry-pick", first)
	if code != exitcode.OK {
		t.Fatalf("a clean cherry-pick failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	head := testutil.Rev(t, dir, "HEAD")
	if parents := testutil.Parents(t, dir, head); len(parents) != 1 || parents[0] != tip {
		t.Fatalf("the picked commit's parents are %v, want [%s]", parents, tip)
	}

	// Authored by the pipeline, which is what the trailer says.
	msg := commitMessageOf(t, dir, "HEAD")
	if !strings.Contains(msg, "Claude-Code-Session-Id: cherry-pick-restructure-test") {
		t.Errorf("the picked commit carries no session trailer, so git authored it:\n%s", msg)
	}
	if !strings.Contains(msg, "the first side change") {
		t.Errorf("the picked commit does not carry the source commit's message:\n%s", msg)
	}

	// The AUTHOR travels with the change; the COMMITTER is whoever ran the
	// command. That division is git's own and the restructure keeps it.
	identity := strings.TrimSpace(testutil.GitOut(t, dir, "log", "-1", "--format=%an <%ae>|%cn <%ce>"))
	author, committer, _ := strings.Cut(identity, "|")
	if !strings.Contains(author, "original@example.com") {
		t.Errorf("author = %q, want the picked commit's own identity", author)
	}
	if strings.Contains(committer, "original@example.com") {
		t.Errorf("committer = %q, want the operator's identity", committer)
	}

	// The change actually arrived.
	if got := testutil.MustShow(t, dir, "HEAD", "one.txt"); got != "one\n" {
		t.Errorf("HEAD:one.txt = %q, want the picked content", got)
	}

	// One entry, under the COMMAND's own op name -- not the conclusion
	// command's, and not two of them.
	entries := oplogEntries(t, dir, "cherry-pick")
	if len(entries) != 1 {
		t.Fatalf("expected exactly one cherry-pick oplog entry, got %d: %v", len(entries), entries)
	}
	extra := oplogExtra(entries[0])
	if got, _ := oplogExtraString(extra, "ref"); got != "refs/heads/main" {
		t.Errorf("the cherry-pick entry records ref %q, want refs/heads/main; extra=%v", got, extra)
	}
	if got, _ := oplogExtraString(extra, "parent"); got != tip {
		t.Errorf("the cherry-pick entry records old tip %q, want %q; extra=%v", got, tip, extra)
	}
	if got, _ := oplogExtraString(extra, "sha", "to", "result"); got != head {
		t.Errorf("the cherry-pick entry records new tip %q, want %q; extra=%v", got, head, extra)
	}
	if n := len(oplogEntries(t, dir, "cherry-pick-continue")); n != 0 {
		t.Errorf("the pick recorded %d cherry-pick-continue entries; the command records under its OWN name", n)
	}

	assertNoSequencerResidue(t, dir, "pipeline-authored cherry-pick")

	// And the whole thing is reversible, which is what authorship buys.
	if _, stderr, code := runSafegitEnv(t, dir, pickSession, "undo"); code != 0 {
		t.Fatalf("undo of a pipeline-authored cherry-pick failed (code %d): %s", code, stderr)
	}
	if back := testutil.Rev(t, dir, "HEAD"); back != tip {
		t.Errorf("undo left HEAD at %s, want the pre-pick tip %s", back, tip)
	}
}

// TestCherryPickRefusesRawGitShapes pins the subset boundary: every command
// line safegit's cherry-pick does not implement is refused before anything
// runs, each naming the capability that is absent.
//
// The range rows are the ones that need saying out loud: `git cherry-pick A..B`
// puts git's SEQUENCER in charge even when the range holds a single commit, so
// the refusal is on the rev-set OPERATORS rather than on how many arguments
// were typed.
func TestCherryPickRefusesRawGitShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		// args is built from the fixture's two side commits.
		args func(first, second string) []string
		// says is a fragment the refusal must carry.
		says string
	}{
		{"two commits", func(f, s string) []string { return []string{"cherry-pick", f, s} }, "one commit"},
		{"two-dot range", func(f, s string) []string { return []string{"cherry-pick", "main..side"} }, "range"},
		{"three-dot range", func(f, s string) []string { return []string{"cherry-pick", "main...side"} }, "range"},
		{"exclusion", func(f, s string) []string { return []string{"cherry-pick", "side", "^main"} }, "range"},
		{"skip", func(f, s string) []string { return []string{"cherry-pick", "--skip"} }, "--skip"},
		{"edit", func(f, s string) []string { return []string{"cherry-pick", "--edit", f} }, "--edit"},
		{"gpg sign", func(f, s string) []string { return []string{"cherry-pick", "-S", f} }, "sign"},
		{"fast-forward", func(f, s string) []string { return []string{"cherry-pick", "--ff", f} }, "--ff"},
		{"commit override", func(f, s string) []string { return []string{"cherry-pick", "--commit", f} }, "--commit"},
		{"allow empty", func(f, s string) []string { return []string{"cherry-pick", "--allow-empty", f} }, "--allow-empty"},
		{"cleanup", func(f, s string) []string { return []string{"cherry-pick", "--cleanup", "verbatim", f} }, "--cleanup"},
		{"no commit named", func(f, s string) []string { return []string{"cherry-pick"} }, "names no commit"},
		{"pathspec", func(f, s string) []string { return []string{"cherry-pick", f, "--", "one.txt"} }, "pathspec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, first, second := newPickableRepo(t)
			tip := testutil.Rev(t, dir, "HEAD")

			args := tc.args(first, second)
			stdout, stderr, code := runSafegitEnv(t, dir, pickSession, args...)
			if code != exitcode.Usage {
				t.Fatalf("safegit %s exited %d, want %d (Usage)\nstdout=%s\nstderr=%s",
					strings.Join(args, " "), code, exitcode.Usage, stdout, stderr)
			}
			if !strings.Contains(stderr, tc.says) {
				t.Errorf("the refusal does not say %q:\n%s", tc.says, stderr)
			}
			if head := testutil.Rev(t, dir, "HEAD"); head != tip {
				t.Errorf("the refused cherry-pick moved HEAD to %s (was %s)", head, tip)
			}
			assertNoSequencerResidue(t, dir, "refused cherry-pick")
			if n := len(oplogEntries(t, dir, "cherry-pick")); n != 0 {
				t.Errorf("a refused command line recorded %d oplog entries; nothing happened to the repository", n)
			}
		})
	}
}

// TestConflictedCherryPickParksWithCherryPickHead: a pick git cannot apply
// cleanly parks in exactly the state git's own conflicted pick leaves -- the
// unmerged index, git's narration, and CHERRY_PICK_HEAD naming the commit being
// applied -- and `safegit cherry-pick-continue` concludes it.
func TestConflictedCherryPickParksWithCherryPickHead(t *testing.T) {
	dir, source := newConflictingPickRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, pickSession, "cherry-pick", source)
	if code == 0 {
		t.Fatalf("the fixture needs a conflict\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "CONFLICT") {
		t.Errorf("git's conflict narration reached neither stream\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the conflicted pick moved HEAD to %s (was %s)", head, tip)
	}

	// The park file: safegit's own write, in git's format, naming the commit
	// being applied.
	parked, err := os.ReadFile(filepath.Join(dir, ".git", "CHERRY_PICK_HEAD"))
	if err != nil {
		t.Fatalf("the conflicted pick left no CHERRY_PICK_HEAD: %v", err)
	}
	if got := strings.TrimSpace(string(parked)); got != source {
		t.Errorf("CHERRY_PICK_HEAD names %q, want the picked commit %s", got, source)
	}
	// Which is what makes git itself report the pick.
	if status := testutil.Git(t, dir, "status"); !strings.Contains(status, "cherry-pick") {
		t.Errorf("git status does not report the parked cherry-pick:\n%s", status)
	}

	// The way out is safegit's own conclusion, and it works.
	testutil.WriteFile(t, dir, "c.txt", "l1\nresolved\nl3\n")
	if _, stderr, code := runSafegitEnv(t, dir, pickSession, "cherry-pick-continue",
		"--resolve", "c.txt=worktree"); code != 0 {
		t.Fatalf("cherry-pick-continue could not conclude the parked pick (code %d): %s", code, stderr)
	}
	if got := testutil.MustShow(t, dir, "HEAD", "c.txt"); got != "l1\nresolved\nl3\n" {
		t.Errorf("HEAD:c.txt = %q, want the resolved content", got)
	}
	assertNoSequencerResidue(t, dir, "concluded cherry-pick")
}

// TestCherryPickAbortStaysAStateControlForm: `--abort` authors nothing, so it
// is not one of the subset refusals -- it reaches the guarded passthrough,
// where the coordination check answers it exactly as it did before the
// restructure and names git's own abort.
//
// And that abort really does clear the state SAFEGIT parked, which it can only
// do because the park file safegit wrote is the file git writes itself. That
// second half is the one the restructure could have broken.
func TestCherryPickAbortStaysAStateControlForm(t *testing.T) {
	dir, source := newConflictingPickRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	if _, stderr, code := runSafegitEnv(t, dir, pickSession, "cherry-pick", source); code == 0 {
		t.Fatalf("the fixture needs a conflict: %s", stderr)
	}

	_, stderr, _ := runSafegitEnv(t, dir, pickSession, "cherry-pick", "--abort")
	if strings.Contains(stderr, "does not support") {
		t.Errorf("--abort was refused by the subset table; it authors nothing and stays a state-control form:\n%s", stderr)
	}
	if !strings.Contains(stderr, "git cherry-pick --abort") {
		t.Errorf("the coordination refusal does not name git's own abort:\n%s", stderr)
	}

	if out, code := testutil.GitTry(t, dir, "cherry-pick", "--abort"); code != 0 {
		t.Fatalf("git cherry-pick --abort could not clear the state safegit parked (code %d): %s", code, out)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the abort left HEAD at %s, want %s", head, tip)
	}
	assertNoSequencerResidue(t, dir, "aborted cherry-pick")
	if status := strings.TrimSpace(testutil.Git(t, dir, "status", "--porcelain")); status != "" {
		t.Errorf("the abort left the working tree dirty:\n%s", status)
	}
}

// TestCherryPickNoCommitStaysAPassthrough: `--no-commit` authors nothing, so it
// is forwarded to git unchanged -- staged result, no commit, and none of
// safegit's park-file handling, because safegit is not concluding anything.
func TestCherryPickNoCommitStaysAPassthrough(t *testing.T) {
	dir, first, _ := newPickableRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	if _, stderr, code := runSafegitEnv(t, dir, pickSession, "cherry-pick", "--no-commit", first); code != 0 {
		t.Fatalf("cherry-pick --no-commit failed (code %d): %s", code, stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Fatalf("--no-commit committed anyway: HEAD moved to %s (was %s)", head, tip)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); !strings.Contains(status, "one.txt") {
		t.Errorf("--no-commit staged nothing:\n%s", status)
	}
}

// TestCherryPickOfAnAlreadyAppliedCommitIsRefused: a pick whose change the
// branch already carries produces nothing to commit. safegit refuses rather
// than making an empty commit, and says so in those words.
//
// It leaves no state behind to name a way out of: the compute step's park is
// safegit's own and is cleaned up on this path. That half is pinned in
// inflight_compute_refusal_test.go, which owns the cleaned-up assertions; what
// is pinned here is the refusal itself.
func TestCherryPickOfAnAlreadyAppliedCommitIsRefused(t *testing.T) {
	dir, first, _ := newPickableRepo(t)

	if _, stderr, code := runSafegitEnv(t, dir, pickSession, "cherry-pick", first); code != 0 {
		t.Fatalf("the first pick failed (code %d): %s", code, stderr)
	}
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, pickSession, "cherry-pick", first)
	if code == 0 {
		t.Fatalf("picking an already-applied commit succeeded\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "no change") {
		t.Errorf("the refusal does not say the pick produces no change:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the refused pick moved HEAD to %s (was %s)", head, tip)
	}
}

// TestCherryPickNamesItsLeftoversWhenTheStateChangedUnderIt: something else
// started a git operation in this worktree while safegit's own compute step was
// running, so the state safegit reads back is not the pick it just computed.
//
// Nothing is committed and nothing is rolled back -- an out-of-band writer is
// demonstrably active here, and safegit refuses to ship work it cannot account
// for rather than destroying it. What it owes instead is an inventory: the state
// files left behind, safegit's OWN CHERRY_PICK_HEAD among them (the compute step
// is `--no-commit`, which writes no such file, so that one is safegit's), and
// the paths the compute staged.
//
// The reproduction device is a `post-index-change` hook: git runs it after the
// compute step writes the index, and this one writes a MERGE_HEAD, which is
// exactly the shape of another operation appearing mid-run. MERGE_HEAD outranks
// CHERRY_PICK_HEAD in the state reader, so the state comes back as a merge.
//
// The hook is CONDITIONAL on the picked file being in the index, and it has to
// be: earlier in the same run the coordination check refreshes a shared index
// safegit's own commits left stale, which writes it and fires the hook too. A
// MERGE_HEAD appearing THERE is refused at the entry check instead -- correctly,
// and at a different exit code -- so an unconditional hook would reproduce the
// wrong situation.
func TestCherryPickNamesItsLeftoversWhenTheStateChangedUnderIt(t *testing.T) {
	dir, first, _ := newPickableRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	mergeHead := filepath.Join(dir, ".git", "MERGE_HEAD")
	installHook(t, dir, "post-index-change",
		"#!/bin/sh\n"+
			"git ls-files --error-unmatch one.txt >/dev/null 2>&1 || exit 0\n"+
			"printf '%s\\n' '"+tip+"' > '"+mergeHead+"'\nexit 0\n")

	stdout, stderr, code := runSafegitEnv(t, dir, pickSession, "cherry-pick", first)
	if code != exitcode.General {
		t.Fatalf("the pick whose state changed under it exited %d, want %d\nstdout=%s\nstderr=%s",
			code, exitcode.General, stdout, stderr)
	}
	if !strings.Contains(stderr, "CHERRY_PICK_HEAD") {
		t.Errorf("the message does not name safegit's own park file among the leftovers:\n%s", stderr)
	}
	if !strings.Contains(stderr, "one.txt") {
		t.Errorf("the message does not report the staged result:\n%s", stderr)
	}
	if !strings.Contains(stderr, "git status") {
		t.Errorf("the message does not point at git status:\n%s", stderr)
	}

	// Nothing rolled back: HEAD stands, and the park file still holds the commit
	// that was being picked.
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the refused pick moved HEAD to %s (was %s)", head, tip)
	}
	parked, err := os.ReadFile(filepath.Join(dir, ".git", "CHERRY_PICK_HEAD"))
	if err != nil {
		t.Fatalf("safegit's own park file was removed: %v", err)
	}
	if got := strings.TrimSpace(string(parked)); got != first {
		t.Errorf("CHERRY_PICK_HEAD holds %s, want the picked commit %s", got, first)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); !strings.Contains(status, "one.txt") {
		t.Errorf("the staged result was discarded:\n%s", status)
	}
}
