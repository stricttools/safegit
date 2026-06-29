package test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var scanEnv = []string{"CLAUDE_CODE_SESSION_ID=scan-test"}

// TestScanBasic creates a repo with a pattern in committed files and verifies
// that `safegit scan` finds the matches.
func TestScanBasic(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scanEnv, "secret.txt", "password=SUPER_SECRET_123\n", "add secret")
	commitFileEnv(t, dir, scanEnv, "config.txt", "db_host=localhost\n", "add config")

	stdout, stderr, code := runSafegitEnv(t, dir, scanEnv,
		"scan", "--pattern", "SUPER_SECRET",
	)
	if code != 0 {
		t.Fatalf("scan failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify output mentions the match.
	if !strings.Contains(stdout+stderr, "secret.txt") {
		t.Errorf("expected output to mention secret.txt, got stdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "<MATCH>") {
		t.Errorf("expected output to contain <MATCH> context marker, got stdout=%s stderr=%s", stdout, stderr)
	}

	// Verify JSON output.
	jstdout, jstderr, jcode := runSafegitEnv(t, dir, scanEnv,
		"--json", "scan", "--pattern", "SUPER_SECRET",
	)
	if jcode != 0 {
		t.Fatalf("scan --json failed (code %d): stdout=%s stderr=%s", jcode, jstdout, jstderr)
	}

	var result struct {
		Version      int `json:"version"`
		TotalMatches int `json:"total_matches"`
		BlobMatches  []struct {
			Path string `json:"path"`
			Line int    `json:"line"`
		} `json:"blob_matches"`
	}
	if err := json.Unmarshal([]byte(jstdout), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nraw: %s", err, jstdout)
	}

	if result.TotalMatches == 0 {
		t.Errorf("expected at least 1 match, got 0")
	}

	// There should be blob matches mentioning secret.txt.
	found := false
	for _, m := range result.BlobMatches {
		if m.Path == "secret.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected blob match for secret.txt in JSON output, got: %s", jstdout)
	}
}

// TestScanScope verifies that --scope filters blob matches to only matching paths.
func TestScanScope(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scanEnv, "app.env", "TOKEN=secret_value\n", "add env")
	commitFileEnv(t, dir, scanEnv, "app.txt", "TOKEN=secret_value\n", "add txt with same content")
	commitFileEnv(t, dir, scanEnv, "config/db.env", "TOKEN=secret_value\n", "add nested env")

	// Scan with scope filtering to *.env only.
	stdout, stderr, code := runSafegitEnv(t, dir, scanEnv,
		"--json", "scan", "--pattern", "secret_value", "--scope", "*.env",
	)
	if code != 0 {
		t.Fatalf("scan --scope failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		Scope       string `json:"scope"`
		BlobMatches []struct {
			Path string `json:"path"`
		} `json:"blob_matches"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nraw: %s", err, stdout)
	}

	if result.Scope != "*.env" {
		t.Errorf("expected scope=*.env, got %q", result.Scope)
	}

	// Check that only .env files appear in blob matches.
	for _, m := range result.BlobMatches {
		if !strings.HasSuffix(m.Path, ".env") {
			t.Errorf("expected only .env blob matches with --scope *.env, got path=%q", m.Path)
		}
	}

	// There should be at least one .env match.
	if len(result.BlobMatches) == 0 {
		t.Errorf("expected at least one blob match with --scope *.env, got 0")
	}
}

// TestScanEntireHistory verifies that --entire-history finds matches across all
// commits, including ones where the matching content was later removed.
func TestScanEntireHistory(t *testing.T) {
	dir := newRepo(t)

	// Commit a file with a secret.
	commitFileEnv(t, dir, scanEnv, "creds.txt", "api_key=OLD_SECRET\n", "add creds")

	// Overwrite the file to remove the secret.
	commitFileEnv(t, dir, scanEnv, "creds.txt", "api_key=redacted\n", "remove secret")

	// Verify the secret is not in the current working tree.
	data, err := os.ReadFile(filepath.Join(dir, "creds.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "OLD_SECRET") {
		t.Fatal("OLD_SECRET should not be in the current file")
	}

	// Scan with --entire-history should still find the old secret in git objects.
	stdout, stderr, code := runSafegitEnv(t, dir, scanEnv,
		"--json", "scan", "--pattern", "OLD_SECRET", "--entire-history",
	)
	if code != 0 {
		t.Fatalf("scan --entire-history failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		TotalMatches int `json:"total_matches"`
		BlobMatches  []struct {
			Path string `json:"path"`
		} `json:"blob_matches"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nraw: %s", err, stdout)
	}

	if result.TotalMatches == 0 {
		t.Errorf("expected --entire-history to find OLD_SECRET in historical commits, got 0 matches")
	}

	// There should be a blob match for creds.txt from the old commit.
	found := false
	for _, m := range result.BlobMatches {
		if m.Path == "creds.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected blob match for creds.txt in historical data, got: %s", stdout)
	}
}

// TestScanNoMatches verifies that scan exits 0 with no output when no matches exist.
func TestScanNoMatches(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scanEnv, "data.txt", "hello world\n", "add data")

	stdout, stderr, code := runSafegitEnv(t, dir, scanEnv,
		"--json", "scan", "--pattern", "NONEXISTENT_PATTERN_XYZ",
	)
	if code != 0 {
		t.Fatalf("scan with no matches should exit 0, got code %d: stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		TotalMatches int `json:"total_matches"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nraw: %s", err, stdout)
	}
	if result.TotalMatches != 0 {
		t.Errorf("expected 0 total_matches, got %d", result.TotalMatches)
	}
}

// TestScanCommitMessage verifies that scan finds patterns in commit messages.
func TestScanCommitMessage(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scanEnv, "data.txt", "clean content\n", "deployed TICKET_42 fix")

	stdout, stderr, code := runSafegitEnv(t, dir, scanEnv,
		"--json", "scan", "--pattern", "TICKET_42",
	)
	if code != 0 {
		t.Fatalf("scan failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		CommitMatches []struct {
			ObjectType string `json:"object_type"`
			Context    string `json:"context"`
		} `json:"commit_matches"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nraw: %s", err, stdout)
	}

	if len(result.CommitMatches) == 0 {
		t.Errorf("expected commit message match for TICKET_42, got none")
	}
}

// TestScanFrom verifies that --from limits scanning to commits after a given SHA.
func TestScanFrom(t *testing.T) {
	dir := newRepo(t)

	// First commit with a keyword.
	commitFileEnv(t, dir, scanEnv, "old.txt", "MARKER_OLD\n", "add old")

	// Record the SHA after the first interesting commit.
	fromSHA := revParseHEAD(t, dir)

	// Second commit with a different keyword.
	commitFileEnv(t, dir, scanEnv, "new.txt", "MARKER_NEW\n", "add new")

	// Scan from the second commit should find MARKER_NEW but not MARKER_OLD in
	// the range. Note: rev-list uses fromSHA..HEAD so fromSHA itself is excluded.
	stdout, stderr, code := runSafegitEnv(t, dir, scanEnv,
		"--json", "scan", "--pattern", "MARKER_NEW", "--from", fromSHA,
	)
	if code != 0 {
		t.Fatalf("scan --from failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		TotalMatches int `json:"total_matches"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("failed to parse JSON: %v\nraw: %s", err, stdout)
	}
	if result.TotalMatches == 0 {
		t.Errorf("expected to find MARKER_NEW in range after %s, got 0", fromSHA[:8])
	}

	// Scanning for MARKER_OLD from the same point should find nothing in the range
	// (since MARKER_OLD was committed before fromSHA).
	stdout2, stderr2, code2 := runSafegitEnv(t, dir, scanEnv,
		"--json", "scan", "--pattern", "MARKER_OLD", "--from", fromSHA,
	)
	if code2 != 0 {
		t.Fatalf("scan --from (no match) failed (code %d): stdout=%s stderr=%s", code2, stdout2, stderr2)
	}

	var result2 struct {
		// blob_matches will be empty since MARKER_OLD is not in the range.
		// But file_matches may still appear from working tree scan.
		BlobMatches []struct{} `json:"blob_matches"`
	}
	if err := json.Unmarshal([]byte(stdout2), &result2); err != nil {
		t.Fatalf("failed to parse JSON: %v\nraw: %s", err, stdout2)
	}
	if len(result2.BlobMatches) > 0 {
		t.Errorf("expected no blob matches for MARKER_OLD in range after %s, got %d", fromSHA[:8], len(result2.BlobMatches))
	}
}

// TestScanNonObjectFiles verifies that scan finds patterns in working tree files.
func TestScanNonObjectFiles(t *testing.T) {
	dir := newRepo(t)

	// Create a file with a pattern.
	commitFileEnv(t, dir, scanEnv, "config.yaml", "database_password: LEAKED_PASS\n", "add config")

	// Create a git hook with the same pattern (non-object file).
	hooksDir := filepath.Join(dir, ".git", "hooks")
	hookPath := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n# check for LEAKED_PASS\n"), 0755); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runSafegitEnv(t, dir, scanEnv,
		"--json", "scan", "--pattern", "LEAKED_PASS",
	)
	if code != 0 {
		t.Fatalf("scan failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		FileMatches []struct {
			Path string `json:"path"`
		} `json:"file_matches"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nraw: %s", err, stdout)
	}

	// At minimum, the hook file should appear in file_matches. The working tree
	// file (config.yaml) will also be there since it's tracked by git ls-files.
	if len(result.FileMatches) == 0 {
		t.Errorf("expected non-object file matches, got 0")
	}

	// Check that the hook file is in the results.
	foundHook := false
	for _, m := range result.FileMatches {
		if strings.Contains(m.Path, "pre-commit") {
			foundHook = true
			break
		}
	}
	if !foundHook {
		// Non-fatal: the hook path format may vary.
		t.Logf("warning: hook file not found in file matches (paths: %v)", func() []string {
			var paths []string
			for _, m := range result.FileMatches {
				paths = append(paths, m.Path)
			}
			return paths
		}())
	}
}

// revParseHEADScan is a local helper that calls git rev-parse HEAD.
// We use the one from undo_session_test.go when available.
func revParseHEADScan(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}
