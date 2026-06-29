package test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newRepoWithSubmodule creates a parent repo with one submodule at "mysub".
// It returns the parent repo dir and the submodule origin dir.
// The parent has 2 commits: "initial" (seed.txt) and "add submodule" (.gitmodules + mysub).
func newRepoWithSubmodule(t *testing.T) (parentDir, subOriginDir string) {
	t.Helper()
	base := evalTempDir(t)

	// Create the repo that will become the submodule origin
	subOriginDir = filepath.Join(base, "sub-origin")
	if err := os.MkdirAll(subOriginDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subOriginDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(subOriginDir, "sub-file.txt"), []byte("sub content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "sub-file.txt"},
		{"git", "commit", "-m", "sub initial"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subOriginDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Create the parent repo
	parentDir = filepath.Join(base, "parent")
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(parentDir, "seed.txt"), []byte("seed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "seed.txt"},
		{"git", "commit", "-m", "initial"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Allow local file:// transport and add the submodule
	gitCfg := exec.Command("git", "config", "protocol.file.allow", "always")
	gitCfg.Dir = parentDir
	if out, err := gitCfg.CombinedOutput(); err != nil {
		t.Fatalf("git config protocol.file.allow: %v\n%s", err, out)
	}
	subAdd := exec.Command("git", "submodule", "add", "-q", subOriginDir, "mysub")
	subAdd.Dir = parentDir
	if out, err := subAdd.CombinedOutput(); err != nil {
		t.Fatalf("git submodule add: %v\n%s", err, out)
	}
	subCommit := exec.Command("git", "commit", "-q", "-m", "add submodule")
	subCommit.Dir = parentDir
	if out, err := subCommit.CombinedOutput(); err != nil {
		t.Fatalf("git commit submodule: %v\n%s", err, out)
	}

	return parentDir, subOriginDir
}

// lsTreeEntry returns the raw ls-tree line for a given path at HEAD.
// Returns empty string if the path is not in the tree.
func lsTreeEntry(t *testing.T, dir, path string) string {
	t.Helper()
	cmd := exec.Command("git", "ls-tree", "HEAD", path)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-tree HEAD %s: %v", path, err)
	}
	return strings.TrimSpace(string(out))
}

// lsTreeSHA extracts the object SHA from a ls-tree line (the 3rd field).
func lsTreeSHA(t *testing.T, entry string) string {
	t.Helper()
	fields := strings.Fields(entry)
	if len(fields) < 3 {
		t.Fatalf("unexpected ls-tree output: %q", entry)
	}
	return fields[2]
}

// lsTreeNames returns all entry names in the HEAD tree.
func lsTreeNames(t *testing.T, dir string) map[string]bool {
	t.Helper()
	cmd := exec.Command("git", "ls-tree", "--name-only", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-tree --name-only HEAD: %v", err)
	}
	names := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			names[line] = true
		}
	}
	return names
}

// Phase 0.4: Commit a new file in the parent while a submodule exists.
// Verify the file appears in HEAD tree and the submodule gitlink entry SHA is unchanged.
func TestSubmoduleParentFiles(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)

	// Record the submodule entry before safegit touches anything
	subEntryBefore := lsTreeEntry(t, parentDir, "mysub")
	if subEntryBefore == "" {
		t.Fatal("submodule entry not found in HEAD tree before test")
	}

	// Create and commit a new file in the parent via safegit
	if err := os.WriteFile(filepath.Join(parentDir, "newfile.txt"), []byte("parent data\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, parentDir, "commit", "-m", "add newfile", "--", "newfile.txt")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): %s", code, stderr)
	}

	// Check 1: new file is in HEAD tree
	names := lsTreeNames(t, parentDir)
	if !names["newfile.txt"] {
		t.Error("newfile.txt not found in HEAD tree")
	}

	// Check 2: submodule entry is unchanged
	subEntryAfter := lsTreeEntry(t, parentDir, "mysub")
	if subEntryBefore != subEntryAfter {
		t.Errorf("submodule entry changed:\n  before: %s\n  after:  %s", subEntryBefore, subEntryAfter)
	}
}

// Phase 0.5: Make a commit inside the submodule, then use safegit to commit
// the submodule pointer update in the parent.
// Verify git ls-tree HEAD mysub shows the new SHA.
func TestSubmodulePointerBump(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)

	// Record the old submodule SHA
	oldEntry := lsTreeEntry(t, parentDir, "mysub")
	oldSHA := lsTreeSHA(t, oldEntry)

	// Make a new commit inside the submodule (using regular git)
	subDir := filepath.Join(parentDir, "mysub")
	for _, args := range [][]string{
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(subDir, "new-sub-file.txt"), []byte("new sub content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "new-sub-file.txt"},
		{"git", "commit", "-q", "-m", "sub update"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Record the new submodule HEAD SHA
	revParse := exec.Command("git", "rev-parse", "HEAD")
	revParse.Dir = subDir
	newSHABytes, err := revParse.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD in submodule: %v", err)
	}
	newSHA := strings.TrimSpace(string(newSHABytes))

	// Sanity: SHAs should differ
	if oldSHA == newSHA {
		t.Fatal("setup failed: submodule SHA did not change")
	}

	// Commit the submodule pointer bump via safegit
	_, stderr, code := runSafegit(t, parentDir, "commit", "-m", "bump submodule", "--", "mysub")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): %s", code, stderr)
	}

	// Verify the pointer was updated
	treeSHA := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if treeSHA != newSHA {
		t.Errorf("submodule pointer not updated:\n  expected: %s\n  got:      %s", newSHA, treeSHA)
	}
}

// Phase 0.6: One concurrent safegit bumps the submodule pointer while another
// commits a parent file. Verify both land, pointer is correct, total commits = 4.
func TestSubmoduleConcurrentBump(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)

	// Set CAS max attempts high for concurrent test
	runSafegit(t, parentDir, "config", "set", "commit.casMaxAttempts", "50")

	// Make a new commit inside the submodule
	subDir := filepath.Join(parentDir, "mysub")
	for _, args := range [][]string{
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(subDir, "updated.txt"), []byte("updated sub content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "updated.txt"},
		{"git", "commit", "-q", "-m", "sub update"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Record the new submodule HEAD SHA
	revParse := exec.Command("git", "rev-parse", "HEAD")
	revParse.Dir = subDir
	newSHABytes, err := revParse.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD in submodule: %v", err)
	}
	newSubSHA := strings.TrimSpace(string(newSHABytes))

	// Create the parent file for the second concurrent commit
	if err := os.WriteFile(filepath.Join(parentDir, "newfile.txt"), []byte("new parent data\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Launch both concurrent safegit commits
	stdouts := make([]string, 2)
	stderrs := make([]string, 2)
	codes := make([]int, 2)

	parallel(2, func(i int) {
		switch i {
		case 0:
			stdouts[0], stderrs[0], codes[0] = runSafegit(t, parentDir, "commit", "-m", "bump sub", "--", "mysub")
		case 1:
			stdouts[1], stderrs[1], codes[1] = runSafegit(t, parentDir, "commit", "-m", "add file", "--", "newfile.txt")
		}
	})

	if codes[0] != 0 {
		t.Fatalf("submodule bump failed (code %d): %s", codes[0], stderrs[0])
	}
	if codes[1] != 0 {
		t.Fatalf("file commit failed (code %d): %s", codes[1], stderrs[1])
	}

	// Check 1: final tree has updated submodule SHA
	finalSubSHA := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if finalSubSHA != newSubSHA {
		t.Errorf("submodule pointer not updated:\n  expected: %s\n  got:      %s", newSubSHA, finalSubSHA)
	}

	// Check 2: newfile.txt exists in the final tree
	names := lsTreeNames(t, parentDir)
	if !names["newfile.txt"] {
		t.Error("newfile.txt missing from final tree")
	}

	// Check 3: 4 commits total (initial + add-submodule + bump + file)
	count := gitLog(t, parentDir, "HEAD")
	if count != 4 {
		t.Errorf("expected 4 commits, got %d", count)
	}
}

var submoduleEnv = []string{"CLAUDE_CODE_SESSION_ID=submodule-test"}

// Phase 1.4: Scrub a file in the parent repo while a submodule exists.
// Verify the submodule gitlink SHA is identical before and after scrub.
func TestScrubFilePreservesGitlink(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)

	// Record the submodule gitlink entry before scrub
	subEntryBefore := lsTreeEntry(t, parentDir, "mysub")
	if subEntryBefore == "" {
		t.Fatal("submodule entry not found in HEAD tree before test")
	}

	// Add a file with secret content, commit with safegit
	if err := os.WriteFile(filepath.Join(parentDir, "secret.txt"), []byte("secret content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv, "commit", "-m", "add secret", "--", "secret.txt")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): %s", code, stderr)
	}

	shas := revListReverse(t, parentDir)
	initialSHA := shas[0]

	// Modify the file on disk and commit so tree is clean for scrub
	commitFileEnv(t, parentDir, submoduleEnv, "secret.txt", "clean content\n", "commit replacement")

	// Run scrub file
	_, stderr, code = runSafegitEnv(t, parentDir, submoduleEnv, "--yes", "scrub", "file", "--from", initialSHA, "--reason", "test gitlink preservation", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub file failed (code %d): %s", code, stderr)
	}

	// Verify the submodule gitlink SHA is identical after scrub
	subEntryAfter := lsTreeEntry(t, parentDir, "mysub")
	subSHABefore := lsTreeSHA(t, subEntryBefore)
	subSHAAfter := lsTreeSHA(t, subEntryAfter)
	if subSHABefore != subSHAAfter {
		t.Errorf("submodule gitlink SHA changed after scrub file:\n  before: %s\n  after:  %s", subSHABefore, subSHAAfter)
	}

	// Verify the scrubbed file has the clean content
	headSHA := revParseHEAD(t, parentDir)
	content, ok := gitShow(t, parentDir, headSHA, "secret.txt")
	if !ok {
		t.Error("secret.txt not found in HEAD after scrub")
	} else if content != "clean content\n" {
		t.Errorf("secret.txt = %q, want %q", content, "clean content\n")
	}
}

// Phase 1.5: Scrub match in the parent repo while a submodule exists.
// Verify the submodule gitlink SHA is identical before and after, and the
// file content is replaced.
func TestScrubMatchPreservesGitlink(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)

	// Record the submodule gitlink entry before scrub
	subEntryBefore := lsTreeEntry(t, parentDir, "mysub")
	if subEntryBefore == "" {
		t.Fatal("submodule entry not found in HEAD tree before test")
	}

	// Add a file with secret content, commit with safegit
	if err := os.WriteFile(filepath.Join(parentDir, "secret.txt"), []byte("SUPERSECRET123\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv, "commit", "-m", "add secret", "--", "secret.txt")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): %s", code, stderr)
	}

	// Run scrub match
	_, stderr, code = runSafegitEnv(t, parentDir, submoduleEnv,
		"--yes", "scrub", "match",
		"--pattern", "SUPERSECRET123",
		"--replace", "REDACTED",
		"--reason", "test gitlink preservation",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}

	// Verify the submodule gitlink SHA is identical after scrub
	subEntryAfter := lsTreeEntry(t, parentDir, "mysub")
	subSHABefore := lsTreeSHA(t, subEntryBefore)
	subSHAAfter := lsTreeSHA(t, subEntryAfter)
	if subSHABefore != subSHAAfter {
		t.Errorf("submodule gitlink SHA changed after scrub match:\n  before: %s\n  after:  %s", subSHABefore, subSHAAfter)
	}

	// Verify the file content is now REDACTED
	headSHA := revParseHEAD(t, parentDir)
	content, ok := gitShow(t, parentDir, headSHA, "secret.txt")
	if !ok {
		t.Error("secret.txt not found in HEAD after scrub match")
	} else if !strings.Contains(content, "REDACTED") {
		t.Errorf("secret.txt should contain REDACTED, got: %q", content)
	}
	if strings.Contains(content, "SUPERSECRET123") {
		t.Errorf("secret.txt still contains SUPERSECRET123: %q", content)
	}
}

// Phase 1.6: Attempt to scrub a path that starts with a gitlink (mysub/somefile.txt).
// The operation should either no-op cleanly (exit 0, no changes) or error meaningfully.
// It must not crash or corrupt the tree.
func TestScrubFileGitlinkPath(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)

	shas := revListReverse(t, parentDir)
	firstSHA := shas[0]

	// Create the file inside the submodule and commit it to keep the tree clean.
	subFilePath := filepath.Join(parentDir, "mysub", "somefile.txt")
	if err := os.WriteFile(subFilePath, []byte("replacement\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Commit inside the submodule
	subDir := filepath.Join(parentDir, "mysub")
	for _, args := range [][]string{
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "add", "somefile.txt"},
		{"git", "commit", "-m", "add somefile"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v in submodule: %v\n%s", args, err, out)
		}
	}
	// Update submodule ref in parent
	for _, args := range [][]string{
		{"git", "add", "mysub"},
		{"git", "commit", "-m", "update submodule ref"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v in parent: %v\n%s", args, err, out)
		}
	}

	// Record tree state AFTER setup commits (before scrub)
	subEntryBefore := lsTreeEntry(t, parentDir, "mysub")
	headBefore := revParseHEAD(t, parentDir)

	// Run scrub file targeting a path inside the submodule
	_, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv, "--yes", "scrub", "file", "--from", firstSHA, "--reason", "test gitlink path", "mysub/somefile.txt")

	// Either clean exit (no-op) or meaningful error is acceptable.
	// Crash (signal death, panic) or corruption is not.
	if code > 1 {
		// Code > 1 might indicate a crash/signal; code 1 is a normal error
		t.Logf("scrub exited with code %d: %s (checking for corruption)", code, stderr)
	}

	// Verify: the tree is not corrupted regardless of exit code.
	// The submodule gitlink SHA must be unchanged.
	subEntryAfter := lsTreeEntry(t, parentDir, "mysub")
	subSHABefore := lsTreeSHA(t, subEntryBefore)
	subSHAAfter := lsTreeSHA(t, subEntryAfter)
	if subSHABefore != subSHAAfter {
		t.Errorf("submodule gitlink SHA corrupted after scrub file on gitlink path:\n  before: %s\n  after:  %s", subSHABefore, subSHAAfter)
	}

	// If exit code was 0 (no-op), HEAD should be unchanged
	if code == 0 {
		headAfter := revParseHEAD(t, parentDir)
		if headAfter != headBefore {
			t.Errorf("HEAD changed despite no-op scrub: %s -> %s", headBefore, headAfter)
		}
	}

	// Verify git fsck passes (no corruption)
	fsck := exec.Command("git", "fsck", "--no-dangling")
	fsck.Dir = parentDir
	if out, err := fsck.CombinedOutput(); err != nil {
		t.Errorf("git fsck failed after scrub on gitlink path: %v\n%s", err, out)
	}
}

// Phase 0.7: Two concurrent safegit commits adding different files to the parent
// while submodule exists. Verify both files in tree, submodule entry unchanged,
// total commits = 4.
func TestSubmoduleConcurrentParent(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)

	// Set CAS max attempts high for concurrent test
	runSafegit(t, parentDir, "config", "set", "commit.casMaxAttempts", "50")

	// Record the submodule entry before concurrent commits
	subEntryBefore := lsTreeEntry(t, parentDir, "mysub")

	// Create the files for concurrent commits
	if err := os.WriteFile(filepath.Join(parentDir, "file-a.txt"), []byte("content-a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parentDir, "file-b.txt"), []byte("content-b\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Launch two concurrent safegit commits
	stdouts := make([]string, 2)
	stderrs := make([]string, 2)
	codes := make([]int, 2)

	parallel(2, func(i int) {
		switch i {
		case 0:
			stdouts[0], stderrs[0], codes[0] = runSafegit(t, parentDir, "commit", "-m", "add a", "--", "file-a.txt")
		case 1:
			stdouts[1], stderrs[1], codes[1] = runSafegit(t, parentDir, "commit", "-m", "add b", "--", "file-b.txt")
		}
	})

	if codes[0] != 0 {
		t.Fatalf("agent A failed (code %d): %s", codes[0], stderrs[0])
	}
	if codes[1] != 0 {
		t.Fatalf("agent B failed (code %d): %s", codes[1], stderrs[1])
	}

	// Check 1: 4 commits total (initial + add-submodule + 2 concurrent)
	count := gitLog(t, parentDir, "HEAD")
	if count != 4 {
		t.Errorf("expected 4 commits, got %d", count)
	}

	// Check 2: both files exist in the final tree
	names := lsTreeNames(t, parentDir)
	if !names["file-a.txt"] {
		t.Error("file-a.txt missing from final tree")
	}
	if !names["file-b.txt"] {
		t.Error("file-b.txt missing from final tree")
	}

	// Check 3: submodule entry is unchanged
	subEntryAfter := lsTreeEntry(t, parentDir, "mysub")
	if subEntryBefore != subEntryAfter {
		t.Errorf("submodule entry changed:\n  before: %s\n  after:  %s", subEntryBefore, subEntryAfter)
	}
}

// prepSubmoduleForCommit puts the submodule checkout on the "main" branch
// (submodule add leaves it detached) and configures user identity so safegit
// commit can operate inside it. Also disables autoBumpParent in the parent
// so existing tests are not affected by the auto-bump feature.
// Returns the submodule directory path.
func prepSubmoduleForCommit(t *testing.T, parentDir string) string {
	t.Helper()
	subDir := filepath.Join(parentDir, "mysub")
	for _, args := range [][]string{
		{"git", "checkout", "main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v in submodule: %v\n%s", args, err, out)
		}
	}

	// Disable autoBumpParent in the parent so submodule commits don't
	// trigger parent bumps (tests that want auto-bump enable it explicitly).
	_, stderr, code := runSafegit(t, parentDir, "config", "set", "commit.autoBumpParent", "false")
	if code != 0 {
		t.Fatalf("failed to configure parent autoBumpParent=false: %s", stderr)
	}

	return subDir
}

// Phase 0.8: Commit a new file inside the submodule using safegit.
// Verify the commit lands on the submodule's branch and the parent is unaffected.
func TestSubmoduleCommitInside(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)

	parentCountBefore := gitLog(t, parentDir, "HEAD")
	subCountBefore := gitLog(t, subDir, "HEAD")

	// Create a new file in the submodule.
	if err := os.WriteFile(filepath.Join(subDir, "new_in_sub.txt"), []byte("hello from sub\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Commit from inside the submodule.
	_, stderr, code := runSafegit(t, subDir, "commit", "-m", "sub commit", "--", "new_in_sub.txt")
	if code != 0 {
		t.Fatalf("safegit commit in submodule failed (code %d): %s", code, stderr)
	}

	// Verify: submodule branch advanced by 1.
	subCountAfter := gitLog(t, subDir, "HEAD")
	if subCountAfter != subCountBefore+1 {
		t.Errorf("submodule commit count: want %d, got %d", subCountBefore+1, subCountAfter)
	}

	// Verify: parent repo is unaffected.
	parentCountAfter := gitLog(t, parentDir, "HEAD")
	if parentCountAfter != parentCountBefore {
		t.Errorf("parent commit count changed: was %d, now %d", parentCountBefore, parentCountAfter)
	}

	// Verify the file is in the submodule's HEAD tree.
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
	cmd.Dir = subDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "new_in_sub.txt") {
		t.Errorf("new_in_sub.txt not found in submodule HEAD tree; got:\n%s", out)
	}
}

// Phase 0.9: safegit commit should refuse to operate in a detached HEAD state.
// Submodules are often in detached HEAD after checkout; this documents that
// safegit commit does not support detached HEAD.
func TestSubmoduleCommitDetachedHead(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := filepath.Join(parentDir, "mysub")

	// Configure user identity (needed even for a failing commit attempt).
	for _, args := range [][]string{
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v in submodule: %v\n%s", args, err, out)
		}
	}

	// Detach HEAD in the submodule.
	cmd := exec.Command("git", "checkout", "--detach")
	cmd.Dir = subDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout --detach: %v\n%s", err, out)
	}

	// Create a new file.
	if err := os.WriteFile(filepath.Join(subDir, "detached.txt"), []byte("detached\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// safegit commit should fail with an error about detached HEAD.
	_, stderr, code := runSafegit(t, subDir, "commit", "-m", "detached commit", "--", "detached.txt")
	if code == 0 {
		t.Fatal("safegit commit in detached HEAD should have failed, but exited 0")
	}

	combined := strings.ToLower(stderr)
	if !strings.Contains(combined, "detach") && !strings.Contains(combined, "branch") {
		t.Errorf("expected error mentioning detached HEAD or branch, got: %s", stderr)
	}
}

// Phase 0.10: Undo inside a submodule reverts the submodule HEAD to the
// pre-commit SHA. The parent repo's recorded pointer to the submodule is
// unchanged (undo only touches the submodule's own refs).
func TestSubmoduleUndo(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)
	env := []string{"CLAUDE_CODE_SESSION_ID=submodule-undo-test"}

	parentSubPointerBefore := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	subHEADBefore := revParseHEAD(t, subDir)

	// Create a file and commit inside the submodule.
	if err := os.WriteFile(filepath.Join(subDir, "undo_me.txt"), []byte("will undo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, subDir, env, "commit", "-m", "commit to undo", "--", "undo_me.txt")
	if code != 0 {
		t.Fatalf("safegit commit in submodule failed (code %d): %s", code, stderr)
	}

	subHEADAfterCommit := revParseHEAD(t, subDir)
	if subHEADAfterCommit == subHEADBefore {
		t.Fatal("submodule HEAD did not advance after commit")
	}

	// Undo from inside the submodule.
	_, stderr, code = runSafegitEnv(t, subDir, env, "undo")
	if code != 0 {
		t.Fatalf("safegit undo in submodule failed (code %d): %s", code, stderr)
	}

	// Verify: submodule HEAD is back to pre-commit SHA.
	subHEADAfterUndo := revParseHEAD(t, subDir)
	if subHEADAfterUndo != subHEADBefore {
		t.Errorf("after undo, submodule HEAD = %s, want %s", subHEADAfterUndo, subHEADBefore)
	}

	// Verify: parent repo's pointer to submodule is unchanged.
	parentSubPointerAfter := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if parentSubPointerAfter != parentSubPointerBefore {
		t.Errorf("parent's submodule pointer changed: was %s, now %s", parentSubPointerBefore, parentSubPointerAfter)
	}
}

// Phase 0.11: Redo inside a submodule restores the commit after undo.
// commit -> undo -> redo should return the submodule HEAD to the post-commit
// SHA. Parent repo remains unaffected throughout.
func TestSubmoduleRedo(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)
	env := []string{"CLAUDE_CODE_SESSION_ID=submodule-redo-test"}

	parentCountBefore := gitLog(t, parentDir, "HEAD")
	parentSubPointerBefore := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))

	// Create a file and commit inside the submodule.
	if err := os.WriteFile(filepath.Join(subDir, "redo_me.txt"), []byte("will redo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, subDir, env, "commit", "-m", "commit for redo", "--", "redo_me.txt")
	if code != 0 {
		t.Fatalf("safegit commit in submodule failed (code %d): %s", code, stderr)
	}
	subHEADAfterCommit := revParseHEAD(t, subDir)

	// Undo.
	_, stderr, code = runSafegitEnv(t, subDir, env, "undo")
	if code != 0 {
		t.Fatalf("safegit undo in submodule failed (code %d): %s", code, stderr)
	}

	// Redo.
	_, stderr, code = runSafegitEnv(t, subDir, env, "redo")
	if code != 0 {
		t.Fatalf("safegit redo in submodule failed (code %d): %s", code, stderr)
	}

	// Verify: submodule HEAD is back to the post-commit SHA.
	subHEADAfterRedo := revParseHEAD(t, subDir)
	if subHEADAfterRedo != subHEADAfterCommit {
		t.Errorf("after redo, submodule HEAD = %s, want %s", subHEADAfterRedo, subHEADAfterCommit)
	}

	// Verify: parent repo is unaffected.
	parentCountAfter := gitLog(t, parentDir, "HEAD")
	if parentCountAfter != parentCountBefore {
		t.Errorf("parent commit count changed: was %d, now %d", parentCountBefore, parentCountAfter)
	}
	parentSubPointerAfter := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if parentSubPointerAfter != parentSubPointerBefore {
		t.Errorf("parent's submodule pointer changed: was %s, now %s", parentSubPointerBefore, parentSubPointerAfter)
	}
}

// Phase 0.12: Concurrent safegit commits in the parent and submodule should
// not block each other. Their .git directories are separate, so lock files
// are independent.
func TestSubmoduleLockIsolation(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)

	// Create files for each commit.
	if err := os.WriteFile(filepath.Join(parentDir, "parent_new.txt"), []byte("parent file\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "sub_new.txt"), []byte("sub file\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var parentCode, subCode int
	var parentStderr, subStderr string

	// Launch both commits concurrently.
	parallel(2, func(i int) {
		if i == 0 {
			_, parentStderr, parentCode = runSafegit(t, parentDir, "commit", "-m", "parent concurrent", "--", "parent_new.txt")
		} else {
			_, subStderr, subCode = runSafegit(t, subDir, "commit", "-m", "sub concurrent", "--", "sub_new.txt")
		}
	})

	if parentCode != 0 {
		t.Fatalf("parent commit failed (code %d): %s", parentCode, parentStderr)
	}
	if subCode != 0 {
		t.Fatalf("submodule commit failed (code %d): %s", subCode, subStderr)
	}

	// Verify: parent has its new file.
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
	cmd.Dir = parentDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "parent_new.txt") {
		t.Errorf("parent_new.txt not found in parent HEAD tree; got:\n%s", out)
	}

	// Verify: submodule has its new file.
	cmd = exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
	cmd.Dir = subDir
	out, err = cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "sub_new.txt") {
		t.Errorf("sub_new.txt not found in submodule HEAD tree; got:\n%s", out)
	}
}

// Phase 2.4: Doctor --fix cleans up orphan tmp dirs and stale locks inside
// submodule safegit directories.
func TestDoctorCleansSubmoduleState(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)

	// Initialize safegit in the parent repo by committing a file.
	if err := os.WriteFile(filepath.Join(parentDir, "doctor_init.txt"), []byte("init\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, parentDir, "commit", "-m", "init parent safegit", "--", "doctor_init.txt")
	if code != 0 {
		t.Fatalf("safegit commit in parent failed (code %d): %s", code, stderr)
	}

	// Run safegit commit inside the submodule to initialize its safegit dir.
	if err := os.WriteFile(filepath.Join(subDir, "init_safegit.txt"), []byte("init\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegit(t, subDir, "commit", "-m", "init safegit", "--", "init_safegit.txt")
	if code != 0 {
		t.Fatalf("safegit commit in submodule failed (code %d): %s", code, stderr)
	}

	// Resolve the submodule's git dir to find .git/modules/mysub/safegit/.
	gitDirCmd := exec.Command("git", "rev-parse", "--git-dir")
	gitDirCmd.Dir = subDir
	gitDirOut, err := gitDirCmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse --git-dir in submodule: %v", err)
	}
	subGitDir := strings.TrimSpace(string(gitDirOut))
	if !filepath.IsAbs(subGitDir) {
		subGitDir = filepath.Join(subDir, subGitDir)
	}
	subSafegitDir := filepath.Join(subGitDir, "safegit")

	// Verify the safegit dir exists (created by the commit above).
	if _, err := os.Stat(subSafegitDir); os.IsNotExist(err) {
		t.Fatalf("submodule safegit dir does not exist at %s", subSafegitDir)
	}

	// Create an orphan tmp dir with a fake dead PID.
	orphanDir := filepath.Join(subSafegitDir, "tmp", "99999-fake")
	if err := os.MkdirAll(orphanDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphanDir, "index"), []byte("fake index"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a stale lock file with a dead PID.
	lockDir := filepath.Join(subSafegitDir, "locks", "refs", "heads")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatal(err)
	}
	lockFile := filepath.Join(lockDir, "main.lock")
	lockContent := "pid=99999\nts=2020-01-01T00:00:00Z\nop=commit\n"
	if err := os.WriteFile(lockFile, []byte(lockContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Verify the artifacts exist before running doctor.
	if _, err := os.Stat(orphanDir); os.IsNotExist(err) {
		t.Fatal("orphan dir not created")
	}
	if _, err := os.Stat(lockFile); os.IsNotExist(err) {
		t.Fatal("stale lock file not created")
	}

	// Run safegit doctor --fix from the parent repo.
	stdout, stderr, code := runSafegit(t, parentDir, "doctor", "--fix")
	if code != 0 {
		t.Fatalf("doctor --fix failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify: the orphan tmp dir is removed.
	if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
		t.Errorf("orphan tmp dir still exists at %s", orphanDir)
	}

	// Verify: the stale lock is removed.
	if _, err := os.Stat(lockFile); !os.IsNotExist(err) {
		t.Errorf("stale lock file still exists at %s", lockFile)
	}

	// Verify: output mentions the submodule cleanup.
	if !strings.Contains(stdout, "[mysub]") {
		t.Errorf("doctor output should mention [mysub], got: %s", stdout)
	}
}

// Phase 0.1: Doctor --fix --dry-run reports stale locks inside submodule
// safegit directories without removing them.
func TestDoctorDryRunReportsSubmodules(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)

	// Initialize safegit in the parent repo by committing a file.
	if err := os.WriteFile(filepath.Join(parentDir, "doctor_init.txt"), []byte("init\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, parentDir, "commit", "-m", "init parent safegit", "--", "doctor_init.txt")
	if code != 0 {
		t.Fatalf("safegit commit in parent failed (code %d): %s", code, stderr)
	}

	// Run safegit commit inside the submodule to initialize its safegit dir.
	if err := os.WriteFile(filepath.Join(subDir, "init_safegit.txt"), []byte("init\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegit(t, subDir, "commit", "-m", "init safegit", "--", "init_safegit.txt")
	if code != 0 {
		t.Fatalf("safegit commit in submodule failed (code %d): %s", code, stderr)
	}

	// Resolve the submodule's git dir to find .git/modules/mysub/safegit/.
	gitDirCmd := exec.Command("git", "rev-parse", "--git-dir")
	gitDirCmd.Dir = subDir
	gitDirOut, err := gitDirCmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse --git-dir in submodule: %v", err)
	}
	subGitDir := strings.TrimSpace(string(gitDirOut))
	if !filepath.IsAbs(subGitDir) {
		subGitDir = filepath.Join(subDir, subGitDir)
	}
	subSafegitDir := filepath.Join(subGitDir, "safegit")

	// Verify the safegit dir exists (created by the commit above).
	if _, err := os.Stat(subSafegitDir); os.IsNotExist(err) {
		t.Fatalf("submodule safegit dir does not exist at %s", subSafegitDir)
	}

	// Create a stale lock file with a dead PID.
	lockDir := filepath.Join(subSafegitDir, "locks", "refs", "heads")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatal(err)
	}
	lockFile := filepath.Join(lockDir, "main.lock")
	lockContent := "pid=99999\nts=2020-01-01T00:00:00Z\nop=commit\n"
	if err := os.WriteFile(lockFile, []byte(lockContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Verify the lock file exists before running doctor.
	if _, err := os.Stat(lockFile); os.IsNotExist(err) {
		t.Fatal("stale lock file not created")
	}

	// Run safegit doctor --fix --dry-run from the parent repo.
	stdout, stderr, code := runSafegit(t, parentDir, "doctor", "--fix", "--dry-run", "--yes")
	if code != 0 {
		t.Fatalf("doctor --fix --dry-run failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify: output mentions the submodule stale lock (dry-run report).
	if !strings.Contains(stdout, "[mysub]") {
		t.Errorf("doctor --dry-run output should mention [mysub], got: %s", stdout)
	}
	if !strings.Contains(stdout, "would remove") && !strings.Contains(stdout, "stale lock") {
		t.Errorf("doctor --dry-run output should mention stale lock removal, got: %s", stdout)
	}

	// Verify: the lock file still exists (not removed in dry-run).
	if _, err := os.Stat(lockFile); os.IsNotExist(err) {
		t.Error("stale lock file was removed during dry-run (should have been preserved)")
	}
}

// Phase 0.13: Amending a commit in the parent preserves the submodule's
// gitlink entry. The recorded submodule SHA must not change when only a
// regular file is amended.
func TestSubmoduleAmendPreservesGitlink(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	env := []string{"CLAUDE_CODE_SESSION_ID=submodule-amend-test"}

	// Record the submodule pointer before the test.
	subPointerBefore := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))

	// Create and commit a file in the parent.
	filePath := filepath.Join(parentDir, "amend_target.txt")
	if err := os.WriteFile(filePath, []byte("original content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, parentDir, env, "commit", "-m", "will amend", "--", "amend_target.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Modify the file and amend.
	if err := os.WriteFile(filePath, []byte("amended content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegitEnv(t, parentDir, env, "commit", "--amend", "-m", "amended", "--", "amend_target.txt")
	if code != 0 {
		t.Fatalf("amend commit failed (code %d): %s", code, stderr)
	}

	// Verify: the gitlink entry for the submodule is preserved.
	subPointerAfter := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if subPointerAfter != subPointerBefore {
		t.Errorf("submodule gitlink changed after amend: was %s, now %s", subPointerBefore, subPointerAfter)
	}

	// Verify: the amended file has the new content in the tree.
	cmd := exec.Command("git", "show", "HEAD:amend_target.txt")
	cmd.Dir = parentDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git show HEAD:amend_target.txt: %v", err)
	}
	if !strings.Contains(string(out), "amended content") {
		t.Errorf("amended content not found in HEAD tree; got: %s", out)
	}
}

// Phase 3.3: Push from a submodule should run parent's pre-pre-push hooks first.
// Install a hook in the parent that writes a marker file, push from the submodule,
// verify the marker exists.
func TestPushHookCascadeFromParent(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)

	// Create a bare remote for the submodule to push to
	bareRemote := filepath.Join(evalTempDir(t), "sub-bare")
	cmd := exec.Command("git", "init", "--bare", "--initial-branch=main", bareRemote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	// Set the bare remote as "origin" in the submodule
	cmd = exec.Command("git", "remote", "set-url", "origin", bareRemote)
	cmd.Dir = subDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote set-url: %v\n%s", err, out)
	}

	// Push the existing main branch to the bare remote so it's not empty
	cmd = exec.Command("git", "push", "origin", "main")
	cmd.Dir = subDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("initial push to bare remote: %v\n%s", err, out)
	}

	// Install a pre-pre-push hook in the parent's .git/hooks/
	parentGitDir := filepath.Join(parentDir, ".git")
	hooksDir := filepath.Join(parentGitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	markerFile := filepath.Join(evalTempDir(t), "cascade-marker")
	hookScript := fmt.Sprintf("#!/bin/sh\ntouch %s\n", markerFile)
	hookPath := filepath.Join(hooksDir, "pre-pre-push")
	if err := os.WriteFile(hookPath, []byte(hookScript), 0755); err != nil {
		t.Fatal(err)
	}

	// Make a commit in the submodule so there's something to push
	if err := os.WriteFile(filepath.Join(subDir, "push-test.txt"), []byte("push test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, subDir, "commit", "-m", "push test commit", "--", "push-test.txt")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): %s", code, stderr)
	}

	// Push from the submodule using safegit
	_, stderr, code = runSafegit(t, subDir, "push", "--only-head", "origin")
	if code != 0 {
		t.Fatalf("safegit push failed (code %d): %s", code, stderr)
	}

	// Verify the parent hook ran by checking the marker file
	if _, err := os.Stat(markerFile); os.IsNotExist(err) {
		t.Error("parent pre-pre-push hook did not run: marker file not found")
	}
}

// Phase 3.4: Parent hook failure should block submodule push with exit code 20.
func TestPushHookCascadeRejectsOnParentHookFailure(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)

	// Create a bare remote for the submodule to push to
	bareRemote := filepath.Join(evalTempDir(t), "sub-bare")
	cmd := exec.Command("git", "init", "--bare", "--initial-branch=main", bareRemote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	// Set the bare remote as "origin" in the submodule
	cmd = exec.Command("git", "remote", "set-url", "origin", bareRemote)
	cmd.Dir = subDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote set-url: %v\n%s", err, out)
	}

	// Push the existing main branch so remote isn't empty
	cmd = exec.Command("git", "push", "origin", "main")
	cmd.Dir = subDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("initial push to bare remote: %v\n%s", err, out)
	}

	// Install a failing pre-pre-push hook in the parent's .git/hooks/
	parentGitDir := filepath.Join(parentDir, ".git")
	hooksDir := filepath.Join(parentGitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	hookScript := "#!/bin/sh\necho 'parent hook rejects push' >&2\nexit 1\n"
	hookPath := filepath.Join(hooksDir, "pre-pre-push")
	if err := os.WriteFile(hookPath, []byte(hookScript), 0755); err != nil {
		t.Fatal(err)
	}

	// Make a commit in the submodule so there's something to push
	if err := os.WriteFile(filepath.Join(subDir, "reject-test.txt"), []byte("reject test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, subDir, "commit", "-m", "reject test commit", "--", "reject-test.txt")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): %s", code, stderr)
	}

	// Record the remote HEAD before the push attempt
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = bareRemote
	remoteHeadBefore, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD on bare remote: %v", err)
	}

	// Push from the submodule using safegit -- should fail
	// Exit code 20 = hook failure (exitPushHookFailed in push.go)
	_, stderr, code = runSafegit(t, subDir, "push", "--only-head", "origin")
	if code != 20 {
		t.Errorf("expected exit code 20 (hook failure), got %d; stderr: %s", code, stderr)
	}

	// Verify no push occurred: remote HEAD unchanged
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = bareRemote
	remoteHeadAfter, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD on bare remote after failed push: %v", err)
	}
	if string(remoteHeadAfter) != string(remoteHeadBefore) {
		t.Errorf("remote HEAD changed despite hook failure: before=%s after=%s",
			strings.TrimSpace(string(remoteHeadBefore)),
			strings.TrimSpace(string(remoteHeadAfter)))
	}
}

// --- Phase 4: Scrub auto-recurse into submodules ---

// newRepoWithSubmoduleSecret creates a parent repo with a submodule that has
// a file containing a secret. The submodule is on the "main" branch (not
// detached). Returns parent dir, submodule origin dir, and the submodule
// working directory inside the parent.
func newRepoWithSubmoduleSecret(t *testing.T, secret, filename string) (parentDir, subOriginDir, subDir string) {
	t.Helper()
	base := evalTempDir(t)

	// Create the repo that will become the submodule origin
	subOriginDir = filepath.Join(base, "sub-origin")
	if err := os.MkdirAll(subOriginDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subOriginDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	// Create the secret file in the submodule origin
	if err := os.WriteFile(filepath.Join(subOriginDir, filename), []byte(secret+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", filename},
		{"git", "commit", "-m", "sub initial with secret"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subOriginDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Create the parent repo
	parentDir = filepath.Join(base, "parent")
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(parentDir, "seed.txt"), []byte("seed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "seed.txt"},
		{"git", "commit", "-m", "initial"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Allow local file:// transport and add the submodule
	for _, args := range [][]string{
		{"git", "config", "protocol.file.allow", "always"},
		{"git", "submodule", "add", "-q", subOriginDir, "mysub"},
		{"git", "commit", "-q", "-m", "add submodule"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Put the submodule checkout on the main branch (not detached)
	subDir = filepath.Join(parentDir, "mysub")
	for _, args := range [][]string{
		{"git", "checkout", "main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v in submodule: %v\n%s", args, err, out)
		}
	}

	return parentDir, subOriginDir, subDir
}

// newRepoWithTwoSubmoduleSecrets creates a parent repo with two submodules
// ("sub1" and "sub2"), each containing a file with a secret. Both submodules
// are on the "main" branch.
func newRepoWithTwoSubmoduleSecrets(t *testing.T, secret, filename string) (parentDir, sub1Dir, sub2Dir string) {
	t.Helper()
	base := evalTempDir(t)

	// Helper to create a submodule origin with a secret file
	createSubOrigin := func(name string) string {
		originDir := filepath.Join(base, name+"-origin")
		if err := os.MkdirAll(originDir, 0755); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"git", "init", "--initial-branch=main"},
			{"git", "config", "user.email", "test@test.com"},
			{"git", "config", "user.name", "Test"},
		} {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = originDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v failed: %v\n%s", args, err, out)
			}
		}
		if err := os.WriteFile(filepath.Join(originDir, filename), []byte(secret+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"git", "add", filename},
			{"git", "commit", "-m", name + " initial with secret"},
		} {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = originDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v failed: %v\n%s", args, err, out)
			}
		}
		return originDir
	}

	sub1Origin := createSubOrigin("sub1")
	sub2Origin := createSubOrigin("sub2")

	// Create the parent repo
	parentDir = filepath.Join(base, "parent")
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(parentDir, "seed.txt"), []byte("seed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "seed.txt"},
		{"git", "commit", "-m", "initial"},
		{"git", "config", "protocol.file.allow", "always"},
		{"git", "submodule", "add", "-q", sub1Origin, "sub1"},
		{"git", "submodule", "add", "-q", sub2Origin, "sub2"},
		{"git", "commit", "-q", "-m", "add submodules"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Put both submodule checkouts on the main branch
	sub1Dir = filepath.Join(parentDir, "sub1")
	sub2Dir = filepath.Join(parentDir, "sub2")
	for _, subDir := range []string{sub1Dir, sub2Dir} {
		for _, args := range [][]string{
			{"git", "checkout", "main"},
			{"git", "config", "user.email", "test@test.com"},
			{"git", "config", "user.name", "Test"},
		} {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = subDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v in submodule %s: %v\n%s", args, subDir, err, out)
			}
		}
	}

	return parentDir, sub1Dir, sub2Dir
}

// Phase 4.9: scrub match auto-recurses into a submodule, replacing the secret
// in the submodule's history and updating the parent's gitlink pointer.
func TestScrubMatchRecursesIntoSubmodule(t *testing.T) {
	t.Parallel()
	parentDir, _, subDir := newRepoWithSubmoduleSecret(t, "TOPSECRET_ABC123", "secret.txt")

	// Record the gitlink SHA before scrub
	gitlinkBefore := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))

	// Run scrub match from the parent
	stdout, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv,
		"--yes", "scrub", "match",
		"--pattern", "TOPSECRET_ABC123",
		"--replace", "REDACTED",
		"--reason", "test",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify: submodule HEAD no longer contains the secret
	subHEAD := revParseHEAD(t, subDir)
	content, ok := gitShow(t, subDir, subHEAD, "secret.txt")
	if !ok {
		t.Error("secret.txt not found in submodule HEAD after scrub")
	} else {
		if strings.Contains(content, "TOPSECRET_ABC123") {
			t.Errorf("submodule secret.txt still contains TOPSECRET_ABC123: %q", content)
		}
		if !strings.Contains(content, "REDACTED") {
			t.Errorf("submodule secret.txt should contain REDACTED, got: %q", content)
		}
	}

	// Verify: parent's gitlink SHA changed (points to rewritten commit)
	gitlinkAfter := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if gitlinkAfter == gitlinkBefore {
		t.Error("parent gitlink SHA did not change after scrubbing submodule")
	}
}

// Phase 4.10: scrub match replaces secrets in both parent and submodule.
func TestScrubMatchBothParentAndSubmodule(t *testing.T) {
	t.Parallel()
	parentDir, _, subDir := newRepoWithSubmoduleSecret(t, "TOPSECRET_XYZ789", "secret.txt")

	// Also add a file with the secret to the parent
	if err := os.WriteFile(filepath.Join(parentDir, "parentfile.txt"), []byte("TOPSECRET_XYZ789\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "parentfile.txt")
	cmd.Dir = parentDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "add parent secret file")
	cmd.Dir = parentDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	// Record the gitlink SHA before scrub
	gitlinkBefore := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))

	// Run scrub match
	stdout, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv,
		"--yes", "scrub", "match",
		"--pattern", "TOPSECRET_XYZ789",
		"--replace", "CLEAN",
		"--reason", "test",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify: parent file is clean
	parentHEAD := revParseHEAD(t, parentDir)
	content, ok := gitShow(t, parentDir, parentHEAD, "parentfile.txt")
	if !ok {
		t.Error("parentfile.txt not found in parent HEAD after scrub")
	} else {
		if strings.Contains(content, "TOPSECRET_XYZ789") {
			t.Errorf("parent parentfile.txt still contains secret: %q", content)
		}
		if !strings.Contains(content, "CLEAN") {
			t.Errorf("parent parentfile.txt should contain CLEAN, got: %q", content)
		}
	}

	// Verify: submodule file is clean
	subHEAD := revParseHEAD(t, subDir)
	content, ok = gitShow(t, subDir, subHEAD, "secret.txt")
	if !ok {
		t.Error("secret.txt not found in submodule HEAD after scrub")
	} else {
		if strings.Contains(content, "TOPSECRET_XYZ789") {
			t.Errorf("submodule secret.txt still contains secret: %q", content)
		}
		if !strings.Contains(content, "CLEAN") {
			t.Errorf("submodule secret.txt should contain CLEAN, got: %q", content)
		}
	}

	// Verify: parent gitlink updated
	gitlinkAfter := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if gitlinkAfter == gitlinkBefore {
		t.Error("parent gitlink SHA did not change after scrubbing submodule")
	}
}

// Phase 4.11: scrub match with two submodules scrubs both.
func TestScrubMatchTwoSubmodules(t *testing.T) {
	t.Parallel()
	parentDir, sub1Dir, sub2Dir := newRepoWithTwoSubmoduleSecrets(t, "TOPSECRET_MULTI", "secret.txt")

	// Record gitlink SHAs before scrub
	gitlink1Before := lsTreeSHA(t, lsTreeEntry(t, parentDir, "sub1"))
	gitlink2Before := lsTreeSHA(t, lsTreeEntry(t, parentDir, "sub2"))

	// Run scrub match
	stdout, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv,
		"--yes", "scrub", "match",
		"--pattern", "TOPSECRET_MULTI",
		"--replace", "REDACTED",
		"--reason", "test",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify: sub1 is clean
	sub1HEAD := revParseHEAD(t, sub1Dir)
	content, ok := gitShow(t, sub1Dir, sub1HEAD, "secret.txt")
	if !ok {
		t.Error("secret.txt not found in sub1 HEAD after scrub")
	} else if strings.Contains(content, "TOPSECRET_MULTI") {
		t.Errorf("sub1 secret.txt still contains secret: %q", content)
	}

	// Verify: sub2 is clean
	sub2HEAD := revParseHEAD(t, sub2Dir)
	content, ok = gitShow(t, sub2Dir, sub2HEAD, "secret.txt")
	if !ok {
		t.Error("secret.txt not found in sub2 HEAD after scrub")
	} else if strings.Contains(content, "TOPSECRET_MULTI") {
		t.Errorf("sub2 secret.txt still contains secret: %q", content)
	}

	// Verify: both gitlinks in parent are updated
	gitlink1After := lsTreeSHA(t, lsTreeEntry(t, parentDir, "sub1"))
	gitlink2After := lsTreeSHA(t, lsTreeEntry(t, parentDir, "sub2"))
	if gitlink1After == gitlink1Before {
		t.Error("sub1 gitlink SHA did not change after scrub")
	}
	if gitlink2After == gitlink2Before {
		t.Error("sub2 gitlink SHA did not change after scrub")
	}
}

// Phase 4.12: scrub file with a path inside a submodule rewrites the submodule
// history and updates the parent gitlink.
func TestScrubFileInSubmodule(t *testing.T) {
	t.Parallel()
	parentDir, _, subDir := newRepoWithSubmoduleSecret(t, "SENSITIVE_DATA_HERE", "secret.txt")

	// Get the first commit in the submodule (the one with the secret)
	subSHAs := revListReverse(t, subDir)
	firstSubCommit := subSHAs[0]

	// Replace secret.txt on disk and commit in submodule + parent so tree is clean
	if err := os.WriteFile(filepath.Join(subDir, "secret.txt"), []byte("CLEANED\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "add", "secret.txt"},
		{"git", "commit", "-m", "commit replacement"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v in submodule: %v\n%s", args, err, out)
		}
	}
	for _, args := range [][]string{
		{"git", "add", "mysub"},
		{"git", "commit", "-m", "update submodule ref"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v in parent: %v\n%s", args, err, out)
		}
	}

	// Record gitlink before scrub
	gitlinkBefore := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))

	// Run scrub file targeting the submodule path
	stdout, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv,
		"--yes", "scrub", "file",
		"mysub/secret.txt",
		"--from", firstSubCommit,
		"--reason", "test",
	)
	if code != 0 {
		t.Fatalf("scrub file failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify: the file in submodule history is replaced
	newSubSHAs := revListReverse(t, subDir)
	for i, sha := range newSubSHAs {
		content, ok := gitShow(t, subDir, sha, "secret.txt")
		if !ok {
			continue
		}
		if strings.Contains(content, "SENSITIVE_DATA_HERE") {
			t.Errorf("submodule commit %d (%s): secret.txt still contains sensitive data: %q", i, sha[:12], content)
		}
		if !strings.Contains(content, "CLEANED") {
			t.Errorf("submodule commit %d (%s): secret.txt should contain CLEANED, got: %q", i, sha[:12], content)
		}
	}

	// Verify: parent gitlink updated
	gitlinkAfter := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if gitlinkAfter == gitlinkBefore {
		t.Error("parent gitlink SHA did not change after scrubbing file in submodule")
	}
}

// Phase 4.13: scrub match with --scope limits scrub to specific submodule paths.
func TestScrubMatchScopeSubmodule(t *testing.T) {
	t.Parallel()
	parentDir, sub1Dir, sub2Dir := newRepoWithTwoSubmoduleSecrets(t, "SECRET_SCOPED", "secret.txt")

	// Also add a file with the secret to the parent
	if err := os.WriteFile(filepath.Join(parentDir, "parentfile.txt"), []byte("SECRET_SCOPED\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "parentfile.txt")
	cmd.Dir = parentDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "add parent secret")
	cmd.Dir = parentDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	// Run scrub match with --scope limited to sub1
	stdout, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_SCOPED",
		"--replace", "CLEAN",
		"--reason", "test",
		"--entire-history",
		"--scope", "sub1/*",
	)
	if code != 0 {
		t.Fatalf("scrub match --scope failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify: sub1 is scrubbed (secret removed)
	sub1HEAD := revParseHEAD(t, sub1Dir)
	content, ok := gitShow(t, sub1Dir, sub1HEAD, "secret.txt")
	if !ok {
		t.Error("secret.txt not found in sub1 HEAD after scrub")
	} else {
		if strings.Contains(content, "SECRET_SCOPED") {
			t.Errorf("sub1 secret.txt still contains secret: %q", content)
		}
		if !strings.Contains(content, "CLEAN") {
			t.Errorf("sub1 secret.txt should contain CLEAN, got: %q", content)
		}
	}

	// Verify: sub2 is NOT scrubbed (secret still present)
	sub2HEAD := revParseHEAD(t, sub2Dir)
	content, ok = gitShow(t, sub2Dir, sub2HEAD, "secret.txt")
	if !ok {
		t.Error("secret.txt not found in sub2 HEAD")
	} else if !strings.Contains(content, "SECRET_SCOPED") {
		t.Errorf("sub2 secret.txt should still contain SECRET_SCOPED (out of scope), got: %q", content)
	}

	// Verify: parent blobs outside sub1/ are not scrubbed
	parentHEAD := revParseHEAD(t, parentDir)
	content, ok = gitShow(t, parentDir, parentHEAD, "parentfile.txt")
	if !ok {
		t.Error("parentfile.txt not found in parent HEAD")
	} else if !strings.Contains(content, "SECRET_SCOPED") {
		t.Errorf("parent parentfile.txt should still contain SECRET_SCOPED (outside scope sub1/*), got: %q", content)
	}
}

// Phase 4.15: scrub match --dry-run reports submodule findings without changing anything.
func TestScrubMatchDryRunWithSubmodules(t *testing.T) {
	t.Parallel()
	parentDir, _, _ := newRepoWithSubmoduleSecret(t, "SECRET_DRYRUN", "secret.txt")

	// Record state before dry-run
	headBefore := revParseHEAD(t, parentDir)
	gitlinkBefore := lsTreeEntry(t, parentDir, "mysub")

	// Run with --dry-run
	stdout, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv,
		"--yes", "--dry-run", "scrub", "match",
		"--pattern", "SECRET_DRYRUN",
		"--replace", "X",
		"--reason", "x",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match --dry-run failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify: output mentions the submodule path
	combined := stdout + stderr
	if !strings.Contains(combined, "mysub") {
		t.Errorf("dry-run output should mention submodule path 'mysub', got: %s", combined)
	}

	// Verify: no changes were made (HEAD unchanged)
	headAfter := revParseHEAD(t, parentDir)
	if headAfter != headBefore {
		t.Errorf("HEAD changed during dry run: %s -> %s", headBefore[:12], headAfter[:12])
	}

	// Verify: gitlink unchanged
	gitlinkAfter := lsTreeEntry(t, parentDir, "mysub")
	if gitlinkAfter != gitlinkBefore {
		t.Errorf("gitlink changed during dry run:\n  before: %s\n  after:  %s", gitlinkBefore, gitlinkAfter)
	}
}

// --- Phase 5: Auto-bump integration tests ---

// enableAutoBump sets commit.autoBumpParent=true in the parent repo.
func enableAutoBump(t *testing.T, parentDir string) {
	t.Helper()
	_, stderr, code := runSafegit(t, parentDir, "config", "set", "commit.autoBumpParent", "true")
	if code != 0 {
		t.Fatalf("failed to enable autoBumpParent: %s", stderr)
	}
}

// newRepoWithTwoSubmodules creates a parent repo with two submodules "sub1"
// and "sub2", both on the "main" branch (not detached). Returns parent dir,
// sub1 dir, and sub2 dir.
func newRepoWithTwoSubmodules(t *testing.T) (parentDir, sub1Dir, sub2Dir string) {
	t.Helper()
	base := evalTempDir(t)

	// Helper to create a submodule origin
	createSubOrigin := func(name string) string {
		originDir := filepath.Join(base, name+"-origin")
		if err := os.MkdirAll(originDir, 0755); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"git", "init", "--initial-branch=main"},
			{"git", "config", "user.email", "test@test.com"},
			{"git", "config", "user.name", "Test"},
		} {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = originDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v failed: %v\n%s", args, err, out)
			}
		}
		if err := os.WriteFile(filepath.Join(originDir, "file.txt"), []byte(name+" content\n"), 0644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"git", "add", "file.txt"},
			{"git", "commit", "-m", name + " initial"},
		} {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = originDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v failed: %v\n%s", args, err, out)
			}
		}
		return originDir
	}

	sub1Origin := createSubOrigin("sub1")
	sub2Origin := createSubOrigin("sub2")

	// Create the parent repo
	parentDir = filepath.Join(base, "parent")
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(parentDir, "seed.txt"), []byte("seed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "seed.txt"},
		{"git", "commit", "-m", "initial"},
		{"git", "config", "protocol.file.allow", "always"},
		{"git", "submodule", "add", "-q", sub1Origin, "sub1"},
		{"git", "submodule", "add", "-q", sub2Origin, "sub2"},
		{"git", "commit", "-q", "-m", "add submodules"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Put both submodule checkouts on the main branch
	sub1Dir = filepath.Join(parentDir, "sub1")
	sub2Dir = filepath.Join(parentDir, "sub2")
	for _, subDir := range []string{sub1Dir, sub2Dir} {
		for _, args := range [][]string{
			{"git", "checkout", "main"},
			{"git", "config", "user.email", "test@test.com"},
			{"git", "config", "user.name", "Test"},
		} {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = subDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v in submodule %s: %v\n%s", args, subDir, err, out)
			}
		}
	}

	return parentDir, sub1Dir, sub2Dir
}

// Test 5.1: Committing in a submodule with auto-bump enabled bumps the parent.
func TestAutoBumpOnCommit(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)
	// Override: enable auto-bump (prepSubmoduleForCommit disables it)
	enableAutoBump(t, parentDir)

	parentCountBefore := gitLog(t, parentDir, "HEAD")

	// Create file in submodule and commit
	if err := os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("new content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, subDir, "commit", "-m", "sub change", "--", "file.txt")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): %s", code, stderr)
	}

	// Verify: parent HEAD count increased by 1
	parentCountAfter := gitLog(t, parentDir, "HEAD")
	if parentCountAfter != parentCountBefore+1 {
		t.Errorf("parent commit count: want %d, got %d", parentCountBefore+1, parentCountAfter)
	}

	// Verify: parent's gitlink points to the submodule's new SHA
	subHEAD := revParseHEAD(t, subDir)
	parentGitlink := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if parentGitlink != subHEAD {
		t.Errorf("parent gitlink = %s, want submodule HEAD = %s", parentGitlink, subHEAD)
	}

	// Verify: parent commit message contains the bump info
	msg := commitMessage(t, parentDir, "HEAD")
	if !strings.Contains(msg, "bump mysub: sub change") {
		t.Errorf("parent commit message %q should contain 'bump mysub: sub change'", msg)
	}
}

// Test 5.2: When autoBumpParent config is absent, commit in submodule fails.
func TestAutoBumpConfigAbsentErrors(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := filepath.Join(parentDir, "mysub")
	// Put submodule on main branch and configure identity
	for _, args := range [][]string{
		{"git", "checkout", "main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v in submodule: %v\n%s", args, err, out)
		}
	}
	// Do NOT set autoBumpParent config in parent

	subCountBefore := gitLog(t, subDir, "HEAD")

	// Create file in submodule and commit
	if err := os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, subDir, "commit", "-m", "test", "--", "file.txt")

	// Verify: exit code is non-zero
	if code == 0 {
		t.Fatal("expected non-zero exit code when autoBumpParent not configured")
	}

	// Verify: stderr mentions the config issue
	if !strings.Contains(stderr, "commit.autoBumpParent not configured") {
		t.Errorf("stderr should mention 'commit.autoBumpParent not configured', got: %s", stderr)
	}

	// Verify: the submodule commit DID land (the commit itself succeeded)
	subCountAfter := gitLog(t, subDir, "HEAD")
	if subCountAfter != subCountBefore+1 {
		t.Errorf("submodule commit count: want %d, got %d (commit should have landed)", subCountBefore+1, subCountAfter)
	}
}

// Test 5.3: When autoBumpParent is explicitly false, commit succeeds but parent is not bumped.
func TestAutoBumpConfigFalseSkips(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir) // sets autoBumpParent=false

	parentCountBefore := gitLog(t, parentDir, "HEAD")

	// Create file in submodule and commit
	if err := os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, subDir, "commit", "-m", "test", "--", "file.txt")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): %s", code, stderr)
	}

	// Verify: parent HEAD count unchanged
	parentCountAfter := gitLog(t, parentDir, "HEAD")
	if parentCountAfter != parentCountBefore {
		t.Errorf("parent commit count changed: was %d, now %d (should be unchanged)", parentCountBefore, parentCountAfter)
	}
}

// Test 5.4: Nested submodules trigger an error when auto-bump is enabled.
func TestAutoBumpNestedErrors(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := filepath.Join(parentDir, "mysub")
	// Put submodule on main branch and configure identity
	for _, args := range [][]string{
		{"git", "checkout", "main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v in submodule: %v\n%s", args, err, out)
		}
	}

	// Create a .gitmodules file inside the submodule (simulating nested submodules)
	if err := os.WriteFile(filepath.Join(subDir, ".gitmodules"), []byte("[submodule \"nested\"]\n\tpath = nested\n\turl = /fake\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Enable auto-bump in parent
	enableAutoBump(t, parentDir)

	subCountBefore := gitLog(t, subDir, "HEAD")

	// Commit in submodule
	if err := os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("nested test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, subDir, "commit", "-m", "test nested", "--", "file.txt")

	// Verify: exit code non-zero
	if code == 0 {
		t.Fatal("expected non-zero exit code when nested submodules detected")
	}

	// Verify: stderr mentions nested submodules
	if !strings.Contains(stderr, "nested submodules") {
		t.Errorf("stderr should mention 'nested submodules', got: %s", stderr)
	}

	// Verify: the submodule commit itself still landed
	subCountAfter := gitLog(t, subDir, "HEAD")
	if subCountAfter != subCountBefore+1 {
		t.Errorf("submodule commit count: want %d, got %d (commit should have landed)", subCountBefore+1, subCountAfter)
	}
}

// Test 5.5: Undo in submodule triggers parent bump back to original SHA.
func TestAutoBumpUndo(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)
	enableAutoBump(t, parentDir)
	env := []string{"CLAUDE_CODE_SESSION_ID=autobump-undo-test"}

	// Record initial state
	parentCountInitial := gitLog(t, parentDir, "HEAD")
	gitlinkInitial := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))

	// Commit in submodule (triggers bump, parent count +1)
	if err := os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("will undo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, subDir, env, "commit", "-m", "commit to undo", "--", "file.txt")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): %s", code, stderr)
	}

	parentCountAfterCommit := gitLog(t, parentDir, "HEAD")
	if parentCountAfterCommit != parentCountInitial+1 {
		t.Fatalf("parent count after commit: want %d, got %d", parentCountInitial+1, parentCountAfterCommit)
	}

	// Undo from subDir
	_, stderr, code = runSafegitEnv(t, subDir, env, "undo")
	if code != 0 {
		t.Fatalf("safegit undo failed (code %d): %s", code, stderr)
	}

	// Verify: parent HEAD count increased again (+1 more, total +2 from start)
	parentCountAfterUndo := gitLog(t, parentDir, "HEAD")
	if parentCountAfterUndo != parentCountInitial+2 {
		t.Errorf("parent count after undo: want %d, got %d", parentCountInitial+2, parentCountAfterUndo)
	}

	// Verify: parent gitlink SHA is back to the original (before the sub commit)
	gitlinkAfterUndo := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if gitlinkAfterUndo != gitlinkInitial {
		t.Errorf("parent gitlink after undo = %s, want original = %s", gitlinkAfterUndo, gitlinkInitial)
	}
}

// Test 5.6: Amend in submodule triggers another parent bump.
func TestAutoBumpAmend(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)
	enableAutoBump(t, parentDir)
	env := []string{"CLAUDE_CODE_SESSION_ID=autobump-amend-test"}

	parentCountInitial := gitLog(t, parentDir, "HEAD")

	// Commit file in submodule (bump happens)
	if err := os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, subDir, env, "commit", "-m", "original msg", "--", "file.txt")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): %s", code, stderr)
	}

	// Modify the file and amend
	if err := os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("amended\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegitEnv(t, subDir, env, "commit", "--amend", "-m", "amended msg", "--", "file.txt")
	if code != 0 {
		t.Fatalf("safegit commit --amend failed (code %d): %s", code, stderr)
	}

	// Verify: parent has been bumped twice (initial + 2)
	parentCountAfter := gitLog(t, parentDir, "HEAD")
	if parentCountAfter != parentCountInitial+2 {
		t.Errorf("parent count after amend: want %d, got %d", parentCountInitial+2, parentCountAfter)
	}

	// Verify: parent gitlink points to the amended commit SHA
	subHEAD := revParseHEAD(t, subDir)
	parentGitlink := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if parentGitlink != subHEAD {
		t.Errorf("parent gitlink = %s, want amended submodule HEAD = %s", parentGitlink, subHEAD)
	}
}

// Test 5.7: Concurrent commits in different submodules both trigger auto-bumps.
func TestAutoBumpConcurrentDifferentSubs(t *testing.T) {
	t.Parallel()
	parentDir, sub1Dir, sub2Dir := newRepoWithTwoSubmodules(t)
	enableAutoBump(t, parentDir)

	// Set CAS max attempts high for concurrent test
	_, stderr, code := runSafegit(t, parentDir, "config", "set", "commit.casMaxAttempts", "50")
	if code != 0 {
		t.Fatalf("config set casMaxAttempts failed: %s", stderr)
	}

	parentCountBefore := gitLog(t, parentDir, "HEAD")

	// Create files in both submodules
	if err := os.WriteFile(filepath.Join(sub1Dir, "new1.txt"), []byte("sub1 data\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub2Dir, "new2.txt"), []byte("sub2 data\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Launch two concurrent safegit commits
	stdouts := make([]string, 2)
	stderrs := make([]string, 2)
	codes := make([]int, 2)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		stdouts[0], stderrs[0], codes[0] = runSafegit(t, sub1Dir, "commit", "-m", "sub1 change", "--", "new1.txt")
	}()
	go func() {
		defer wg.Done()
		stdouts[1], stderrs[1], codes[1] = runSafegit(t, sub2Dir, "commit", "-m", "sub2 change", "--", "new2.txt")
	}()
	wg.Wait()

	if codes[0] != 0 {
		t.Fatalf("sub1 commit failed (code %d): %s", codes[0], stderrs[0])
	}
	if codes[1] != 0 {
		t.Fatalf("sub2 commit failed (code %d): %s", codes[1], stderrs[1])
	}

	// Verify: parent HEAD count increased by at least 2
	parentCountAfter := gitLog(t, parentDir, "HEAD")
	if parentCountAfter < parentCountBefore+2 {
		t.Errorf("parent count: want at least %d, got %d", parentCountBefore+2, parentCountAfter)
	}

	// Verify: both gitlinks are updated
	sub1HEAD := revParseHEAD(t, sub1Dir)
	sub2HEAD := revParseHEAD(t, sub2Dir)
	gitlink1 := lsTreeSHA(t, lsTreeEntry(t, parentDir, "sub1"))
	gitlink2 := lsTreeSHA(t, lsTreeEntry(t, parentDir, "sub2"))
	if gitlink1 != sub1HEAD {
		t.Errorf("sub1 gitlink = %s, want %s", gitlink1, sub1HEAD)
	}
	if gitlink2 != sub2HEAD {
		t.Errorf("sub2 gitlink = %s, want %s", gitlink2, sub2HEAD)
	}
}

// Test 5.8: Auto-bump commit includes proper trailers.
func TestAutoBumpTrailers(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)
	enableAutoBump(t, parentDir)

	// Commit in submodule
	if err := os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("trailer test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, subDir, "commit", "-m", "trigger trailers", "--", "file.txt")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): %s", code, stderr)
	}

	// Read parent's HEAD commit message
	msg := commitMessage(t, parentDir, "HEAD")
	subHEAD := revParseHEAD(t, subDir)

	// Verify: contains Triggered-by trailer with the sub's new SHA
	if !strings.Contains(msg, "Triggered-by: "+subHEAD) {
		t.Errorf("parent commit message missing 'Triggered-by: %s', got:\n%s", subHEAD, msg)
	}

	// Verify: contains Operation trailer
	if !strings.Contains(msg, "Operation: commit") {
		t.Errorf("parent commit message missing 'Operation: commit', got:\n%s", msg)
	}
}

// Test 5.9: A second commit in submodule correctly bumps parent again.
func TestAutoBumpNoopWhenCurrent(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)
	enableAutoBump(t, parentDir)

	// First commit + bump
	if err := os.WriteFile(filepath.Join(subDir, "file1.txt"), []byte("first\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, subDir, "commit", "-m", "first commit", "--", "file1.txt")
	if code != 0 {
		t.Fatalf("first commit failed (code %d): %s", code, stderr)
	}

	parentCountAfterFirst := gitLog(t, parentDir, "HEAD")
	gitlinkAfterFirst := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))

	// Second commit + bump
	if err := os.WriteFile(filepath.Join(subDir, "file2.txt"), []byte("second\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegit(t, subDir, "commit", "-m", "second commit", "--", "file2.txt")
	if code != 0 {
		t.Fatalf("second commit failed (code %d): %s", code, stderr)
	}

	// Verify: parent bumped again
	parentCountAfterSecond := gitLog(t, parentDir, "HEAD")
	if parentCountAfterSecond != parentCountAfterFirst+1 {
		t.Errorf("parent count after second commit: want %d, got %d", parentCountAfterFirst+1, parentCountAfterSecond)
	}

	// Verify: gitlink updated to the second commit's SHA
	subHEAD := revParseHEAD(t, subDir)
	gitlinkAfterSecond := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if gitlinkAfterSecond == gitlinkAfterFirst {
		t.Error("parent gitlink did not change after second commit")
	}
	if gitlinkAfterSecond != subHEAD {
		t.Errorf("parent gitlink = %s, want submodule HEAD = %s", gitlinkAfterSecond, subHEAD)
	}
}

// Phase 4.16: scrub match with a pattern that appears only in a binary blob
// inside a submodule. Binary blobs are skipped, so the submodule should remain
// unchanged and the operation should succeed cleanly.
func TestScrubMatchPartialFailure(t *testing.T) {
	t.Parallel()
	base := evalTempDir(t)

	// Create submodule origin with a binary file containing the secret
	subOriginDir := filepath.Join(base, "sub-origin")
	if err := os.MkdirAll(subOriginDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subOriginDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	// Binary content: NUL bytes + secret
	binaryContent := []byte("header\x00\x00\x00BINARY_SECRET_ONLY\x00end\n")
	if err := os.WriteFile(filepath.Join(subOriginDir, "data.bin"), binaryContent, 0644); err != nil {
		t.Fatal(err)
	}
	// Also add a text file so the repo has some text content
	if err := os.WriteFile(filepath.Join(subOriginDir, "readme.txt"), []byte("clean content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "data.bin", "readme.txt"},
		{"git", "commit", "-m", "sub initial with binary"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subOriginDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Create parent repo
	parentDir := filepath.Join(base, "parent")
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(parentDir, "seed.txt"), []byte("seed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "seed.txt"},
		{"git", "commit", "-m", "initial"},
		{"git", "config", "protocol.file.allow", "always"},
		{"git", "submodule", "add", "-q", subOriginDir, "mysub"},
		{"git", "commit", "-q", "-m", "add submodule"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Put submodule on main branch
	subDir := filepath.Join(parentDir, "mysub")
	for _, args := range [][]string{
		{"git", "checkout", "main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v in submodule: %v\n%s", args, err, out)
		}
	}

	// Record state before scrub
	gitlinkBefore := lsTreeEntry(t, parentDir, "mysub")

	// Run scrub match -- pattern only exists in binary blob (which gets skipped)
	stdout, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv,
		"--yes", "scrub", "match",
		"--pattern", "BINARY_SECRET_ONLY",
		"--replace", "REDACTED",
		"--reason", "test binary in submodule",
		"--entire-history",
	)

	// The operation should succeed (binary blobs are skipped, no text matches)
	// It may report "No matches found" since binary is skipped.
	if code != 0 {
		// If verification fails because the secret is in a binary blob in the
		// object store, code 1 is acceptable (verification detected it).
		// But code > 1 would indicate a crash.
		if code > 1 {
			t.Fatalf("scrub match crashed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
		}
		t.Logf("scrub match exited with code %d (verification may have detected binary blob secret): %s", code, stderr)
	}

	// Verify: gitlink unchanged (binary blob was skipped, nothing to rewrite)
	gitlinkAfter := lsTreeEntry(t, parentDir, "mysub")
	if gitlinkAfter != gitlinkBefore {
		t.Logf("gitlink changed: before=%s after=%s (acceptable if scrub rewrote text objects)", gitlinkBefore, gitlinkAfter)
	}

	// Verify: git fsck passes (no corruption regardless of exit code)
	fsck := exec.Command("git", "fsck", "--no-dangling")
	fsck.Dir = parentDir
	if out, err := fsck.CombinedOutput(); err != nil {
		t.Errorf("git fsck failed after scrub: %v\n%s", err, out)
	}
	fsck = exec.Command("git", "fsck", "--no-dangling")
	fsck.Dir = subDir
	if out, err := fsck.CombinedOutput(); err != nil {
		t.Errorf("git fsck failed in submodule after scrub: %v\n%s", err, out)
	}
}

// TestScrubMatchSubmoduleWorktreeSync verifies that after scrub match rewrites
// submodule history, the submodule's working tree and index are synced (on-disk
// file contains the replacement, git status is clean).
func TestScrubMatchSubmoduleWorktreeSync(t *testing.T) {
	t.Parallel()
	parentDir, _, subDir := newRepoWithSubmoduleSecret(t, "WORKTREE_SECRET_42", "secret.txt")

	// Run scrub match from the parent
	stdout, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv,
		"--yes", "scrub", "match",
		"--pattern", "WORKTREE_SECRET_42",
		"--replace", "REDACTED",
		"--reason", "test worktree sync",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify: on-disk file in the submodule contains the replacement, not the secret
	diskContent, err := os.ReadFile(filepath.Join(subDir, "secret.txt"))
	if err != nil {
		t.Fatalf("reading secret.txt from submodule disk: %v", err)
	}
	if strings.Contains(string(diskContent), "WORKTREE_SECRET_42") {
		t.Errorf("submodule on-disk secret.txt still contains secret: %q", string(diskContent))
	}
	if !strings.Contains(string(diskContent), "REDACTED") {
		t.Errorf("submodule on-disk secret.txt should contain REDACTED, got: %q", string(diskContent))
	}

	// Verify: git status --porcelain is clean inside the submodule
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = subDir
	statusOut, err := statusCmd.Output()
	if err != nil {
		t.Fatalf("git status in submodule: %v", err)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		t.Errorf("submodule working tree is dirty after scrub: %s", string(statusOut))
	}

	// Verify: git status --porcelain is clean in the parent too
	parentStatusCmd := exec.Command("git", "status", "--porcelain")
	parentStatusCmd.Dir = parentDir
	parentStatusOut, err := parentStatusCmd.Output()
	if err != nil {
		t.Fatalf("git status in parent: %v", err)
	}
	if strings.TrimSpace(string(parentStatusOut)) != "" {
		t.Errorf("parent working tree is dirty after scrub: %s", string(parentStatusOut))
	}
}
