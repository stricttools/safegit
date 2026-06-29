package test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// makeCommits creates n commits in repoDir with the given author/committer identity.
// Each commit adds a file named <prefix><i>.txt with content <prefix><i>.
func makeCommits(t *testing.T, repoDir string, authorName, authorEmail string, n int, prefix string) {
	t.Helper()
	for i := 0; i < n; i++ {
		fname := fmt.Sprintf("%s%d.txt", prefix, i)
		content := fmt.Sprintf("%s%d", prefix, i)
		fpath := filepath.Join(repoDir, fname)
		if err := os.WriteFile(fpath, []byte(content), 0644); err != nil {
			t.Fatalf("writing %s: %v", fname, err)
		}

		cmd := exec.Command("git", "add", fname)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git add %s: %v\n%s", fname, err, out)
		}

		msg := fmt.Sprintf("%s commit %d", prefix, i)
		cmd = exec.Command("git", "commit", "-m", msg)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME="+authorName,
			"GIT_AUTHOR_EMAIL="+authorEmail,
			"GIT_COMMITTER_NAME="+authorName,
			"GIT_COMMITTER_EMAIL="+authorEmail,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit %s: %v\n%s", fname, err, out)
		}
	}
}

// getAuthorNames returns the list of author names from all commits (topo-order).
func getAuthorNames(t *testing.T, repoDir string) []string {
	t.Helper()
	cmd := exec.Command("git", "log", "--all", "--topo-order", "--format=%an")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log --format=%%an: %v", err)
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

// getCommitCount returns the total number of commits across all refs.
func getCommitCount(t *testing.T, repoDir string) int {
	t.Helper()
	cmd := exec.Command("git", "rev-list", "--all", "--count")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-list --all --count: %v", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("parsing commit count: %v", err)
	}
	return n
}

// getTreeHashes returns the list of tree hashes from all commits (topo-order).
func getTreeHashes(t *testing.T, repoDir string) []string {
	t.Helper()
	cmd := exec.Command("git", "log", "--all", "--topo-order", "--format=%T")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log --format=%%T: %v", err)
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

// containsName checks if a name appears in the list.
func containsName(names []string, target string) bool {
	for _, n := range names {
		if n == target {
			return true
		}
	}
	return false
}

// slicesEqual checks if two string slices are identical.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRewriteAuthorBasic(t *testing.T) {
	dir := newRepo(t) // 1 initial commit by "Test" <test@test.com>
	makeCommits(t, dir, "oldname", "old@test.com", 10, "basic")

	// Record pre-rewrite state
	beforeCount := getCommitCount(t, dir)
	beforeTrees := getTreeHashes(t, dir)

	stdout, stderr, code := runSafegit(t, dir, "--yes", "author", "rewrite", "--old-name", "oldname", "--new-name", "newname")
	if code != 0 {
		t.Fatalf("rewrite-author failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	names := getAuthorNames(t, dir)

	if containsName(names, "oldname") {
		t.Errorf("author name 'oldname' still present after rewrite: %v", names)
	}
	if !containsName(names, "newname") {
		t.Errorf("author name 'newname' not present after rewrite: %v", names)
	}
	if !containsName(names, "Test") {
		t.Errorf("initial commit author 'Test' should be preserved: %v", names)
	}

	afterCount := getCommitCount(t, dir)
	if afterCount != beforeCount {
		t.Errorf("commit count changed: before=%d, after=%d", beforeCount, afterCount)
	}
	if beforeCount != 11 {
		t.Errorf("expected 11 total commits (1 initial + 10), got %d", beforeCount)
	}

	afterTrees := getTreeHashes(t, dir)
	if !slicesEqual(beforeTrees, afterTrees) {
		t.Errorf("tree hashes changed after rewrite")
	}
}

func TestRewriteAuthorMixedAuthors(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "alice", "alice@test.com", 5, "alice")
	makeCommits(t, dir, "bob", "bob@test.com", 5, "bob")

	stdout, stderr, code := runSafegit(t, dir, "--yes", "author", "rewrite", "--old-name", "alice", "--new-name", "alice-new")
	if code != 0 {
		t.Fatalf("rewrite-author failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	names := getAuthorNames(t, dir)

	if containsName(names, "alice") {
		t.Errorf("author name 'alice' still present after rewrite: %v", names)
	}
	if !containsName(names, "alice-new") {
		t.Errorf("author name 'alice-new' not present after rewrite: %v", names)
	}
	if !containsName(names, "bob") {
		t.Errorf("author name 'bob' should be preserved: %v", names)
	}
	if !containsName(names, "Test") {
		t.Errorf("initial commit author 'Test' should be preserved: %v", names)
	}

	afterCount := getCommitCount(t, dir)
	if afterCount != 11 {
		t.Errorf("expected 11 commits, got %d", afterCount)
	}
}

func TestRewriteAuthorWithTags(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "oldname", "old@test.com", 5, "tag")

	// Record the commit messages that v1.0 and v0.1 will point to
	// v1.0 -> HEAD, v0.1 -> HEAD~2
	cmdMsg := exec.Command("git", "log", "-1", "--format=%s", "HEAD")
	cmdMsg.Dir = dir
	outMsg, err := cmdMsg.Output()
	if err != nil {
		t.Fatalf("getting HEAD message: %v", err)
	}
	v10Msg := strings.TrimSpace(string(outMsg))

	cmdMsg = exec.Command("git", "log", "-1", "--format=%s", "HEAD~2")
	cmdMsg.Dir = dir
	outMsg, err = cmdMsg.Output()
	if err != nil {
		t.Fatalf("getting HEAD~2 message: %v", err)
	}
	v01Msg := strings.TrimSpace(string(outMsg))

	// Create tags
	cmd := exec.Command("git", "tag", "v1.0")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git tag v1.0: %v\n%s", err, out)
	}

	cmd = exec.Command("git", "tag", "v0.1", "HEAD~2")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git tag v0.1: %v\n%s", err, out)
	}

	beforeCount := getCommitCount(t, dir)

	stdout, stderr, code := runSafegit(t, dir, "--yes", "author", "rewrite", "--old-name", "oldname", "--new-name", "newname")
	if code != 0 {
		t.Fatalf("rewrite-author failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Tags still exist
	cmd = exec.Command("git", "tag", "-l")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git tag -l: %v", err)
	}
	tags := strings.TrimSpace(string(out))
	if !strings.Contains(tags, "v1.0") {
		t.Errorf("tag v1.0 missing after rewrite")
	}
	if !strings.Contains(tags, "v0.1") {
		t.Errorf("tag v0.1 missing after rewrite")
	}

	// Each tag still points to a commit with the same message
	for _, tc := range []struct {
		tag     string
		wantMsg string
	}{
		{"v1.0", v10Msg},
		{"v0.1", v01Msg},
	} {
		cmdMsg = exec.Command("git", "log", "-1", "--format=%s", tc.tag)
		cmdMsg.Dir = dir
		outMsg, err = cmdMsg.Output()
		if err != nil {
			t.Fatalf("getting message for tag %s: %v", tc.tag, err)
		}
		gotMsg := strings.TrimSpace(string(outMsg))
		if gotMsg != tc.wantMsg {
			t.Errorf("tag %s message changed: want %q, got %q", tc.tag, tc.wantMsg, gotMsg)
		}
	}

	// Author names rewritten
	names := getAuthorNames(t, dir)
	if containsName(names, "oldname") {
		t.Errorf("author name 'oldname' still present after rewrite: %v", names)
	}

	// Commit count unchanged
	afterCount := getCommitCount(t, dir)
	if afterCount != beforeCount {
		t.Errorf("commit count changed: before=%d, after=%d", beforeCount, afterCount)
	}
}

func TestRewriteAuthorMerge(t *testing.T) {
	dir := newRepo(t) // initial commit by "Test"

	// Create feature branch
	cmd := exec.Command("git", "checkout", "-b", "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b feature: %v\n%s", err, out)
	}

	makeCommits(t, dir, "oldname", "old@test.com", 2, "feat")

	// Switch back to main
	cmd = exec.Command("git", "checkout", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout main: %v\n%s", err, out)
	}

	makeCommits(t, dir, "oldname", "old@test.com", 2, "main")

	// Merge feature into main with --no-ff
	cmd = exec.Command("git", "merge", "feature", "--no-ff", "-m", "merge")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=oldname",
		"GIT_AUTHOR_EMAIL=old@test.com",
		"GIT_COMMITTER_NAME=oldname",
		"GIT_COMMITTER_EMAIL=old@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git merge: %v\n%s", err, out)
	}

	beforeCount := getCommitCount(t, dir)
	beforeTrees := getTreeHashes(t, dir)

	stdout, stderr, code := runSafegit(t, dir, "--yes", "author", "rewrite", "--old-name", "oldname", "--new-name", "newname")
	if code != 0 {
		t.Fatalf("rewrite-author failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Commit count unchanged
	afterCount := getCommitCount(t, dir)
	if afterCount != beforeCount {
		t.Errorf("commit count changed: before=%d, after=%d", beforeCount, afterCount)
	}

	// Verify merge commit (HEAD) has 2 parents
	cmd = exec.Command("git", "log", "-1", "--format=%P", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("getting HEAD parents: %v", err)
	}
	parents := strings.Fields(strings.TrimSpace(string(out)))
	if len(parents) != 2 {
		t.Errorf("merge commit should have 2 parents, got %d: %v", len(parents), parents)
	}

	// Tree hashes unchanged
	afterTrees := getTreeHashes(t, dir)
	if !slicesEqual(beforeTrees, afterTrees) {
		t.Errorf("tree hashes changed after rewrite")
	}

	// No "oldname" in author names
	names := getAuthorNames(t, dir)
	if containsName(names, "oldname") {
		t.Errorf("author name 'oldname' still present after rewrite: %v", names)
	}
}

func TestRewriteAuthorMultipleBranches(t *testing.T) {
	dir := newRepo(t) // 1 initial commit by "Test"

	// Get the initial commit SHA for creating branches from
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	initialSHA := strings.TrimSpace(string(out))

	// Create 3 branches from initial commit, each with 3 commits by "oldname"
	for _, branch := range []string{"br1", "br2", "br3"} {
		cmd = exec.Command("git", "checkout", "-b", branch, initialSHA)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git checkout -b %s: %v\n%s", branch, err, out)
		}
		makeCommits(t, dir, "oldname", "old@test.com", 3, branch+"_")
	}

	// Switch back to main
	cmd = exec.Command("git", "checkout", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout main: %v\n%s", err, out)
	}

	beforeCount := getCommitCount(t, dir)

	stdout, stderr, code := runSafegit(t, dir, "--yes", "author", "rewrite", "--old-name", "oldname", "--new-name", "newname")
	if code != 0 {
		t.Fatalf("rewrite-author failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// All 3 branches exist
	for _, branch := range []string{"br1", "br2", "br3"} {
		cmd = exec.Command("git", "rev-parse", "--verify", "refs/heads/"+branch)
		cmd.Dir = dir
		if _, err := cmd.Output(); err != nil {
			t.Errorf("branch %s missing after rewrite", branch)
		}
	}

	// Commit count unchanged (1 initial shared + 3*3 = 10)
	afterCount := getCommitCount(t, dir)
	if afterCount != beforeCount {
		t.Errorf("commit count changed: before=%d, after=%d", beforeCount, afterCount)
	}

	// No "oldname" in any branch's log
	names := getAuthorNames(t, dir)
	if containsName(names, "oldname") {
		t.Errorf("author name 'oldname' still present after rewrite: %v", names)
	}
}

func TestRewriteAuthorNoOp(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "alice", "alice@test.com", 5, "noop")

	beforeCount := getCommitCount(t, dir)
	beforeTrees := getTreeHashes(t, dir)
	beforeNames := getAuthorNames(t, dir)

	stdout, stderr, code := runSafegit(t, dir, "--yes", "author", "rewrite", "--old-name", "nonexistent", "--new-name", "something")
	if code != 0 {
		t.Fatalf("rewrite-author failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	afterCount := getCommitCount(t, dir)
	if afterCount != beforeCount {
		t.Errorf("commit count changed: before=%d, after=%d", beforeCount, afterCount)
	}

	afterNames := getAuthorNames(t, dir)
	if !slicesEqual(beforeNames, afterNames) {
		t.Errorf("author names changed for no-op rewrite: before=%v, after=%v", beforeNames, afterNames)
	}

	afterTrees := getTreeHashes(t, dir)
	if !slicesEqual(beforeTrees, afterTrees) {
		t.Errorf("tree hashes changed for no-op rewrite")
	}
}

func TestRewriteAuthorIdempotent(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "oldname", "old@test.com", 5, "idem")

	// First rewrite
	stdout, stderr, code := runSafegit(t, dir, "--yes", "author", "rewrite", "--old-name", "oldname", "--new-name", "newname")
	if code != 0 {
		t.Fatalf("first rewrite-author failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	afterFirstCount := getCommitCount(t, dir)
	afterFirstTrees := getTreeHashes(t, dir)

	// Second rewrite (same arguments -- should be a no-op)
	stdout, stderr, code = runSafegit(t, dir, "--yes", "author", "rewrite", "--old-name", "oldname", "--new-name", "newname")
	if code != 0 {
		t.Fatalf("second rewrite-author failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	afterSecondCount := getCommitCount(t, dir)
	if afterSecondCount != afterFirstCount {
		t.Errorf("commit count changed between runs: first=%d, second=%d", afterFirstCount, afterSecondCount)
	}

	afterSecondTrees := getTreeHashes(t, dir)
	if !slicesEqual(afterFirstTrees, afterSecondTrees) {
		t.Errorf("tree hashes changed between runs")
	}

	names := getAuthorNames(t, dir)
	if containsName(names, "oldname") {
		t.Errorf("author name 'oldname' still present after two rewrites: %v", names)
	}
}

func TestRewriteAuthorRootCommit(t *testing.T) {
	// Create a new repo from scratch (not using newRepo, so we control the root commit's author)
	dir := evalTempDir(t)

	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Create the root commit with author "oldname"
	rootFile := filepath.Join(dir, "root.txt")
	if err := os.WriteFile(rootFile, []byte("root"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "root.txt")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add root.txt: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "root commit")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=oldname",
		"GIT_AUTHOR_EMAIL=old@test.com",
		"GIT_COMMITTER_NAME=oldname",
		"GIT_COMMITTER_EMAIL=old@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit root: %v\n%s", err, out)
	}

	// Add 3 more commits by "oldname"
	makeCommits(t, dir, "oldname", "old@test.com", 3, "root")

	// Initialize safegit in the repo (rewrite-author requires it)
	runSafegit(t, dir, "config", "set", "commit.casMaxAttempts", "200")

	stdout, stderr, code := runSafegit(t, dir, "--yes", "author", "rewrite", "--old-name", "oldname", "--new-name", "newname")
	if code != 0 {
		t.Fatalf("rewrite-author failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	names := getAuthorNames(t, dir)
	if containsName(names, "oldname") {
		t.Errorf("author name 'oldname' still present after rewrite: %v", names)
	}

	// Verify root commit was rewritten: the earliest commit should have author "newname"
	cmd = exec.Command("git", "log", "--format=%an", "--reverse")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		t.Fatal("no commits found")
	}
	if lines[0] != "newname" {
		t.Errorf("root commit author = %q, want 'newname'", lines[0])
	}

	afterCount := getCommitCount(t, dir)
	if afterCount != 4 {
		t.Errorf("expected 4 commits, got %d", afterCount)
	}
}

func TestRewriteAuthorDryRun(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "oldname", "old@test.com", 5, "dry")

	beforeNames := getAuthorNames(t, dir)
	beforeCount := getCommitCount(t, dir)

	// --dry-run is a global flag, placed before the command name
	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "author", "rewrite", "--old-name", "oldname", "--new-name", "newname")
	if code != 0 {
		t.Fatalf("dry-run rewrite-author failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// stdout should mention "Would rewrite"
	if !strings.Contains(stdout, "Would rewrite") {
		t.Errorf("dry-run output missing 'Would rewrite': %s", stdout)
	}

	// Author names UNCHANGED (nothing was modified)
	afterNames := getAuthorNames(t, dir)
	if !slicesEqual(beforeNames, afterNames) {
		t.Errorf("author names changed during dry-run: before=%v, after=%v", beforeNames, afterNames)
	}

	// Commit count unchanged
	afterCount := getCommitCount(t, dir)
	if afterCount != beforeCount {
		t.Errorf("commit count changed during dry-run: before=%d, after=%d", beforeCount, afterCount)
	}
}

func TestRewriteAuthorAnnotatedTags(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "oldname", "old@test.com", 5, "f")

	// Create an annotated tag on HEAD
	cmd := exec.Command("git", "tag", "-a", "v1.0", "-m", "release v1.0")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_COMMITTER_NAME=oldname",
		"GIT_COMMITTER_EMAIL=old@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git tag -a: %v\n%s", err, out)
	}

	// Record the commit message the tag points to
	cmd = exec.Command("git", "log", "-1", "--format=%s", "v1.0")
	cmd.Dir = dir
	beforeMsg, _ := cmd.Output()

	_, stderr, code := runSafegit(t, dir, "--yes", "author", "rewrite", "--old-name", "oldname", "--new-name", "newname")
	if code != 0 {
		t.Fatalf("rewrite-author failed (exit %d): %s", code, stderr)
	}

	// Tag still exists
	cmd = exec.Command("git", "tag", "-l", "v1.0")
	cmd.Dir = dir
	tagOut, _ := cmd.Output()
	if !strings.Contains(string(tagOut), "v1.0") {
		t.Error("tag v1.0 missing after rewrite")
	}

	// Tag still points to same commit message
	cmd = exec.Command("git", "log", "-1", "--format=%s", "v1.0")
	cmd.Dir = dir
	afterMsg, _ := cmd.Output()
	if string(beforeMsg) != string(afterMsg) {
		t.Errorf("tag message changed: %q -> %q", beforeMsg, afterMsg)
	}

	// Tag object has rewritten tagger
	cmd = exec.Command("git", "cat-file", "-p", "v1.0")
	cmd.Dir = dir
	tagContent, _ := cmd.Output()
	if strings.Contains(string(tagContent), "oldname") {
		t.Error("annotated tag still contains oldname in tagger field")
	}
	if !strings.Contains(string(tagContent), "newname") {
		t.Error("annotated tag does not contain newname in tagger field")
	}

	// No oldname in commit authors
	names := getAuthorNames(t, dir)
	if containsName(names, "oldname") {
		t.Error("oldname still present in author names")
	}
}

func TestRewriteAuthorMissingFlags(t *testing.T) {
	dir := newRepo(t)

	// No flags at all
	_, _, code := runSafegit(t, dir, "author", "rewrite")
	if code == 0 {
		t.Error("rewrite-author with no flags should fail, but exited 0")
	}

	// Missing --new-name
	_, _, code = runSafegit(t, dir, "author", "rewrite", "--old-name", "foo")
	if code == 0 {
		t.Error("rewrite-author without --new-name should fail, but exited 0")
	}
}

// getAuthorEmails returns the list of author emails from all commits (topo-order).
func getAuthorEmails(t *testing.T, repoDir string) []string {
	t.Helper()
	cmd := exec.Command("git", "log", "--all", "--topo-order", "--format=%ae")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log emails: %v", err)
	}
	var emails []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			emails = append(emails, line)
		}
	}
	return emails
}

func TestRewriteAuthorFlagEquals(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "oldname", "old@test.com", 5, "eq")

	// --yes skips the interactive confirmation prompt (runSafegit has no stdin)
	stdout, stderr, code := runSafegit(t, dir, "--yes", "author", "rewrite", "--old-name=oldname", "--new-name=newname")
	if code != 0 {
		t.Fatalf("rewrite-author failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	names := getAuthorNames(t, dir)
	if containsName(names, "oldname") {
		t.Errorf("author name 'oldname' still present after rewrite: %v", names)
	}
	if !containsName(names, "newname") {
		t.Errorf("author name 'newname' not present after rewrite: %v", names)
	}
}

func TestRewriteAuthorEmailOnly(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "alice", "old@test.com", 5, "email")

	// --yes skips the interactive confirmation prompt (runSafegit has no stdin)
	stdout, stderr, code := runSafegit(t, dir, "--yes", "author", "rewrite", "--old-email=old@test.com", "--new-email=new@test.com")
	if code != 0 {
		t.Fatalf("rewrite-author failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Name unchanged
	names := getAuthorNames(t, dir)
	if !containsName(names, "alice") {
		t.Errorf("author name 'alice' should still be present: %v", names)
	}

	// Email changed
	emails := getAuthorEmails(t, dir)
	for _, e := range emails {
		if e == "old@test.com" {
			t.Errorf("old email 'old@test.com' still present after rewrite: %v", emails)
			break
		}
	}
	if !containsName(emails, "new@test.com") {
		t.Errorf("new email 'new@test.com' not present after rewrite: %v", emails)
	}
}

func TestRewriteAuthorANDMatching(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "alice", "alice@work.com", 3, "work")
	makeCommits(t, dir, "alice", "alice@home.com", 3, "home")

	stdout, stderr, code := runSafegit(t, dir, "--yes", "author", "rewrite",
		"--old-name=alice", "--new-name=alice-new",
		"--old-email=alice@work.com", "--new-email=new@work.com")
	if code != 0 {
		t.Fatalf("rewrite-author AND matching failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// AND matching: only commits with BOTH alice + alice@work.com should be rewritten
	names := getAuthorNames(t, dir)
	if !containsName(names, "alice-new") {
		t.Errorf("author name 'alice-new' not present (work-email commits should have been rewritten): %v", names)
	}
	if !containsName(names, "alice") {
		t.Errorf("author name 'alice' should still be present (home-email commits should NOT be rewritten): %v", names)
	}

	emails := getAuthorEmails(t, dir)
	if !containsName(emails, "new@work.com") {
		t.Errorf("email 'new@work.com' not present after rewrite: %v", emails)
	}
	if !containsName(emails, "alice@home.com") {
		t.Errorf("email 'alice@home.com' should still be present (home-email commits should NOT be rewritten): %v", emails)
	}
	for _, e := range emails {
		if e == "alice@work.com" {
			t.Errorf("old email 'alice@work.com' still present after rewrite: %v", emails)
			break
		}
	}
}

func TestRewriteAuthorDirtyTreeAlwaysRejected(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "oldname", "old@test.com", 3, "dirty")

	// Create a dirty (untracked) file
	dirtyPath := filepath.Join(dir, "dirty.txt")
	if err := os.WriteFile(dirtyPath, []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}

	// Even with --yes, should fail due to dirty tree (dirty check is unconditional)
	_, stderr, code := runSafegit(t, dir, "--yes", "author", "rewrite", "--old-name=oldname", "--new-name=newname")
	if code == 0 {
		t.Error("rewrite-author with --yes should still fail on dirty tree, but exited 0")
	}
	if !strings.Contains(stderr, "working tree is dirty") {
		t.Errorf("expected dirty-tree error message, got stderr: %s", stderr)
	}
}

func TestRewriteAuthorConfirmAbort(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "oldname", "old@test.com", 3, "abort")

	cmd := exec.Command(safegitBin, "author", "rewrite", "--old-name=oldname", "--new-name=newname")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("n\n")

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("running safegit: %v", err)
		}
	}

	if exitCode != 0 {
		t.Errorf("expected exit code 0 on abort, got %d", exitCode)
	}

	combined := outBuf.String() + errBuf.String()
	if !strings.Contains(combined, "Aborted") {
		t.Errorf("output should contain 'Aborted', got: %s", combined)
	}

	// Nothing was rewritten
	names := getAuthorNames(t, dir)
	if !containsName(names, "oldname") {
		t.Errorf("author name 'oldname' should still be present after abort: %v", names)
	}
}

func TestRewriteAuthorConfirmProceed(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "oldname", "old@test.com", 3, "proceed")

	cmd := exec.Command(safegitBin, "author", "rewrite", "--old-name=oldname", "--new-name=newname")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("y\n")

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("running safegit: %v", err)
		}
	}

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d: stdout=%s stderr=%s", exitCode, outBuf.String(), errBuf.String())
	}

	names := getAuthorNames(t, dir)
	if containsName(names, "oldname") {
		t.Errorf("author name 'oldname' still present after confirm-proceed: %v", names)
	}
}

func TestRewriteAuthorYesSkipsPrompt(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "oldname", "old@test.com", 3, "noprompt")

	cmd := exec.Command(safegitBin, "--yes", "author", "rewrite", "--old-name=oldname", "--new-name=newname")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("")

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("running safegit: %v", err)
		}
	}

	if exitCode != 0 {
		t.Fatalf("expected exit code 0 with --yes, got %d: stdout=%s stderr=%s", exitCode, outBuf.String(), errBuf.String())
	}

	names := getAuthorNames(t, dir)
	if containsName(names, "oldname") {
		t.Errorf("author name 'oldname' still present after --yes rewrite: %v", names)
	}
}

func TestRewriteAuthorHelpFlag(t *testing.T) {
	dir := newRepo(t)

	stdout, stderr, code := runSafegit(t, dir, "author", "rewrite", "--help")
	if code != 0 {
		t.Fatalf("rewrite-author --help failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// strictcli intercepts --help and shows the command description
	combined := stdout + stderr
	if !strings.Contains(combined, "rewrite author and committer name or email across all commit history") {
		t.Errorf("help output should contain command description, got: %s", combined)
	}
}

func TestRewriteAuthorJSON(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "oldname", "old@test.com", 5, "json")

	stdout, stderr, code := runSafegit(t, dir, "--json", "--yes", "author", "rewrite", "--old-name", "oldname", "--new-name", "newname")
	if code != 0 {
		t.Fatalf("rewrite-author --json failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		Version          int               `json:"version"`
		DryRun           bool              `json:"dry_run"`
		Rewrites         map[string]string `json:"rewrites"`
		Tags             []interface{}     `json:"tags"`
		CommitsRewritten int               `json:"commits_rewritten"`
		NameChanged      int               `json:"name_changed"`
		ParentOnly       int               `json:"parent_only"`
		OldHead          string            `json:"old_head"`
		NewHead          string            `json:"new_head"`
		OldName          string            `json:"old_name"`
		NewName          string            `json:"new_name"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nstdout: %s", err, stdout)
	}

	if result.Version != 1 {
		t.Errorf("version: got %d, want 1", result.Version)
	}
	if result.DryRun {
		t.Error("dry_run should be false")
	}
	if len(result.Rewrites) == 0 {
		t.Error("rewrites map should be non-empty")
	}
	for old, new_ := range result.Rewrites {
		if old == new_ {
			t.Errorf("rewrites entry has old == new: %s", old)
		}
	}
	if result.CommitsRewritten == 0 {
		t.Error("commits_rewritten should be > 0")
	}
	if result.NameChanged == 0 {
		t.Error("name_changed should be > 0")
	}
	if result.OldHead == "" {
		t.Error("old_head should not be empty")
	}
	if result.NewHead == "" {
		t.Error("new_head should not be empty")
	}
	if result.OldHead == result.NewHead {
		t.Errorf("old_head should differ from new_head: %s", result.OldHead)
	}
	if result.OldName != "oldname" {
		t.Errorf("old_name: got %q, want %q", result.OldName, "oldname")
	}
	if result.NewName != "newname" {
		t.Errorf("new_name: got %q, want %q", result.NewName, "newname")
	}

	// Verify no oldname in author names (rewrite actually happened)
	names := getAuthorNames(t, dir)
	if containsName(names, "oldname") {
		t.Errorf("author name 'oldname' still present after rewrite: %v", names)
	}
}

func TestRewriteAuthorQuiet(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "oldname", "old@test.com", 3, "quiet")

	stdout, stderr, code := runSafegit(t, dir, "--quiet", "--yes", "author", "rewrite", "--old-name", "oldname", "--new-name", "newname")
	if code != 0 {
		t.Fatalf("rewrite-author --quiet failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	if stdout != "" {
		t.Errorf("expected empty stdout with --quiet, got: %q", stdout)
	}

	// Verify the rewrite actually happened by checking git log author names.
	names := getAuthorNames(t, dir)
	if containsName(names, "oldname") {
		t.Errorf("author name 'oldname' still present after quiet rewrite: %v", names)
	}
	if !containsName(names, "newname") {
		t.Errorf("author name 'newname' not present after quiet rewrite: %v", names)
	}
}

func TestRewriteAuthorJSONDryRun(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "oldname", "old@test.com", 5, "jsondry")

	headBefore := revParseHEAD(t, dir)

	stdout, stderr, code := runSafegit(t, dir, "--json", "--dry-run", "--yes", "author", "rewrite", "--old-name", "oldname", "--new-name", "newname")
	if code != 0 {
		t.Fatalf("rewrite-author --json --dry-run failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		Version        int    `json:"version"`
		DryRun         bool   `json:"dry_run"`
		CommitsToCheck int    `json:"commits_to_check"`
		CommitsMatched int    `json:"commits_matched"`
		OldName        string `json:"old_name"`
		NewName        string `json:"new_name"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nstdout: %s", err, stdout)
	}

	if result.Version != 1 {
		t.Errorf("version: got %d, want 1", result.Version)
	}
	if !result.DryRun {
		t.Error("dry_run should be true")
	}
	if result.CommitsToCheck == 0 {
		t.Error("commits_to_check should be > 0")
	}
	if result.CommitsMatched == 0 {
		t.Error("commits_matched should be > 0")
	}
	if result.OldName != "oldname" {
		t.Errorf("old_name: got %q, want %q", result.OldName, "oldname")
	}
	if result.NewName != "newname" {
		t.Errorf("new_name: got %q, want %q", result.NewName, "newname")
	}

	// HEAD should be unchanged (dry-run)
	headAfter := revParseHEAD(t, dir)
	if headAfter != headBefore {
		t.Errorf("HEAD changed during dry run: %s -> %s", headBefore[:12], headAfter[:12])
	}
}

// makeCommitWithTrailer creates a single commit with the given author identity
// and a commit message that includes the specified trailer line.
func makeCommitWithTrailer(t *testing.T, repoDir string, authorName, authorEmail, prefix string, index int, trailerLine string) {
	t.Helper()
	fname := fmt.Sprintf("%s%d.txt", prefix, index)
	content := fmt.Sprintf("%s%d", prefix, index)
	fpath := filepath.Join(repoDir, fname)
	if err := os.WriteFile(fpath, []byte(content), 0644); err != nil {
		t.Fatalf("writing %s: %v", fname, err)
	}

	cmd := exec.Command("git", "add", fname)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add %s: %v\n%s", fname, err, out)
	}

	msg := fmt.Sprintf("%s commit %d\n\n%s\n", prefix, index, trailerLine)
	cmd = exec.Command("git", "commit", "-m", msg)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+authorName,
		"GIT_AUTHOR_EMAIL="+authorEmail,
		"GIT_COMMITTER_NAME="+authorName,
		"GIT_COMMITTER_EMAIL="+authorEmail,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit %s: %v\n%s", fname, err, out)
	}
}

// getCommitMessageBodies returns full commit messages (%B) from all commits (reverse topo-order).
func getCommitMessageBodies(t *testing.T, repoDir string) []string {
	t.Helper()
	cmd := exec.Command("git", "log", "--all", "--reverse", "--format=%B")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log --format=%%B: %v", err)
	}
	// Split by double-newline between commit messages and trim
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil
	}
	// git log %B separates messages with blank lines, but messages themselves
	// can contain blank lines. Use a different approach: get per-commit.
	cmd = exec.Command("git", "log", "--all", "--reverse", "--format=%x01%B")
	cmd.Dir = repoDir
	out, err = cmd.Output()
	if err != nil {
		t.Fatalf("git log --format=%%x01%%B: %v", err)
	}
	parts := strings.Split(string(out), "\x01")
	var msgs []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		msgs = append(msgs, trimmed)
	}
	return msgs
}

func TestRewriteAuthorTrailerRewrite(t *testing.T) {
	dir := newRepo(t)

	// Create commits with Co-authored-by and Signed-off-by trailers
	makeCommitWithTrailer(t, dir, "oldname", "old@test.com", "trailer", 0,
		"Co-authored-by: Old Name <old@test.com>")
	makeCommitWithTrailer(t, dir, "oldname", "old@test.com", "trailer", 1,
		"Signed-off-by: Old Name <old@test.com>")
	// A commit with a non-matching trailer (should be unchanged)
	makeCommitWithTrailer(t, dir, "oldname", "old@test.com", "trailer", 2,
		"Co-authored-by: Other Person <other@test.com>")

	// Run rewrite with email-only change
	stdout, stderr, code := runSafegit(t, dir, "--yes", "author", "rewrite",
		"--old-email=old@test.com", "--new-email=new@test.com")
	if code != 0 {
		t.Fatalf("rewrite-author failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify author emails are rewritten
	emails := getAuthorEmails(t, dir)
	for _, e := range emails {
		if e == "old@test.com" {
			t.Errorf("old email 'old@test.com' still present in author emails: %v", emails)
			break
		}
	}

	// Verify trailers are rewritten in commit messages
	msgs := getCommitMessageBodies(t, dir)
	foundNewEmail := false
	foundOtherPreserved := false
	for _, m := range msgs {
		if strings.Contains(m, "Co-authored-by: Old Name <new@test.com>") ||
			strings.Contains(m, "Signed-off-by: Old Name <new@test.com>") {
			foundNewEmail = true
		}
		if strings.Contains(m, "<old@test.com>") {
			t.Errorf("old email still present in trailer: %s", m)
		}
		if strings.Contains(m, "Co-authored-by: Other Person <other@test.com>") {
			foundOtherPreserved = true
		}
	}
	if !foundNewEmail {
		t.Errorf("no trailer with new email found in commit messages: %v", msgs)
	}
	if !foundOtherPreserved {
		t.Errorf("non-matching trailer should be preserved: %v", msgs)
	}
}

func TestRewriteAuthorTrailerBothNameAndEmail(t *testing.T) {
	dir := newRepo(t)

	makeCommitWithTrailer(t, dir, "oldname", "old@test.com", "both", 0,
		"Signed-off-by: oldname <old@test.com>")

	stdout, stderr, code := runSafegit(t, dir, "--yes", "author", "rewrite",
		"--old-name=oldname", "--new-name=newname",
		"--old-email=old@test.com", "--new-email=new@test.com")
	if code != 0 {
		t.Fatalf("rewrite-author failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	msgs := getCommitMessageBodies(t, dir)
	found := false
	for _, m := range msgs {
		if strings.Contains(m, "Signed-off-by: newname <new@test.com>") {
			found = true
		}
		if strings.Contains(m, "oldname <old@test.com>") {
			t.Errorf("old identity still present in trailer: %s", m)
		}
	}
	if !found {
		t.Errorf("expected trailer with new identity, got messages: %v", msgs)
	}
}
