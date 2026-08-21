package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// `safegit commit --amend` (and its no-files spelling, reword) shares the
// commit pipeline's path machinery: internal/commit/amend.go:81 calls the same
// resolveFiles as commit, and amend.go:171 re-runs the same detectMoves. Every
// defect class pinned for plain commit therefore has an amend-flavored twin,
// and none of them was covered.
//
// Two things are unique to amend and covered only here:
//
//   - Parent reconstruction. tryAmend resolves ONE parent with
//     git.RevParse(ctx, ref+"^") (amend.go:125) and hands it to
//     git.CommitTree (amend.go:185), whose signature carries a single
//     parentSHA and emits at most one -p (internal/git/git.go:163-167).
//     tryReword does the same at amend.go:343 and amend.go:349. Amending a
//     MERGE commit therefore rebuilds it with only its first parent: the
//     merged branch is silently unmerged and its history detaches from the
//     line. git preserves every parent across `git commit --amend` (verified:
//     amending a two-parent commit, with and without new files, keeps both).
//
//   - Amending while a merge or cherry-pick is in progress. git refuses
//     outright -- "fatal: You are in the middle of a merge -- cannot amend."
//     (exit 128), and the same for a cherry-pick -- regardless of whether the
//     conflict has been staged. Nothing in the amend pipeline reads MERGE_HEAD
//     or CHERRY_PICK_HEAD.
//
// Every helper here is prefixed amendPar so this file stays self-contained and
// cannot collide with the other investigations' files in this package.

// amendParParents returns the parent SHAs of a commit, in order.
func amendParParents(t *testing.T, dir, ref string) []string {
	t.Helper()
	out := testutil.GitRaw(t, dir, "rev-list", "--parents", "-n", "1", ref)
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		t.Fatalf("rev-list --parents produced nothing for %s", ref)
	}
	return fields[1:]
}

// amendParCommit commits paths through safegit and fails the test if it does
// not succeed. Used only for fixture setup.
func amendParCommit(t *testing.T, dir, message string, paths ...string) string {
	t.Helper()
	args := append([]string{"commit", "-m", message, "--"}, paths...)
	_, stderr, code := runSafegit(t, dir, args...)
	if code != 0 {
		t.Fatalf("fixture commit %q failed (code %d): %s", message, code, stderr)
	}
	return testutil.Rev(t, dir, "HEAD")
}

// amendParStatus returns `git status --porcelain`, trimmed.
func amendParStatus(t *testing.T, dir string) string {
	t.Helper()
	out, _ := testutil.GitTry(t, dir, "status", "--porcelain")
	return strings.TrimSpace(out)
}

// ---------------------------------------------------------------------------
// Class 1: symlink collapse
// ---------------------------------------------------------------------------

// A symlink named on `safegit commit --amend` must enter the amended commit as
// the symlink object itself (mode 120000 whose blob is the link target), not
// resolved away to whatever it points at.
//
// resolveFiles (internal/commit/commit.go:387) runs resolveSymlinks over the
// full path INCLUDING the final component, so the argument "link" becomes the
// absolute path of "file.txt". Amend calls that same resolveFiles at
// amend.go:81, so the amended commit re-stages file.txt -- already in the tree
// being amended, unchanged -- and never records the link at all. Unlike plain
// commit there is no tree-unchanged refusal on the amend path, so the command
// exits 0 having silently dropped the argument.
func TestAmendSymlink_LinkToCommittedFile(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "file.txt", "target content\n")
	amendParCommit(t, dir, "add file.txt", "file.txt")

	testutil.WriteFile(t, dir, "tip.txt", "tip\n")
	tip := amendParCommit(t, dir, "tip", "tip.txt")
	tipParent := amendParParents(t, dir, tip)

	if err := os.Symlink("file.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "tip plus link", "--", "link")
	if code != 0 {
		t.Fatalf("amending in a symlink failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	head := testutil.Rev(t, dir, "HEAD")
	if head == tip {
		t.Fatalf("HEAD did not move; the amend produced no new commit")
	}
	if got := amendParParents(t, dir, head); strings.Join(got, ",") != strings.Join(tipParent, ",") {
		t.Errorf("amended commit parents = %v, want %v (the amended commit must replace the tip, not extend it)", got, tipParent)
	}

	// The symlink must be in the amended tree, as a symlink.
	mode := ""
	for _, line := range strings.Split(testutil.GitRaw(t, dir, "ls-tree", "-r", "HEAD"), "\n") {
		tab := strings.IndexByte(line, '\t')
		if tab < 0 || line[tab+1:] != "link" {
			continue
		}
		mode = strings.Fields(line[:tab])[0]
	}
	if mode != "120000" {
		t.Fatalf("expected HEAD entry %q with mode 120000, got mode %q; tree:\n%s",
			"link", mode, testutil.GitRaw(t, dir, "ls-tree", "-r", "HEAD"))
	}
	if target := testutil.GitRaw(t, dir, "cat-file", "blob", "HEAD:link"); target != "file.txt" {
		t.Errorf("symlink blob = %q, want the link target %q", target, "file.txt")
	}

	info, err := os.Lstat(filepath.Join(dir, "link"))
	if err != nil {
		t.Fatalf("lstat link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link must remain a symlink on disk, got mode %v", info.Mode())
	}
}

// ---------------------------------------------------------------------------
// Class 2: directory pathspec + a blob-matching new file
// ---------------------------------------------------------------------------

// Amending a directory pathspec whose files were deleted and pre-staged, in the
// same amend as a new file that shares a blob with one of them, must succeed.
//
// stageFile drops the whole directory from the temp index; detectMoves
// (re-run for amend at amend.go:171) then independently issues
// `git rm --cached` for an individual path inside that directory that is
// already gone from the index, and git rejects it. Exactly the plain-commit
// shape, reached through amend.
func TestAmendStagedDeletions_DirectoryPathWithMovedFile(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "dir/a.txt", "alpha\n")
	testutil.WriteFile(t, dir, "dir/b.txt", "beta\n")
	base := amendParCommit(t, dir, "add dir", "dir/a.txt", "dir/b.txt")

	testutil.WriteFile(t, dir, "tip.txt", "tip\n")
	amendParCommit(t, dir, "tip", "tip.txt")

	// The external deletion tool: removes the files and stages the deletions.
	testutil.GitRaw(t, dir, "rm", "-r", "dir")

	// dir/a.txt reappears at the root with identical content, so move
	// detection fires.
	testutil.WriteFile(t, dir, "new.txt", "alpha\n")

	stdout, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "tip, move a out of dir", "--", "dir/", "new.txt")
	if code != 0 {
		t.Fatalf("amending a directory of staged deletions alongside a moved file failed (code %d)\nstdout: %s\nstderr: %s",
			code, stdout, stderr)
	}

	head := testutil.Rev(t, dir, "HEAD")
	if parents := amendParParents(t, dir, head); len(parents) != 1 || parents[0] != base {
		t.Errorf("amended commit parents = %v, want [%s]", parents, base)
	}

	paths := testutil.TreePaths(t, dir, "HEAD")
	for _, gone := range []string{"dir/a.txt", "dir/b.txt"} {
		if testutil.Contains(paths, gone) {
			t.Errorf("%s should be absent from the amended tree, got: %v", gone, paths)
		}
	}
	for _, want := range []string{"new.txt", "tip.txt"} {
		if !testutil.Contains(paths, want) {
			t.Errorf("%s missing from the amended tree, got: %v", want, paths)
		}
	}
	if status := amendParStatus(t, dir); status != "" {
		t.Errorf("expected a clean working tree after the amend, got: %s", status)
	}
}

// The same shape with no deliberate move: an empty file deleted with the
// directory and an unrelated new empty file share the empty blob, which is all
// it takes for move detection to fire.
func TestAmendStagedDeletions_DirectoryPathWithUnrelatedEmptyFile(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "dir/empty.txt", "")
	testutil.WriteFile(t, dir, "dir/b.txt", "beta\n")
	amendParCommit(t, dir, "add dir", "dir/empty.txt", "dir/b.txt")

	testutil.WriteFile(t, dir, "tip.txt", "tip\n")
	amendParCommit(t, dir, "tip", "tip.txt")

	testutil.GitRaw(t, dir, "rm", "-r", "dir")
	testutil.WriteFile(t, dir, "notes.md", "")

	stdout, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "tip, remove dir, add notes", "--", "dir/", "notes.md")
	if code != 0 {
		t.Fatalf("amending a directory of staged deletions alongside an unrelated empty file failed (code %d)\nstdout: %s\nstderr: %s",
			code, stdout, stderr)
	}

	paths := testutil.TreePaths(t, dir, "HEAD")
	for _, gone := range []string{"dir/empty.txt", "dir/b.txt"} {
		if testutil.Contains(paths, gone) {
			t.Errorf("%s should be absent from the amended tree, got: %v", gone, paths)
		}
	}
	if !testutil.Contains(paths, "notes.md") {
		t.Errorf("notes.md missing from the amended tree, got: %v", paths)
	}
}

// The control: naming the individual file paths instead of the directory takes
// the branch move detection skips, and must keep working after any fix.
func TestAmendStagedDeletions_FilePathsWithMovedFile(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "dir/a.txt", "alpha\n")
	testutil.WriteFile(t, dir, "dir/b.txt", "beta\n")
	amendParCommit(t, dir, "add dir", "dir/a.txt", "dir/b.txt")

	testutil.WriteFile(t, dir, "tip.txt", "tip\n")
	amendParCommit(t, dir, "tip", "tip.txt")

	testutil.GitRaw(t, dir, "rm", "-r", "dir")
	testutil.WriteFile(t, dir, "new.txt", "alpha\n")

	stdout, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "tip, move a out of dir", "--",
		"dir/a.txt", "dir/b.txt", "new.txt")
	if code != 0 {
		t.Fatalf("amending staged deletions by file path failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	paths := testutil.TreePaths(t, dir, "HEAD")
	for _, gone := range []string{"dir/a.txt", "dir/b.txt"} {
		if testutil.Contains(paths, gone) {
			t.Errorf("%s should be absent from the amended tree, got: %v", gone, paths)
		}
	}
	if !testutil.Contains(paths, "new.txt") {
		t.Errorf("new.txt missing from the amended tree, got: %v", paths)
	}
}

// ---------------------------------------------------------------------------
// Class 3: subdirectory-relative path with no pending change
// ---------------------------------------------------------------------------

// Invoked from a repository subdirectory with two cwd-relative arguments, one
// of which has no pending change, `commit --amend` must resolve both against
// the invoking cwd. Move detection resolves the unchanged relative path against
// the repository ROOT instead, so the mis-resolved path is absent from the temp
// index and the underlying `git rm --cached` fails.
func TestAmendFromSubdirRelativePathNoPendingChange(t *testing.T) {
	dir := newRepo(t)
	sub := filepath.Join(dir, "sub")

	testutil.WriteFile(t, dir, "sub/unchanged.txt", "unchanged\n")
	testutil.WriteFile(t, dir, "sub/edited.txt", "before\n")
	amendParCommit(t, dir, "add sub files", "sub/unchanged.txt", "sub/edited.txt")

	testutil.WriteFile(t, dir, "tip.txt", "tip\n")
	tip := amendParCommit(t, dir, "tip", "tip.txt")
	tipParents := amendParParents(t, dir, tip)

	testutil.WriteFile(t, dir, "sub/edited.txt", "after\n")

	stdout, stderr, code := runSafegit(t, sub, "commit", "--amend", "-m", "tip plus subdir edit", "--",
		"edited.txt", "unchanged.txt")
	if code != 0 {
		t.Fatalf("amending from a subdirectory with an unchanged relative path failed (code %d)\nstdout:\n%s\nstderr:\n%s",
			code, stdout, stderr)
	}

	if got := amendParParents(t, dir, "HEAD"); strings.Join(got, ",") != strings.Join(tipParents, ",") {
		t.Errorf("amended commit parents = %v, want %v", got, tipParents)
	}
	if got, ok := testutil.Show(t, dir, "HEAD", "sub/edited.txt"); !ok || got != "after\n" {
		t.Errorf("sub/edited.txt at HEAD = %q (present=%t), want %q", got, ok, "after\n")
	}
	if got, ok := testutil.Show(t, dir, "HEAD", "sub/unchanged.txt"); !ok || got != "unchanged\n" {
		t.Errorf("sub/unchanged.txt at HEAD = %q (present=%t): naming a path with no pending change must never remove it", got, ok)
	}
	if status := amendParStatus(t, dir); status != "" {
		t.Errorf("expected a clean working tree after the amend, got: %s", status)
	}
}

// The control from the repository root, where relative and repo-relative
// spellings coincide. If this fails too, the defect is broader than relative
// path resolution.
func TestAmendFromRepoRootUnchangedPath(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "unchanged.txt", "unchanged\n")
	testutil.WriteFile(t, dir, "edited.txt", "before\n")
	amendParCommit(t, dir, "add root files", "unchanged.txt", "edited.txt")

	testutil.WriteFile(t, dir, "tip.txt", "tip\n")
	amendParCommit(t, dir, "tip", "tip.txt")

	testutil.WriteFile(t, dir, "edited.txt", "after\n")

	stdout, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "tip plus root edit", "--",
		"edited.txt", "unchanged.txt")
	if code != 0 {
		t.Fatalf("amending from the repo root with an unchanged path failed (code %d)\nstdout:\n%s\nstderr:\n%s",
			code, stdout, stderr)
	}
	if got, ok := testutil.Show(t, dir, "HEAD", "edited.txt"); !ok || got != "after\n" {
		t.Errorf("edited.txt at HEAD = %q (present=%t), want %q", got, ok, "after\n")
	}
	if got, ok := testutil.Show(t, dir, "HEAD", "unchanged.txt"); !ok || got != "unchanged\n" {
		t.Errorf("unchanged.txt at HEAD = %q (present=%t), want %q", got, ok, "unchanged\n")
	}
}

// ---------------------------------------------------------------------------
// Class 4: gitignored-path refusal blocks untracking
// ---------------------------------------------------------------------------

// The untrack cleanup -- add the pattern to .gitignore, `git rm -r --cached`
// the tracked copies, record both in one commit -- has no amend-mediated form
// either. resolveFiles (internal/commit/commit.go:411) refuses any named path
// that exists on disk and is gitignored, which is precisely the path being
// untracked. Amend reaches the same check through amend.go:81.
func TestAmendUntrackGitignoredPath(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "dir/junk.txt", "build artifact\n")
	amendParCommit(t, dir, "add junk", "dir/junk.txt")

	testutil.WriteFile(t, dir, "tip.txt", "tip\n")
	amendParCommit(t, dir, "tip", "tip.txt")

	// The pattern that makes the tracked file ignored from now on.
	testutil.WriteFile(t, dir, ".gitignore", "dir/\n")
	// The operator's own step: stage the removal, keep the file on disk.
	testutil.GitRaw(t, dir, "rm", "-r", "--cached", "dir")
	if _, err := os.Stat(filepath.Join(dir, "dir", "junk.txt")); err != nil {
		t.Fatalf("dir/junk.txt must survive `git rm --cached` on disk: %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "tip, untrack dir", "--",
		".gitignore", "dir/junk.txt")
	if code != 0 {
		t.Fatalf("amending the untrack cleanup failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	paths := testutil.TreePaths(t, dir, "HEAD")
	if !testutil.Contains(paths, ".gitignore") {
		t.Errorf(".gitignore missing from the amended tree, got: %v", paths)
	}
	if testutil.Contains(paths, "dir/junk.txt") {
		t.Errorf("dir/junk.txt should no longer be tracked, amended tree: %v", paths)
	}
	if _, err := os.Stat(filepath.Join(dir, "dir", "junk.txt")); err != nil {
		t.Errorf("dir/junk.txt must remain on disk after being untracked: %v", err)
	}
}

// The cross-branch amend sharpens the same class of check. resolveFiles answers
// "is this path tracked?" with git.IsTracked (internal/commit/commit.go:402),
// which reads the CURRENT branch's index -- but `--amend --branch other`
// rebuilds a commit on `other`, seeding its temp index from that branch's tip
// (amend.go:150). A path that is tracked on `other` and absent from both the
// working tree and the current branch is therefore rejected as "does not exist
// and is not tracked by git", even though the operation -- record its deletion
// on the target branch -- is exactly what the tracked check should have
// allowed. The check answers a question about the wrong ref.
func TestAmendCrossBranchDeletionOfPathTrackedOnlyOnTarget(t *testing.T) {
	dir := newRepo(t)

	// `other` carries only-on-other.txt; main never sees it.
	testutil.GitRaw(t, dir, "branch", "other")
	testutil.GitRaw(t, dir, "switch", "other")
	testutil.WriteFile(t, dir, "only-on-other.txt", "other content\n")
	otherTip := amendParCommit(t, dir, "add only-on-other", "only-on-other.txt")
	otherParents := amendParParents(t, dir, otherTip)

	testutil.GitRaw(t, dir, "switch", "main")
	if _, err := os.Stat(filepath.Join(dir, "only-on-other.txt")); !os.IsNotExist(err) {
		t.Fatalf("fixture: only-on-other.txt must be absent from main's working tree, stat err = %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "--amend", "--branch", "other",
		"-m", "add only-on-other (dropped again)", "--", "only-on-other.txt")
	if code != 0 {
		t.Fatalf("cross-branch amend recording a deletion of a path tracked only on the target branch "+
			"failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	if got := amendParParents(t, dir, "refs/heads/other"); strings.Join(got, ",") != strings.Join(otherParents, ",") {
		t.Errorf("other's amended tip parents = %v, want %v", got, otherParents)
	}
	if paths := testutil.TreePaths(t, dir, "refs/heads/other"); testutil.Contains(paths, "only-on-other.txt") {
		t.Errorf("only-on-other.txt should be gone from other's amended tip, got: %v", paths)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != testutil.Rev(t, dir, "refs/heads/main") {
		t.Errorf("HEAD left main during a cross-branch amend")
	}
}

// ---------------------------------------------------------------------------
// Class 5: merge commits
// ---------------------------------------------------------------------------

// amendParMergeFixture is a repository whose tip is a genuine two-parent merge
// commit, produced through safegit's own merge.
type amendParMergeFixture struct {
	dir        string
	mergeSHA   string
	mainSHA    string // first parent
	featureSHA string // second parent
}

// amendParNewMergedRepo builds main and feature diverging on different files
// (so the merge cannot fast-forward) and merges feature into main cleanly.
func amendParNewMergedRepo(t *testing.T) amendParMergeFixture {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "base.txt", "base\n")
	amendParCommit(t, dir, "base", "base.txt")

	testutil.GitRaw(t, dir, "branch", "feature")
	testutil.GitRaw(t, dir, "switch", "feature")
	testutil.WriteFile(t, dir, "f.txt", "feature\n")
	featureSHA := amendParCommit(t, dir, "feature edit", "f.txt")

	testutil.GitRaw(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "m.txt", "main\n")
	mainSHA := amendParCommit(t, dir, "main edit", "m.txt")

	stdout, stderr, code := runSafegit(t, dir, "merge", "feature")
	if code != 0 {
		t.Fatalf("safegit merge feature failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	mergeSHA := testutil.Rev(t, dir, "HEAD")
	parents := amendParParents(t, dir, mergeSHA)
	if len(parents) != 2 || parents[0] != mainSHA || parents[1] != featureSHA {
		t.Fatalf("fixture: HEAD is not the expected merge commit; parents = %v, want [%s %s]",
			parents, mainSHA, featureSHA)
	}
	return amendParMergeFixture{dir: dir, mergeSHA: mergeSHA, mainSHA: mainSHA, featureSHA: featureSHA}
}

// Rewording a merge commit must keep both parents. `git commit --amend -m` on a
// two-parent commit preserves both; safegit's Reword resolves a single parent
// with `ref^` (internal/commit/amend.go:343) and hands it to git.CommitTree
// (amend.go:349), whose one -p (internal/git/git.go:163-167) cannot express the
// second. The reworded tip therefore has one parent, and the merged branch is
// silently unmerged.
func TestRewordMergeCommitPreservesBothParents(t *testing.T) {
	fx := amendParNewMergedRepo(t)

	stdout, stderr, code := runSafegit(t, fx.dir, "commit", "--amend", "-m", "Merge feature (reworded)")
	if code != 0 {
		t.Fatalf("rewording a merge commit failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	head := testutil.Rev(t, fx.dir, "HEAD")
	parents := amendParParents(t, fx.dir, head)
	if len(parents) != 2 {
		t.Fatalf("reworded merge commit has %d parent(s) (%v), want 2 (%s, %s): the second parent was dropped, "+
			"so feature is no longer merged into main",
			len(parents), parents, fx.mainSHA, fx.featureSHA)
	}
	if parents[0] != fx.mainSHA {
		t.Errorf("first parent = %s, want %s", parents[0], fx.mainSHA)
	}
	if parents[1] != fx.featureSHA {
		t.Errorf("second parent = %s, want %s", parents[1], fx.featureSHA)
	}

	// A reword changes nothing but the message.
	if got, want := testutil.Rev(t, fx.dir, "HEAD^{tree}"), testutil.Rev(t, fx.dir, fx.mergeSHA+"^{tree}"); got != want {
		t.Errorf("reworded tree = %s, want the merge's own tree %s", got, want)
	}
	if msg := testutil.GitRaw(t, fx.dir, "log", "-1", "--format=%s"); !strings.Contains(msg, "reworded") {
		t.Errorf("message was not reworded: %q", strings.TrimSpace(msg))
	}
	// feature must still be an ancestor of the tip.
	if _, code := testutil.GitTry(t, fx.dir, "merge-base", "--is-ancestor", fx.featureSHA, "HEAD"); code != 0 {
		t.Errorf("feature (%s) is no longer an ancestor of the reworded tip", fx.featureSHA)
	}
}

// Amending a merge commit WITH new files must likewise keep both parents.
// `git commit --amend` with staged changes on a two-parent commit preserves
// both. tryAmend resolves one parent at internal/commit/amend.go:125 and passes
// it to git.CommitTree at amend.go:185.
func TestAmendMergeCommitWithFilesPreservesBothParents(t *testing.T) {
	fx := amendParNewMergedRepo(t)

	testutil.WriteFile(t, fx.dir, "extra.txt", "extra\n")

	stdout, stderr, code := runSafegit(t, fx.dir, "commit", "--amend", "-m", "Merge feature plus extra", "--", "extra.txt")
	if code != 0 {
		t.Fatalf("amending a merge commit with a new file failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	head := testutil.Rev(t, fx.dir, "HEAD")
	parents := amendParParents(t, fx.dir, head)
	if len(parents) != 2 {
		t.Fatalf("amended merge commit has %d parent(s) (%v), want 2 (%s, %s): the second parent was dropped",
			len(parents), parents, fx.mainSHA, fx.featureSHA)
	}
	if parents[0] != fx.mainSHA || parents[1] != fx.featureSHA {
		t.Errorf("parents = %v, want [%s %s]", parents, fx.mainSHA, fx.featureSHA)
	}

	paths := testutil.TreePaths(t, fx.dir, "HEAD")
	for _, want := range []string{"extra.txt", "m.txt", "f.txt", "base.txt"} {
		if !testutil.Contains(paths, want) {
			t.Errorf("%s missing from the amended merge tree, got: %v", want, paths)
		}
	}
	if _, code := testutil.GitTry(t, fx.dir, "merge-base", "--is-ancestor", fx.featureSHA, "HEAD"); code != 0 {
		t.Errorf("feature (%s) is no longer an ancestor of the amended tip", fx.featureSHA)
	}
}

// The cross-branch spelling reaches the same single-parent reconstruction on a
// branch that is not checked out, where nothing in the working tree hints that
// a merge was just undone.
func TestRewordMergeCommitCrossBranchPreservesBothParents(t *testing.T) {
	fx := amendParNewMergedRepo(t)

	// Step off main so the reword targets a branch that is not HEAD.
	testutil.GitRaw(t, fx.dir, "switch", "feature")

	stdout, stderr, code := runSafegit(t, fx.dir, "commit", "--amend", "--branch", "main",
		"-m", "Merge feature (reworded cross-branch)")
	if code != 0 {
		t.Fatalf("cross-branch reword of a merge commit failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	parents := amendParParents(t, fx.dir, "refs/heads/main")
	if len(parents) != 2 {
		t.Fatalf("cross-branch reworded merge commit has %d parent(s) (%v), want 2 (%s, %s)",
			len(parents), parents, fx.mainSHA, fx.featureSHA)
	}
	if parents[0] != fx.mainSHA || parents[1] != fx.featureSHA {
		t.Errorf("parents = %v, want [%s %s]", parents, fx.mainSHA, fx.featureSHA)
	}
}

// ---------------------------------------------------------------------------
// Class 5b: amending while a merge or cherry-pick is in progress
// ---------------------------------------------------------------------------

// amendParConflictedMergeRepo parks a repo mid-merge: MERGE_HEAD names feature,
// the index is unmerged, and the conflict has been resolved in the working tree
// but not staged.
func amendParConflictedMergeRepo(t *testing.T) (dir, mainSHA, featureSHA string) {
	t.Helper()
	dir = newRepo(t)

	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nbase\nline3\n")
	amendParCommit(t, dir, "base", "conflicted.txt")

	testutil.GitRaw(t, dir, "branch", "feature")
	testutil.GitRaw(t, dir, "switch", "feature")
	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nfeature\nline3\n")
	featureSHA = amendParCommit(t, dir, "feature edit", "conflicted.txt")

	testutil.GitRaw(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nmain\nline3\n")
	mainSHA = amendParCommit(t, dir, "main edit", "conflicted.txt")

	stdout, stderr, code := runSafegit(t, dir, "merge", "feature")
	if code == 0 {
		t.Fatalf("fixture needs a conflict; safegit merge feature succeeded\nstdout=%s stderr=%s", stdout, stderr)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".git", "MERGE_HEAD"))
	if err != nil {
		t.Fatalf("reading .git/MERGE_HEAD: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != featureSHA {
		t.Fatalf("MERGE_HEAD = %s, want %s", got, featureSHA)
	}

	// Resolve in the working tree, leaving the index unmerged.
	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nresolved\nline3\n")
	return dir, mainSHA, featureSHA
}

// amendParAssertStillMerging fails unless MERGE_HEAD is still present and names
// want.
func amendParAssertStillMerging(t *testing.T, dir, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".git", "MERGE_HEAD"))
	if err != nil {
		t.Fatalf(".git/MERGE_HEAD must survive a refused amend: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != want {
		t.Fatalf("MERGE_HEAD = %s, want %s", got, want)
	}
}

// git refuses `git commit --amend` outright while a merge is in progress:
//
//	fatal: You are in the middle of a merge -- cannot amend.
//
// It refuses whether or not the resolution has been staged, because the amended
// commit would replace the tip the merge is being built on top of while
// MERGE_HEAD still points at the other side. safegit must refuse too. Nothing
// in Amend or Reword reads MERGE_HEAD, so both spellings run and rewrite the
// tip out from under the merge.
func TestAmendRefusedWhileMerging(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"reword", []string{"commit", "--amend", "-m", "rewritten mid-merge"}},
		{"amend-with-file", []string{"commit", "--amend", "-m", "rewritten mid-merge", "--", "conflicted.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, mainSHA, featureSHA := amendParConflictedMergeRepo(t)

			// git's own refusal, recorded so the test documents the contract
			// safegit is expected to match.
			gitOut, gitCode := testutil.GitTry(t, dir, "commit", "--amend", "-m", "rewritten mid-merge")
			if gitCode == 0 {
				t.Fatalf("git commit --amend mid-merge succeeded; expected a refusal\n%s", gitOut)
			}
			if !strings.Contains(gitOut, "cannot amend") {
				t.Fatalf("git refused for an unexpected reason (code %d): %s", gitCode, strings.Join(strings.Fields(gitOut), " "))
			}

			before := testutil.Rev(t, dir, "HEAD")
			if before != mainSHA {
				t.Fatalf("fixture: HEAD = %s, want the pre-merge main tip %s", before, mainSHA)
			}

			stdout, stderr, code := runSafegit(t, dir, tc.args...)
			if code == 0 {
				head := testutil.Rev(t, dir, "HEAD")
				t.Fatalf("safegit %s succeeded mid-merge (git refuses the same command).\n"+
					"  new tip: %s (was %s)\n"+
					"  parents: %v\n"+
					"  tree: %v\n"+
					"  MERGE_HEAD still present: %t\n"+
					"  stdout: %s",
					strings.Join(tc.args, " "), head, before,
					amendParParents(t, dir, head), testutil.TreePaths(t, dir, "HEAD"),
					!amendParMergeStateGone(t, dir), strings.Join(strings.Fields(stdout), " "))
			}

			if head := testutil.Rev(t, dir, "HEAD"); head != before {
				t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, before)
			}
			amendParAssertStillMerging(t, dir, featureSHA)
			if !strings.Contains(strings.ToLower(stderr), "merge") {
				t.Errorf("the refusal does not mention the merge: %s", strings.Join(strings.Fields(stderr), " "))
			}
		})
	}
}

// amendParMergeStateGone reports whether git considers the merge concluded.
func amendParMergeStateGone(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD"))
	return os.IsNotExist(err)
}

// git refuses an amend during a cherry-pick for the same reason:
//
//	fatal: You are in the middle of a cherry-pick -- cannot amend.
//
// safegit's cherry-pick is a guarded passthrough, so CHERRY_PICK_HEAD is left
// behind on a conflict exactly as git leaves it, and the amend pipeline never
// reads it.
func TestAmendRefusedWhileCherryPicking(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "c.txt", "base\n")
	amendParCommit(t, dir, "base", "c.txt")

	testutil.GitRaw(t, dir, "branch", "feature")
	testutil.GitRaw(t, dir, "switch", "feature")
	testutil.WriteFile(t, dir, "c.txt", "feature\n")
	amendParCommit(t, dir, "feature edit", "c.txt")

	testutil.GitRaw(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "c.txt", "main\n")
	mainSHA := amendParCommit(t, dir, "main edit", "c.txt")

	stdout, stderr, code := runSafegit(t, dir, "cherry-pick", "feature")
	if code == 0 {
		t.Fatalf("fixture needs a cherry-pick conflict; it succeeded\nstdout=%s stderr=%s", stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "CHERRY_PICK_HEAD")); err != nil {
		t.Fatalf("fixture: .git/CHERRY_PICK_HEAD must exist after the conflict: %v", err)
	}

	gitOut, gitCode := testutil.GitTry(t, dir, "commit", "--amend", "-m", "rewritten mid-cherry-pick")
	if gitCode == 0 || !strings.Contains(gitOut, "cannot amend") {
		t.Fatalf("git commit --amend mid-cherry-pick did not refuse as expected (code %d): %s",
			gitCode, strings.Join(strings.Fields(gitOut), " "))
	}

	stdout, stderr, code = runSafegit(t, dir, "commit", "--amend", "-m", "rewritten mid-cherry-pick")
	if code == 0 {
		head := testutil.Rev(t, dir, "HEAD")
		t.Fatalf("safegit commit --amend succeeded mid-cherry-pick (git refuses the same command).\n"+
			"  new tip: %s (was %s)\n"+
			"  CHERRY_PICK_HEAD still present: %t\n"+
			"  stdout: %s",
			head, mainSHA,
			amendParFileExists(filepath.Join(dir, ".git", "CHERRY_PICK_HEAD")),
			strings.Join(strings.Fields(stdout), " "))
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != mainSHA {
		t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, mainSHA)
	}
	if !strings.Contains(strings.ToLower(stderr), "cherry") {
		t.Errorf("the refusal does not mention the cherry-pick: %s", strings.Join(strings.Fields(stderr), " "))
	}
}

// amendParFileExists reports whether path exists.
func amendParFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
