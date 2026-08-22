package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// The scrub engine rewrites trees through git.LsTree (internal/git/git.go:655),
// which runs `git ls-tree` without --full-tree and without pinning the
// subprocess working directory. git resolves a tree-ish listing relative to the
// current directory prefix, so from a subdirectory `git ls-tree <root-tree>`
// enumerates that SUBDIRECTORY's entries and `git ls-tree <subtree>` enumerates
// nothing at all.
//
// Every scrub tree rewrite reads through that call: replaceInTree
// (tree_ops.go:24), lookupBlobAtPath (tree_ops.go:87), replaceInTreeByBlobMap
// (tree_ops.go:122), remap.go:114, scrub_match.go:1025 and scrub_verify.go:129
// and :135. Both replaceInTree (tree_ops.go:38-41) and replaceInTreeByBlobMap
// (tree_ops.go:156-159) read an empty/unmatched entry list as "path absent,
// tree unchanged", so the mis-rooted listing degrades into either a silent
// no-op or -- worse -- a tree rebuilt from the subdirectory's entries, which
// promotes the subdirectory to the repository root and deletes everything
// outside it.
//
// The tests below assert the DESIRED contract for every scrub subcommand run
// from a subdirectory: it either does the right thing (secret gone from all
// history, every unrelated path preserved at its original repo-relative path)
// or it refuses with a nonzero exit and changes nothing. Silently reporting
// success while the secret survives, and silently destroying unrelated files,
// are both failures.

const scrubSubdirSecret = "AKIA_SECRET_VALUE_123"

// scrubSubdirCommit stages the given repo-relative paths with raw git and
// commits them. Raw git is fine here: these are throwaway repos created by
// newRepo, not the safegit working tree.
func scrubSubdirCommit(t *testing.T, root, msg string, paths ...string) {
	t.Helper()
	testutil.Git(t, root, append([]string{"add", "--"}, paths...)...)
	testutil.Git(t, root, "commit", "-m", msg)
}

// scrubSubdirRepoRootSecret builds a repo whose secret lives at the ROOT level
// (secret.txt) and which also has a subdirectory (sub/other.txt) to run scrub
// from. Returns the repo root and the subdirectory path.
func scrubSubdirRepoRootSecret(t *testing.T) (root, sub string) {
	t.Helper()
	root = newRepo(t)
	testutil.WriteFile(t, root, "secret.txt", scrubSubdirSecret+"\n")
	testutil.WriteFile(t, root, "sub/other.txt", "hello\n")
	scrubSubdirCommit(t, root, "add secret at root", "secret.txt", "sub/other.txt")
	testutil.WriteFile(t, root, "sub/other.txt", "hello\nmore\n")
	scrubSubdirCommit(t, root, "second", "sub/other.txt")
	return root, filepath.Join(root, "sub")
}

// scrubSubdirRepoNestedSecret builds a repo whose secret lives INSIDE the
// subdirectory scrub is run from (sub/secret.txt), alongside a sibling in the
// same subdirectory (sub/other.txt) and a file at the repository root
// (rootfile.txt) that must survive any rewrite.
func scrubSubdirRepoNestedSecret(t *testing.T) (root, sub string) {
	t.Helper()
	root = newRepo(t)
	testutil.WriteFile(t, root, "sub/secret.txt", scrubSubdirSecret+"\n")
	testutil.WriteFile(t, root, "sub/other.txt", "hello\n")
	testutil.WriteFile(t, root, "rootfile.txt", "root file\n")
	scrubSubdirCommit(t, root, "add secret under sub/", "sub/secret.txt", "sub/other.txt", "rootfile.txt")
	return root, filepath.Join(root, "sub")
}

// scrubSubdirFirstCommit returns the root commit SHA.
func scrubSubdirFirstCommit(t *testing.T, root string) string {
	t.Helper()
	out := testutil.Git(t, root, "rev-list", "--max-parents=0", "HEAD")
	return strings.Fields(out)[0]
}

// scrubSubdirSecretInHistory reports whether the secret string still appears in
// any blob reachable from any ref, checked from the repository ROOT so the
// answer cannot itself be distorted by a cwd prefix.
func scrubSubdirSecretInHistory(t *testing.T, root string) bool {
	t.Helper()
	revs := strings.Fields(testutil.Git(t, root, "rev-list", "--all"))
	if len(revs) == 0 {
		return false
	}
	args := append([]string{"grep", "-I", "--fixed-strings", "--quiet", scrubSubdirSecret}, revs...)
	args = append(args, "--")
	// git grep --quiet exits 0 on a match and 1 on none, so "found" is exactly
	// "git exited zero" -- which is what GitTryOut reports.
	_, found := testutil.GitTryOut(t, root, args...)
	return found
}

// scrubSubdirRequirePaths fails when any of the given repo-relative paths is
// missing from HEAD's tree.
func scrubSubdirRequirePaths(t *testing.T, root string, want ...string) {
	t.Helper()
	have := make(map[string]bool)
	for _, p := range testutil.TreePaths(t, root, "HEAD") {
		have[p] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("path %q was destroyed by the scrub; HEAD tree is now: %v", w, testutil.TreePaths(t, root, "HEAD"))
		}
	}
}

// scrubSubdirWriteRecipe writes a single-operation recipe OUTSIDE the repo (an
// untracked file inside the repo would trip the dirty-working-tree check) and
// returns its path.
func scrubSubdirWriteRecipe(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "recipe.toml")
	body := "[[operations]]\npattern = \"" + scrubSubdirSecret + "\"\nreplace = \"REDACTED\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestScrubSubdirFileRootLevelSecret: `scrub file` run from a subdirectory,
// removing a file that lives at the repository root.
//
// Desired: the file is gone from every commit, or the command refuses with a
// nonzero exit. Observed: exit 0, "Verification passed: 2 checks across 0
// rewritten commits", "Scrub complete: 0 commits rewritten", and secret.txt is
// still present -- with its secret -- in every commit.
func TestScrubSubdirFileRootLevelSecret(t *testing.T) {
	root, sub := scrubSubdirRepoRootSecret(t)
	first := scrubSubdirFirstCommit(t, root)

	stdout, stderr, code := runSafegit(t, sub,
		"scrub", "file", "--replace-with", "secret.txt", "--from", first, "--reason", "subdir scrub test",
		"--approve-consequential", "secret.txt")

	if code == 0 {
		if scrubSubdirSecretInHistory(t, root) {
			t.Errorf("scrub file from a subdirectory reported success (exit 0) but the secret "+
				"is still in history\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		scrubSubdirRequirePaths(t, root, "seed.txt", "sub/other.txt")
		return
	}

	// A loud refusal is acceptable, but it must not have half-rewritten anything.
	scrubSubdirRequirePaths(t, root, "seed.txt", "sub/other.txt", "secret.txt")
}

// TestScrubSubdirFileNestedSecretPreservesRoot: `scrub file` run from sub/ with
// a path that resolves against the mis-rooted listing (sub/secret.txt is seen
// as "secret.txt" at what the code believes is the root tree).
//
// Desired: either the secret is gone and every unrelated path survives, or a
// nonzero exit with nothing changed. Observed: exit 0, "Scrub complete: 1
// commits rewritten", and the root tree is REPLACED by sub/'s entries --
// rootfile.txt and seed.txt are deleted from history, sub/ is flattened, and
// (because a same-named file exists in the working tree, so the mode is
// "replace") the secret survives verbatim in the rewritten blob.
func TestScrubSubdirFileNestedSecretPreservesRoot(t *testing.T) {
	root, sub := scrubSubdirRepoNestedSecret(t)
	first := scrubSubdirFirstCommit(t, root)

	stdout, stderr, code := runSafegit(t, sub,
		"scrub", "file", "--replace-with", "secret.txt", "--from", first, "--reason", "subdir scrub test",
		"--approve-consequential", "secret.txt")

	// Whatever the exit code, the repository must not have lost unrelated files.
	scrubSubdirRequirePaths(t, root, "seed.txt", "rootfile.txt", "sub/other.txt")

	if code == 0 && scrubSubdirSecretInHistory(t, root) {
		t.Errorf("scrub file from a subdirectory reported success (exit 0) but the secret "+
			"is still in history\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// TestScrubSubdirMatchNestedSecretPreservesRoot: `scrub match` run from sub/
// where the matching blob lives at sub/secret.txt.
//
// Desired: the secret is replaced in place and every unrelated path survives.
// Observed: exit 0, "Verification passed: no matches found in object stores.",
// "1 commits rewritten" -- and the rewritten root tree is sub/'s tree, so
// rootfile.txt and seed.txt are gone from history and sub/other.txt now lives
// at other.txt. The engine's own post-rewrite re-scan passes precisely because
// the secret was deleted along with the rest of the repository.
func TestScrubSubdirMatchNestedSecretPreservesRoot(t *testing.T) {
	root, sub := scrubSubdirRepoNestedSecret(t)

	stdout, stderr, code := runSafegit(t, sub,
		"scrub", "match", "--pattern", scrubSubdirSecret, "--replace", "REDACTED",
		"--entire-history", "--reason", "subdir scrub test", "--approve-consequential")

	scrubSubdirRequirePaths(t, root, "seed.txt", "rootfile.txt", "sub/other.txt", "sub/secret.txt")

	if code == 0 && scrubSubdirSecretInHistory(t, root) {
		t.Errorf("scrub match from a subdirectory reported success (exit 0) but the secret "+
			"is still in history\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// TestScrubSubdirRunNestedSecretPreservesRoot: the same nested-secret shape
// driven through `scrub run`, which shares the tree-rewrite engine with
// `scrub match` (scrub_exec.go). Observed: identical silent destruction --
// exit 0, "Verification passed", root tree replaced by sub/'s tree.
func TestScrubSubdirRunNestedSecretPreservesRoot(t *testing.T) {
	root, sub := scrubSubdirRepoNestedSecret(t)
	recipe := scrubSubdirWriteRecipe(t)

	stdout, stderr, code := runSafegit(t, sub,
		"scrub", "run", "--reason", "subdir scrub test", "--entire-history",
		"--approve-consequential", recipe)

	scrubSubdirRequirePaths(t, root, "seed.txt", "rootfile.txt", "sub/other.txt", "sub/secret.txt")

	if code == 0 && scrubSubdirSecretInHistory(t, root) {
		t.Errorf("scrub run from a subdirectory reported success (exit 0) but the secret "+
			"is still in history\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// TestScrubSubdirMatchRootLevelSecretIsLoud is a control: `scrub match` run
// from a subdirectory against a ROOT-level secret rewrites nothing, but its
// post-rewrite re-scan (scrub_exec.go:297-311) notices the secret survived and
// exits nonzero. Nothing is destroyed. This documents the one shape where the
// engine's own verification covers for the mis-rooted tree listing.
func TestScrubSubdirMatchRootLevelSecretIsLoud(t *testing.T) {
	root, sub := scrubSubdirRepoRootSecret(t)

	stdout, stderr, code := runSafegit(t, sub,
		"scrub", "match", "--pattern", scrubSubdirSecret, "--replace", "REDACTED",
		"--entire-history", "--reason", "subdir scrub test", "--approve-consequential")

	stillThere := scrubSubdirSecretInHistory(t, root)
	if code == 0 && stillThere {
		t.Errorf("scrub match reported success (exit 0) but the secret is still in history\n"+
			"stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	scrubSubdirRequirePaths(t, root, "seed.txt", "sub/other.txt")
}

// TestScrubSubdirRunRootLevelSecretIsLoud is the same control for `scrub run`.
func TestScrubSubdirRunRootLevelSecretIsLoud(t *testing.T) {
	root, sub := scrubSubdirRepoRootSecret(t)
	recipe := scrubSubdirWriteRecipe(t)

	stdout, stderr, code := runSafegit(t, sub,
		"scrub", "run", "--reason", "subdir scrub test", "--entire-history",
		"--approve-consequential", recipe)

	if code == 0 && scrubSubdirSecretInHistory(t, root) {
		t.Errorf("scrub run reported success (exit 0) but the secret is still in history\n"+
			"stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	scrubSubdirRequirePaths(t, root, "seed.txt", "sub/other.txt")
}

// TestScrubSubdirVerifyReportsViolation is a control: `scrub verify` is not
// cwd-blind. Its object enumeration goes through cat-file
// --batch-all-objects and rev-list --all --objects, neither of which is
// prefix-relative, so a violating blob is reported from a subdirectory exactly
// as it is from the root. Both an unscoped and a scoped run are exercised,
// since scoping is the only part of verify that consults paths.
func TestScrubSubdirVerifyReportsViolation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		scope string
	}{
		{"unscoped", ""},
		{"scoped", "sub/**"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, sub := scrubSubdirRepoNestedSecret(t)

			args := []string{"scrub", "verify", "--pattern", scrubSubdirSecret}
			if tc.scope != "" {
				args = append(args, "--scope", tc.scope)
			}

			rootOut, rootErr, rootCode := runSafegit(t, root, args...)
			if rootCode == 0 {
				t.Fatalf("scrub verify from the repo root passed while the secret is present\n"+
					"stdout:\n%s\nstderr:\n%s", rootOut, rootErr)
			}

			subOut, subErr, subCode := runSafegit(t, sub, args...)
			if subCode == 0 {
				t.Errorf("scrub verify from a subdirectory FALSELY PASSED while the secret is "+
					"present (root run correctly failed)\nstdout:\n%s\nstderr:\n%s", subOut, subErr)
			}
		})
	}
}
