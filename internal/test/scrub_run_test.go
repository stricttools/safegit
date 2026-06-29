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

var scrubRunEnv = []string{"CLAUDE_CODE_SESSION_ID=scrub-run-test"}

// writeRecipe writes a TOML recipe file to a temp directory (outside the repo)
// and returns the full path. This avoids dirtying the working tree.
func writeRecipe(t *testing.T, filename, content string) string {
	t.Helper()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing recipe file: %v", err)
	}
	return path
}

// TestScrubRunBasic creates a repo with SECRET_ABC in multiple files and uses a
// single-operation recipe (equivalent to scrub match) to verify history is
// rewritten correctly.
func TestScrubRunBasic(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubRunEnv, "file1.txt", "data SECRET_ABC here\n", "add file1")
	commitFileEnv(t, dir, scrubRunEnv, "file2.txt", "also SECRET_ABC inside\n", "add file2")
	commitFileEnv(t, dir, scrubRunEnv, "file1.txt", "updated SECRET_ABC v2\n", "update file1")

	recipe := writeRecipe(t, "recipe.toml", `
[[operations]]
pattern = "SECRET_ABC"
replace = "REDACTED"
`)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubRunEnv,
		"--yes", "scrub", "run",
		"--reason", "test basic recipe",
		"--entire-history",
		recipe,
	)
	if code != 0 {
		t.Fatalf("scrub run failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify all commits have REDACTED instead of SECRET_ABC
	shas := revListReverse(t, dir)
	for i, sha := range shas {
		for _, fname := range []string{"file1.txt", "file2.txt"} {
			content, ok := gitShow(t, dir, sha, fname)
			if !ok {
				continue
			}
			if strings.Contains(content, "SECRET_ABC") {
				t.Errorf("commit %d (%s): %s still contains SECRET_ABC: %q", i, sha[:12], fname, content)
			}
			if strings.Contains(content, "REDACTED") {
				// Expected
			}
		}
	}

	// Verify on-disk files
	for _, fname := range []string{"file1.txt", "file2.txt"} {
		diskContent, err := os.ReadFile(filepath.Join(dir, fname))
		if err != nil {
			t.Fatalf("reading %s from disk: %v", fname, err)
		}
		if strings.Contains(string(diskContent), "SECRET_ABC") {
			t.Errorf("on-disk %s still contains SECRET_ABC: %q", fname, string(diskContent))
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
		t.Errorf("working tree is dirty after scrub run: %s", string(statusOut))
	}
}

// TestScrubRunMultiOp creates a repo with two independent secrets and uses a
// two-operation recipe to remove both in a single pass.
func TestScrubRunMultiOp(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubRunEnv, "config.txt", "api_key=secret123 db_pass=hunter2\n", "add config")
	commitFileEnv(t, dir, scrubRunEnv, "config.txt", "api_key=secret456 db_pass=hunter3\n", "update config")

	recipe := writeRecipe(t, "recipe.toml", `
[[operations]]
pattern = "secret[0-9]+"
replace = "REDACTED"

[[operations]]
pattern = "hunter[0-9]+"
replace = "REDACTED"
`)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubRunEnv,
		"--yes", "scrub", "run",
		"--reason", "test multi-op recipe",
		"--entire-history",
		recipe,
	)
	if code != 0 {
		t.Fatalf("scrub run failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify all commits have both secrets replaced
	shas := revListReverse(t, dir)
	for i, sha := range shas {
		content, ok := gitShow(t, dir, sha, "config.txt")
		if !ok {
			continue
		}
		if strings.Contains(content, "secret123") || strings.Contains(content, "secret456") {
			t.Errorf("commit %d (%s): config.txt still contains API key secret: %q", i, sha[:12], content)
		}
		if strings.Contains(content, "hunter2") || strings.Contains(content, "hunter3") {
			t.Errorf("commit %d (%s): config.txt still contains DB password: %q", i, sha[:12], content)
		}
		if !strings.Contains(content, "api_key=REDACTED") {
			t.Errorf("commit %d (%s): config.txt missing api_key=REDACTED: %q", i, sha[:12], content)
		}
		if !strings.Contains(content, "db_pass=REDACTED") {
			t.Errorf("commit %d (%s): config.txt missing db_pass=REDACTED: %q", i, sha[:12], content)
		}
	}
}

// TestScrubRunDependsOn creates a recipe with chained operations via depends_on.
// Op 0 replaces "SECRET" with "HIDDEN", then op 1 (depends on op 0) replaces
// "HIDDEN" with "REDACTED". The end result should have "REDACTED".
func TestScrubRunDependsOn(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubRunEnv, "data.txt", "value=SECRET\n", "add data")

	recipe := writeRecipe(t, "recipe.toml", `
[[operations]]
pattern = "SECRET"
replace = "HIDDEN"

[[operations]]
pattern = "HIDDEN"
replace = "REDACTED"
depends_on = [0]
`)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubRunEnv,
		"--yes", "scrub", "run",
		"--reason", "test depends_on",
		"--entire-history",
		recipe,
	)
	if code != 0 {
		t.Fatalf("scrub run failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify the final result is REDACTED (not HIDDEN or SECRET)
	shas := revListReverse(t, dir)
	for i, sha := range shas {
		content, ok := gitShow(t, dir, sha, "data.txt")
		if !ok {
			continue
		}
		if strings.Contains(content, "SECRET") {
			t.Errorf("commit %d (%s): data.txt still contains SECRET: %q", i, sha[:12], content)
		}
		if strings.Contains(content, "HIDDEN") {
			t.Errorf("commit %d (%s): data.txt still contains HIDDEN (intermediate): %q", i, sha[:12], content)
		}
		if !strings.Contains(content, "REDACTED") {
			t.Errorf("commit %d (%s): data.txt missing REDACTED: %q", i, sha[:12], content)
		}
	}
}

// TestScrubRunOverlapError creates a recipe with two independent operations
// whose patterns overlap on the same byte ranges, which should produce a
// hard error.
func TestScrubRunOverlapError(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubRunEnv, "data.txt", "SECRET_KEY_123\n", "add data")

	// Both patterns match overlapping ranges on "SECRET_KEY_123":
	// Op 0 matches "SECRET_KEY" and op 1 matches "KEY_123".
	recipe := writeRecipe(t, "recipe.toml", `
[[operations]]
pattern = "SECRET_KEY"
replace = "REDACTED1"

[[operations]]
pattern = "KEY_123"
replace = "REDACTED2"
`)

	_, stderr, code := runSafegitEnv(t, dir, scrubRunEnv,
		"--yes", "scrub", "run",
		"--reason", "test overlap error",
		"--entire-history",
		recipe,
	)

	if code == 0 {
		t.Fatal("expected scrub run to fail with overlapping operations, but exited 0")
	}
	if !strings.Contains(stderr, "overlapping") {
		t.Errorf("error should mention overlapping, got: %s", stderr)
	}
}

// TestScrubRunDiffPreview verifies that --diff shows diffs without modifying
// any objects in the repository.
func TestScrubRunDiffPreview(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubRunEnv, "secret.txt", "password=SECRET_ABC here\n", "add secret")
	commitFileEnv(t, dir, scrubRunEnv, "secret.txt", "password=SECRET_ABC updated\n", "update secret")
	headBefore := revParseHEAD(t, dir)

	recipe := writeRecipe(t, "recipe.toml", `
[[operations]]
pattern = "SECRET_ABC"
replace = "REDACTED"
`)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubRunEnv,
		"--yes", "scrub", "run",
		"--diff",
		"--reason", "test diff preview",
		"--entire-history",
		recipe,
	)
	if code != 0 {
		t.Fatalf("scrub run --diff failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// HEAD should be unchanged
	headAfter := revParseHEAD(t, dir)
	if headAfter != headBefore {
		t.Errorf("HEAD changed during --diff preview: %s -> %s", headBefore[:12], headAfter[:12])
	}

	// Output should show diff content
	combined := stdout + stderr
	if !strings.Contains(combined, "SECRET_ABC") && !strings.Contains(combined, "REDACTED") {
		t.Errorf("--diff output should contain before/after content, got: %s", combined)
	}
	if !strings.Contains(combined, "Diff preview:") {
		t.Errorf("--diff output should contain diff preview summary, got: %s", combined)
	}

	// Verify on-disk file is unchanged
	diskContent, err := os.ReadFile(filepath.Join(dir, "secret.txt"))
	if err != nil {
		t.Fatalf("reading secret.txt: %v", err)
	}
	if !strings.Contains(string(diskContent), "SECRET_ABC") {
		t.Errorf("on-disk secret.txt should still contain SECRET_ABC: %q", string(diskContent))
	}
}

// TestScrubRunJSON verifies the JSON output structure of scrub run.
func TestScrubRunJSON(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubRunEnv, "file1.txt", "data SECRET_JSON here\n", "add file1")
	commitFileEnv(t, dir, scrubRunEnv, "file2.txt", "also SECRET_JSON inside\n", "add file2")

	recipe := writeRecipe(t, "recipe.toml", `
[[operations]]
pattern = "SECRET_JSON"
replace = "REDACTED"
`)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubRunEnv,
		"--yes", "--json", "scrub", "run",
		"--reason", "test json output",
		"--entire-history",
		recipe,
	)
	if code != 0 {
		t.Fatalf("scrub run --json failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
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
		OperationCount   int               `json:"operation_count"`
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
	if result.CommitsRewritten == 0 {
		t.Error("commits_rewritten should be > 0")
	}
	if result.BlobsReplaced == 0 {
		t.Error("blobs_replaced should be > 0")
	}
	if result.OperationCount != 1 {
		t.Errorf("operation_count: got %d, want 1", result.OperationCount)
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

// TestScrubRunDiffNoObjectWrites verifies that --diff does not write any objects
// to the git object store.
func TestScrubRunDiffNoObjectWrites(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubRunEnv, "secret.txt", "password=SECRET_OBJ here\n", "add secret")
	commitFileEnv(t, dir, scrubRunEnv, "secret.txt", "password=SECRET_OBJ updated\n", "update secret")

	// Count objects before --diff
	countBefore := countGitObjects(t, dir)

	recipe := writeRecipe(t, "recipe.toml", `
[[operations]]
pattern = "SECRET_OBJ"
replace = "REDACTED"
`)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubRunEnv,
		"--yes", "scrub", "run",
		"--diff",
		"--reason", "test no object writes",
		"--entire-history",
		recipe,
	)
	if code != 0 {
		t.Fatalf("scrub run --diff failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Count objects after --diff
	countAfter := countGitObjects(t, dir)

	if countAfter != countBefore {
		t.Errorf("--diff wrote objects: before=%d after=%d", countBefore, countAfter)
	}
}

// countGitObjects returns the number of objects in the git object store
// (both loose and packed).
func countGitObjects(t *testing.T, dir string) int {
	t.Helper()
	cmd := exec.Command("git", "count-objects", "-v")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git count-objects: %v", err)
	}
	// Parse "count: N" (loose) and "in-pack: M" (packed) lines
	total := 0
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "count: ") {
			var n int
			fmt.Sscanf(line, "count: %d", &n)
			total += n
		}
		if strings.HasPrefix(line, "in-pack: ") {
			var n int
			fmt.Sscanf(line, "in-pack: %d", &n)
			total += n
		}
	}
	return total
}

// TestScrubRunDryRun verifies that --dry-run shows per-operation match counts
// and does not write any objects to the git object store.
func TestScrubRunDryRun(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubRunEnv, "config.txt", "api_key=secret123 db_pass=hunter2\n", "add config")
	commitFileEnv(t, dir, scrubRunEnv, "config.txt", "api_key=secret456 db_pass=hunter3\n", "update config")

	headBefore := revParseHEAD(t, dir)
	countBefore := countGitObjects(t, dir)

	recipe := writeRecipe(t, "recipe.toml", `
[[operations]]
pattern = "secret[0-9]+"
replace = "REDACTED"

[[operations]]
pattern = "hunter[0-9]+"
replace = "REDACTED"
`)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubRunEnv,
		"--dry-run", "--yes", "scrub", "run",
		"--reason", "test dry-run",
		"--entire-history",
		recipe,
	)
	if code != 0 {
		t.Fatalf("scrub run --dry-run failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// HEAD should be unchanged.
	headAfter := revParseHEAD(t, dir)
	if headAfter != headBefore {
		t.Errorf("HEAD changed during --dry-run: %s -> %s", headBefore[:12], headAfter[:12])
	}

	// No objects should be written.
	countAfter := countGitObjects(t, dir)
	if countAfter != countBefore {
		t.Errorf("--dry-run wrote objects: before=%d after=%d", countBefore, countAfter)
	}

	// Output should contain per-operation match counts.
	combined := stdout + stderr
	if !strings.Contains(combined, "Operation 0") {
		t.Errorf("output should contain Operation 0 summary, got: %s", combined)
	}
	if !strings.Contains(combined, "Operation 1") {
		t.Errorf("output should contain Operation 1 summary, got: %s", combined)
	}
	if !strings.Contains(combined, "blob") {
		t.Errorf("output should mention blob matches, got: %s", combined)
	}
	if !strings.Contains(combined, "Affected files") {
		t.Errorf("output should mention affected files, got: %s", combined)
	}

	// On-disk file should be unchanged.
	diskContent, err := os.ReadFile(filepath.Join(dir, "config.txt"))
	if err != nil {
		t.Fatalf("reading config.txt: %v", err)
	}
	if !strings.Contains(string(diskContent), "secret456") {
		t.Errorf("on-disk config.txt should still contain secret456: %q", string(diskContent))
	}
}

// TestScrubRunDryRunJSON verifies the JSON output structure of --dry-run,
// including per-operation match counts.
func TestScrubRunDryRunJSON(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubRunEnv, "config.txt", "api_key=secret123 db_pass=hunter2\n", "add config")
	commitFileEnv(t, dir, scrubRunEnv, "config.txt", "api_key=secret456 db_pass=hunter3\n", "update config")

	recipe := writeRecipe(t, "recipe.toml", `
[[operations]]
pattern = "secret[0-9]+"
replace = "REDACTED"

[[operations]]
pattern = "hunter[0-9]+"
replace = "REDACTED"
`)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubRunEnv,
		"--dry-run", "--json", "--yes", "scrub", "run",
		"--reason", "test dry-run json",
		"--entire-history",
		recipe,
	)
	if code != 0 {
		t.Fatalf("scrub run --dry-run --json failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		Version            int  `json:"version"`
		DryRun             bool `json:"dry_run"`
		OperationCount     int  `json:"operation_count"`
		Operations         []struct {
			Index         int      `json:"index"`
			Pattern       string   `json:"pattern"`
			BlobMatches   int      `json:"blob_matches"`
			CommitMatches int      `json:"commit_matches"`
			TagMatches    int      `json:"tag_matches"`
			AffectedFiles []string `json:"affected_files"`
		} `json:"operations"`
		TotalBlobMatches   int `json:"total_blob_matches"`
		TotalCommitMatches int `json:"total_commit_matches"`
		TotalTagMatches    int `json:"total_tag_matches"`
		TotalAffectedFiles int `json:"total_affected_files"`
		EstimatedCommits   int `json:"estimated_commits"`
		ObjectsScanned     int `json:"objects_scanned"`
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
	if result.OperationCount != 2 {
		t.Errorf("operation_count: got %d, want 2", result.OperationCount)
	}
	if len(result.Operations) != 2 {
		t.Fatalf("expected 2 operations in output, got %d", len(result.Operations))
	}

	// Operation 0: secret[0-9]+ should have blob matches (2 versions of config.txt).
	op0 := result.Operations[0]
	if op0.BlobMatches == 0 {
		t.Error("operation 0: expected blob matches for secret pattern")
	}
	if op0.Pattern != "secret[0-9]+" {
		t.Errorf("operation 0 pattern: got %q, want %q", op0.Pattern, "secret[0-9]+")
	}

	// Operation 1: hunter[0-9]+ should also have blob matches.
	op1 := result.Operations[1]
	if op1.BlobMatches == 0 {
		t.Error("operation 1: expected blob matches for hunter pattern")
	}

	// Total matches should be the sum of per-operation matches.
	expectedTotalBlobs := op0.BlobMatches + op1.BlobMatches
	if result.TotalBlobMatches != expectedTotalBlobs {
		t.Errorf("total_blob_matches: got %d, want %d", result.TotalBlobMatches, expectedTotalBlobs)
	}

	if result.TotalAffectedFiles == 0 {
		t.Error("total_affected_files should be > 0")
	}
	if result.EstimatedCommits == 0 {
		t.Error("estimated_commits should be > 0")
	}
	if result.ObjectsScanned == 0 {
		t.Error("objects_scanned should be > 0")
	}
}

// TestScrubRunDryRunDiffMutex verifies that --dry-run and --diff cannot be used
// together.
func TestScrubRunDryRunDiffMutex(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubRunEnv, "file.txt", "SECRET here\n", "add file")

	recipe := writeRecipe(t, "recipe.toml", `
[[operations]]
pattern = "SECRET"
replace = "REDACTED"
`)

	_, stderr, code := runSafegitEnv(t, dir, scrubRunEnv,
		"--dry-run", "--yes", "scrub", "run",
		"--diff",
		"--reason", "test mutex",
		"--entire-history",
		recipe,
	)

	if code == 0 {
		t.Fatal("expected --dry-run --diff to fail, but exited 0")
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("error should mention 'mutually exclusive', got: %s", stderr)
	}
}

// TestScrubRunPerOpScope creates a repo with two files (config.env and data.yaml),
// each containing a different secret. A recipe with two operations scoped to
// different file patterns verifies that only the scoped files are affected by
// each operation.
func TestScrubRunPerOpScope(t *testing.T) {
	dir := newRepo(t)

	// Create two files, each with a unique secret.
	commitFileEnv(t, dir, scrubRunEnv, "config.env", "password=ENV_SECRET_1\n", "add config.env")
	commitFileEnv(t, dir, scrubRunEnv, "data.yaml", "token: YAML_SECRET_2\n", "add data.yaml")
	// Update both to create multiple versions in history.
	commitFileEnv(t, dir, scrubRunEnv, "config.env", "password=ENV_SECRET_1 v2\n", "update config.env")
	commitFileEnv(t, dir, scrubRunEnv, "data.yaml", "token: YAML_SECRET_2 v2\n", "update data.yaml")

	recipe := writeRecipe(t, "recipe.toml", `
[[operations]]
pattern = "ENV_SECRET_1"
replace = "REDACTED_ENV"
scope = "*.env"

[[operations]]
pattern = "YAML_SECRET_2"
replace = "REDACTED_YAML"
scope = "*.yaml"
`)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubRunEnv,
		"--yes", "scrub", "run",
		"--reason", "test per-op scope",
		"--entire-history",
		recipe,
	)
	if code != 0 {
		t.Fatalf("scrub run failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify all commits: config.env should have REDACTED_ENV, data.yaml should
	// have REDACTED_YAML. Neither original secret should remain.
	shas := revListReverse(t, dir)
	for i, sha := range shas {
		envContent, envOk := gitShow(t, dir, sha, "config.env")
		if envOk {
			if strings.Contains(envContent, "ENV_SECRET_1") {
				t.Errorf("commit %d (%s): config.env still contains ENV_SECRET_1: %q", i, sha[:12], envContent)
			}
			if strings.Contains(envContent, "REDACTED_ENV") {
				// Expected -- the .env scope matched config.env.
			}
			// The YAML operation should NOT have touched config.env (even if
			// the pattern happened to match, the scope restricts it).
			if strings.Contains(envContent, "REDACTED_YAML") {
				t.Errorf("commit %d (%s): config.env has REDACTED_YAML (YAML op leaked into .env scope): %q", i, sha[:12], envContent)
			}
		}

		yamlContent, yamlOk := gitShow(t, dir, sha, "data.yaml")
		if yamlOk {
			if strings.Contains(yamlContent, "YAML_SECRET_2") {
				t.Errorf("commit %d (%s): data.yaml still contains YAML_SECRET_2: %q", i, sha[:12], yamlContent)
			}
			if strings.Contains(yamlContent, "REDACTED_YAML") {
				// Expected -- the .yaml scope matched data.yaml.
			}
			// The ENV operation should NOT have touched data.yaml.
			if strings.Contains(yamlContent, "REDACTED_ENV") {
				t.Errorf("commit %d (%s): data.yaml has REDACTED_ENV (ENV op leaked into .yaml scope): %q", i, sha[:12], yamlContent)
			}
		}
	}

	// Verify on-disk files.
	envDisk, err := os.ReadFile(filepath.Join(dir, "config.env"))
	if err != nil {
		t.Fatalf("reading config.env: %v", err)
	}
	if strings.Contains(string(envDisk), "ENV_SECRET_1") {
		t.Errorf("on-disk config.env still contains ENV_SECRET_1: %q", string(envDisk))
	}

	yamlDisk, err := os.ReadFile(filepath.Join(dir, "data.yaml"))
	if err != nil {
		t.Fatalf("reading data.yaml: %v", err)
	}
	if strings.Contains(string(yamlDisk), "YAML_SECRET_2") {
		t.Errorf("on-disk data.yaml still contains YAML_SECRET_2: %q", string(yamlDisk))
	}
}
