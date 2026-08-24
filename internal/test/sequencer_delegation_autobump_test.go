package test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// newQueuedSubmodulePick parks the SUBMODULE of a parent repository in a
// three-command cherry-pick queue whose first two commands conflict, so
// concluding the first stops git again with one commit made and one command
// still queued.
//
// The fixture's own history is built with plain git so that building it does
// not bump the parent; the parent's auto-bump is enabled afterwards, which
// makes the conclusion the only thing in the test that can move the gitlink.
func newQueuedSubmodulePick(t *testing.T) (parentDir, subDir string) {
	t.Helper()
	parentDir, _ = newRepoWithSubmodule(t)
	subDir = prepSubmoduleForCommit(t, parentDir)

	testutil.WriteFile(t, subDir, "c.txt", "base\n")
	testutil.WriteFile(t, subDir, "d.txt", "base\n")
	testutil.Git(t, subDir, "add", "c.txt", "d.txt")
	testutil.Git(t, subDir, "commit", "-m", "base")

	testutil.Git(t, subDir, "branch", "side")
	testutil.WriteFile(t, subDir, "c.txt", "main c\n")
	testutil.WriteFile(t, subDir, "d.txt", "main d\n")
	testutil.Git(t, subDir, "add", "c.txt", "d.txt")
	testutil.Git(t, subDir, "commit", "-m", "main edits both")

	testutil.Git(t, subDir, "switch", "side")
	testutil.WriteFile(t, subDir, "c.txt", "side c\n")
	testutil.Git(t, subDir, "add", "c.txt")
	testutil.Git(t, subDir, "commit", "-m", "side c")
	first := testutil.Rev(t, subDir, "HEAD")
	testutil.WriteFile(t, subDir, "d.txt", "side d\n")
	testutil.Git(t, subDir, "add", "d.txt")
	testutil.Git(t, subDir, "commit", "-m", "side d")
	second := testutil.Rev(t, subDir, "HEAD")
	testutil.WriteFile(t, subDir, "e.txt", "side e\n")
	testutil.Git(t, subDir, "add", "e.txt")
	testutil.Git(t, subDir, "commit", "-m", "side e")
	third := testutil.Rev(t, subDir, "HEAD")
	testutil.Git(t, subDir, "switch", "main")

	// The parent starts pointing at the submodule's current tip, so the only
	// thing that can move its gitlink afterwards is the conclusion.
	testutil.Git(t, parentDir, "add", "mysub")
	testutil.Git(t, parentDir, "commit", "-m", "point at the submodule fixture")
	enableAutoBump(t, parentDir)

	// Raw git: a multi-command queue is a state only git can create now.
	if out, code := testutil.GitTry(t, subDir, "cherry-pick", first, second, third); code == 0 {
		t.Fatalf("the fixture needs the queue to stop on its first command: %s", out)
	}
	return parentDir, subDir
}

// TestDelegatedConclusionBumpsTheParentWhenItStopsMidQueue: a delegated
// conclusion inside a submodule that stops on the queue's NEXT conflict has
// still moved the submodule's branch, and the parent's gitlink has to follow.
//
// The auto-bump used to run only on the path where git finished the whole
// queue, so a conclusion that stopped mid-queue left the parent pointing at the
// commit the submodule had BEFORE the conclusion -- a gitlink naming a tip that
// is no longer the submodule's, produced by a safegit command that the operator
// was told nothing about. The commits git made before stopping are real either
// way, so the bump keys on the branch having MOVED, not on the outcome.
func TestDelegatedConclusionBumpsTheParentWhenItStopsMidQueue(t *testing.T) {
	parentDir, subDir := newQueuedSubmodulePick(t)

	subBefore := testutil.Rev(t, subDir, "HEAD")
	gitlinkBefore := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if gitlinkBefore != subBefore {
		t.Fatalf("the fixture must start with the parent pointing at the submodule tip: gitlink %s, sub HEAD %s", gitlinkBefore, subBefore)
	}

	stdout, stderr, code := runSafegitEnv(t, subDir, conclusionSession,
		"cherry-pick-continue", "--resolve", "c.txt=theirs")
	if code == 0 {
		t.Fatalf("the queue's second command conflicts, so this must exit nonzero:\nstdout=%s\nstderr=%s", stdout, stderr)
	}

	subAfter := testutil.Rev(t, subDir, "HEAD")
	if subAfter == subBefore {
		t.Fatal("the conclusion committed nothing in the submodule, so there is nothing for the parent to follow")
	}
	if got := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub")); got != subAfter {
		t.Errorf("parent gitlink = %s, want the submodule's new tip %s (the conclusion stopped mid-queue but the branch moved)", got, subAfter)
	}

	// The bump is a parent commit that says what moved it.
	msg := commitMessage(t, parentDir, "HEAD")
	if !strings.Contains(msg, "bump mysub") {
		t.Errorf("the parent's tip commit %q is not the bump", msg)
	}
	if !strings.Contains(msg, subAfter) {
		t.Errorf("the parent's bump commit does not name the submodule commit it followed:\n%s", msg)
	}

	// The stop itself is unchanged: git's state is still in flight, and the
	// operator is told the way out.
	if !testutil.FileExists(filepath.Join(subDir, ".git")) {
		t.Fatal("the submodule's git link vanished")
	}
	if !strings.Contains(stderr, "cherry-pick-continue") {
		t.Errorf("the report does not name the command that concludes the new stop:\n%s", stderr)
	}
}

// TestDelegatedConclusionBumpsTheParentWhenTheQueueFinishes is the control for
// the branch-moved rule above: the path where git finishes the whole queue
// bumps the parent as it always did, and it is reached here by concluding the
// same fixture twice.
func TestDelegatedConclusionBumpsTheParentWhenTheQueueFinishes(t *testing.T) {
	parentDir, subDir := newQueuedSubmodulePick(t)

	if _, stderr, code := runSafegitEnv(t, subDir, conclusionSession,
		"cherry-pick-continue", "--resolve", "c.txt=theirs"); code == 0 {
		t.Fatalf("the fixture needs the second stop: %s", stderr)
	}
	if _, stderr, code := runSafegitEnv(t, subDir, conclusionSession,
		"cherry-pick-continue", "--resolve", "d.txt=theirs"); code != 0 {
		t.Fatalf("the second delegated conclusion failed (code %d): %s", code, stderr)
	}

	assertNoSequencerResidue(t, subDir, "the finished three-command queue in a submodule")
	subHead := testutil.Rev(t, subDir, "HEAD")
	if got := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub")); got != subHead {
		t.Errorf("parent gitlink = %s, want the submodule's tip %s after the queue finished", got, subHead)
	}
}
