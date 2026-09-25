package test

// A tracked directory replaced on disk by a symlink.
//
// The tree holds `logs/.gitignore`; the working tree now holds `logs` as a
// symlink. The commit that records this deletes `logs/.gitignore` from the tree
// and adds `logs` as a 120000 entry holding the link text -- which is what plain
// git records for `git add logs/.gitignore logs`. Two things must hold on the
// way there. A path under the link is still a path IN the repository: whether
// `logs/.gitignore` is inside is decided on its spelling, never by resolving it
// through the link to wherever the link points. And the link is one object: it
// is never expanded into the files of the directory it points at, and nothing
// underneath it is ever read from disk, because on disk "underneath it" is the
// target's own content, which the repository never carried.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// trackedDirReplacedByLink commits logs/.gitignore, then replaces logs/ on disk
// with a symlink holding target. It returns the repository and the tip before
// the commit under test.
func trackedDirReplacedByLink(t *testing.T, target string) (dir, before string) {
	t.Helper()
	dir = newRepo(t)
	testutil.WriteFile(t, dir, "logs/.gitignore", "*\n!.gitignore\n")
	testutil.Git(t, dir, "add", "logs/.gitignore")
	testutil.Git(t, dir, "commit", "-m", "track logs/")

	if err := os.RemoveAll(filepath.Join(dir, "logs")); err != nil {
		t.Fatalf("removing logs/: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "logs")); err != nil {
		t.Fatalf("creating the logs symlink: %v", err)
	}
	return dir, testutil.Rev(t, dir, "HEAD")
}

// outsideLogTarget builds a directory outside the repository shaped like a
// machine's log directory: subdirectories and a file, and optionally a
// .gitignore of its own, which is what a read through the link would find at
// logs/.gitignore.
func outsideLogTarget(t *testing.T, withGitignore bool) string {
	t.Helper()
	outside := evalTempDir(t)
	testutil.WriteFileAt(t, filepath.Join(outside, "events", "a.log"), "event\n")
	testutil.WriteFileAt(t, filepath.Join(outside, "telemetry", "b.log"), "telemetry\n")
	testutil.WriteFileAt(t, filepath.Join(outside, "shutdown.log"), "shutdown\n")
	if withGitignore {
		testutil.WriteFileAt(t, filepath.Join(outside, ".gitignore"), "machine-local\n")
	}
	return outside
}

// assertLinkReplacedDirectory checks the one commit the operation must make:
// logs/.gitignore gone, logs a 120000 entry holding target, nothing from the
// link's target in the tree, and exactly one commit on top of before.
func assertLinkReplacedDirectory(t *testing.T, dir, before, target string) {
	t.Helper()
	if parents := testutil.Parents(t, dir, "HEAD"); len(parents) != 1 || parents[0] != before {
		t.Fatalf("expected exactly one new commit on top of %s, got parents %v", before, parents)
	}
	if mode := treeEntryMode(t, dir, "logs"); mode != "120000" {
		t.Errorf("expected HEAD entry \"logs\" with mode 120000, got %q; tree:\n%s", mode, lsTreeHEAD(t, dir))
	}
	if got := catFileBlob(t, dir, "logs"); got != target {
		t.Errorf("symlink blob = %q, want the link text %q", got, target)
	}
	for _, p := range testutil.TreePaths(t, dir, "HEAD") {
		if strings.HasPrefix(p, "logs/") {
			t.Errorf("HEAD still holds %s under the link; tree:\n%s", p, lsTreeHEAD(t, dir))
		}
	}
	info, err := os.Lstat(filepath.Join(dir, "logs"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("logs must still be a symlink on disk (err %v)", err)
	}
}

// TestCommitLinkReplacingTrackedDirectory covers both spellings of the commit
// -- naming the deleted path and the link, and naming the link alone -- over an
// absolute target outside the repository, with and without a .gitignore in the
// target that a read through the link would mistake for the tracked one.
func TestCommitLinkReplacingTrackedDirectory(t *testing.T) {
	for _, tc := range []struct {
		name          string
		args          []string
		withGitignore bool
	}{
		{"deleted path and link named", []string{"logs/.gitignore", "logs"}, false},
		{"link alone named", []string{"logs"}, false},
		{"deleted path and link named, target has a .gitignore", []string{"logs/.gitignore", "logs"}, true},
		{"link alone named, target has a .gitignore", []string{"logs"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := outsideLogTarget(t, tc.withGitignore)
			dir, before := trackedDirReplacedByLink(t, target)

			// The target is absolute, so without the election the commit is the
			// non-portable-target refusal -- and only that: the path under the
			// link is in the repository and the link is not expanded.
			args := append([]string{"commit", "-m", "logs becomes a link", "--"}, tc.args...)
			_, stderr, code := runSafegit(t, dir, args...)
			if code != exitcode.NonPortableTarget {
				t.Fatalf("without the election, exit %d, want %d (NonPortableTarget); stderr:\n%s",
					code, exitcode.NonPortableTarget, stderr)
			}
			if !strings.Contains(stderr, "logs -> "+target) {
				t.Errorf("the refusal must name the link and its target; stderr:\n%s", stderr)
			}

			args = append([]string{"commit", "--allow-non-portable-targets", "-m", "logs becomes a link", "--"}, tc.args...)
			stdout, stderr, code := runSafegit(t, dir, args...)
			if code != 0 {
				t.Fatalf("commit failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
			assertLinkReplacedDirectory(t, dir, before, target)
		})
	}
}

// TestCommitLinkReplacingTrackedDirectoryPointsInside: the link points at a
// directory inside the repository. The link is still committed as a link, and
// the directory it points at contributes nothing.
func TestCommitLinkReplacingTrackedDirectoryPointsInside(t *testing.T) {
	for _, args := range [][]string{{"logs/.gitignore", "logs"}, {"logs"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir := newRepo(t)
			testutil.WriteFile(t, dir, "logs/.gitignore", "*.log\n")
			testutil.WriteFile(t, dir, "real/kept.txt", "kept\n")
			testutil.Git(t, dir, "add", "logs/.gitignore", "real/kept.txt")
			testutil.Git(t, dir, "commit", "-m", "track logs/ and real/")
			testutil.WriteFile(t, dir, "real/untracked.txt", "not named\n")
			if err := os.RemoveAll(filepath.Join(dir, "logs")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("real", filepath.Join(dir, "logs")); err != nil {
				t.Fatal(err)
			}
			before := testutil.Rev(t, dir, "HEAD")

			stdout, stderr, code := runSafegit(t, dir, append([]string{"commit", "-m", "logs becomes a link", "--"}, args...)...)
			if code != 0 {
				t.Fatalf("commit failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
			assertLinkReplacedDirectory(t, dir, before, "real")
			if _, ok := testutil.Show(t, dir, "HEAD", "real/untracked.txt"); ok {
				t.Error("a file in the link's target was committed; naming the link must not reach through it")
			}
		})
	}
}

// TestCommitLinkReplacingTrackedDirectoryDangling: the link's target does not
// exist at all.
func TestCommitLinkReplacingTrackedDirectoryDangling(t *testing.T) {
	for _, args := range [][]string{{"logs/.gitignore", "logs"}, {"logs"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir, before := trackedDirReplacedByLink(t, "not-there")
			stdout, stderr, code := runSafegit(t, dir, append([]string{"commit", "-m", "logs becomes a link", "--"}, args...)...)
			if code != 0 {
				t.Fatalf("commit failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
			assertLinkReplacedDirectory(t, dir, before, "not-there")
		})
	}
}

// TestAmendLinkReplacingTrackedDirectory: --amend resolves its files through
// the same intake, so it records the same change.
func TestAmendLinkReplacingTrackedDirectory(t *testing.T) {
	dir, _ := trackedDirReplacedByLink(t, "not-there")
	parent := testutil.Rev(t, dir, "HEAD~1")
	stdout, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "logs is a link", "--", "logs/.gitignore", "logs")
	if code != 0 {
		t.Fatalf("amend failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	assertLinkReplacedDirectory(t, dir, parent, "not-there")
}

// TestCommitPathOutsideTheRepositoryIsStillRefused: judging containment on the
// spelling must not let a path that really is outside through.
func TestCommitPathOutsideTheRepositoryIsStillRefused(t *testing.T) {
	parent := evalTempDir(t)
	dir := filepath.Join(parent, "repo")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, dir, "init", "--initial-branch=main")
	testutil.Git(t, dir, "config", "user.email", "test@test.com")
	testutil.Git(t, dir, "config", "user.name", "Test")
	testutil.WriteFile(t, dir, "seed.txt", "seed\n")
	testutil.Git(t, dir, "add", "seed.txt")
	testutil.Git(t, dir, "commit", "-m", "initial")
	testutil.WriteFileAt(t, filepath.Join(parent, "elsewhere", "file"), "outside\n")
	before := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "reach outside", "--", "../elsewhere/file")
	if code == 0 {
		t.Fatal("a path outside the repository was committed")
	}
	if !strings.Contains(stderr, "file ../elsewhere/file is outside the repository") {
		t.Errorf("expected the outside-the-repository refusal; stderr:\n%s", stderr)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("HEAD moved: %s -> %s", before, after)
	}
}

// TestCommitLinkReplacingTrackedDirectoryFromALinkedSpellingOfTheRoot: the
// caller's working directory is a link to the repository root (the macOS
// /var -> /private/var shape), so the absolute path of an argument does not
// start with the root git reports. The root is still found by resolving the
// ancestor that IS the root, and everything below it is still read as spelled.
func TestCommitLinkReplacingTrackedDirectoryFromALinkedSpellingOfTheRoot(t *testing.T) {
	dir, before := trackedDirReplacedByLink(t, "not-there")
	alias := filepath.Join(evalTempDir(t), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runSafegitEnv(t, alias, []string{"PWD=" + alias},
		"commit", "-m", "logs becomes a link", "--", "logs/.gitignore", "logs")
	if code != 0 {
		t.Fatalf("commit from the linked spelling failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	assertLinkReplacedDirectory(t, dir, before, "not-there")
}

// mvThroughLinkFixture tracks logs/a.txt and real/kept.txt, then replaces logs/
// on disk with a symlink holding target. It returns the repository and HEAD.
func mvThroughLinkFixture(t *testing.T, target string) (dir, before string) {
	t.Helper()
	dir = newRepo(t)
	testutil.WriteFile(t, dir, "logs/a.txt", "tracked\n")
	testutil.WriteFile(t, dir, "real/kept.txt", "kept\n")
	testutil.Git(t, dir, "add", "logs/a.txt", "real/kept.txt")
	testutil.Git(t, dir, "commit", "-m", "track logs/ and real/")
	if err := os.RemoveAll(filepath.Join(dir, "logs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "logs")); err != nil {
		t.Fatal(err)
	}
	return dir, testutil.Rev(t, dir, "HEAD")
}

// assertMvBeyondLinkRefused checks the refusal of an mv pair naming a path
// beyond a symbolic link: exit 19, the path and the link both named, and
// nothing moved or committed.
func assertMvBeyondLinkRefused(t *testing.T, dir, before, pair, path string) {
	t.Helper()
	_, stderr, code := runSafegit(t, dir, "mv", "-m", "move it", pair)
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("mv %q exited %d, want %d (MoveNotBorneOut); stderr:\n%s", pair, code, exitcode.MoveNotBorneOut, stderr)
	}
	want := path + " is beyond a symbolic link: logs is a symlink"
	if !strings.Contains(stderr, want) {
		t.Errorf("the refusal must say %q; stderr:\n%s", want, stderr)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("HEAD moved: %s -> %s", before, after)
	}
}

// TestMvSourceBeyondALinkIsRefused: a tracked path whose directory has become a
// link is beyond a symbolic link, as git mv says, whether the link leads out of
// the repository or back into it. The file the filesystem shows under the link
// belongs to the link's target, and moving it would pull it out of there -- so
// it stays put even when its content matches the tracked blob.
func TestMvSourceBeyondALinkIsRefused(t *testing.T) {
	t.Run("link leaves the repository", func(t *testing.T) {
		outside := evalTempDir(t)
		testutil.WriteFileAt(t, filepath.Join(outside, "a.txt"), "tracked\n")
		dir, before := mvThroughLinkFixture(t, outside)
		assertMvBeyondLinkRefused(t, dir, before, "logs/a.txt -> b.txt", "logs/a.txt")
		if !testutil.FileExists(filepath.Join(outside, "a.txt")) {
			t.Error("the file in the link's target was moved away")
		}
		if testutil.FileExists(filepath.Join(dir, "b.txt")) {
			t.Error("b.txt appeared in the repository")
		}
	})
	t.Run("link points inside the repository", func(t *testing.T) {
		dir, before := mvThroughLinkFixture(t, "real")
		testutil.WriteFile(t, dir, "real/a.txt", "tracked\n")
		assertMvBeyondLinkRefused(t, dir, before, "logs/a.txt -> b.txt", "logs/a.txt")
		if !testutil.FileExists(filepath.Join(dir, "real", "a.txt")) {
			t.Error("the file in the link's target was moved away")
		}
	})
}

// TestMvDestinationBeyondALinkIsRefused: a destination under a symlinked
// directory is refused the same way, and nothing is written through the link.
func TestMvDestinationBeyondALinkIsRefused(t *testing.T) {
	dir, before := mvThroughLinkFixture(t, "real")
	assertMvBeyondLinkRefused(t, dir, before, "real/kept.txt -> logs/kept.txt", "logs/kept.txt")
	if !testutil.FileExists(filepath.Join(dir, "real", "kept.txt")) {
		t.Error("the source was moved")
	}
}

// TestMvPathOutsideTheRepositoryIsStillRefused: a path that really is outside
// keeps its own refusal.
func TestMvPathOutsideTheRepositoryIsStillRefused(t *testing.T) {
	dir := newRepo(t)
	_, stderr, code := runSafegit(t, dir, "mv", "-m", "move it", "seed.txt -> ../elsewhere/seed.txt")
	if code == 0 {
		t.Fatal("mv moved a file outside the repository")
	}
	if !strings.Contains(stderr, "file ../elsewhere/seed.txt is outside the repository") {
		t.Errorf("expected the outside-the-repository refusal; stderr:\n%s", stderr)
	}
	if !testutil.FileExists(filepath.Join(dir, "seed.txt")) {
		t.Error("seed.txt was moved")
	}
}

// TestMvDirectoryFormBeyondALinkIsRefused: the directory form of a pair is read
// as spelled too. `logs/` with `logs` a symlink names the link's target, and a
// path under `logs/` lies beyond the link, so each is refused as beyond a
// symbolic link -- never resolved through the link to the directory it points
// at and moved from there, and never called outside the repository when the
// link leads out of it.
func TestMvDirectoryFormBeyondALinkIsRefused(t *testing.T) {
	t.Run("the link itself as a source", func(t *testing.T) {
		dir, before := mvThroughLinkFixture(t, "real")
		assertMvBeyondLinkRefused(t, dir, before, "logs/ -> x/", "logs/")
		if !testutil.FileExists(filepath.Join(dir, "real", "kept.txt")) {
			t.Error("the link's target real/ was moved")
		}
		if testutil.FileExists(filepath.Join(dir, "x")) {
			t.Error("x/ appeared in the repository")
		}
	})
	t.Run("a directory under the link as a source", func(t *testing.T) {
		dir, _ := mvThroughLinkFixture(t, "real")
		testutil.WriteFile(t, dir, "real/sub/f.txt", "under real\n")
		testutil.Git(t, dir, "add", "real/sub/f.txt")
		testutil.Git(t, dir, "commit", "-m", "track real/sub")
		before := testutil.Rev(t, dir, "HEAD")
		assertMvBeyondLinkRefused(t, dir, before, "logs/sub/ -> x/", "logs/sub/")
		if !testutil.FileExists(filepath.Join(dir, "real", "sub", "f.txt")) {
			t.Error("real/sub, the directory under the link's target, was moved")
		}
	})
	t.Run("a link leaving the repository", func(t *testing.T) {
		outside := evalTempDir(t)
		testutil.WriteFileAt(t, filepath.Join(outside, "a.txt"), "tracked\n")
		dir, before := mvThroughLinkFixture(t, outside)
		assertMvBeyondLinkRefused(t, dir, before, "logs/ -> x/", "logs/")
		if !testutil.FileExists(filepath.Join(outside, "a.txt")) {
			t.Error("the file in the link's target was moved away")
		}
	})
	t.Run("a directory under the link as a destination", func(t *testing.T) {
		dir, before := mvThroughLinkFixture(t, "real")
		testutil.WriteFile(t, dir, "other/o.txt", "other\n")
		testutil.Git(t, dir, "add", "other/o.txt")
		testutil.Git(t, dir, "commit", "-m", "track other/")
		before = testutil.Rev(t, dir, "HEAD")
		assertMvBeyondLinkRefused(t, dir, before, "other/ -> logs/sub/", "logs/sub/")
		if !testutil.FileExists(filepath.Join(dir, "other", "o.txt")) {
			t.Error("other/ was moved")
		}
		if testutil.FileExists(filepath.Join(dir, "real", "sub")) {
			t.Error("the move wrote through the link into real/sub")
		}
	})
}
