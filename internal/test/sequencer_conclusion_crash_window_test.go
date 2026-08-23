package test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// FINDING: there is a crash window between the ref update a conclusion makes
// and the state cleanup that follows it (finishConclusion in
// sequencer_continue.go runs AFTER commit.Pipeline.Execute has moved the ref).
// A process killed inside that window leaves the merge commit on the branch
// AND git's merge state on disk, and re-running the same conclusion mints a
// SECOND merge commit whose second parent is already an ancestor of its first
// -- a degenerate merge, reported as a clean success.
//
// RULED TARGET: a conclusion whose commit already stands finishes the cleanup
// instead of committing again.
//
// The window is simulated rather than raced: the three files that define the
// state a re-run would see -- .git/index (the unmerged stages), .git/MERGE_HEAD
// and .git/MERGE_MSG -- are snapshotted before a successful conclusion and
// restored after it, which is exactly the on-disk state a crash in that window
// leaves behind.

// crashWindowFiles are the files whose contents define "a merge is in flight
// here": the unmerged stages plus the two state files sequencer.Read consults
// for a merge.
var crashWindowFiles = []string{"index", "MERGE_HEAD", "MERGE_MSG"}

// snapshotCrashWindow reads the three files, failing when one is missing --
// the fixture is meant to be parked mid-merge, and a missing file would make
// the restore below silently reconstruct a different state.
func snapshotCrashWindow(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	snap := make(map[string][]byte, len(crashWindowFiles))
	for _, name := range crashWindowFiles {
		data, err := os.ReadFile(filepath.Join(dir, ".git", name))
		if err != nil {
			t.Fatalf("the fixture must be parked mid-merge; reading .git/%s: %v", name, err)
		}
		snap[name] = data
	}
	return snap
}

// restoreCrashWindow puts the snapshotted files back, which is the state a
// process killed between the ref update and the cleanup leaves behind.
func restoreCrashWindow(t *testing.T, dir string, snap map[string][]byte) {
	t.Helper()
	for _, name := range crashWindowFiles {
		if err := os.WriteFile(filepath.Join(dir, ".git", name), snap[name], 0o644); err != nil {
			t.Fatalf("restoring .git/%s: %v", name, err)
		}
	}
}

// TestReRunAfterTheCrashWindowDoesNotMintASecondMergeCommit: the re-run finds a
// commit that already stands and must finish the cleanup rather than commit
// again.
func TestReRunAfterTheCrashWindowDoesNotMintASecondMergeCommit(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true, resolveInTree: true})

	snap := snapshotCrashWindow(t, fx.dir)

	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=worktree"); code != 0 {
		t.Fatalf("the first merge-continue failed (code %d): %s", code, stderr)
	}
	concluded := testutil.Rev(t, fx.dir, "HEAD")
	if parents := testutil.Parents(t, fx.dir, concluded); len(parents) != 2 || parents[0] != fx.mainSHA || parents[1] != fx.featureSHA {
		t.Fatalf("the first conclusion has parents %v, want [%s %s]", parents, fx.mainSHA, fx.featureSHA)
	}

	// The crash: the commit stands, the state files are back.
	restoreCrashWindow(t, fx.dir, snap)

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=worktree")
	if code != 0 {
		t.Fatalf("the re-run after the crash window failed (code %d): %s\n%s", code, stderr, stdout)
	}

	if head := testutil.Rev(t, fx.dir, "HEAD"); head != concluded {
		t.Errorf("the re-run created a second commit: HEAD moved %s -> %s; the conclusion's commit already stood, so the re-run must only finish the cleanup",
			concluded, head)

		// Name the shape of what it created, so a failure reads as the finding
		// rather than as an unexplained ref move.
		parents := testutil.Parents(t, fx.dir, head)
		if len(parents) == 2 {
			if _, ancestor := testutil.GitTry(t, fx.dir, "merge-base", "--is-ancestor", parents[1], parents[0]); ancestor == 0 {
				t.Errorf("the second commit is a degenerate merge: its second parent %s is already an ancestor of its first %s",
					parents[1], parents[0])
			}
		}
	}

	// Whatever it did, the state has to be gone afterwards and the repository
	// usable -- that is the half of the conclusion the crash interrupted.
	assertNoSequencerResidue(t, fx.dir, "the re-run after the crash window")
}
