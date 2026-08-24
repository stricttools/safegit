package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// FINDING (new ruling): an ordinary `safegit commit` accepts an index that still
// holds UNMERGED entries.
//
// safegit's mid-operation refusal reads git's STATE FILES, so a repository whose
// state files are gone but whose index still carries stage 1/2/3 entries reads
// as idle. git itself refuses every commit in that repository ("Committing is
// not possible because you have unmerged files"); safegit commits the
// marker-laden file from the working tree and reports a clean success.
//
// The state is not hypothetical: a crashed operation, a hand-deleted MERGE_HEAD,
// or any of the several ways `git merge --abort` can leave half its work behind
// produce it, and it is exactly what `safegit doctor` exists to find.
//
// RULED TARGET: refuse (git parity), naming the doctor repair as the way out.

// orphanedStateFiles are the merge's own state files. Removing them while
// leaving the index alone is what produces an orphaned unmerged index.
var orphanedStateFiles = []string{"MERGE_HEAD", "MERGE_MSG", "MERGE_MODE", "AUTO_MERGE"}

// newOrphanedUnmergedRepo parks a repository in a conflicted merge and then
// strips the state that says so, leaving the unmerged index entries and the
// marker-laden file on disk.
func newOrphanedUnmergedRepo(t *testing.T) conflictedMergeFixture {
	t.Helper()
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true})

	for _, name := range orphanedStateFiles {
		if err := os.Remove(filepath.Join(fx.dir, ".git", name)); err != nil && !os.IsNotExist(err) {
			t.Fatalf("removing .git/%s: %v", name, err)
		}
	}
	// AUTO_MERGE is a ref as well as a file on some layouts.
	testutil.GitTry(t, fx.dir, "update-ref", "-d", "AUTO_MERGE")

	// The two halves of the state this test is about: git sees nothing in flight,
	// and the index is still unmerged.
	if n := unmergedCount(t, fx.dir); n == 0 {
		t.Fatal("the fixture must keep the unmerged index entries")
	}
	if !strings.Contains(readWorktree(t, fx.dir, "conflicted.txt"), "<<<<<<<") {
		t.Fatal("the fixture must keep the conflict markers in the working-tree file")
	}
	return fx
}

// TestCommitRefusesAnUnmergedIndex: git refuses this repository outright, and so
// must safegit.
func TestCommitRefusesAnUnmergedIndex(t *testing.T) {
	fx := newOrphanedUnmergedRepo(t)
	before := testutil.Rev(t, fx.dir, "HEAD")

	// The recorded fact the parity is measured against: git's own commit refuses
	// here, and says why.
	gitSaid, gitCode := testutil.GitTry(t, fx.dir, "commit", "-m", "git's own attempt")
	if gitCode == 0 {
		t.Fatalf("git committed an unmerged index; the parity this test asserts no longer exists:\n%s", gitSaid)
	}
	if !strings.Contains(gitSaid, "you have unmerged files") {
		t.Errorf("git refused for an unexpected reason, so the wording safegit reproduces needs re-checking:\n%s", oneLine(gitSaid))
	}

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"commit", "-m", "an ordinary commit over an unmerged index", "--", "conflicted.txt")
	if code == 0 {
		t.Errorf("safegit committed over an unmerged index and exited 0:\nstdout=%s", stdout)
	}
	if head := testutil.Rev(t, fx.dir, "HEAD"); head != before {
		t.Errorf("HEAD moved to %s (was %s); the refusal must come before the commit", head, before)
	}
	if !strings.Contains(stderr, "doctor") {
		t.Errorf("the refusal does not name the doctor repair as the way out:\n%s", stderr)
	}

	// The consequence, asserted independently of the exit code: no commit
	// anywhere in this repository carries the conflict markers.
	assertNoCommittedMarkers(t, fx.dir, "conflicted.txt")
}

// TestUnmergedIndexRefusalExitsTwentyEightOnEveryAuthoringForm: the refusal is
// the pipeline's, so every route into it -- the plain commit, the amend and the
// reword -- reports the same registered code rather than a generic failure.
func TestUnmergedIndexRefusalExitsTwentyEightOnEveryAuthoringForm(t *testing.T) {
	forms := []struct {
		name string
		args []string
	}{
		{"commit", []string{"commit", "-m", "a commit", "--", "conflicted.txt"}},
		{"amend", []string{"commit", "--amend", "-m", "an amend", "--", "conflicted.txt"}},
		{"reword", []string{"commit", "--amend", "-m", "a reword"}},
	}
	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			fx := newOrphanedUnmergedRepo(t)
			before := testutil.Rev(t, fx.dir, "HEAD")

			stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession, form.args...)
			if code != exitcode.UnmergedIndex {
				t.Fatalf("%s over an unmerged index exited %d, want %d (UnmergedIndex)\nstdout=%s\nstderr=%s",
					form.name, code, exitcode.UnmergedIndex, stdout, stderr)
			}
			if !strings.Contains(stderr, "unmerged") {
				t.Errorf("the refusal does not name git's own fact:\n%s", stderr)
			}
			if !strings.Contains(stderr, "safegit doctor --action fix") {
				t.Errorf("the refusal does not name the doctor repair as the way out:\n%s", stderr)
			}
			if head := testutil.Rev(t, fx.dir, "HEAD"); head != before {
				t.Errorf("HEAD moved to %s (was %s); the refusal must come before any ref update", head, before)
			}
		})
	}
}

// TestConclusionsCommitWhileTheIndexIsUnmerged: the refusal above must not reach
// the commands whose whole job is to resolve that index. A conclusion declares
// itself as one -- it carries the sequencer context and seeds its commit from the
// shared index -- and that declaration is the exemption.
func TestConclusionsCommitWhileTheIndexIsUnmerged(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession})
	if n := unmergedCount(t, fx.dir); n == 0 {
		t.Fatal("the fixture must be parked with an unmerged index")
	}

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=ours")
	if code != 0 {
		t.Fatalf("merge-continue exited %d over the unmerged index it exists to conclude\nstdout=%s\nstderr=%s",
			code, stdout, stderr)
	}
	head := testutil.Rev(t, fx.dir, "HEAD")
	if head == fx.mainSHA {
		t.Fatalf("HEAD is still %s; the conclusion committed nothing", head)
	}
	parents := testutil.GitOut(t, fx.dir, "rev-list", "--parents", "-n", "1", "HEAD")
	if !strings.Contains(parents, fx.featureSHA) {
		t.Errorf("the merge commit does not carry the side it merged (%s):\n%s", fx.featureSHA, parents)
	}
	if n := unmergedCount(t, fx.dir); n != 0 {
		t.Errorf("the conclusion left %d unmerged index entries behind", n)
	}
}

// assertNoCommittedMarkers fails when any commit reachable from any ref holds a
// version of path that still carries conflict markers.
func assertNoCommittedMarkers(t *testing.T, dir, path string) {
	t.Helper()
	for _, sha := range testutil.SplitLines(testutil.GitOut(t, dir, "rev-list", "--all")) {
		sha = strings.TrimSpace(sha)
		if sha == "" {
			continue
		}
		blob, ok := testutil.Show(t, dir, sha, path)
		if !ok {
			continue
		}
		if strings.Contains(blob, "<<<<<<<") {
			t.Errorf("commit %s records %s with conflict markers still in it", sha, path)
		}
	}
}
