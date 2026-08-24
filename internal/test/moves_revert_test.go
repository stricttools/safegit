package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
	"github.com/smm-h/safegit/internal/trailer"
)

// Reverting a commit that declared a move undoes the move, so the revert
// commit declares the move back: the same pair with its sides swapped, under a
// FRESH id, because it is a different claim about a different step and not a
// second copy of the first one.
//
// A revert of more than one commit used to be git's own sequencer, and the
// asymmetry -- git's commits carrying no records at all -- was pinned here.
// `safegit revert` now reverts one commit and authors the result itself, so
// that command line is refused instead, and the refusal is what is pinned.

// projectionChain reads the repository's first-parent history, oldest first,
// into the shape internal/trailer's projection takes: what each commit
// declared, and the trees that decide whether the declarations hold.
func projectionChain(t *testing.T, dir string) []trailer.Commit {
	t.Helper()
	revs := strings.Fields(testutil.Git(t, dir, "rev-list", "--reverse", "HEAD"))
	chain := make([]trailer.Commit, 0, len(revs))
	for _, sha := range revs {
		parents := strings.Fields(testutil.Git(t, dir, "rev-list", "--parents", "-n", "1", sha))[1:]
		c := trailer.Commit{
			ID:    sha,
			Moves: trailer.ReadMoves(commitMessageOf(t, dir, sha)),
			Tree:  treePathSet(t, dir, sha),
		}
		for _, p := range parents {
			c.Parents = append(c.Parents, treePathSet(t, dir, p))
		}
		chain = append(chain, c)
	}
	return chain
}

// treePathSet is one commit's tree as the set of paths it holds.
func treePathSet(t *testing.T, dir, rev string) trailer.PathSet {
	t.Helper()
	return trailer.NewPathSet(strings.Fields(testutil.Git(t, dir, "ls-tree", "-r", "--name-only", rev))...)
}

func TestRevertOfAMoveDeclaresTheMoveBack(t *testing.T) {
	dir := mvSeed(t)

	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "move a to moved", "a.txt -> moved.txt"); code != 0 {
		t.Fatalf("mv failed (code %d): %s", code, stderr)
	}
	moveSHA := testutil.Rev(t, dir, "HEAD")
	forward := movedRecordsIn(t, commitMessageOf(t, dir, moveSHA))
	if len(forward) != 1 {
		t.Fatalf("the fixture recorded no move: %v", forward)
	}

	if _, stderr, code := runSafegit(t, dir, "revert", moveSHA); code != 0 {
		t.Fatalf("revert failed (code %d): %s", code, stderr)
	}

	msg := commitMessageOf(t, dir, "HEAD")
	inverse := movedRecordsIn(t, msg)
	if len(inverse) != 1 {
		t.Fatalf("the revert carries %d records, want one:\n%s", len(inverse), msg)
	}
	if inverse[0][1] != "moved.txt -> a.txt" {
		t.Errorf("the inverse pair is %q, want %q", inverse[0][1], "moved.txt -> a.txt")
	}
	if inverse[0][0] == forward[0][0] {
		t.Errorf("the inverse record reuses the original's id %s; it is a different claim", forward[0][0])
	}
	// OBSERVED, whatever the reverted record's own origin was: nobody stated
	// this move, safegit derived it by turning the other commit's record around.
	assertOrigins(t, msg, "observed")

	// The projection follows the path forward across both commits and lands
	// where the trees say the content actually is.
	p := trailer.Forward("a.txt", projectionChain(t, dir))
	if p.Path != "a.txt" {
		t.Errorf("a.txt projects to %q across the move and its revert, want a.txt", p.Path)
	}
	if len(p.Hops) != 2 {
		t.Errorf("the projection applied %d records, want two (the move and its inverse)", len(p.Hops))
	}
	if !p.Present {
		t.Error("the projected path is not present in the final tree")
	}
	// And the intermediate name resolves the same way from where it existed.
	if mid := trailer.Forward("moved.txt", projectionChain(t, dir)[1:]); mid.Path != "a.txt" {
		t.Errorf("moved.txt projects to %q across the revert, want a.txt", mid.Path)
	}
}

func TestRevertOfASubtreeMoveDeclaresTheSubtreeBack(t *testing.T) {
	dir := mvSeed(t)

	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "move the directory", "src/ -> lib/"); code != 0 {
		t.Fatalf("subtree mv failed (code %d): %s", code, stderr)
	}
	moveSHA := testutil.Rev(t, dir, "HEAD")

	if _, stderr, code := runSafegit(t, dir, "revert", moveSHA); code != 0 {
		t.Fatalf("revert failed (code %d): %s", code, stderr)
	}

	inverse := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))
	if len(inverse) != 1 || inverse[0][1] != "lib/ -> src/" {
		t.Fatalf("the revert's records are %v, want one subtree inverse", inverse)
	}
	assertOrigins(t, commitMessageOf(t, dir, "HEAD"), "observed")

	p := trailer.Forward("src/deep/two.txt", projectionChain(t, dir))
	if p.Path != "src/deep/two.txt" || !p.Present {
		t.Errorf("the path projects to %q (present=%v) across the subtree move and its revert", p.Path, p.Present)
	}
}

// TestRevertOfAMoveWhoseRecordWasRetractedStillDeclaresTheMoveBack pins that
// the inverse record does not consult the source record's standing.
//
// The inverse describes THE REVERT COMMIT'S OWN tree delta: this commit put the
// content back where it came from, which happened whatever a later commit said
// about the record that first declared it. A retraction says the earlier CLAIM
// was wrong, not that the content never moved -- and the projection arbitrates
// every claim against the trees anyway, so a wrong inverse would be caught
// there rather than by refusing to write one.
//
// Making the revert read the retraction would also make an inverse depend on
// how much history the reader happens to walk, which is the opposite of a
// record's whole point: a claim written once, on the commit it describes.
func TestRevertOfAMoveWhoseRecordWasRetractedStillDeclaresTheMoveBack(t *testing.T) {
	dir := mvSeed(t)

	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "move a to moved", "a.txt -> moved.txt"); code != 0 {
		t.Fatalf("mv failed (code %d): %s", code, stderr)
	}
	moveSHA := testutil.Rev(t, dir, "HEAD")
	forward := movedRecordsIn(t, commitMessageOf(t, dir, moveSHA))
	if len(forward) != 1 {
		t.Fatalf("the fixture recorded no move: %v", forward)
	}

	// A later commit retracts the record the move commit carries.
	testutil.WriteFile(t, dir, "b.txt", "changed\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "that record was wrong",
		"--trailer", "Moved-Retract: "+forward[0][0], "--", "b.txt"); code != 0 {
		t.Fatalf("retraction commit failed (code %d): %s", code, stderr)
	}

	if _, stderr, code := runSafegit(t, dir, "revert", moveSHA); code != 0 {
		t.Fatalf("revert failed (code %d): %s", code, stderr)
	}

	msg := commitMessageOf(t, dir, "HEAD")
	inverse := movedRecordsIn(t, msg)
	if len(inverse) != 1 {
		t.Fatalf("the revert of a retracted move carries %d records, want one:\n%s", len(inverse), msg)
	}
	if inverse[0][1] != "moved.txt -> a.txt" {
		t.Errorf("the inverse pair is %q, want %q", inverse[0][1], "moved.txt -> a.txt")
	}
	if inverse[0][0] == forward[0][0] {
		t.Errorf("the inverse record reuses the retracted record's id %s; it is a different claim", forward[0][0])
	}
	// The revert declares a move; it does not carry the retraction forward.
	if strings.Contains(msg, "Moved-Retract:") {
		t.Errorf("the revert carried the retraction forward:\n%s", msg)
	}
}

func TestRevertOfARetractionDeclaresNothing(t *testing.T) {
	dir := mvSeed(t)

	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "move a", "a.txt -> moved.txt"); code != 0 {
		t.Fatalf("mv failed (code %d): %s", code, stderr)
	}
	id := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))[0][0]

	testutil.WriteFile(t, dir, "b.txt", "changed\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "that record was wrong",
		"--trailer", "Moved-Retract: "+id, "--", "b.txt"); code != 0 {
		t.Fatalf("retraction commit failed (code %d): %s", code, stderr)
	}
	retractSHA := testutil.Rev(t, dir, "HEAD")

	if _, stderr, code := runSafegit(t, dir, "revert", retractSHA); code != 0 {
		t.Fatalf("revert failed (code %d): %s", code, stderr)
	}

	msg := commitMessageOf(t, dir, "HEAD")
	if records := movedRecordsIn(t, msg); len(records) != 0 {
		t.Errorf("reverting a retraction declared a move:\n%s", msg)
	}
	// Reverting a retraction does not put the retracted record back either:
	// only Moved: records get inverses, and a retraction is not one.
	if strings.Contains(msg, "Moved-Retract:") {
		t.Errorf("the revert carried the retraction forward:\n%s", msg)
	}
}

// A single revert has TWO doors into the same commit: the clean one, where
// `safegit revert` computes the inverse patch and commits in one invocation,
// and the conflicted one, where git parks the state and `safegit revert-continue`
// concludes it. Both go through the same inverse-minting, and this is the
// conflicted door's own end-to-end assertion -- every test above enters through
// the clean one, so nothing else here would notice if the conclusion engine
// stopped minting inverses.
//
// The conflict is arranged on a path the move does not touch: the commit being
// reverted moves a.txt and also edits c.txt, and a later commit edits c.txt
// again, so the inverse patch cannot apply. The move half stages cleanly, which
// is what leaves the records as the only thing under test.
func TestConflictedRevertConcludedThroughRevertContinueDeclaresTheMoveBack(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "a.txt", "content that does not change\n")
	testutil.WriteFile(t, dir, "c.txt", "l1\nbase\nl3\n")
	if _, stderr, code := runSafegitEnv(t, dir, conclusionSession,
		"commit", "-m", "base", "--", "a.txt", "c.txt"); code != 0 {
		t.Fatalf("base commit failed (code %d): %s", code, stderr)
	}

	// The commit to revert: a declared move plus an unrelated edit.
	if err := os.Rename(filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")); err != nil {
		t.Fatalf("rename a.txt -> b.txt: %v", err)
	}
	testutil.WriteFile(t, dir, "c.txt", "l1\nthe change\nl3\n")
	if _, stderr, code := runSafegitEnv(t, dir, conclusionSession, "commit", "-m", "move a and edit c",
		"--moved", "a.txt -> b.txt", "--", "a.txt", "b.txt", "c.txt"); code != 0 {
		t.Fatalf("move commit failed (code %d): %s", code, stderr)
	}
	source := testutil.Rev(t, dir, "HEAD")
	forward := movedRecordsIn(t, commitMessageOf(t, dir, source))
	if len(forward) != 1 {
		t.Fatalf("the fixture recorded no move: %v", forward)
	}

	// The later edit is what makes the inverse patch conflict.
	testutil.WriteFile(t, dir, "c.txt", "l1\nlater\nl3\n")
	if _, stderr, code := runSafegitEnv(t, dir, conclusionSession,
		"commit", "-m", "a later edit", "--", "c.txt"); code != 0 {
		t.Fatalf("later commit failed (code %d): %s", code, stderr)
	}

	stdout, stderr, code := runSafegitEnv(t, dir, conclusionSession, "revert", "--no-edit", source)
	if code == 0 {
		t.Fatalf("the revert succeeded; this fixture needs a conflict\nstdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "CONFLICT") {
		t.Fatalf("the revert did not report a conflict (code %d)\nstdout=%s stderr=%s", code, stdout, stderr)
	}

	if _, stderr, code := runSafegitEnv(t, dir, conclusionSession,
		"revert-continue", "--resolve", "c.txt=theirs"); code != 0 {
		t.Fatalf("revert-continue failed (code %d): %s", code, stderr)
	}

	msg := commitMessageOf(t, dir, "HEAD")
	inverse := movedRecordsIn(t, msg)
	if len(inverse) != 1 {
		t.Fatalf("the concluded revert carries %d records, want one:\n%s", len(inverse), msg)
	}
	if inverse[0][1] != "b.txt -> a.txt" {
		t.Errorf("the inverse pair is %q, want %q", inverse[0][1], "b.txt -> a.txt")
	}
	if inverse[0][0] == forward[0][0] {
		t.Errorf("the inverse record reuses the original's id %s; it is a different claim", forward[0][0])
	}
	// OBSERVED, whatever the reverted record's own origin was: nobody stated
	// this move, safegit derived it by turning the other commit's record around.
	assertOrigins(t, msg, "observed")

	// The commit is a real revert of the move as well as a record of it.
	paths := testutil.TreePaths(t, dir, "HEAD")
	if !testutil.Contains(paths, "a.txt") || testutil.Contains(paths, "b.txt") {
		t.Errorf("the concluded revert did not put the content back at a.txt (tree: %v)", paths)
	}
	assertNoSequencerResidue(t, dir, "conflicted revert of a move")
}

// TestMultiCommitRevertOfMovesIsRefused: a revert of several commits used to be
// git's own sequencer, and the property pinned here was that the commits git
// authored carried no move records -- safegit adds nothing to a commit it did
// not make.
//
// `safegit revert` now reverts ONE commit and authors the result itself, so
// there is no second authorship class left to distinguish: the multi-commit
// command line is refused, and the only revert commits that exist are safegit's
// own, which do carry the inverse records.
func TestMultiCommitRevertOfMovesIsRefused(t *testing.T) {
	dir := mvSeed(t)

	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "first move", "a.txt -> one.txt"); code != 0 {
		t.Fatalf("first mv failed (code %d): %s", code, stderr)
	}
	first := testutil.Rev(t, dir, "HEAD")
	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "second move", "one.txt -> two.txt"); code != 0 {
		t.Fatalf("second mv failed (code %d): %s", code, stderr)
	}
	second := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegit(t, dir, "revert", "--no-edit", second, first)
	if code != exitcode.Usage {
		t.Fatalf("a two-commit revert exited %d, want %d (Usage): %s", code, exitcode.Usage, stderr)
	}
	if !strings.Contains(stderr, "one commit") {
		t.Errorf("the refusal does not say a revert takes one commit:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != second {
		t.Errorf("the refused revert moved HEAD to %s (was %s)", head, second)
	}

	// The sequential form is the way through, and each of its commits carries
	// the inverse record -- which is what the refusal buys.
	if _, stderr, code := runSafegit(t, dir, "revert", "--no-edit", second); code != 0 {
		t.Fatalf("reverting the second move on its own failed (code %d): %s", code, stderr)
	}
	if records := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD")); len(records) != 1 {
		t.Errorf("the sequential revert declares %d record(s), want the one inverse move", len(records))
	}
}
