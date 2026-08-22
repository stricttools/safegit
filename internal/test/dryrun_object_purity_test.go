package test

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// A `--dry-run` commit promises a preview: docs/commands-guide.md states it
// shows what would happen "without writing any changes to disk".
//
// The commit pipeline once kept only half of that promise. It stopped before
// the ref moved, but every object the commit was made of had already been
// written for real: staging ran `git add` against a temp index (blob objects),
// `git write-tree` wrote the tree, `git commit-tree` wrote the commit, and only
// then did the dry-run branch return. No GIT_OBJECT_DIRECTORY was ever set, so
// those writes went straight into the repository's own object store and stayed
// there as unreferenced loose objects. The amend and reword paths had the same
// seam.
//
// They no longer do. Every commit-family operation opens an object quarantine
// before its first object-writing call (commit.beginPreview, which points
// GIT_OBJECT_DIRECTORY at a throwaway preview area and lists the repository's
// own store as an alternate), so a preview still computes the exact tree and
// commit SHAs a real run would while writing every object it makes into a
// directory that is deleted with the preview. gitexec.Command backs that up:
// an invocation its classification table says can write objects is REFUSED on a
// previewing context that carries no quarantine.
//
// These tests assert the promise as written: the object store is byte-identical
// before and after a preview.

// dryPurityObjectSnapshot returns the sorted list of every path under
// .git/objects, relative to that directory. Directories are included too, so a
// preview that creates an empty fanout directory is visible as well.
func dryPurityObjectSnapshot(t *testing.T, repoDir string) []string {
	t.Helper()
	return dryPurityObjectSnapshotAt(t, filepath.Join(repoDir, ".git", "objects"))
}

// dryPurityObjectSnapshotAt is the same listing for an object store named
// directly, which is how a submodule's store (under the parent's
// .git/modules/<name>/objects) is reached.
func dryPurityObjectSnapshotAt(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			rel += "/"
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	sort.Strings(paths)
	return paths
}

// dryPurityDiff returns the entries present in after but not in before.
func dryPurityDiff(before, after []string) []string {
	seen := make(map[string]bool, len(before))
	for _, p := range before {
		seen[p] = true
	}
	var added []string
	for _, p := range after {
		if !seen[p] {
			added = append(added, p)
		}
	}
	return added
}

// dryPurityDescribe renders each added object path with the type and, for small
// blobs, the content git stored -- so a failure names exactly what the preview
// leaked into the repository.
func dryPurityDescribe(t *testing.T, repoDir string, added []string) string {
	t.Helper()
	var b strings.Builder
	for _, rel := range added {
		b.WriteString("\n  .git/objects/" + rel)
		if strings.HasSuffix(rel, "/") {
			continue
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 2 || len(parts[0]) != 2 {
			continue
		}
		sha := parts[0] + parts[1]
		typ := strings.TrimSpace(testutil.Git(t, repoDir, "cat-file", "-t", sha))
		b.WriteString("  (" + typ + " " + sha + ")")
	}
	return b.String()
}

// dryPurityAssertUntouched runs fn and fails if it changed .git/objects.
func dryPurityAssertUntouched(t *testing.T, repoDir, what string, fn func()) {
	t.Helper()
	before := dryPurityObjectSnapshot(t, repoDir)
	fn()
	after := dryPurityObjectSnapshot(t, repoDir)
	if added := dryPurityDiff(before, after); len(added) > 0 {
		t.Errorf("%s wrote %d new entries into the object store, but a preview must write nothing to disk:%s",
			what, len(added), dryPurityDescribe(t, repoDir, added))
	}
	if removed := dryPurityDiff(after, before); len(removed) > 0 {
		t.Errorf("%s removed %d entries from the object store: %v", what, len(removed), removed)
	}
}

// TestCommitDryRunLeavesObjectStoreUntouched: `safegit commit --dry-run` of a
// new file writes a blob, a tree and a commit object into .git/objects.
func TestCommitDryRunLeavesObjectStoreUntouched(t *testing.T) {
	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "newfile.txt"), []byte("preview content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	dryPurityAssertUntouched(t, dir, "commit --dry-run", func() {
		stdout, stderr, code := runSafegit(t, dir, "--dry-run", "commit", "-m", "preview", "--", "newfile.txt")
		if code != 0 {
			t.Fatalf("commit --dry-run failed (%d): stdout=%s stderr=%s", code, stdout, stderr)
		}
	})
}

// TestCommitDryRunObjectPurityAcrossPaths covers the other two entry points
// that share the seam: an amend that stages files (blob + tree + commit) and a
// reword (commit object only, since the tree is reused).
func TestCommitDryRunObjectPurityAcrossPaths(t *testing.T) {
	cases := []struct {
		name       string
		writeFiles bool
		args       []string
	}{
		{
			name:       "amend with files",
			writeFiles: true,
			args:       []string{"--dry-run", "commit", "--amend", "-m", "preview amend", "--", "newfile.txt"},
		},
		{
			name:       "reword",
			writeFiles: false,
			args:       []string{"--dry-run", "commit", "--amend", "-m", "preview reword"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			if tc.writeFiles {
				if err := os.WriteFile(filepath.Join(dir, "newfile.txt"), []byte("preview content\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}

			dryPurityAssertUntouched(t, dir, "safegit "+strings.Join(tc.args, " "), func() {
				stdout, stderr, code := runSafegit(t, dir, tc.args...)
				if code != 0 {
					t.Fatalf("%s failed (%d): stdout=%s stderr=%s", tc.name, code, stdout, stderr)
				}
			})
		})
	}
}

// TestCommitDryRunHunkPathLeavesObjectStoreUntouched covers the hunk-staging
// branch, which reaches the object store through `git apply --cached` and
// `git hash-object` rather than `git add`.
func TestCommitDryRunHunkPathLeavesObjectStoreUntouched(t *testing.T) {
	dir := newRepo(t)
	// A tracked multi-line file, so a hunk selection is meaningful.
	if err := os.WriteFile(filepath.Join(dir, "hunked.txt"),
		[]byte("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed hunked", "--", "hunked.txt"); code != 0 {
		t.Fatalf("seeding hunked.txt failed (%d): %s", code, stderr)
	}
	if err := os.WriteFile(filepath.Join(dir, "hunked.txt"),
		[]byte("ONE\ntwo\nthree\nfour\nfive\nsix\nseven\nEIGHT\n"), 0644); err != nil {
		t.Fatal(err)
	}

	dryPurityAssertUntouched(t, dir, "commit --dry-run with a hunk selection", func() {
		stdout, stderr, code := runSafegit(t, dir,
			"--dry-run", "commit", "-m", "preview hunks", "--hunks", "hunked.txt:1")
		if code != 0 {
			t.Fatalf("hunk dry-run commit failed (%d): stdout=%s stderr=%s", code, stdout, stderr)
		}
	})
}

// TestSubmoduleScrubDryRunLeavesBothObjectStoresUntouched covers the paths the
// repository-root pin does not reach: the submodule scan and cat-file calls,
// which target a git directory given as an argument. A preview of a submodule
// scrub reads BOTH object stores -- the parent's and the submodule's -- and it
// must leave both exactly as it found them.
//
// The two stores are asserted separately on purpose: a quarantine installed for
// the parent while the submodule's store stayed writable would look clean from
// the parent's side alone.
func TestSubmoduleScrubDryRunLeavesBothObjectStoresUntouched(t *testing.T) {
	parentDir, _, subDir := newRepoWithSubmoduleSecret(t, "SUBPREVIEW_SECRET", "secret.txt")

	subSHAs := revListReverse(t, subDir)
	firstSubCommit := subSHAs[0]

	parentObjects := filepath.Join(parentDir, ".git", "objects")
	subObjects := filepath.Join(submoduleGitDir(t, subDir), "objects")

	parentBefore := dryPurityObjectSnapshotAt(t, parentObjects)
	subBefore := dryPurityObjectSnapshotAt(t, subObjects)

	stdout, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv,
		"--dry-run", "scrub", "file", "mysub/secret.txt",
		"--from", firstSubCommit, "--reason", "preview purity")
	if code != 0 {
		t.Fatalf("submodule scrub --dry-run failed (%d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	for _, store := range []struct {
		name   string
		root   string
		before []string
	}{
		{"the parent's", parentObjects, parentBefore},
		{"the submodule's", subObjects, subBefore},
	} {
		after := dryPurityObjectSnapshotAt(t, store.root)
		if added := dryPurityDiff(store.before, after); len(added) > 0 {
			t.Errorf("the submodule scrub preview wrote %d new entries into %s object store (%s): %v",
				len(added), store.name, store.root, added)
		}
		if removed := dryPurityDiff(after, store.before); len(removed) > 0 {
			t.Errorf("the submodule scrub preview removed %d entries from %s object store (%s): %v",
				len(removed), store.name, store.root, removed)
		}
	}
}
