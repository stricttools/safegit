package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
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
// The window is simulated rather than raced: the files that define the state a
// re-run would see -- .git/index (the unmerged stages), .git/MERGE_HEAD,
// .git/MERGE_MSG and .git/AUTO_MERGE -- are snapshotted before a successful
// conclusion and restored after it, which is exactly the on-disk state a crash
// in that window leaves behind.

// crashWindowFiles are the files whose contents define "a merge is in flight
// here": the unmerged stages, the two state files sequencer.Read consults for a
// merge, and AUTO_MERGE.
//
// AUTO_MERGE is in the set because a REAL crash in this window leaves it there:
// it is removed by the same cleanup the crash interrupted. Leaving it out made
// the restored state one safegit refuses for a different reason entirely -- a
// content conflict git recorded no AUTO_MERGE for is a merge safegit does not
// conclude -- so the fixture would have been testing that refusal instead of
// the crash.
var crashWindowFiles = []string{"index", "MERGE_HEAD", "MERGE_MSG", "AUTO_MERGE"}

// snapshotCrashWindow reads the three files, failing when one is missing --
// the fixture is meant to be parked mid-merge, and a missing file would make
// the restore below silently reconstruct a different state.
// The git dir is git's own answer rather than <dir>/.git, so a SUBMODULE
// checkout -- whose .git is a file pointing into the parent's modules
// directory -- is snapshotted as readily as an ordinary repository.
func snapshotCrashWindow(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	gitDir := submoduleGitDir(t, dir)
	snap := make(map[string][]byte, len(crashWindowFiles))
	for _, name := range crashWindowFiles {
		data, err := os.ReadFile(filepath.Join(gitDir, name))
		if err != nil {
			t.Fatalf("the fixture must be parked mid-merge; reading %s: %v", filepath.Join(gitDir, name), err)
		}
		snap[name] = data
	}
	return snap
}

// restoreCrashWindow puts the snapshotted files back, which is the state a
// process killed between the ref update and the cleanup leaves behind.
func restoreCrashWindow(t *testing.T, dir string, snap map[string][]byte) {
	t.Helper()
	gitDir := submoduleGitDir(t, dir)
	for _, name := range crashWindowFiles {
		if err := os.WriteFile(filepath.Join(gitDir, name), snap[name], 0o644); err != nil {
			t.Fatalf("restoring %s: %v", filepath.Join(gitDir, name), err)
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

// crashedConclusion is a repository whose merge was concluded by a run that was
// then "killed" before its cleanup: the commit stands, and the state files, the
// unmerged index and AUTO_MERGE are all back exactly as the crash left them.
//
// The conclusion resolves conflicted.txt to OURS rather than to the working
// tree, so the content the standing commit holds for that path is a stage blob
// an operator can name again on the re-run -- which is what the declaration
// checks below need.
type crashedConclusion struct {
	fx conflictedMergeFixture
	// concluded is the commit the killed run created.
	concluded string
	// committed is what that commit holds for conflicted.txt, and what the
	// working tree held when the crash happened.
	committed string
}

func newCrashedConclusion(t *testing.T) crashedConclusion {
	t.Helper()
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true})

	snap := snapshotCrashWindow(t, fx.dir)
	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=ours"); code != 0 {
		t.Fatalf("the first merge-continue failed (code %d): %s", code, stderr)
	}
	concluded := testutil.Rev(t, fx.dir, "HEAD")
	committed := testutil.MustShow(t, fx.dir, "HEAD", "conflicted.txt")

	// The crash: the commit stands, the state files are back.
	restoreCrashWindow(t, fx.dir, snap)

	return crashedConclusion{fx: fx, concluded: concluded, committed: committed}
}

// TestCrashReRunRefusesToDestroyAnEditMadeAfterTheCrash: the overwrite refusal
// covers the crash path too.
//
// The conclusion's own path refuses a resolution whose write would destroy
// working-tree content no side of the conflict accounts for. The crash re-run
// materializes the same content by a different route, and it did so without
// asking: an operator who edited the file after the crash lost that edit
// outright, with an exit 0 on top.
func TestCrashReRunRefusesToDestroyAnEditMadeAfterTheCrash(t *testing.T) {
	c := newCrashedConclusion(t)

	// The hand edit: made after the crash, held in no commit, no stage and no
	// stash.
	const handEdited = "line1\nedited after the crash\nline3\n"
	testutil.WriteFile(t, c.fx.dir, "conflicted.txt", handEdited)
	if handEdited == c.committed {
		t.Fatal("the fixture's hand edit equals the committed content; it must differ for anything to be destroyed")
	}

	stdout, stderr, code := runSafegitEnv(t, c.fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=ours")
	if code != exitcode.ConclusionWouldOverwrite {
		t.Errorf("the crash re-run exited %d, want %d (the overwrite refusal)\nstdout=%s\nstderr=%s",
			code, exitcode.ConclusionWouldOverwrite, stdout, stderr)
	}
	if got := readWorktree(t, c.fx.dir, "conflicted.txt"); got != handEdited {
		t.Errorf("conflicted.txt holds %q, want the hand edit %q that exists nowhere else", got, handEdited)
	}
	if !strings.Contains(stderr, "conflicted.txt") {
		t.Errorf("the refusal does not name the file whose content would be destroyed:\n%s", stderr)
	}
	if head := testutil.Rev(t, c.fx.dir, "HEAD"); head != c.concluded {
		t.Errorf("HEAD moved to %s (was %s); the refusal must leave the standing commit alone", head, c.concluded)
	}
}

// TestCrashReRunFinishesTheCleanupCompletely: a re-run that declares nothing
// must finish the conclusion the crash interrupted -- and its success message
// must be true.
//
// The re-run used to apply only the resolutions the OPERATOR declared, so a
// re-run with none applied nothing: the reconcile deliberately preserves
// unmerged stages, three of them survived, and the run printed "the index and
// working tree are in step with the commit" over the top of them. The commit
// itself embodies the resolutions, so the edits come from ITS tree.
func TestCrashReRunFinishesTheCleanupCompletely(t *testing.T) {
	c := newCrashedConclusion(t)

	stdout, stderr, code := runSafegitEnv(t, c.fx.dir, conclusionSession, "merge-continue")
	if code != 0 {
		t.Fatalf("the crash re-run failed (code %d): %s\n%s", code, stderr, stdout)
	}
	if head := testutil.Rev(t, c.fx.dir, "HEAD"); head != c.concluded {
		t.Errorf("the re-run moved HEAD to %s (was %s); nothing was left to commit", head, c.concluded)
	}
	if n := unmergedCount(t, c.fx.dir); n != 0 {
		t.Errorf("%d unmerged index entr(ies) survived a run that reported the index in step with the commit:\n%s\nstdout=%s",
			n, testutil.Git(t, c.fx.dir, "ls-files", "-u"), stdout)
	}
	if got := readWorktree(t, c.fx.dir, "conflicted.txt"); got != c.committed {
		t.Errorf("conflicted.txt holds %q, want the committed content %q", got, c.committed)
	}
	if status := strings.TrimSpace(testutil.Git(t, c.fx.dir, "status", "--porcelain")); status != "" {
		t.Errorf("the re-run left the index or the working tree out of step with the commit:\n%s", status)
	}
	assertNoSequencerResidue(t, c.fx.dir, "the crash re-run that declared nothing")
}

// TestCrashReRunRefusesADeclarationTheCommitContradicts: the commit already
// embodies the resolutions, so a re-run declaring a DIFFERENT side is a
// statement about content that was decided before this run started. It is
// refused naming the commit that stands, rather than silently ignored.
func TestCrashReRunRefusesADeclarationTheCommitContradicts(t *testing.T) {
	c := newCrashedConclusion(t)

	stdout, stderr, code := runSafegitEnv(t, c.fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=theirs")
	if code == 0 {
		t.Fatalf("a declaration the standing commit contradicts was accepted (exit 0)\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "conflicted.txt") {
		t.Errorf("the refusal does not name the path it is about:\n%s", stderr)
	}
	if !strings.Contains(stderr, c.concluded[:8]) {
		t.Errorf("the refusal does not name the commit that stands (%s):\n%s", c.concluded[:8], stderr)
	}
	if head := testutil.Rev(t, c.fx.dir, "HEAD"); head != c.concluded {
		t.Errorf("HEAD moved to %s (was %s)", head, c.concluded)
	}
	// Nothing was cleaned up either: the refusal comes before the aftercare.
	if !testutil.FileExists(filepath.Join(c.fx.dir, ".git", "MERGE_HEAD")) {
		t.Error("the refusal removed the merge state; a refusal changes nothing")
	}
}

// TestCrashReRunAcceptsADeclarationTheCommitBearsOut: the same declaration the
// killed run made is not a contradiction -- it is moot, and the re-run concludes
// exactly as one that declared nothing.
func TestCrashReRunAcceptsADeclarationTheCommitBearsOut(t *testing.T) {
	c := newCrashedConclusion(t)

	stdout, stderr, code := runSafegitEnv(t, c.fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=ours")
	if code != 0 {
		t.Fatalf("the matching declaration was refused (code %d): %s\n%s", code, stderr, stdout)
	}
	if head := testutil.Rev(t, c.fx.dir, "HEAD"); head != c.concluded {
		t.Errorf("the re-run moved HEAD to %s (was %s)", head, c.concluded)
	}
	if n := unmergedCount(t, c.fx.dir); n != 0 {
		t.Errorf("%d unmerged index entr(ies) survived the re-run", n)
	}
	if got := readWorktree(t, c.fx.dir, "conflicted.txt"); got != c.committed {
		t.Errorf("conflicted.txt holds %q, want the committed content %q", got, c.committed)
	}
	assertNoSequencerResidue(t, c.fx.dir, "the crash re-run with a matching declaration")
}
