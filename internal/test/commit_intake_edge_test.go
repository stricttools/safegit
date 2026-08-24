package test

// Argument-intake edge cases for `safegit commit`.
//
// Two mechanisms are under test here.
//
// A. Hunk selection and literal paths. A hunk selection arrives on --hunks and
// nowhere else, and a positional argument is the literal name of a file. The
// grammar of a command line is therefore a property of the command line: no
// filesystem probe decides how an argument parses, so the same argv means the
// same thing from every directory and whatever is or is not on disk.
//
// The tests in this section were written against the predecessor, where a
// positional argument was split into path plus hunks when its tail looked
// numeric AND os.Stat could not see the whole string as a file. They are kept,
// converted, because the properties they pin -- hunk staging works from a
// subdirectory and from the root, a colon in a filename is harmless, one argv
// has one meaning -- are the properties the new grammar has to keep.
//
// B. Tracked-deletion validation for cross-branch commits
// (internal/commit/commit.go resolveFiles -> git.IsTracked, which asks
// `git cat-file -e HEAD:<path>`). A commit with --branch <other> resolves its
// parent, and seeds its temporary index, from the TARGET branch's tip, but a
// disk-absent path is validated against HEAD. When HEAD and the target branch
// disagree about a path, the accept/refuse decision is made against a tree the
// commit will never touch. The amend path shares resolveFiles and inherits the
// same divergence.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// --- helpers specific to this investigation (the generic git and filesystem
// ones live in internal/testutil; these stay prefixed intakeEdge so they cannot
// collide with the other investigations in package test) ---

// intakeEdgeNumbered builds n lines "line N", with the lines named in
// replacements substituted, so a file can be given two well-separated hunks.
func intakeEdgeNumbered(n int, replacements map[int]string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if r, ok := replacements[i]; ok {
			b.WriteString(r + "\n")
			continue
		}
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

// intakeEdgeTwoHunks writes a 20-line file whose lines 2 and 18 are changed,
// which git renders as two separate hunks.
func intakeEdgeTwoHunks(t *testing.T, path string) {
	t.Helper()
	testutil.WriteFileAt(t, path, intakeEdgeNumbered(20, map[int]string{2: "FIRST-CHANGE", 18: "SECOND-CHANGE"}))
}

// --- A. hunk selection and literal paths ---

// TestIntakeEdgeHunkSpecFromSubdir pins the plain subdirectory case: a tracked
// file in a subdirectory has two hunks of modifications and the caller,
// standing in that subdirectory, selects hunk 1 with a cwd-relative path.
//
// The path inside a --hunks element is resolved exactly like a positional path
// -- against the caller's own directory -- so `git apply --cached` reaches the
// right blob from the subdirectory.
func TestIntakeEdgeHunkSpecFromSubdir(t *testing.T) {
	dir := newRepo(t)
	sub := filepath.Join(dir, "sub")
	path := filepath.Join(sub, "edited.txt")

	testutil.WriteFileAt(t, path, intakeEdgeNumbered(20, nil))
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed edited", "--", "sub/edited.txt"); code != 0 {
		t.Fatalf("seed commit failed (%d): %s", code, stderr)
	}
	intakeEdgeTwoHunks(t, path)

	_, stderr, code := runSafegit(t, sub, "commit", "-m", "hunk 1 only", "--hunks", "edited.txt:1")
	if code != 0 {
		t.Fatalf("hunk staging from subdirectory failed (%d): %s", code, stderr)
	}

	got, ok := testutil.Show(t, dir, "HEAD", "sub/edited.txt")
	if !ok {
		t.Fatal("sub/edited.txt missing from HEAD")
	}
	if !strings.Contains(got, "FIRST-CHANGE") {
		t.Errorf("hunk 1 not staged; HEAD content:\n%s", got)
	}
	if strings.Contains(got, "SECOND-CHANGE") {
		t.Errorf("hunk 2 leaked into the commit; HEAD content:\n%s", got)
	}
}

// TestIntakeEdgeHunkSpecFromRoot is the repo-root control for the test above.
func TestIntakeEdgeHunkSpecFromRoot(t *testing.T) {
	dir := newRepo(t)
	path := filepath.Join(dir, "sub", "edited.txt")

	testutil.WriteFileAt(t, path, intakeEdgeNumbered(20, nil))
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed edited", "--", "sub/edited.txt"); code != 0 {
		t.Fatalf("seed commit failed (%d): %s", code, stderr)
	}
	intakeEdgeTwoHunks(t, path)

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "hunk 1 only", "--hunks", "sub/edited.txt:1")
	if code != 0 {
		t.Fatalf("hunk staging from repo root failed (%d): %s", code, stderr)
	}
	got, _ := testutil.Show(t, dir, "HEAD", "sub/edited.txt")
	if !strings.Contains(got, "FIRST-CHANGE") || strings.Contains(got, "SECOND-CHANGE") {
		t.Errorf("wrong hunk selection from repo root; HEAD content:\n%s", got)
	}
}

// TestIntakeEdgePlainDeletionControl is the discriminator for the two tests
// below: a deletion of an ordinary filename commits fine, so any failure there
// comes from the colon in the name, not from deletion support.
func TestIntakeEdgePlainDeletionControl(t *testing.T) {
	dir := newRepo(t)
	name := "sprint.txt"
	testutil.WriteFileAt(t, filepath.Join(dir, name), "planning\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "add", "--", name); code != 0 {
		t.Fatalf("seed commit failed (%d): %s", code, stderr)
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "delete", "--", name); code != 0 {
		t.Fatalf("committing the deletion of %q failed (%d): %s", name, code, stderr)
	}
	if _, ok := testutil.Show(t, dir, "HEAD", name); ok {
		t.Errorf("%q still present in HEAD after the deletion commit", name)
	}
}

// TestIntakeEdgeColonNameNonNumericSuffixDeletion is the second discriminator:
// a colon whose suffix could not be mistaken for a hunk selection. Under the
// predecessor grammar that was the ONLY colon that was safe; now every colon is,
// and this test keeps the easy case pinned beside the hard one below.
func TestIntakeEdgeColonNameNonNumericSuffixDeletion(t *testing.T) {
	dir := newRepo(t)
	name := "sprint:final"
	testutil.WriteFileAt(t, filepath.Join(dir, name), "planning\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "add", "--", name); code != 0 {
		t.Fatalf("seed commit of %q failed (%d): %s", name, code, stderr)
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "delete", "--", name); code != 0 {
		t.Fatalf("committing the deletion of %q failed (%d): %s", name, code, stderr)
	}
	if _, ok := testutil.Show(t, dir, "HEAD", name); ok {
		t.Errorf("%q still present in HEAD after the deletion commit", name)
	}
}

// TestIntakeEdgeColonNameDeletion: a tracked file whose literal name ends in a
// colon plus digits, deleted from disk. Committing that deletion is exactly the
// same argv that committed the file in the first place, and it has to work for
// the same reason: a positional argument is the literal name of a file, so
// nothing about the argument changes when the file leaves the disk.
//
// Under the predecessor grammar it did change: the probe could no longer see
// the file, so the argument was reparsed as path "sprint" plus hunk 1 and the
// commit was refused naming a path the caller never typed.
func TestIntakeEdgeColonNameDeletion(t *testing.T) {
	dir := newRepo(t)
	name := "sprint:1"
	testutil.WriteFileAt(t, filepath.Join(dir, name), "planning\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "add colon file", "--", name); code != 0 {
		t.Fatalf("seed commit of %q failed (%d): %s", name, code, stderr)
	}
	if _, ok := testutil.Show(t, dir, "HEAD", name); !ok {
		t.Fatalf("%q not committed by the seed step", name)
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "delete colon file", "--", name)
	if code != 0 {
		t.Errorf("committing the deletion of %q was refused (%d): %s", name, code, stderr)
	}
	if _, ok := testutil.Show(t, dir, "HEAD", name); ok {
		t.Errorf("%q still present in HEAD after the deletion commit", name)
	}
}

// TestIntakeEdgeDanglingSymlinkNoColon pins that a dangling symlink commits
// like any other path: git itself commits them, and safegit supports symlinks
// everywhere else.
//
// It was a defect once, and the cause is worth keeping because it is the shape
// a future one would take. The link survived validation (os.Lstat sees the link
// itself) and staging (`git add` records the link text happily), and then MOVE
// DETECTION hashed it with `git hash-object -- <path>`, which opens the TARGET
// and fails. Detection skipped disk-absent paths but not paths whose target was
// absent. That detection is gone: safegit records moves either because a caller
// DECLARED one or because a commit's own raw delta witnesses it, and the delta
// is read from the object names git already reported (internal/commit's
// infer_moves.go). Nothing on the commit path hashes a working-tree file to ask
// what it holds, so nothing opens a symlink's target any more.
func TestIntakeEdgeDanglingSymlinkNoColon(t *testing.T) {
	dir := newRepo(t)
	name := "link1"
	if err := os.Symlink("missing-target.txt", filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add dangling symlink", "--", name)
	if code != 0 {
		t.Errorf("committing dangling symlink %q was refused (%d): %s", name, code, stderr)
	}
	if _, ok := testutil.Show(t, dir, "HEAD", name); !ok {
		t.Errorf("%q missing from HEAD", name)
	}
}

// TestIntakeEdgeColonNameBrokenSymlink isolated the probe, which called os.Stat
// and therefore FOLLOWED symlinks, while the pipeline decided existence with
// os.Lstat: a dangling symlink whose name ended in a colon plus digits was
// invisible to one and visible to the other -- two answers about one path
// inside one process. With no probe left there is one answer, and the argument
// is the link's own name.
//
// The assertion is about the classification: what the probe produced uniquely
// was a refusal naming "link", a path the caller never typed.
func TestIntakeEdgeColonNameBrokenSymlink(t *testing.T) {
	dir := newRepo(t)
	name := "link:1"
	if err := os.Symlink("missing-target.txt", filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}

	_, stderr, _ := runSafegit(t, dir, "commit", "-m", "add dangling symlink", "--", name)
	if strings.Contains(stderr, "file link does not exist") {
		t.Errorf("argument %q was split into a path plus a hunk spec because os.Stat cannot see a dangling symlink; error names a path the caller never typed: %s", name, stderr)
	}
}

// TestIntakeEdgeSameArgvHasOneMeaningFromEveryDirectory is the inversion of a
// test that used to pin the opposite. Under the probe-based grammar one argv in
// one repository state meant two entirely different commits depending on the
// caller's directory: from sub/, "notes:1" found nothing on disk and became
// "hunk 1 of sub/notes"; from the repo root it found the literal file and
// became "the whole file named notes:1". The parse of the argument, not merely
// the file it resolved to, depended on where the caller stood.
//
// That cannot happen now, and this is the pin for it. The same fixture is run
// from both directories in both spellings, and each spelling means the same
// thing in both places:
//
//   - "notes:1" is the literal name of a file, always. From the root it names
//     the file that is there; from sub/ it names sub/notes:1, which does not
//     exist, so the commit is refused -- ordinary relative-path resolution, the
//     same answer any positional path would give.
//   - "--hunks notes:1" is hunk 1 of the file "notes", always. From sub/ it
//     reaches sub/notes; from the root it names a file that is not there and is
//     refused.
//
// What differs between the two directories is which file a relative path
// resolves to. What does not differ, and is what this test exists to hold, is
// how the argument is read.
func TestIntakeEdgeSameArgvHasOneMeaningFromEveryDirectory(t *testing.T) {
	dir := newRepo(t)
	sub := filepath.Join(dir, "sub")

	// A literal file named "notes:1" at the repo root, and a tracked file
	// named "notes" inside sub/ with two hunks of pending modifications.
	testutil.WriteFileAt(t, filepath.Join(dir, "notes:1"), "literal colon file\n")
	testutil.WriteFileAt(t, filepath.Join(sub, "notes"), intakeEdgeNumbered(20, nil))
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed notes", "--", "sub/notes"); code != 0 {
		t.Fatalf("seed commit failed (%d): %s", code, stderr)
	}
	intakeEdgeTwoHunks(t, filepath.Join(sub, "notes"))

	// Positional, from sub/: the literal path sub/notes:1, which is not there.
	// The refusal must name what the caller typed and must not have staged any
	// hunk of sub/notes.
	_, stderr, code := runSafegit(t, sub, "commit", "-m", "positional from sub", "--", "notes:1")
	if code == 0 {
		t.Errorf("a positional naming a file that is not in the caller's directory was accepted; stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "notes:1") {
		t.Errorf("the refusal must name the literal argument, got: %s", stderr)
	}
	if got, ok := testutil.Show(t, dir, "HEAD", "sub/notes"); ok && strings.Contains(got, "FIRST-CHANGE") {
		t.Error("a positional argument was read as a hunk selection")
	}

	// Positional, from the repo root: the literal file that is there.
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "positional from root", "--", "notes:1"); code != 0 {
		t.Fatalf("committing the literal file notes:1 from the repo root failed (%d): %s", code, stderr)
	}
	if _, ok := testutil.Show(t, dir, "HEAD", "notes:1"); !ok {
		t.Error("the literal file notes:1 was not committed from the repo root")
	}

	// --hunks, from the repo root: the path "notes", which is not there. The
	// refusal has to be that one -- a path that is neither on disk nor tracked
	// -- and not, say, a parse failure of the element or a refusal of the
	// literal file "notes:1" that IS there. So the code, the argument named and
	// the reason are all asserted, and so is the absence of any staging of
	// sub/notes, which is the file a probe-based reading would have found.
	rootTip := testutil.Rev(t, dir, "HEAD")
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "hunks from root", "--hunks", "notes:1")
	if code != exitcode.PathMatchedNothing {
		t.Errorf("--hunks naming a file absent from the caller's directory exited %d, want %d (PathMatchedNothing); stderr: %s",
			code, exitcode.PathMatchedNothing, stderr)
	}
	if !strings.Contains(stderr, "notes") || !strings.Contains(stderr, "does not exist and is not tracked") {
		t.Errorf("the refusal must name the path the --hunks element addressed and say why; stderr: %s", stderr)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != rootTip {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", rootTip, after)
	}
	if got, ok := testutil.Show(t, dir, "HEAD", "sub/notes"); ok && strings.Contains(got, "FIRST-CHANGE") {
		t.Error("--hunks from the repo root reached sub/notes, so the element's path was resolved against something other than the caller's directory")
	}

	// --hunks, from sub/: hunk 1 of sub/notes, and only hunk 1.
	if _, stderr, code := runSafegit(t, sub, "commit", "-m", "hunks from sub", "--hunks", "notes:1"); code != 0 {
		t.Fatalf("selecting hunk 1 of sub/notes from sub/ failed (%d): %s", code, stderr)
	}
	got, ok := testutil.Show(t, dir, "HEAD", "sub/notes")
	if !ok {
		t.Fatal("sub/notes missing from HEAD")
	}
	if !strings.Contains(got, "FIRST-CHANGE") || strings.Contains(got, "SECOND-CHANGE") {
		t.Errorf("--hunks from sub/ did not stage exactly hunk 1 of sub/notes; content:\n%s", got)
	}
}

// --- B. --branch validates against HEAD, stages against the target branch ---

// intakeEdgeBranchWithFile creates branch `other` off main carrying an extra
// file that main does not have, and returns the checkout to main, so the file
// is absent from disk while being tracked on the target branch.
func intakeEdgeBranchWithFile(t *testing.T, dir, name, content string) {
	t.Helper()
	testutil.GitRaw(t, dir, "checkout", "-b", "other")
	testutil.WriteFileAt(t, filepath.Join(dir, name), content)
	testutil.GitRaw(t, dir, "add", name)
	testutil.GitRaw(t, dir, "commit", "-m", "add "+name+" on other")
	testutil.GitRaw(t, dir, "checkout", "main")
}

// TestIntakeEdgeCrossBranchDeleteSharedControl is the discriminator for the
// two tests below: when HEAD and the target branch agree that the path is
// tracked, a cross-branch deletion commits fine. Any failure below therefore
// comes from the HEAD-vs-target divergence, not from cross-branch deletion
// support.
func TestIntakeEdgeCrossBranchDeleteSharedControl(t *testing.T) {
	dir := newRepo(t)
	name := "shared.txt"
	testutil.WriteFileAt(t, filepath.Join(dir, name), "shared\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "add shared", "--", name); code != 0 {
		t.Fatalf("seed commit failed (%d): %s", code, stderr)
	}
	testutil.GitRaw(t, dir, "branch", "other")
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegit(t, dir, "commit", "--branch", "other", "-m", "delete on other", "--", name)
	if code != 0 {
		t.Fatalf("cross-branch deletion of a shared path failed (%d): %s", code, stderr)
	}
	if _, ok := testutil.Show(t, dir, "refs/heads/other", name); ok {
		t.Errorf("%s still present on other after the deletion commit", name)
	}
	// HEAD must be untouched: the deletion was committed to the other branch.
	if _, ok := testutil.Show(t, dir, "HEAD", name); !ok {
		t.Errorf("%s disappeared from HEAD; a --branch commit must not move HEAD's branch", name)
	}
}

// TestIntakeEdgeCrossBranchDeleteTrackedOnlyOnTarget: the path is tracked on
// the TARGET branch, is not in HEAD, and is absent from disk (HEAD's checkout
// never had it). Committing its deletion onto the target branch is a coherent
// request against the tree the commit will actually be built from -- the
// commit's parent is the target branch's tip (internal/commit/commit.go:190)
// and the temporary index is seeded from that tip (commit.go:210). Validation
// nonetheless asks HEAD (commit.go:402 -> internal/git/git.go:217-218,
// `git cat-file -e HEAD:<path>`) and refuses.
//
// RED: asserts the desired behavior -- the tree that decides is the parent the
// commit is built on.
func TestIntakeEdgeCrossBranchDeleteTrackedOnlyOnTarget(t *testing.T) {
	dir := newRepo(t)
	name := "only-on-other.txt"
	intakeEdgeBranchWithFile(t, dir, name, "other branch content\n")

	if _, ok := testutil.Show(t, dir, "refs/heads/other", name); !ok {
		t.Fatalf("setup: %s missing from other", name)
	}
	if _, ok := testutil.Show(t, dir, "HEAD", name); ok {
		t.Fatalf("setup: %s should not be in HEAD", name)
	}
	if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Fatalf("setup: %s should be absent from disk on main", name)
	}

	_, stderr, code := runSafegit(t, dir, "commit", "--branch", "other", "-m", "delete on other", "--", name)
	if code != 0 {
		t.Errorf("deleting a path tracked on the target branch was refused (%d): %s", code, stderr)
	}
	if _, ok := testutil.Show(t, dir, "refs/heads/other", name); ok {
		t.Errorf("%s still present on other after the deletion commit", name)
	}
}

// TestIntakeEdgeCrossBranchDeleteTrackedOnlyOnHead is the mirror image: the
// path is tracked on HEAD and absent from the target branch. HEAD-based
// validation (commit.go:402) accepts it, and the refusal is deferred to
// staging, where `git rm --cached` against an index seeded from the target
// branch's tip fails with a git plumbing message quoting an absolute path
// (internal/commit/commit.go:442 -> internal/git/git.go:201).
//
// The safety property is pinned GREEN (nothing is committed, the target branch
// does not move). The message is asserted RED: the request is refusable at
// validation time, and the caller should be told that the path is not tracked
// on the branch being committed to.
func TestIntakeEdgeCrossBranchDeleteTrackedOnlyOnHead(t *testing.T) {
	dir := newRepo(t)
	name := "only-on-main.txt"

	testutil.WriteFileAt(t, filepath.Join(dir, name), "main content\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "add "+name, "--", name); code != 0 {
		t.Fatalf("seed commit failed (%d): %s", code, stderr)
	}
	// `other` points at the commit before the file existed.
	testutil.GitRaw(t, dir, "branch", "other", "HEAD~1")
	if _, ok := testutil.Show(t, dir, "refs/heads/other", name); ok {
		t.Fatalf("setup: %s should not exist on other", name)
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}

	before := testutil.RevTry(t, dir, "refs/heads/other")
	_, stderr, code := runSafegit(t, dir, "commit", "--branch", "other", "-m", "delete on other", "--", name)

	if code == 0 {
		t.Errorf("deleting a path the target branch does not track succeeded (%d): %s", code, stderr)
	}
	if after := testutil.RevTry(t, dir, "refs/heads/other"); after != before {
		t.Errorf("target branch moved despite the failure: %s -> %s", before, after)
	}
	if strings.Contains(stderr, "git rm --cached") || strings.Contains(stderr, "did not match any files") {
		t.Errorf("refusal surfaced as raw git plumbing instead of a validation error naming the target branch: %s", stderr)
	}
	if !strings.Contains(stderr, "other") {
		t.Errorf("refusal does not name the target branch it was decided against: %s", stderr)
	}
}

// TestIntakeEdgeCrossBranchAmendDeleteTrackedOnlyOnTarget: the same divergence
// on the amend path, which shares resolveFiles (internal/commit/amend.go:81)
// while seeding its temporary index from the target branch's tip
// (amend.go:118, amend.go:150). Cross-branch amend is a headline safegit
// feature, so the HEAD-based validation is felt here too.
//
// RED: asserts the desired behavior.
func TestIntakeEdgeCrossBranchAmendDeleteTrackedOnlyOnTarget(t *testing.T) {
	dir := newRepo(t)
	name := "only-on-other.txt"
	intakeEdgeBranchWithFile(t, dir, name, "other branch content\n")

	_, stderr, code := runSafegit(t, dir, "commit", "--amend", "--branch", "other", "-m", "amended", "--", name)
	if code != 0 {
		t.Errorf("amending away a path tracked on the target branch was refused (%d): %s", code, stderr)
	}
	if _, ok := testutil.Show(t, dir, "refs/heads/other", name); ok {
		t.Errorf("%s still present on other after the amend", name)
	}
}

// TestIntakeEdgeCrossBranchAddControl pins that a cross-branch commit of a
// file that exists on disk works, so the tests above isolate the disk-absent
// tracked-deletion path.
func TestIntakeEdgeCrossBranchAddControl(t *testing.T) {
	dir := newRepo(t)
	testutil.GitRaw(t, dir, "branch", "other")
	testutil.WriteFileAt(t, filepath.Join(dir, "fresh.txt"), "fresh\n")

	_, stderr, code := runSafegit(t, dir, "commit", "--branch", "other", "-m", "add fresh on other", "--", "fresh.txt")
	if code != 0 {
		t.Fatalf("cross-branch add failed (%d): %s", code, stderr)
	}
	if _, ok := testutil.Show(t, dir, "refs/heads/other", "fresh.txt"); !ok {
		t.Error("fresh.txt missing from other after cross-branch commit")
	}
}

// C. --allow-empty and a named path that changes nothing
//
// The two rules meet here, and the per-path rule governs. --allow-empty answers
// one question -- may this commit have the SAME TREE as its parent -- and says
// nothing about the arguments. Naming a path is a separate statement, that the
// path belongs in the commit, and an argument that turns out to contribute
// nothing is a typo or a stale command line either way: the refusal names it
// (exit 11, PathMatchedNothing) whether or not --allow-empty is present.
//
// The ordering follows from that. The commit pipeline asks the per-path
// question BEFORE the empty-tree question, so a run carrying both conditions
// gets the per-path verdict, which is the one that names something actionable.
// An empty commit is still reachable exactly as it always was -- --allow-empty
// with NO named path -- and the last case below pins that it is.
func TestAllowEmptyDoesNotExcuseANamedPathThatChangesNothing(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "unchanged\n")
	safegitCommit(t, dir, "seed a", "a.txt")
	tip := testutil.Rev(t, dir, "HEAD")

	// The path is committed already with this exact content, so staging it
	// changes nothing.
	_, stderr, code := runSafegit(t, dir, "commit", "--allow-empty", "-m", "empty plus a stale path", "--", "a.txt")
	if code != exitcode.PathMatchedNothing {
		t.Fatalf("--allow-empty with a no-match named path exited %d, want %d (PathMatchedNothing): %s",
			code, exitcode.PathMatchedNothing, stderr)
	}
	if !strings.Contains(stderr, "a.txt") {
		t.Errorf("the refusal does not name the argument it was decided about: %s", stderr)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != tip {
		t.Errorf("the tip moved despite the refusal: %s -> %s", tip, got)
	}

	// A directory whose contents are all unchanged is the same verdict: the
	// argument covers everything underneath it and none of it contributed.
	testutil.WriteFile(t, dir, "sub/b.txt", "also unchanged\n")
	safegitCommit(t, dir, "seed sub", "sub")
	tip = testutil.Rev(t, dir, "HEAD")
	if _, stderr, code := runSafegit(t, dir, "commit", "--allow-empty", "-m", "empty plus a stale directory", "--", "sub"); code != exitcode.PathMatchedNothing {
		t.Fatalf("--allow-empty with a no-match directory exited %d, want %d: %s",
			code, exitcode.PathMatchedNothing, stderr)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != tip {
		t.Errorf("the tip moved despite the directory refusal: %s -> %s", tip, got)
	}

	// And the thing --allow-empty is actually for still works: no named path,
	// an empty commit on top of the tip.
	if _, stderr, code := runSafegit(t, dir, "commit", "--allow-empty", "-m", "a deliberately empty commit"); code != 0 {
		t.Fatalf("--allow-empty with no named path failed (code %d): %s", code, stderr)
	}
	head := testutil.Rev(t, dir, "HEAD")
	if head == tip {
		t.Fatal("--allow-empty with no named path created no commit")
	}
	if parents := testutil.Parents(t, dir, head); len(parents) != 1 || parents[0] != tip {
		t.Errorf("the empty commit's parents are %v, want [%s]", parents, tip)
	}
	if got, want := testutil.Rev(t, dir, "HEAD^{tree}"), testutil.Rev(t, dir, tip+"^{tree}"); got != want {
		t.Errorf("the empty commit's tree is %s, want the tip's own %s", got, want)
	}
}
