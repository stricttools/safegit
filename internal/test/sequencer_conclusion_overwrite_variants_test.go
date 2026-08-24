package test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// The rest of the overwrite refusal: the shapes the headline case
// (sequencer_conclusion_overwrite_test.go) does not reach.
//
// The rule is one rule -- a conclusion refuses to destroy working-tree content
// that matches neither an index stage nor the blob git itself wrote into the
// file -- and these are the places it has to hold: a resolution that DELETES
// rather than writes, a stage keyword whose stage is absent (which deletes too),
// the election that goes through with it anyway, and the conflict shape whose
// untouched emission must NOT be mistaken for a hand edit.

// handEdited is content that is plausible, marker-free and equal to nothing git
// has for the fixture's conflicted path.
const handEdited = "line1\nhand-resolved by the operator\nline3\n"

// TestDiscardUnmatchedWorktreeElectsTheDestruction: the flag is the only way to
// say "yes, throw my hand edit away", and it says so on the way past.
func TestDiscardUnmatchedWorktreeElectsTheDestruction(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true})
	testutil.WriteFile(t, fx.dir, "conflicted.txt", handEdited)

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=ours", "--discard-unmatched-worktree")
	if code != 0 {
		t.Fatalf("the elected conclusion failed (code %d): %s\n%s", code, stderr, stdout)
	}

	if got := readWorktree(t, fx.dir, "conflicted.txt"); got != "line1\nmain\nline3\n" {
		t.Errorf("conflicted.txt = %q, want the ours content the election chose", got)
	}
	if !strings.Contains(stderr, "conflicted.txt") {
		t.Errorf("the election does not name the file whose content it destroyed:\n%s", stderr)
	}
	if parents := testutil.Parents(t, fx.dir, testutil.Rev(t, fx.dir, "HEAD")); len(parents) != 2 {
		t.Errorf("HEAD has parents %v, want the merge commit the conclusion made", parents)
	}
}

// TestDeleteResolutionRefusesToDestroyAHandEdit: `delete` removes the file from
// disk, so it destroys a hand edit exactly as a stage write does. The marker
// verification never sees this path at all -- a deleted path contributes no
// content to the commit -- which is why the check is a pass of its own.
func TestDeleteResolutionRefusesToDestroyAHandEdit(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true})
	before := testutil.Rev(t, fx.dir, "HEAD")
	testutil.WriteFile(t, fx.dir, "conflicted.txt", handEdited)

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=delete")
	if code != exitcode.ConclusionWouldOverwrite {
		t.Errorf("exit %d, want %d (the overwrite refusal)\nstdout=%s\nstderr=%s",
			code, exitcode.ConclusionWouldOverwrite, stdout, stderr)
	}
	if head := testutil.Rev(t, fx.dir, "HEAD"); head != before {
		t.Errorf("HEAD moved to %s (was %s); the refusal must come before the commit", head, before)
	}
	if got := readWorktree(t, fx.dir, "conflicted.txt"); got != handEdited {
		t.Errorf("conflicted.txt holds %q, want the hand-edit %q left untouched", got, handEdited)
	}
	if !strings.Contains(stderr, "conflicted.txt") || !strings.Contains(stderr, "worktree") {
		t.Errorf("the refusal does not name the file and the resolution that keeps it:\n%s", stderr)
	}
}

// TestAbsentStageResolutionRefusesToDestroyAHandEdit: `theirs` on a
// modify/delete conflict resolves to the side that deleted the path, so the
// materialization is a DELETION. The keyword says "write a stage" and the effect
// is a removal, which is exactly the case a keyword-shaped check would miss.
func TestAbsentStageResolutionRefusesToDestroyAHandEdit(t *testing.T) {
	fx := newModifyDeleteMergeRepo(t)
	before := testutil.Rev(t, fx.dir, "HEAD")
	testutil.WriteFile(t, fx.dir, fx.path, handEdited)

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", fx.path+"=theirs")
	if code != exitcode.ConclusionWouldOverwrite {
		t.Errorf("exit %d, want %d (the overwrite refusal)\nstdout=%s\nstderr=%s",
			code, exitcode.ConclusionWouldOverwrite, stdout, stderr)
	}
	if head := testutil.Rev(t, fx.dir, "HEAD"); head != before {
		t.Errorf("HEAD moved to %s (was %s); the refusal must come before the commit", head, before)
	}
	if got := readWorktree(t, fx.dir, fx.path); got != handEdited {
		t.Errorf("%s holds %q, want the hand-edit %q left untouched", fx.path, got, handEdited)
	}
}

// numberedLines builds a file of n numbered lines, with the given 1-based lines
// replaced. It gives a fixture enough shared content for git's rename detection
// to pair a moved file with its source.
func numberedLines(n int, replaced map[int]string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if text, ok := replaced[i]; ok {
			fmt.Fprintf(&b, "%s\n", text)
			continue
		}
		fmt.Fprintf(&b, "%d\n", i)
	}
	return b.String()
}

// TestRenameMediatedConflictsUntouchedEmissionIsNotAHandEdit is the arm the
// accepted set exists for.
//
// git labels the markers of a rename-mediated conflict with the PATH each side
// carried ("HEAD:old.txt"), and no reproduction from the index stages can
// recover those labels -- a check that compared against a reconstruction would
// call git's own untouched output a hand edit and refuse a perfectly ordinary
// conclusion. The blob is therefore read verbatim out of AUTO_MERGE.
func TestRenameMediatedConflictsUntouchedEmissionIsNotAHandEdit(t *testing.T) {
	dir := newRepo(t)

	// The file is long and the edits are one line of it: git pairs a move with
	// its source by CONTENT SIMILARITY, and a three-line file whose middle line
	// both sides rewrite is not similar enough -- it comes out as an unrelated
	// add beside a modify/delete, which is a different conflict shape entirely
	// (probed).
	base := numberedLines(20, map[int]string{})
	testutil.WriteFile(t, dir, "old.txt", base)
	safegitCommitEnv(t, dir, conclusionSession, "base", "old.txt")

	// feature MOVES the file and edits it; main edits it where it was. git
	// pairs the two up and reports the conflict at the new path, with the old
	// one named on the marker line.
	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "-q", "feature")
	testutil.Git(t, dir, "mv", "old.txt", "new.txt")
	testutil.WriteFile(t, dir, "new.txt", numberedLines(20, map[int]string{10: "feature"}))
	testutil.Git(t, dir, "commit", "-q", "-am", "feature moves and edits the file")

	testutil.Git(t, dir, "switch", "-q", "main")
	testutil.WriteFile(t, dir, "old.txt", numberedLines(20, map[int]string{10: "main"}))
	safegitCommitEnv(t, dir, conclusionSession, "main edits the file", "old.txt")

	stdout, stderr, code := runSafegitEnv(t, dir, conclusionSession, "merge", "feature")
	if code == 0 {
		t.Fatalf("the merge was expected to conflict\nstdout=%s stderr=%s", stdout, stderr)
	}

	// The fixture's whole point: git's emission carries a path-suffixed label,
	// which is what no reconstruction can reproduce.
	emitted := readWorktree(t, dir, "new.txt")
	if !strings.Contains(emitted, ":old.txt") && !strings.Contains(emitted, ":new.txt") {
		t.Fatalf("the fixture must produce path-suffixed marker labels; git wrote:\n%s", emitted)
	}

	// Untouched: the file on disk is exactly what git put there.
	stdout, stderr, code = runSafegitEnv(t, dir, conclusionSession,
		"merge-continue", "--resolve", "new.txt=ours")
	if code != 0 {
		t.Fatalf("the conclusion refused git's own untouched emission (code %d): %s\n%s", code, stderr, stdout)
	}
	if !testutil.FileExists(filepath.Join(dir, "new.txt")) {
		t.Error("new.txt is gone after a conclusion that resolved it to a stage")
	}
}
