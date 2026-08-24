package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// Inferred move records.
//
// safegit still detects no renames: it runs no similarity scoring and reads no
// diff -M. What it does read is the commit's own RAW DELTA -- the modes and
// blob names on both sides -- and where that delta witnesses a move on its own,
// the commit records it.
//
// Witnessing is a chain of fences, and these tests are one per fence. Each one
// builds a repository where the fence is the only thing standing between the
// delta and a record, and asserts the record is absent (and announced as
// absent). The first test is the other side of the same coin: everything clear,
// one record.
//
// The declared spelling (`--moved`) is unchanged and takes precedence: a path a
// declaration names is out of the candidate sets before any pairing happens, so
// a declared move is never also inferred.

// inferredSession keeps these runs distinguishable in the op log.
var inferredSession = []string{"CLAUDE_CODE_SESSION_ID=inferred-moves-test"}

// movePairsIn returns just the pair halves of a message's move records, so a
// test can state what it expects without predicting an id.
func movePairsIn(t *testing.T, message string) []string {
	t.Helper()
	var out []string
	for _, r := range movedRecordsIn(t, message) {
		out = append(out, r[1])
	}
	return out
}

// assertInferredPairs fails unless HEAD's records are exactly these pairs.
func assertInferredPairs(t *testing.T, dir string, want ...string) {
	t.Helper()
	msg := commitMessageOf(t, dir, "HEAD")
	got := movePairsIn(t, msg)
	if len(got) != len(want) {
		t.Fatalf("HEAD carries %d move record(s) %v, want %d %v; message:\n%s", len(got), got, len(want), want, msg)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record %d is %q, want %q; message:\n%s", i, got[i], want[i], msg)
		}
	}
}

// assertDeclareNotice fails unless the run pointed the operator at --moved.
func assertDeclareNotice(t *testing.T, stderr string) {
	t.Helper()
	if !strings.Contains(stderr, "--moved") {
		t.Errorf("a refused inference said nothing about declaring the move:\n%s", stderr)
	}
}

// moveOnDisk renames a path in the working tree, creating the destination's
// directory. It is the world the commit then describes.
func moveOnDisk(t *testing.T, dir, old, new string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, new)), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", new, err)
	}
	if err := os.Rename(filepath.Join(dir, old), filepath.Join(dir, new)); err != nil {
		t.Fatalf("rename %s -> %s: %v", old, new, err)
	}
}

// A move nothing else in either tree could be confused with is recorded, and
// the commit is otherwise exactly what it was: a deletion and an addition, no
// content adopted, no path staged that the caller did not name.
func TestInferredFileMoveIsRecorded(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	safegitCommitEnv(t, dir, inferredSession, "seed", "a.txt")

	moveOnDisk(t, dir, "a.txt", "sub/b.txt")
	_, stderr, code := runSafegitEnv(t, dir, inferredSession, "commit", "-m", "move a", "--", "a.txt", "sub/b.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	assertInferredPairs(t, dir, "a.txt -> sub/b.txt")

	diffTree := testutil.Git(t, dir, "diff-tree", "--no-commit-id", "--no-renames", "-r", "--name-status", "HEAD")
	if !strings.Contains(diffTree, "A\tsub/b.txt") || !strings.Contains(diffTree, "D\ta.txt") {
		t.Errorf("the commit's paths are:\n%s", diffTree)
	}
}

// An ordinary commit witnesses nothing and records nothing. This is the fast
// path: no blob is deleted and added, so neither tree is ever listed.
func TestOrdinaryCommitInfersNothing(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "one\n")
	safegitCommitEnv(t, dir, inferredSession, "seed", "a.txt")

	testutil.WriteFile(t, dir, "a.txt", "two\n")
	testutil.WriteFile(t, dir, "b.txt", "new file\n")
	_, stderr, code := runSafegitEnv(t, dir, inferredSession, "commit", "-m", "edit and add", "--", "a.txt", "b.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	assertInferredPairs(t, dir)
}

// FENCE: one-to-one exactness. The same blob leaves two paths and arrives at
// two others, so there is no evidence which went where -- and no tie-break is
// invented. Nothing is recorded, and the operator is told to declare it.
func TestInferenceRefusesAnAmbiguousBlob(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "x1.txt", "identical\n")
	testutil.WriteFile(t, dir, "x2.txt", "identical\n")
	safegitCommitEnv(t, dir, inferredSession, "seed", "x1.txt", "x2.txt")

	moveOnDisk(t, dir, "x1.txt", "y1.txt")
	moveOnDisk(t, dir, "x2.txt", "y2.txt")
	_, stderr, code := runSafegitEnv(t, dir, inferredSession, "commit", "-m", "move both",
		"--", "x1.txt", "x2.txt", "y1.txt", "y2.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	assertInferredPairs(t, dir)
	assertDeclareNotice(t, stderr)
}

// FENCE: parent-tree uniqueness. Only one path is deleted and only one added,
// so the pairing is one-to-one -- but the content also sits at a path the
// commit never touched, so its disappearance from one place says nothing about
// its appearance in another.
func TestInferenceRefusesWhenTheParentTreeHoldsTheBlobTwice(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "shared bytes\n")
	testutil.WriteFile(t, dir, "copy.txt", "shared bytes\n")
	safegitCommitEnv(t, dir, inferredSession, "seed", "a.txt", "copy.txt")

	moveOnDisk(t, dir, "a.txt", "b.txt")
	_, stderr, code := runSafegitEnv(t, dir, inferredSession, "commit", "-m", "move a", "--", "a.txt", "b.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	assertInferredPairs(t, dir)
	assertDeclareNotice(t, stderr)
}

// FENCE: new-tree uniqueness. The deleted content was unique in the parent, and
// exactly one path was added -- but the SAME commit also made another,
// untouched-by-the-pairing path hold those bytes, so the destination is not the
// content's one home.
func TestInferenceRefusesWhenTheNewTreeHoldsTheBlobTwice(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "travelling bytes\n")
	testutil.WriteFile(t, dir, "keep.txt", "something else\n")
	safegitCommitEnv(t, dir, inferredSession, "seed", "a.txt", "keep.txt")

	moveOnDisk(t, dir, "a.txt", "b.txt")
	testutil.WriteFile(t, dir, "keep.txt", "travelling bytes\n")
	_, stderr, code := runSafegitEnv(t, dir, inferredSession, "commit", "-m", "move a and echo it",
		"--", "a.txt", "b.txt", "keep.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	assertInferredPairs(t, dir)
	assertDeclareNotice(t, stderr)
}

// FENCE: an empty file never pairs. Every empty file in a repository shares one
// object name, so an empty file vanishing here and appearing there is not
// evidence of anything.
func TestInferenceRefusesTheEmptyBlob(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "empty.txt", "")
	safegitCommitEnv(t, dir, inferredSession, "seed", "empty.txt")

	moveOnDisk(t, dir, "empty.txt", "still-empty.txt")
	_, stderr, code := runSafegitEnv(t, dir, inferredSession, "commit", "-m", "move the empty file",
		"--", "empty.txt", "still-empty.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	assertInferredPairs(t, dir)
}

// FENCE: regular files only, and an exec-bit change across a pair is still a
// move. A symlink's blob is its target text, which several links routinely
// share, so a symlink never pairs; a file that gained +x on the way is one
// record all the same.
func TestInferenceTakesRegularFilesOnlyAndAllowsTheExecBit(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "script.sh", "#!/bin/sh\necho unique-to-this-file\n")
	if err := os.Symlink("script.sh", filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	safegitCommitEnv(t, dir, inferredSession, "seed", "script.sh", "link")

	// The script moves and becomes executable; the link moves too.
	moveOnDisk(t, dir, "script.sh", "bin/run.sh")
	if err := os.Chmod(filepath.Join(dir, "bin/run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	moveOnDisk(t, dir, "link", "bin/link")
	_, stderr, code := runSafegitEnv(t, dir, inferredSession, "commit", "-m", "move both",
		"--", "script.sh", "bin/run.sh", "link", "bin/link")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	// Exactly one record: the regular file, exec bit and all. The symlink pairs
	// with nothing.
	assertInferredPairs(t, dir, "script.sh -> bin/run.sh")
}

// SUPPRESSION FIRST. A pair the caller declared is out of both candidate sets
// before pairing, so the commit carries the declaration and NOT a second record
// saying the same thing with a different id.
func TestDeclaredMoveSuppressesTheInferredOne(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	safegitCommitEnv(t, dir, inferredSession, "seed", "a.txt")

	moveOnDisk(t, dir, "a.txt", "b.txt")
	_, stderr, code := runSafegitEnv(t, dir, inferredSession, "commit", "-m", "move a",
		"--moved", "a.txt -> b.txt", "--", "a.txt", "b.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	assertInferredPairs(t, dir, "a.txt -> b.txt")
}

// SUPPRESSION UN-BLOCKS. The same blob leaves two paths and arrives at two
// others, which on its own witnesses nothing -- but the caller DECLARED one of
// the two moves, and a declaration is the human answering that question. The
// declared paths are out of the candidate sets AND out of the listings the
// uniqueness fences read, so what is left is one deletion and one addition of a
// blob that now sits at exactly one path on each side: x2's fate is judged on
// its own, and it is recorded.
func TestDeclarationUnblocksTheOtherHalfOfAnAmbiguousBlob(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "x1.txt", "identical\n")
	testutil.WriteFile(t, dir, "x2.txt", "identical\n")
	safegitCommitEnv(t, dir, inferredSession, "seed", "x1.txt", "x2.txt")

	moveOnDisk(t, dir, "x1.txt", "y.txt")
	moveOnDisk(t, dir, "x2.txt", "z.txt")
	_, stderr, code := runSafegitEnv(t, dir, inferredSession, "commit", "-m", "move both",
		"--moved", "x1.txt -> y.txt", "--", "x1.txt", "x2.txt", "y.txt", "z.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	assertInferredPairs(t, dir, "x1.txt -> y.txt", "x2.txt -> z.txt")
}

// `safegit mv` mints its own records, and they arrive at the pipeline the same
// way a declaration does. So the commit it writes carries ONE record per move,
// never a declared one and an inferred duplicate beside it.
func TestSafegitMvCarriesOneRecordPerMove(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	safegitCommitEnv(t, dir, inferredSession, "seed", "a.txt")

	_, stderr, code := runSafegitEnv(t, dir, inferredSession, "mv", "-m", "move a", "a.txt -> b.txt")
	if code != 0 {
		t.Fatalf("mv failed (code %d): %s", code, stderr)
	}
	assertInferredPairs(t, dir, "a.txt -> b.txt")
}

// A SHARED-INDEX commit skips inference entirely. The content of a conclusion
// is whatever git's own operation staged, and reading a move out of that delta
// would attribute someone else's authorship to this commit.
func TestConclusionInfersNothing(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nbase\nline3\n")
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	safegitCommitEnv(t, dir, inferredSession, "base", "conflicted.txt", "a.txt")

	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "feature")
	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nfeature\nline3\n")
	moveOnDisk(t, dir, "a.txt", "b.txt")
	safegitCommitEnv(t, dir, inferredSession, "feature edit and move", "conflicted.txt", "a.txt", "b.txt")

	testutil.Git(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nmain\nline3\n")
	safegitCommitEnv(t, dir, inferredSession, "main edit", "conflicted.txt")

	if _, _, code := runSafegitEnv(t, dir, inferredSession, "merge", "feature"); code == 0 {
		t.Fatal("the fixture needs a conflicted merge")
	}
	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nresolved\nline3\n")
	if _, stderr, code := runSafegitEnv(t, dir, inferredSession, "merge-continue",
		"--resolve", "conflicted.txt=worktree"); code != 0 {
		t.Fatalf("merge-continue failed (code %d): %s", code, stderr)
	}

	// The merge commit's delta against its first parent does hold the move --
	// and the conclusion records none of it.
	assertInferredPairs(t, dir)
}

// An AMEND infers against the AUTHORING EVENT's own delta -- the amended tree
// against the replaced tip's first parent -- rather than against the tip it
// replaces. The commit that comes out is the one the record describes: the step
// from that parent to this commit, moves and all.
func TestAmendMintsTheMovesTheAuthoringEventWitnesses(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	safegitCommitEnv(t, dir, inferredSession, "seed", "a.txt")
	testutil.WriteFile(t, dir, "other.txt", "other\n")
	safegitCommitEnv(t, dir, inferredSession, "second", "other.txt")

	moveOnDisk(t, dir, "a.txt", "b.txt")
	_, stderr, code := runSafegitEnv(t, dir, inferredSession, "commit", "--amend",
		"--", "a.txt", "b.txt")
	if code != 0 {
		t.Fatalf("amend failed (code %d): %s", code, stderr)
	}
	assertInferredPairs(t, dir, "a.txt -> b.txt")
	assertOrigins(t, commitMessageOf(t, dir, "HEAD"), "observed")
}

// An amend's inference is ADDITIVE: the records already on the message are
// preserved verbatim, and what the amend's own delta witnesses joins them. The
// first record's id is the pin -- a preserved record is carried across, never
// re-minted.
func TestAmendAddsToTheRecordsTheMessageAlreadyCarries(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	testutil.WriteFile(t, dir, "c.txt", "other content nothing else holds\n")
	safegitCommitEnv(t, dir, inferredSession, "seed", "a.txt", "c.txt")

	moveOnDisk(t, dir, "a.txt", "b.txt")
	if _, stderr, code := runSafegitEnv(t, dir, inferredSession, "commit", "-m", "move a",
		"--moved", "a.txt -> b.txt", "--", "a.txt", "b.txt"); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	declaredID := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))[0][0]

	// The second move rides an amend of that same commit.
	moveOnDisk(t, dir, "c.txt", "d.txt")
	if _, stderr, code := runSafegitEnv(t, dir, inferredSession, "commit", "--amend",
		"--", "c.txt", "d.txt"); code != 0 {
		t.Fatalf("amend failed (code %d): %s", code, stderr)
	}

	msg := commitMessageOf(t, dir, "HEAD")
	assertInferredPairs(t, dir, "a.txt -> b.txt", "c.txt -> d.txt")
	assertOrigins(t, msg, "declared", "observed")
	if got := movedRecordsIn(t, msg)[0][0]; got != declaredID {
		t.Errorf("the preserved record's id is %q, want the original %q", got, declaredID)
	}
}

// A REWORD mints nothing, because rewording changes no tree: there is no
// authoring event to read. What it does do is carry every record across
// verbatim -- dropping one is a retraction the caller states, never a side
// effect of replacing the message.
func TestRewordMintsNothingAndKeepsEveryRecord(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	safegitCommitEnv(t, dir, inferredSession, "seed", "a.txt")

	// A move committed by raw git, so the commit carries no record at all while
	// its delta witnesses one. A reword of it still records nothing.
	moveOnDisk(t, dir, "a.txt", "b.txt")
	testutil.Git(t, dir, "add", "a.txt", "b.txt")
	testutil.Git(t, dir, "commit", "-m", "move a with raw git")
	if _, stderr, code := runSafegitEnv(t, dir, inferredSession, "commit", "--amend",
		"-m", "reworded subject"); code != 0 {
		t.Fatalf("reword failed (code %d): %s", code, stderr)
	}
	msg := commitMessageOf(t, dir, "HEAD")
	if !strings.HasPrefix(msg, "reworded subject") {
		t.Fatalf("the reword did not replace the message:\n%s", msg)
	}
	assertInferredPairs(t, dir)

	// And a reword of a commit that DOES carry a record keeps it, id and origin
	// intact.
	testutil.WriteFile(t, dir, "c.txt", "other content nothing else holds\n")
	safegitCommitEnv(t, dir, inferredSession, "seed c", "c.txt")
	moveOnDisk(t, dir, "c.txt", "d.txt")
	if _, stderr, code := runSafegitEnv(t, dir, inferredSession, "commit", "-m", "move c",
		"--", "c.txt", "d.txt"); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	before := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))
	if len(before) != 1 {
		t.Fatalf("the fixture recorded %v, want one record", before)
	}

	if _, stderr, code := runSafegitEnv(t, dir, inferredSession, "commit", "--amend",
		"-m", "move c, said better"); code != 0 {
		t.Fatalf("reword failed (code %d): %s", code, stderr)
	}
	after := commitMessageOf(t, dir, "HEAD")
	if got := movedRecordsIn(t, after); len(got) != 1 || got[0] != before[0] {
		t.Errorf("the reword changed the records: %v, want %v; message:\n%s", got, before, after)
	}
	assertOrigins(t, after, "observed")
}
