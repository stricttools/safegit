package coord

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/sequencer"
	"github.com/smm-h/safegit/internal/testutil"
)

// Every kind the reader can report must have a way out, and it must be the
// right one. The pairs are asserted individually rather than as a table of
// "some non-empty string" because the whole value of the advice is that it
// names a command that actually finishes THIS state: an operator sent to
// `git rebase --continue` mid-`git am` gets a refusal from git, not a
// conclusion.
func TestWayOutNamesTheCommandThatEndsEachState(t *testing.T) {
	for _, tc := range []struct {
		kind     sequencer.Kind
		conclude string
		abandon  string
	}{
		{sequencer.KindMerge, "safegit merge-continue", "git merge --abort"},
		{sequencer.KindCherryPick, "safegit cherry-pick-continue", "git cherry-pick --abort"},
		{sequencer.KindRevert, "safegit revert-continue", "git revert --abort"},
		{sequencer.KindRebase, "git rebase --continue", "git rebase --abort"},
		{sequencer.KindAM, "git am --continue", "git am --abort"},
	} {
		w := WayOutOf(sequencer.State{Kind: tc.kind})
		if w.Conclude != tc.conclude {
			t.Errorf("%s: Conclude = %q, want %q", tc.kind, w.Conclude, tc.conclude)
		}
		if w.Abandon != tc.abandon {
			t.Errorf("%s: Abandon = %q, want %q", tc.kind, w.Abandon, tc.abandon)
		}
	}

	// A mailbox application must never be told to use git's rebase commands,
	// even though the two share .git/rebase-apply.
	am := WayOutOf(sequencer.State{Kind: sequencer.KindAM})
	if strings.Contains(am.Conclude, "rebase") || strings.Contains(am.Abandon, "rebase") {
		t.Errorf("a git am was advised to run a rebase command: %+v", am)
	}

	if w := WayOutOf(sequencer.State{Kind: sequencer.KindNone}); w.Conclude != "" || w.Abandon != "" {
		t.Errorf("KindNone has a way out: %+v", w)
	}
}

// A refusal states what is in flight and how to end it. Both halves are
// required: naming the state without the way out leaves an operator stuck, and
// the way out without the state leaves them guessing which operation it is
// about.
func TestRefusalNamesTheStateAndTheWayOut(t *testing.T) {
	state := sequencer.State{Kind: sequencer.KindMerge, MergeHeads: []string{strings.Repeat("a", 40)}}
	msg := RefuseInFlight("commit", state)

	for _, want := range []string{"refusing commit", "merge", "aaaaaaaa", "safegit merge-continue", "git merge --abort"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal is missing %q:\n%s", want, msg)
		}
	}
}

// The declared context is the whole mechanism that lets a conclusion command
// commit during the state everyone else is refused for -- and it is checked,
// not trusted.
func TestGuardInFlightHonorsOnlyAMatchingDeclaration(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.WriteFile(t, dir, "f.txt", "base\n")
	testutil.Git(t, dir, "add", "f.txt")
	testutil.Git(t, dir, "commit", "-m", "base")
	testutil.Git(t, dir, "checkout", "-q", "-b", "side")
	testutil.WriteFile(t, dir, "f.txt", "side\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "side")
	testutil.Git(t, dir, "checkout", "-q", "main")
	testutil.WriteFile(t, dir, "f.txt", "main\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "main")
	if _, code := testutil.GitTry(t, dir, "merge", "side"); code == 0 {
		t.Fatal("the fixture needs a conflicted merge; the merge succeeded")
	}
	gitDir := filepath.Join(dir, ".git")

	// No declaration: refused, with a typed error carrying the state.
	err := GuardInFlight(gitDir, "commit", nil)
	var inflight *InFlightError
	if !errors.As(err, &inflight) {
		t.Fatalf("an undeclared commit mid-merge returned %v, want an *InFlightError", err)
	}
	if inflight.State.Kind != sequencer.KindMerge {
		t.Errorf("the refusal carries kind %s, want merge", inflight.State.Kind)
	}
	if !strings.Contains(inflight.Error(), "merge") {
		t.Errorf("the refusal does not name the merge: %s", inflight.Error())
	}

	// A matching declaration passes: this is the seam the conclusion commands
	// commit through.
	if err := GuardInFlight(gitDir, "merge-continue", &SequencerContext{Kind: sequencer.KindMerge}); err != nil {
		t.Errorf("a declared merge conclusion was refused mid-merge: %v", err)
	}

	// A declaration for a different operation is itself a refusal.
	if err := GuardInFlight(gitDir, "cherry-pick-continue", &SequencerContext{Kind: sequencer.KindCherryPick}); err == nil {
		t.Error("a cherry-pick conclusion was allowed to run mid-merge")
	}
}

// Nothing in flight: an ordinary caller passes, and a declaration that names an
// operation git is not running is refused rather than silently ignored.
func TestGuardInFlightOnAQuietRepository(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.WriteFile(t, dir, "f.txt", "base\n")
	testutil.Git(t, dir, "add", "f.txt")
	testutil.Git(t, dir, "commit", "-m", "base")
	gitDir := filepath.Join(dir, ".git")

	if err := GuardInFlight(gitDir, "commit", nil); err != nil {
		t.Errorf("an ordinary commit was refused with nothing in flight: %v", err)
	}
	if err := GuardInFlight(gitDir, "merge-continue", &SequencerContext{Kind: sequencer.KindMerge}); err == nil {
		t.Error("a merge conclusion was allowed with no merge in progress")
	}
}

// A state file safegit cannot parse is not evidence that nothing is in flight,
// so the guard refuses instead of proceeding.
func TestGuardInFlightRefusesAnUnreadableState(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	gitDir := filepath.Join(dir, ".git")
	testutil.WriteFileAt(t, filepath.Join(gitDir, sequencer.FileMergeHead), "not-an-object-name\n")

	err := GuardInFlight(gitDir, "commit", nil)
	if err == nil {
		t.Fatal("a corrupt MERGE_HEAD was read as nothing in flight")
	}
	var inflight *InFlightError
	if errors.As(err, &inflight) {
		t.Errorf("an unreadable state was reported as a known operation: %v", err)
	}
}

// The dirty-tree refusal changes its advice mid-operation: "commit your work"
// is impossible to follow while the dirt IS the operation's conflict, so the
// refusal names the operation and the command that ends it instead.
func TestRefuseSwitchesAdviceWhileAnOperationIsInFlight(t *testing.T) {
	dirty := &DirtyState{ModifiedFiles: []string{"M  conflicted.txt"}}

	ordinary := dirty.Refuse("checkout")
	if !strings.Contains(ordinary, "working tree is not clean") || !strings.Contains(ordinary, "safegit commit") {
		t.Errorf("the ordinary refusal lost its shape:\n%s", ordinary)
	}

	dirty.Sequencer = sequencer.State{Kind: sequencer.KindCherryPick, Source: strings.Repeat("b", 40)}
	midOperation := dirty.Refuse("checkout")
	if strings.Contains(midOperation, "safegit commit -m") {
		t.Errorf("mid-cherry-pick refusal still advises a commit that safegit itself refuses:\n%s", midOperation)
	}
	for _, want := range []string{"cherry-pick", "safegit cherry-pick-continue", "conflicted.txt"} {
		if !strings.Contains(midOperation, want) {
			t.Errorf("mid-cherry-pick refusal is missing %q:\n%s", want, midOperation)
		}
	}
}
