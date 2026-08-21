package sequencer_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/sequencer"
)

// The declared sets, restated here rather than derived from Paths: a test that
// asks the implementation what it owns cannot notice the implementation
// changing what it owns.
var declaredSets = map[sequencer.Kind][]string{
	sequencer.KindMerge: {
		sequencer.FileMergeHead, sequencer.FileMergeMode,
		sequencer.FileMergeMsg, sequencer.FileAutoMerge,
	},
	sequencer.KindCherryPick: {
		sequencer.FileCherryPickHead, sequencer.FileMergeMsg,
		sequencer.FileAutoMerge, sequencer.DirSequencer,
	},
	sequencer.KindRevert: {
		sequencer.FileRevertHead, sequencer.FileMergeMsg,
		sequencer.FileAutoMerge, sequencer.DirSequencer,
	},
}

func TestPathsMatchesTheDeclaredSets(t *testing.T) {
	for kind, want := range declaredSets {
		got := sequencer.Paths(kind)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("Paths(%v) = %v, want %v", kind, got, want)
		}
	}
	for _, kind := range []sequencer.Kind{sequencer.KindNone, sequencer.KindRebase, sequencer.KindAM} {
		if got := sequencer.Paths(kind); got != nil {
			t.Errorf("Paths(%v) = %v, want nil: safegit owns no set there", kind, got)
		}
	}
}

// The core cleanup contract: everything in the operation's set is on disk
// before, exactly that set is gone after, and files that belong to a DIFFERENT
// operation are still there.
func TestCleanupRemovesExactlyTheMergeSet(t *testing.T) {
	dir, _ := conflictedMerge(t)
	gd := gitDir(dir)

	mustBePresent(t, gd, declaredSets[sequencer.KindMerge]...)

	// Residue from a different context, which a merge conclusion has no
	// business touching.
	strays := []string{sequencer.FileCherryPickHead, sequencer.FileRevertHead}
	for _, name := range strays {
		writeStray(t, gd, name)
	}
	strayQueue := filepath.Join(gd, sequencer.DirSequencer)
	if err := os.MkdirAll(strayQueue, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := sequencer.Cleanup(gd, sequencer.KindMerge); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	mustBeAbsent(t, gd, declaredSets[sequencer.KindMerge]...)
	mustBePresent(t, gd, strays...)
	mustBePresent(t, gd, sequencer.DirSequencer)

	// ORIG_HEAD is git's record of where the branch was, not merge state; git
	// leaves it behind on a concluded merge and so must Cleanup.
	mustBePresent(t, gd, "ORIG_HEAD")
}

func TestCleanupRemovesExactlyTheCherryPickSetIncludingTheQueue(t *testing.T) {
	dir, _ := queuedCherryPick(t)
	gd := gitDir(dir)

	mustBePresent(t, gd, declaredSets[sequencer.KindCherryPick]...)

	strays := []string{sequencer.FileMergeHead, sequencer.FileMergeMode, sequencer.FileRevertHead}
	for _, name := range strays {
		writeStray(t, gd, name)
	}

	if err := sequencer.Cleanup(gd, sequencer.KindCherryPick); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	mustBeAbsent(t, gd, declaredSets[sequencer.KindCherryPick]...)
	mustBePresent(t, gd, strays...)
}

func TestCleanupRemovesExactlyTheRevertSet(t *testing.T) {
	dir, _ := singleRevert(t)
	gd := gitDir(dir)

	mustBePresent(t, gd,
		sequencer.FileRevertHead, sequencer.FileMergeMsg, sequencer.FileAutoMerge)

	strays := []string{sequencer.FileMergeHead, sequencer.FileCherryPickHead}
	for _, name := range strays {
		writeStray(t, gd, name)
	}

	if err := sequencer.Cleanup(gd, sequencer.KindRevert); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	mustBeAbsent(t, gd, declaredSets[sequencer.KindRevert]...)
	mustBePresent(t, gd, strays...)
}

// After the set is gone the reader agrees: nothing is in flight. This is the
// property the conclusion commands depend on -- a subsequent commit must not be
// refused by a state that was supposed to be concluded.
func TestReadReportsNoneAfterCleanup(t *testing.T) {
	for name, fixture := range map[string]func(*testing.T) (string, string){
		"merge":              func(t *testing.T) (string, string) { return conflictedMerge(t) },
		"single cherry-pick": singleCherryPick,
		"queued cherry-pick": queuedCherryPick,
		"single revert":      singleRevert,
		"queued revert":      queuedRevert,
	} {
		t.Run(name, func(t *testing.T) {
			dir, _ := fixture(t)
			gd := gitDir(dir)

			before, err := sequencer.Read(gd)
			if err != nil {
				t.Fatalf("Read before: %v", err)
			}
			if !before.InProgress() {
				t.Fatalf("the fixture left nothing in flight")
			}

			if err := sequencer.Cleanup(gd, before.Kind); err != nil {
				t.Fatalf("Cleanup: %v", err)
			}

			after, err := sequencer.Read(gd)
			if err != nil {
				t.Fatalf("Read after: %v", err)
			}
			if after.Kind != sequencer.KindNone {
				t.Fatalf("Read after Cleanup reports %v, want none", after.Kind)
			}
		})
	}
}

// git does not write every file of a set every time -- an octopus merge leaves
// no AUTO_MERGE on the git versions that do not write one -- and a concluded
// operation is concluded either way.
//
// This is the one genuinely git-version-conditional assertion in the file, so
// it is the only thing here that skips. Idempotence, which needs no absent
// member to hold, is a test of its own below and always runs.
func TestCleanupToleratesAbsentMembersOfTheSet(t *testing.T) {
	dir, _ := octopusMerge(t)
	gd := gitDir(dir)

	if present(t, gd, sequencer.FileAutoMerge) {
		t.Skip("this git writes AUTO_MERGE for an octopus merge, so no member of the set is absent to tolerate")
	}

	if err := sequencer.Cleanup(gd, sequencer.KindMerge); err != nil {
		t.Fatalf("Cleanup with AUTO_MERGE absent: %v", err)
	}
	mustBeAbsent(t, gd, declaredSets[sequencer.KindMerge]...)
}

// Cleanup on a repository whose set is already gone removes nothing and reports
// no error. A conclusion that is retried, or a second caller arriving after the
// first finished, must not fail on the absence of what it came to remove.
func TestCleanupIsIdempotent(t *testing.T) {
	dir, _ := conflictedMerge(t)
	gd := gitDir(dir)

	mustBePresent(t, gd, declaredSets[sequencer.KindMerge]...)

	if err := sequencer.Cleanup(gd, sequencer.KindMerge); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	mustBeAbsent(t, gd, declaredSets[sequencer.KindMerge]...)

	// And again, with the whole set already gone.
	if err := sequencer.Cleanup(gd, sequencer.KindMerge); err != nil {
		t.Fatalf("Cleanup on an already-clean repository: %v", err)
	}
	mustBeAbsent(t, gd, declaredSets[sequencer.KindMerge]...)

	// The reader still agrees, which is what a retried conclusion depends on.
	state, err := sequencer.Read(gd)
	if err != nil {
		t.Fatalf("Read after a repeated Cleanup: %v", err)
	}
	if state.Kind != sequencer.KindNone {
		t.Fatalf("Read after a repeated Cleanup reports %v, want none", state.Kind)
	}
}

func TestCleanupRefusesTheKindsSafegitDoesNotOwn(t *testing.T) {
	for name, tc := range map[string]struct {
		fixture func(*testing.T) string
		kind    sequencer.Kind
		want    string
	}{
		"rebase, merge backend": {
			fixture: func(t *testing.T) string { d, _ := rebaseMergeBackend(t); return d },
			kind:    sequencer.KindRebase,
			want:    "does not own",
		},
		"rebase, apply backend": {
			fixture: func(t *testing.T) string { d, _ := rebaseApplyBackend(t); return d },
			kind:    sequencer.KindRebase,
			want:    "does not own",
		},
		"git am": {
			fixture: mailboxApplication,
			kind:    sequencer.KindAM,
			want:    "does not own",
		},
		"nothing in flight": {
			fixture: newRepo,
			kind:    sequencer.KindNone,
			want:    "no operation in progress",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := tc.fixture(t)
			gd := gitDir(dir)

			// Everything the refusal must leave alone, sampled before.
			watched := []string{
				sequencer.DirRebaseMerge, sequencer.DirRebaseApply,
				sequencer.FileMergeHead, sequencer.FileMergeMsg, sequencer.FileAutoMerge,
			}
			before := map[string]bool{}
			for _, name := range watched {
				before[name] = present(t, gd, name)
			}

			err := sequencer.Cleanup(gd, tc.kind)
			if err == nil {
				t.Fatalf("Cleanup(%v) was accepted", tc.kind)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}

			// A refusal removes nothing.
			for _, name := range watched {
				if present(t, gd, name) != before[name] {
					t.Fatalf("%s changed across a refused Cleanup", name)
				}
			}
			if tc.kind == sequencer.KindRebase || tc.kind == sequencer.KindAM {
				state, err := sequencer.Read(gd)
				if err != nil {
					t.Fatalf("Read after a refused Cleanup: %v", err)
				}
				if state.Kind != tc.kind {
					t.Fatalf("a refused Cleanup changed the state to %v", state.Kind)
				}
			}
		})
	}
}

// writeStray plants residue that does not belong to the operation being cleaned
// up, with content git would have written, so a cleanup that removed it would
// be removing a plausible file rather than an obvious marker.
func writeStray(t *testing.T, gitDirPath, name string) {
	t.Helper()
	const sha = "0123456789abcdef0123456789abcdef01234567"
	if err := os.WriteFile(filepath.Join(gitDirPath, name), []byte(sha+"\n"), 0o644); err != nil {
		t.Fatalf("planting %s: %v", name, err)
	}
}
