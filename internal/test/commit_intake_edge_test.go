package test

// Argument-intake edge cases for `safegit commit`.
//
// Two mechanisms are under test here.
//
// A. Hunk-spec disambiguation (main.go parseFileSpecs / fileExists). An
// argument like "file.txt:1,3" is split into a path plus a hunk selection only
// when the WHOLE argument does not name an existing file, probed with os.Stat
// against the process working directory. The grammar of the command line is
// therefore a function of disk state: the same argv parses differently
// depending on what happens to be on disk, and on which directory the caller
// stands in.
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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// --- helpers (all prefixed intakeEdge to avoid collisions in package test) ---

// intakeEdgeShow returns the blob content of path at rev, and whether it exists.
func intakeEdgeShow(t *testing.T, dir, rev, path string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", "show", rev+":"+path)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// intakeEdgeTip returns the SHA a ref points at ("" when the ref is missing).
func intakeEdgeTip(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

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

// --- A. hunk-spec disambiguation ---

// TestIntakeEdgeHunkSpecFromSubdir pins the plain subdirectory case: a tracked
// file in a subdirectory has two hunks of modifications and the caller,
// standing in that subdirectory, selects hunk 1 with a cwd-relative path.
//
// GREEN: the probe and the pipeline's path resolution both use the process
// working directory, so they agree, and `git apply --cached` reaches the right
// blob from the subdirectory.
func TestIntakeEdgeHunkSpecFromSubdir(t *testing.T) {
	dir := newRepo(t)
	sub := filepath.Join(dir, "sub")
	path := filepath.Join(sub, "edited.txt")

	testutil.WriteFileAt(t, path, intakeEdgeNumbered(20, nil))
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed edited", "--", "sub/edited.txt"); code != 0 {
		t.Fatalf("seed commit failed (%d): %s", code, stderr)
	}
	intakeEdgeTwoHunks(t, path)

	_, stderr, code := runSafegit(t, sub, "commit", "-m", "hunk 1 only", "--", "edited.txt:1")
	if code != 0 {
		t.Fatalf("hunk staging from subdirectory failed (%d): %s", code, stderr)
	}

	got, ok := intakeEdgeShow(t, dir, "HEAD", "sub/edited.txt")
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

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "hunk 1 only", "--", "sub/edited.txt:1")
	if code != 0 {
		t.Fatalf("hunk staging from repo root failed (%d): %s", code, stderr)
	}
	got, _ := intakeEdgeShow(t, dir, "HEAD", "sub/edited.txt")
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
	if _, ok := intakeEdgeShow(t, dir, "HEAD", name); ok {
		t.Errorf("%q still present in HEAD after the deletion commit", name)
	}
}

// TestIntakeEdgeColonNameNonNumericSuffixDeletion is the second discriminator:
// a colon in the name is harmless as long as the suffix does not LOOK like a
// hunk spec (isHunkSpec, main.go:891), because then the argument is never a
// candidate for splitting and the probe is not consulted.
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
	if _, ok := intakeEdgeShow(t, dir, "HEAD", name); ok {
		t.Errorf("%q still present in HEAD after the deletion commit", name)
	}
}

// TestIntakeEdgeColonNameDeletion: a tracked file whose literal name ends in a
// colon plus digits, deleted from disk. Committing that deletion is exactly
// the same argv that committed the file in the first place -- but the probe
// (main.go:870, fileExists at main.go:885) can no longer see the file, so the
// argument is now reparsed as path "sprint" plus hunk 1, and the commit is
// refused with an error naming a path the caller never typed.
//
// RED: asserts the desired behavior -- a tracked file can always be deleted by
// the name it was committed under.
func TestIntakeEdgeColonNameDeletion(t *testing.T) {
	dir := newRepo(t)
	name := "sprint:1"
	testutil.WriteFileAt(t, filepath.Join(dir, name), "planning\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "add colon file", "--", name); code != 0 {
		t.Fatalf("seed commit of %q failed (%d): %s", name, code, stderr)
	}
	if _, ok := intakeEdgeShow(t, dir, "HEAD", name); !ok {
		t.Fatalf("%q not committed by the seed step", name)
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "delete colon file", "--", name)
	if code != 0 {
		t.Errorf("committing the deletion of %q was refused (%d): %s", name, code, stderr)
	}
	if _, ok := intakeEdgeShow(t, dir, "HEAD", name); ok {
		t.Errorf("%q still present in HEAD after the deletion commit", name)
	}
}

// TestIntakeEdgeDanglingSymlinkNoColon documents a SEPARATE defect found while
// building the discriminator for the test below: a dangling symlink cannot be
// committed at all, whatever its name. It survives validation (os.Lstat sees
// the link, internal/commit/commit.go:396) and staging (`git add` records the
// link text happily), then move detection hashes it with
// `git hash-object -- <path>` (internal/commit/moves.go:93 ->
// internal/git/git.go:665), which opens the TARGET and fails. Move detection
// skips disk-absent paths (moves.go:68) but not paths whose target is absent.
//
// RED: asserts the desired behavior -- git itself commits dangling symlinks,
// and safegit already supports symlinks elsewhere.
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
	if _, ok := intakeEdgeShow(t, dir, "HEAD", name); !ok {
		t.Errorf("%q missing from HEAD", name)
	}
}

// TestIntakeEdgeColonNameBrokenSymlink isolates the probe: it calls os.Stat,
// which FOLLOWS symlinks (main.go:886), while the pipeline decides existence
// with os.Lstat (internal/commit/commit.go:396). A dangling symlink whose name
// ends in a colon plus digits is therefore invisible to the probe and visible
// to the pipeline -- two answers about one path inside one process.
//
// The assertion is deliberately about the classification, not about the commit
// succeeding: a dangling symlink also trips the unrelated move-detection
// defect pinned above, so requiring success here would conflate the two. What
// the probe defect produces uniquely is a refusal naming "link" -- a path the
// caller never typed.
//
// RED.
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

// TestIntakeEdgeSameArgvDifferentMeaningByCwd pins the consequence of a
// probe-based grammar: one argv, one repository state, two working
// directories, two entirely different commits.
//
//   - from sub/, "notes:1" finds nothing on disk and becomes "hunk 1 of
//     sub/notes";
//   - from the repo root, "notes:1" finds the literal file and becomes "the
//     whole file named notes:1".
//
// GREEN pin of today's behavior. It is not a bug report on either outcome
// taken alone -- each is defensible for its own directory -- but a record that
// the parse of an argument, not merely the file it resolves to, depends on
// where the caller stands. The intended fix is an explicit syntax rule, and
// this test is what such a change would have to update deliberately.
func TestIntakeEdgeSameArgvDifferentMeaningByCwd(t *testing.T) {
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

	if _, stderr, code := runSafegit(t, sub, "commit", "-m", "from sub", "--", "notes:1"); code != 0 {
		t.Fatalf("commit from sub/ failed (%d): %s", code, stderr)
	}
	if _, ok := intakeEdgeShow(t, dir, "HEAD", "notes:1"); ok {
		t.Errorf("running from sub/ committed the root file notes:1; expected it to be read as a hunk spec")
	}
	got, ok := intakeEdgeShow(t, dir, "HEAD", "sub/notes")
	if !ok {
		t.Fatal("sub/notes missing from HEAD")
	}
	if !strings.Contains(got, "FIRST-CHANGE") || strings.Contains(got, "SECOND-CHANGE") {
		t.Errorf("running from sub/ did not stage hunk 1 of sub/notes; content:\n%s", got)
	}

	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "from root", "--", "notes:1"); code != 0 {
		t.Fatalf("commit from repo root failed (%d): %s", code, stderr)
	}
	if _, ok := intakeEdgeShow(t, dir, "HEAD", "notes:1"); !ok {
		t.Errorf("running from the repo root did not commit the literal file notes:1")
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
	if _, ok := intakeEdgeShow(t, dir, "refs/heads/other", name); ok {
		t.Errorf("%s still present on other after the deletion commit", name)
	}
	// HEAD must be untouched: the deletion was committed to the other branch.
	if _, ok := intakeEdgeShow(t, dir, "HEAD", name); !ok {
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

	if _, ok := intakeEdgeShow(t, dir, "refs/heads/other", name); !ok {
		t.Fatalf("setup: %s missing from other", name)
	}
	if _, ok := intakeEdgeShow(t, dir, "HEAD", name); ok {
		t.Fatalf("setup: %s should not be in HEAD", name)
	}
	if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Fatalf("setup: %s should be absent from disk on main", name)
	}

	_, stderr, code := runSafegit(t, dir, "commit", "--branch", "other", "-m", "delete on other", "--", name)
	if code != 0 {
		t.Errorf("deleting a path tracked on the target branch was refused (%d): %s", code, stderr)
	}
	if _, ok := intakeEdgeShow(t, dir, "refs/heads/other", name); ok {
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
	if _, ok := intakeEdgeShow(t, dir, "refs/heads/other", name); ok {
		t.Fatalf("setup: %s should not exist on other", name)
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}

	before := intakeEdgeTip(t, dir, "refs/heads/other")
	_, stderr, code := runSafegit(t, dir, "commit", "--branch", "other", "-m", "delete on other", "--", name)

	if code == 0 {
		t.Errorf("deleting a path the target branch does not track succeeded (%d): %s", code, stderr)
	}
	if after := intakeEdgeTip(t, dir, "refs/heads/other"); after != before {
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
	if _, ok := intakeEdgeShow(t, dir, "refs/heads/other", name); ok {
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
	if _, ok := intakeEdgeShow(t, dir, "refs/heads/other", "fresh.txt"); !ok {
		t.Error("fresh.txt missing from other after cross-branch commit")
	}
}
