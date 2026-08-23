package test

import (
	"path/filepath"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// FINDING: on the QUEUED delegation path a `--resolve path=delete` destroys the
// working-tree file BEFORE git has committed anything.
//
// delegateQueuedSequence (sequencer_delegate.go) calls materializeResolutions
// ahead of `git <verb> --continue`, because git checks the working tree against
// the index it was handed. For `ours` and `theirs` that is harmless -- the
// content it writes is a stage blob, still in the object store. For `delete` it
// is not: the file is unlinked, and if git's own `--continue` then fails -- a
// commit-msg hook that refuses is enough -- nothing was committed, the operation
// is still in flight, and the operator's hand-edited content is gone from the
// only place it existed.
//
// RULED TARGET: the delete arm runs only after git's --continue succeeds.

// TestQueuedDeleteDoesNotDestroyTheFileBeforeGitCommits: git refuses the commit,
// so the file the operator hand-edited must still be there.
func TestQueuedDeleteDoesNotDestroyTheFileBeforeGitCommits(t *testing.T) {
	fx := newQueuedPickRepo(t, "cherry-pick")

	// Content that exists in exactly one place: the working tree. It is not any
	// stage of the conflict and it is in no commit, so a delete that runs before
	// the commit makes it unrecoverable.
	const handEdited = "content that exists nowhere but this file\n"
	testutil.WriteFile(t, fx.dir, fx.conflicted, handEdited)

	// The repository saying no, after safegit's own checks have passed: git's
	// `cherry-pick --continue` runs the commit-msg hook for the step it is
	// concluding, and a refusal there aborts the commit.
	installHook(t, fx.dir, "commit-msg", "#!/bin/sh\necho 'commit-msg: nope' >&2\nexit 1\n")

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"cherry-pick-continue", "--resolve", fx.conflicted+"=delete")
	if code == 0 {
		t.Fatalf("the commit-msg hook refuses, so the delegated conclusion must exit nonzero\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if head := testutil.Rev(t, fx.dir, "HEAD"); head != fx.tip {
		t.Fatalf("HEAD moved to %s despite the hook refusal (was %s); the fixture no longer produces the case", head, fx.tip)
	}

	// Nothing was committed, so nothing may have been destroyed.
	abs := filepath.Join(fx.dir, fx.conflicted)
	if !testutil.FileExists(abs) {
		t.Fatalf("%s was deleted from disk although the delegated conclusion committed nothing; the hand-edited content is unrecoverable\nstdout=%s\nstderr=%s",
			fx.conflicted, stdout, stderr)
	}
	if got := readWorktree(t, fx.dir, fx.conflicted); got != handEdited {
		t.Errorf("%s holds %q, want the hand-edited content %q that nothing had committed", fx.conflicted, got, handEdited)
	}
}
