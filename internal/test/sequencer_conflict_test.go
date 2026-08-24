package test

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// A git command that stops mid-sequence -- a conflicted cherry-pick, a
// conflicted revert, a --no-commit merge -- leaves its whole result in the
// index: unmerged stage 1/2/3 entries for every conflicted path, ordinary
// staged entries for every path that merged cleanly, plus a state file
// (CHERRY_PICK_HEAD, REVERT_HEAD, MERGE_HEAD, .git/sequencer,
// .git/rebase-merge) naming what is in flight. `git <cmd> --continue` reads
// exactly that index. It is the sequencer's only memory of the work already
// done.
//
// safegit's tree-mutating wrappers end by running `git read-tree HEAD`
// (coord_cmd.go:48-55 -> internal/git/git.go:265-300) to restore its own
// index-equals-HEAD invariant. read-tree without --merge discards every
// unmerged entry and every staged path, so wherever that sync runs while a
// sequencer is in flight, the sequencer's memory is erased while its state
// file survives. The repo is then in a state git can never produce on its
// own: mid-cherry-pick with an index that says nothing happened.
//
// Two seams reach the sync in that condition:
//
//	coord_cmd.go:371-373  runGuardedPassthrough (cherry-pick, revert) captures
//	                      git's exit code and syncs unconditionally, so the
//	                      conflict path is destroyed.
//	coord_cmd.go:186-193  runMerge returns 1 before its sync when git fails, so
//	                      the conflict path survives -- but `merge --no-commit`
//	                      succeeds, reaches the sync, and loses the staged
//	                      merge result while MERGE_HEAD stays behind.
//
// Every test below states the desired behavior as an equality against plain
// git run over an identical repository: after the same command line, safegit
// must leave the same index, the same porcelain status and the same sequencer
// state files as git does. Nothing here asserts today's behavior, so the suite
// needs no edits when the bug is fixed.
//
// RED today: cherry-pick, revert, their --no-commit forms, the multi-commit
// sequencer case, and merge --no-commit.
// GREEN today: the merge and rebase conflict paths, which return before their
// sync. They are kept as controls -- a fix must not regress them.

// seqConflictMarkers are the sequencer state entries under .git that say a
// multi-step git operation is in flight.
var seqConflictMarkers = []string{
	"CHERRY_PICK_HEAD",
	"REVERT_HEAD",
	"MERGE_HEAD",
	"rebase-merge",
	"rebase-apply",
	"sequencer",
}

// seqConflictState is everything about a repository that a sequencer needs in
// order to be continuable: the unmerged index entries, the full porcelain
// status (which also carries the cleanly staged paths), and the state files.
type seqConflictState struct {
	unmerged  []string
	porcelain []string
	markers   []string
	exitCode  int
}

func (s seqConflictState) String() string {
	return "unmerged=" + strings.Join(s.unmerged, " / ") +
		"; status=" + strings.Join(s.porcelain, " | ") +
		"; markers=" + strings.Join(s.markers, ",") +
		"; exit=" + strconv.Itoa(s.exitCode)
}

// seqConflictCapture reads the continuable state of a repository.
func seqConflictCapture(t *testing.T, dir string, exitCode int) seqConflictState {
	t.Helper()
	st := seqConflictState{exitCode: exitCode}

	unmerged := testutil.Git(t, dir, "ls-files", "-u")
	if unmerged != "" {
		st.unmerged = strings.Split(unmerged, "\n")
	}
	sort.Strings(st.unmerged)

	porcelain := testutil.Git(t, dir, "status", "--porcelain")
	if porcelain != "" {
		st.porcelain = strings.Split(porcelain, "\n")
	}
	sort.Strings(st.porcelain)

	for _, m := range seqConflictMarkers {
		if _, err := os.Stat(filepath.Join(dir, ".git", m)); err == nil {
			st.markers = append(st.markers, m)
		}
	}
	sort.Strings(st.markers)
	return st
}

// seqConflictRepo builds the fixture both twins share:
//
//	base       c.txt ("base"), other.txt        -- on main and side
//	main edit  c.txt -> "main"                  -- main only
//	add extra  extra.txt                        -- side only, merges cleanly
//	side edit  c.txt -> "side", newfile.txt     -- side only, conflicts on c.txt
//
// `side edit` deliberately mixes a conflicting path with a clean one:
// newfile.txt is what the index sync silently throws away, and it is what a
// `--continue` must still carry into the resulting commit.
func seqConflictRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "c.txt", "line1\nbase\nline3\n")
	testutil.WriteFile(t, dir, "other.txt", "v1\n")
	testutil.Git(t, dir, "add", "c.txt", "other.txt")
	testutil.Git(t, dir, "commit", "-m", "base")

	testutil.Git(t, dir, "branch", "side")

	testutil.WriteFile(t, dir, "c.txt", "line1\nmain\nline3\n")
	testutil.Git(t, dir, "add", "c.txt")
	testutil.Git(t, dir, "commit", "-m", "main edit")

	testutil.Git(t, dir, "switch", "side")
	testutil.WriteFile(t, dir, "extra.txt", "extra\n")
	testutil.Git(t, dir, "add", "extra.txt")
	testutil.Git(t, dir, "commit", "-m", "add extra")

	testutil.WriteFile(t, dir, "c.txt", "line1\nside\nline3\n")
	testutil.WriteFile(t, dir, "newfile.txt", "new\n")
	testutil.Git(t, dir, "add", "c.txt", "newfile.txt")
	testutil.Git(t, dir, "commit", "-m", "side edit")

	testutil.Git(t, dir, "switch", "main")
	return dir
}

// seqConflictTwins builds two identical fixture repos and runs the same
// command line in each: plain git in the first, safegit in the second. It
// returns both directories and both captured states.
//
// setup, when non-nil, runs in each repo before the command under test (used
// by the rebase case, which must be on the side branch).
func seqConflictTwins(t *testing.T, setup func(t *testing.T, dir string), argv ...string) (gitDir, sgDir string, gitState, sgState seqConflictState) {
	t.Helper()

	gitDir = seqConflictRepo(t)
	sgDir = seqConflictRepo(t)
	if setup != nil {
		setup(t, gitDir)
		setup(t, sgDir)
	}

	_, gitCode := testutil.GitTry(t, gitDir, argv...)
	gitState = seqConflictCapture(t, gitDir, gitCode)

	_, _, sgCode := runSafegit(t, sgDir, argv...)
	sgState = seqConflictCapture(t, sgDir, sgCode)

	return gitDir, sgDir, gitState, sgState
}

// seqConflictAssertSameState fails unless safegit left the repository in the
// same continuable state plain git did.
func seqConflictAssertSameState(t *testing.T, what string, gitState, sgState seqConflictState) {
	t.Helper()
	if strings.Join(gitState.unmerged, "\n") == strings.Join(sgState.unmerged, "\n") &&
		strings.Join(gitState.porcelain, "\n") == strings.Join(sgState.porcelain, "\n") &&
		strings.Join(gitState.markers, "\n") == strings.Join(sgState.markers, "\n") {
		return
	}
	t.Fatalf("%s: safegit left a different repository state than git.\n  git:     %s\n  safegit: %s",
		what, gitState, sgState)
}

// seqConflictAssertMidSequence is a fixture sanity check: plain git really did
// stop mid-sequence with unmerged entries, so the comparison above is about
// something.
func seqConflictAssertMidSequence(t *testing.T, gitState seqConflictState, wantMarker string) {
	t.Helper()
	if len(gitState.unmerged) == 0 {
		t.Fatalf("fixture is wrong: plain git left no unmerged entries (%s)", gitState)
	}
	found := false
	for _, m := range gitState.markers {
		if m == wantMarker {
			found = true
		}
	}
	if !found {
		t.Fatalf("fixture is wrong: plain git left no %s (%s)", wantMarker, gitState)
	}
}

// A conflicted cherry-pick must leave exactly what git leaves: three unmerged
// entries for c.txt, newfile.txt staged, CHERRY_PICK_HEAD present.
//
// RED: runGuardedPassthrough (coord_cmd.go:371-373) captures git's exit code
// and then runs the index sync anyway, so safegit ends with zero unmerged
// entries and newfile.txt untracked while CHERRY_PICK_HEAD stays behind.
func TestSeqConflictCherryPickPreservesConflictState(t *testing.T) {
	_, _, gitState, sgState := seqConflictTwins(t, nil, "cherry-pick", "side")
	seqConflictAssertMidSequence(t, gitState, "CHERRY_PICK_HEAD")
	seqConflictAssertSameState(t, "conflicted cherry-pick", gitState, sgState)
}

// Resolving the conflict and running `git cherry-pick --continue` must produce
// the same commit after safegit as after git: the picked commit's whole
// content, newfile.txt included.
//
// RED: with the index wiped, `git add` stages only the file the operator
// touched, so --continue commits a tree missing newfile.txt -- a wrong commit,
// silently, with exit 0.
func TestSeqConflictCherryPickContinueProducesSameCommit(t *testing.T) {
	gitDir, sgDir, gitState, _ := seqConflictTwins(t, nil, "cherry-pick", "side")
	seqConflictAssertMidSequence(t, gitState, "CHERRY_PICK_HEAD")

	resolveAndContinue := func(dir string) (paths []string, out string, code int) {
		testutil.WriteFile(t, dir, "c.txt", "line1\nresolved\nline3\n")
		testutil.Git(t, dir, "add", "c.txt")
		out, code = testutil.GitTryEnv(t, dir, []string{"GIT_EDITOR=true"}, "cherry-pick", "--continue")
		if code != 0 {
			return nil, out, code
		}
		listing := testutil.Git(t, dir, "ls-tree", "-r", "--name-only", "HEAD")
		if listing != "" {
			paths = strings.Split(listing, "\n")
		}
		sort.Strings(paths)
		return paths, out, code
	}

	gitPaths, gitOut, gitCode := resolveAndContinue(gitDir)
	sgPaths, sgOut, sgCode := resolveAndContinue(sgDir)

	if gitCode != 0 {
		t.Fatalf("fixture is wrong: git cherry-pick --continue failed (%d): %s", gitCode, oneLine(gitOut))
	}
	if sgCode != gitCode {
		t.Fatalf("after safegit, git cherry-pick --continue exited %d (git twin: %d): %s",
			sgCode, gitCode, oneLine(sgOut))
	}
	if strings.Join(gitPaths, " ") != strings.Join(sgPaths, " ") {
		t.Fatalf("the concluded cherry-pick has a different tree after safegit.\n  git:     %v\n  safegit: %v\n  safegit --continue said: %s",
			gitPaths, sgPaths, oneLine(sgOut))
	}
}

// A conflicted revert must leave git's unmerged entries and REVERT_HEAD.
//
// Reverting the commit that created c.txt, while a later commit modified it,
// is a delete/modify conflict.
//
// RED: same unconditional sync at coord_cmd.go:373.
func TestSeqConflictRevertPreservesConflictState(t *testing.T) {
	_, _, gitState, sgState := seqConflictTwins(t, nil, "revert", "--no-edit", "HEAD~1")
	seqConflictAssertMidSequence(t, gitState, "REVERT_HEAD")
	seqConflictAssertSameState(t, "conflicted revert", gitState, sgState)
}

// `cherry-pick --no-commit` succeeds and leaves its whole result staged. That
// staged result is the entire point of the flag.
//
// RED: the sync at coord_cmd.go:373 runs on the success path too, so extra.txt
// ends up untracked instead of staged.
func TestSeqConflictCherryPickNoCommitPreservesStagedResult(t *testing.T) {
	_, _, gitState, sgState := seqConflictTwins(t, nil, "cherry-pick", "--no-commit", "side~1")
	if gitState.exitCode != 0 {
		t.Fatalf("fixture is wrong: git cherry-pick --no-commit exited %d (%s)", gitState.exitCode, gitState)
	}
	if len(gitState.porcelain) == 0 {
		t.Fatalf("fixture is wrong: git cherry-pick --no-commit staged nothing (%s)", gitState)
	}
	seqConflictAssertSameState(t, "cherry-pick --no-commit", gitState, sgState)
}

// `revert --no-commit` stages the inverse patch and leaves REVERT_HEAD.
//
// RED: the staged inverse patch is unstaged by the sync; only the working-tree
// change survives, and REVERT_HEAD is left pointing at an operation whose
// result is no longer in the index.
func TestSeqConflictRevertNoCommitPreservesStagedResult(t *testing.T) {
	_, _, gitState, sgState := seqConflictTwins(t, nil, "revert", "--no-commit", "--no-edit", "HEAD")
	if gitState.exitCode != 0 {
		t.Fatalf("fixture is wrong: git revert --no-commit exited %d (%s)", gitState.exitCode, gitState)
	}
	seqConflictAssertSameState(t, "revert --no-commit", gitState, sgState)
}

// A multi-commit cherry-pick used to be a state safegit could put a repository
// in, and the property pinned here was that safegit left git's partial progress
// -- .git/sequencer plus the conflict -- exactly as git left it.
//
// safegit's cherry-pick now applies ONE commit and authors the result itself,
// so this command line is refused before any git runs and there is no partial
// progress to preserve. What the twins pin instead is the difference itself:
// plain git queues the picks and stops mid-sequence, safegit refuses and leaves
// the repository untouched.
func TestSeqConflictMultiPickIsRefusedWhileGitQueuesIt(t *testing.T) {
	gitDir, sgDir, gitState, sgState := seqConflictTwins(t, nil, "cherry-pick", "side~1", "side")

	// Plain git does what it always did.
	seqConflictAssertMidSequence(t, gitState, "CHERRY_PICK_HEAD")
	if !testutil.FileExists(filepath.Join(gitDir, ".git", "sequencer")) {
		t.Fatalf("fixture is wrong: plain git left no queue (%s)", gitState)
	}

	// safegit refuses the command line, and nothing in the repository moved.
	if sgState.exitCode != exitcode.Usage {
		t.Fatalf("safegit exited %d, want %d (Usage): %s", sgState.exitCode, exitcode.Usage, sgState)
	}
	if len(sgState.unmerged) != 0 || len(sgState.porcelain) != 0 || len(sgState.markers) != 0 {
		t.Errorf("the refused cherry-pick left state behind: %s", sgState)
	}
	if testutil.Rev(t, sgDir, "HEAD") == "" {
		t.Error("the refusal left no readable HEAD")
	}
}

// `merge --no-commit` succeeds, stages the merge result and leaves MERGE_HEAD
// for the operator to conclude.
//
// RED, and the reason "runMerge returns before its sync" is not a fix: the
// early return at coord_cmd.go:186-188 only covers git FAILING. A --no-commit
// merge exits 0, reaches the sync at coord_cmd.go:193, and loses the staged
// merge result while MERGE_HEAD survives -- so a later `git commit` writes a
// merge commit whose tree is missing everything the merge brought in.
func TestSeqConflictMergeNoCommitPreservesStagedResult(t *testing.T) {
	_, _, gitState, sgState := seqConflictTwins(t, nil, "merge", "--no-commit", "--no-ff", "side~1")
	if gitState.exitCode != 0 {
		t.Fatalf("fixture is wrong: git merge --no-commit exited %d (%s)", gitState.exitCode, gitState)
	}
	if len(gitState.porcelain) == 0 {
		t.Fatalf("fixture is wrong: git merge --no-commit staged nothing (%s)", gitState)
	}
	seqConflictAssertSameState(t, "merge --no-commit", gitState, sgState)
}

// GREEN control: a conflicted merge. runMerge returns 1 at coord_cmd.go:186-188
// before reaching its sync, so the conflict survives today. A fix to the
// passthrough seam must not regress this.
func TestSeqConflictMergePreservesConflictState(t *testing.T) {
	_, _, gitState, sgState := seqConflictTwins(t, nil, "merge", "side")
	seqConflictAssertMidSequence(t, gitState, "MERGE_HEAD")
	seqConflictAssertSameState(t, "conflicted merge", gitState, sgState)
}

// GREEN control: a conflicted rebase. runRebase returns 1 at
// coord_cmd.go:227-229 before its sync at coord_cmd.go:234.
func TestSeqConflictRebasePreservesConflictState(t *testing.T) {
	onSide := func(t *testing.T, dir string) {
		t.Helper()
		testutil.Git(t, dir, "switch", "side")
	}
	_, _, gitState, sgState := seqConflictTwins(t, onSide, "rebase", "main")
	seqConflictAssertMidSequence(t, gitState, "rebase-merge")
	seqConflictAssertSameState(t, "conflicted rebase", gitState, sgState)
}
