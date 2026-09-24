package test

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// SPEC PIN -- deliberately failing until the ruled behavior is implemented.
//
// The finding: `safegit mv` silently mints a missing destination directory. A
// pair whose destination names a directory that does not exist -- `a.txt ->
// newdir/a.txt` with no `newdir/` -- is not refused. mvFilesystem.move calls
// ensureParent (mv.go), which creates the directory through the effects handle
// and pushes a removal onto the rollback list, and the move then succeeds. The
// operator asked to move a file into a place that does not exist and got a new
// place instead, with nothing in the output distinguishing that from a move
// into a directory they already had.
//
// The ruling: a missing destination directory is a REFUSAL, and the refusal
// names the directory that is missing. Creating it becomes an election the
// operator makes explicitly -- a dedicated qualified flag will later opt in to
// it. That flag does not exist yet and is NOT pinned here: this test pins only
// the refusal, so it stays correct whatever the flag ends up being called.
//
// Existing mv tests that pin the mkdir behavior -- TestMvCreatesTheDestination
// Directory in internal/test/mv_test.go, and the destination-parent setup in
// the moves_* declaration tests -- are SANCTIONED REWRITES at implementation
// time. This pin deliberately contradicts them; when the refusal is
// implemented, those tests move to the flag that elects creation.
//
// Today this fails because the invocation exits 0 and newdir/ is on disk.
func TestMvRefusesAMissingDestinationDirectory(t *testing.T) {
	dir := mvSeed(t)
	before := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegit(t, dir, "mv", "-m", "move a", "a.txt -> newdir/a.txt")

	// The verdict: a destination directory that does not exist is refused.
	if code == 0 {
		t.Errorf("mv into a missing directory exited 0; a missing destination directory must be refused\nstdout: %s\nstderr: %s",
			stdout, stderr)
	}
	// The refusal is actionable only if it says WHICH directory is missing.
	if !strings.Contains(stderr, "newdir") {
		t.Errorf("the refusal does not name the missing directory: %s", stderr)
	}

	// Nothing was minted: the directory the operator did not ask for is absent.
	if mvExists(t, dir, "newdir") {
		t.Error("the missing destination directory was created; a refusal creates nothing")
	}
	// Nothing was moved: the source is where it was, the destination is nowhere.
	if !mvExists(t, dir, "a.txt") {
		t.Error("the source left its original path during a refused invocation")
	}
	if mvExists(t, dir, "newdir/a.txt") {
		t.Error("a refused invocation moved the file anyway")
	}

	// Nothing was committed: the branch is exactly where it was.
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("a commit was made despite the refusal: %s -> %s", before, after)
	}
	assertNoCommitHappened(t, dir, "seed")
}
