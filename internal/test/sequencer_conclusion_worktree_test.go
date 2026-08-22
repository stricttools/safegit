package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// What a conclusion does to the WORKING TREE.
//
// The resolution keywords follow git's own idiom: `ours` and `theirs` write the
// chosen content onto the file on disk, exactly as `git checkout --ours <path>`
// does, and `delete` removes the file, exactly as `git rm <path>` does. Only
// `worktree` leaves disk alone, because disk is where its content came from.
//
// The alternative -- resolving the index alone and warning that the file still
// carries markers -- leaves the conflict one ordinary `safegit commit -- <path>`
// away from being committed. These tests pin the disk state, not the notice.

// worktreeText reads a repo-relative working-tree file verbatim.
func worktreeText(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("reading the working-tree file %s: %v", rel, err)
	}
	return string(data)
}

// assertWorkingTreeClean fails when git still reports anything to commit, which
// is the observable consequence of the index and the working tree agreeing.
func assertWorkingTreeClean(t *testing.T, dir, context string) {
	t.Helper()
	if status := strings.TrimSpace(testutil.Git(t, dir, "status", "--porcelain")); status != "" {
		t.Errorf("%s: the working tree is not clean after the conclusion:\n%s", context, status)
	}
}

// TestConclusionWritesOursIntoTheWorkingTree is the headline of the rule: after
// a concluded `ours` resolution the file on disk is marker-free and holds the
// committed content.
func TestConclusionWritesOursIntoTheWorkingTree(t *testing.T) {
	// resolveInTree is deliberately OFF: the file on disk still carries git's
	// conflict markers when the conclusion runs, which is the state that made
	// the index-only form dangerous.
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession})
	if before := worktreeText(t, fx.dir, "conflicted.txt"); !strings.Contains(before, "<<<<<<<") {
		t.Fatalf("the fixture must leave conflict markers on disk, got:\n%s", before)
	}

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=ours")
	if code != 0 {
		t.Fatalf("merge-continue failed (code %d): %s", code, stderr)
	}

	onDisk := worktreeText(t, fx.dir, "conflicted.txt")
	if strings.Contains(onDisk, "<<<<<<<") || strings.Contains(onDisk, ">>>>>>>") {
		t.Errorf("the conclusion left conflict markers on disk:\n%s", onDisk)
	}
	if want := "line1\nmain\nline3\n"; onDisk != want {
		t.Errorf("working-tree content = %q, want the committed ours content %q", onDisk, want)
	}
	if committed := testutil.MustShow(t, fx.dir, "HEAD", "conflicted.txt"); committed != onDisk {
		t.Errorf("disk (%q) and the commit (%q) disagree", onDisk, committed)
	}
	assertWorkingTreeClean(t, fx.dir, "ours")
	if !strings.Contains(stdout, "conflicted.txt") || !strings.Contains(stdout, "working-tree file(s) written") {
		t.Errorf("the report does not say the working tree was written:\n%s", stdout)
	}
}

// TestConclusionWritesTheirsIntoTheWorkingTree is the same rule on the incoming
// side, so `ours` cannot be passing by accident (the branch's own content is
// what a stale index-only conclusion would also leave staged).
func TestConclusionWritesTheirsIntoTheWorkingTree(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession})

	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=theirs"); code != 0 {
		t.Fatalf("merge-continue failed (code %d): %s", code, stderr)
	}

	onDisk := worktreeText(t, fx.dir, "conflicted.txt")
	if want := "line1\nfeature\nline3\n"; onDisk != want {
		t.Errorf("working-tree content = %q, want the committed theirs content %q", onDisk, want)
	}
	assertWorkingTreeClean(t, fx.dir, "theirs")
}

// TestConclusionDeleteRemovesTheFileFromDisk pins the `delete` half: the path
// leaves the commit AND the working tree, which is what `git rm` does and what
// an operator who typed "delete" means.
func TestConclusionDeleteRemovesTheFileFromDisk(t *testing.T) {
	// cleanSideFile gives the merge something else to commit, so the conclusion
	// is not an empty one.
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true})

	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=delete"); code != 0 {
		t.Fatalf("merge-continue failed (code %d): %s", code, stderr)
	}

	if testutil.FileExists(filepath.Join(fx.dir, "conflicted.txt")) {
		t.Errorf("a delete resolution left the file on disk: %q", worktreeText(t, fx.dir, "conflicted.txt"))
	}
	if paths := testutil.TreePaths(t, fx.dir, "HEAD"); testutil.Contains(paths, "conflicted.txt") {
		t.Errorf("a delete resolution left the path in the commit (tree: %v)", paths)
	}
	assertWorkingTreeClean(t, fx.dir, "delete")
}

// TestConclusionWorktreeResolutionLeavesDiskAlone is the exemption: the file on
// disk is where a `worktree` resolution's content came from, so nothing is
// written back over it.
func TestConclusionWorktreeResolutionLeavesDiskAlone(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, resolveInTree: true})
	before := worktreeText(t, fx.dir, "conflicted.txt")

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=worktree")
	if code != 0 {
		t.Fatalf("merge-continue failed (code %d): %s", code, stderr)
	}

	if after := worktreeText(t, fx.dir, "conflicted.txt"); after != before {
		t.Errorf("a worktree resolution rewrote the file: %q -> %q", before, after)
	}
	if strings.Contains(stdout, "working-tree file(s) written") {
		t.Errorf("a worktree resolution must not claim to have written the working tree:\n%s", stdout)
	}
	assertWorkingTreeClean(t, fx.dir, "worktree")
}

// TestConclusionPreviewStatesTheWorkingTreeWrites: a dry run performs none of
// it and says what it would do, so the preview does not understate the command.
func TestConclusionPreviewStatesTheWorkingTreeWrites(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true})
	before := worktreeText(t, fx.dir, "conflicted.txt")

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--dry-run", "--resolve", "conflicted.txt=ours")
	if code != 0 {
		t.Fatalf("the preview failed (code %d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "would be overwritten") {
		t.Errorf("the preview does not state the working-tree write:\n%s", stdout)
	}
	if after := worktreeText(t, fx.dir, "conflicted.txt"); after != before {
		t.Errorf("the preview wrote the working tree: %q -> %q", before, after)
	}

	// And the delete form says the file would go, without removing it.
	stdout, stderr, code = runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--dry-run", "--resolve", "conflicted.txt=delete")
	if code != 0 {
		t.Fatalf("the delete preview failed (code %d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "would be deleted") {
		t.Errorf("the preview does not state the working-tree deletion:\n%s", stdout)
	}
	if !testutil.FileExists(filepath.Join(fx.dir, "conflicted.txt")) {
		t.Error("the preview deleted the working-tree file")
	}
}

// TestRefusedConclusionNeverTouchesTheWorkingTree: the write happens on a
// SUCCEEDED conclusion only. A refusal -- here the completeness refusal, which
// fires after the resolutions are parsed and before anything is committed --
// leaves every file exactly as it was.
func TestRefusedConclusionNeverTouchesTheWorkingTree(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true})
	before := worktreeText(t, fx.dir, "conflicted.txt")

	// A stray path alongside the real one: the whole declaration is refused.
	_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession, "merge-continue",
		"--resolve", "conflicted.txt=ours", "--resolve", "feature-only.txt=delete")
	if code == 0 {
		t.Fatalf("the stray resolution must be refused: %s", stderr)
	}
	if after := worktreeText(t, fx.dir, "conflicted.txt"); after != before {
		t.Errorf("a refused conclusion rewrote the working tree: %q -> %q", before, after)
	}
	if !testutil.FileExists(filepath.Join(fx.dir, "feature-only.txt")) {
		t.Error("a refused conclusion deleted a working-tree file")
	}
}
