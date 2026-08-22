package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// Explicit beats expanded.
//
// A --hunks element is an explicit statement about ONE file's content. A
// directory argument is a statement about a directory: whatever happens to sit
// underneath it is incidental, and every name the expansion produces is a name
// the caller never typed. So when an expansion sweeps up a path that carries a
// hunk selection, the selection wins and the expansion passes the path over.
//
// Before this, intake's dedup decided it by order of arrival: the expansion ran
// first, put the member in the seen set, and the later explicit entry was
// dropped without a word. `safegit commit -m x --hunks sub/b.go:1 -- sub`
// exited 0 and committed sub/b.go WHOLE -- both hunks -- when the caller had
// asked for one.
//
// The precedence is the same one --untrack already has against an expansion
// that covers its target (pinned at the bottom of this file), and it is scoped
// to explicit-vs-expanded: two EXPLICIT arguments contradicting each other are
// still a hard error, which the control below holds.

// hunksExpansionRepo seeds a repo with sub/b.go (two hunks of modifications)
// and sub/other.go (a whole-file modification), so a directory argument naming
// sub/ has something of its own to contribute besides the hunk-selected file.
func hunksExpansionRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	sub := filepath.Join(dir, "sub")

	testutil.WriteFileAt(t, filepath.Join(sub, "b.go"), intakeEdgeNumbered(20, nil))
	testutil.WriteFileAt(t, filepath.Join(sub, "other.go"), "other original\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", "sub/b.go", "sub/other.go"); code != 0 {
		t.Fatalf("seed commit failed (%d): %s", code, stderr)
	}

	intakeEdgeTwoHunks(t, filepath.Join(sub, "b.go"))
	testutil.WriteFileAt(t, filepath.Join(sub, "other.go"), "other CHANGED\n")
	return dir
}

// assertHunkOneOnlyWithSiblings holds the whole outcome of a successful
// expansion-plus-hunk-selection commit: exactly hunk 1 of sub/b.go is in the
// committed blob, the second hunk is not, and the directory's other member came
// along whole.
func assertHunkOneOnlyWithSiblings(t *testing.T, dir string) {
	t.Helper()

	partial := testutil.MustShow(t, dir, "HEAD", "sub/b.go")
	if !strings.Contains(partial, "FIRST-CHANGE") {
		t.Errorf("sub/b.go is missing hunk 1, which is the one that was selected; content:\n%s", partial)
	}
	if strings.Contains(partial, "SECOND-CHANGE") {
		t.Errorf("sub/b.go was committed WHOLE: the hunk selection was dropped by the directory expansion; content:\n%s", partial)
	}
	if other := testutil.MustShow(t, dir, "HEAD", "sub/other.go"); !strings.Contains(other, "CHANGED") {
		t.Errorf("the directory's other member did not make it into the commit; content:\n%s", other)
	}
	// The unselected hunk is still an uncommitted modification on disk.
	if status := testutil.Git(t, dir, "status", "--porcelain"); !strings.Contains(status, "sub/b.go") {
		t.Errorf("sub/b.go should still carry the unselected hunk as a working-tree change; status: %q", status)
	}
}

// TestCommitHunkSelectionSurvivesDirectoryExpansion is the defect itself:
// --hunks first, the directory positional second.
func TestCommitHunkSelectionSurvivesDirectoryExpansion(t *testing.T) {
	dir := hunksExpansionRepo(t)

	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "hunk 1 plus the rest of sub",
		"--hunks", "sub/b.go:1", "--", "sub"); code != 0 {
		t.Fatalf("a hunk selection under a named directory was refused (%d): %s", code, stderr)
	}
	assertHunkOneOnlyWithSiblings(t, dir)
}

// TestCommitHunkSelectionSurvivesDirectoryExpansionFlagLast is the same command
// line with the directory typed BEFORE the flag (no `--`, which would make
// every later word a positional). The specs reach intake in one canonical order
// whatever the operator typed, so this pins that the outcome does not depend on
// which argument came first on the command line.
func TestCommitHunkSelectionSurvivesDirectoryExpansionFlagLast(t *testing.T) {
	dir := hunksExpansionRepo(t)

	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "the other argument order",
		"sub", "--hunks", "sub/b.go:1"); code != 0 {
		t.Fatalf("a hunk selection typed after the directory was refused (%d): %s", code, stderr)
	}
	assertHunkOneOnlyWithSiblings(t, dir)
}

// TestCommitHunkSelectionSurvivesRepositoryRootExpansion: "." is the widest
// expansion there is -- the repository root -- and it must pass over the
// hunk-selected path just the same.
func TestCommitHunkSelectionSurvivesRepositoryRootExpansion(t *testing.T) {
	dir := hunksExpansionRepo(t)

	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "hunk 1 plus the whole tree",
		"--hunks", "sub/b.go:1", "--", "."); code != 0 {
		t.Fatalf("a hunk selection under the repository root was refused (%d): %s", code, stderr)
	}
	assertHunkOneOnlyWithSiblings(t, dir)
}

// TestCommitHunkSelectionUnderDirectoryFromSubdir spells both arguments
// relative to the caller's own directory, so the exclusion has to be decided on
// canonical repo-relative paths rather than on the argument strings.
func TestCommitHunkSelectionUnderDirectoryFromSubdir(t *testing.T) {
	dir := hunksExpansionRepo(t)
	sub := filepath.Join(dir, "sub")

	if _, stderr, code := runSafegit(t, sub, "commit", "-m", "from inside sub",
		"--hunks", "b.go:1", "--", "."); code != 0 {
		t.Fatalf("a hunk selection under a directory named from a subdirectory was refused (%d): %s", code, stderr)
	}
	assertHunkOneOnlyWithSiblings(t, dir)
}

// TestAmendHunkSelectionSurvivesDirectoryExpansion: amend shares intake with
// commit, so it shares the precedence. Pinned separately because it reaches
// resolveFiles through its own entry point.
func TestAmendHunkSelectionSurvivesDirectoryExpansion(t *testing.T) {
	dir := hunksExpansionRepo(t)

	if _, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "amended",
		"--hunks", "sub/b.go:1", "--", "sub"); code != 0 {
		t.Fatalf("amend refused a hunk selection under a named directory (%d): %s", code, stderr)
	}
	assertHunkOneOnlyWithSiblings(t, dir)
}

// TestCommitExplicitWholeFileVersusHunksIsStillRefused is the control. The
// exclusion is scoped to explicit-vs-EXPANDED: naming the file itself as a
// positional and in --hunks is two explicit statements about one file, which
// stays a hard error rather than being resolved by the same precedence.
func TestCommitExplicitWholeFileVersusHunksIsStillRefused(t *testing.T) {
	dir := hunksExpansionRepo(t)
	before := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "both ways",
		"--hunks", "sub/b.go:1", "--", "sub/b.go")
	if code != exitcode.Usage {
		t.Fatalf("explicit-vs-explicit exited %d, want %d (Usage); stderr: %s", code, exitcode.Usage, stderr)
	}
	if !strings.Contains(stderr, "sub/b.go") || !strings.Contains(stderr, "--hunks") {
		t.Errorf("the refusal must name the path and the flag; stderr: %s", stderr)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", before, after)
	}
}

// TestCommitUntrackTargetSurvivesDirectoryExpansion is the analogous precedent,
// asserted here because nothing else covered the DIRECTORY case: an --untrack
// target that a named directory's expansion also sweeps up resolves to the
// removal, silently, with no conflict refusal. (A path named explicitly on both
// sides is still a hard error; that half is covered in the untrack tests.)
func TestCommitUntrackTargetSurvivesDirectoryExpansion(t *testing.T) {
	dir := newRepo(t)
	sub := filepath.Join(dir, "sub")

	testutil.WriteFileAt(t, filepath.Join(sub, "keep.txt"), "keep original\n")
	testutil.WriteFileAt(t, filepath.Join(sub, "drop.txt"), "drop me\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", "sub/keep.txt", "sub/drop.txt"); code != 0 {
		t.Fatalf("seed commit failed (%d): %s", code, stderr)
	}
	testutil.WriteFileAt(t, filepath.Join(sub, "keep.txt"), "keep CHANGED\n")

	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "untrack under a named directory",
		"--untrack", "sub/drop.txt", "--", "sub"); code != 0 {
		t.Fatalf("an --untrack target under a named directory was refused (%d): %s", code, stderr)
	}

	diff := testutil.GitRaw(t, dir, "diff-tree", "--no-commit-id", "-r", "--name-status", "HEAD")
	if !strings.Contains(diff, "D\tsub/drop.txt") {
		t.Errorf("the expansion overrode the removal: sub/drop.txt should be deleted by this commit; diff:\n%s", diff)
	}
	if !strings.Contains(diff, "M\tsub/keep.txt") {
		t.Errorf("the directory's other member did not make it into the commit; diff:\n%s", diff)
	}
	if _, err := os.Stat(filepath.Join(sub, "drop.txt")); err != nil {
		t.Errorf("sub/drop.txt must stay on disk: --untrack removes the index entry only: %v", err)
	}
}
