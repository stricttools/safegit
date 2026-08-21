package test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// The shared .git/index belongs to every session in the worktree, not to the
// command that happens to be running. Each safegit verb that moves a ref used
// to rebuild the index from the new tip's tree, which erased whatever the index
// held that the tree did not -- another session's staged work, silently and
// with nothing in the oplog to recover from.
//
// `safegit undo` has its own coverage in undo_index_sync_test.go. The three
// tests here cover the other ref movers -- commit, amend and reword -- against
// the same three kinds of in-flight work.

// stagedStatusOf returns the status letter `git diff --cached --name-status`
// reports for a path ("M", "A", "D"), or "" when the staged delta does not
// mention it at all.
func stagedStatusOf(lines []string, path string) string {
	for _, l := range lines {
		fields := strings.Fields(l)
		if len(fields) >= 2 && fields[len(fields)-1] == path {
			return fields[0]
		}
	}
	return ""
}

// stageForeignWork puts the three kinds of work another session can have in
// flight into the shared index: a modification of a tracked file, a brand-new
// addition, and a cache-only deletion (the file stays on disk). It fails the
// test unless all three are staged, so a fixture that never established the
// precondition cannot be mistaken for a passing assertion.
//
// foreign.txt must already be tracked and committed.
func stageForeignWork(t *testing.T, dir string) {
	t.Helper()
	testutil.WriteFile(t, dir, "foreign.txt", "staged edit\n")
	testutil.WriteFile(t, dir, "brand-new.txt", "staged addition\n")
	testutil.Git(t, dir, "add", "foreign.txt", "brand-new.txt")
	testutil.Git(t, dir, "rm", "--cached", "seed.txt")

	staged := undoSyncStagedStatus(t, dir)
	for path, want := range map[string]string{"foreign.txt": "M", "brand-new.txt": "A", "seed.txt": "D"} {
		if got := stagedStatusOf(staged, path); got != want {
			t.Fatalf("fixture: %s is staged as %q, want %q (staged: %v)", path, got, want, staged)
		}
	}
}

// assertForeignWorkSurvived checks all three staged items are still staged,
// naming every one that was lost rather than stopping at the first.
func assertForeignWorkSurvived(t *testing.T, dir, operation string) {
	t.Helper()
	staged := undoSyncStagedStatus(t, dir)
	var lost []string
	for path, want := range map[string]string{"foreign.txt": "M", "brand-new.txt": "A", "seed.txt": "D"} {
		if got := stagedStatusOf(staged, path); got != want {
			lost = append(lost, path+" (want "+want+", got "+got+")")
		}
	}
	if len(lost) > 0 {
		t.Fatalf("`safegit %s` destroyed another session's staged index state.\n"+
			"  wrong or missing: %s\n"+
			"  staged after the operation: %v\n"+
			"  git status: %s",
			operation, strings.Join(lost, ", "), staged,
			oneLine(testutil.Git(t, dir, "status", "--porcelain")))
	}
	// The cache-only deletion must not have taken the file with it.
	if !testutil.FileExists(filepath.Join(dir, "seed.txt")) {
		t.Errorf("seed.txt was removed from disk; the staged deletion was cache-only")
	}
}

// foreignStagedRepo builds a repo whose HEAD holds seed.txt and foreign.txt,
// then stages the three kinds of foreign work over it.
func foreignStagedRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "foreign.txt", "base\n")
	safegitCommit(t, dir, "add foreign.txt", "foreign.txt")
	stageForeignWork(t, dir)
	return dir
}

// A safegit commit of its own file must leave another session's staged work
// alone.
func TestCommitPreservesForeignStagedState(t *testing.T) {
	dir := foreignStagedRepo(t)

	testutil.WriteFile(t, dir, "owned.txt", "owned\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "owned commit", "--", "owned.txt"); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	if !testutil.Contains(testutil.TreePaths(t, dir, "HEAD"), "owned.txt") {
		t.Fatalf("the commit did not record owned.txt: %v", testutil.TreePaths(t, dir, "HEAD"))
	}
	assertForeignWorkSurvived(t, dir, "commit")
}

// An amend rewrites the tip; the staged work of a session that had nothing to
// do with that tip must survive it.
func TestAmendPreservesForeignStagedState(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "foreign.txt", "base\n")
	safegitCommit(t, dir, "add foreign.txt", "foreign.txt")
	testutil.WriteFile(t, dir, "owned.txt", "owned\n")
	safegitCommit(t, dir, "owned commit", "owned.txt")

	stageForeignWork(t, dir)

	testutil.WriteFile(t, dir, "owned.txt", "owned v2\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "owned commit amended", "--", "owned.txt"); code != 0 {
		t.Fatalf("amend failed (code %d): %s", code, stderr)
	}

	if got := testutil.MustShow(t, dir, "HEAD", "owned.txt"); got != "owned v2\n" {
		t.Fatalf("the amend did not record the new content: %q", got)
	}
	assertForeignWorkSurvived(t, dir, "commit --amend")
}

// A reword changes only the message, so there is no excuse at all for it to
// touch the index.
func TestRewordPreservesForeignStagedState(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "foreign.txt", "base\n")
	safegitCommit(t, dir, "add foreign.txt", "foreign.txt")
	testutil.WriteFile(t, dir, "owned.txt", "owned\n")
	safegitCommit(t, dir, "owned commit", "owned.txt")

	stageForeignWork(t, dir)

	if _, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "owned commit reworded"); code != 0 {
		t.Fatalf("reword failed (code %d): %s", code, stderr)
	}

	if msg := testutil.Git(t, dir, "log", "-1", "--format=%s"); !strings.Contains(msg, "reworded") {
		t.Fatalf("the reword did not change the message: %q", msg)
	}
	assertForeignWorkSurvived(t, dir, "commit --amend (reword)")
}
