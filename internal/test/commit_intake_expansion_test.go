package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stricttools/safegit/internal/exitcode"
	"github.com/stricttools/safegit/internal/testutil"
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

// TestCommitIgnoredFileRefusalNamesTheRuleAndAFixThatWorks: the refusal of a
// named ignored file names the ignore file, the line, and the rule, and offers
// a narrower rule where one would work -- a negation line below a glob, anchored
// at the .gitignore's own directory -- and not where git could not honor it,
// because the file's directory is itself excluded. Each fix it names, performed
// as written, lets the commit through.
func TestCommitIgnoredFileRefusalNamesTheRuleAndAFixThatWorks(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ignore    map[string]string // .gitignore path -> content
		file      string
		message   string
		narrowed  map[string]string // the fix: .gitignore path -> new content
		noExample bool
	}{
		{
			name:     "a glob at the root",
			ignore:   map[string]string{".gitignore": "*.log\n"},
			file:     "debug.log",
			message:  "file debug.log is gitignored, through line 1 of .gitignore (`*.log`), and safegit never commits an ignored path: narrow that rule so it no longer matches debug.log -- for example, add the line `!/debug.log` to .gitignore below line 1 -- or leave debug.log out of this commit",
			narrowed: map[string]string{".gitignore": "*.log\n!/debug.log\n"},
		},
		{
			name:     "a glob in a nested .gitignore",
			ignore:   map[string]string{"sub/.gitignore": "# scratch\n*.tmp\n"},
			file:     "sub/a.tmp",
			message:  "file sub/a.tmp is gitignored, through line 2 of sub/.gitignore (`*.tmp`), and safegit never commits an ignored path: narrow that rule so it no longer matches sub/a.tmp -- for example, add the line `!/a.tmp` to sub/.gitignore below line 2 -- or leave sub/a.tmp out of this commit",
			narrowed: map[string]string{"sub/.gitignore": "# scratch\n*.tmp\n!/a.tmp\n"},
		},
		{
			name:      "a rule naming the file",
			ignore:    map[string]string{".gitignore": "/secret.env\n"},
			file:      "secret.env",
			message:   "file secret.env is gitignored, through line 1 of .gitignore (`/secret.env`), and safegit never commits an ignored path: narrow that rule so it no longer matches secret.env or leave secret.env out of this commit",
			narrowed:  map[string]string{".gitignore": ""},
			noExample: true,
		},
		{
			name:      "a glob excluding the directory",
			ignore:    map[string]string{".gitignore": "out*/\n"},
			file:      "output/x.txt",
			message:   "file output/x.txt is gitignored, through line 1 of .gitignore (`out*/`), and safegit never commits an ignored path: narrow that rule so it no longer matches output/x.txt or leave output/x.txt out of this commit",
			narrowed:  map[string]string{".gitignore": ""},
			noExample: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			for p, content := range tc.ignore {
				testutil.WriteFile(t, dir, p, content)
			}
			testutil.WriteFile(t, dir, tc.file, "content\n")
			before := testutil.Rev(t, dir, "HEAD")

			_, stderr, code := runSafegit(t, dir, "commit", "-m", "commit it", "--", tc.file)
			if code != exitcode.General {
				t.Fatalf("exit %d, want %d (General); stderr:\n%s", code, exitcode.General, stderr)
			}
			if !strings.Contains(stderr, tc.message) {
				t.Errorf("the refusal must say %q; stderr:\n%s", tc.message, stderr)
			}
			if tc.noExample && strings.Contains(stderr, "for example") {
				t.Errorf("no negation line can re-include %s, so none may be offered; stderr:\n%s", tc.file, stderr)
			}
			if after := testutil.Rev(t, dir, "HEAD"); after != before {
				t.Fatalf("HEAD moved despite the refusal: %s -> %s", before, after)
			}

			for p, content := range tc.narrowed {
				testutil.WriteFile(t, dir, p, content)
			}
			stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "commit it", "--", tc.file)
			if code != 0 {
				t.Fatalf("commit after the suggested fix failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
			if !testutil.Contains(testutil.TreePaths(t, dir, "HEAD"), tc.file) {
				t.Errorf("%s is not in HEAD after the suggested fix", tc.file)
			}
		})
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

// TestCommitNoMatchRefusalNamesEveryUnmatchedArgument: when several named
// arguments each contribute nothing, the refusal names all of them rather than
// stopping at the first. Reporting one at a time turns a single mistake into a
// sequence of retries, each of which discovers one more argument that was
// already wrong when the first was reported.
func TestCommitNoMatchRefusalNamesEveryUnmatchedArgument(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "moving.txt", "one\n")
	testutil.WriteFile(t, dir, "settled-a.txt", "a\n")
	testutil.WriteFile(t, dir, "settled-b.txt", "b\n")
	safegitCommit(t, dir, "seed", "moving.txt", "settled-a.txt", "settled-b.txt")
	head := testutil.Rev(t, dir, "HEAD")

	testutil.WriteFile(t, dir, "moving.txt", "two\n")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "second",
		"--", "moving.txt", "settled-a.txt", "settled-b.txt")
	if code != exitcode.PathMatchedNothing {
		t.Fatalf("naming two unchanged files exited %d, want %d: %s", code, exitcode.PathMatchedNothing, stderr)
	}
	for _, want := range []string{"settled-a.txt", "settled-b.txt"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not name %s; every argument that contributed nothing must be named: %s", want, stderr)
		}
	}
	if now := testutil.Rev(t, dir, "HEAD"); now != head {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", head, now)
	}
}

// TestAmendNoMatchRefusalNamesEveryUnmatchedArgument is the amend twin: the
// same aggregation, judged against the tip being replaced.
func TestAmendNoMatchRefusalNamesEveryUnmatchedArgument(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "settled-a.txt", "a\n")
	testutil.WriteFile(t, dir, "settled-b.txt", "b\n")
	safegitCommit(t, dir, "seed", "settled-a.txt", "settled-b.txt")
	testutil.WriteFile(t, dir, "tip.txt", "tip\n")
	tip := safegitCommit(t, dir, "tip", "tip.txt")

	_, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "tip again",
		"--", "settled-a.txt", "settled-b.txt")
	if code != exitcode.PathMatchedNothing {
		t.Fatalf("amending with two unchanged files exited %d, want %d: %s", code, exitcode.PathMatchedNothing, stderr)
	}
	for _, want := range []string{"settled-a.txt", "settled-b.txt"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the amend refusal does not name %s: %s", want, stderr)
		}
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != tip {
		t.Errorf("the tip moved despite the refusal: %s -> %s", tip, got)
	}
}
