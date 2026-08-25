package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// The three restructured commands -- merge, cherry-pick and revert -- COMPUTE
// their operation with `git <verb> --no-commit` and then commit the staged
// result through safegit's own pipeline. That compute door is the reason they
// need an in-flight check of their own.
//
// Raw git refuses outright: `git cherry-pick <c>` over a parked revert exits
// 128 with "a revert is already in progress". But `git cherry-pick --no-commit`
// slips past that refusal -- the compute form is not the form git guards -- so
// the restructured command ran its compute over somebody else's parked
// operation and then concluded it as if it were its own.
//
// Two shapes of damage, both reproduced below:
//
//   - a pick over a parked REVERT committed, and left REVERT_HEAD orphaned in
//     the git directory for the next `safegit commit` to refuse over;
//   - a revert over a parked PICK staged an inverse patch it never committed
//     and reported a diagnosis that was not true of the state it found.
//
// The fix is the check every other author already makes: coord.GuardInFlight at
// ENTRY, before the compute, exit 5, rendering the way out from the single
// way-out authority -- the same refusal `safegit commit` produces in exactly
// this state.
//
// The state-control forms (--abort, --quit) and the -continue commands are NOT
// covered by it: they are the way out, and a way out that refused over the
// state it exists to clear would strand the repository.

var inflightSession = []string{"CLAUDE_CODE_SESSION_ID=inflight-compute-refusal-test"}

// stateFilePresent reports whether one of git's operation state files exists.
func stateFilePresent(t *testing.T, dir, name string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(dir, ".git", name))
	return err == nil
}

// newParkedRevertRepo builds a repository whose HEAD commit has been reverted,
// and whose SECOND revert of the same commit stopped with nothing to do and
// PARKED git's revert state over a clean working tree.
//
// It returns the repository and the commit that was reverted twice. A `side`
// branch diverges from before the reverted commit, so a merge run against this
// state has real work to compute.
func newParkedRevertRepo(t *testing.T) (dir, reverted string) {
	t.Helper()
	dir = newRepo(t)

	testutil.WriteFile(t, dir, "f.txt", "one\n")
	safegitCommitEnv(t, dir, inflightSession, "the first change", "f.txt")

	testutil.Git(t, dir, "branch", "side")
	testutil.Git(t, dir, "switch", "-q", "side")
	testutil.WriteFile(t, dir, "s.txt", "side\n")
	safegitCommitEnv(t, dir, inflightSession, "the side change", "s.txt")
	testutil.Git(t, dir, "switch", "-q", "main")

	testutil.WriteFile(t, dir, "f.txt", "one\ntwo\n")
	reverted = safegitCommitEnv(t, dir, inflightSession, "the second change", "f.txt")

	if _, stderr, code := runSafegitEnv(t, dir, inflightSession, "revert", reverted); code != 0 {
		t.Fatalf("the first revert failed (code %d): %s", code, stderr)
	}
	// The second revert has nothing to undo, so it stops -- and PARKS.
	if _, _, code := runSafegitEnv(t, dir, inflightSession, "revert", reverted); code == 0 {
		t.Fatalf("reverting an already-reverted commit succeeded; the fixture's premise is gone")
	}
	return dir, reverted
}

// newParkedPickRepo builds a repository that has picked a side commit, and
// whose SECOND pick of the same commit stopped with nothing to do and PARKED
// git's cherry-pick state over a clean working tree.
func newParkedPickRepo(t *testing.T) (dir, picked string) {
	t.Helper()
	dir, first, _ := newPickableRepo(t)

	if _, stderr, code := runSafegitEnv(t, dir, inflightSession, "cherry-pick", first); code != 0 {
		t.Fatalf("the first pick failed (code %d): %s", code, stderr)
	}
	if _, _, code := runSafegitEnv(t, dir, inflightSession, "cherry-pick", first); code == 0 {
		t.Fatalf("picking an already-applied commit succeeded; the fixture's premise is gone")
	}
	return dir, first
}

// TestANoChangeSecondPickParksItsState pins the fixture's premise rather than
// leaving it accidental: a pick whose change the branch already carries exits
// nonzero, leaves the working tree CLEAN, leaves git's pick state PARKED, and
// names git's own abort as the way out of it.
//
// The clean tree is what makes the state dangerous and is the reason the entry
// check cannot be the dirty-tree guard: nothing about this repository looks
// busy to anything that only asks `git diff HEAD`.
func TestANoChangeSecondPickParksItsState(t *testing.T) {
	dir, first, _ := newPickableRepo(t)

	if _, stderr, code := runSafegitEnv(t, dir, inflightSession, "cherry-pick", first); code != 0 {
		t.Fatalf("the first pick failed (code %d): %s", code, stderr)
	}
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, inflightSession, "cherry-pick", first)
	if code != exitcode.General {
		t.Fatalf("the no-change pick exited %d, want %d\nstdout=%s\nstderr=%s", code, exitcode.General, stdout, stderr)
	}
	if !strings.Contains(stderr, "git cherry-pick --abort") {
		t.Errorf("the no-change pick does not name git's abort as the way out:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the no-change pick moved HEAD to %s (was %s)", head, tip)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the no-change pick left the working tree dirty:\n%s", status)
	}
	if !stateFilePresent(t, dir, "CHERRY_PICK_HEAD") {
		t.Errorf("the no-change pick left no CHERRY_PICK_HEAD; the parked state is the premise of the refusal tests")
	}
}

// TestANoChangeSecondRevertParksItsState is the same pin on the revert side.
func TestANoChangeSecondRevertParksItsState(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "f.txt", "one\n")
	safegitCommitEnv(t, dir, inflightSession, "the first change", "f.txt")
	testutil.WriteFile(t, dir, "f.txt", "one\ntwo\n")
	reverted := safegitCommitEnv(t, dir, inflightSession, "the second change", "f.txt")

	if _, stderr, code := runSafegitEnv(t, dir, inflightSession, "revert", reverted); code != 0 {
		t.Fatalf("the first revert failed (code %d): %s", code, stderr)
	}
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, inflightSession, "revert", reverted)
	if code != exitcode.General {
		t.Fatalf("the no-change revert exited %d, want %d\nstdout=%s\nstderr=%s", code, exitcode.General, stdout, stderr)
	}
	if !strings.Contains(stderr, "git revert --abort") {
		t.Errorf("the no-change revert does not name git's abort as the way out:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the no-change revert moved HEAD to %s (was %s)", head, tip)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the no-change revert left the working tree dirty:\n%s", status)
	}
	if !stateFilePresent(t, dir, "REVERT_HEAD") {
		t.Errorf("the no-change revert left no REVERT_HEAD; the parked state is the premise of the refusal tests")
	}
}

// TestCherryPickRefusesOverAParkedRevert: a pick asked for while git holds a
// parked revert.
//
// Before the entry check this exited 0: git's own refusal never fired (the
// compute form is `--no-commit`, which git does not guard), safegit concluded
// the pick, committed it, and left REVERT_HEAD orphaned -- so the repository
// then refused every later `safegit commit` over a revert nobody was running.
func TestCherryPickRefusesOverAParkedRevert(t *testing.T) {
	dir, reverted := newParkedRevertRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, inflightSession, "cherry-pick", reverted)
	if code != exitcode.CoordinationBusy {
		t.Fatalf("a pick over a parked revert exited %d, want %d (CoordinationBusy)\nstdout=%s\nstderr=%s",
			code, exitcode.CoordinationBusy, stdout, stderr)
	}
	// The refusal names what is in flight -- the REVERT -- and the way out of
	// it, which is the way out safegit commit already names in this state.
	if !strings.Contains(stderr, "revert") {
		t.Errorf("the refusal does not name the revert in flight:\n%s", stderr)
	}
	if !strings.Contains(stderr, "safegit revert-continue") {
		t.Errorf("the refusal does not name the command that concludes the revert:\n%s", stderr)
	}
	if !strings.Contains(stderr, "git revert --abort") {
		t.Errorf("the refusal does not name the command that abandons the revert:\n%s", stderr)
	}

	// Nothing committed, nothing touched.
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the refused pick moved HEAD to %s (was %s)", head, tip)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the refused pick left something in the index or the working tree:\n%s", status)
	}
	if stateFilePresent(t, dir, "CHERRY_PICK_HEAD") {
		t.Errorf("the refused pick parked a cherry-pick of its own over somebody else's revert")
	}
	if !stateFilePresent(t, dir, "REVERT_HEAD") {
		t.Errorf("the refused pick removed the revert state it refused over")
	}
}

// TestCherryPickPreviewRefusesOverAParkedRevert: a preview of a command that
// cannot run is not a preview of anything, and the preview path computes the
// pick with git's own merge engine -- over the very state the refusal is about.
// So the check sits in front of the dry-run branch, not behind it.
func TestCherryPickPreviewRefusesOverAParkedRevert(t *testing.T) {
	dir, reverted := newParkedRevertRepo(t)

	// The quartet flag goes BEFORE the command name: after it, argv belongs to
	// the git-shaped parser and safegit's allowlist refuses it.
	stdout, stderr, code := runSafegitEnv(t, dir, inflightSession, "--dry-run", "cherry-pick", reverted)
	if code != exitcode.CoordinationBusy {
		t.Fatalf("a previewed pick over a parked revert exited %d, want %d (CoordinationBusy)\nstdout=%s\nstderr=%s",
			code, exitcode.CoordinationBusy, stdout, stderr)
	}
	if !strings.Contains(stderr, "safegit revert-continue") {
		t.Errorf("the previewed refusal does not name the way out:\n%s", stderr)
	}
}

// TestRevertRefusesOverAParkedPick: a revert asked for while git holds a parked
// cherry-pick.
//
// Before the entry check this exited 1 with a message that was not true of the
// state it found -- "git staged the revert but left no revert state behind",
// said over a repository that had BOTH CHERRY_PICK_HEAD and a REVERT_HEAD the
// compute step had just written -- and it left the inverse patch staged in the
// index.
func TestRevertRefusesOverAParkedPick(t *testing.T) {
	dir, _ := newParkedPickRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, inflightSession, "revert", "HEAD")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("a revert over a parked pick exited %d, want %d (CoordinationBusy)\nstdout=%s\nstderr=%s",
			code, exitcode.CoordinationBusy, stdout, stderr)
	}
	// The refusal names the operation ACTUALLY in flight, which is the pick --
	// not the revert the operator asked for.
	if !strings.Contains(stderr, "cherry-pick") {
		t.Errorf("the refusal does not name the cherry-pick in flight:\n%s", stderr)
	}
	if !strings.Contains(stderr, "safegit cherry-pick-continue") {
		t.Errorf("the refusal does not name the command that concludes the pick:\n%s", stderr)
	}

	// The index is untouched: no inverse patch staged, no REVERT_HEAD written.
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the refused revert moved HEAD to %s (was %s)", head, tip)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the refused revert left something staged:\n%s", status)
	}
	if stateFilePresent(t, dir, "REVERT_HEAD") {
		t.Errorf("the refused revert wrote REVERT_HEAD over somebody else's parked cherry-pick")
	}
	if !stateFilePresent(t, dir, "CHERRY_PICK_HEAD") {
		t.Errorf("the refused revert removed the pick state it refused over")
	}
}

// TestMergeRefusesOverAParkedPick is the same check on the merge arm.
//
// Merge's dirty-tree guard already catches the ordinary parked merge, because a
// parked merge dirties the tree by construction. It does not catch THIS: a
// parked pick over a clean tree looks like nothing at all to `git diff HEAD`,
// and merge's compute step would then run against it.
func TestMergeRefusesOverAParkedPick(t *testing.T) {
	dir, _ := newParkedPickRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, inflightSession, "merge", "side")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("a merge over a parked pick exited %d, want %d (CoordinationBusy)\nstdout=%s\nstderr=%s",
			code, exitcode.CoordinationBusy, stdout, stderr)
	}
	if !strings.Contains(stderr, "safegit cherry-pick-continue") {
		t.Errorf("the refusal does not name the command that concludes the pick:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the refused merge moved HEAD to %s (was %s)", head, tip)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the refused merge left something staged:\n%s", status)
	}
	if stateFilePresent(t, dir, "MERGE_HEAD") {
		t.Errorf("the refused merge parked a merge of its own over somebody else's cherry-pick")
	}
}

// TestMergeRefusesOverAParkedRevert is the merge case that actually committed,
// and the reason merge could not be left to git's own refusal.
//
// git's merge does refuse over a parked CHERRY-PICK -- "You have not concluded
// your cherry-pick (CHERRY_PICK_HEAD exists)", exit 128 -- which is why the
// parked-pick case above was never the dangerous one. It does NOT refuse over a
// parked REVERT: `git merge --no-ff --no-commit <branch>` there reports
// "Automatic merge went well; stopped before committing as requested" and exits
// 0 (probed). Without an entry check of its own, safegit's merge then concluded
// that staged result into a commit and left REVERT_HEAD orphaned -- the same
// damage the pick arm did, through a different door.
func TestMergeRefusesOverAParkedRevert(t *testing.T) {
	dir, _ := newParkedRevertRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, inflightSession, "merge", "side")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("a merge over a parked revert exited %d, want %d (CoordinationBusy)\nstdout=%s\nstderr=%s",
			code, exitcode.CoordinationBusy, stdout, stderr)
	}
	if !strings.Contains(stderr, "safegit revert-continue") {
		t.Errorf("the refusal does not name the command that concludes the revert:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the refused merge moved HEAD to %s (was %s)", head, tip)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the refused merge left something staged:\n%s", status)
	}
	if stateFilePresent(t, dir, "MERGE_HEAD") {
		t.Errorf("the refused merge parked a merge of its own over somebody else's revert")
	}
	if !stateFilePresent(t, dir, "REVERT_HEAD") {
		t.Errorf("the refused merge removed the revert state it refused over")
	}
}

// TestPullRefusesOverAParkedRevert covers the FOURTH compute door.
//
// `safegit pull` is a fetch followed by the same merge step `safegit merge`
// performs, and it reaches that step through its own handler rather than
// through merge's -- so merge's entry check does not cover it. Over a parked
// revert it had exactly the merge arm's defect: git computes the merge
// happily, safegit's pipeline commits it, and REVERT_HEAD is orphaned.
//
// The refusal is placed before the FETCH, not just before the merge step: a
// command that cannot merge should not go to the network first.
func TestPullRefusesOverAParkedRevert(t *testing.T) {
	dir := newDivergedFromRemoteRepo(t)

	// Park a revert over a clean tree, the same way the fixtures above do:
	// revert the local tip, then revert the same commit again, which has
	// nothing left to undo and stops with its state parked.
	target := testutil.Rev(t, dir, "HEAD")
	if _, stderr, code := runSafegitEnv(t, dir, inflightSession, "revert", target); code != 0 {
		t.Fatalf("the first revert failed (code %d): %s", code, stderr)
	}
	if _, _, code := runSafegitEnv(t, dir, inflightSession, "revert", target); code == 0 {
		t.Fatalf("reverting an already-reverted commit succeeded; the fixture's premise is gone")
	}
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, inflightSession,
		"pull", "--merge-strategy", "ff", "origin", "main")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("a pull over a parked revert exited %d, want %d (CoordinationBusy)\nstdout=%s\nstderr=%s",
			code, exitcode.CoordinationBusy, stdout, stderr)
	}
	if !strings.Contains(stderr, "safegit revert-continue") {
		t.Errorf("the refusal does not name the command that concludes the revert:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the refused pull moved HEAD to %s (was %s)", head, tip)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the refused pull left something staged:\n%s", status)
	}
	if !stateFilePresent(t, dir, "REVERT_HEAD") {
		t.Errorf("the refused pull removed the revert state it refused over")
	}
}

// TestTheWayOutStillWorksOverAParkedOperation is the other half of the ruling:
// the entry check is scoped to the COMPUTE forms, so the state-control verbs --
// the commands that exist to clear the very state being refused over -- still
// run. A check that covered them would strand the repository in the state it
// was protecting.
func TestTheWayOutStillWorksOverAParkedOperation(t *testing.T) {
	t.Run("cherry-pick --abort over a parked pick", func(t *testing.T) {
		dir, _ := newParkedPickRepo(t)
		if stdout, stderr, code := runSafegitEnv(t, dir, inflightSession, "cherry-pick", "--abort"); code != 0 {
			t.Fatalf("safegit cherry-pick --abort exited %d over the state it exists to clear\nstdout=%s\nstderr=%s",
				code, stdout, stderr)
		}
		assertNoSequencerResidue(t, dir, "cherry-pick --abort over a parked pick")
	})

	t.Run("revert --abort over a parked revert", func(t *testing.T) {
		dir, _ := newParkedRevertRepo(t)
		if stdout, stderr, code := runSafegitEnv(t, dir, inflightSession, "revert", "--abort"); code != 0 {
			t.Fatalf("safegit revert --abort exited %d over the state it exists to clear\nstdout=%s\nstderr=%s",
				code, stdout, stderr)
		}
		assertNoSequencerResidue(t, dir, "revert --abort over a parked revert")
	})

	t.Run("cherry-pick-continue over a parked pick", func(t *testing.T) {
		dir, _ := newParkedPickRepo(t)
		// The parked pick has nothing left to resolve and nothing left to
		// commit, so the conclusion refuses on its own terms -- but it must
		// reach that verdict rather than be refused at the door for the state
		// it is the conclusion of.
		_, stderr, _ := runSafegitEnv(t, dir, inflightSession, "cherry-pick-continue")
		if strings.Contains(stderr, "is in progress") {
			t.Errorf("cherry-pick-continue was refused over the operation it concludes:\n%s", stderr)
		}
	})
}
