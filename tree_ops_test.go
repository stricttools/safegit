package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/smm-h/safegit/internal/git"
)

// initTestRepo creates a bare-minimum git repo in a temp dir, configures
// user.name/email (required for commits), and changes the working directory
// to it for the duration of the test. Returns the repo path and a cleanup
// function that restores the original working directory.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Save original dir so we can restore it.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("init")
	run("config", "user.name", "Test")
	run("config", "user.email", "test@test.com")

	return dir
}

// writeFile creates a file relative to dir with the given content.
func writeFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	abs := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("writefile: %v", err)
	}
}

// commitAll stages all files and commits, returning the commit SHA.
func commitAll(t *testing.T, dir, msg string) string {
	t.Helper()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("add", "-A")
	run("commit", "-m", msg)
	ctx := context.Background()
	sha, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return sha
}

// commitTreeSHA returns the tree SHA of a commit.
func commitTreeSHA(t *testing.T, commitSHA string) string {
	t.Helper()
	ctx := context.Background()
	info, err := git.ParseCommit(ctx, commitSHA)
	if err != nil {
		t.Fatalf("parse-commit %s: %v", commitSHA, err)
	}
	return info.Tree
}

// hashBlob writes content as a blob object and returns its SHA.
func hashBlob(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, ".tmp-blob")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write tmp blob: %v", err)
	}
	defer os.Remove(path)
	ctx := context.Background()
	sha, err := git.HashObjectWrite(ctx, path)
	if err != nil {
		t.Fatalf("hash-object -w: %v", err)
	}
	return sha
}

// findBlobInTree recursively searches the tree for filePath and returns its
// blob SHA, or empty string if not found.
func findBlobInTree(t *testing.T, treeSHA, filePath string) string {
	t.Helper()
	ctx := context.Background()
	entries, err := git.LsTreeAll(ctx, treeSHA)
	if err != nil {
		t.Fatalf("ls-tree -r %s: %v", treeSHA, err)
	}
	for _, e := range entries {
		if e.Path == filePath {
			return e.SHA
		}
	}
	return ""
}

// findEntryMode recursively searches the tree for filePath and returns its mode.
func findEntryMode(t *testing.T, treeSHA, filePath string) string {
	t.Helper()
	ctx := context.Background()
	entries, err := git.LsTreeAll(ctx, treeSHA)
	if err != nil {
		t.Fatalf("ls-tree -r %s: %v", treeSHA, err)
	}
	for _, e := range entries {
		if e.Path == filePath {
			return e.Mode
		}
	}
	return ""
}

// treeEntryNames returns the names of all entries at the top level of a tree.
func treeEntryNames(t *testing.T, treeSHA string) []string {
	t.Helper()
	ctx := context.Background()
	entries, err := git.LsTree(ctx, treeSHA)
	if err != nil {
		t.Fatalf("ls-tree %s: %v", treeSHA, err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Path
	}
	return names
}

func TestReplaceInTreeFlat(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "hello.txt", "hello world\n")
	writeFile(t, dir, "other.txt", "other content\n")
	sha := commitAll(t, dir, "init")
	treeSHA := commitTreeSHA(t, sha)

	ctx := context.Background()
	newBlob := hashBlob(t, dir, "replaced content\n")

	origOtherBlob := findBlobInTree(t, treeSHA, "other.txt")
	origHelloBlob := findBlobInTree(t, treeSHA, "hello.txt")

	cache := make(map[string]string)
	newTree, err := replaceInTree(ctx, treeSHA, "hello.txt", newBlob, cache)
	if err != nil {
		t.Fatalf("replaceInTree: %v", err)
	}

	// Tree SHA must change.
	if newTree == treeSHA {
		t.Fatal("expected tree SHA to change")
	}

	// The replaced file should have the new blob.
	gotBlob := findBlobInTree(t, newTree, "hello.txt")
	if gotBlob != newBlob {
		t.Errorf("hello.txt blob = %s, want %s", gotBlob, newBlob)
	}

	// The original blob should differ.
	if gotBlob == origHelloBlob {
		t.Errorf("hello.txt blob should differ from original")
	}

	// other.txt should be unchanged.
	gotOther := findBlobInTree(t, newTree, "other.txt")
	if gotOther != origOtherBlob {
		t.Errorf("other.txt blob changed: got %s, want %s", gotOther, origOtherBlob)
	}
}

func TestReplaceInTreeNested(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "a/b/c.txt", "deep content\n")
	writeFile(t, dir, "a/b/other.txt", "sibling\n")
	writeFile(t, dir, "a/top.txt", "top\n")
	writeFile(t, dir, "root.txt", "root\n")
	sha := commitAll(t, dir, "init")
	treeSHA := commitTreeSHA(t, sha)

	ctx := context.Background()
	newBlob := hashBlob(t, dir, "new deep content\n")

	origRootBlob := findBlobInTree(t, treeSHA, "root.txt")
	origTopBlob := findBlobInTree(t, treeSHA, "a/top.txt")
	origSiblingBlob := findBlobInTree(t, treeSHA, "a/b/other.txt")

	cache := make(map[string]string)
	newTree, err := replaceInTree(ctx, treeSHA, "a/b/c.txt", newBlob, cache)
	if err != nil {
		t.Fatalf("replaceInTree: %v", err)
	}

	if newTree == treeSHA {
		t.Fatal("expected root tree SHA to change")
	}

	// Target file should have the new blob.
	gotBlob := findBlobInTree(t, newTree, "a/b/c.txt")
	if gotBlob != newBlob {
		t.Errorf("a/b/c.txt blob = %s, want %s", gotBlob, newBlob)
	}

	// Sibling, parent-level, and root files should be unchanged.
	if got := findBlobInTree(t, newTree, "a/b/other.txt"); got != origSiblingBlob {
		t.Errorf("a/b/other.txt changed: got %s, want %s", got, origSiblingBlob)
	}
	if got := findBlobInTree(t, newTree, "a/top.txt"); got != origTopBlob {
		t.Errorf("a/top.txt changed: got %s, want %s", got, origTopBlob)
	}
	if got := findBlobInTree(t, newTree, "root.txt"); got != origRootBlob {
		t.Errorf("root.txt changed: got %s, want %s", got, origRootBlob)
	}
}

func TestReplaceInTreeRemove(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "keep.txt", "keep\n")
	writeFile(t, dir, "remove.txt", "doomed\n")
	sha := commitAll(t, dir, "init")
	treeSHA := commitTreeSHA(t, sha)

	ctx := context.Background()
	origKeepBlob := findBlobInTree(t, treeSHA, "keep.txt")

	cache := make(map[string]string)
	newTree, err := replaceInTree(ctx, treeSHA, "remove.txt", "", cache)
	if err != nil {
		t.Fatalf("replaceInTree: %v", err)
	}

	if newTree == treeSHA {
		t.Fatal("expected tree SHA to change")
	}

	// Removed file should be absent.
	if got := findBlobInTree(t, newTree, "remove.txt"); got != "" {
		t.Errorf("remove.txt still present with blob %s", got)
	}

	// keep.txt should survive.
	if got := findBlobInTree(t, newTree, "keep.txt"); got != origKeepBlob {
		t.Errorf("keep.txt changed: got %s, want %s", got, origKeepBlob)
	}
}

func TestReplaceInTreeNotFound(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "exists.txt", "here\n")
	sha := commitAll(t, dir, "init")
	treeSHA := commitTreeSHA(t, sha)

	ctx := context.Background()
	dummyBlob := hashBlob(t, dir, "irrelevant\n")

	cache := make(map[string]string)
	newTree, err := replaceInTree(ctx, treeSHA, "nonexistent.txt", dummyBlob, cache)
	if err != nil {
		t.Fatalf("replaceInTree: %v", err)
	}

	// Should return original tree unchanged.
	if newTree != treeSHA {
		t.Errorf("expected original tree %s, got %s", treeSHA, newTree)
	}
}

func TestReplaceInTreePreservesMode(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "script.sh", "#!/bin/sh\necho hello\n")

	// Make the file executable and stage it with the executable mode.
	cmd := exec.Command("chmod", "+x", filepath.Join(dir, "script.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("chmod: %v\n%s", err, out)
	}

	writeFile(t, dir, "normal.txt", "normal\n")
	sha := commitAll(t, dir, "init")
	treeSHA := commitTreeSHA(t, sha)

	// Verify the file is stored as executable.
	origMode := findEntryMode(t, treeSHA, "script.sh")
	if origMode != "100755" {
		t.Fatalf("expected mode 100755 for script.sh, got %s", origMode)
	}

	ctx := context.Background()
	newBlob := hashBlob(t, dir, "#!/bin/sh\necho replaced\n")

	cache := make(map[string]string)
	newTree, err := replaceInTree(ctx, treeSHA, "script.sh", newBlob, cache)
	if err != nil {
		t.Fatalf("replaceInTree: %v", err)
	}

	// Mode must be preserved.
	gotMode := findEntryMode(t, newTree, "script.sh")
	if gotMode != "100755" {
		t.Errorf("mode after replace = %s, want 100755", gotMode)
	}

	// Blob must be replaced.
	gotBlob := findBlobInTree(t, newTree, "script.sh")
	if gotBlob != newBlob {
		t.Errorf("script.sh blob = %s, want %s", gotBlob, newBlob)
	}
}

func TestReplaceInTreeByBlobMapFlat(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "a.txt", "aaa\n")
	writeFile(t, dir, "b.txt", "bbb\n")
	writeFile(t, dir, "c.txt", "ccc\n")
	sha := commitAll(t, dir, "init")
	treeSHA := commitTreeSHA(t, sha)

	ctx := context.Background()

	origA := findBlobInTree(t, treeSHA, "a.txt")
	origB := findBlobInTree(t, treeSHA, "b.txt")
	origC := findBlobInTree(t, treeSHA, "c.txt")

	newA := hashBlob(t, dir, "AAA\n")
	newB := hashBlob(t, dir, "BBB\n")

	blobMap := map[string]string{
		origA: newA,
		origB: newB,
	}

	cache := make(map[string]string)
	newTree, err := replaceInTreeByBlobMap(ctx, treeSHA, blobMap, nil, cache)
	if err != nil {
		t.Fatalf("replaceInTreeByBlobMap: %v", err)
	}
	if newTree == treeSHA {
		t.Fatal("expected tree SHA to change")
	}

	// a.txt and b.txt should be replaced.
	if got := findBlobInTree(t, newTree, "a.txt"); got != newA {
		t.Errorf("a.txt blob = %s, want %s", got, newA)
	}
	if got := findBlobInTree(t, newTree, "b.txt"); got != newB {
		t.Errorf("b.txt blob = %s, want %s", got, newB)
	}

	// c.txt should be unchanged.
	if got := findBlobInTree(t, newTree, "c.txt"); got != origC {
		t.Errorf("c.txt blob changed: got %s, want %s", got, origC)
	}
}

func TestReplaceInTreeByBlobMapNested(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "root.txt", "root\n")
	writeFile(t, dir, "a/mid.txt", "mid\n")
	writeFile(t, dir, "a/b/deep.txt", "deep\n")
	writeFile(t, dir, "a/b/sibling.txt", "sibling\n")
	sha := commitAll(t, dir, "init")
	treeSHA := commitTreeSHA(t, sha)

	ctx := context.Background()

	origRoot := findBlobInTree(t, treeSHA, "root.txt")
	origMid := findBlobInTree(t, treeSHA, "a/mid.txt")
	origDeep := findBlobInTree(t, treeSHA, "a/b/deep.txt")
	origSibling := findBlobInTree(t, treeSHA, "a/b/sibling.txt")

	newDeep := hashBlob(t, dir, "DEEP REPLACED\n")

	blobMap := map[string]string{
		origDeep: newDeep,
	}

	cache := make(map[string]string)
	newTree, err := replaceInTreeByBlobMap(ctx, treeSHA, blobMap, nil, cache)
	if err != nil {
		t.Fatalf("replaceInTreeByBlobMap: %v", err)
	}
	if newTree == treeSHA {
		t.Fatal("expected root tree SHA to change")
	}

	// Deep blob should be replaced.
	if got := findBlobInTree(t, newTree, "a/b/deep.txt"); got != newDeep {
		t.Errorf("a/b/deep.txt blob = %s, want %s", got, newDeep)
	}

	// Sibling, mid-level, and root blobs should be unchanged.
	if got := findBlobInTree(t, newTree, "a/b/sibling.txt"); got != origSibling {
		t.Errorf("a/b/sibling.txt changed: got %s, want %s", got, origSibling)
	}
	if got := findBlobInTree(t, newTree, "a/mid.txt"); got != origMid {
		t.Errorf("a/mid.txt changed: got %s, want %s", got, origMid)
	}
	if got := findBlobInTree(t, newTree, "root.txt"); got != origRoot {
		t.Errorf("root.txt changed: got %s, want %s", got, origRoot)
	}
}

func TestReplaceInTreeByBlobMapNoMatch(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "hello.txt", "hello\n")
	sha := commitAll(t, dir, "init")
	treeSHA := commitTreeSHA(t, sha)

	ctx := context.Background()

	// blobMap with SHAs that don't exist in the tree.
	bogusOld := hashBlob(t, dir, "does not exist in tree\n")
	bogusNew := hashBlob(t, dir, "replacement\n")

	blobMap := map[string]string{
		bogusOld: bogusNew,
	}

	cache := make(map[string]string)
	newTree, err := replaceInTreeByBlobMap(ctx, treeSHA, blobMap, nil, cache)
	if err != nil {
		t.Fatalf("replaceInTreeByBlobMap: %v", err)
	}
	if newTree != treeSHA {
		t.Errorf("expected original tree %s, got %s", treeSHA, newTree)
	}
}

func TestReplaceInTreeByBlobMapWithCache(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "file.txt", "content\n")
	sha1 := commitAll(t, dir, "first")
	tree1 := commitTreeSHA(t, sha1)

	// Second commit with a different file, same tree structure concept.
	// We reuse the same tree by making a commit that doesn't change the tree.
	writeFile(t, dir, "file.txt", "content\n") // no-op write
	// Force a new commit with --allow-empty to get a different commit but same tree.
	cmd := exec.Command("git", "commit", "--allow-empty", "-m", "second")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit --allow-empty: %v\n%s", err, out)
	}
	ctx := context.Background()
	sha2, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	tree2 := commitTreeSHA(t, sha2)

	// Both commits should have the same tree SHA.
	if tree1 != tree2 {
		t.Fatalf("expected same tree SHA for both commits, got %s and %s", tree1, tree2)
	}

	origBlob := findBlobInTree(t, tree1, "file.txt")
	newBlob := hashBlob(t, dir, "replaced\n")
	blobMap := map[string]string{origBlob: newBlob}

	// Use built-in cache: first call populates it, second call hits it.
	cache := make(map[string]string)

	// First call: cache miss, compute result.
	result1, err := replaceInTreeByBlobMap(ctx, tree1, blobMap, nil, cache)
	if err != nil {
		t.Fatalf("replaceInTreeByBlobMap (first): %v", err)
	}

	// Cache should now contain the tree mapping.
	if len(cache) == 0 {
		t.Fatal("expected cache to be populated after first call")
	}

	// Second call with the same tree SHA should hit the cache.
	result2, err := replaceInTreeByBlobMap(ctx, tree2, blobMap, nil, cache)
	if err != nil {
		t.Fatalf("replaceInTreeByBlobMap (second): %v", err)
	}

	if result1 != result2 {
		t.Errorf("cached result mismatch: %s vs %s", result1, result2)
	}

	// Verify the result is correct.
	if got := findBlobInTree(t, result1, "file.txt"); got != newBlob {
		t.Errorf("file.txt blob = %s, want %s", got, newBlob)
	}
}

func TestReplaceInTreeRemoveNested(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "a/b/c.txt", "target\n")
	writeFile(t, dir, "a/b/sibling.txt", "sibling\n")
	writeFile(t, dir, "a/top.txt", "top\n")
	sha := commitAll(t, dir, "init")
	treeSHA := commitTreeSHA(t, sha)

	ctx := context.Background()

	cache := make(map[string]string)
	newTree, err := replaceInTree(ctx, treeSHA, "a/b/c.txt", "", cache)
	if err != nil {
		t.Fatalf("replaceInTree: %v", err)
	}

	if newTree == treeSHA {
		t.Fatal("expected tree SHA to change")
	}

	// Removed file should be absent.
	if got := findBlobInTree(t, newTree, "a/b/c.txt"); got != "" {
		t.Errorf("a/b/c.txt still present with blob %s", got)
	}

	// Sibling should survive -- intermediate directory "a/b" must still exist.
	if got := findBlobInTree(t, newTree, "a/b/sibling.txt"); got == "" {
		t.Error("a/b/sibling.txt missing after removing a/b/c.txt")
	}

	// Parent-level file should survive.
	if got := findBlobInTree(t, newTree, "a/top.txt"); got == "" {
		t.Error("a/top.txt missing after removing a/b/c.txt")
	}

	// Verify intermediate directories still exist at the top level of their
	// respective trees by checking entry names.
	rootNames := treeEntryNames(t, newTree)
	found := false
	for _, n := range rootNames {
		if n == "a" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("directory 'a' missing from root tree; entries: %v", rootNames)
	}
}
