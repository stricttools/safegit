package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// `safegit mv` commits the RENAME AND NOTHING ELSE: each path is carried across
// as the exact blob its parent held, through index edits rather than by staging
// from disk. That is the right commit, and it is the wrong answer for a file
// the operator has edited and not committed -- the move succeeds, the edit is
// left behind as an uncommitted change at a path the operator did not name, and
// nothing in the output says so.
//
// So a move of a file carrying uncommitted content changes is REFUSED. There is
// no flag: both legitimate intents already have a route, and the refusal names
// them.
//
//   - The edits belong in their own commit: commit the content first, then mv.
//   - The edits should ride along with the move: move it on disk yourself, then
//     `safegit commit --moved 'old -> new' -- <old> <new>`, which stages from
//     disk and commits the content and the move together. Both paths, because a
//     declaration is checked against the tree the commit writes: naming only the
//     destination leaves the old path in that tree and the declaration is
//     refused.

// mvDirtySeed builds mvSeed's repository and then edits a.txt on disk without
// committing it, which is the whole condition under test.
func mvDirtySeed(t *testing.T) string {
	t.Helper()
	dir := mvSeed(t)
	testutil.WriteFile(t, dir, "a.txt", "a, edited and not committed\n")
	return dir
}

func TestMvRefusesAMoveOfADirtyFile(t *testing.T) {
	dir := mvDirtySeed(t)
	before := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegit(t, dir, "mv", "-m", "move a", "a.txt -> moved.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Errorf("moving a dirty file exited %d, want %d (MoveNotBorneOut)\nstdout: %s\nstderr: %s",
			code, exitcode.MoveNotBorneOut, stdout, stderr)
	}
	if !strings.Contains(stderr, "a.txt") {
		t.Errorf("the refusal does not name the dirty path; stderr:\n%s", stderr)
	}

	// Both routes, named. The refusal is only actionable if it says what to do
	// for each of the two things the operator might have meant.
	if !strings.Contains(stderr, "commit") {
		t.Errorf("the refusal does not name the commit-the-content-first route; stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "--moved") {
		t.Errorf("the refusal does not name the move-it-yourself route (safegit commit --moved); stderr:\n%s", stderr)
	}

	// Nothing moved, nothing committed, and the edit is still where it was.
	if !mvExists(t, dir, "a.txt") || mvExists(t, dir, "moved.txt") {
		t.Error("a refused move touched the working tree")
	}
	if got, err := os.ReadFile(filepath.Join(dir, "a.txt")); err != nil || string(got) != "a, edited and not committed\n" {
		t.Errorf("the uncommitted edit was disturbed: %q (%v)", got, err)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("a commit was made despite the refusal: %s -> %s", before, after)
	}
}

// TestMvDryRunRefusesADirtyMove: a preview of a move safegit would refuse must
// BE the refusal, not a rehearsal of a move that could never happen.
func TestMvDryRunRefusesADirtyMove(t *testing.T) {
	dir := mvDirtySeed(t)

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "mv", "-m", "move a", "a.txt -> moved.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Errorf("a dry-run dirty move exited %d, want %d (MoveNotBorneOut)\nstdout: %s\nstderr: %s",
			code, exitcode.MoveNotBorneOut, stdout, stderr)
	}
	if !strings.Contains(stderr, "a.txt") {
		t.Errorf("the preview's refusal does not name the dirty path; stderr:\n%s", stderr)
	}
}

// TestMvSubtreeDirtyRefusalNamesEveryDirtyPath: a subtree move is one pair over
// many files, so the refusal aggregates under that pair -- and it names EVERY
// dirty path underneath it. A refusal that named one would be a discovery loop:
// fix that file, run again, learn about the next.
func TestMvSubtreeDirtyRefusalNamesEveryDirtyPath(t *testing.T) {
	dir := mvSeed(t)
	testutil.WriteFile(t, dir, "src/one.txt", "1, edited\n")
	testutil.WriteFile(t, dir, "src/deep/two.txt", "2, edited\n")

	stdout, stderr, code := runSafegit(t, dir, "mv", "-m", "move the directory", "src/ -> lib/")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("a dirty subtree move exited %d, want %d (MoveNotBorneOut)\nstdout: %s\nstderr: %s",
			code, exitcode.MoveNotBorneOut, stdout, stderr)
	}
	for _, want := range []string{"src/one.txt", "src/deep/two.txt"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not name the dirty path %q; stderr:\n%s", want, stderr)
		}
	}
	if mvExists(t, dir, "lib") {
		t.Error("a refused subtree move created the destination")
	}

	// Machine mode carries the same verdict: the envelope's exit_code is the
	// refusal's, and the complete list of dirty paths is on stderr, which
	// machine mode never suppresses.
	stdout, stderr, code = runSafegit(t, dir, "--json", "mv", "-m", "move the directory", "src/ -> lib/")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("under --json the dirty subtree move exited %d, want %d\nstdout: %s\nstderr: %s",
			code, exitcode.MoveNotBorneOut, stdout, stderr)
	}
	if !strings.Contains(stdout, `"exit_code":19`) {
		t.Errorf("the envelope does not carry the refusal's exit code: %s", stdout)
	}
	for _, want := range []string{"src/one.txt", "src/deep/two.txt"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("under --json the refusal does not name %q; stderr:\n%s", want, stderr)
		}
	}
}

// TestMvDoesNotRefuseAFilterConvertedCheckout is the control that keeps the
// dirtiness question honest. On a checkout where git's own filters convert line
// endings, the bytes on disk differ from the blob and the file is still CLEAN --
// git says so, and so must safegit. The comparison is therefore filter-aware:
// the disk bytes are hashed AS THE NEW PATH (`git hash-object --path`), which is
// how attributes are resolved for content that is about to live there.
func TestMvDoesNotRefuseAFilterConvertedCheckout(t *testing.T) {
	dir := mvSeed(t)

	// The blob committed by mvSeed holds LF. Turn on the conversion and write
	// the working-tree spelling a CRLF checkout would have produced.
	testutil.Git(t, dir, "config", "core.autocrlf", "true")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\r\n"), 0644); err != nil {
		t.Fatalf("writing the CRLF working-tree copy: %v", err)
	}

	// git's own verdict first: if this fixture is dirty to git, it proves
	// nothing about safegit refusing it.
	if porcelain := testutil.Git(t, dir, "status", "--porcelain"); porcelain != "" {
		t.Fatalf("the fixture is dirty to git itself, so it cannot be the control:\n%s", porcelain)
	}

	stdout, stderr, code := runSafegit(t, dir, "mv", "-m", "move a", "a.txt -> moved.txt")
	if code != 0 {
		t.Fatalf("a filter-converted checkout was refused as dirty (code %d)\nstdout: %s\nstderr: %s",
			code, stdout, stderr)
	}
	if !mvExists(t, dir, "moved.txt") {
		t.Error("the move did not happen")
	}
}

// TestMvMovesAFileRewrittenWithItsOwnContent: the question is CONTENT, not
// whether anything touched the file. A file rewritten byte-for-byte with what
// the parent commit holds is not a change, whatever its timestamp says, and
// refusing it would make the check a stat cache rather than a comparison.
func TestMvMovesAFileRewrittenWithItsOwnContent(t *testing.T) {
	dir := mvSeed(t)
	testutil.WriteFile(t, dir, "a.txt", "a\n")

	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "move a", "a.txt -> moved.txt"); code != 0 {
		t.Fatalf("a move of an unchanged-but-rewritten file was refused (code %d): %s", code, stderr)
	}
	if !mvExists(t, dir, "moved.txt") {
		t.Error("the move did not happen")
	}
}

// TestMvRefusesOnlyTheDirtyPairs: the dirty check joins mv's COLLECTED refusal,
// so a set of pairs is judged as one statement -- every bad pair named at once,
// and the clean ones not performed either, because a refused set leaves the
// working tree exactly as it was.
func TestMvRefusesOnlyTheDirtyPairs(t *testing.T) {
	dir := mvDirtySeed(t)

	_, stderr, code := runSafegit(t, dir, "mv", "-m", "two moves",
		"a.txt -> x.txt", "b.txt -> y.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("the set exited %d, want %d (MoveNotBorneOut); stderr: %s",
			code, exitcode.MoveNotBorneOut, stderr)
	}
	if !strings.Contains(stderr, "a.txt") {
		t.Errorf("the refusal does not name the dirty pair; stderr:\n%s", stderr)
	}
	if mvExists(t, dir, "y.txt") || !mvExists(t, dir, "b.txt") {
		t.Error("the clean pair was performed although the set was refused")
	}
}
