package test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var scrubVerifyEnv = []string{"CLAUDE_CODE_SESSION_ID=scrub-verify-test"}

// readPolicyFile reads and parses the scrub-policies.jsonl file from the
// repo's .git/safegit/ directory (untracked).
func readPolicyFile(t *testing.T, dir string) []map[string]interface{} {
	t.Helper()
	policyPath := filepath.Join(dir, ".git", "safegit", "scrub-policies.jsonl")
	f, err := os.Open(policyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("opening policy file: %v", err)
	}
	defer f.Close()

	var policies []map[string]interface{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var p map[string]interface{}
		if err := json.Unmarshal(line, &p); err != nil {
			t.Fatalf("parsing policy line: %v", err)
		}
		policies = append(policies, p)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading policy file: %v", err)
	}
	return policies
}

// TestScrubVerifyRoundTrip scrubs a secret with scrub match, then runs
// scrub verify to confirm the secret is gone. The policy file should have
// been auto-created by the scrub.
func TestScrubVerifyRoundTrip(t *testing.T) {
	dir := newRepo(t)

	// Create files with a secret
	commitFileEnv(t, dir, scrubVerifyEnv, "config.txt", "password=SECRET_XYZ_123\n", "add config with secret")
	commitFileEnv(t, dir, scrubVerifyEnv, "data.txt", "key: SECRET_XYZ_123\n", "add data with secret")

	// Scrub the secret
	_, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_XYZ_123",
		"--replace", "REDACTED",
		"--reason", "test round-trip verify",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}

	// Verify the policy file was auto-created
	policies := readPolicyFile(t, dir)
	if len(policies) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(policies))
	}
	p := policies[0]
	if p["type"] != "match" {
		t.Errorf("policy type = %v, want match", p["type"])
	}
	if p["pattern"] != "SECRET_XYZ_123" {
		t.Errorf("policy pattern = %v, want SECRET_XYZ_123", p["pattern"])
	}
	if p["reason"] != "test round-trip verify" {
		t.Errorf("policy reason = %v, want 'test round-trip verify'", p["reason"])
	}
	if p["created_by_op"] != "scrub-match" {
		t.Errorf("policy created_by_op = %v, want scrub-match", p["created_by_op"])
	}
	if p["created_at"] == nil || p["created_at"] == "" {
		t.Error("policy created_at is empty")
	}

	// Run scrub verify -- should pass
	stdout, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv, "scrub", "verify")
	if code != 0 {
		t.Fatalf("scrub verify failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "PASS") {
		t.Errorf("expected PASS in output, got: %s", stdout)
	}
}

// TestScrubVerifyHandWrittenPolicy tests that a manually-written policy
// file is respected by scrub verify.
func TestScrubVerifyHandWrittenPolicy(t *testing.T) {
	dir := newRepo(t)

	// Create a file with a known token
	commitFileEnv(t, dir, scrubVerifyEnv, "app.env", "API_KEY=sk_live_abc123\n", "add env file")

	// Scrub the secret
	_, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"--yes", "scrub", "match",
		"--pattern", "sk_live_abc123",
		"--replace", "REDACTED",
		"--reason", "remove api key",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}

	// Write an additional hand-crafted policy that should also pass
	// (pattern that was never in the repo). The policy file lives in
	// .git/safegit/ (untracked), so no commit is needed.
	policyPath := filepath.Join(dir, ".git", "safegit", "scrub-policies.jsonl")
	f, err := os.OpenFile(policyPath, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("opening policy file for append: %v", err)
	}
	handWritten := `{"type":"match","pattern":"NEVER_EXISTED_TOKEN","reason":"hand-written test policy","created_at":"2026-01-01T00:00:00Z"}` + "\n"
	if _, err := f.WriteString(handWritten); err != nil {
		t.Fatalf("writing hand-crafted policy: %v", err)
	}
	f.Close()

	// Run scrub verify -- both policies should pass
	stdout, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv, "scrub", "verify")
	if code != 0 {
		t.Fatalf("scrub verify failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "2 passed") {
		t.Errorf("expected '2 passed' in output, got: %s", stdout)
	}
}

// TestScrubVerifyDetectsReintroduced scrubs a secret, then re-introduces it
// in a new commit. Verify should detect the re-introduced secret and fail.
func TestScrubVerifyDetectsReintroduced(t *testing.T) {
	dir := newRepo(t)

	// Create file with secret
	commitFileEnv(t, dir, scrubVerifyEnv, "config.txt", "token=SUPER_SECRET_42\n", "add secret")

	// Scrub the secret
	_, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"--yes", "scrub", "match",
		"--pattern", "SUPER_SECRET_42",
		"--replace", "REDACTED",
		"--reason", "remove token",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}

	// Verify it's clean after scrub
	_, _, code = runSafegitEnv(t, dir, scrubVerifyEnv, "scrub", "verify")
	if code != 0 {
		t.Fatal("scrub verify should pass immediately after scrub")
	}

	// Re-introduce the secret in a new commit
	commitFileEnv(t, dir, scrubVerifyEnv, "config.txt", "token=SUPER_SECRET_42\n", "oops reintroduced secret")

	// Verify should now fail
	_, stderr, code = runSafegitEnv(t, dir, scrubVerifyEnv, "scrub", "verify")
	if code == 0 {
		t.Fatal("scrub verify should fail after re-introducing the secret")
	}
	if !strings.Contains(stderr, "FAIL") {
		t.Errorf("expected FAIL in stderr, got: %s", stderr)
	}
}

// TestScrubVerifyEmptyPolicies verifies that scrub verify exits 0 when
// there is no policy file.
func TestScrubVerifyEmptyPolicies(t *testing.T) {
	dir := newRepo(t)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv, "scrub", "verify")
	if code != 0 {
		t.Fatalf("scrub verify failed with no policies (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "No scrub policies found") {
		t.Errorf("expected 'No scrub policies found' in output, got: %s", stdout)
	}
}

// TestScrubVerifyMissingPolicyFile verifies that scrub verify exits 0 when
// the policy file doesn't exist (same as empty, but file is absent entirely).
func TestScrubVerifyMissingPolicyFile(t *testing.T) {
	dir := newRepo(t)

	// Ensure safegit is initialized but no policy file exists
	_, _, _ = runSafegitEnv(t, dir, scrubVerifyEnv, "config", "show")

	_, _, code := runSafegitEnv(t, dir, scrubVerifyEnv, "scrub", "verify")
	if code != 0 {
		t.Fatalf("scrub verify should exit 0 when policy file is absent, got code %d", code)
	}
}

// TestScrubVerifyJSONOutput verifies that --json flag produces structured output.
func TestScrubVerifyJSONOutput(t *testing.T) {
	dir := newRepo(t)

	// Create and scrub a secret
	commitFileEnv(t, dir, scrubVerifyEnv, "secret.txt", "password=MY_SECRET_99\n", "add secret")
	_, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"--yes", "scrub", "match",
		"--pattern", "MY_SECRET_99",
		"--replace", "GONE",
		"--reason", "json output test",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}

	// Run verify with --json
	stdout, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv, "--json", "scrub", "verify")
	if code != 0 {
		t.Fatalf("scrub verify --json failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		Version  int `json:"version"`
		Policies int `json:"policies"`
		Passed   int `json:"passed"`
		Failed   int `json:"failed"`
		Results  []struct {
			Pattern string `json:"pattern"`
			Pass    bool   `json:"pass"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nstdout: %s", err, stdout)
	}
	if result.Version != 1 {
		t.Errorf("version = %d, want 1", result.Version)
	}
	if result.Policies != 1 {
		t.Errorf("policies = %d, want 1", result.Policies)
	}
	if result.Passed != 1 {
		t.Errorf("passed = %d, want 1", result.Passed)
	}
	if result.Failed != 0 {
		t.Errorf("failed = %d, want 0", result.Failed)
	}
	if len(result.Results) != 1 {
		t.Fatalf("results length = %d, want 1", len(result.Results))
	}
	if result.Results[0].Pattern != "MY_SECRET_99" {
		t.Errorf("result pattern = %q, want MY_SECRET_99", result.Results[0].Pattern)
	}
	if !result.Results[0].Pass {
		t.Error("result should pass")
	}
}

// TestScrubVerifyMultiPolicySingleScan creates a repo with two different
// secrets, scrubs both, then verifies both policies pass in a single
// `scrub verify` call. This exercises the optimized single-scan path
// that tests all compiled patterns against each object in one pass.
func TestScrubVerifyMultiPolicySingleScan(t *testing.T) {
	dir := newRepo(t)

	// Create files with two distinct secrets.
	commitFileEnv(t, dir, scrubVerifyEnv, "db.env", "DB_PASS=alpha_secret_one\n", "add db secret")
	commitFileEnv(t, dir, scrubVerifyEnv, "api.env", "API_KEY=beta_secret_two\n", "add api secret")

	// Scrub first secret.
	_, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"--yes", "scrub", "match",
		"--pattern", "alpha_secret_one",
		"--replace", "REDACTED_1",
		"--reason", "remove db password",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match (first) failed (code %d): %s", code, stderr)
	}

	// Scrub second secret.
	_, stderr, code = runSafegitEnv(t, dir, scrubVerifyEnv,
		"--yes", "scrub", "match",
		"--pattern", "beta_secret_two",
		"--replace", "REDACTED_2",
		"--reason", "remove api key",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match (second) failed (code %d): %s", code, stderr)
	}

	// Verify both policies exist.
	policies := readPolicyFile(t, dir)
	if len(policies) != 2 {
		t.Fatalf("expected 2 policies, got %d", len(policies))
	}

	// Run scrub verify -- single call should verify both policies.
	stdout, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv, "scrub", "verify")
	if code != 0 {
		t.Fatalf("scrub verify failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "2 passed") {
		t.Errorf("expected '2 passed' in output, got: %s", stdout)
	}

	// Also verify JSON output reports both policies.
	stdout, stderr, code = runSafegitEnv(t, dir, scrubVerifyEnv, "--json", "scrub", "verify")
	if code != 0 {
		t.Fatalf("scrub verify --json failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		Policies int `json:"policies"`
		Passed   int `json:"passed"`
		Failed   int `json:"failed"`
		Results  []struct {
			Pattern string `json:"pattern"`
			Pass    bool   `json:"pass"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("failed to parse JSON: %v\nstdout: %s", err, stdout)
	}
	if result.Policies != 2 {
		t.Errorf("policies = %d, want 2", result.Policies)
	}
	if result.Passed != 2 {
		t.Errorf("passed = %d, want 2", result.Passed)
	}
	if len(result.Results) != 2 {
		t.Fatalf("results length = %d, want 2", len(result.Results))
	}
	for _, r := range result.Results {
		if !r.Pass {
			t.Errorf("policy %q should pass", r.Pattern)
		}
	}
}

// TestScrubVerifyScopeAutoAppend verifies that scrub match with --scope
// stores the scope in the auto-appended policy.
func TestScrubVerifyScopeAutoAppend(t *testing.T) {
	dir := newRepo(t)

	// Create files in different paths
	commitFileEnv(t, dir, scrubVerifyEnv, "config/db.env", "DB_PASS=secret123\n", "add db config")
	commitFileEnv(t, dir, scrubVerifyEnv, "readme.txt", "no secrets here\n", "add readme")

	// Scrub with scope
	_, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"--yes", "scrub", "match",
		"--pattern", "secret123",
		"--replace", "REDACTED",
		"--reason", "scoped scrub test",
		"--scope", "*.env",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match with scope failed (code %d): %s", code, stderr)
	}

	// Check policy has scope
	policies := readPolicyFile(t, dir)
	if len(policies) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(policies))
	}
	if policies[0]["scope"] != "*.env" {
		t.Errorf("policy scope = %v, want '*.env'", policies[0]["scope"])
	}

	// Verify should pass
	_, _, code = runSafegitEnv(t, dir, scrubVerifyEnv, "scrub", "verify")
	if code != 0 {
		t.Fatal("scrub verify should pass after scoped scrub")
	}
}

