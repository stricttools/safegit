package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// `safegit merge` is pipeline-authored.
//
// It used to hand the operator's whole command line to git and let git author
// whatever commit came out of it: no safegit trailers, no commit-msg handling,
// nothing `safegit undo` could reverse, and git's own AUTO_MERGE left behind.
// The restructure splits the operation where git itself splits it --
// `git merge --no-ff --no-commit` COMPUTES the merge and parks it, and the
// conclusion engine (the one behind `safegit merge-continue`) turns that parked
// state into a commit -- so a merge safegit performed is a safegit commit.
//
// The three outcomes are distinct and each is pinned here:
//
//   - a FAST-FORWARD moves the ref and syncs the index and the working tree
//     onto it. No commit is created, so there is nothing for safegit to author
//     and nothing for `safegit undo` to reverse;
//   - a clean NON-FAST-FORWARD merge is concluded immediately: one
//     pipeline-authored merge commit, both parents, trailers, undoable;
//   - a CONFLICTED merge parks exactly as it always did, and the operator
//     concludes it with `safegit merge-continue`.
//
// The command line itself is narrower than git's, under the subset law: one
// branch, no strategy selection, no --squash, no --edit, no --autostash.

var mergeSession = []string{"CLAUDE_CODE_SESSION_ID=merge-restructure-test"}

// newMergeableRepo builds a repository whose main and feature branches have
// each moved on in ways that do not collide: the merge is clean and is NOT a
// fast-forward, which is the shape that produces a merge commit.
func newMergeableRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "base.txt", "base\n")
	safegitCommitEnv(t, dir, mergeSession, "base", "base.txt")

	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "feature")
	testutil.WriteFile(t, dir, "feature.txt", "feature\n")
	safegitCommitEnv(t, dir, mergeSession, "feature side", "feature.txt")

	testutil.Git(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "main.txt", "main\n")
	safegitCommitEnv(t, dir, mergeSession, "main side", "main.txt")

	return dir
}

// newFastForwardableRepo builds a repository whose feature branch is strictly
// ahead of main, so merging it is a fast-forward.
func newFastForwardableRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "base.txt", "base\n")
	safegitCommitEnv(t, dir, mergeSession, "base", "base.txt")

	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "feature")
	testutil.WriteFile(t, dir, "ahead.txt", "ahead\n")
	safegitCommitEnv(t, dir, mergeSession, "one ahead", "ahead.txt")
	testutil.WriteFile(t, dir, "ahead2.txt", "ahead again\n")
	safegitCommitEnv(t, dir, mergeSession, "two ahead", "ahead2.txt")

	testutil.Git(t, dir, "switch", "main")
	return dir
}

// TestCleanMergeIsPipelineAuthored is the centerpiece: a clean merge that is
// not a fast-forward produces ONE safegit commit, with both parents, safegit's
// trailers, exactly one oplog entry under the command's own op name, and
// nothing of git's operation state left behind.
func TestCleanMergeIsPipelineAuthored(t *testing.T) {
	dir := newMergeableRepo(t)
	mainSHA := testutil.Rev(t, dir, "HEAD")
	featureSHA := testutil.Rev(t, dir, "feature")

	stdout, stderr, code := runSafegitEnv(t, dir, mergeSession, "merge", "feature")
	if code != 0 {
		t.Fatalf("a clean merge failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	head := testutil.Rev(t, dir, "HEAD")
	parents := testutil.Parents(t, dir, head)
	if len(parents) != 2 || parents[0] != mainSHA || parents[1] != featureSHA {
		t.Fatalf("the merge commit's parents are %v, want [%s %s]", parents, mainSHA, featureSHA)
	}

	// Authored by the pipeline, which is what the trailer says.
	msg := commitMessageOf(t, dir, "HEAD")
	if !strings.Contains(msg, "Claude-Code-Session-Id: merge-restructure-test") {
		t.Errorf("the merge commit carries no session trailer, so git authored it:\n%s", msg)
	}

	// Both sides' files are in the tree: the conclusion commits the merge's
	// whole staged result.
	paths := testutil.TreePaths(t, dir, head)
	for _, want := range []string{"main.txt", "feature.txt"} {
		if !testutil.Contains(paths, want) {
			t.Errorf("the merge commit dropped %s (tree: %v)", want, paths)
		}
	}

	// One entry, under the COMMAND's own op name.
	entries := oplogEntries(t, dir, "merge")
	if len(entries) != 1 {
		t.Fatalf("expected exactly one merge oplog entry, got %d: %v", len(entries), entries)
	}
	extra := oplogExtra(entries[0])
	if got, _ := oplogExtraString(extra, "ref"); got != "refs/heads/main" {
		t.Errorf("the merge entry records ref %q, want refs/heads/main; extra=%v", got, extra)
	}
	if got, _ := oplogExtraString(extra, "parent"); got != mainSHA {
		t.Errorf("the merge entry records old tip %q, want %q; extra=%v", got, mainSHA, extra)
	}
	if got, _ := oplogExtraString(extra, "sha", "to", "result"); got != head {
		t.Errorf("the merge entry records new tip %q, want %q; extra=%v", got, head, extra)
	}

	// git's operation state is gone, so an ordinary commit works again.
	assertNoSequencerResidue(t, dir, "pipeline-authored merge")

	// And the whole thing is reversible, which is what authorship buys.
	if _, stderr, code := runSafegitEnv(t, dir, mergeSession, "undo"); code != 0 {
		t.Fatalf("undo of a pipeline-authored merge failed (code %d): %s", code, stderr)
	}
	if back := testutil.Rev(t, dir, "HEAD"); back != mainSHA {
		t.Errorf("undo left HEAD at %s, want the pre-merge tip %s", back, mainSHA)
	}
}

// TestFastForwardMergeMovesTheRefAndSyncs pins the fast-forward path: safegit
// decides the fast-forward itself and moves the ref under compare-and-swap,
// then puts the index and the working tree in step with it. A bare ref move
// leaves the INVERSE of the incoming diff staged, so the sync is not optional.
func TestFastForwardMergeMovesTheRefAndSyncs(t *testing.T) {
	dir := newFastForwardableRepo(t)
	before := testutil.Rev(t, dir, "HEAD")
	featureSHA := testutil.Rev(t, dir, "feature")

	stdout, stderr, code := runSafegitEnv(t, dir, mergeSession, "merge", "feature")
	if code != 0 {
		t.Fatalf("the fast-forward merge failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	head := testutil.Rev(t, dir, "HEAD")
	if head != featureSHA {
		t.Fatalf("the fast-forward left HEAD at %s, want the incoming tip %s", head, featureSHA)
	}
	if parents := testutil.Parents(t, dir, head); len(parents) != 1 {
		t.Errorf("a fast-forward must create no merge commit; HEAD has %d parent(s): %v", len(parents), parents)
	}

	// The index AND the working tree are in step with the new tip.
	if status := strings.TrimSpace(testutil.Git(t, dir, "status", "--porcelain")); status != "" {
		t.Errorf("the fast-forward left the index or the working tree out of step:\n%s", status)
	}
	for _, want := range []string{"ahead.txt", "ahead2.txt"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("the fast-forward did not put %s in the working tree: %v", want, err)
		}
	}

	// The outcome is recorded, and it says what happened rather than reading
	// like a commit safegit made.
	entries := oplogEntries(t, dir, "merge")
	if len(entries) != 1 {
		t.Fatalf("expected exactly one merge oplog entry, got %d: %v", len(entries), entries)
	}
	extra := oplogExtra(entries[0])
	outcome, _ := extra["outcome"].(string)
	if !strings.Contains(outcome, "fast-forward") {
		t.Errorf("the fast-forward entry records outcome %q, which does not say it was a fast-forward; extra=%v", outcome, extra)
	}
	if got, _ := oplogExtraString(extra, "parent"); got != before {
		t.Errorf("the fast-forward entry records old tip %q, want %q; extra=%v", got, before, extra)
	}
	if got, _ := oplogExtraString(extra, "sha", "to", "result"); got != featureSHA {
		t.Errorf("the fast-forward entry records new tip %q, want %q; extra=%v", got, featureSHA, extra)
	}

	// The commits the branch now carries are git's, not safegit's, so undo has
	// nothing of its own to reverse and refuses rather than walking the ref
	// back over them.
	_, undoErr, undoCode := runSafegitEnv(t, dir, mergeSession, "undo")
	if undoCode == 0 {
		t.Fatalf("undo reversed a fast-forward; the commits it moved onto are not safegit's:\n%s", undoErr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != featureSHA {
		t.Errorf("the refused undo moved HEAD to %s (was %s)", head, featureSHA)
	}
}

// TestMergeRefusesRawGitShapes pins the subset boundary: the command lines
// safegit's merge does not implement are refused before anything runs, each
// naming why.
func TestMergeRefusesRawGitShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		// says is a fragment the refusal must carry, so the operator learns
		// which capability is absent rather than only that something failed.
		says string
	}{
		{"octopus", []string{"merge", "feature", "other"}, "one branch"},
		// --commit was accepted and quietly dropped from the argv the compute
		// step forwards. What it asks for is the one thing the restructure
		// cannot give: git's own commit, moving the ref outside safegit's
		// compare-and-swap. cherry-pick and revert already refuse it by name.
		{"commit", []string{"merge", "--commit", "feature"}, "--commit"},
		{"strategy selection", []string{"merge", "-s", "ours", "feature"}, "strateg"},
		{"strategy option", []string{"merge", "-X", "ours", "feature"}, "strateg"},
		{"squash", []string{"merge", "--squash", "feature"}, "--squash"},
		{"edit", []string{"merge", "--edit", "feature"}, "--edit"},
		{"autostash", []string{"merge", "--autostash", "feature"}, "Commit your changes first"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newMergeableRepo(t)
			testutil.Git(t, dir, "branch", "other")
			tip := testutil.Rev(t, dir, "HEAD")

			stdout, stderr, code := runSafegitEnv(t, dir, mergeSession, tc.args...)
			if code != exitcode.Usage {
				t.Fatalf("safegit %s exited %d, want %d (Usage)\nstdout=%s\nstderr=%s",
					strings.Join(tc.args, " "), code, exitcode.Usage, stdout, stderr)
			}
			if !strings.Contains(stderr, tc.says) {
				t.Errorf("the refusal does not say %q:\n%s", tc.says, stderr)
			}
			if head := testutil.Rev(t, dir, "HEAD"); head != tip {
				t.Errorf("the refused merge moved HEAD to %s (was %s)", head, tip)
			}
			assertNoSequencerResidue(t, dir, "refused merge")
		})
	}
}

// TestMergeSubsetRefusalsApplyToAPreviewToo: an option safegit's merge does not
// implement is refused whether or not the run was going to happen. A preview of
// a command that cannot run is not a preview of anything, and nothing is
// recorded in the would-do log for it either.
//
// These three cases used to be preview-specific refusals ("merge-tree
// implements only ort"), which said the outcome could not be COMPUTED. It is
// the wrong answer now that the option cannot be RUN.
func TestMergeSubsetRefusalsApplyToAPreviewToo(t *testing.T) {
	for _, args := range [][]string{
		{"--dry-run", "merge", "-s", "resolve", "feature"},
		{"--dry-run", "merge", "-X", "ours", "feature"},
		{"--dry-run", "merge", "--squash", "feature"},
	} {
		t.Run(strings.Join(args[2:], " "), func(t *testing.T) {
			dir := newMergeableRepo(t)
			stdout, stderr, code := runSafegitEnv(t, dir, mergeSession, args...)
			if code != exitcode.Usage {
				t.Fatalf("safegit %s exited %d, want %d (Usage)\nstdout=%s\nstderr=%s",
					strings.Join(args, " "), code, exitcode.Usage, stdout, stderr)
			}
			if !strings.Contains(stderr, "does not support") {
				t.Errorf("the refusal does not name the absent capability:\n%s", stderr)
			}
			if strings.Contains(stdout, "run: git") {
				t.Errorf("the refused invocation was still recorded as a would-do:\n%s", stdout)
			}
		})
	}
}

// TestMergeNoCommitParksWithoutConcluding: `--no-commit` is the operator saying
// they want to look at the result before it becomes a commit, so safegit
// computes the merge and PARKS it -- even though it is clean and could have
// been concluded on the spot.
func TestMergeNoCommitParksWithoutConcluding(t *testing.T) {
	dir := newMergeableRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")
	featureSHA := testutil.Rev(t, dir, "feature")

	stdout, stderr, code := runSafegitEnv(t, dir, mergeSession, "merge", "--no-commit", "feature")
	if code != 0 {
		t.Fatalf("merge --no-commit failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Fatalf("merge --no-commit committed anyway: HEAD moved to %s (was %s)", head, tip)
	}
	testutil.AssertMergeHead(t, dir, featureSHA, "merge --no-commit must park the merge")

	// The way out is safegit's own conclusion, and it works.
	if _, stderr, code := runSafegitEnv(t, dir, mergeSession, "merge-continue"); code != 0 {
		t.Fatalf("merge-continue could not conclude the parked merge (code %d): %s", code, stderr)
	}
	if parents := testutil.Parents(t, dir, testutil.Rev(t, dir, "HEAD")); len(parents) != 2 {
		t.Errorf("the concluded merge has %d parent(s), want 2: %v", len(parents), parents)
	}
	assertNoSequencerResidue(t, dir, "concluded parked merge")
}

// TestMergeFFOnlyRefusesANonFastForward: --ff-only is a declaration that the
// operator wants no merge commit. Where one would be needed, the merge is
// refused and nothing moves.
func TestMergeFFOnlyRefusesANonFastForward(t *testing.T) {
	dir := newMergeableRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, mergeSession, "merge", "--ff-only", "feature")
	if code == 0 {
		t.Fatalf("merge --ff-only succeeded on a non-fast-forward\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "fast-forward") {
		t.Errorf("the refusal does not say the merge is not a fast-forward:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the refused merge moved HEAD to %s (was %s)", head, tip)
	}
	assertNoSequencerResidue(t, dir, "refused --ff-only merge")
}

// TestMergeNoFFElectsAMergeCommit: a fast-forwardable merge with --no-ff takes
// the merge-commit path, and that commit is safegit's like any other.
func TestMergeNoFFElectsAMergeCommit(t *testing.T) {
	dir := newFastForwardableRepo(t)
	before := testutil.Rev(t, dir, "HEAD")
	featureSHA := testutil.Rev(t, dir, "feature")

	stdout, stderr, code := runSafegitEnv(t, dir, mergeSession, "merge", "--no-ff", "feature")
	if code != 0 {
		t.Fatalf("merge --no-ff failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	parents := testutil.Parents(t, dir, testutil.Rev(t, dir, "HEAD"))
	if len(parents) != 2 || parents[0] != before || parents[1] != featureSHA {
		t.Fatalf("--no-ff produced parents %v, want [%s %s]", parents, before, featureSHA)
	}
	if msg := commitMessageOf(t, dir, "HEAD"); !strings.Contains(msg, "Claude-Code-Session-Id: merge-restructure-test") {
		t.Errorf("the --no-ff merge commit carries no session trailer, so git authored it:\n%s", msg)
	}
	assertNoSequencerResidue(t, dir, "--no-ff merge")
}

// TestMergeCarriesTheOperatorsMessage: `-m` replaces git's own draft, and the
// conclusion commits exactly that text.
func TestMergeCarriesTheOperatorsMessage(t *testing.T) {
	dir := newMergeableRepo(t)

	if _, stderr, code := runSafegitEnv(t, dir, mergeSession, "merge", "-m", "bring the feature in", "feature"); code != 0 {
		t.Fatalf("merge -m failed (code %d): %s", code, stderr)
	}
	msg := commitMessageOf(t, dir, "HEAD")
	if !strings.HasPrefix(msg, "bring the feature in") {
		t.Errorf("the merge commit does not carry the operator's message:\n%s", msg)
	}
}

// TestConflictedMergeParksAndNarrates: the conflicted path is unchanged. git's
// own narration reaches the operator, the merge is parked, and merge-continue
// concludes it.
func TestConflictedMergeParksAndNarrates(t *testing.T) {
	dir := newDivergedBranchRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")
	featureSHA := testutil.Rev(t, dir, "feature")

	stdout, stderr, code := runSafegitEnv(t, dir, mergeSession, "merge", "feature")
	if code == 0 {
		t.Fatalf("the fixture needs a conflict\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "CONFLICT") {
		t.Errorf("git's conflict narration reached neither stream\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the conflicted merge moved HEAD to %s (was %s)", head, tip)
	}
	testutil.AssertMergeHead(t, dir, featureSHA, "a conflicted merge must park")

	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nresolved\nline3\n")
	if _, stderr, code := runSafegitEnv(t, dir, mergeSession, "merge-continue",
		"--resolve", "conflicted.txt=worktree"); code != 0 {
		t.Fatalf("merge-continue could not conclude the conflicted merge (code %d): %s", code, stderr)
	}
	assertNoSequencerResidue(t, dir, "concluded conflicted merge")
}
