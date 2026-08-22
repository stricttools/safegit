package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// safegit once kept one invariant about the shared .git/index: it equals HEAD.
// Every mutating path enforced it the same way, with a plain
// `git read-tree <treeish>` over the whole index -- which meant the invariant
// was enforced by destroying whatever the index held that HEAD did not:
// another session's staged work.
//
// It no longer holds. git.ReconcileMainIndex (internal/git/index_reconcile.go)
// is the single index-reconciliation authority: it snapshots the shared index's
// delta against the pre-operation tip, syncs, and replays that delta -- stage 0
// and the unmerged stages alike -- with every failure hard. undo goes through
// it, as do commit, amend and reword. The post-passthrough sync is gone
// entirely: during a passthrough git owns the shared index, and the sync
// repaired nothing while destroying the operation's own conflict stages.
//
// The tests here pin what that changed, on the paths nothing else exercises:
// undo's rollback, and the guarded passthroughs. All four are green; each was
// written against the defect it now forbids.

// undoSyncRead returns a working-tree file's content, or "" plus a fail if it
// is missing.
func undoSyncRead(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}

// undoSyncStagedStatus returns `git diff --cached --name-status` as lines: the
// staged-vs-HEAD delta, which is exactly the state a read-tree sync destroys.
func undoSyncStagedStatus(t *testing.T, dir string) []string {
	t.Helper()
	out := testutil.Git(t, dir, "diff", "--cached", "--name-status")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// undoSyncHasStagedPath reports whether the staged delta mentions path.
func undoSyncHasStagedPath(lines []string, path string) bool {
	for _, l := range lines {
		fields := strings.Fields(l)
		if len(fields) >= 2 && fields[len(fields)-1] == path {
			return true
		}
	}
	return false
}

// undoSyncUnmergedStages returns `git ls-files -u` output: the conflict stages
// git writes for an unresolved merge or cherry-pick. Empty means git no longer
// believes there is a conflict to resolve.
func undoSyncUnmergedStages(t *testing.T, dir string) string {
	t.Helper()
	return testutil.Git(t, dir, "ls-files", "-u")
}

// undoSyncSession is the session handshake for the session that owns the
// commits these tests undo.
func undoSyncSession() []string {
	return []string{"CLAUDE_CODE_SESSION_ID=undo-index-sync-session"}
}

// A second session's staged index state must survive `safegit undo`.
//
// Undo used to read-tree the rollback target over the whole shared index, so
// any path another session had staged and not yet committed -- a modification,
// a newly added file, a staged deletion -- was silently reverted to its HEAD
// state, with no warning, no record, and nothing in the oplog to recover from.
// It now reconciles through git.ReconcileMainIndex, which replays that delta.
//
// The staging below happens AFTER the undone commit deliberately: commit
// reconciles the index too, so staging first would leave a failure ambiguous;
// staging afterwards attributes it to undo alone.
func TestUndoPreservesForeignStagedState(t *testing.T) {
	dir := newRepo(t)
	env := undoSyncSession()

	// A tracked file for the foreign session to modify, plus the seed file it
	// will stage a deletion of.
	testutil.WriteFile(t, dir, "foreign.txt", "base\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "add foreign.txt", "--", "foreign.txt"); code != 0 {
		t.Fatalf("setup commit failed (code %d): %s", code, stderr)
	}

	// The commit this session will undo.
	testutil.WriteFile(t, dir, "owned.txt", "owned\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "owned commit", "--", "owned.txt"); code != 0 {
		t.Fatalf("owned commit failed (code %d): %s", code, stderr)
	}
	beforeUndo := testutil.Rev(t, dir, "HEAD")

	// The other session stages three kinds of work in the shared index.
	testutil.WriteFile(t, dir, "foreign.txt", "staged edit\n")
	testutil.WriteFile(t, dir, "brand-new.txt", "staged addition\n")
	testutil.Git(t, dir, "add", "foreign.txt", "brand-new.txt")
	testutil.Git(t, dir, "rm", "--cached", "seed.txt")

	staged := undoSyncStagedStatus(t, dir)
	for _, want := range []string{"foreign.txt", "brand-new.txt", "seed.txt"} {
		if !undoSyncHasStagedPath(staged, want) {
			t.Fatalf("fixture: %s not staged before undo (staged: %v)", want, staged)
		}
	}

	_, stderr, code := runSafegitEnv(t, dir, env, "undo")
	if code != 0 {
		t.Fatalf("undo failed (code %d): %s", code, stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head == beforeUndo {
		t.Fatalf("undo exited 0 but HEAD did not move from %s", beforeUndo)
	}

	after := undoSyncStagedStatus(t, dir)
	var lost []string
	for _, want := range []string{"foreign.txt", "brand-new.txt", "seed.txt"} {
		if !undoSyncHasStagedPath(after, want) {
			lost = append(lost, want)
		}
	}
	if len(lost) > 0 {
		t.Fatalf("`safegit undo` destroyed another session's staged index state.\n"+
			"  lost from the index: %s\n"+
			"  staged before undo: %v\n"+
			"  staged after undo:  %v\n"+
			"  git status: %s\n"+
			"  cause: undo reconciles the shared index through git.ReconcileMainIndex\n"+
			"         (internal/git/index_reconcile.go); a delta it fails to replay is lost work.",
			strings.Join(lost, ", "), staged, after,
			oneLine(testutil.Git(t, dir, "status", "--porcelain")))
	}
}

// `safegit undo` must refuse while a merge is in progress.
//
// Mid-merge, MERGE_HEAD names the second parent and the index carries the
// merge's staged result plus its unresolved conflict stages. Undoing the
// pre-merge commit is incoherent on its face -- the merge in flight was
// computed against a commit that would no longer be HEAD -- and undo had no
// guard for it: nothing in undo read MERGE_HEAD, CHERRY_PICK_HEAD, REVERT_HEAD
// or .git/rebase-merge.
//
// What used to happen was worse than an incoherent HEAD: the index
// reconciliation erased the conflict stages, so git stopped reporting a
// conflict at all -- while MERGE_HEAD survived. The repository was left looking
// clean and mid-merge at once, and concluding it produced a commit built from
// the wrong tree. Verified by hand: after that sequence a plain `git commit`
// succeeded and wrote a commit whose tree had lost every path the merge brought
// in.
//
// undo now refuses outright, through coord.GuardInFlight.
func TestUndoRefusedMidMerge(t *testing.T) {
	env := undoSyncSession()
	// The conflict is deliberately left unresolved: the stages git wrote are
	// exactly what undo's read-tree erases, so they must still be in the index.
	// "main edit" -- the commit the fixture leaves at HEAD -- is the one undo
	// would roll back.
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: env, cleanSideFile: true})
	dir, mainSHA := fx.dir, fx.mainSHA

	mergeHeadPath := filepath.Join(dir, ".git", "MERGE_HEAD")
	mergeHead, err := os.ReadFile(mergeHeadPath)
	if err != nil {
		t.Fatalf("fixture: reading .git/MERGE_HEAD: %v", err)
	}
	stagesBefore := undoSyncUnmergedStages(t, dir)
	if stagesBefore == "" {
		t.Fatal("fixture: no unmerged index stages after a conflicted merge")
	}

	stdout, stderr, code := runSafegitEnv(t, dir, env, "undo")

	if code == 0 {
		t.Fatalf("`safegit undo` ran to completion mid-merge instead of refusing.\n"+
			"  HEAD: %s -> %s (rolled back past the commit the in-flight merge was computed against)\n"+
			"  .git/MERGE_HEAD survives: %t\n"+
			"  unmerged index stages after undo: %q (were %d line(s); empty means git no longer\n"+
			"    reports the conflict, because the rollback's index reconciliation ran over them)\n"+
			"  git status: %s\n"+
			"  stdout: %s",
			mainSHA, testutil.Rev(t, dir, "HEAD"),
			!testutil.MergeStateGone(t, dir),
			undoSyncUnmergedStages(t, dir), len(strings.Split(stagesBefore, "\n")),
			oneLine(testutil.Git(t, dir, "status", "--porcelain")),
			oneLine(stdout))
	}

	// A refusal must leave the merge exactly as it found it.
	if head := testutil.Rev(t, dir, "HEAD"); head != mainSHA {
		t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, mainSHA)
	}
	if got, rerr := os.ReadFile(mergeHeadPath); rerr != nil || strings.TrimSpace(string(got)) != strings.TrimSpace(string(mergeHead)) {
		t.Errorf("MERGE_HEAD changed or vanished after the refusal (err=%v, got=%q, want=%q)",
			rerr, strings.TrimSpace(string(got)), strings.TrimSpace(string(mergeHead)))
	}
	if got := undoSyncUnmergedStages(t, dir); got != stagesBefore {
		t.Errorf("unmerged index stages changed despite the refusal:\n  before: %q\n  after:  %q", stagesBefore, got)
	}
	if !strings.Contains(strings.ToLower(stderr), "merge") {
		t.Errorf("refusal does not name the merge, so the operator cannot act on it: %s", oneLine(stderr))
	}
	// The refusal is a coordination refusal, and the registry gives that its
	// own code so a caller can tell "an operation owns this tree" from every
	// other reason undo could fail.
	if code != exitcode.CoordinationBusy {
		t.Errorf("the refusal exited %d, want %d (CoordinationBusy): %s",
			code, exitcode.CoordinationBusy, oneLine(stderr))
	}
}

// GREEN pin: `safegit undo` must not touch the working tree.
//
// undo reconciles the index without touching the working tree, so the undone
// commit's content stays on disk: a file the commit added remains as an
// untracked file, and a file the commit modified remains modified relative to
// the restored HEAD. That is the correct contract for undo -- it reverses the
// commit, not the work. Reconciling with a worktree-updating variant
// (git.SyncMainIndexWithWorktree) would silently discard the operator's edits,
// so this test exists to make that regression loud.
func TestUndoLeavesWorkingTreeIntact(t *testing.T) {
	dir := newRepo(t)
	env := undoSyncSession()

	testutil.WriteFile(t, dir, "tracked.txt", "v1\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "add tracked.txt", "--", "tracked.txt"); code != 0 {
		t.Fatalf("setup commit failed (code %d): %s", code, stderr)
	}
	beforeCommit := testutil.Rev(t, dir, "HEAD")

	// One commit that both modifies a tracked file and adds a new one.
	testutil.WriteFile(t, dir, "tracked.txt", "v2\n")
	testutil.WriteFile(t, dir, "added.txt", "added by the undone commit\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "the commit to undo", "--", "tracked.txt", "added.txt"); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	if _, stderr, code := runSafegitEnv(t, dir, env, "undo"); code != 0 {
		t.Fatalf("undo failed (code %d): %s", code, stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != beforeCommit {
		t.Fatalf("after undo HEAD = %s, want %s", head, beforeCommit)
	}

	if got := undoSyncRead(t, dir, "tracked.txt"); got != "v2\n" {
		t.Errorf("undo reverted the working tree: tracked.txt = %q, want %q (the undone commit's content must stay on disk)", got, "v2\n")
	}
	if got := undoSyncRead(t, dir, "added.txt"); got != "added by the undone commit\n" {
		t.Errorf("added.txt content changed after undo: %q", got)
	}

	status := testutil.Git(t, dir, "status", "--porcelain")
	if !strings.Contains(status, "tracked.txt") {
		t.Errorf("tracked.txt is not reported as modified after undo; status: %s", oneLine(status))
	}
	if !strings.Contains(status, "?? added.txt") {
		t.Errorf("added.txt is not reported as untracked after undo; status: %s", oneLine(status))
	}
}

// The same read-tree sync used to run after every guarded passthrough, where it
// destroyed state belonging to the operation safegit had just run. It is
// deleted: during a passthrough git owns the shared index.
//
// A conflicted `git cherry-pick` leaves unmerged stages in the index; that is
// how git records which paths still need resolving and how `git cherry-pick
// --continue` knows the conflict was addressed. safegit used to run
// cherry-pick and then read-tree HEAD over the index, erasing every stage: git
// stopped reporting `UU`, `git ls-files -u` came back empty, and
// CHERRY_PICK_HEAD was still there.
//
// This test lives in this file because it was the same defect at another call
// site: an index sync that assumes the index may be rebuilt from HEAD.
func TestGuardedPassthroughKeepsCherryPickConflictStages(t *testing.T) {
	dir := newRepo(t)
	env := undoSyncSession()

	testutil.WriteFile(t, dir, "c.txt", "base\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "base", "--", "c.txt"); code != 0 {
		t.Fatalf("base commit failed (code %d): %s", code, stderr)
	}
	testutil.Git(t, dir, "branch", "side")
	testutil.Git(t, dir, "switch", "side")
	testutil.WriteFile(t, dir, "c.txt", "side\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "side edit", "--", "c.txt"); code != 0 {
		t.Fatalf("side commit failed (code %d): %s", code, stderr)
	}
	sideSHA := testutil.Rev(t, dir, "HEAD")

	testutil.Git(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "c.txt", "trunk\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "trunk edit", "--", "c.txt"); code != 0 {
		t.Fatalf("trunk commit failed (code %d): %s", code, stderr)
	}

	if _, _, code := runSafegitEnv(t, dir, env, "cherry-pick", sideSHA); code == 0 {
		t.Fatal("fixture: safegit cherry-pick succeeded; a conflict is required")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "CHERRY_PICK_HEAD")); err != nil {
		t.Fatalf("fixture: CHERRY_PICK_HEAD missing after a conflicted cherry-pick: %v", err)
	}

	stages := undoSyncUnmergedStages(t, dir)
	if stages == "" {
		statusOut, _ := testutil.GitTry(t, dir, "status", "--porcelain")
		t.Fatalf("`safegit cherry-pick` erased the conflict stages git had just written.\n"+
			"  git ls-files -u: empty (raw `git cherry-pick` leaves three stages for c.txt)\n"+
			"  git status: %s (raw git reports `UU c.txt`)\n"+
			"  CHERRY_PICK_HEAD is still present, so the repository claims a cherry-pick is in\n"+
			"    flight while the index no longer records anything to resolve.\n"+
			"  cause: something read-tree'd HEAD over the shared index after the passthrough.\n"+
			"         During a passthrough git owns that index; safegit must not touch it.",
			oneLine(statusOut))
	}
}
