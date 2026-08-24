package test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// Witnessed subtree collapse, and the cap on scattered moves.
//
// A commit that moves a whole directory says ONE thing, and the record says it
// once: a per-file record for every file under the prefix would be the same
// statement repeated, and it would push an ordinary reorganization past the cap
// below for no reason.
//
// Collapse happens only where the delta FULLY WITNESSES the prefix mapping --
// every path the parent held under the old prefix moved, and nothing else
// arrived under the new one. Anything short of that stays per-file, because a
// subtree record would then claim more than the commit did.
//
// The CAP is the other half. Past a certain number of scattered inferred moves
// a wrong pairing is both likelier and harder to notice, so the commit records
// none of them and points at --moved. A uniform subtree move is one record and
// never approaches it.

// A subtree the delta FULLY witnesses is one record, not one per file: every
// path the parent held under the old prefix moved, and nothing else arrived
// under the new one.
func TestUniformSubtreeMoveIsOneRecord(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "dir/x.txt", "x content\n")
	testutil.WriteFile(t, dir, "dir/y.txt", "y content\n")
	testutil.WriteFile(t, dir, "dir/sub/z.txt", "z content\n")
	safegitCommitEnv(t, dir, inferredSession, "seed", "dir/x.txt", "dir/y.txt", "dir/sub/z.txt")

	moveOnDisk(t, dir, "dir/x.txt", "other/x.txt")
	moveOnDisk(t, dir, "dir/y.txt", "other/y.txt")
	moveOnDisk(t, dir, "dir/sub/z.txt", "other/sub/z.txt")
	_, stderr, code := runSafegitEnv(t, dir, inferredSession, "commit", "-m", "move dir",
		"--", "dir/x.txt", "dir/y.txt", "dir/sub/z.txt", "other/x.txt", "other/y.txt", "other/sub/z.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	assertInferredPairs(t, dir, "dir/ -> other/")
}

// A PARTIAL move is not a subtree move. A file left behind means the directory
// did not move, so the records stay per-file and say exactly what happened.
func TestPartialSubtreeMoveFallsThroughToPerFileRecords(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "dir/x.txt", "x content\n")
	testutil.WriteFile(t, dir, "dir/y.txt", "y content\n")
	testutil.WriteFile(t, dir, "dir/keep.txt", "keep content\n")
	safegitCommitEnv(t, dir, inferredSession, "seed", "dir/x.txt", "dir/y.txt", "dir/keep.txt")

	moveOnDisk(t, dir, "dir/x.txt", "other/x.txt")
	moveOnDisk(t, dir, "dir/y.txt", "other/y.txt")
	_, stderr, code := runSafegitEnv(t, dir, inferredSession, "commit", "-m", "move part of dir",
		"--", "dir/x.txt", "dir/y.txt", "other/x.txt", "other/y.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	assertInferredPairs(t, dir, "dir/x.txt -> other/x.txt", "dir/y.txt -> other/y.txt")
}

// The CAP. A commit whose delta witnesses more scattered moves than safegit
// records on its own records NONE of them and says so: at that scale a wrong
// pairing is both likelier and harder to notice, and the caller who really did
// perform them can declare them.
func TestScatteredMovesAboveTheCapAreRefused(t *testing.T) {
	const count = 21 // one past the cap

	dir := newRepo(t)
	var seed, paths []string
	for i := 0; i < count; i++ {
		old := fmt.Sprintf("old%02d.txt", i)
		testutil.WriteFile(t, dir, old, fmt.Sprintf("content number %d\n", i))
		seed = append(seed, old)
	}
	safegitCommitEnv(t, dir, inferredSession, "seed", seed...)

	for i := 0; i < count; i++ {
		old := fmt.Sprintf("old%02d.txt", i)
		// The destinations share no directory with the sources and no name with
		// them, so nothing collapses into a subtree record.
		new := fmt.Sprintf("new%02d.dat", i)
		moveOnDisk(t, dir, old, new)
		paths = append(paths, old, new)
	}

	args := append([]string{"commit", "-m", "scatter"}, "--")
	args = append(args, paths...)
	_, stderr, code := runSafegitEnv(t, dir, inferredSession, args...)
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	assertInferredPairs(t, dir)
	assertDeclareNotice(t, stderr)
	if !strings.Contains(stderr, "none were recorded") {
		t.Errorf("the cap refusal did not say the moves went unrecorded:\n%s", stderr)
	}
}

// One under the cap is recorded, so the cap is a boundary rather than a
// blanket refusal of large commits.
func TestScatteredMovesAtTheCapAreRecorded(t *testing.T) {
	const count = 20 // exactly the cap

	dir := newRepo(t)
	var seed, paths []string
	for i := 0; i < count; i++ {
		old := fmt.Sprintf("old%02d.txt", i)
		testutil.WriteFile(t, dir, old, fmt.Sprintf("content number %d\n", i))
		seed = append(seed, old)
	}
	safegitCommitEnv(t, dir, inferredSession, "seed", seed...)

	for i := 0; i < count; i++ {
		old := fmt.Sprintf("old%02d.txt", i)
		new := fmt.Sprintf("new%02d.dat", i)
		moveOnDisk(t, dir, old, new)
		paths = append(paths, old, new)
	}

	args := append([]string{"commit", "-m", "scatter"}, "--")
	args = append(args, paths...)
	if _, stderr, code := runSafegitEnv(t, dir, inferredSession, args...); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	if got := len(movePairsIn(t, commitMessageOf(t, dir, "HEAD"))); got != count {
		t.Errorf("HEAD carries %d records, want %d", got, count)
	}
}
