package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// This file pins the behavior safegit must have when it is invoked from a
// repository SUBDIRECTORY rather than the repository root.
//
// Several git plumbing commands safegit relies on are scoped to the process
// working directory: `git ls-files` (with or without -v / -i -c) defaults to
// the pathspec "." and `git ls-tree` prefixes the cwd path onto the tree it
// reads. safegit runs every one of them through internal/git with a bare
// context.Background(), so the process cwd -- an operator's or an agent's
// arbitrary directory -- silently narrows what safegit sees.
//
// Every test below runs the SAME safegit command twice: once from the repo
// root (the control, which documents the behavior safegit already has) and
// once from a subdirectory (which must produce the identical outcome).

// coordSubdirRepo builds a repo with a subdirectory, a tracked file at the
// root and a tracked file inside the subdirectory. Returns (repoDir, subDir).
func coordSubdirRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := newRepo(t)
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "s.txt"), []byte("sub content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testutil.GitRaw(t, dir, "add", "sub/s.txt")
	testutil.GitRaw(t, dir, "commit", "-m", "add sub")
	return dir, sub
}

// coordSubdirBranch returns the current branch name.
func coordSubdirBranch(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(testutil.GitRaw(t, dir, "rev-parse", "--abbrev-ref", "HEAD"))
}

// coordSubdirHasSkipWorktree reports whether file carries the skip-worktree
// flag in the main index.
func coordSubdirHasSkipWorktree(t *testing.T, dir, file string) bool {
	t.Helper()
	out := testutil.GitRaw(t, dir, "ls-files", "-v")
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(line, "S ") && strings.TrimSpace(line[2:]) == file {
			return true
		}
	}
	return false
}

// coordSubdirTrackedPaths returns the paths git currently tracks in the index.
func coordSubdirTrackedPaths(t *testing.T, dir string) []string {
	t.Helper()
	return testutil.SplitLines(strings.TrimSpace(testutil.GitRaw(t, dir, "ls-files")))
}

// coordSubdirIgnoreRepo builds a repo where config.env is tracked AND
// gitignored (committed first, gitignored afterwards) and sub/secret.txt
// carries the same secret. Both files contain the literal "production_key".
// Returns (repoDir, subDir).
func coordSubdirIgnoreRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := newRepo(t)
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.env"), []byte("SECRET=production_key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "secret.txt"), []byte("token=production_key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testutil.GitRaw(t, dir, "add", "config.env", "sub/secret.txt")
	testutil.GitRaw(t, dir, "commit", "-m", "add secrets")

	// Gitignore config.env AFTER it was committed: the tracked-but-ignored
	// state git.ListTrackedIgnoredFiles exists to protect.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("config.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testutil.GitRaw(t, dir, "update-index", "--add", "--", ".gitignore")
	testutil.GitRaw(t, dir, "commit", "-m", "gitignore config.env")
	return dir, sub
}

// ---------------------------------------------------------------------------
// A. Coordination guard (internal/coord/coord.go)
// ---------------------------------------------------------------------------

// TestCoordSubdirCheckoutRefusesUntrackedFromRoot is the control: from the repo
// root the guard sees an untracked file and refuses the checkout.
func TestCoordSubdirCheckoutRefusesUntrackedFromRoot(t *testing.T) {
	dir, _ := coordSubdirRepo(t)
	testutil.GitRaw(t, dir, "branch", "other")

	if err := os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("uncommitted work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runSafegit(t, dir, "checkout", "other")
	if code != 5 {
		t.Fatalf("checkout from root with an untracked file: exit %d, want 5\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "stray.txt") {
		t.Errorf("refusal should name stray.txt; stderr: %s", stderr)
	}
	if b := coordSubdirBranch(t, dir); b != "main" {
		t.Errorf("branch moved to %q despite the refusal", b)
	}
}

// TestCoordSubdirCheckoutRefusesUntrackedFromSubdir asserts the guard is
// repo-wide: an untracked file at the repo root must block a checkout invoked
// from a subdirectory exactly as it blocks one invoked from the root.
//
// coord.Check runs `git ls-files --others --exclude-standard` (coord.go:36)
// with no cwd pinning, and that listing is scoped to the process working
// directory, so from sub/ the root-level untracked file is invisible and the
// guard reports a clean tree.
func TestCoordSubdirCheckoutRefusesUntrackedFromSubdir(t *testing.T) {
	dir, sub := coordSubdirRepo(t)
	testutil.GitRaw(t, dir, "branch", "other")

	if err := os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("uncommitted work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runSafegit(t, sub, "checkout", "other")
	if code != 5 {
		t.Errorf("checkout from sub/ with an untracked file at the repo root: exit %d, want 5 (guard must refuse)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "stray.txt") {
		t.Errorf("refusal should name stray.txt; stderr: %s", stderr)
	}
	if b := coordSubdirBranch(t, dir); b != "main" {
		t.Errorf("branch moved to %q: the guard let a tree-mutating checkout through from a subdirectory", b)
	}
}

// TestCoordSubdirCheckoutRefusesModifiedFromSubdir is the control for the other
// half of coord.Check: `git diff HEAD --name-status` (coord.go:24) IS repo-wide,
// so a modified tracked file outside the subtree is still seen from sub/.
func TestCoordSubdirCheckoutRefusesModifiedFromSubdir(t *testing.T) {
	dir, sub := coordSubdirRepo(t)
	testutil.GitRaw(t, dir, "branch", "other")

	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("locally modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runSafegit(t, sub, "checkout", "other")
	if code != 5 {
		t.Fatalf("checkout from sub/ with a modified file at the repo root: exit %d, want 5\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "seed.txt") {
		t.Errorf("refusal should name seed.txt; stderr: %s", stderr)
	}
	if b := coordSubdirBranch(t, dir); b != "main" {
		t.Errorf("branch moved to %q despite the refusal", b)
	}
}

// TestCoordSubdirResetHardRefusesUntrackedFromSubdir asserts the same guard
// property for `reset --hard`, the most destructive of the guarded commands.
func TestCoordSubdirResetHardRefusesUntrackedFromSubdir(t *testing.T) {
	dir, sub := coordSubdirRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("uncommitted work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Control: from the repo root the guard refuses.
	if _, _, code := runSafegit(t, dir, "reset", "--hard", "HEAD"); code != 5 {
		t.Fatalf("reset --hard from root with an untracked file: exit %d, want 5", code)
	}

	stdout, stderr, code := runSafegit(t, sub, "reset", "--hard", "HEAD")
	if code != 5 {
		t.Errorf("reset --hard from sub/ with an untracked file at the repo root: exit %d, want 5 (guard must refuse)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
}

// ---------------------------------------------------------------------------
// B1. skip-worktree preservation across the index sync
//     (git.ListSkipWorktreeFiles, used by git.ReconcileMainIndex)
// ---------------------------------------------------------------------------

// TestCoordSubdirSkipWorktreeSurvivesCommitFromRoot is the control: a plain
// safegit commit from the repo root preserves a skip-worktree flag.
func TestCoordSubdirSkipWorktreeSurvivesCommitFromRoot(t *testing.T) {
	dir, sub := coordSubdirRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "config.local"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testutil.GitRaw(t, dir, "add", "config.local")
	testutil.GitRaw(t, dir, "commit", "-m", "add config.local")
	testutil.GitRaw(t, dir, "update-index", "--skip-worktree", "config.local")

	if err := os.WriteFile(filepath.Join(sub, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "add feature", "--", "sub/feature.txt"); code != 0 {
		t.Fatalf("commit from root failed (code %d): %s", code, stderr)
	}

	if !coordSubdirHasSkipWorktree(t, dir, "config.local") {
		t.Error("skip-worktree on config.local was dropped by a commit from the repo root")
	}
}

// TestCoordSubdirSkipWorktreeSurvivesCommitFromSubdir asserts that the same
// commit, issued from a subdirectory, preserves the same flag.
//
// The commit pipeline reconciles the shared index through
// git.ReconcileMainIndex, which collects the flags to restore with
// git.ListSkipWorktreeFiles (`git ls-files -v -z`). That listing is cwd-scoped,
// so from sub/ the root-level entry is not collected, the reconciliation's
// `git read-tree HEAD` clears it, and nothing restores it: the flag is silently
// lost, and the local-only content the operator hid behind it becomes visible
// to every later tree operation.
func TestCoordSubdirSkipWorktreeSurvivesCommitFromSubdir(t *testing.T) {
	dir, sub := coordSubdirRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "config.local"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testutil.GitRaw(t, dir, "add", "config.local")
	testutil.GitRaw(t, dir, "commit", "-m", "add config.local")
	testutil.GitRaw(t, dir, "update-index", "--skip-worktree", "config.local")
	if !coordSubdirHasSkipWorktree(t, dir, "config.local") {
		t.Fatal("precondition: skip-worktree not set")
	}

	if err := os.WriteFile(filepath.Join(sub, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runSafegit(t, sub, "commit", "-m", "add feature", "--", "feature.txt"); code != 0 {
		t.Fatalf("commit from sub/ failed (code %d): %s", code, stderr)
	}

	if !coordSubdirHasSkipWorktree(t, dir, "config.local") {
		t.Error("skip-worktree on config.local was dropped by a commit issued from a subdirectory")
	}
}

// ---------------------------------------------------------------------------
// B2. tracked-but-gitignored protection across read-tree --reset -u
//     (git.ListTrackedIgnoredFiles, used by git.SyncMainIndexWithWorktree,
//      reached from the scrub finalization in rewrite_result.go)
// ---------------------------------------------------------------------------

// TestCoordSubdirScrubProtectsTrackedIgnoredFromRoot is the control: a scrub
// run from the repo root rewrites config.env's blob in history, keeps the
// on-disk copy untouched, and untracks it from the index.
func TestCoordSubdirScrubProtectsTrackedIgnoredFromRoot(t *testing.T) {
	dir, _ := coordSubdirIgnoreRepo(t)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubEnv,
		"--approve-consequential", "scrub", "match",
		"--pattern", "production_key",
		"--replace", "REDACTED",
		"--entire-history",
		"--reason", "cwd-scoping control from the repo root",
	)
	if code != 0 {
		t.Fatalf("scrub match from root failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	disk, err := os.ReadFile(filepath.Join(dir, "config.env"))
	if err != nil {
		t.Fatalf("reading config.env: %v", err)
	}
	if string(disk) != "SECRET=production_key\n" {
		t.Errorf("on-disk config.env = %q, want the untouched local content", string(disk))
	}
	if tracked := coordSubdirTrackedPaths(t, dir); testutil.Contains(tracked, "config.env") {
		t.Errorf("config.env should have been untracked from the index; tracked: %v", tracked)
	}
	if content, ok := testutil.Show(t, dir, "HEAD", "config.env"); !ok || content != "SECRET=REDACTED\n" {
		t.Errorf("HEAD:config.env = %q (found=%v), want the scrubbed content", content, ok)
	}
}

// TestCoordSubdirScrubProtectsTrackedIgnoredFromSubdir asserts the identical
// outcome when the scrub is issued from a subdirectory.
//
// git.SyncMainIndexWithWorktree collects the paths to protect with
// git.ListTrackedIgnoredFiles (`git ls-files -i -c --exclude-standard`). From
// sub/ that listing is empty, so the function takes its "no tracked+gitignored
// files" fast path and runs `read-tree --reset -u` with no content save/restore and
// no skip-worktree preservation at all -- the local-only file outside the
// subtree is left to whatever read-tree does to it.
func TestCoordSubdirScrubProtectsTrackedIgnoredFromSubdir(t *testing.T) {
	dir, sub := coordSubdirIgnoreRepo(t)

	stdout, stderr, code := runSafegitEnv(t, sub, scrubEnv,
		"--approve-consequential", "scrub", "match",
		"--pattern", "production_key",
		"--replace", "REDACTED",
		"--entire-history",
		"--reason", "cwd-scoping check from a subdirectory",
	)
	if code != 0 {
		t.Errorf("scrub match from sub/ failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	switch disk, err := os.ReadFile(filepath.Join(dir, "config.env")); {
	case os.IsNotExist(err):
		t.Error("on-disk config.env was DELETED from the working tree by a scrub issued from a subdirectory; the tracked-but-gitignored protection never ran")
	case err != nil:
		t.Errorf("reading config.env: %v", err)
	case string(disk) != "SECRET=production_key\n":
		t.Errorf("on-disk config.env = %q, want the untouched local content", string(disk))
	}
	if tracked := coordSubdirTrackedPaths(t, dir); testutil.Contains(tracked, "config.env") {
		t.Errorf("config.env should have been untracked from the index; tracked: %v", tracked)
	}
	if content, ok := testutil.Show(t, dir, "HEAD", "config.env"); !ok || content != "SECRET=REDACTED\n" {
		t.Errorf("HEAD:config.env = %q (found=%v), want the scrubbed content -- a scrub from a subdirectory must rewrite the whole repository", content, ok)
	}
}

// ---------------------------------------------------------------------------
// C. Tree rewriting must not be scoped to the working directory
//     (git.LsTree, git.go:656 / git.LsTreeAll, git.go:645)
// ---------------------------------------------------------------------------

// TestCoordSubdirScrubFromSubdirPreservesHistoryPaths asserts that a scrub
// issued from a subdirectory rewrites the repository's history, not the
// subdirectory's, and leaves every path in every commit tree where it was.
//
// replaceInTreeByBlobMap (tree_ops.go:122) reads each tree with git.LsTree,
// which runs `git ls-tree <tree>` without --full-tree (git.go:656). git
// resolves that against the process working directory, so from sub/ it returns
// the entries of sub/ INSIDE the tree it was handed. The walker then rebuilds a
// root tree out of those entries via mktree (tree_ops.go:161): every commit's
// tree becomes the subdirectory's tree with the prefix stripped, and every file
// outside the subdirectory disappears from all of history. The post-rewrite
// cleanup (reflog expire + prune) then removes the original objects.
func TestCoordSubdirScrubFromSubdirPreservesHistoryPaths(t *testing.T) {
	dir, sub := coordSubdirIgnoreRepo(t)

	before := testutil.TreePaths(t, dir, "HEAD")
	for _, want := range []string{".gitignore", "config.env", "sub/secret.txt"} {
		if !testutil.Contains(before, want) {
			t.Fatalf("precondition: %s missing from HEAD tree; got %v", want, before)
		}
	}

	// The path set of every commit, oldest first, BEFORE the rewrite. A scrub
	// only changes blob CONTENT, so the rewritten history must present exactly
	// the same paths at exactly the same positions. Each commit is compared
	// against its own before-state rather than against HEAD's path set: the
	// fixture grows over three commits, so demanding every commit hold every
	// path would demand paths that were not yet added.
	beforeTrees := make([][]string, 0, 3)
	for _, sha := range revListReverse(t, dir) {
		beforeTrees = append(beforeTrees, testutil.TreePaths(t, dir, sha))
	}

	stdout, stderr, code := runSafegitEnv(t, sub, scrubEnv,
		"--approve-consequential", "scrub", "match",
		"--pattern", "production_key",
		"--replace", "REDACTED",
		"--entire-history",
		"--reason", "history must survive a scrub issued from a subdirectory",
	)
	if code != 0 {
		t.Errorf("scrub match from sub/ failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// Every commit in the rewritten history must keep its paths.
	afterSHAs := revListReverse(t, dir)
	if len(afterSHAs) != len(beforeTrees) {
		t.Fatalf("history length changed: %d commits before the scrub, %d after", len(beforeTrees), len(afterSHAs))
	}
	for i, sha := range afterSHAs {
		paths := testutil.TreePaths(t, dir, sha)
		for _, want := range beforeTrees[i] {
			if !testutil.Contains(paths, want) {
				t.Errorf("commit %d (%s): %s vanished from the tree after a scrub issued from a subdirectory; tree now: %v, was: %v", i, sha[:8], want, paths, beforeTrees[i])
			}
		}
		for _, got := range paths {
			if !testutil.Contains(beforeTrees[i], got) {
				t.Errorf("commit %d (%s): %s appeared in the tree after a scrub issued from a subdirectory; tree now: %v, was: %v", i, sha[:8], got, paths, beforeTrees[i])
			}
		}
	}

	// And the scrub must actually have done its job on the subtree file.
	if content, ok := testutil.Show(t, dir, "HEAD", "sub/secret.txt"); !ok || content != "token=REDACTED\n" {
		t.Errorf("HEAD:sub/secret.txt = %q (found=%v), want the scrubbed content", content, ok)
	}
}
