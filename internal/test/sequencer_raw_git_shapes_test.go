package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// The states safegit's conclusions REFUSE, because safegit can no longer put a
// repository in them.
//
// Every operation safegit starts is one it can finish: one branch merged with
// the default strategy, one commit picked, one commit reverted. Three shapes
// fall outside that and only RAW git can create them now:
//
//   - a SEQUENCER QUEUE (`git cherry-pick <a> <b>`, `git revert <a> <b>`), whose
//     remaining commands are part of the state a conclusion removes, so
//     concluding one step natively would throw the rest away;
//   - an OCTOPUS merge, whose staged result every check safegit makes over a
//     merge is written against two sides of;
//   - a content conflict with NO AUTO_MERGE, which is the signature of a
//     non-default merge strategy: on the git version safegit requires, the
//     default strategy always records that tree, and it is what the
//     marker verification reads to tell a conflict block git wrote from one that
//     was already in the file.
//
// Each is refused, and each refusal names git's own conclusion. What git started
// git can finish; what safegit did not start it does not pretend to own.

// queuedFixture is a repository parked mid-queue: two commands queued, the
// first one conflicted.
type queuedFixture struct {
	dir string
	// tip is the branch tip the queue stopped at, which is where a refused
	// conclusion has to leave it.
	tip string
	// conflicted is the path the queue stopped on.
	conflicted string
	// clean is the path the queue's SECOND command touches. It is what proves
	// the rest of the queue survived: it can only reach the tree if something
	// got past the conflicted step.
	clean string
}

// newQueuedPickRepo builds a repository parked in a conflicted two-command
// cherry-pick or revert queue.
//
// For the cherry-pick, `side one` conflicts with main's edit of c.txt and
// `side two` adds d.txt cleanly on top. For the revert, two commits on main are
// reverted newest-first; the older one conflicts because a later edit sits over
// it, and the newer one reverts cleanly.
//
// RAW GIT creates the queue, and that is the point: `safegit cherry-pick` and
// `safegit revert` each take exactly one commit and author the result
// themselves, so a multi-command sequence is a state only git can put a
// repository in -- which is exactly why safegit's conclusions refuse it.
func newQueuedPickRepo(t *testing.T, verb string) queuedFixture {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "c.txt", "base\n")
	testutil.WriteFile(t, dir, "d.txt", "base\n")
	safegitCommitEnv(t, dir, conclusionSession, "base", "c.txt", "d.txt")

	var argv []string
	switch verb {
	case "cherry-pick":
		testutil.Git(t, dir, "branch", "side")
		testutil.WriteFile(t, dir, "c.txt", "main\n")
		safegitCommitEnv(t, dir, conclusionSession, "main", "c.txt")

		testutil.Git(t, dir, "switch", "side")
		testutil.WriteFile(t, dir, "c.txt", "side one\n")
		first := safegitCommitEnv(t, dir, conclusionSession, "side one", "c.txt")
		testutil.WriteFile(t, dir, "d.txt", "side two\n")
		second := safegitCommitEnv(t, dir, conclusionSession, "side two", "d.txt")
		testutil.Git(t, dir, "switch", "main")
		argv = []string{"cherry-pick", first, second}

	case "revert":
		testutil.WriteFile(t, dir, "c.txt", "the change to undo\n")
		older := safegitCommitEnv(t, dir, conclusionSession, "older change", "c.txt")
		testutil.WriteFile(t, dir, "d.txt", "the clean change\n")
		newer := safegitCommitEnv(t, dir, conclusionSession, "newer change", "d.txt")
		// A later edit over c.txt is what makes the older revert conflict.
		testutil.WriteFile(t, dir, "c.txt", "a later edit\n")
		safegitCommitEnv(t, dir, conclusionSession, "a later edit", "c.txt")
		argv = []string{"revert", "--no-edit", newer, older}

	default:
		t.Fatalf("unknown verb %q", verb)
	}

	if out, code := testutil.GitTry(t, dir, argv...); code == 0 {
		t.Fatalf("the fixture needs a mid-queue conflict from `git %s`: %s", strings.Join(argv, " "), out)
	}
	if !testutil.FileExists(filepath.Join(dir, ".git", "sequencer")) {
		t.Fatalf("the fixture must leave a sequencer queue behind (%s)", strings.Join(argv, " "))
	}

	// AFTER the queue stopped, which is where the branch stands when a
	// conclusion is attempted: a revert queue commits its clean command before
	// stopping on the conflicted one, so the tip a refusal must not move is this
	// one and not the fixture's starting point.
	return queuedFixture{dir: dir, tip: testutil.Rev(t, dir, "HEAD"), conflicted: "c.txt", clean: "d.txt"}
}

// unmergedCount reports how many unmerged slots the SHARED index holds.
func unmergedCount(t *testing.T, dir string) int {
	t.Helper()
	out := testutil.Git(t, dir, "ls-files", "-u")
	if strings.TrimSpace(out) == "" {
		return 0
	}
	return len(strings.Split(strings.TrimSpace(out), "\n"))
}

// TestQueuedConclusionIsRefusedAndNamesGit: a queue is git's, from end to end.
func TestQueuedConclusionIsRefusedAndNamesGit(t *testing.T) {
	for _, verb := range []string{"cherry-pick", "revert"} {
		t.Run(verb, func(t *testing.T) {
			fx := newQueuedPickRepo(t, verb)

			stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
				verb+"-continue", "--resolve", fx.conflicted+"=theirs")
			if code != exitcode.CoordinationBusy {
				t.Fatalf("%s-continue on a raw-git queue exited %d, want %d (CoordinationBusy)\nstdout=%s\nstderr=%s",
					verb, code, exitcode.CoordinationBusy, stdout, stderr)
			}
			for _, want := range []string{"git " + verb + " --continue", "git " + verb + " --abort"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("the refusal does not name %q as the way out:\n%s", want, stderr)
				}
			}

			// Nothing happened: the branch has not moved and the queue it
			// declined to conclude is intact.
			if head := testutil.Rev(t, fx.dir, "HEAD"); head != fx.tip {
				t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, fx.tip)
			}
			if !testutil.FileExists(filepath.Join(fx.dir, ".git", "sequencer")) {
				t.Error("the refusal destroyed the queue it declined to conclude")
			}
			if n := unmergedCount(t, fx.dir); n == 0 {
				t.Error("the refusal resolved the conflict it declined to conclude")
			}
		})
	}
}

// TestQueuedConclusionIsRefusedBeforeTheResolutionChecks: the refusal is about
// WHOSE operation this is, so it does not depend on the operator declaring a
// well-formed resolution set first.
func TestQueuedConclusionIsRefusedBeforeTheResolutionChecks(t *testing.T) {
	fx := newQueuedPickRepo(t, "cherry-pick")

	_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession, "cherry-pick-continue")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("exit %d, want %d (CoordinationBusy): %s", code, exitcode.CoordinationBusy, stderr)
	}
	if !strings.Contains(stderr, "git cherry-pick --continue") {
		t.Errorf("the refusal does not name git's own conclusion:\n%s", stderr)
	}
}

// TestQueuedStateNamesGitAsTheWayOutEverywhere: the way-out authority is one
// authority, so every refusal issued while a raw-git queue is in flight names
// the same command the conclusion's own refusal names. A message telling an
// operator to run a command that then refuses is worse than no message.
func TestQueuedStateNamesGitAsTheWayOutEverywhere(t *testing.T) {
	fx := newQueuedPickRepo(t, "cherry-pick")

	testutil.WriteFile(t, fx.dir, "e.txt", "unrelated\n")
	_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"commit", "-m", "a commit mid-queue", "--", "e.txt")
	if code == 0 {
		t.Fatalf("a commit during a cherry-pick sequence must be refused:\n%s", stderr)
	}
	if !strings.Contains(stderr, "git cherry-pick --continue") {
		t.Errorf("the mid-operation refusal names a way out that does not conclude this state:\n%s", stderr)
	}
}

// newParkedOctopus parks a CLEAN octopus merge with raw git. safegit's merge
// takes exactly one branch, so this is a state only git can create.
func newParkedOctopus(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "a.txt", "base\n")
	safegitCommitEnv(t, dir, conclusionSession, "base", "a.txt")

	for _, name := range []string{"b1", "b2"} {
		testutil.Git(t, dir, "branch", name)
		testutil.Git(t, dir, "switch", name)
		testutil.WriteFile(t, dir, name+".txt", name+"\n")
		safegitCommitEnv(t, dir, conclusionSession, name, name+".txt")
		testutil.Git(t, dir, "switch", "main")
	}
	testutil.WriteFile(t, dir, "m.txt", "main\n")
	safegitCommitEnv(t, dir, conclusionSession, "main", "m.txt")

	if out, code := testutil.GitTry(t, dir, "merge", "--no-commit", "b1", "b2"); code != 0 {
		t.Fatalf("octopus merge --no-commit failed (code %d): %s", code, out)
	}
	head, err := os.ReadFile(filepath.Join(dir, ".git", "MERGE_HEAD"))
	if err != nil || len(testutil.SplitLines(strings.TrimSpace(string(head)))) < 2 {
		t.Fatalf("the fixture must park a merge of more than one head: %v / %q", err, head)
	}
	return dir
}

// TestOctopusConclusionIsRefusedAndNamesGit: an octopus is a merge safegit
// cannot start, so it is one safegit does not conclude either.
func TestOctopusConclusionIsRefusedAndNamesGit(t *testing.T) {
	dir := newParkedOctopus(t)
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, conclusionSession, "merge-continue")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("merge-continue on an octopus exited %d, want %d (CoordinationBusy)\nstdout=%s\nstderr=%s",
			code, exitcode.CoordinationBusy, stdout, stderr)
	}
	if !strings.Contains(stderr, "octopus") {
		t.Errorf("the refusal does not say what the state is:\n%s", stderr)
	}
	for _, want := range []string{"git merge --continue", "git merge --abort"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not name %q as the way out:\n%s", want, stderr)
		}
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, tip)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD")); err != nil {
		t.Errorf("the refusal removed the merge state it declined to conclude: %v", err)
	}
}

// newNoAutoMergeConflict parks a content conflict computed by a NON-DEFAULT
// merge strategy, which is the one shape that produces unmerged stages with no
// AUTO_MERGE tree beside them (probe-verified on git 2.55).
func newNoAutoMergeConflict(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "f.txt", "l1\nl2\nl3\n")
	safegitCommitEnv(t, dir, conclusionSession, "base", "f.txt")

	testutil.Git(t, dir, "switch", "-c", "side")
	testutil.WriteFile(t, dir, "f.txt", "S1\nl2\nl3\n")
	safegitCommitEnv(t, dir, conclusionSession, "side", "f.txt")

	testutil.Git(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "f.txt", "M1\nl2\nl3\n")
	safegitCommitEnv(t, dir, conclusionSession, "main", "f.txt")

	if out, code := testutil.GitTry(t, dir, "merge", "-s", "resolve", "side"); code == 0 {
		t.Fatalf("the fixture needs a conflict from the resolve strategy: %s", out)
	}
	if unmergedCount(t, dir) == 0 {
		t.Fatal("the fixture must leave a content conflict in the index")
	}
	if out, ok := testutil.GitTryOut(t, dir, "rev-parse", "--verify", "--quiet", "AUTO_MERGE"); ok && strings.TrimSpace(out) != "" {
		t.Fatalf("the fixture must leave NO AUTO_MERGE; git recorded %s", strings.TrimSpace(out))
	}
	return dir
}

// TestConflictWithoutAutoMergeIsRefusedAndNamesGit: the marker verification
// reads AUTO_MERGE to tell a conflict block git wrote from one that was already
// in the file, so a conflict recorded without it is one safegit cannot check --
// and it can only have been computed by a strategy safegit does not select.
func TestConflictWithoutAutoMergeIsRefusedAndNamesGit(t *testing.T) {
	dir := newNoAutoMergeConflict(t)
	tip := testutil.Rev(t, dir, "HEAD")

	testutil.WriteFile(t, dir, "f.txt", "resolved\nl2\nl3\n")
	stdout, stderr, code := runSafegitEnv(t, dir, conclusionSession,
		"merge-continue", "--resolve", "f.txt=worktree")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("merge-continue on a conflict with no AUTO_MERGE exited %d, want %d (CoordinationBusy)\nstdout=%s\nstderr=%s",
			code, exitcode.CoordinationBusy, stdout, stderr)
	}
	if !strings.Contains(stderr, "AUTO_MERGE") {
		t.Errorf("the refusal does not name what is missing:\n%s", stderr)
	}
	for _, want := range []string{"git merge --continue", "git merge --abort"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not name %q as the way out:\n%s", want, stderr)
		}
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, tip)
	}
}

// TestOrdinaryConflictedMergeStillConcludes is the control for the refusal
// above: the ordinary conflicted merge -- default strategy, one side, AUTO_MERGE
// recorded -- is exactly the shape safegit does conclude, and it still does.
func TestOrdinaryConflictedMergeStillConcludes(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true, resolveInTree: true})

	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=worktree"); code != 0 {
		t.Fatalf("the ordinary conflicted merge was refused (code %d): %s", code, stderr)
	}
	assertNoSequencerResidue(t, fx.dir, "ordinary conflicted merge")
}
