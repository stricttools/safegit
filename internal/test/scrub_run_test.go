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
