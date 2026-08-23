package test

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// FINDING (new ruling): a stage resolution silently destroys unsaved work in the
// working tree.
//
// `--resolve path=ours` (and `=theirs`) writes the chosen stage blob over the
// file on disk, which is git's own `checkout --ours` idiom. But safegit does it
// without looking at what is being overwritten: an operator who edited the
// conflicted file by hand and then chose a stage loses that edit outright, and
// the content is in no commit, no stage and no stash, so it is unrecoverable.
// The report even calls it "written with the resolved content", which is true of
// the file and says nothing about what was there.
//
// RULED TARGET: refuse, naming the file and suggesting `worktree`. A dedicated
// override flag is to exist later; the refusal is what is pinned here, and the
// flag is deliberately not tested yet.

// TestStageResolutionRefusesToOverwriteAHandEdit: the file on disk matches no
// stage of the conflict, so overwriting it destroys content held nowhere else.
func TestStageResolutionRefusesToOverwriteAHandEdit(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true})
	before := testutil.Rev(t, fx.dir, "HEAD")

	// A hand-resolution: plausible content, no conflict markers, and equal to
	// nothing git has.
	const handEdited = "line1\nhand-resolved by the operator\nline3\n"
	testutil.WriteFile(t, fx.dir, "conflicted.txt", handEdited)

	// Verified rather than assumed: it matches none of the three stages, which is
	// the whole precondition of the finding.
	for stage, name := range map[string]string{":1:": "base", ":2:": "ours", ":3:": "theirs"} {
		out, ok := testutil.GitTryOut(t, fx.dir, "show", stage+"conflicted.txt")
		if !ok {
			continue // a side that has no stage cannot be what is on disk
		}
		if out == handEdited {
			t.Fatalf("the fixture's hand-edit equals the %s stage; it must match no stage for the overwrite to destroy anything", name)
		}
	}

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=ours")
	if code == 0 {
		t.Errorf("the conclusion overwrote a hand-edit that matches no stage and exited 0:\nstdout=%s", stdout)
	}
	if head := testutil.Rev(t, fx.dir, "HEAD"); head != before {
		t.Errorf("HEAD moved to %s (was %s); the refusal must come before the commit", head, before)
	}
	if got := readWorktree(t, fx.dir, "conflicted.txt"); got != handEdited {
		t.Errorf("conflicted.txt holds %q, want the hand-edit %q that exists nowhere else", got, handEdited)
	}
	if !strings.Contains(stderr, "conflicted.txt") {
		t.Errorf("the refusal does not name the file whose content would be destroyed:\n%s", stderr)
	}
	if !strings.Contains(stderr, "worktree") {
		t.Errorf("the refusal does not point at the resolution that keeps the hand-edit (=worktree):\n%s", stderr)
	}
}
