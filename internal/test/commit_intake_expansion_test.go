package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// Argument intake, as it behaves after the commit-pipeline rewrite.
//
// Two rules are under test.
//
// A. A named DIRECTORY expands to the union of what is on disk under it and
// what the commit's parent tree holds under it. The tree half is what makes a
// deletion committable by naming the directory it was in; the disk half is what
// makes an addition committable the same way. Expansion stops at a submodule
// boundary and passes over gitignored files without a word.
//
// B. A named path that contributes nothing to the commit is a hard error naming
// that path. Naming a path is a statement that it belongs in the commit; when
// it cannot be in it -- absent and untracked, an empty directory, a file whose
// content the commit would not change -- a refusal is the honest answer, and
// the silent omission it replaces was how a caller learned nothing about a
// mistyped path.

// intakeExpMkdir creates a directory inside the repo.
func intakeExpMkdir(t *testing.T, dir, rel string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, rel), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
}

// --- A. directory expansion ---

// TestCommitDirectoryExpandsToDiskAndTreeUnion is the core of the union rule:
// one commit naming one directory records the file that appeared in it and the
// file that vanished from it.
func TestCommitDirectoryExpandsToDiskAndTreeUnion(t *testing.T) {
	dir := newRepo(t)

	intakeExpMkdir(t, dir, "pkg")
	testutil.WriteFile(t, dir, "pkg/kept.txt", "kept\n")
	testutil.WriteFile(t, dir, "pkg/gone.txt", "gone\n")
	safegitCommit(t, dir, "seed pkg", "pkg/kept.txt", "pkg/gone.txt")

	if err := os.Remove(filepath.Join(dir, "pkg", "gone.txt")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, dir, "pkg/added.txt", "added\n")

	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "pkg churn", "--", "pkg"); code != 0 {
		t.Fatalf("committing a directory failed (code %d): %s", code, stderr)
	}

	paths := testutil.TreePaths(t, dir, "HEAD")
	if testutil.Contains(paths, "pkg/gone.txt") {
		t.Errorf("the deletion under the named directory was not recorded: %v", paths)
	}
	if !testutil.Contains(paths, "pkg/added.txt") {
		t.Errorf("the addition under the named directory was not recorded: %v", paths)
	}
	if !testutil.Contains(paths, "pkg/kept.txt") {
		t.Errorf("an untouched file under the named directory was dropped: %v", paths)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); status != "" {
		t.Errorf("expected a clean working tree, got: %s", status)
	}
}

// TestCommitDirectoryExpansionStopsAtSubmoduleBoundary: a submodule is another
// repository's working tree. Naming a directory above it must never sweep its
// files into this repository's commit, and must never move its pointer either
// -- a gitlink is staged only when the caller names it.
func TestCommitDirectoryExpansionStopsAtSubmoduleBoundary(t *testing.T) {
	parentDir, subOriginDir := newRepoWithSubmodule(t)

	// A second submodule, this time inside a directory, so the directory can
	// be named without naming the submodule.
	intakeExpMkdir(t, parentDir, "vendor")
	testutil.GitRaw(t, parentDir, "submodule", "add", "-q", subOriginDir, "vendor/lib")
	testutil.GitRaw(t, parentDir, "commit", "-q", "-m", "add vendor/lib")

	pointerBefore := lsTreeSHA(t, lsTreeEntry(t, parentDir, "vendor/lib"))

	// Move the submodule forward, so a pointer bump would be visible, and add
	// an ordinary file beside it.
	subDir := filepath.Join(parentDir, "vendor", "lib")
	testutil.GitRaw(t, subDir, "config", "user.email", "test@test.com")
	testutil.GitRaw(t, subDir, "config", "user.name", "Test")
	testutil.WriteFile(t, subDir, "inside.txt", "inside the submodule\n")
	testutil.GitRaw(t, subDir, "add", "inside.txt")
	testutil.GitRaw(t, subDir, "commit", "-q", "-m", "submodule moves")

	testutil.WriteFile(t, parentDir, "vendor/note.txt", "beside the submodule\n")

	if _, stderr, code := runSafegit(t, parentDir, "commit", "-m", "vendor note", "--", "vendor"); code != 0 {
		t.Fatalf("committing a directory holding a submodule failed (code %d): %s", code, stderr)
	}

	paths := testutil.TreePaths(t, parentDir, "HEAD")
	if !testutil.Contains(paths, "vendor/note.txt") {
		t.Errorf("the ordinary file beside the submodule was not committed: %v", paths)
	}
	for _, leaked := range []string{"vendor/lib/inside.txt", "vendor/lib/sub-file.txt"} {
		if testutil.Contains(paths, leaked) {
			t.Errorf("expansion descended into the submodule and committed %s: %v", leaked, paths)
		}
	}
	if after := lsTreeSHA(t, lsTreeEntry(t, parentDir, "vendor/lib")); after != pointerBefore {
		t.Errorf("expansion moved the submodule pointer nobody named: %s -> %s", pointerBefore, after)
	}
}

// TestCommitDirectoryExpansionSkipsIgnoredFiles: expansion produces names the
// caller never typed, so an ignored file under a named directory is passed over
// silently -- exactly as git's own directory handling does -- rather than
// refusing the whole commit.
func TestCommitDirectoryExpansionSkipsIgnoredFiles(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, ".gitignore", "build/\n*.log\n")
	safegitCommit(t, dir, "add gitignore", ".gitignore")

	intakeExpMkdir(t, dir, "app/build")
	testutil.WriteFile(t, dir, "app/main.txt", "source\n")
	testutil.WriteFile(t, dir, "app/debug.log", "noise\n")
	testutil.WriteFile(t, dir, "app/build/artifact.txt", "output\n")

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "add app", "--", "app")
	if code != 0 {
		t.Fatalf("committing a directory holding ignored files failed (code %d): %s", code, stderr)
	}
	if strings.Contains(stderr, "gitignored") || strings.Contains(stdout, "gitignored") {
		t.Errorf("a skipped ignored file was announced on the human stream:\nstdout: %s\nstderr: %s", stdout, stderr)
	}

	paths := testutil.TreePaths(t, dir, "HEAD")
	if !testutil.Contains(paths, "app/main.txt") {
		t.Errorf("the non-ignored file under the named directory was not committed: %v", paths)
	}
	for _, ignored := range []string{"app/debug.log", "app/build/artifact.txt"} {
		if testutil.Contains(paths, ignored) {
			t.Errorf("%s is gitignored and must not have been committed: %v", ignored, paths)
		}
	}
}

// TestCommitExplicitlyNamedIgnoredFileIsRefused is the other half of the same
// rule: a path the caller typed is judged as typed, so naming an ignored file
// is still a refusal.
func TestCommitExplicitlyNamedIgnoredFileIsRefused(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, ".gitignore", "*.log\n")
	safegitCommit(t, dir, "add gitignore", ".gitignore")
	testutil.WriteFile(t, dir, "debug.log", "noise\n")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "commit the log", "--", "debug.log")
	if code == 0 {
		t.Fatalf("naming a gitignored file succeeded; stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "gitignored") {
		t.Errorf("the refusal does not say the path is gitignored: %s", stderr)
	}
	if testutil.Contains(testutil.TreePaths(t, dir, "HEAD"), "debug.log") {
		t.Error("debug.log reached the tree despite the refusal")
	}
}

// --- B. a named path that contributes nothing ---

// TestCommitNamedMissingFileIsAnError: a path that is neither on disk nor in
// the tree the commit is built on cannot be anything -- not an addition, not a
// deletion.
func TestCommitNamedMissingFileIsAnError(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "real.txt", "real\n")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "typo", "--", "real.txt", "raelo.txt")
	if code != exitcode.PathMatchedNothing {
		t.Fatalf("naming a missing, untracked path exited %d, want %d: %s", code, exitcode.PathMatchedNothing, stderr)
	}
	if !strings.Contains(stderr, "raelo.txt") {
		t.Errorf("the refusal does not name the path it is about: %s", stderr)
	}
	if testutil.Contains(testutil.TreePaths(t, dir, "HEAD"), "real.txt") {
		t.Error("the commit went ahead despite the refusal")
	}
}

// TestCommitNamedEmptyDirectoryIsAnError: a directory that exists on disk but
// holds nothing, and has no paths in the parent tree either, contributes
// nothing. It used to reach git as a pathspec that matched no files, and the
// caller got a plumbing message about an absolute path instead.
func TestCommitNamedEmptyDirectoryIsAnError(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "real.txt", "real\n")
	intakeExpMkdir(t, dir, "empty")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "empty dir", "--", "real.txt", "empty")
	if code != exitcode.PathMatchedNothing {
		t.Fatalf("naming an empty directory exited %d, want %d: %s", code, exitcode.PathMatchedNothing, stderr)
	}
	if !strings.Contains(stderr, "empty") {
		t.Errorf("the refusal does not name the directory it is about: %s", stderr)
	}
	if strings.Contains(stderr, "did not match any files") || strings.Contains(stderr, "pathspec") {
		t.Errorf("the refusal surfaced as raw git plumbing: %s", stderr)
	}
}

// TestCommitNamedVanishedDirectoryIsAnError: the same answer for a directory
// that is gone from disk and was never in the tree.
func TestCommitNamedVanishedDirectoryIsAnError(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "real.txt", "real\n")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "vanished dir", "--", "real.txt", "no-such-dir/")
	if code != exitcode.PathMatchedNothing {
		t.Fatalf("naming a vanished directory exited %d, want %d: %s", code, exitcode.PathMatchedNothing, stderr)
	}
	if !strings.Contains(stderr, "no-such-dir") {
		t.Errorf("the refusal does not name the directory it is about: %s", stderr)
	}
}

// TestCommitNamedUnchangedFileIsAnError: the file exists and is tracked, but
// the commit would not change it. Committing "successfully" while quietly
// leaving it out told the caller nothing; the refusal names it.
func TestCommitNamedUnchangedFileIsAnError(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "settled.txt", "settled\n")
	testutil.WriteFile(t, dir, "moving.txt", "one\n")
	safegitCommit(t, dir, "seed", "settled.txt", "moving.txt")
	head := testutil.Rev(t, dir, "HEAD")

	testutil.WriteFile(t, dir, "moving.txt", "two\n")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "second", "--", "moving.txt", "settled.txt")
	if code != exitcode.PathMatchedNothing {
		t.Fatalf("naming an unchanged file exited %d, want %d: %s", code, exitcode.PathMatchedNothing, stderr)
	}
	if !strings.Contains(stderr, "settled.txt") {
		t.Errorf("the refusal does not name the unchanged path: %s", stderr)
	}
	if now := testutil.Rev(t, dir, "HEAD"); now != head {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", head, now)
	}
}

// TestAmendNamedUnchangedFileIsAnError is the amend twin: the tree the argument
// is judged against is the tip being replaced.
func TestAmendNamedUnchangedFileIsAnError(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "settled.txt", "settled\n")
	safegitCommit(t, dir, "seed", "settled.txt")
	testutil.WriteFile(t, dir, "tip.txt", "tip\n")
	tip := safegitCommit(t, dir, "tip", "tip.txt")

	_, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "tip again", "--", "settled.txt")
	if code != exitcode.PathMatchedNothing {
		t.Fatalf("amending with an unchanged file exited %d, want %d: %s", code, exitcode.PathMatchedNothing, stderr)
	}
	if !strings.Contains(stderr, "settled.txt") {
		t.Errorf("the refusal does not name the unchanged path: %s", stderr)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != tip {
		t.Errorf("the tip moved despite the refusal: %s -> %s", tip, got)
	}
}
