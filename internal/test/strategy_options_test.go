package test

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// Strategy OPTIONS on merge, cherry-pick and revert.
//
// Strategy SELECTION (`-s`/`--strategy`) stays refused everywhere: safegit's
// conclusion protections are written against what the ort strategy stages, and
// another strategy parks a content conflict with no AUTO_MERGE for them to read.
// Strategy OPTIONS are the other half of git's `-X` vocabulary and do not have
// that property: they tune the ort compute's content decisions and change
// neither the authorship, the parking, nor anything a conclusion reads.
//
// So they are honored, and these tests hold that promise to both ends of it:
// the real compute obeys the option, and the PREVIEW computes the very same
// tree rather than the unoptioned one -- which would be an answer to a
// different command line.

// newConflictingRevertRepo builds a repository whose HEAD~1 cannot be reverted
// cleanly: the commit after it edited the same line again, so the inverse patch
// collides with what is there now.
//
// Reverting resolves the collision the other way round from a pick: the revert
// applies an INVERSE patch, so `theirs` is what the reverted commit's PARENT
// held. `-X theirs` therefore keeps the revert.
func newConflictingRevertRepo(t *testing.T) (dir, target string) {
	t.Helper()
	dir = newRepo(t)

	testutil.WriteFile(t, dir, "c.txt", "l1\nbase\nl3\n")
	safegitCommit(t, dir, "base", "c.txt")

	testutil.WriteFile(t, dir, "c.txt", "l1\nfirst\nl3\n")
	target = safegitCommit(t, dir, "the change to revert", "c.txt")

	testutil.WriteFile(t, dir, "c.txt", "l1\nsecond\nl3\n")
	safegitCommit(t, dir, "the change on top of it", "c.txt")
	return dir, target
}

// TestStrategyOptionsAreHonoredOnAllThreeVerbs: a compute that would conflict
// resolves under a strategy option, on every verb that computes with git's
// merge engine, and the result is one pipeline-authored commit.
func TestStrategyOptionsAreHonoredOnAllThreeVerbs(t *testing.T) {
	t.Run("merge", func(t *testing.T) {
		fx := newPreviewRepo(t)
		if _, stderr, code := runSafegit(t, fx.dir, "merge", "-X", "ours", "side"); code != 0 {
			t.Fatalf("merge -X ours was refused (code %d): %s", code, stderr)
		}
		if got := testutil.MustShow(t, fx.dir, "HEAD", "c.txt"); got != "l1\nmain\nl3\n" {
			t.Errorf("-X ours did not keep our side of the conflict: %q", got)
		}
		if parents := testutil.Parents(t, fx.dir, "HEAD"); len(parents) != 2 {
			t.Errorf("the merge produced %d parent(s), want 2", len(parents))
		}
	})

	t.Run("cherry-pick", func(t *testing.T) {
		dir, source := newConflictingPickRepo(t)
		if _, stderr, code := runSafegitEnv(t, dir, pickSession, "cherry-pick", "-X", "theirs", source); code != 0 {
			t.Fatalf("cherry-pick -X theirs was refused (code %d): %s", code, stderr)
		}
		if got := testutil.MustShow(t, dir, "HEAD", "c.txt"); got != "l1\nside\nl3\n" {
			t.Errorf("-X theirs did not take the picked side of the conflict: %q", got)
		}
	})

	t.Run("revert", func(t *testing.T) {
		dir, target := newConflictingRevertRepo(t)
		if _, stderr, code := runSafegit(t, dir, "revert", "-X", "theirs", target); code != 0 {
			t.Fatalf("revert -X theirs was refused (code %d): %s", code, stderr)
		}
		if got := testutil.MustShow(t, dir, "HEAD", "c.txt"); got != "l1\nbase\nl3\n" {
			t.Errorf("-X theirs did not carry the revert through: %q", got)
		}
	})
}

// TestAConflictUnderAStrategyOptionParksAndConcludes: an option that does not
// resolve the collision changes nothing about how the operation parks or how it
// is concluded. The protections over the staged result see exactly what they
// see without one.
func TestAConflictUnderAStrategyOptionParksAndConcludes(t *testing.T) {
	fx := newPreviewRepo(t)

	_, stderr, code := runSafegit(t, fx.dir, "merge", "-X", "ignore-space-change", "side")
	if code == 0 {
		t.Fatalf("the conflicting merge did not park: %s", stderr)
	}
	testutil.AssertMergeHead(t, fx.dir, testutil.Rev(t, fx.dir, "side"),
		"a merge conflicted under a strategy option must park exactly as one without it does")

	testutil.WriteFile(t, fx.dir, "c.txt", "l1\nresolved by hand\nl3\n")
	if _, stderr, code := runSafegit(t, fx.dir, "merge-continue", "--resolve", "c.txt=worktree"); code != 0 {
		t.Fatalf("merge-continue could not conclude the parked merge (code %d): %s", code, stderr)
	}
	if parents := testutil.Parents(t, fx.dir, "HEAD"); len(parents) != 2 {
		t.Errorf("the concluded merge has %d parent(s), want 2", len(parents))
	}
}

// mergeTreeTakesStrategyOptions probes whether THIS git's merge-tree accepts
// `-X`, which it learned in 2.43. The preview's forwarding cannot be pinned on
// an older one, and the pins below skip rather than fail there -- the
// capability is probed, never derived from a parsed version number.
func mergeTreeTakesStrategyOptions(t *testing.T, dir string) bool {
	t.Helper()
	// HEAD against itself: the question is whether the OPTION parses, and a
	// merge of a commit with itself needs no second branch in the fixture.
	_, ok := testutil.GitTryOut(t, dir, "merge-tree", "--write-tree", "-X", "ours", "HEAD", "HEAD")
	return ok
}

// TestThePreviewForwardsStrategyOptions is the property the allowance turns on:
// a preview of a command line carrying strategy options computes the tree that
// command line really produces. Previewing the UNOPTIONED merge instead would
// be a silent answer to a different question.
func TestThePreviewForwardsStrategyOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []string
	}{
		{"one option", []string{"-X", "ours"}},
		// BOTH occurrences must be forwarded: a reader that took only the first
		// would silently drop the second.
		{"two options", []string{"-X", "ours", "-X", "ignore-space-change"}},
		// The long spelling is a DIFFERENT option name, not an alias the argv
		// reader folds into the short one.
		{"the long spelling", []string{"--strategy-option", "ours"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newPreviewRepo(t)
			if !mergeTreeTakesStrategyOptions(t, fx.dir) {
				t.Skip("this git's merge-tree does not take -X (it learned the option in 2.43)")
			}

			args := append([]string{"--dry-run", "merge"}, tc.opts...)
			args = append(args, "side")
			stdout, stderr, code := runSafegit(t, fx.dir, args...)
			if code != 0 {
				t.Fatalf("the preview failed (code %d): %s", code, stderr)
			}
			previewed := previewTree(t, stdout)
			if previewed == "" {
				t.Fatalf("the preview reported no tree, so the option was not honored:\n%s", stdout)
			}

			real := append([]string{"merge"}, tc.opts...)
			real = append(real, "side")
			if _, stderr, code := runSafegit(t, fx.dir, real...); code != 0 {
				t.Fatalf("the real merge failed (code %d): %s", code, stderr)
			}
			if got := testutil.Rev(t, fx.dir, "HEAD^{tree}"); got != previewed {
				t.Errorf("the preview computed %s and the real merge produced %s", previewed, got)
			}
		})
	}
}

// TestThePreviewForwardsStrategyOptionsOnAReplay is the same property for the
// two replay verbs, whose preview builder is the other one.
func TestThePreviewForwardsStrategyOptionsOnAReplay(t *testing.T) {
	t.Run("cherry-pick", func(t *testing.T) {
		dir, source := newConflictingPickRepo(t)
		if !mergeTreeTakesStrategyOptions(t, dir) {
			t.Skip("this git's merge-tree does not take -X (it learned the option in 2.43)")
		}
		stdout, stderr, code := runSafegitEnv(t, dir, pickSession, "--dry-run", "cherry-pick", "-X", "theirs", source)
		if code != 0 {
			t.Fatalf("the preview failed (code %d): %s", code, stderr)
		}
		previewed := previewTree(t, stdout)
		if previewed == "" {
			t.Fatalf("the preview reported no tree:\n%s", stdout)
		}
		if _, stderr, code := runSafegitEnv(t, dir, pickSession, "cherry-pick", "-X", "theirs", source); code != 0 {
			t.Fatalf("the real pick failed (code %d): %s", code, stderr)
		}
		if got := testutil.Rev(t, dir, "HEAD^{tree}"); got != previewed {
			t.Errorf("the preview computed %s and the real pick produced %s", previewed, got)
		}
	})

	t.Run("revert", func(t *testing.T) {
		dir, target := newConflictingRevertRepo(t)
		if !mergeTreeTakesStrategyOptions(t, dir) {
			t.Skip("this git's merge-tree does not take -X (it learned the option in 2.43)")
		}
		stdout, stderr, code := runSafegit(t, dir, "--dry-run", "revert", "-X", "theirs", target)
		if code != 0 {
			t.Fatalf("the preview failed (code %d): %s", code, stderr)
		}
		previewed := previewTree(t, stdout)
		if previewed == "" {
			t.Fatalf("the preview reported no tree:\n%s", stdout)
		}
		if _, stderr, code := runSafegit(t, dir, "revert", "-X", "theirs", target); code != 0 {
			t.Fatalf("the real revert failed (code %d): %s", code, stderr)
		}
		if got := testutil.Rev(t, dir, "HEAD^{tree}"); got != previewed {
			t.Errorf("the preview computed %s and the real revert produced %s", previewed, got)
		}
	})
}

// TestStrategySelectionIsStillRefusedUnderTheOptionAllowance: the two halves of
// git's strategy vocabulary part company here, and the refusal that stays must
// not be softened by the allowance beside it.
func TestStrategySelectionIsStillRefusedUnderTheOptionAllowance(t *testing.T) {
	for _, args := range [][]string{
		{"merge", "-s", "resolve", "side"},
		{"merge", "--strategy", "resolve", "side"},
		{"cherry-pick", "--strategy", "resolve", "side"},
		{"revert", "--strategy", "resolve", "HEAD"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			fx := newPreviewRepo(t)
			stderr := assertSubsetRefusal(t, fx.dir, args...)
			if !strings.Contains(stderr, "strateg") {
				t.Errorf("the refusal does not name the strategy capability:\n%s", stderr)
			}
		})
	}
}

// TestSignoffIsStillTheShortSpellingOnAReplay guards the asymmetry the option
// allowance had to preserve: on cherry-pick and revert, `-s` is SIGNOFF and not
// strategy selection, and it was allowed before this change and after it.
func TestSignoffIsStillTheShortSpellingOnAReplay(t *testing.T) {
	dir, first, _ := newPickableRepo(t)
	if _, stderr, code := runSafegitEnv(t, dir, pickSession, "cherry-pick", "-s", first); code != 0 {
		t.Fatalf("cherry-pick -s (signoff) was refused (code %d): %s", code, stderr)
	}
	if msg := testutil.GitOut(t, dir, "log", "-1", "--format=%B"); !strings.Contains(msg, "Signed-off-by:") {
		t.Errorf("the pick did not carry the signoff trailer:\n%s", msg)
	}
}
