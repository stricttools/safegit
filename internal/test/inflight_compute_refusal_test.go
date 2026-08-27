package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// The commands that COMPUTE an operation -- merge, cherry-pick, revert, and
// pull, whose merge step is merge's -- run `git <verb> --no-commit` and then
// commit the staged result through safegit's own pipeline. That compute door is
// the reason each needs an in-flight check of its own.
//
// Raw git refuses to start one operation over another: `git cherry-pick <c>`
// over a parked revert exits 128 with "a revert is already in progress". The
// `--no-commit` form does not inherit that refusal uniformly, and the gaps are
// probe-verified rather than assumed:
//
//   - `git cherry-pick --no-commit` over a parked revert applies cleanly;
//   - `git revert --no-commit` over a parked cherry-pick stages its inverse
//     patch and writes REVERT_HEAD beside the pick's own state file;
//   - `git merge --no-ff --no-commit` over a parked revert reports "Automatic
//     merge went well" and exits 0 -- though over a parked cherry-pick it does
//     refuse, which is why the merge arm's dangerous case is the revert one.
//
// What each gap produced, before the check existed: a pick, a merge or a pull
// over a parked revert COMMITTED and left REVERT_HEAD orphaned in the git
// directory for the next `safegit commit` to refuse over; a revert over a
// parked pick staged an inverse patch it never committed and reported a
// diagnosis that was not true of the state it found.
//
// The fix is the check every other author already makes: coord.GuardInFlight at
// ENTRY, before the compute -- and for pull before its fetch -- exit 5,
// rendering the way out from the single way-out authority, which is the same
// refusal `safegit commit` produces in exactly this state.
//
// The state-control forms (--abort, --quit) and the -continue commands are NOT
// covered by it: they are the way out, and a way out that refused over the
// state it exists to clear would strand the repository.
//
// `safegit rebase` belongs to the same refusal class without being a compute
// door: git replays and authors there, but the replay runs over whatever state
// it finds, and over a parked revert on a clean tree it exits 0 and strands that
// revert's state files. Its refusal is KIND-SCOPED -- an in-flight state that is
// not a rebase -- so a mid-rebase `--continue`/`--abort`/`--skip` passes by
// construction rather than by an argv exemption list. Its two pins are
// TestRebaseRefusesOverAParkedNonRebaseOperation and
// TestRebaseStillRunsWithNothingInFlight, below.

var inflightSession = []string{"CLAUDE_CODE_SESSION_ID=inflight-compute-refusal-test"}

// stateFilePresent reports whether one of git's operation state files exists.
func stateFilePresent(t *testing.T, dir, name string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(dir, ".git", name))
	return err == nil
}

// THE PARK IS RAW GIT'S, in both fixture builders below, and that is a
// requirement rather than a preference. safegit's own compute no longer leaves
// a no-change operation parked: when the result changes nothing it removes the
// state it parked a moment earlier, which is what the two cleaned-up pins below
// assert. A fixture that parked through safegit would therefore build nothing
// at all.
//
// Raw git writes the state these tests need, probe-verified on both sides:
// `git cherry-pick <already-applied>` stops at exit 1 with CHERRY_PICK_HEAD,
// MERGE_MSG and AUTO_MERGE beside a clean working tree, and `git revert
// --no-commit <already-reverted>` exits 0 leaving REVERT_HEAD, MERGE_MSG and
// AUTO_MERGE beside one. Neither creates a `.git/sequencer` directory, so
// neither is the queued shape safegit refuses to conclude.

// newParkedRevertRepo builds a repository whose HEAD commit has been reverted,
// and over which RAW GIT has then parked a second revert of the same commit --
// one with nothing left to undo, over a clean working tree.
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
	// The second revert has nothing to undo, so raw git stages nothing, exits 0
	// and leaves its state parked.
	if out, code := testutil.GitTry(t, dir, "revert", "--no-commit", reverted); code != 0 {
		t.Fatalf("the raw-git revert park failed (code %d): %s", code, out)
	}
	if !stateFilePresent(t, dir, "REVERT_HEAD") {
		t.Fatalf("the raw-git revert parked no REVERT_HEAD; the fixture's premise is gone")
	}
	return dir, reverted
}

// newParkedPickRepo builds a repository that has picked a side commit, and over
// which RAW GIT has then parked a second pick of the same commit -- one with
// nothing left to apply, over a clean working tree.
func newParkedPickRepo(t *testing.T) (dir, picked string) {
	t.Helper()
	dir, first, _ := newPickableRepo(t)

	if _, stderr, code := runSafegitEnv(t, dir, inflightSession, "cherry-pick", first); code != 0 {
		t.Fatalf("the first pick failed (code %d): %s", code, stderr)
	}
	// The second pick has nothing to apply, so raw git stops with its state
	// parked and exits nonzero.
	if out, code := testutil.GitTry(t, dir, "cherry-pick", first); code == 0 {
		t.Fatalf("the raw-git pick of an already-applied commit succeeded: %s", out)
	}
	if !stateFilePresent(t, dir, "CHERRY_PICK_HEAD") {
		t.Fatalf("the raw-git pick parked no CHERRY_PICK_HEAD; the fixture's premise is gone")
	}
	return dir, first
}

// TestANoChangeSecondPickCleansItsState: a pick whose change the branch already
// carries produces no commit, and safegit REMOVES the state its own compute
// step parked a moment earlier.
//
// The state was safegit's own: it ran `git cherry-pick --no-commit` and wrote
// CHERRY_PICK_HEAD itself, then found nothing to commit. Leaving that behind
// made a repository nobody was operating in refuse every later `safegit commit`
// until somebody deleted the files by hand, and the refusal it printed advised
// an abort of an operation the operator had never chosen to start. So the
// refusal stands -- nothing is committed, the exit code is unchanged -- and the
// park does not.
//
// The last assertion is the point of the whole ruling: the next commit just
// works.
func TestANoChangeSecondPickCleansItsState(t *testing.T) {
	dir, first, _ := newPickableRepo(t)

	if _, stderr, code := runSafegitEnv(t, dir, inflightSession, "cherry-pick", first); code != 0 {
		t.Fatalf("the first pick failed (code %d): %s", code, stderr)
	}
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, inflightSession, "cherry-pick", first)
	if code != exitcode.General {
		t.Fatalf("the no-change pick exited %d, want %d\nstdout=%s\nstderr=%s", code, exitcode.General, stdout, stderr)
	}
	if !strings.Contains(stderr, "no change") {
		t.Errorf("the refusal does not say the pick produces no change:\n%s", stderr)
	}
	if !strings.Contains(stderr, "has been cleaned up") {
		t.Errorf("the refusal does not say the parked state was cleaned up:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the no-change pick moved HEAD to %s (was %s)", head, tip)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the no-change pick left the working tree dirty:\n%s", status)
	}
	assertNoSequencerResidue(t, dir, "a no-change cherry-pick")

	testutil.WriteFile(t, dir, "after.txt", "after\n")
	safegitCommitEnv(t, dir, inflightSession, "the commit after the no-change pick", "after.txt")
}

// TestANoChangeSecondRevertCleansItsState is the same pin on the revert side.
func TestANoChangeSecondRevertCleansItsState(t *testing.T) {
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
	if !strings.Contains(stderr, "no change") {
		t.Errorf("the refusal does not say the revert produces no change:\n%s", stderr)
	}
	if !strings.Contains(stderr, "has been cleaned up") {
		t.Errorf("the refusal does not say the parked state was cleaned up:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the no-change revert moved HEAD to %s (was %s)", head, tip)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the no-change revert left the working tree dirty:\n%s", status)
	}
	assertNoSequencerResidue(t, dir, "a no-change revert")

	testutil.WriteFile(t, dir, "after.txt", "after\n")
	safegitCommitEnv(t, dir, inflightSession, "the commit after the no-change revert", "after.txt")
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
	// revert the local tip, then have RAW GIT revert the same commit again --
	// nothing left to undo, and its state parked. (Raw git for the same reason
	// the shared builders use it: safegit's own no-change revert cleans up after
	// itself and parks nothing.)
	target := testutil.Rev(t, dir, "HEAD")
	if _, stderr, code := runSafegitEnv(t, dir, inflightSession, "revert", target); code != 0 {
		t.Fatalf("the first revert failed (code %d): %s", code, stderr)
	}
	if out, code := testutil.GitTry(t, dir, "revert", "--no-commit", target); code != 0 {
		t.Fatalf("the raw-git revert park failed (code %d): %s", code, out)
	}
	if !stateFilePresent(t, dir, "REVERT_HEAD") {
		t.Fatalf("the raw-git revert parked no REVERT_HEAD; the fixture's premise is gone")
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

// TestCherryPickNoCommitRefusesOverAParkedRevert covers the FIRST of the two
// holes the restructure left open: `--no-commit` is forwarded to git, and the
// forwarded form computes too.
//
// It exited 0 before the check reached it: `git cherry-pick --no-commit` over a
// parked revert applies cleanly, and safegit wrote no CHERRY_PICK_HEAD for it
// (that file is the restructured pick's own addition, not the passthrough's).
// The pick's files then sat in the index of somebody else's revert, and a later
// `safegit revert-continue` would have committed them into the revert commit.
func TestCherryPickNoCommitRefusesOverAParkedRevert(t *testing.T) {
	dir, _ := newParkedRevertRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, inflightSession, "cherry-pick", "--no-commit", "side")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("cherry-pick --no-commit over a parked revert exited %d, want %d (CoordinationBusy)\nstdout=%s\nstderr=%s",
			code, exitcode.CoordinationBusy, stdout, stderr)
	}
	if !strings.Contains(stderr, "revert") {
		t.Errorf("the refusal does not name the revert in flight:\n%s", stderr)
	}
	if !strings.Contains(stderr, "safegit revert-continue") {
		t.Errorf("the refusal does not name the command that concludes the revert:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the refused pick moved HEAD to %s (was %s)", head, tip)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the refused pick staged something:\n%s", status)
	}
	if stateFilePresent(t, dir, "CHERRY_PICK_HEAD") {
		t.Errorf("the refused pick wrote a state file of its own over somebody else's revert")
	}
	if !stateFilePresent(t, dir, "REVERT_HEAD") {
		t.Errorf("the refused pick removed the revert state it refused over")
	}
}

// TestCherryPickNoCommitPreviewRefusesOverAParkedRevert: the check sits in front
// of the passthrough's dry-run branch, for the same reason it sits in front of
// the restructured command's -- a preview computes the operation with git's own
// merge engine, over the very state being refused over.
func TestCherryPickNoCommitPreviewRefusesOverAParkedRevert(t *testing.T) {
	dir, _ := newParkedRevertRepo(t)

	stdout, stderr, code := runSafegitEnv(t, dir, inflightSession, "--dry-run", "cherry-pick", "--no-commit", "side")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("a previewed cherry-pick --no-commit over a parked revert exited %d, want %d (CoordinationBusy)\nstdout=%s\nstderr=%s",
			code, exitcode.CoordinationBusy, stdout, stderr)
	}
	if !strings.Contains(stderr, "safegit revert-continue") {
		t.Errorf("the previewed refusal does not name the way out:\n%s", stderr)
	}
}

// TestRevertNoCommitRefusesOverAParkedPick covers the SECOND hole: over a parked
// cherry-pick, `git revert --no-commit` staged its inverse patch AND wrote
// REVERT_HEAD beside the pick's own state file, leaving TWO operations in flight
// at once.
func TestRevertNoCommitRefusesOverAParkedPick(t *testing.T) {
	dir, _ := newParkedPickRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, inflightSession, "revert", "--no-commit", "HEAD")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("revert --no-commit over a parked pick exited %d, want %d (CoordinationBusy)\nstdout=%s\nstderr=%s",
			code, exitcode.CoordinationBusy, stdout, stderr)
	}
	if !strings.Contains(stderr, "cherry-pick") {
		t.Errorf("the refusal does not name the cherry-pick in flight:\n%s", stderr)
	}
	if !strings.Contains(stderr, "safegit cherry-pick-continue") {
		t.Errorf("the refusal does not name the command that concludes the pick:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the refused revert moved HEAD to %s (was %s)", head, tip)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the refused revert staged something:\n%s", status)
	}
	if stateFilePresent(t, dir, "REVERT_HEAD") {
		t.Errorf("the refused revert wrote REVERT_HEAD over somebody else's parked cherry-pick")
	}
	if !stateFilePresent(t, dir, "CHERRY_PICK_HEAD") {
		t.Errorf("the refused revert removed the pick state it refused over")
	}
}

// TestMergeNoCommitRefusesOverAParkedRevert is the merge arm's `--no-commit`
// form, which needs no wrapper of its own: `safegit merge --no-commit` is
// safegit's own restructured merge parking deliberately, so it reaches the
// restructured command's entry check rather than a passthrough. The pin exists
// because the merge arm's other pins exercise only the PLAIN form, and the
// enumeration is what this subphase changed.
func TestMergeNoCommitRefusesOverAParkedRevert(t *testing.T) {
	dir, _ := newParkedRevertRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, inflightSession, "merge", "--no-commit", "side")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("merge --no-commit over a parked revert exited %d, want %d (CoordinationBusy)\nstdout=%s\nstderr=%s",
			code, exitcode.CoordinationBusy, stdout, stderr)
	}
	if !strings.Contains(stderr, "safegit revert-continue") {
		t.Errorf("the refusal does not name the command that concludes the revert:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the refused merge moved HEAD to %s (was %s)", head, tip)
	}
	if stateFilePresent(t, dir, "MERGE_HEAD") {
		t.Errorf("the refused merge parked a merge of its own over somebody else's revert")
	}
}

// TestRebaseRefusesOverAParkedNonRebaseOperation: a rebase is not a compute
// door, but it is a git operation that runs over whatever state it finds -- and
// over a parked revert on a clean tree it exited 0 and STRANDED the revert's
// state files behind it, so every later `safegit commit` refused over a revert
// nobody was running.
//
// The predicate is KIND-SCOPED rather than "anything in flight": mid-rebase
// state reports the rebase kind, so `rebase --continue`/`--abort`/`--skip` pass
// by construction and need no exemption list of their own.
func TestRebaseRefusesOverAParkedNonRebaseOperation(t *testing.T) {
	dir, _ := newParkedRevertRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, inflightSession, "rebase", "side")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("a rebase over a parked revert exited %d, want %d (CoordinationBusy)\nstdout=%s\nstderr=%s",
			code, exitcode.CoordinationBusy, stdout, stderr)
	}
	if !strings.Contains(stderr, "revert") {
		t.Errorf("the refusal does not name the revert in flight:\n%s", stderr)
	}
	if !strings.Contains(stderr, "safegit revert-continue") {
		t.Errorf("the refusal does not name the command that concludes the revert:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("the refused rebase moved HEAD to %s (was %s)", head, tip)
	}
	if !stateFilePresent(t, dir, "REVERT_HEAD") {
		t.Errorf("the refused rebase removed the revert state it refused over")
	}

	// A preview refuses identically: the refusal is before git runs, so there is
	// nothing to preview.
	if _, stderr, code := runSafegitEnv(t, dir, inflightSession, "--dry-run", "rebase", "side"); code != exitcode.CoordinationBusy {
		t.Errorf("a previewed rebase over a parked revert exited %d, want %d\n%s",
			code, exitcode.CoordinationBusy, stderr)
	}
}

// TestRebaseStillRunsWithNothingInFlight is the control for the refusal above:
// the kind-scoped predicate must not touch an ordinary rebase.
func TestRebaseStillRunsWithNothingInFlight(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "base.txt", "base\n")
	safegitCommitEnv(t, dir, inflightSession, "the base", "base.txt")
	testutil.Git(t, dir, "branch", "topic")
	testutil.WriteFile(t, dir, "main.txt", "main\n")
	safegitCommitEnv(t, dir, inflightSession, "the main change", "main.txt")
	testutil.Git(t, dir, "switch", "-q", "topic")
	testutil.WriteFile(t, dir, "topic.txt", "topic\n")
	safegitCommitEnv(t, dir, inflightSession, "the topic change", "topic.txt")

	if stdout, stderr, code := runSafegitEnv(t, dir, inflightSession, "rebase", "main"); code != 0 {
		t.Fatalf("an ordinary rebase exited %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if !testutil.FileExists(filepath.Join(dir, "main.txt")) {
		t.Errorf("the rebase did not replay topic onto main")
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

// TestTheWayOutOfAConflictedParkedOperation is the shape an operator actually
// meets, and it answers differently from the empty parked states above -- which
// is the whole reason it is pinned separately.
//
// The states above are parked with NOTHING to resolve: raw git stopped over a
// clean working tree, so safegit's own `--abort` gets past the dirty-tree check
// and clears the state. A CONFLICTED park is the ordinary case, and there the
// conflict markers and the staged result ARE the dirt, so the dirty-tree check
// refuses every guarded command line over it -- `safegit cherry-pick --abort`
// included, at exit 5.
//
// That is not the check failing to make an exception. The refusal changes SHAPE
// instead of relaxing: it names the operation in flight, the safegit command
// that CONCLUDES it, and git's own `--abort` for abandoning it. Abandoning is
// git's command in practice, and this refusal is where safegit says so -- the
// same position docs/commands-guide.md states in "The guarded commands and
// their two coordination layers".
//
// So the way out of a conflicted park is: conclude it with safegit, or abandon
// it with git. This test walks the abandoning half end to end, because that is
// the leg no other test covers: the refusal, the git abort it names, and the
// property that makes the whole thing worth anything -- the NEXT safegit commit
// just works.
func TestTheWayOutOfAConflictedParkedOperation(t *testing.T) {
	for _, verb := range []string{"cherry-pick", "revert"} {
		t.Run(verb, func(t *testing.T) {
			fx := newConflictedPickRepo(t, verb)

			// The compute forms refuse over it, which is the state-control
			// forms' counterpart and the reason the two are worth telling apart.
			_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession, "revert", "--no-edit", "HEAD")
			if code != exitcode.CoordinationBusy {
				t.Fatalf("a compute form over a conflicted parked %s exited %d, want %d (CoordinationBusy)\n%s",
					verb, code, exitcode.CoordinationBusy, stderr)
			}
			if !strings.Contains(stderr, "safegit "+verb+"-continue") {
				t.Errorf("the refusal does not name the command that concludes the %s:\n%s", verb, stderr)
			}

			// safegit's own state-control form is refused too, and by the DIRTY
			// layer rather than the in-flight one: a conflicted park is dirt.
			// The refusal names git's abort, which is the way out it has.
			_, stderr, code = runSafegitEnv(t, fx.dir, conclusionSession, verb, "--abort")
			if code != exitcode.CoordinationBusy {
				t.Fatalf("safegit %s --abort over a conflicted park exited %d, want %d (CoordinationBusy)\n%s",
					verb, code, exitcode.CoordinationBusy, stderr)
			}
			if !strings.Contains(stderr, "git "+verb+" --abort") {
				t.Errorf("the refusal does not name git's own abort, which is the way out here:\n%s", stderr)
			}
			if !stateFilePresent(t, fx.dir, stateFileFor(verb)) {
				t.Fatalf("the refused abort removed the %s state it refused over", verb)
			}

			// The way out the refusal named, taken.
			if out, code := testutil.GitTry(t, fx.dir, verb, "--abort"); code != 0 {
				t.Fatalf("git %s --abort failed (code %d): %s", verb, code, out)
			}
			assertNoSequencerResidue(t, fx.dir, "git "+verb+" --abort over a conflicted park")
			if head := testutil.Rev(t, fx.dir, "HEAD"); head != fx.tip {
				t.Errorf("the abort left HEAD at %s, want the pre-operation tip %s", head, fx.tip)
			}
			if status := testutil.Git(t, fx.dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
				t.Errorf("the abort left the working tree dirty:\n%s", status)
			}

			// The point of the whole walk.
			testutil.WriteFile(t, fx.dir, "after.txt", "after\n")
			safegitCommitEnv(t, fx.dir, conclusionSession, "the commit after the abort", "after.txt")
		})
	}
}

// stateFileFor names the state file git writes for a single cherry-pick or
// revert, which is the file an abort has to remove.
func stateFileFor(verb string) string {
	if verb == "revert" {
		return "REVERT_HEAD"
	}
	return "CHERRY_PICK_HEAD"
}
