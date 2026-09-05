package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// A declared move is a claim about what THIS COMMIT did, so the commit's own
// tree has to bear it out: the old path gone from that tree, the new path
// present in it.
//
// The claim used to be judged against the WORKING TREE alone -- the old path
// missing from disk, the new path sitting there -- and the tree the commit
// actually wrote was never consulted. A destination the commit did not stage
// therefore produced a commit carrying the old path's deletion, no addition at
// all, and a record pointing at a path the commit does not hold: half a rename,
// silently, exit 0. The same hole on the other side produced a commit that kept
// the old path and declared it moved away.
//
// Nothing here is fixed by staging the destination on the caller's behalf.
// safegit stages the paths the caller named and no others -- that rule is what
// keeps `--moved` a statement about content rather than a second way to add
// files -- so the honest answer for a declaration the commit does not carry is
// a refusal naming the path to add to the file list.

// seedTwoCommitMove leaves a repository whose tip is a second commit, with
// `old` tracked since the first one and renamed to `new` on disk only. It is
// the state an --amend or a reword declaration acts on: the tip's own first
// parent still tracks the old path.
func seedTwoCommitMove(t *testing.T, old, new string) string {
	t.Helper()
	dir := newRepo(t)
	testutil.WriteFile(t, dir, old, "content that does not change\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", old); code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}
	testutil.WriteFile(t, dir, "unrelated.txt", "unrelated\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "tip", "--", "unrelated.txt"); code != 0 {
		t.Fatalf("tip commit failed (code %d): %s", code, stderr)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, new)), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", new, err)
	}
	if err := os.Rename(filepath.Join(dir, old), filepath.Join(dir, new)); err != nil {
		t.Fatalf("rename %s -> %s: %v", old, new, err)
	}
	return dir
}

// TestDeclaredMoveRefusesADestinationTheCommitDoesNotStage is the reported
// defect: both paths exist in the working tree, but only the old one is named,
// so the commit records the deletion and nothing else while the message claims
// the content arrived somewhere.
func TestDeclaredMoveRefusesADestinationTheCommitDoesNotStage(t *testing.T) {
	dir := seedMove(t, "a.txt", "b.txt")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "move a to b",
		"--moved", "a.txt -> b.txt", "--", "a.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	if !strings.Contains(stderr, "b.txt") {
		t.Errorf("the refusal does not name the destination: %s", stderr)
	}
	if !strings.Contains(stderr, "commit") || !strings.Contains(stderr, "tree") {
		t.Errorf("the refusal does not say the commit's tree lacks it: %s", stderr)
	}
	assertNoCommitHappened(t, dir, "seed")
}

// TestDeclaredMoveRefusesADestinationADirectoryExpansionSkipped is the same
// hole reached by naming the destination: a directory argument covers it, and
// the expansion passes it over because it is gitignored. Naming an ignored path
// explicitly is already a refusal; reaching one through an expansion is a silent
// skip, which is right for an expansion and wrong for a path a record points at.
func TestDeclaredMoveRefusesADestinationADirectoryExpansionSkipped(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, ".gitignore", ".done/\n")
	testutil.WriteFile(t, dir, "todo/a.md", "a\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", ".gitignore", "todo"); code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}
	if err := os.MkdirAll(filepath.Join(dir, "todo", ".done"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Rename(filepath.Join(dir, "todo", "a.md"), filepath.Join(dir, "todo", ".done", "a.md")); err != nil {
		t.Fatalf("rename: %v", err)
	}

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "file it away",
		"--moved", "todo/a.md -> todo/.done/a.md", "--", "todo")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	if !strings.Contains(stderr, "todo/.done/a.md") {
		t.Errorf("the refusal does not name the destination: %s", stderr)
	}
	assertNoCommitHappened(t, dir, "seed")
}

// TestDeclaredSubtreeMoveRefusesADestinationTheCommitDoesNotStage: the subtree
// form speaks for a prefix, so the question is whether the commit's tree holds
// anything under the destination prefix at all.
func TestDeclaredSubtreeMoveRefusesADestinationTheCommitDoesNotStage(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "src/one.txt", "1\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", "src"); code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}
	if err := os.Rename(filepath.Join(dir, "src"), filepath.Join(dir, "lib")); err != nil {
		t.Fatalf("rename: %v", err)
	}

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "move the directory",
		"--moved", "src/ -> lib/", "--", "src")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	if !strings.Contains(stderr, "lib") {
		t.Errorf("the refusal does not name the destination prefix: %s", stderr)
	}
	assertNoCommitHappened(t, dir, "seed")
}

// TestDeclaredMoveRefusesADestinationTheCommitDELETES covers the second way a
// declaration reaches the tree check: the destination was never on disk at all,
// and passed the earlier check by being in the base tree -- a move recorded
// after the fact. When the same commit stages that path's DELETION, the tree it
// writes carries neither side, and the refusal must not tell the caller their
// destination is sitting on disk, because it is not.
func TestDeclaredMoveRefusesADestinationTheCommitDELETES(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "a\n")
	testutil.WriteFile(t, dir, "b.txt", "b\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", "a.txt", "b.txt"); code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatalf("remove %s: %v", name, err)
		}
	}

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "delete both",
		"--moved", "a.txt -> b.txt", "--", "a.txt", "b.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	if !strings.Contains(stderr, "b.txt") {
		t.Errorf("the refusal does not name the destination: %s", stderr)
	}
	if strings.Contains(stderr, "on disk") {
		t.Errorf("the refusal claims the destination is on disk, and it is not: %s", stderr)
	}
	assertNoCommitHappened(t, dir, "seed")
}

// TestDeclaredMoveRefusesAnOldPathTheCommitStillCarries is the other side of
// the same hole: the destination is staged, the old path is gone from disk, and
// nobody named it -- so the commit is a COPY while its record says the content
// moved.
func TestDeclaredMoveRefusesAnOldPathTheCommitStillCarries(t *testing.T) {
	dir := seedMove(t, "a.txt", "b.txt")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "move a to b",
		"--moved", "a.txt -> b.txt", "--", "b.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	if !strings.Contains(stderr, "a.txt") {
		t.Errorf("the refusal does not name the old path: %s", stderr)
	}
	assertNoCommitHappened(t, dir, "seed")
}

// TestDeclaredMoveOnAnAmendRefusesAnOldPathTheTreeStillCarries: an amend
// resolves its declarations against the tip's first parent and stages into a
// tree seeded from the tip, so the same two questions have to be asked of the
// tree the amend writes.
func TestDeclaredMoveOnAnAmendRefusesAnOldPathTheTreeStillCarries(t *testing.T) {
	dir := seedTwoCommitMove(t, "a.txt", "b.txt")
	before := testutil.Rev(t, dir, "HEAD")

	// b.txt is staged, so the destination is in the amended tree -- but nothing
	// removes a.txt from it, and the tip's tree still holds it.
	_, stderr, code := runSafegit(t, dir, "commit", "--amend", "--moved", "a.txt -> b.txt", "--", "b.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	if !strings.Contains(stderr, "a.txt") {
		t.Errorf("the refusal does not name the old path: %s", stderr)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != before {
		t.Errorf("a refused amend replaced the tip (%s -> %s)", before, got)
	}
}

// TestDeclaredMoveOnARewordRefusesATreeThatDoesNotBearItOut: a reword changes
// no tree at all, so a declaration on one is a claim about the tip's own tree.
// A tip that still carries the old path and has never seen the new one bears
// out nothing, and the record would be false the moment it was written.
func TestDeclaredMoveOnARewordRefusesATreeThatDoesNotBearItOut(t *testing.T) {
	dir := seedTwoCommitMove(t, "a.txt", "b.txt")
	before := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "reworded",
		"--moved", "a.txt -> b.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != before {
		t.Errorf("a refused reword replaced the tip (%s -> %s)", before, got)
	}
	if strings.Contains(commitMessageOf(t, dir, "HEAD"), "Moved: ") {
		t.Errorf("a refused reword wrote a record:\n%s", commitMessageOf(t, dir, "HEAD"))
	}
}

// TestDeclaredMovePreviewRefusesADestinationTheCommitDoesNotStage: a preview of
// a commit safegit would refuse is the refusal, not a rehearsal of the wrong
// commit -- the same rule the other declaration refusals follow.
func TestDeclaredMovePreviewRefusesADestinationTheCommitDoesNotStage(t *testing.T) {
	dir := seedMove(t, "a.txt", "b.txt")
	_, stderr, code := runSafegit(t, dir, "--dry-run", "commit", "-m", "move",
		"--moved", "a.txt -> b.txt", "--", "a.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("dry-run exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	assertNoCommitHappened(t, dir, "seed")
}

// TestDeclaredMoveStandsWhenTheCommitCarriesBothSides is the control: the shape
// the refusals above are meant to leave untouched still commits, records the
// move, and holds both sides of it in the tree.
func TestDeclaredMoveStandsWhenTheCommitCarriesBothSides(t *testing.T) {
	dir := seedMove(t, "a.txt", "sub/b.txt")

	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "move a into sub",
		"--moved", "a.txt -> sub/b.txt", "--", "a.txt", "sub/b.txt"); code != 0 {
		t.Fatalf("the honest shape was refused (code %d): %s", code, stderr)
	}
	paths := testutil.Git(t, dir, "ls-tree", "-r", "--name-only", "HEAD")
	if !strings.Contains(paths, "sub/b.txt") {
		t.Errorf("the commit does not carry the destination:\n%s", paths)
	}
	if strings.Contains(paths, "a.txt") {
		t.Errorf("the commit still carries the old path:\n%s", paths)
	}
	if n := strings.Count(commitMessageOf(t, dir, "HEAD"), "Moved: "); n != 1 {
		t.Errorf("the commit carries %d records, want 1", n)
	}
}

// TestGitMvStagedBlobSurvivesASafegitCommit pins the shared index's state after
// the shape that produced the report: `git mv` stages the rename with the
// PRE-EDIT blob under the new name, the file is then edited, and safegit commits
// it.
//
// safegit stages from DISK, so the commit carries the edited content -- that
// half is not in question. What the shared index keeps afterwards is: git mv's
// stage-0 entry differs from the pre-commit tip, which makes it foreign staged
// work, and the reconciler preserves foreign staged work rather than deciding
// for the operator which of two blobs they meant to stage. So `git status`
// reports the path staged-and-modified while `git diff HEAD` is empty -- the
// commit is complete and the leftover entry is the operator's own, undone with
// `safegit reset --mixed HEAD`.
//
// This is not a --moved interaction: it is identical with the flag and without
// it, which is why the test asserts both.
func TestGitMvStagedBlobSurvivesASafegitCommit(t *testing.T) {
	for _, declare := range []bool{true, false} {
		name := "without --moved"
		if declare {
			name = "with --moved"
		}
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t)
			testutil.WriteFile(t, dir, "old/p.py", "one\n")
			if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", "old/p.py"); code != 0 {
				t.Fatalf("seed commit failed (code %d): %s", code, stderr)
			}
			if err := os.MkdirAll(filepath.Join(dir, "new"), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			testutil.Git(t, dir, "mv", "old/p.py", "new/p.py")
			testutil.WriteFile(t, dir, "new/p.py", "one\ntwo\n")

			args := []string{"commit", "-m", "move and edit"}
			if declare {
				args = append(args, "--moved", "old/p.py -> new/p.py")
			}
			args = append(args, "--", "old/p.py", "new/p.py")
			if _, stderr, code := runSafegit(t, dir, args...); code != 0 {
				t.Fatalf("commit failed (code %d): %s", code, stderr)
			}

			// The commit holds the WORKING TREE's content, not the blob git mv
			// staged. Nothing about the stale index entry reaches the commit.
			if got := testutil.Git(t, dir, "show", "HEAD:new/p.py"); got != "one\ntwo" {
				t.Errorf("the commit carries %q, want the edited content", got)
			}
			if got := testutil.Git(t, dir, "ls-tree", "-r", "--name-only", "HEAD"); strings.Contains(got, "old/p.py") {
				t.Errorf("the commit still carries the old path:\n%s", got)
			}

			// The working tree agrees with the commit: nothing was lost.
			if got := testutil.Git(t, dir, "diff", "HEAD", "--name-only"); got != "" {
				t.Errorf("the working tree differs from the commit: %s", got)
			}

			// git mv's own staged entry is still there, preserved as foreign
			// staged work. It is what makes `git status` say the path is both
			// staged and modified.
			if got := testutil.Git(t, dir, "status", "--porcelain"); !strings.Contains(got, "new/p.py") {
				t.Errorf("the staged entry git mv wrote did not survive: %q", got)
			}
		})
	}
}
