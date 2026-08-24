package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// lsTreeHEAD returns `git ls-tree -r HEAD` output, which carries the mode of
// every entry ("120000" for a symlink, "100644" for a regular file). The
// testutil.TreePaths listing drops the modes, which are the whole point here.
func lsTreeHEAD(t *testing.T, repoDir string) string {
	t.Helper()
	return testutil.GitRaw(t, repoDir, "ls-tree", "-r", "HEAD")
}

// treeEntryMode returns the mode of path in the HEAD tree, or "" if absent.
func treeEntryMode(t *testing.T, repoDir, path string) string {
	t.Helper()
	for _, line := range strings.Split(lsTreeHEAD(t, repoDir), "\n") {
		// Format: "<mode> <type> <sha>\t<path>"
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		if line[tab+1:] != path {
			continue
		}
		fields := strings.Fields(line[:tab])
		if len(fields) == 0 {
			continue
		}
		return fields[0]
	}
	return ""
}

// catFileBlob returns the content of the blob at path in the HEAD tree. For a
// symlink entry the blob content IS the link target. It reads the blob rather
// than going through testutil.MustShow so that no path-based interpretation
// (or filter) can stand between the object store and the assertion.
func catFileBlob(t *testing.T, repoDir, path string) string {
	t.Helper()
	return testutil.GitRaw(t, repoDir, "cat-file", "blob", "HEAD:"+path)
}

// TestCommitSymlink_LinkToCommittedFile checks that a symlink passed to
// `safegit commit` is committed as the symlink object itself (mode 120000),
// not resolved away to its target.
//
// Regression: internal/commit/commit.go resolveFiles() runs resolveSymlinks()
// (filepath.EvalSymlinks) on the full path including the final component, so
// the argument "link" collapses to the target "file.txt". Because file.txt is
// already committed unchanged, the resulting tree equals the parent tree and
// the commit is refused with "nothing to commit (tree unchanged)" -- making
// symlinks uncommittable through safegit.
func TestCommitSymlink_LinkToCommittedFile(t *testing.T) {
	dir := newRepo(t)

	// Commit the symlink's future target first, so its content is already in
	// the tree and resolving the link away produces no tree change at all.
	testutil.WriteFile(t, dir, "file.txt", "target content\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "add file.txt", "--", "file.txt"); code != 0 {
		t.Fatalf("committing file.txt failed (code %d): %s", code, stderr)
	}

	if err := os.Symlink("file.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "add link", "--", "link")
	if code != 0 {
		t.Fatalf("committing symlink failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	if mode := treeEntryMode(t, dir, "link"); mode != "120000" {
		t.Fatalf("expected HEAD entry \"link\" with mode 120000, got mode %q; tree:\n%s",
			mode, lsTreeHEAD(t, dir))
	}

	if target := catFileBlob(t, dir, "link"); target != "file.txt" {
		t.Fatalf("expected symlink blob to hold the target path %q, got %q", "file.txt", target)
	}

	// The symlink must still be a symlink on disk -- committing it must not
	// have replaced it with its target's content.
	info, err := os.Lstat(filepath.Join(dir, "link"))
	if err != nil {
		t.Fatalf("lstat link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected link to remain a symlink on disk, got mode %v", info.Mode())
	}
}

// TestCommitSymlinkEscapingTargetIsCommittedWhenElected: a symlink whose
// target leaves the repository is REFUSED (see
// TestWave2CommitEscapingSymlinkIsRefused) -- the object it would write is a
// reference to a place only this machine has. --allow-escaping-targets is the
// election, and it restores the one-line notice the refusal replaced: the link
// text is what gets recorded, and it resolves to nothing in another checkout.
func TestCommitSymlinkEscapingTargetIsCommittedWhenElected(t *testing.T) {
	dir := newRepo(t)

	if err := os.Symlink("../elsewhere/secret.txt", filepath.Join(dir, "escapes")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "--allow-escaping-targets",
		"-m", "add escaping link", "--", "escapes")
	if code != 0 {
		t.Fatalf("an elected escaping symlink was refused (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if mode := treeEntryMode(t, dir, "escapes"); mode != "120000" {
		t.Errorf("expected HEAD entry %q with mode 120000, got mode %q; tree:\n%s", "escapes", mode, lsTreeHEAD(t, dir))
	}
	if target := catFileBlob(t, dir, "escapes"); target != "../elsewhere/secret.txt" {
		t.Errorf("symlink blob = %q, want the link text %q", target, "../elsewhere/secret.txt")
	}
	if !strings.Contains(stderr, "notice:") || !strings.Contains(stderr, "escapes") ||
		!strings.Contains(stderr, "outside the repository") {
		t.Errorf("expected a one-line stderr notice naming the link and saying its target is outside the repository, got:\n%s", stderr)
	}
}

// TestCommitEscapingSymlinkRefusalNamesTheLiteralTarget: the refusal has to say
// the link's own TEXT, not a resolved absolute path, because the text is what
// would be committed and what the operator has to recognize. A relative target
// that never resolves anywhere is the sharpest case: there is nothing to
// resolve, and only the literal answer exists.
func TestCommitEscapingSymlinkRefusalNamesTheLiteralTarget(t *testing.T) {
	dir := newRepo(t)

	const target = "../../nowhere/at/all.txt"
	if err := os.Symlink(target, filepath.Join(dir, "escapes")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	before := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add escaping link", "--", "escapes")
	if code != exitcode.EscapingSymlinkTarget {
		t.Errorf("an escaping symlink exited %d, want %d (EscapingSymlinkTarget); stderr: %s",
			code, exitcode.EscapingSymlinkTarget, stderr)
	}
	if !strings.Contains(stderr, target) {
		t.Errorf("the refusal must name the literal target %q; stderr:\n%s", target, stderr)
	}
	if !strings.Contains(stderr, "--allow-escaping-targets") {
		t.Errorf("the refusal must name the flag that elects recording it; stderr:\n%s", stderr)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", before, after)
	}
}

// TestCommitEscapingSymlinkRefusalNamesEveryOffender: intake resolves the whole
// argument list before it judges, so one invocation naming several escaping
// links is one refusal naming all of them -- not the first one, discovered
// again on the next attempt.
func TestCommitEscapingSymlinkRefusalNamesEveryOffender(t *testing.T) {
	dir := newRepo(t)

	for _, link := range []struct{ name, target string }{
		{"one", "../elsewhere/first.txt"},
		{"two", "../elsewhere/second.txt"},
	} {
		if err := os.Symlink(link.target, filepath.Join(dir, link.name)); err != nil {
			t.Fatalf("creating symlink %s: %v", link.name, err)
		}
	}

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add two escaping links", "--", "one", "two")
	if code != exitcode.EscapingSymlinkTarget {
		t.Fatalf("two escaping symlinks exited %d, want %d (EscapingSymlinkTarget); stderr: %s",
			code, exitcode.EscapingSymlinkTarget, stderr)
	}
	for _, want := range []string{"../elsewhere/first.txt", "../elsewhere/second.txt"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not name %q; stderr:\n%s", want, stderr)
		}
	}
}

// TestAmendEscapingSymlinkIsRefusedAndElects: the refusal is made in intake,
// which the amend path shares, so --amend inherits both halves of the ruling
// from the one place both forms resolve their files.
func TestAmendEscapingSymlinkIsRefusedAndElects(t *testing.T) {
	dir := newRepo(t)

	const target = "../elsewhere/secret.txt"
	if err := os.Symlink(target, filepath.Join(dir, "escapes")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	before := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "amend in the link", "--", "escapes")
	if code != exitcode.EscapingSymlinkTarget {
		t.Errorf("an --amend of an escaping symlink exited %d, want %d (EscapingSymlinkTarget); stderr: %s",
			code, exitcode.EscapingSymlinkTarget, stderr)
	}
	if !strings.Contains(stderr, target) {
		t.Errorf("the refusal must name the escaping target %q; stderr:\n%s", target, stderr)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", before, after)
	}

	_, stderr, code = runSafegit(t, dir, "commit", "--amend", "--allow-escaping-targets",
		"-m", "amend in the link", "--", "escapes")
	if code != 0 {
		t.Fatalf("an elected --amend was refused (code %d): %s", code, stderr)
	}
	if mode := treeEntryMode(t, dir, "escapes"); mode != "120000" {
		t.Errorf("expected HEAD entry %q with mode 120000, got mode %q; tree:\n%s", "escapes", mode, lsTreeHEAD(t, dir))
	}
	if !strings.Contains(stderr, "outside the repository") {
		t.Errorf("the elected amend must restore the notice; stderr:\n%s", stderr)
	}
}

// TestCommitSymlinkInsideTargetIsSilent is the control for the notice above: a
// symlink whose target stays inside the repository is ordinary and says
// nothing.
func TestCommitSymlinkInsideTargetIsSilent(t *testing.T) {
	dir := newRepo(t)

	if err := os.Symlink("seed.txt", filepath.Join(dir, "inside")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add inside link", "--", "inside")
	if code != 0 {
		t.Fatalf("committing an in-repository symlink failed (code %d): %s", code, stderr)
	}
	if strings.Contains(stderr, "outside the repository") {
		t.Errorf("a symlink that stays inside the repository must produce no notice, got:\n%s", stderr)
	}
}

// TestCommitHunkSelectionOnSymlinkIsRefused: a symlink's whole content is the
// path it points at -- one line the filesystem produces -- so there are no
// hunks to choose between and a selection could only ever select nothing. The
// refusal is typed, and it says what to do instead.
func TestCommitHunkSelectionOnSymlinkIsRefused(t *testing.T) {
	dir := newRepo(t)

	if err := os.Symlink("seed.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "hunks of a symlink", "--hunks", "link:1")
	if code != exitcode.SymlinkHunkSpec {
		t.Errorf("a hunk selection on a symlink exited %d, want %d (SymlinkHunkSpec); stderr: %s",
			code, exitcode.SymlinkHunkSpec, stderr)
	}
	if !strings.Contains(stderr, "symlink") {
		t.Errorf("the refusal must say the path is a symlink; stderr: %s", stderr)
	}
	if _, ok := testutil.Show(t, dir, "HEAD", "link"); ok {
		t.Error("the refused commit must not have recorded the link")
	}
}

// TestAmendHunkSelectionOnSymlinkIsRefused is the same refusal on the amend
// path, which reaches intake through its own code.
func TestAmendHunkSelectionOnSymlinkIsRefused(t *testing.T) {
	dir := newRepo(t)

	if err := os.Symlink("seed.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	before := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "hunks of a symlink", "--hunks", "link:1")
	if code != exitcode.SymlinkHunkSpec {
		t.Errorf("an --amend hunk selection on a symlink exited %d, want %d (SymlinkHunkSpec); stderr: %s",
			code, exitcode.SymlinkHunkSpec, stderr)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", before, after)
	}
}

// TestCommitDirectorySymlinkBareNameCommitsTheLink and its trailing-slash twin
// below are the two halves of the disambiguation: the spelling of the argument,
// and nothing on disk, decides whether a directory symlink means the link
// object or the directory it points at.
func TestCommitDirectorySymlinkBareNameCommitsTheLink(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "real/inner.txt", "inner\n")
	if err := os.Symlink("real", filepath.Join(dir, "linkdir")); err != nil {
		t.Fatalf("creating directory symlink: %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "commit the link itself", "--", "linkdir")
	if code != 0 {
		t.Fatalf("committing a bare directory-symlink name failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	if mode := treeEntryMode(t, dir, "linkdir"); mode != "120000" {
		t.Errorf("expected HEAD entry %q with mode 120000, got mode %q; tree:\n%s", "linkdir", mode, lsTreeHEAD(t, dir))
	}
	if target := catFileBlob(t, dir, "linkdir"); target != "real" {
		t.Errorf("symlink blob = %q, want %q", target, "real")
	}
	if _, ok := testutil.Show(t, dir, "HEAD", "real/inner.txt"); ok {
		t.Error("naming the link itself must not commit anything through it")
	}
}

// TestCommitDirectorySymlinkTrailingSlashCommitsThroughIt: the same argument
// with a trailing separator means the directory the link points at, and the
// paths committed are that directory's own -- not names invented under the
// link, which no checkout could reproduce.
func TestCommitDirectorySymlinkTrailingSlashCommitsThroughIt(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "real/inner.txt", "inner\n")
	if err := os.Symlink("real", filepath.Join(dir, "linkdir")); err != nil {
		t.Fatalf("creating directory symlink: %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "commit through the link", "--", "linkdir/")
	if code != 0 {
		t.Fatalf("committing through a directory symlink failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	if _, ok := testutil.Show(t, dir, "HEAD", "real/inner.txt"); !ok {
		t.Errorf("real/inner.txt missing from HEAD; tree:\n%s", lsTreeHEAD(t, dir))
	}
	if mode := treeEntryMode(t, dir, "linkdir"); mode != "" {
		t.Errorf("the link object itself must not be committed by the trailing-slash form, got mode %q", mode)
	}
	if _, ok := testutil.Show(t, dir, "HEAD", "linkdir/inner.txt"); ok {
		t.Error("a path under the link name was committed; expansion must produce the target directory's own paths")
	}
}

// TestCommitSymlink_MixedWithRegularFile checks a commit that mixes one new
// regular file with one new symlink: both must be in the commit, and the
// reported file count must match what was actually committed.
//
// Regression: the symlink resolves away to seed.txt (already committed
// unchanged), so only the regular file is staged -- yet commit.go:113 prints
// len(files)+len(result.AutoStagedDeletions), i.e. the INPUT spec count, so
// the command reports "2 file(s) committed" for a commit containing 1. The
// caller gets no signal that anything was dropped.
func TestCommitSymlink_MixedWithRegularFile(t *testing.T) {
	dir := newRepo(t)

	// seed.txt is created and committed by newRepo, so a link to it collapses
	// to an already-committed, unchanged path.
	testutil.WriteFile(t, dir, "regular.txt", "regular content\n")
	if err := os.Symlink("seed.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "add regular and link", "--", "regular.txt", "link")
	if code != 0 {
		t.Fatalf("mixed commit failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// Errorf, not Fatalf: the tree assertion and the count assertion are the
	// two halves of the same defect, and seeing both in one run is the point.
	if mode := treeEntryMode(t, dir, "regular.txt"); mode != "100644" {
		t.Errorf("expected HEAD entry \"regular.txt\" with mode 100644, got mode %q; tree:\n%s",
			mode, lsTreeHEAD(t, dir))
	}
	if mode := treeEntryMode(t, dir, "link"); mode != "120000" {
		t.Errorf("expected HEAD entry \"link\" with mode 120000, got mode %q; tree:\n%s",
			mode, lsTreeHEAD(t, dir))
	} else if target := catFileBlob(t, dir, "link"); target != "seed.txt" {
		t.Errorf("expected symlink blob to hold the target path %q, got %q", "seed.txt", target)
	}

	// Both entries must have been introduced by THIS commit, not merely be
	// present from an earlier one.
	introduced := testutil.GitRaw(t, dir, "diff-tree", "--no-commit-id", "-r", "--name-only", "HEAD")
	for _, want := range []string{"regular.txt", "link"} {
		if !strings.Contains(introduced, want) {
			t.Errorf("expected %q to be changed by HEAD, got diff-tree:\n%s", want, introduced)
		}
	}

	// The reported count must equal what the commit actually contains. Today
	// it is derived from the input spec list, so it says 2 regardless.
	changed := len(strings.Fields(introduced))
	if !strings.Contains(stdout, "2 file(s) committed") {
		t.Errorf("expected stdout to report \"2 file(s) committed\", got:\n%s", stdout)
	}
	if changed != 2 {
		t.Errorf("reported count and reality disagree: HEAD changed %d path(s) (%v) but safegit printed:\n%s",
			changed, strings.Fields(introduced), stdout)
	}
}
