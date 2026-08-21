package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// safegit keeps one invariant about the shared .git/index: it equals HEAD.
// Every mutating path enforces it the same way -- git.SyncMainIndex, which is
// `git read-tree <treeish>` (internal/git/git.go:265-300). read-tree overwrites
// the whole index, so the invariant is enforced by destroying whatever the
// index held that HEAD does not: another session's staged work.
//
// `safegit commit` is the known instance (internal/commit/commit.go:335). The
// tests here cover the paths that nothing else exercises:
//
//   - undo.go:207   -- `safegit undo` syncs to the rollback target
//   - coord_cmd.go:50 -- the guarded passthroughs' sync helper, reached from
//     runGuardedPassthrough (coord_cmd.go:373) after cherry-pick and revert
//
// Two of the three tests below are RED on purpose: they assert what safegit
// must do, and fail against today's binary. The third is a GREEN pin recording
// behavior that is correct and must not regress.

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

// undoSyncHead returns the repo's HEAD SHA.
func undoSyncHead(t *testing.T, dir string) string {
	t.Helper()
	return testutil.Git(t, dir, "rev-parse", "HEAD")
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
// undo.go:207 calls git.SyncMainIndex with the rollback target, which is
// `git read-tree <target>`: the whole shared index is replaced by that tree.
// Any path another session had staged and not yet committed -- a modification,
// a newly added file, a staged deletion -- is silently reverted to its HEAD
// state, with no warning, no record, and nothing in the oplog to recover from.
//
// The staging below happens AFTER the undone commit deliberately. `safegit
// commit` performs the same destruction (internal/commit/commit.go:335), so
// staging first would leave the failure ambiguous; staging afterwards
// attributes it to undo alone.
//
// RED today: undo exits 0 and the staged delta comes back empty.
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
	beforeUndo := undoSyncHead(t, dir)

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
	if head := undoSyncHead(t, dir); head == beforeUndo {
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
			"  cause: undo.go:207 calls git.SyncMainIndex (read-tree, internal/git/git.go:265-300),\n"+
			"         which replaces the whole shared index with the rollback target's tree.",
			strings.Join(lost, ", "), staged, after,
			oneLine(testutil.Git(t, dir, "status", "--porcelain")))
	}
}

// `safegit undo` must refuse while a merge is in progress.
//
// Mid-merge, MERGE_HEAD names the second parent and the index carries the
// merge's staged result plus its unresolved conflict stages. Undoing the
// pre-merge commit is incoherent on its face -- the merge in flight was
// computed against a commit that would no longer be HEAD -- and undo.go has no
// guard for it: there is no coord.Check call anywhere in undo.go, and nothing
// reads MERGE_HEAD, CHERRY_PICK_HEAD, REVERT_HEAD or .git/rebase-merge.
//
// What happens today is worse than an incoherent HEAD. undo.go:207 read-trees
// the rollback target over the index, which erases the conflict stages, so git
// stops reporting a conflict at all -- while MERGE_HEAD survives. The
// repository is left looking clean and mid-merge at once, and concluding it
// produces a commit built from the wrong tree. Verified by hand: after this
// sequence a plain `git commit` succeeds and writes a commit whose tree has
// lost every path the merge brought in.
//
// RED today: undo exits 0, HEAD moves, and the stages are gone.
func TestUndoRefusedMidMerge(t *testing.T) {
	dir := newRepo(t)
	env := undoSyncSession()

	// Base revision of the conflicted file.
	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nbase\nline3\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "base", "--", "conflicted.txt"); code != 0 {
		t.Fatalf("base commit failed (code %d): %s", code, stderr)
	}

	// feature: a conflicting edit plus one clean addition.
	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "feature")
	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nfeature\nline3\n")
	testutil.WriteFile(t, dir, "feature-only.txt", "only on feature\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "feature edit", "--", "conflicted.txt", "feature-only.txt"); code != 0 {
		t.Fatalf("feature commit failed (code %d): %s", code, stderr)
	}

	// main: the conflicting edit. This is the commit undo would roll back.
	testutil.Git(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nmain\nline3\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "main edit", "--", "conflicted.txt"); code != 0 {
		t.Fatalf("main commit failed (code %d): %s", code, stderr)
	}
	mainSHA := undoSyncHead(t, dir)

	if _, _, code := runSafegitEnv(t, dir, env, "merge", "feature"); code == 0 {
		t.Fatalf("fixture: safegit merge feature succeeded; a conflict is required")
	}
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
			"    reports the conflict, because undo.go:207 read-tree'd over them)\n"+
			"  git status: %s\n"+
			"  stdout: %s",
			mainSHA, undoSyncHead(t, dir),
			!undoSyncMergeStateGone(t, dir),
			undoSyncUnmergedStages(t, dir), len(strings.Split(stagesBefore, "\n")),
			oneLine(testutil.Git(t, dir, "status", "--porcelain")),
			oneLine(stdout))
	}

	// A refusal must leave the merge exactly as it found it.
	if head := undoSyncHead(t, dir); head != mainSHA {
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
}

// undoSyncMergeStateGone reports whether .git/MERGE_HEAD is absent.
func undoSyncMergeStateGone(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD"))
	return os.IsNotExist(err)
}

// GREEN pin: `safegit undo` must not touch the working tree.
//
// undo.go:207 uses git.SyncMainIndex, the plain read-tree with no -u, so the
// undone commit's content stays on disk: a file the commit added remains as an
// untracked file, and a file the commit modified remains modified relative to
// the restored HEAD. That is the correct contract for undo -- it reverses the
// commit, not the work -- and any fix for the staged-state destruction above
// must keep it. Switching this call site to the -u variant
// (SyncMainIndexWithWorktree, internal/git/git.go:320) would silently discard
// the operator's edits, so this test exists to make that regression loud.
func TestUndoLeavesWorkingTreeIntact(t *testing.T) {
	dir := newRepo(t)
	env := undoSyncSession()

	testutil.WriteFile(t, dir, "tracked.txt", "v1\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "add tracked.txt", "--", "tracked.txt"); code != 0 {
		t.Fatalf("setup commit failed (code %d): %s", code, stderr)
	}
	beforeCommit := undoSyncHead(t, dir)

	// One commit that both modifies a tracked file and adds a new one.
	testutil.WriteFile(t, dir, "tracked.txt", "v2\n")
	testutil.WriteFile(t, dir, "added.txt", "added by the undone commit\n")
	if _, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "the commit to undo", "--", "tracked.txt", "added.txt"); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	if _, stderr, code := runSafegitEnv(t, dir, env, "undo"); code != 0 {
		t.Fatalf("undo failed (code %d): %s", code, stderr)
	}
	if head := undoSyncHead(t, dir); head != beforeCommit {
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

// The same read-tree sync runs after every guarded passthrough
// (coord_cmd.go:373 -> coord_cmd.go:50 -> git.SyncMainIndex(ctx, "HEAD")), and
// there it destroys state that belongs to the operation safegit just ran.
//
// A conflicted `git cherry-pick` leaves unmerged stages in the index; that is
// how git records which paths still need resolving and how `git cherry-pick
// --continue` knows the conflict was addressed. safegit runs cherry-pick, then
// unconditionally read-trees HEAD over the index, erasing every stage. git
// stops reporting `UU`, `git ls-files -u` comes back empty, and
// CHERRY_PICK_HEAD is still there.
//
// This test lives in this file because it is the same defect at another call
// site: an index sync that assumes the index may be rebuilt from HEAD.
//
// RED today: the stages are gone.
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
	sideSHA := undoSyncHead(t, dir)

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
			"  cause: coord_cmd.go:373 calls syncMainIndex (coord_cmd.go:50) after every guarded\n"+
			"         passthrough, which is git.SyncMainIndex -> read-tree HEAD.",
			oneLine(statusOut))
	}
}
