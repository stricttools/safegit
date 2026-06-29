package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newRepoWithRemote creates a repo with a bare remote added as "origin".
// Returns (repoDir, bareRemoteDir).
func newRepoWithRemote(t *testing.T) (string, string) {
	t.Helper()
	dir := newRepo(t)

	remoteDir := evalTempDir(t)
	cmd := exec.Command("git", "init", "--bare", "--initial-branch=main", remoteDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "remote", "add", "origin", remoteDir)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	return dir, remoteDir
}

// remoteBranches returns the branch names on a bare remote.
func remoteBranches(t *testing.T, remoteDir string) []string {
	t.Helper()
	cmd := exec.Command("git", "branch")
	cmd.Dir = remoteDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git branch on remote: %v", err)
	}
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "* ")
		if line != "" {
			branches = append(branches, line)
		}
	}
	return branches
}

// remoteTags returns the tag names on a bare remote.
func remoteTags(t *testing.T, remoteDir string) []string {
	t.Helper()
	cmd := exec.Command("git", "tag")
	cmd.Dir = remoteDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git tag on remote: %v", err)
	}
	var tags []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			tags = append(tags, line)
		}
	}
	return tags
}

// sliceContains returns true if the slice contains the given string.
func sliceContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestPushOnlyHead(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)

	// Make a commit so there's something to push beyond the seed
	if err := os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add file1", "--", "file1.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	// Push with --only-head
	_, stderr, code = runSafegit(t, dir, "push", "--only-head", "origin")
	if code != 0 {
		t.Fatalf("push --only-head failed (code %d): %s", code, stderr)
	}

	// Verify main branch exists on remote
	branches := remoteBranches(t, remoteDir)
	if !sliceContains(branches, "main") {
		t.Errorf("remote missing branch 'main'; got branches: %v", branches)
	}

	// Verify the file is in the pushed branch on the remote.
	// Use explicit ref instead of HEAD to avoid CI failures where
	// the bare remote's default branch name differs from "main".
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", "refs/heads/main")
	cmd.Dir = remoteDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-tree on remote: %v", err)
	}
	if !strings.Contains(string(out), "file1.txt") {
		t.Error("remote refs/heads/main missing file1.txt")
	}
}

func TestPushOnlyBranches(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)

	// Create additional branches with commits
	for _, br := range []string{"feature-a", "feature-b"} {
		cmd := exec.Command("git", "branch", br)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git branch %s: %v\n%s", br, err, out)
		}
	}

	// Commit files to each branch via safegit --branch
	if err := os.WriteFile(filepath.Join(dir, "fa.txt"), []byte("feature A\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "fa commit", "--branch", "feature-a", "--", "fa.txt")
	if code != 0 {
		t.Fatalf("commit to feature-a failed (code %d): %s", code, stderr)
	}

	if err := os.WriteFile(filepath.Join(dir, "fb.txt"), []byte("feature B\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "fb commit", "--branch", "feature-b", "--", "fb.txt")
	if code != 0 {
		t.Fatalf("commit to feature-b failed (code %d): %s", code, stderr)
	}

	// Push with --only-branches
	_, stderr, code = runSafegit(t, dir, "push", "--only-branches", "origin")
	if code != 0 {
		t.Fatalf("push --only-branches failed (code %d): %s", code, stderr)
	}

	// Verify all branches exist on remote
	branches := remoteBranches(t, remoteDir)
	for _, expected := range []string{"main", "feature-a", "feature-b"} {
		if !sliceContains(branches, expected) {
			t.Errorf("remote missing branch %q; got branches: %v", expected, branches)
		}
	}
}

func TestPushOnlyTags(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)

	// Create tags
	for _, tag := range []string{"v1.0.0", "v1.1.0"} {
		cmd := exec.Command("git", "tag", tag)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git tag %s: %v\n%s", tag, err, out)
		}
	}

	// Push with --only-tags
	_, stderr, code := runSafegit(t, dir, "push", "--only-tags", "origin")
	if code != 0 {
		t.Fatalf("push --only-tags failed (code %d): %s", code, stderr)
	}

	// Verify tags exist on remote
	tags := remoteTags(t, remoteDir)
	for _, expected := range []string{"v1.0.0", "v1.1.0"} {
		if !sliceContains(tags, expected) {
			t.Errorf("remote missing tag %q; got tags: %v", expected, tags)
		}
	}

	// Verify no branches were pushed (only tags)
	branches := remoteBranches(t, remoteDir)
	if len(branches) > 0 {
		t.Errorf("--only-tags should not push branches; got: %v", branches)
	}
}

func TestPushBothBranchesAndTags(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)

	// Create a branch
	cmd := exec.Command("git", "branch", "feature-x")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v\n%s", err, out)
	}

	// Create tags
	for _, tag := range []string{"v0.1.0", "v0.2.0"} {
		cmd = exec.Command("git", "tag", tag)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git tag %s: %v\n%s", tag, err, out)
		}
	}

	// Push with --both-branches-and-tags
	_, stderr, code := runSafegit(t, dir, "push", "--both-branches-and-tags", "origin")
	if code != 0 {
		t.Fatalf("push --both-branches-and-tags failed (code %d): %s", code, stderr)
	}

	// Verify branches
	branches := remoteBranches(t, remoteDir)
	for _, expected := range []string{"main", "feature-x"} {
		if !sliceContains(branches, expected) {
			t.Errorf("remote missing branch %q; got branches: %v", expected, branches)
		}
	}

	// Verify tags
	tags := remoteTags(t, remoteDir)
	for _, expected := range []string{"v0.1.0", "v0.2.0"} {
		if !sliceContains(tags, expected) {
			t.Errorf("remote missing tag %q; got tags: %v", expected, tags)
		}
	}
}

func TestPushMutexEnforcement(t *testing.T) {
	dir, _ := newRepoWithRemote(t)

	// Two mode flags at once should error
	_, stderr, code := runSafegit(t, dir, "push", "--only-head", "--only-branches", "origin")
	if code == 0 {
		t.Error("expected error when specifying two push mode flags, but got exit 0")
	}
	if !strings.Contains(stderr, "mutex") && !strings.Contains(stderr, "mutually exclusive") && !strings.Contains(stderr, "cannot") {
		// strictcli may use different error wording; just verify it fails
		t.Logf("two-flag error stderr: %s", stderr)
	}

	// No mode flag should error (strictcli mutex with no default requires one)
	_, stderr, code = runSafegit(t, dir, "push", "origin")
	if code == 0 {
		t.Error("expected error when no push mode flag specified, but got exit 0")
	}
}

func TestPushOnlyHeadDetachedErrors(t *testing.T) {
	dir, _ := newRepoWithRemote(t)

	// Detach HEAD
	cmd := exec.Command("git", "checkout", "--detach")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout --detach: %v\n%s", err, out)
	}

	// Push with --only-head should fail with detached HEAD error
	_, stderr, code := runSafegit(t, dir, "push", "--only-head", "origin")
	if code == 0 {
		t.Fatal("expected error pushing with detached HEAD, but got exit 0")
	}
	if !strings.Contains(stderr, "detached") {
		t.Errorf("expected 'detached' in error message; got stderr: %s", stderr)
	}
}
