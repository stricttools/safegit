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

func TestAuthorList(t *testing.T) {
	dir := newRepo(t) // 1 initial commit by "Test" <test@test.com>

	stdout, stderr, code := runSafegit(t, dir, "author", "list")
	if code != 0 {
		t.Fatalf("author list failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Should contain the test repo's identity.
	if !strings.Contains(stdout, "Test") {
		t.Errorf("output should contain 'Test', got: %s", stdout)
	}
	if !strings.Contains(stdout, "test@test.com") {
		t.Errorf("output should contain 'test@test.com', got: %s", stdout)
	}

	// Should have a header row.
	if !strings.Contains(stdout, "Name") || !strings.Contains(stdout, "Email") || !strings.Contains(stdout, "Role") || !strings.Contains(stdout, "Count") {
		t.Errorf("output should contain table headers (Name, Email, Role, Count), got: %s", stdout)
	}
}

func TestAuthorListJSON(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "alice", "alice@test.com", 3, "listjson")

	stdout, stderr, code := runSafegit(t, dir, "--json", "author", "list")
	if code != 0 {
		t.Fatalf("author list --json failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var entries []struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Role  string `json:"role"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nstdout: %s", err, stdout)
	}

	if len(entries) == 0 {
		t.Fatal("expected at least one identity entry")
	}

	// Should have alice and Test.
	foundAlice := false
	foundTest := false
	for _, e := range entries {
		if e.Name == "alice" && e.Email == "alice@test.com" {
			foundAlice = true
			if e.Count == 0 {
				t.Error("alice's count should be > 0")
			}
			if e.Role == "" {
				t.Error("alice's role should not be empty")
			}
		}
		if e.Name == "Test" && e.Email == "test@test.com" {
			foundTest = true
		}
	}
	if !foundAlice {
		t.Errorf("expected to find alice in entries: %+v", entries)
	}
	if !foundTest {
		t.Errorf("expected to find Test in entries: %+v", entries)
	}
}

func TestAuthorListMultipleIdentities(t *testing.T) {
	dir := newRepo(t) // Initial commit by "Test" <test@test.com>
	makeCommits(t, dir, "alice", "alice@work.com", 3, "alice")
	makeCommits(t, dir, "bob", "bob@work.com", 2, "bob")

	stdout, stderr, code := runSafegit(t, dir, "--json", "author", "list")
	if code != 0 {
		t.Fatalf("author list --json failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var entries []struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Role  string `json:"role"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nstdout: %s", err, stdout)
	}

	// Expect at least 3 identities: Test, alice, bob.
	if len(entries) < 3 {
		t.Errorf("expected at least 3 distinct identities, got %d: %+v", len(entries), entries)
	}

	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
	}
	for _, want := range []string{"Test", "alice", "bob"} {
		if !names[want] {
			t.Errorf("expected identity %q not found in entries: %+v", want, entries)
		}
	}

	// Should be sorted by count descending: alice (3 commits, author+committer)
	// should appear before bob (2 commits).
	aliceIdx, bobIdx := -1, -1
	for i, e := range entries {
		if e.Name == "alice" {
			aliceIdx = i
		}
		if e.Name == "bob" {
			bobIdx = i
		}
	}
	if aliceIdx >= 0 && bobIdx >= 0 && aliceIdx > bobIdx {
		t.Errorf("alice (more commits) should appear before bob in sorted output: alice@%d, bob@%d", aliceIdx, bobIdx)
	}
}

func TestAuthorCheckPass(t *testing.T) {
	dir := newRepo(t)
	// All commits use the same identity as git config (Test / test@test.com).

	stdout, stderr, code := runSafegit(t, dir, "author", "check", "--name", "Test", "--email", "test@test.com")
	if code != 0 {
		t.Fatalf("author check should pass (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	if !strings.Contains(stdout, "All commits match") {
		t.Errorf("expected success message, got: %s", stdout)
	}
}

func TestAuthorCheckFail(t *testing.T) {
	dir := newRepo(t) // Initial commit by "Test" <test@test.com>
	makeCommits(t, dir, "alice", "alice@test.com", 3, "deviant")

	// Check for "Test" identity -- alice's commits should deviate.
	stdout, stderr, code := runSafegit(t, dir, "author", "check", "--name", "Test")
	if code != 1 {
		t.Fatalf("author check should fail with exit 1 (got %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Output should mention deviations.
	if !strings.Contains(stdout, "deviating") {
		t.Errorf("output should mention deviations, got: %s", stdout)
	}

	// Should suggest a rewrite command.
	if !strings.Contains(stdout, "safegit author rewrite") {
		t.Errorf("output should suggest safegit author rewrite, got: %s", stdout)
	}
}

func TestAuthorCheckJSON(t *testing.T) {
	dir := newRepo(t)
	// Add commits with a different identity.
	makeCommits(t, dir, "alice", "alice@test.com", 2, "checkjson")

	stdout, stderr, code := runSafegit(t, dir, "--json", "author", "check", "--name", "Test", "--email", "test@test.com")
	if code != 1 {
		t.Fatalf("author check --json should fail with exit 1 (got %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		Expected struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"expected"`
		Deviations []struct {
			SHA            string `json:"sha"`
			AuthorName     string `json:"author_name"`
			AuthorEmail    string `json:"author_email"`
			CommitterName  string `json:"committer_name"`
			CommitterEmail string `json:"committer_email"`
		} `json:"deviations"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nstdout: %s", err, stdout)
	}

	if result.Expected.Name != "Test" {
		t.Errorf("expected.name: got %q, want %q", result.Expected.Name, "Test")
	}
	if result.Expected.Email != "test@test.com" {
		t.Errorf("expected.email: got %q, want %q", result.Expected.Email, "test@test.com")
	}

	if len(result.Deviations) == 0 {
		t.Fatal("expected deviations, got none")
	}

	// All deviations should be from alice.
	for _, d := range result.Deviations {
		if d.AuthorName != "alice" {
			t.Errorf("deviation author_name: got %q, want %q", d.AuthorName, "alice")
		}
		if d.AuthorEmail != "alice@test.com" {
			t.Errorf("deviation author_email: got %q, want %q", d.AuthorEmail, "alice@test.com")
		}
		if d.SHA == "" {
			t.Error("deviation sha should not be empty")
		}
		// SHA should be a full 40-char hash.
		if len(d.SHA) != 40 {
			t.Errorf("deviation sha should be 40 chars, got %d: %q", len(d.SHA), d.SHA)
		}
	}
}

func TestAuthorCheckNameOnly(t *testing.T) {
	dir := newRepo(t)
	makeCommits(t, dir, "bob", "bob@test.com", 2, "nameonly")

	// Check --name only (no --email).
	_, _, code := runSafegit(t, dir, "author", "check", "--name", "Test")
	if code != 1 {
		t.Errorf("author check --name should fail when deviations exist, got exit %d", code)
	}

	// All commits match name "bob" -- but initial commit is "Test".
	_, _, code = runSafegit(t, dir, "author", "check", "--name", "bob")
	if code != 1 {
		// Initial commit is "Test", so there should be one deviation.
		t.Errorf("author check --name bob should fail (initial commit is Test), got exit %d", code)
	}
}

func TestAuthorCheckEmailOnly(t *testing.T) {
	dir := newRepo(t)

	// Check --email only. Initial commit uses test@test.com.
	_, _, code := runSafegit(t, dir, "author", "check", "--email", "test@test.com")
	if code != 0 {
		t.Errorf("author check --email should pass when all commits match, got exit %d", code)
	}

	// Add commits with a different email.
	makeCommits(t, dir, "Test", "other@test.com", 2, "emailonly")
	_, _, code = runSafegit(t, dir, "author", "check", "--email", "test@test.com")
	if code != 1 {
		t.Errorf("author check --email should fail when deviations exist, got exit %d", code)
	}
}

func TestAuthorCheckMissingFlags(t *testing.T) {
	dir := newRepo(t)

	// No --name or --email should fail.
	_, stderr, code := runSafegit(t, dir, "author", "check")
	if code == 0 {
		t.Error("author check with no flags should fail, but exited 0")
	}
	if !strings.Contains(stderr, "at least one of --name or --email") {
		t.Errorf("expected missing-flag error message, got: %s", stderr)
	}
}

func TestAuthorCheckPassJSON(t *testing.T) {
	dir := newRepo(t) // Only "Test" <test@test.com>

	stdout, stderr, code := runSafegit(t, dir, "--json", "author", "check", "--name", "Test")
	if code != 0 {
		t.Fatalf("author check --json should pass (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		Deviations []interface{} `json:"deviations"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("failed to parse JSON: %v\nstdout: %s", err, stdout)
	}
	if len(result.Deviations) != 0 {
		t.Errorf("expected 0 deviations, got %d", len(result.Deviations))
	}
}

// makeCommitsWithDifferentCommitter creates commits where author and committer
// differ, for testing identity separation.
func makeCommitsWithDifferentCommitter(t *testing.T, repoDir string, authorName, authorEmail, committerName, committerEmail string, n int, prefix string) {
	t.Helper()
	for i := 0; i < n; i++ {
		fname := fmt.Sprintf("%s%d.txt", prefix, i)
		fpath := filepath.Join(repoDir, fname)
		if err := os.WriteFile(fpath, []byte(fname), 0644); err != nil {
			t.Fatalf("writing %s: %v", fname, err)
		}

		cmd := exec.Command("git", "add", fname)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git add %s: %v\n%s", fname, err, out)
		}

		cmd = exec.Command("git", "commit", "-m", prefix+" commit "+fmt.Sprintf("%d", i))
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME="+authorName,
			"GIT_AUTHOR_EMAIL="+authorEmail,
			"GIT_COMMITTER_NAME="+committerName,
			"GIT_COMMITTER_EMAIL="+committerEmail,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit: %v\n%s", err, out)
		}
	}
}
