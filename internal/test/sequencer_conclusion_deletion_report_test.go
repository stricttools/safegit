package test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// FINDING: the report and the preview claim a working-tree file was "written
// with the resolved content" when the real effect on disk is a DELETION.
//
// worktreeEffects (sequencer_continue_cmd.go) splits the declared resolutions
// by KEYWORD alone: `ours` and `theirs` always land in the `written` list and
// `delete` always in the `removed` one. For a modify/delete conflict the side
// that deleted the path has NO index stage, so resolving to that side removes
// the path from the commit and removes the file from disk -- and the report
// still says it was written with the resolved content, naming a file that is
// no longer there.
//
// RULED TARGET: the report and the preview state the deletion for a stage
// resolution whose stage is absent, rather than claiming a write.

// modifyDeleteFixture is a repository parked in a merge whose only conflict is
// a modify/delete: main edited the path, the incoming side deleted it.
type modifyDeleteFixture struct {
	dir string
	// path is the modify/delete conflicted path. `theirs` resolves to the side
	// that deleted it, so the conclusion removes it from disk.
	path string
}

// newModifyDeleteMergeRepo builds the fixture. The delete is committed with
// plain git because `safegit commit` of a removal is not what is under test
// here -- the merge state it produces is.
func newModifyDeleteMergeRepo(t *testing.T) modifyDeleteFixture {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "y.txt", "l1\nbase\nl3\n")
	safegitCommitEnv(t, dir, conclusionSession, "base", "y.txt")

	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "-q", "feature")
	testutil.Git(t, dir, "rm", "-q", "y.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "feature deletes y.txt")

	testutil.Git(t, dir, "switch", "-q", "main")
	testutil.WriteFile(t, dir, "y.txt", "l1\nmain\nl3\n")
	safegitCommitEnv(t, dir, conclusionSession, "main edits y.txt", "y.txt")

	stdout, stderr, code := runSafegitEnv(t, dir, conclusionSession, "merge", "feature")
	if code == 0 {
		t.Fatalf("safegit merge feature succeeded; the fixture needs a modify/delete conflict\nstdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "CONFLICT") {
		t.Fatalf("safegit merge feature did not report a conflict (code %d)\nstdout=%s stderr=%s", code, stdout, stderr)
	}
	// The stage that makes this fixture what it is: the incoming side has none.
	if _, ok := testutil.GitTryOut(t, dir, "rev-parse", "--verify", "--quiet", ":3:y.txt"); ok {
		t.Fatal("the fixture must be a modify/delete conflict: the incoming side still has a stage-3 blob for y.txt")
	}

	return modifyDeleteFixture{dir: dir, path: "y.txt"}
}

// TestConclusionReportsDeletionWhenResolvingToTheDeletingSide: the executed
// run. The file really is deleted, and the report must say so instead of
// claiming it was written.
func TestConclusionReportsDeletionWhenResolvingToTheDeletingSide(t *testing.T) {
	fx := newModifyDeleteMergeRepo(t)

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", fx.path+"=theirs")
	if code != 0 {
		t.Fatalf("merge-continue failed (code %d): %s\n%s", code, stderr, stdout)
	}

	// The fact the report has to describe: the file is gone from disk and from
	// the commit.
	if testutil.FileExists(filepath.Join(fx.dir, fx.path)) {
		t.Fatalf("%s is still on disk; resolving to the side that deleted it must remove it", fx.path)
	}
	if testutil.Contains(testutil.TreePaths(t, fx.dir, "HEAD"), fx.path) {
		t.Fatalf("%s is still in the concluded tree", fx.path)
	}

	if strings.Contains(stdout, "written with the resolved content") {
		t.Errorf("the report claims %s was written with the resolved content, but the effect was a deletion:\n%s", fx.path, stdout)
	}
	if !strings.Contains(stdout, "deleted") {
		t.Errorf("the report does not state the deletion of %s:\n%s", fx.path, stdout)
	}
}

// TestConclusionPreviewReportsDeletionWhenResolvingToTheDeletingSide is the
// same claim on the preview's would-do line, which is where an operator reads
// it BEFORE the file is gone.
func TestConclusionPreviewReportsDeletionWhenResolvingToTheDeletingSide(t *testing.T) {
	fx := newModifyDeleteMergeRepo(t)

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--dry-run", "--resolve", fx.path+"=theirs")
	if code != 0 {
		t.Fatalf("merge-continue --dry-run failed (code %d): %s\n%s", code, stderr, stdout)
	}

	// A preview changes nothing, so the file is still there while it is read.
	if !testutil.FileExists(filepath.Join(fx.dir, fx.path)) {
		t.Fatalf("the preview removed %s from disk", fx.path)
	}

	if strings.Contains(stdout, "would be overwritten with the resolved content") {
		t.Errorf("the preview says %s would be overwritten, but the run would delete it:\n%s", fx.path, stdout)
	}
	if !strings.Contains(stdout, "would be deleted") {
		t.Errorf("the preview does not state that %s would be deleted:\n%s", fx.path, stdout)
	}
}
