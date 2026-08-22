package test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// `safegit revert` of a SINGLE commit, and the passthrough refusals around it.
//
// A plain passthrough revert left two things behind: a commit git authored,
// with none of safegit's trailers, and git's own operation state (REVERT_HEAD,
// AUTO_MERGE, MERGE_MSG), which the commit pipeline reads as an operation in
// flight -- so the NEXT `safegit commit` was refused until somebody deleted
// those files by hand.
//
// The restructured form splits the operation where git splits it: `git revert
// --no-commit` computes the inverse patch and stages it, and the conclusion
// engine commits it. The tests below assert both halves, plus the cases that
// deliberately stay plain passthroughs.

// revertSession is the handshake these tests spawn safegit with, so the session
// trailer is observable at all.
var revertSession = []string{"CLAUDE_CODE_SESSION_ID=revert-restructure-test"}

// newRevertRepo builds a repository with a commit worth reverting, authored by
// somebody else so that author preservation is observable.
func newRevertRepo(t *testing.T) (dir, target string) {
	t.Helper()
	dir = newRepo(t)

	otherEnv := append([]string{
		"GIT_AUTHOR_NAME=Original Author",
		"GIT_AUTHOR_EMAIL=original@example.com",
	}, revertSession...)

	testutil.WriteFile(t, dir, "r.txt", "before\n")
	safegitCommitEnv(t, dir, revertSession, "base", "r.txt")
	testutil.WriteFile(t, dir, "r.txt", "the change to undo\n")
	target = safegitCommitEnv(t, dir, otherEnv, "the change to undo", "r.txt")
	return dir, target
}

// TestSingleCommitRevertIsPipelineAuthored is the headline of the restructure:
// safegit writes the commit, so it carries safegit's trailers; git's state
// files are gone; and the next safegit commit is not refused.
func TestSingleCommitRevertIsPipelineAuthored(t *testing.T) {
	dir, target := newRevertRepo(t)

	stdout, stderr, code := runSafegitEnv(t, dir, revertSession, "revert", "--no-edit", target)
	if code != exitcode.OK {
		t.Fatalf("safegit revert failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	// safegit's own trailer: the commit went through the pipeline, not through
	// git's commit path.
	msg := commitMessage(t, dir, "HEAD")
	if !strings.Contains(msg, "Claude-Code-Session-Id: revert-restructure-test") {
		t.Errorf("the revert commit carries no safegit trailer, so git authored it:\n%s", msg)
	}
	// git's own draft is still the message.
	if !strings.Contains(msg, `Revert "the change to undo"`) {
		t.Errorf("the revert commit does not carry git's message draft:\n%s", msg)
	}
	// The author is preserved from the commit being reverted; the committer is
	// whoever ran the command.
	if author := testutil.GitOut(t, dir, "log", "-1", "--format=%an <%ae>"); !strings.Contains(author, "original@example.com") {
		t.Errorf("author = %q, want the identity of the commit being reverted", author)
	}
	// And the revert actually reverted.
	if got := testutil.MustShow(t, dir, "HEAD", "r.txt"); got != "before\n" {
		t.Errorf("HEAD:r.txt = %q, want the content from before the reverted commit", got)
	}

	assertNoSequencerResidue(t, dir, "a single-commit safegit revert")

	// The practical consequence, and the reason the residue matters: the next
	// commit is not refused.
	testutil.WriteFile(t, dir, "after.txt", "after\n")
	if _, stderr, code := runSafegitEnv(t, dir, revertSession,
		"commit", "-m", "after the revert", "--", "after.txt"); code != 0 {
		t.Fatalf("a commit after the revert was refused (code %d): %s", code, stderr)
	}
}

// TestConflictedSingleRevertComposesWithRevertContinue: the compute step is
// allowed to fail. When it does, the operator gets the ordinary conflicted
// revert -- git's own state, git's own exit code -- and revert-continue
// concludes it through the same engine the clean path used.
func TestConflictedSingleRevertComposesWithRevertContinue(t *testing.T) {
	dir, target := newRevertRepo(t)
	// A later edit over the same line is what makes the inverse patch conflict.
	testutil.WriteFile(t, dir, "r.txt", "a later edit\n")
	safegitCommitEnv(t, dir, revertSession, "a later edit", "r.txt")
	tip := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegitEnv(t, dir, revertSession, "revert", "--no-edit", target)
	if code == 0 {
		t.Fatalf("the fixture needs a conflicted revert: %s", stderr)
	}
	if !strings.Contains(stderr, "safegit revert-continue") {
		t.Errorf("the conflicted revert does not name the command that concludes it:\n%s", stderr)
	}
	if !testutil.FileExists(filepath.Join(dir, ".git", "REVERT_HEAD")) {
		t.Fatal("the conflicted revert left no REVERT_HEAD: the state is not git's ordinary one")
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("HEAD moved despite the conflict")
	}

	stdout, stderr, code := runSafegitEnv(t, dir, revertSession,
		"revert-continue", "--resolve", "r.txt=theirs")
	if code != exitcode.OK {
		t.Fatalf("revert-continue of the conflicted revert failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if got := testutil.MustShow(t, dir, "HEAD", "r.txt"); got != "before\n" {
		t.Errorf("HEAD:r.txt = %q, want the reverted content", got)
	}
	assertNoSequencerResidue(t, dir, "revert-continue after a conflicted safegit revert")
}

// TestMultiCommitRevertStaysASequencerPassthrough: more than one commit is a
// queue, which safegit does not restructure -- git's sequencer runs it, and a
// mid-queue conflict is concluded by revert-continue's delegation.
func TestMultiCommitRevertStaysASequencerPassthrough(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "a1\n")
	safegitCommitEnv(t, dir, revertSession, "a1", "a.txt")
	first := testutil.Rev(t, dir, "HEAD")
	testutil.WriteFile(t, dir, "b.txt", "b1\n")
	safegitCommitEnv(t, dir, revertSession, "b1", "b.txt")
	second := testutil.Rev(t, dir, "HEAD")

	if _, stderr, code := runSafegitEnv(t, dir, revertSession, "revert", "--no-edit", second, first); code != 0 {
		t.Fatalf("a clean two-commit revert failed (code %d): %s", code, stderr)
	}
	// git ran the queue: two revert commits on top of the fixture.
	subjects := testutil.GitOut(t, dir, "log", "--format=%s", "-2")
	for _, want := range []string{`Revert "a1"`, `Revert "b1"`} {
		if !strings.Contains(subjects, want) {
			t.Errorf("the queue did not run; log holds:\n%s", subjects)
		}
	}
	// A queue's commits are git's, so they carry no safegit trailer. Stated as
	// an assertion so the difference between the two forms stays deliberate.
	if msg := commitMessage(t, dir, "HEAD"); strings.Contains(msg, "Claude-Code-Session-Id") {
		t.Errorf("a queued revert's commit carries a safegit trailer; only the single-commit form is pipeline-authored:\n%s", msg)
	}
}

// TestRevertFormsThatStayPassthroughs: the restructure applies only where
// safegit can honor every flag. Each case below must behave exactly as it did
// before the restructure existed, which for --no-commit means the staged result
// and the state files are still there for the operator to use.
func TestRevertFormsThatStayPassthroughs(t *testing.T) {
	t.Run("--no-commit", func(t *testing.T) {
		dir, target := newRevertRepo(t)
		tip := testutil.Rev(t, dir, "HEAD")

		if _, stderr, code := runSafegitEnv(t, dir, revertSession,
			"revert", "--no-commit", "--no-edit", target); code != 0 {
			t.Fatalf("revert --no-commit failed (code %d): %s", code, stderr)
		}
		if head := testutil.Rev(t, dir, "HEAD"); head != tip {
			t.Error("--no-commit committed anyway: the restructure must not claim this form")
		}
		if !testutil.FileExists(filepath.Join(dir, ".git", "REVERT_HEAD")) {
			t.Error("--no-commit left no REVERT_HEAD, so its staged result cannot be concluded")
		}
		if status := testutil.Git(t, dir, "status", "--porcelain"); !strings.Contains(status, "r.txt") {
			t.Errorf("--no-commit staged nothing:\n%s", status)
		}
	})

	t.Run("--edit", func(t *testing.T) {
		dir, target := newRevertRepo(t)
		// GIT_EDITOR=true accepts the draft without a terminal, which is what
		// an --edit revert needs; the point of the case is that git's own
		// commit path ran, not that an editor appeared.
		env := append([]string{"GIT_EDITOR=true"}, revertSession...)
		if _, stderr, code := runSafegitEnv(t, dir, env, "revert", "--edit", target); code != 0 {
			t.Fatalf("revert --edit failed (code %d): %s", code, stderr)
		}
		if msg := commitMessage(t, dir, "HEAD"); strings.Contains(msg, "Claude-Code-Session-Id") {
			t.Errorf("--edit was restructured; safegit's conclusion has no editor to honor it:\n%s", msg)
		}
	})
}

// TestMergeContinuePassthroughNamesMergeContinue: `safegit merge --continue`
// is refused and names safegit's own command.
//
// Both halves are asserted because they are refused by different code. A
// repository mid-conflict is DIRTY, so the working-tree guard refuses it; a
// merge whose result equals the current tip is CLEAN, and only the
// operation-specific refusal catches that one. Whether a conclusion is
// safegit's or git's must not depend on whether the merge changed a file.
func TestMergeContinuePassthroughNamesMergeContinue(t *testing.T) {
	// A merge fixture whose conflict resolution can be made either different
	// from the tip or identical to it.
	fixture := func(t *testing.T) string {
		t.Helper()
		dir := newRepo(t)
		testutil.WriteFile(t, dir, "c.txt", "l1\nbase\nl3\n")
		safegitCommitEnv(t, dir, revertSession, "base", "c.txt")
		testutil.Git(t, dir, "branch", "side")
		testutil.WriteFile(t, dir, "c.txt", "l1\nmain\nl3\n")
		safegitCommitEnv(t, dir, revertSession, "main", "c.txt")
		testutil.Git(t, dir, "switch", "side")
		testutil.WriteFile(t, dir, "c.txt", "l1\nside\nl3\n")
		safegitCommitEnv(t, dir, revertSession, "side", "c.txt")
		testutil.Git(t, dir, "switch", "main")
		if _, stderr, code := runSafegitEnv(t, dir, revertSession, "merge", "side"); code == 0 {
			t.Fatalf("the fixture needs a conflicted merge: %s", stderr)
		}
		return dir
	}

	// GIT_EDITOR is set on purpose. Without it git's own `merge --continue`
	// fails on the test environment's dumb terminal, which would make this test
	// pass whether or not safegit refused anything -- the failure it asserts
	// has to be safegit's refusal, not git's missing editor.
	env := append([]string{"GIT_EDITOR=true"}, revertSession...)

	t.Run("with the conflict unresolved", func(t *testing.T) {
		dir := fixture(t)
		_, stderr, code := runSafegitEnv(t, dir, env, "merge", "--continue")
		if code != exitcode.CoordinationBusy {
			t.Fatalf("exit %d, want %d (CoordinationBusy): %s", code, exitcode.CoordinationBusy, stderr)
		}
		if !strings.Contains(stderr, "safegit merge-continue") {
			t.Errorf("the refusal does not name safegit merge-continue:\n%s", stderr)
		}
	})

	t.Run("with the merge resolved to the current tip", func(t *testing.T) {
		dir := fixture(t)
		tip := testutil.Rev(t, dir, "HEAD")
		// Resolving every path to the branch's own content leaves nothing for
		// `git diff HEAD` to report, so the dirty-tree guard sees a clean tree.
		testutil.WriteFile(t, dir, "c.txt", "l1\nmain\nl3\n")
		testutil.Git(t, dir, "update-index", "--add", "c.txt")
		if diff := testutil.Git(t, dir, "diff", "HEAD", "--name-status"); strings.TrimSpace(diff) != "" {
			t.Fatalf("the fixture must present a clean tree to the guard:\n%s", diff)
		}

		_, stderr, code := runSafegitEnv(t, dir, env, "merge", "--continue")
		if code == 0 {
			t.Fatalf("git authored the merge commit: safegit must own its own conclusions:\n%s", stderr)
		}
		// The operation-specific refusal, not the working-tree guard's: this
		// tree is clean, so only the former can have produced it.
		if !strings.Contains(stderr, "does not conclude") {
			t.Errorf("the refusal is not the operation-specific one:\n%s", stderr)
		}
		if !strings.Contains(stderr, "safegit merge-continue") {
			t.Errorf("the refusal does not name safegit merge-continue:\n%s", stderr)
		}
		if head := testutil.Rev(t, dir, "HEAD"); head != tip {
			t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, tip)
		}
		if !testutil.FileExists(filepath.Join(dir, ".git", "MERGE_HEAD")) {
			t.Error("the refusal destroyed the merge it declined to conclude")
		}

		// And safegit's own command does conclude it: the path was resolved in
		// the index already, so there is nothing left to declare, and an empty
		// merge needs no flag. The way out the refusal names actually works
		// from here.
		if _, stderr, code := runSafegitEnv(t, dir, revertSession, "merge-continue"); code != 0 {
			t.Fatalf("the command the refusal named failed (code %d): %s", code, stderr)
		}
		if parents := testutil.Parents(t, dir, testutil.Rev(t, dir, "HEAD")); len(parents) != 2 {
			t.Errorf("the conclusion produced %d parent(s), want a merge commit's two", len(parents))
		}
	})
}

// TestPickAndRevertContinuePassthroughsNameSafegitsCommand: the same refusal
// for the other two operations safegit concludes, including a QUEUED sequence,
// which safegit now concludes too (by delegation).
func TestPickAndRevertContinuePassthroughsNameSafegitsCommand(t *testing.T) {
	for _, verb := range []string{"cherry-pick", "revert"} {
		t.Run(verb, func(t *testing.T) {
			fx := newQueuedPickRepo(t, verb)
			_, stderr, code := runSafegitEnv(t, fx.dir, revertSession, verb, "--continue")
			if code != exitcode.CoordinationBusy {
				t.Fatalf("exit %d, want %d (CoordinationBusy): %s", code, exitcode.CoordinationBusy, stderr)
			}
			// The operation-specific refusal fires before the working-tree
			// guard, so its own wording is what an operator sees even though a
			// mid-conflict tree is dirty too.
			if !strings.Contains(stderr, "does not conclude") {
				t.Errorf("the refusal is not the operation-specific one:\n%s", stderr)
			}
			if !strings.Contains(stderr, "safegit "+verb+"-continue") {
				t.Errorf("the refusal does not name safegit %s-continue:\n%s", verb, stderr)
			}
		})
	}
}

// TestRebaseContinuePassthroughStillNamesGit is the control for the refusal
// above: the new refusal is keyed on WHO OWNS the conclusion, read from the
// single way-out authority, not on the word "--continue". safegit has no rebase
// conclusion, so nothing here may ever claim one.
//
// What a mid-rebase `safegit rebase --continue` gets today is the working-tree
// guard's refusal, which names `git rebase --continue` -- pre-existing
// behavior, and the reason this test asserts the message rather than success:
// the rebase conclusion is a declared non-goal of this campaign, and the only
// property being pinned is that safegit does not start claiming it.
func TestRebaseContinuePassthroughStillNamesGit(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "c.txt", "l1\nbase\nl3\n")
	safegitCommitEnv(t, dir, revertSession, "base", "c.txt")
	testutil.Git(t, dir, "branch", "topic")
	testutil.WriteFile(t, dir, "c.txt", "l1\nmain\nl3\n")
	safegitCommitEnv(t, dir, revertSession, "main", "c.txt")
	testutil.Git(t, dir, "switch", "topic")
	testutil.WriteFile(t, dir, "c.txt", "l1\ntopic\nl3\n")
	safegitCommitEnv(t, dir, revertSession, "topic", "c.txt")

	if _, stderr, code := runSafegitEnv(t, dir, revertSession, "rebase", "main"); code == 0 {
		t.Fatalf("the fixture needs a conflicted rebase: %s", stderr)
	}
	testutil.WriteFile(t, dir, "c.txt", "l1\nresolved\nl3\n")
	testutil.Git(t, dir, "update-index", "--add", "c.txt")

	env := append([]string{"GIT_EDITOR=true"}, revertSession...)
	_, stderr, _ := runSafegitEnv(t, dir, env, "rebase", "--continue")
	if strings.Contains(stderr, "safegit does") || strings.Contains(stderr, "safegit rebase-continue") {
		t.Errorf("safegit claimed a rebase conclusion it does not have:\n%s", stderr)
	}
	if stderr != "" && !strings.Contains(stderr, "git rebase --continue") {
		t.Errorf("the refusal does not name git's own rebase conclusion:\n%s", stderr)
	}
}
