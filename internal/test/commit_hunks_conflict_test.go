package test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// Two arguments that name the same file and say different things about it are a
// contradiction: "commit all of it" and "commit hunk 1 of it" cannot both be
// obeyed, and neither can two --hunks elements for one path, since each element
// states that path's whole selection.
//
// The refusal is decided on CANONICAL repo-relative paths, in intake, because
// one file has many spellings. A comparison of the argument strings answers
// correctly for `a.go` against `a.go` and wrongly for every other pair that
// means the same file -- `./a.go`, `sub/../a.go`, a path relative to a
// subdirectory -- and the wrong answer was not a lenient one: the second entry
// was silently dropped by intake's dedup, so the caller who asked for one hunk
// got the whole file committed with no message of any kind.

// hunksConflictRepo seeds a repository with a tracked, two-hunk-modified file
// at the root and a second such file inside sub/, so a conflict can be spelled
// from the root and from a subdirectory.
func hunksConflictRepo(t *testing.T) (dir, sub string) {
	t.Helper()
	dir = newRepo(t)
	sub = filepath.Join(dir, "sub")

	testutil.WriteFileAt(t, filepath.Join(dir, "a.go"), intakeEdgeNumbered(20, nil))
	testutil.WriteFileAt(t, filepath.Join(sub, "b.go"), intakeEdgeNumbered(20, nil))
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", "a.go", "sub/b.go"); code != 0 {
		t.Fatalf("seed commit failed (%d): %s", code, stderr)
	}
	intakeEdgeTwoHunks(t, filepath.Join(dir, "a.go"))
	intakeEdgeTwoHunks(t, filepath.Join(sub, "b.go"))
	return dir, sub
}

// assertConflictRefused runs one conflicting command line and holds the whole
// refusal: exit Usage, a message naming both spellings, and a tip that did not
// move -- the last of which is what a silently-dropped entry would break, since
// the commit it produced looked like a success.
func assertConflictRefused(t *testing.T, repo, cwd string, args ...string) string {
	t.Helper()
	before := testutil.Rev(t, repo, "HEAD")

	_, stderr, code := runSafegit(t, cwd, args...)
	if code != exitcode.Usage {
		t.Errorf("safegit %s exited %d, want %d (Usage); stderr: %s",
			strings.Join(args, " "), code, exitcode.Usage, stderr)
	}
	if after := testutil.Rev(t, repo, "HEAD"); after != before {
		t.Errorf("HEAD moved despite the refusal of %s: %s -> %s", strings.Join(args, " "), before, after)
	}
	return stderr
}

// TestCommitSamePathWholeAndInHunksIsRefused: the plain spelling, which the
// predecessor's string comparison already refused. It stays refused.
func TestCommitSamePathWholeAndInHunksIsRefused(t *testing.T) {
	dir, _ := hunksConflictRepo(t)

	stderr := assertConflictRefused(t, dir, dir,
		"commit", "-m", "both ways", "--hunks", "a.go:1", "--", "a.go")
	if !strings.Contains(stderr, "a.go") || !strings.Contains(stderr, "--hunks") {
		t.Errorf("the refusal must name the path and the flag it clashes with; stderr: %s", stderr)
	}
}

// TestCommitSamePathDifferentSpellingIsRefused is the bug this file was added
// for. `./a.go` and `a.go` are one file, and the argument strings differ, so
// the refusal has to come from canonicalization. Before it did, this command
// line committed the WHOLE of a.go: the positional was staged, the hunk entry
// was dropped as a duplicate, and nothing said so.
func TestCommitSamePathDifferentSpellingIsRefused(t *testing.T) {
	dir, _ := hunksConflictRepo(t)

	stderr := assertConflictRefused(t, dir, dir,
		"commit", "-m", "both ways, spelled differently", "--hunks", "a.go:1", "--", "./a.go")
	if !strings.Contains(stderr, "a.go") {
		t.Errorf("the refusal must name the path; stderr: %s", stderr)
	}

	// The other direction of the same collision: a dot-slash inside the
	// --hunks element instead of on the positional.
	assertConflictRefused(t, dir, dir,
		"commit", "-m", "the other way round", "--hunks", "./a.go:1", "--", "a.go")

	// And a spelling that walks out of a subdirectory and back in.
	assertConflictRefused(t, dir, dir,
		"commit", "-m", "through a subdirectory", "--hunks", "sub/../a.go:1", "--", "a.go")
}

// TestCommitSamePathDifferentSpellingFromSubdirIsRefused: the same collision
// spelled from a subdirectory, where both arguments are relative to the
// caller's own directory rather than to the repository root.
func TestCommitSamePathDifferentSpellingFromSubdirIsRefused(t *testing.T) {
	dir, sub := hunksConflictRepo(t)

	stderr := assertConflictRefused(t, dir, sub,
		"commit", "-m", "from sub", "--hunks", "b.go:1", "--", "./b.go")
	if !strings.Contains(stderr, "b.go") {
		t.Errorf("the refusal must name the path; stderr: %s", stderr)
	}
}

// TestCommitOnePathTwiceInHunksDifferentSpellingIsRefused: each --hunks element
// states its path's whole selection, so two elements for one path contradict
// each other however each is spelled.
func TestCommitOnePathTwiceInHunksDifferentSpellingIsRefused(t *testing.T) {
	dir, _ := hunksConflictRepo(t)

	stderr := assertConflictRefused(t, dir, dir,
		"commit", "-m", "twice", "--hunks", "a.go:1", "--hunks", "./a.go:2")
	if !strings.Contains(stderr, "--hunks") {
		t.Errorf("the refusal must name the flag; stderr: %s", stderr)
	}
}

// TestAmendSamePathDifferentSpellingIsRefused: amend shares intake with commit,
// so it shares the refusal. Pinned separately because it reaches resolveFiles
// through its own entry point.
func TestAmendSamePathDifferentSpellingIsRefused(t *testing.T) {
	dir, _ := hunksConflictRepo(t)

	assertConflictRefused(t, dir, dir,
		"commit", "--amend", "-m", "both ways", "--hunks", "a.go:1", "--", "./a.go")
}

// TestCommitDistinctPathsSpelledDifferentlyAreNotAConflict is the control: the
// check is about one file named twice, not about a command line carrying both a
// positional and a --hunks element. Two different files are two different
// statements, and the hunk selection is honoured.
func TestCommitDistinctPathsSpelledDifferentlyAreNotAConflict(t *testing.T) {
	dir, _ := hunksConflictRepo(t)

	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "one whole, one hunk",
		"--hunks", "sub/b.go:1", "--", "./a.go"); code != 0 {
		t.Fatalf("a positional and a --hunks element naming different files were refused (%d): %s", code, stderr)
	}

	whole := testutil.MustShow(t, dir, "HEAD", "a.go")
	if !strings.Contains(whole, "FIRST-CHANGE") || !strings.Contains(whole, "SECOND-CHANGE") {
		t.Errorf("a.go was not committed whole; content:\n%s", whole)
	}
	partial := testutil.MustShow(t, dir, "HEAD", "sub/b.go")
	if !strings.Contains(partial, "FIRST-CHANGE") || strings.Contains(partial, "SECOND-CHANGE") {
		t.Errorf("sub/b.go did not get exactly hunk 1; content:\n%s", partial)
	}
}
