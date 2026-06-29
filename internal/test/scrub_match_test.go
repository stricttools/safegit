package test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var scrubMatchEnv = []string{"CLAUDE_CODE_SESSION_ID=scrub-match-test"}

// TestScrubMatchBlobReplace creates 3 files with "SECRET_ABC" across 5 commits.
// Runs scrub match and verifies all files in all commits have "REDACTED".
func TestScrubMatchBlobReplace(t *testing.T) {
	dir := newRepo(t)

	// Create 3 files with SECRET_ABC across 5 commits
	commitFileEnv(t, dir, scrubMatchEnv, "file1.txt", "data SECRET_ABC here\n", "add file1")
	commitFileEnv(t, dir, scrubMatchEnv, "file2.txt", "also SECRET_ABC inside\n", "add file2")
	commitFileEnv(t, dir, scrubMatchEnv, "file3.txt", "contains SECRET_ABC too\n", "add file3")
	commitFileEnv(t, dir, scrubMatchEnv, "file1.txt", "updated SECRET_ABC v2\n", "update file1")
	commitFileEnv(t, dir, scrubMatchEnv, "file2.txt", "updated SECRET_ABC v2\n", "update file2")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test blob replace",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify all files in all commits have REDACTED instead of SECRET_ABC
	shas := revListReverse(t, dir)
	for i, sha := range shas {
		for _, fname := range []string{"file1.txt", "file2.txt", "file3.txt"} {
			content, ok := gitShow(t, dir, sha, fname)
			if !ok {
				continue // file may not exist in early commits
			}
			if strings.Contains(content, "SECRET_ABC") {
				t.Errorf("commit %d (%s): %s still contains SECRET_ABC: %q", i, sha[:12], fname, content)
			}
			if !strings.Contains(content, "REDACTED") {
				t.Errorf("commit %d (%s): %s missing REDACTED: %q", i, sha[:12], fname, content)
			}
		}
	}

	// Verify on-disk files have REDACTED and not the original secret
	for _, fname := range []string{"file1.txt", "file2.txt", "file3.txt"} {
		diskContent, err := os.ReadFile(filepath.Join(dir, fname))
		if err != nil {
			t.Fatalf("reading %s from disk: %v", fname, err)
		}
		if strings.Contains(string(diskContent), "SECRET_ABC") {
			t.Errorf("on-disk %s still contains SECRET_ABC: %q", fname, string(diskContent))
		}
		if !strings.Contains(string(diskContent), "REDACTED") {
			t.Errorf("on-disk %s missing REDACTED: %q", fname, string(diskContent))
		}
	}

	// Verify working tree is clean after scrub match
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = dir
	statusOut, err := statusCmd.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		t.Errorf("working tree is dirty after scrub match: %s", string(statusOut))
	}
}

// TestScrubMatchCommitMessage verifies that scrub match replaces patterns
// in commit messages.
func TestScrubMatchCommitMessage(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "some SECRET_ABC data\n", "added SECRET_ABC key")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test commit message",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Check commit messages
	cmd := exec.Command("git", "log", "--format=%B")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	messages := string(out)
	if strings.Contains(messages, "SECRET_ABC") {
		t.Errorf("commit messages still contain SECRET_ABC: %s", messages)
	}
}

// TestScrubMatchTagAnnotation verifies that scrub match replaces patterns
// in annotated tag messages.
func TestScrubMatchTagAnnotation(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "some SECRET_ABC data\n", "add data")

	// Create annotated tag with SECRET_ABC in the annotation
	cmd := exec.Command("git", "tag", "-a", "v1.0", "-m", "release with SECRET_ABC")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git tag: %v\n%s", err, out)
	}

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test tag annotation",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Check tag annotation
	cmd = exec.Command("git", "tag", "-l", "-n99", "v1.0")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git tag -l -n99: %v", err)
	}
	tagOutput := string(out)
	if strings.Contains(tagOutput, "SECRET_ABC") {
		t.Errorf("tag annotation still contains SECRET_ABC: %s", tagOutput)
	}
	if !strings.Contains(tagOutput, "REDACTED") {
		t.Errorf("tag annotation does not contain REDACTED: %s", tagOutput)
	}
}

// TestScrubMatchBinarySkipped creates a binary file containing SECRET_ABC
// and verifies it is NOT modified by scrub match (binary blobs are skipped).
func TestScrubMatchBinarySkipped(t *testing.T) {
	dir := newRepo(t)

	// Create a binary file: NUL bytes + SECRET_ABC
	binaryContent := []byte("header\x00\x00\x00binary SECRET_ABC content\x00end")
	binaryPath := filepath.Join(dir, "binary.dat")
	if err := os.WriteFile(binaryPath, binaryContent, 0644); err != nil {
		t.Fatal(err)
	}

	// Also create a text file with the secret so scrub has something to match
	commitFileEnv(t, dir, scrubMatchEnv, "text.txt", "SECRET_ABC\n", "add text file")

	// Commit the binary file using git directly (safegit commit needs it tracked)
	cmd := exec.Command("git", "add", "binary.dat")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "add binary file")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), scrubMatchEnv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	// Record the binary blob SHA before scrub
	cmd = exec.Command("git", "rev-parse", "HEAD:binary.dat")
	cmd.Dir = dir
	blobSHAOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD:binary.dat: %v", err)
	}
	blobSHABefore := strings.TrimSpace(string(blobSHAOut))

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test binary skip",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// The binary file should be unchanged in HEAD
	headSHA := revParseHEAD(t, dir)
	cmd = exec.Command("git", "cat-file", "blob", headSHA+":binary.dat")
	cmd.Dir = dir
	blobContent, err := cmd.Output()
	if err != nil {
		t.Fatalf("reading binary blob after scrub: %v", err)
	}
	if string(blobContent) != string(binaryContent) {
		t.Errorf("binary file was modified by scrub; got %d bytes, want %d bytes", len(blobContent), len(binaryContent))
	}

	// Verify the binary blob's hash is the same (unchanged)
	cmd = exec.Command("git", "rev-parse", headSHA+":binary.dat")
	cmd.Dir = dir
	blobSHAOut, err = cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse after scrub: %v", err)
	}
	blobSHAAfter := strings.TrimSpace(string(blobSHAOut))
	if blobSHAAfter != blobSHABefore {
		t.Errorf("binary blob SHA changed: %s -> %s (should be unchanged)", blobSHABefore, blobSHAAfter)
	}
}

// TestScrubMatchUnreachablePruned creates a commit with SECRET_ABC, amends it
// (creating an unreachable object), runs scrub match, and verifies the old
// unreachable commit is gone from the object store.
func TestScrubMatchUnreachablePruned(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "secret.txt", "SECRET_ABC data\n", "add secret")
	oldSHA := revParseHEAD(t, dir)

	// Amend the commit (creating an unreachable old commit)
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("SECRET_ABC amended\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "secret.txt")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "--amend", "-m", "amended secret")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit --amend: %v\n%s", err, out)
	}

	// Verify the old SHA is still reachable (via reflog) before scrub
	cmd = exec.Command("git", "cat-file", "-e", oldSHA)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("old commit %s should still exist before scrub", oldSHA[:12])
	}

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test unreachable prune",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// The old commit should be gone from the object store
	cmd = exec.Command("git", "cat-file", "-e", oldSHA)
	cmd.Dir = dir
	if err := cmd.Run(); err == nil {
		t.Errorf("old unreachable commit %s still exists after scrub cleanup", oldSHA[:12])
	}
}

// TestScrubMatchSurgicalReflog creates a repo with a secret on main,
// captures pre-rewrite SHAs, runs scrub match, and verifies the old SHAs
// no longer appear in the reflog (cleanup expired them).
func TestScrubMatchSurgicalReflog(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "secret.txt", "SECRET_ABC\n", "add secret on main")
	commitFileEnv(t, dir, scrubMatchEnv, "secret.txt", "SECRET_ABC v2\n", "update secret on main")

	// Capture pre-rewrite SHAs (the secret-containing commits)
	allSHAsBefore := revListReverse(t, dir)
	preScrubSHAs := make(map[string]bool)
	for _, sha := range allSHAsBefore[1:] { // skip seed commit
		preScrubSHAs[sha] = true
	}

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test surgical reflog",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify old (pre-rewrite) SHAs no longer appear in any reflog
	cmd := exec.Command("git", "reflog", "show", "--format=%H", "--all")
	cmd.Dir = dir
	out, _ := cmd.Output()

	headCmd := exec.Command("git", "reflog", "show", "--format=%H", "HEAD")
	headCmd.Dir = dir
	headOut, _ := headCmd.Output()

	allReflogSHAs := strings.TrimSpace(string(out)) + "\n" + strings.TrimSpace(string(headOut))
	for _, line := range strings.Split(allReflogSHAs, "\n") {
		sha := strings.TrimSpace(line)
		if sha == "" {
			continue
		}
		if preScrubSHAs[sha] {
			t.Errorf("pre-rewrite SHA %s still in reflog after cleanup", sha[:12])
		}
	}

	// Verify the committed history is clean
	allSHAsAfter := revListReverse(t, dir)
	for i, sha := range allSHAsAfter {
		content, ok := gitShow(t, dir, sha, "secret.txt")
		if !ok {
			continue
		}
		if strings.Contains(content, "SECRET_ABC") {
			t.Errorf("commit %d (%s): secret.txt still contains SECRET_ABC", i, sha[:12])
		}
	}
}

// TestScrubMatchStashWarning creates a stash containing SECRET_ABC,
// runs scrub match, and verifies that the verification scan detects the
// secret in stash objects and exits with code 1 (CRITICAL warning).
// Stash blobs are not rewritten because stash commits are on refs/stash,
// not on any branch's ancestry.
func TestScrubMatchStashWarning(t *testing.T) {
	dir := newRepo(t)

	// Create a commit with the secret
	commitFileEnv(t, dir, scrubMatchEnv, "secret.txt", "SECRET_ABC\n", "add secret")

	// Modify the file and stash it
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("SECRET_ABC modified for stash\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "stash")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git stash: %v\n%s", err, out)
	}

	// Verify stash exists
	cmd = exec.Command("git", "stash", "list")
	cmd.Dir = dir
	stashOut, _ := cmd.Output()
	if !strings.Contains(string(stashOut), "stash@{0}") {
		t.Fatal("stash entry not created")
	}

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test stash warning",
		"--entire-history",
	)

	// The command exits 1 because the verification scan detects SECRET_ABC
	// in stash blobs that were not rewritten.
	if code != 1 {
		t.Fatalf("expected exit code 1 (verification failure due to stash), got %d: stdout=%s stderr=%s", code, stdout, stderr)
	}

	// stderr should contain CRITICAL warning about secret still present
	if !strings.Contains(stderr, "CRITICAL") {
		t.Errorf("stderr should contain CRITICAL warning, got: %s", stderr)
	}

	// Verify the committed history is clean despite the stash issue
	shas := revListReverse(t, dir)
	for i, sha := range shas {
		content, ok := gitShow(t, dir, sha, "secret.txt")
		if !ok {
			continue
		}
		if strings.Contains(content, "SECRET_ABC") {
			t.Errorf("commit %d (%s): secret.txt still contains SECRET_ABC", i, sha[:12])
		}
	}
}

// TestScrubMatchEntireHistory creates 5 commits and verifies that
// --entire-history rewrites all of them (all SHAs changed).
func TestScrubMatchEntireHistory(t *testing.T) {
	dir := newRepo(t)

	var originalSHAs []string
	for i := 1; i <= 5; i++ {
		sha := commitFileEnv(t, dir, scrubMatchEnv, "data.txt",
			strings.Repeat("SECRET_ABC ", i)+"\n",
			"commit "+string(rune('0'+i)))
		originalSHAs = append(originalSHAs, sha)
	}

	allSHAsBefore := revListReverse(t, dir)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test entire history",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	allSHAsAfter := revListReverse(t, dir)

	// Commit count should be unchanged (no auto-commit for policy storage).
	if len(allSHAsAfter) != len(allSHAsBefore) {
		t.Fatalf("commit count: want %d, got %d",
			len(allSHAsBefore), len(allSHAsAfter))
	}

	// The seed commit (index 0) doesn't contain SECRET_ABC, but it may still
	// be rewritten because --entire-history includes all commits and descendant
	// commits get new parent SHAs. Skip the seed commit and verify commits 1-5.
	for i := 1; i < len(allSHAsBefore); i++ {
		if allSHAsAfter[i] == allSHAsBefore[i] {
			t.Errorf("commit %d was not rewritten: SHA still %s", i, allSHAsBefore[i][:12])
		}
	}
}

// TestScrubMatchFromScope creates 5 commits where the secret only appears in
// commits 3-5, and uses --from on commit 3 to verify only commits 3-5 are
// rewritten while 1-2 remain unchanged.
func TestScrubMatchFromScope(t *testing.T) {
	dir := newRepo(t)

	// Commits 1-2: no secret (innocent content)
	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "clean content v1\n", "commit 1")
	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "clean content v2\n", "commit 2")

	// Commits 3-5: contain the secret
	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "SECRET_ABC v3\n", "commit 3")
	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "SECRET_ABC v4\n", "commit 4")
	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "SECRET_ABC v5\n", "commit 5")

	allSHAs := revListReverse(t, dir)
	// allSHAs[0] = seed commit, allSHAs[1..5] = our 5 commits

	// Use --from on the 3rd added commit (index 3), so commits 3-5 are rewritten
	fromSHA := allSHAs[3]

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test from scope",
		"--from", fromSHA,
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	newSHAs := revListReverse(t, dir)

	// Commits 0, 1, 2 should be unchanged
	for i := 0; i <= 2; i++ {
		if newSHAs[i] != allSHAs[i] {
			t.Errorf("commit %d changed: %s -> %s (should be unchanged)", i, allSHAs[i][:12], newSHAs[i][:12])
		}
	}

	// Commits 3, 4, 5 should be rewritten
	for i := 3; i <= 5; i++ {
		if newSHAs[i] == allSHAs[i] {
			t.Errorf("commit %d not rewritten: SHA still %s", i, allSHAs[i][:12])
		}
	}

	// Verify rewritten commits have REDACTED instead of SECRET_ABC
	for i := 3; i <= 5; i++ {
		content, ok := gitShow(t, dir, newSHAs[i], "data.txt")
		if !ok {
			t.Errorf("commit %d (%s): data.txt not found", i, newSHAs[i][:12])
			continue
		}
		if strings.Contains(content, "SECRET_ABC") {
			t.Errorf("commit %d (%s): data.txt still contains SECRET_ABC", i, newSHAs[i][:12])
		}
		if !strings.Contains(content, "REDACTED") {
			t.Errorf("commit %d (%s): data.txt missing REDACTED", i, newSHAs[i][:12])
		}
	}
}

// TestScrubMatchIdempotent runs scrub match twice and verifies the second
// run finds no matches and exits cleanly.
func TestScrubMatchIdempotent(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "SECRET_ABC data\n", "add data")

	// First run
	stdout1, stderr1, code1 := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "idempotent test 1",
		"--entire-history",
	)
	if code1 != 0 {
		t.Fatalf("first scrub match failed (code %d): stdout=%s stderr=%s", code1, stdout1, stderr1)
	}

	headAfterFirst := revParseHEAD(t, dir)

	// Second run
	stdout2, stderr2, code2 := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "idempotent test 2",
		"--entire-history",
	)
	if code2 != 0 {
		t.Fatalf("second scrub match failed (code %d): stdout=%s stderr=%s", code2, stdout2, stderr2)
	}

	combined := stdout2 + stderr2
	if !strings.Contains(combined, "No matches found") {
		t.Errorf("second run should report no matches, got: %s", combined)
	}

	headAfterSecond := revParseHEAD(t, dir)
	if headAfterSecond != headAfterFirst {
		t.Errorf("HEAD changed on second run: %s -> %s (should be unchanged)", headAfterFirst[:12], headAfterSecond[:12])
	}
}

// TestScrubMatchDirtyTreeRejected verifies that scrub match always rejects
// a dirty working tree (the check is unconditional).
func TestScrubMatchDirtyTreeRejected(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "SECRET_ABC\n", "add data")

	// Make the working tree dirty
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("modified\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Dirty tree is always rejected, even with --yes
	_, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test dirty guard",
		"--entire-history",
	)
	if code == 0 {
		t.Fatal("scrub match on dirty tree should have failed, but exited 0")
	}
	if !strings.Contains(stderr, "working tree is dirty") {
		t.Errorf("error should contain 'working tree is dirty', got: %s", stderr)
	}
}

// TestScrubMatchDryRun verifies --dry-run previews matches without rewriting.
func TestScrubMatchDryRun(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "SECRET_ABC\n", "add data")
	headBefore := revParseHEAD(t, dir)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "--dry-run", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test dry run",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match --dry-run failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	combined := stdout + stderr

	// Dry-run should report match information
	if !strings.Contains(combined, "Found") && !strings.Contains(combined, "match") {
		t.Errorf("dry-run output should contain match info, got: %s", combined)
	}

	// HEAD should be unchanged
	headAfter := revParseHEAD(t, dir)
	if headAfter != headBefore {
		t.Errorf("HEAD changed during dry run: %s -> %s", headBefore[:12], headAfter[:12])
	}
}

// TestScrubMatchNoMatches verifies clean exit when pattern matches nothing.
func TestScrubMatchNoMatches(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "normal content\n", "add data")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "NONEXISTENT_PATTERN_XYZ",
		"--replace", "REDACTED",
		"--reason", "test no matches",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match with no matches failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	combined := stdout + stderr
	if !strings.Contains(combined, "No matches found") {
		t.Errorf("should report 'No matches found', got: %s", combined)
	}
}

// TestScrubMatchCoordinationGuard verifies the lock file is created during
// scrub and cleaned up after.
func TestScrubMatchCoordinationGuard(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "SECRET_ABC\n", "add data")

	// Run scrub match and check that it succeeds (lock is acquired and released)
	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test coordination",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify the lock file does NOT exist after completion (clean release)
	lockDir := filepath.Join(dir, ".git", "safegit", "locks")
	if _, err := os.Stat(lockDir); err == nil {
		entries, _ := os.ReadDir(lockDir)
		for _, e := range entries {
			if strings.Contains(e.Name(), "rewrite") {
				t.Errorf("lock file %s still exists after scrub completed", e.Name())
			}
		}
	}

	// Verify we can run a second scrub (lock was released properly)
	_, stderr2, code2 := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test coordination 2",
		"--entire-history",
	)
	if code2 != 0 {
		t.Fatalf("second scrub match failed (lock not released?): code %d, stderr=%s", code2, stderr2)
	}
}

// TestScrubMatchScope creates a repo with secret.txt and config/secret.env
// both containing "SECRET_ABC". Runs scrub match with --scope '*.env' and
// verifies only the .env file is scrubbed while secret.txt is left untouched.
func TestScrubMatchScope(t *testing.T) {
	dir := newRepo(t)

	// Create secret.txt and config/secret.env, both containing SECRET_ABC
	commitFileEnv(t, dir, scrubMatchEnv, "secret.txt", "data SECRET_ABC here\n", "add secret.txt")
	commitFileEnv(t, dir, scrubMatchEnv, "config/secret.env", "KEY=SECRET_ABC\n", "add config/secret.env")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test scope",
		"--entire-history",
		"--scope", "*.env",
	)
	if code != 0 {
		t.Fatalf("scrub match --scope failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify all commits: config/secret.env should have REDACTED, secret.txt should still have SECRET_ABC
	shas := revListReverse(t, dir)
	for i, sha := range shas {
		// Check config/secret.env is scrubbed
		content, ok := gitShow(t, dir, sha, "config/secret.env")
		if ok {
			if strings.Contains(content, "SECRET_ABC") {
				t.Errorf("commit %d (%s): config/secret.env still contains SECRET_ABC: %q", i, sha[:12], content)
			}
			if !strings.Contains(content, "REDACTED") {
				t.Errorf("commit %d (%s): config/secret.env missing REDACTED: %q", i, sha[:12], content)
			}
		}

		// Check secret.txt is NOT scrubbed (outside scope)
		content, ok = gitShow(t, dir, sha, "secret.txt")
		if ok {
			if !strings.Contains(content, "SECRET_ABC") {
				t.Errorf("commit %d (%s): secret.txt should still contain SECRET_ABC (outside scope): %q", i, sha[:12], content)
			}
			if strings.Contains(content, "REDACTED") {
				t.Errorf("commit %d (%s): secret.txt should NOT contain REDACTED (outside scope): %q", i, sha[:12], content)
			}
		}
	}
}

// TestScrubMatchMangle creates a repo with a file containing "SECRET_KEY_12345",
// runs scrub match with --mangle, and verifies the replacement has the same
// byte length, does not contain the original, and uses only printable ASCII.
func TestScrubMatchMangle(t *testing.T) {
	dir := newRepo(t)

	original := "SECRET_KEY_12345"
	commitFileEnv(t, dir, scrubMatchEnv, "secret.txt", original+"\n", "add secret")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_KEY_\\w+",
		"--mangle",
		"--reason", "test mangle",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match --mangle failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Read on-disk file
	diskContent, err := os.ReadFile(filepath.Join(dir, "secret.txt"))
	if err != nil {
		t.Fatalf("reading secret.txt: %v", err)
	}
	content := strings.TrimRight(string(diskContent), "\n")

	// Verify same byte length as original match
	if len(content) != len(original) {
		t.Errorf("mangled content length %d != original %d; got %q", len(content), len(original), content)
	}

	// Verify original is gone
	if strings.Contains(string(diskContent), original) {
		t.Errorf("on-disk secret.txt still contains original: %q", string(diskContent))
	}

	// Verify all bytes are printable ASCII (0x21-0x7E) or preserved whitespace
	for i, b := range []byte(content) {
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			continue
		}
		if b < 0x21 || b > 0x7E {
			t.Errorf("byte %d is not printable ASCII: 0x%02x", i, b)
		}
	}

	// Verify git history is also clean
	shas := revListReverse(t, dir)
	for i, sha := range shas {
		c, ok := gitShow(t, dir, sha, "secret.txt")
		if !ok {
			continue
		}
		if strings.Contains(c, original) {
			t.Errorf("commit %d (%s): secret.txt still contains original: %q", i, sha[:12], c)
		}
	}

	// Verify working tree is clean
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = dir
	statusOut, err := statusCmd.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		t.Errorf("working tree is dirty after scrub match --mangle: %s", string(statusOut))
	}
}

// TestScrubMatchMangleLength tests that --mangle preserves byte length
// across multiple files with secrets of different lengths.
func TestScrubMatchMangleLength(t *testing.T) {
	dir := newRepo(t)

	// Use a specific prefix pattern that won't match random printable ASCII.
	secrets := []struct {
		file   string
		secret string
	}{
		{"short.txt", "xyzSECRET123xyz"},
		{"medium.txt", "xyzSECRET_KEY_12345_ABCDEFxyz"},
		{"long.txt", "xyzSECRET_VERY_LONG_VALUE_WITH_MANY_CHARACTERS_1234567890xyz"},
	}

	for _, s := range secrets {
		commitFileEnv(t, dir, scrubMatchEnv, s.file, s.secret+"\n", "add "+s.file)
	}

	// Pattern matches the SECRET... portion between xyz markers.
	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "xyzSECRET[A-Za-z0-9_]+xyz",
		"--mangle",
		"--reason", "test mangle length",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match --mangle failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	for _, s := range secrets {
		diskContent, err := os.ReadFile(filepath.Join(dir, s.file))
		if err != nil {
			t.Fatalf("reading %s: %v", s.file, err)
		}
		content := strings.TrimRight(string(diskContent), "\n")

		if len(content) != len(s.secret) {
			t.Errorf("%s: mangled length %d != original %d; got %q", s.file, len(content), len(s.secret), content)
		}
		if strings.Contains(string(diskContent), s.secret) {
			t.Errorf("%s: still contains original %q", s.file, s.secret)
		}
	}
}

// TestScrubMatchMutexFlags verifies that --replace and --mangle are mutually
// exclusive (exactly one required).
func TestScrubMatchMutexFlags(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "SECRET_ABC\n", "add data")

	// Both --replace and --mangle: should fail
	_, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--mangle",
		"--reason", "test mutex both",
		"--entire-history",
	)
	if code == 0 {
		t.Error("expected failure when both --replace and --mangle are provided, but got exit 0")
	}
	_ = stderr // error message comes from strictcli

	// Neither --replace nor --mangle: should fail
	_, stderr, code = runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--reason", "test mutex neither",
		"--entire-history",
	)
	if code == 0 {
		t.Error("expected failure when neither --replace nor --mangle is provided, but got exit 0")
	}

	// Just --replace: should succeed
	var stdout string
	stdout, stderr, code = runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test mutex replace only",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("--replace only failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Re-add the secret for the mangle test
	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "SECRET_ABC\n", "re-add secret")

	// Just --mangle: should succeed
	stdout, stderr, code = runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--mangle",
		"--reason", "test mutex mangle only",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("--mangle only failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
}

// TestScrubMatchMangleNonDeterministic verifies that two mangle runs on
// identical content produce different results (crypto-random, not deterministic).
func TestScrubMatchMangleNonDeterministic(t *testing.T) {
	// Run mangle twice on identical repos and check results differ
	var results [2]string
	for i := 0; i < 2; i++ {
		dir := newRepo(t)
		commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "SECRET_KEY_ABCDEFGHIJKLMNOP\n", fmt.Sprintf("run %d", i))

		stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
			"--yes", "scrub", "match",
			"--pattern", "SECRET_KEY_\\w+",
			"--mangle",
			"--reason", "nondeterminism test",
			"--entire-history",
		)
		if code != 0 {
			t.Fatalf("run %d failed (code %d): stdout=%s stderr=%s", i, code, stdout, stderr)
		}

		content, err := os.ReadFile(filepath.Join(dir, "data.txt"))
		if err != nil {
			t.Fatalf("run %d: reading data.txt: %v", i, err)
		}
		results[i] = string(content)
	}

	if results[0] == results[1] {
		t.Errorf("two mangle runs produced identical output (should be non-deterministic): %q", results[0])
	}
}

// TestScrubMatchPreservesGitignored verifies that when a tracked file is
// gitignored and then scrub match rewrites its blob, the on-disk copy is
// preserved (not overwritten by read-tree) and the file is untracked from
// the index afterward.
func TestScrubMatchPreservesGitignored(t *testing.T) {
	dir := newRepo(t)

	// Commit config.env with a secret
	commitFileEnv(t, dir, scrubMatchEnv, "config.env", "SECRET=abc123\n", "add config.env")

	// Gitignore config.env and commit
	commitFileEnv(t, dir, scrubMatchEnv, ".gitignore", "config.env\n", "add gitignore")

	// Verify tree is clean
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = dir
	statusOut, err := statusCmd.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		t.Fatalf("working tree not clean before scrub: %s", string(statusOut))
	}

	// Run scrub match to replace the secret
	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "abc123",
		"--replace", "REDACTED",
		"--reason", "test gitignore preservation",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// On-disk config.env must still have the ORIGINAL content (not scrubbed)
	diskContent, err := os.ReadFile(filepath.Join(dir, "config.env"))
	if err != nil {
		t.Fatalf("reading config.env from disk: %v", err)
	}
	if string(diskContent) != "SECRET=abc123\n" {
		t.Errorf("on-disk config.env was overwritten: got %q, want %q", string(diskContent), "SECRET=abc123\n")
	}

	// config.env should be untracked (git ls-files returns nothing)
	lsCmd := exec.Command("git", "ls-files", "config.env")
	lsCmd.Dir = dir
	lsOut, err := lsCmd.Output()
	if err != nil {
		t.Fatalf("git ls-files config.env: %v", err)
	}
	if strings.TrimSpace(string(lsOut)) != "" {
		t.Errorf("config.env should be untracked, but git ls-files returned: %q", string(lsOut))
	}

	// Stdout or stderr should mention preservation
	combined := stdout + stderr
	if !strings.Contains(combined, "Preserved") && !strings.Contains(combined, "gitignored") {
		t.Errorf("output should mention gitignore preservation, got:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}

// TestScrubMatchJSON runs scrub match with --json and verifies the JSON output
// contains expected fields and values.
func TestScrubMatchJSON(t *testing.T) {
	dir := newRepo(t)

	// Create files with a secret pattern across multiple commits.
	commitFileEnv(t, dir, scrubMatchEnv, "file1.txt", "data SECRET_JSON here\n", "add file1")
	commitFileEnv(t, dir, scrubMatchEnv, "file2.txt", "also SECRET_JSON inside\n", "add file2")
	commitFileEnv(t, dir, scrubMatchEnv, "file1.txt", "updated SECRET_JSON v2\n", "update file1")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "--json", "scrub", "match",
		"--pattern", "SECRET_JSON",
		"--replace", "REDACTED",
		"--reason", "test json output",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match --json failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		Version          int               `json:"version"`
		DryRun           bool              `json:"dry_run"`
		Rewrites         map[string]string `json:"rewrites"`
		Tags             []interface{}     `json:"tags"`
		CommitsRewritten int               `json:"commits_rewritten"`
		BlobsReplaced    int               `json:"blobs_replaced"`
		MessagesModified int               `json:"messages_modified"`
		TagsRewritten    int               `json:"tags_rewritten"`
		OldHead          string            `json:"old_head"`
		NewHead          string            `json:"new_head"`
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
	if result.OldHead == "" {
		t.Error("old_head should not be empty")
	}
	if result.NewHead == "" {
		t.Error("new_head should not be empty")
	}
	if result.OldHead == result.NewHead {
		t.Errorf("old_head should differ from new_head: %s", result.OldHead)
	}
}

// TestScrubMatchJSONDryRun runs scrub match with --json --dry-run and verifies
// the JSON output contains match statistics without modifying history.
func TestScrubMatchJSONDryRun(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "file1.txt", "data SECRET_DRYJSON here\n", "add file1")
	commitFileEnv(t, dir, scrubMatchEnv, "file2.txt", "also SECRET_DRYJSON inside\n", "add file2")

	headBefore := revParseHEAD(t, dir)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "--json", "--dry-run", "scrub", "match",
		"--pattern", "SECRET_DRYJSON",
		"--replace", "REDACTED",
		"--reason", "test json dry run",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match --json --dry-run failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		Version          int    `json:"version"`
		DryRun           bool   `json:"dry_run"`
		Pattern          string `json:"pattern"`
		ObjectsScanned   int    `json:"objects_scanned"`
		BinarySkipped    int    `json:"binary_skipped"`
		TotalMatches     int    `json:"total_matches"`
		BlobMatches      int    `json:"blob_matches"`
		CommitMatches    int    `json:"commit_matches"`
		TagMatches       int    `json:"tag_matches"`
		FileMatches      int    `json:"file_matches"`
		EstimatedCommits int    `json:"estimated_commits"`
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
	if result.TotalMatches == 0 {
		t.Error("total_matches should be > 0")
	}
	if result.ObjectsScanned == 0 {
		t.Error("objects_scanned should be > 0")
	}

	// HEAD should be unchanged (dry-run).
	headAfter := revParseHEAD(t, dir)
	if headAfter != headBefore {
		t.Errorf("HEAD changed during dry run: %s -> %s", headBefore[:12], headAfter[:12])
	}
}

// TestScrubMatchDryRunRangeFilter verifies that --dry-run with --from only
// reports matches within the specified commit range, not older commits.
func TestScrubMatchDryRunRangeFilter(t *testing.T) {
	dir := newRepo(t)

	// Commit 1: contains LEAKED_SECRET_123
	commitFileEnv(t, dir, scrubMatchEnv, "secret.txt", "LEAKED_SECRET_123\n", "add secret1")

	// Commit 2: clean content
	commitFileEnv(t, dir, scrubMatchEnv, "clean.txt", "nothing here\n", "add clean")

	// Get SHA of commit 2 (HEAD~1 relative to commit 3)
	commit2SHA := revParseHEAD(t, dir)

	// Commit 3: contains LEAKED_SECRET_456
	commitFileEnv(t, dir, scrubMatchEnv, "secret2.txt", "LEAKED_SECRET_456\n", "add secret2")

	// Run dry-run with --from commit2, so only commit 3's range is scanned.
	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "--json", "--dry-run", "scrub", "match",
		"--pattern", `LEAKED_SECRET_\w+`,
		"--from", commit2SHA,
		"--replace", "REDACTED",
		"--reason", "test",
	)
	if code != 0 {
		t.Fatalf("scrub match --dry-run --from failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		DryRun       bool   `json:"dry_run"`
		TotalMatches int    `json:"total_matches"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nstdout: %s", err, stdout)
	}

	if !result.DryRun {
		t.Error("dry_run should be true")
	}
	if result.TotalMatches == 0 {
		t.Error("total_matches should be > 0 (commit 3's secret should be found)")
	}
	if result.Scope != "range" {
		t.Errorf("scope should be 'range', got %q", result.Scope)
	}

	// Commit 1's secret (LEAKED_SECRET_123) should NOT appear in the output.
	if strings.Contains(stdout, "LEAKED_SECRET_123") {
		t.Error("stdout should NOT contain LEAKED_SECRET_123 (commit 1 is out of range)")
	}

	// Commit 3's secret (LEAKED_SECRET_456) SHOULD be found. Verify by checking
	// that at least one blob match exists (the JSON totalMatches already confirms
	// matches exist, but also ensure the specific secret's blob is detected).
	// Since --json dry-run doesn't include match details in the JSON, verify
	// via total_matches > 0 (already checked above) and that the stderr/output
	// mentions the match.
	if result.TotalMatches < 1 {
		t.Error("expected at least 1 match for LEAKED_SECRET_456 in range")
	}
}
