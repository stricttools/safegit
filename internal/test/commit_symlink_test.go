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

// TestCommitSymlinkOutsideTargetIsCommittedWhenElected: a symlink whose target
// leaves the repository is REFUSED (see
// TestWave2CommitNonPortableSymlinkIsRefused) -- the object it would write is a
// reference to a place only this machine has. --allow-non-portable-targets is
// the election, and it restores the one-line notice the refusal replaced: the
// link text is what gets recorded, and it resolves to nothing in another
// checkout.
func TestCommitSymlinkOutsideTargetIsCommittedWhenElected(t *testing.T) {
	dir := newRepo(t)

	if err := os.Symlink("../elsewhere/secret.txt", filepath.Join(dir, "escapes")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "--allow-non-portable-targets",
		"-m", "add a link that leaves", "--", "escapes")
	if code != 0 {
		t.Fatalf("an elected outside-the-repository symlink was refused (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
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

// TestCommitNonPortableSymlinkRefusalNamesTheLiteralTarget: the refusal has to
// say the link's own TEXT, not a resolved absolute path, because the text is
// what would be committed and what the operator has to recognize. A relative
// target that never resolves anywhere is the sharpest case: there is nothing to
// resolve, and only the literal answer exists.
func TestCommitNonPortableSymlinkRefusalNamesTheLiteralTarget(t *testing.T) {
	dir := newRepo(t)

	const target = "../../nowhere/at/all.txt"
	if err := os.Symlink(target, filepath.Join(dir, "escapes")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	before := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add a link that leaves", "--", "escapes")
	if code != exitcode.NonPortableTarget {
		t.Errorf("a symlink leaving the repository exited %d, want %d (NonPortableTarget); stderr: %s",
			code, exitcode.NonPortableTarget, stderr)
	}
	if !strings.Contains(stderr, target) {
		t.Errorf("the refusal must name the literal target %q; stderr:\n%s", target, stderr)
	}
	if !strings.Contains(stderr, "--allow-non-portable-targets") {
		t.Errorf("the refusal must name the flag that elects recording it; stderr:\n%s", stderr)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", before, after)
	}
}

// TestCommitNonPortableSymlinkRefusalNamesEveryOffender: intake resolves the
// whole argument list before it judges, so one invocation naming several
// non-portable links is one refusal naming all of them -- not the first one,
// discovered again on the next attempt.
func TestCommitNonPortableSymlinkRefusalNamesEveryOffender(t *testing.T) {
	dir := newRepo(t)

	for _, link := range []struct{ name, target string }{
		{"one", "../elsewhere/first.txt"},
		{"two", "../elsewhere/second.txt"},
	} {
		if err := os.Symlink(link.target, filepath.Join(dir, link.name)); err != nil {
			t.Fatalf("creating symlink %s: %v", link.name, err)
		}
	}

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add two links that leave", "--", "one", "two")
	if code != exitcode.NonPortableTarget {
		t.Fatalf("two non-portable symlinks exited %d, want %d (NonPortableTarget); stderr: %s",
			code, exitcode.NonPortableTarget, stderr)
	}
	for _, want := range []string{"../elsewhere/first.txt", "../elsewhere/second.txt"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not name %q; stderr:\n%s", want, stderr)
		}
	}
}

// TestAmendNonPortableSymlinkIsRefusedAndElects: the refusal is made in intake,
// which the amend path shares, so --amend inherits both halves of the ruling
// from the one place both forms resolve their files.
func TestAmendNonPortableSymlinkIsRefusedAndElects(t *testing.T) {
	dir := newRepo(t)

	const target = "../elsewhere/secret.txt"
	if err := os.Symlink(target, filepath.Join(dir, "escapes")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	before := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "amend in the link", "--", "escapes")
	if code != exitcode.NonPortableTarget {
		t.Errorf("an --amend of a non-portable symlink exited %d, want %d (NonPortableTarget); stderr: %s",
			code, exitcode.NonPortableTarget, stderr)
	}
	if !strings.Contains(stderr, target) {
		t.Errorf("the refusal must name the target %q; stderr:\n%s", target, stderr)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", before, after)
	}

	_, stderr, code = runSafegit(t, dir, "commit", "--amend", "--allow-non-portable-targets",
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
	// The phrase is one every notice carries, whichever shape produced it, so
	// this stays a real assertion when a notice is reworded rather than going
	// silently vacuous against text nothing writes any more.
	if strings.Contains(stderr, "the commit records the link text") {
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

// TestCommitNonPortableSymlinkFoundByDirectoryExpansionIsRefused pins the OTHER
// route a symlink reaches the non-portable-target verdict by: not named on the
// command line at all, but swept up by a directory argument's expansion. The
// expansion collects every link it walks over and hands them to the same
// end-of-intake judgement, so a link nobody typed is refused exactly like one
// that was.
//
// Pinned because the refusal is easy to read as a property of NAMED arguments
// only, and a future intake change that stopped collecting the expansion's
// links would leave the whole directory-argument route unguarded with every
// named-argument test still green.
func TestCommitNonPortableSymlinkFoundByDirectoryExpansionIsRefused(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "sub/ordinary.txt", "ordinary\n")
	const target = "../../elsewhere/secret.txt"
	if err := os.Symlink(target, filepath.Join(dir, "sub", "escapes")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	before := testutil.Rev(t, dir, "HEAD")

	// The argument names the DIRECTORY. sub/escapes is reached only by the
	// expansion.
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "commit the directory", "--", "sub")
	if code != exitcode.NonPortableTarget {
		t.Errorf("a directory holding a non-portable symlink exited %d, want %d (NonPortableTarget); stderr: %s",
			code, exitcode.NonPortableTarget, stderr)
	}
	if !strings.Contains(stderr, target) {
		t.Errorf("the refusal must name the target %q; stderr:\n%s", target, stderr)
	}
	if !strings.Contains(stderr, "sub/escapes") {
		t.Errorf("the refusal must name the expanded path sub/escapes; stderr:\n%s", stderr)
	}

	// The refusal is made before anything is staged, so the ordinary file the
	// same argument would have committed is not committed either.
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", before, after)
	}
	if _, ok := testutil.Show(t, dir, "HEAD", "sub/ordinary.txt"); ok {
		t.Error("the refused commit staged the directory's other members")
	}
}

// TestCommitAbsoluteSymlinkTargetInsideTheRepositoryIsRefused pins the
// definition of the refused class: a target whose recorded TEXT will not
// resolve in another checkout. An ABSOLUTE target is in that class whether or
// not it resolves inside this repository, because the text names a location on
// this machine -- a checkout at any other path resolves it to nothing, or to a
// file the repository never carried.
//
// The remedy differs from the outside-the-repository one and is pinned with the
// refusal: an absolute in-repository target has a portable spelling, so the
// advice is to spell it relative to the link rather than to point it inside.
// The election is pinned too, on this shape as well as on the outside one.
func TestCommitAbsoluteSymlinkTargetInsideTheRepositoryIsRefused(t *testing.T) {
	dir := newRepo(t)

	// newRepo's directory is already symlink-resolved, so the absolute target
	// really is inside the repository by path comparison -- and is refused all
	// the same.
	target := filepath.Join(dir, "seed.txt")
	if err := os.Symlink(target, filepath.Join(dir, "abslink")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	before := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add absolute link", "--", "abslink")
	if code != exitcode.NonPortableTarget {
		t.Errorf("an absolute in-repository symlink exited %d, want %d (NonPortableTarget); stderr: %s",
			code, exitcode.NonPortableTarget, stderr)
	}
	if !strings.Contains(stderr, target) {
		t.Errorf("the refusal must name the literal target %q; stderr:\n%s", target, stderr)
	}
	if !strings.Contains(stderr, "Spell the target relative to the link") {
		t.Errorf("the refusal must offer the relative spelling as the remedy; stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "--allow-non-portable-targets") {
		t.Errorf("the refusal must name the flag that elects recording it; stderr:\n%s", stderr)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", before, after)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "--allow-non-portable-targets",
		"-m", "add absolute link", "--", "abslink")
	if code != 0 {
		t.Fatalf("an elected absolute symlink was refused (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if mode := treeEntryMode(t, dir, "abslink"); mode != "120000" {
		t.Errorf("expected HEAD entry %q with mode 120000, got mode %q; tree:\n%s", "abslink", mode, lsTreeHEAD(t, dir))
	}
	if got := catFileBlob(t, dir, "abslink"); got != target {
		t.Errorf("symlink blob = %q, want the absolute link text %q", got, target)
	}
	if !strings.Contains(stderr, "notice:") || !strings.Contains(stderr, "the commit records the link text") {
		t.Errorf("the election must say once what the recorded text will not resolve to; stderr:\n%s", stderr)
	}
	// The notice's absolute sentence has to be true of EVERY absolute target,
	// not only of one that happens to land inside this checkout -- the
	// outside-resolving sibling below asserts the same phrase.
	if !strings.Contains(stderr, absoluteNoticePhrase) {
		t.Errorf("the notice must say what is true of every absolute target (%q); stderr:\n%s", absoluteNoticePhrase, stderr)
	}
}

// absoluteNoticePhrase and the two remedy phrases below are the distinctive
// fragments of the absolute shape's operator text. They are named once because
// the same statement has to hold for an absolute target that resolves inside
// this checkout and for one that resolves nowhere near it: a sentence true of
// only one of the two would be a falsehood printed for the other.
const (
	absoluteNoticePhrase  = "an absolute path naming a location on this machine rather than a place in the repository"
	absoluteRemedyInside  = "Spell the target relative to the link"
	absoluteRemedyOutside = "when it points outside there is nothing portable to spell"
)

// TestCommitAbsoluteSymlinkTargetOutsideTheRepositoryIsRefused is the other
// half of the absolute shape, and the one whose operator text is easiest to get
// wrong: the target is absolute AND resolves outside the repository, so the
// advice that fits an absolute in-repository target -- spell it relative --
// would only produce the other refused shape.
//
// The refusal itself is the same one the in-repository sibling gets (the
// judgment is on the SPELLING alone and never resolves), so the exit and the
// grouping are pinned here as the second member of that class; the wording
// assertions are what this case adds.
func TestCommitAbsoluteSymlinkTargetOutsideTheRepositoryIsRefused(t *testing.T) {
	dir := newRepo(t)

	// A directory of its own, outside the repository entirely -- built here
	// rather than naming a system path like /etc/hostname, which would tie the
	// test to the machine running it.
	outside := evalTempDir(t)
	target := filepath.Join(outside, "machine.conf")
	testutil.WriteFileAt(t, target, "machine-local\n")
	if err := os.Symlink(target, filepath.Join(dir, "abslink")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	before := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add absolute link", "--", "abslink")
	if code != exitcode.NonPortableTarget {
		t.Errorf("an absolute symlink resolving outside the repository exited %d, want %d (NonPortableTarget); stderr: %s",
			code, exitcode.NonPortableTarget, stderr)
	}
	for _, want := range []string{"abslink", target} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal must name %q; stderr:\n%s", want, stderr)
		}
	}
	// It is judged as an ABSOLUTE target, not as one that leaves the
	// repository: the group header is the absolute one.
	if !strings.Contains(stderr, "absolute targets, which name a path on this machine rather than a place in the repository") {
		t.Errorf("the offender must sit under the absolute group; stderr:\n%s", stderr)
	}
	// The absolute group's remedy carries BOTH halves, because the group holds
	// both cases: the relative spelling for a target pointing inside, and the
	// statement that a target pointing outside has no such spelling.
	for _, want := range []string{absoluteRemedyInside, absoluteRemedyOutside} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the absolute remedy does not carry %q; stderr:\n%s", want, stderr)
		}
	}
	if !strings.Contains(stderr, "--allow-non-portable-targets") {
		t.Errorf("the refusal must name the flag that elects recording it; stderr:\n%s", stderr)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", before, after)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "--allow-non-portable-targets",
		"-m", "add absolute link", "--", "abslink")
	if code != 0 {
		t.Fatalf("an elected absolute symlink was refused (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if got := catFileBlob(t, dir, "abslink"); got != target {
		t.Errorf("symlink blob = %q, want the absolute link text %q", got, target)
	}
	if !strings.Contains(stderr, absoluteNoticePhrase) {
		t.Errorf("the notice must say what is true of every absolute target (%q); stderr:\n%s", absoluteNoticePhrase, stderr)
	}
}

// TestCommitDryRunNonPortableSymlinkIsRefused: a preview of a commit safegit
// would refuse must BE the refusal, not a rehearsal of a commit that could
// never happen. The precedent is mv's dirty-move preview
// (TestMvDryRunRefusesADirtyMove); this pins the same property for the
// non-portable-target judgment, which sits in the same intake and runs before
// anything is staged either way.
func TestCommitDryRunNonPortableSymlinkIsRefused(t *testing.T) {
	dir := newRepo(t)

	const target = "../elsewhere/secret.txt"
	if err := os.Symlink(target, filepath.Join(dir, "escapes")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	before := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "commit", "-m", "preview the link", "--", "escapes")
	if code != exitcode.NonPortableTarget {
		t.Errorf("a dry-run commit of a non-portable symlink exited %d, want %d (NonPortableTarget)\nstdout: %s\nstderr: %s",
			code, exitcode.NonPortableTarget, stdout, stderr)
	}
	if !strings.Contains(stderr, target) {
		t.Errorf("the preview's refusal does not name the target %q; stderr:\n%s", target, stderr)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", before, after)
	}
}

// TestCommitNonPortableSymlinkRefusalGroupsOffendersByShape: the two shapes of
// the refused class have different remedies, and a commit naming offenders of
// both still gets ONE refusal -- the offenders grouped by shape, each group
// followed by the advice that fits it. The collected refusal is what keeps the
// operator out of a fix-one-rerun-discover-the-next loop, and grouping is what
// keeps the advice from being wrong for half the list.
func TestCommitNonPortableSymlinkRefusalGroupsOffendersByShape(t *testing.T) {
	dir := newRepo(t)

	absTarget := filepath.Join(dir, "seed.txt")
	if err := os.Symlink(absTarget, filepath.Join(dir, "abslink")); err != nil {
		t.Fatalf("creating absolute symlink: %v", err)
	}
	const outTarget = "../elsewhere/secret.txt"
	if err := os.Symlink(outTarget, filepath.Join(dir, "escapes")); err != nil {
		t.Fatalf("creating the symlink that leaves the repository: %v", err)
	}
	before := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add both shapes", "--", "abslink", "escapes")
	if code != exitcode.NonPortableTarget {
		t.Fatalf("a mixed-shape commit exited %d, want %d (NonPortableTarget); stderr: %s",
			code, exitcode.NonPortableTarget, stderr)
	}
	for _, want := range []string{absTarget, outTarget, "abslink", "escapes"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not name %q; stderr:\n%s", want, stderr)
		}
	}
	for _, remedy := range []string{"Spell the target relative to the link", "Point the link inside the repository"} {
		if !strings.Contains(stderr, remedy) {
			t.Errorf("the refusal does not carry the remedy %q; stderr:\n%s", remedy, stderr)
		}
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", before, after)
	}
}

// TestCommitTraversingSymlinkTargetLandingInsideIsCommitted pins that for a
// RELATIVE target the verdict is about where it resolves, never about how it is
// spelled: one that climbs out of its own directory with `..` and comes back
// down inside the repository is an ordinary portable link. (Absolute targets
// are the other half of the rule, and they ARE judged on spelling alone --
// where they resolve here says nothing about where they resolve elsewhere.)
//
// Pinned because the cheap implementation of the check -- looking for a leading
// `..` in the link text -- would refuse this one, and every refusal test in the
// file would still pass.
func TestCommitTraversingSymlinkTargetLandingInsideIsCommitted(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "sub/holder.txt", "holder\n")
	// From sub/, `../seed.txt` climbs to the repository root and lands on a
	// tracked file: traversal that never leaves.
	const target = "../seed.txt"
	if err := os.Symlink(target, filepath.Join(dir, "sub", "climber")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "add climbing link", "--", "sub/climber")
	if code != 0 {
		t.Fatalf("a traversing but in-repository symlink was refused (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if mode := treeEntryMode(t, dir, "sub/climber"); mode != "120000" {
		t.Errorf("expected HEAD entry %q with mode 120000, got mode %q; tree:\n%s", "sub/climber", mode, lsTreeHEAD(t, dir))
	}
	if got := catFileBlob(t, dir, "sub/climber"); got != target {
		t.Errorf("symlink blob = %q, want the link text %q", got, target)
	}
	// As in the inside-target control: the phrase asserted absent is one every
	// notice carries, so a reworded notice fails this test instead of passing
	// it vacuously.
	if strings.Contains(stderr, "the commit records the link text") {
		t.Errorf("a target that resolves inside the repository must produce no notice, got:\n%s", stderr)
	}
}
