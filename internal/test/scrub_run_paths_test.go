package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stricttools/safegit/internal/testutil"
)

// pathsRepo is a history holding a name in file contents, in a commit
// message, in a directory name, and in a file name.
func pathsRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	commitFileEnv(t, dir, scrubRunEnv, "examples/acme/a.toml", "name = \"Acme\"\n", "add the Acme example")
	commitFileEnv(t, dir, scrubRunEnv, "docs/acme-notes.md", "notes on acme\n", "add notes")
	commitFileEnv(t, dir, scrubRunEnv, "keep.txt", "unrelated\n", "add keep")
	return dir
}

// everyPath is every file path of every commit of HEAD's history.
func everyPath(t *testing.T, dir string) []string {
	t.Helper()
	var all []string
	for _, sha := range revListReverse(t, dir) {
		all = append(all, testutil.TreePaths(t, dir, sha)...)
	}
	return all
}

func TestScrubRunRenamesThePathsAnOperationTargetingPathsMatches(t *testing.T) {
	dir := pathsRepo(t)
	recipe := writeRecipe(t, "recipe.toml", `
[[operations]]
pattern = "(?i)acme"
replace = "a client"

[[operations]]
pattern = "(?i)acme"
replace = "client"
target = "paths"
`)
	stdout, stderr, code := runSafegitEnv(t, dir, scrubRunEnv,
		"--approve-consequential", "scrub", "run", "--reason", "rename a name", "--entire-history", recipe)
	if code != 0 {
		t.Fatalf("scrub run failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	for _, p := range everyPath(t, dir) {
		if strings.Contains(strings.ToLower(p), "acme") {
			t.Errorf("a path still holds the name: %s", p)
		}
	}
	if got := testutil.MustShow(t, dir, "HEAD", "examples/client/a.toml"); got != "name = \"a client\"\n" {
		t.Errorf("examples/client/a.toml = %q", got)
	}
	if got := testutil.MustShow(t, dir, "HEAD", "docs/client-notes.md"); got != "notes on a client\n" {
		t.Errorf("docs/client-notes.md = %q", got)
	}
	if got := testutil.MustShow(t, dir, "HEAD", "keep.txt"); got != "unrelated\n" {
		t.Errorf("keep.txt = %q", got)
	}
	if msg := testutil.Git(t, dir, "log", "--format=%B"); strings.Contains(strings.ToLower(msg), "acme") {
		t.Errorf("the message still holds the name: %q", msg)
	}
	// The working tree follows the rewritten HEAD.
	if _, err := os.Stat(filepath.Join(dir, "examples", "acme")); !os.IsNotExist(err) {
		t.Errorf("the working tree still holds examples/acme: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "docs", "client-notes.md")); err != nil || string(data) != "notes on a client\n" {
		t.Errorf("the working tree's docs/client-notes.md = %q, %v", data, err)
	}
	// The recipe now verifies clean, path names included.
	stdout, stderr, code = runSafegitEnv(t, dir, scrubRunEnv, "scrub", "verify", recipe)
	if code != 0 {
		t.Fatalf("scrub verify after the rewrite failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
}

func TestAnOperationTargetingPathsLeavesContentAlone(t *testing.T) {
	dir := pathsRepo(t)
	recipe := writeRecipe(t, "recipe.toml", `
[[operations]]
pattern = "(?i)acme"
replace = "client"
target = "paths"
`)
	stdout, stderr, code := runSafegitEnv(t, dir, scrubRunEnv,
		"--approve-consequential", "scrub", "run", "--reason", "rename a name", "--entire-history", recipe)
	if code != 0 {
		t.Fatalf("scrub run failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	if got := testutil.MustShow(t, dir, "HEAD", "examples/client/a.toml"); got != "name = \"Acme\"\n" {
		t.Errorf("examples/client/a.toml = %q", got)
	}
	if msg := testutil.Git(t, dir, "log", "--format=%B"); !strings.Contains(msg, "Acme") {
		t.Errorf("the message was rewritten: %q", msg)
	}
}

func TestAPathRenameOntoAnotherEntrysNameIsRefused(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, scrubRunEnv, "acme.txt", "one\n", "add one")
	commitFileEnv(t, dir, scrubRunEnv, "client.txt", "two\n", "add two")
	head := testutil.Rev(t, dir, "HEAD")
	recipe := writeRecipe(t, "recipe.toml", `
[[operations]]
pattern = "acme"
replace = "client"
target = "paths"
`)
	stdout, stderr, code := runSafegitEnv(t, dir, scrubRunEnv,
		"--approve-consequential", "scrub", "run", "--reason", "rename a name", "--entire-history", recipe)
	if code == 0 || !strings.Contains(stdout+stderr, `two entries named "client.txt"`) {
		t.Fatalf("a colliding rename was not refused (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != head {
		t.Errorf("HEAD moved from %s to %s", head, got)
	}
}

func TestAnOperationTargetingCommitsLeavesBlobsAlone(t *testing.T) {
	dir := pathsRepo(t)
	recipe := writeRecipe(t, "recipe.toml", `
[[operations]]
pattern = "Acme"
replace = "Client"
target = "commits"
`)
	stdout, stderr, code := runSafegitEnv(t, dir, scrubRunEnv,
		"--approve-consequential", "scrub", "run", "--reason", "rewrite messages", "--entire-history", recipe)
	if code != 0 {
		t.Fatalf("scrub run failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	if msg := testutil.Git(t, dir, "log", "--format=%B"); !strings.Contains(msg, "Client") {
		t.Errorf("the message was not rewritten: %q", msg)
	}
	if got := testutil.MustShow(t, dir, "HEAD", "examples/acme/a.toml"); got != "name = \"Acme\"\n" {
		t.Errorf("a blob was rewritten by an operation targeting commits: %q", got)
	}
}

func TestTheDryRunNamesThePathsAnOperationTargetingPathsRenames(t *testing.T) {
	dir := pathsRepo(t)
	head := testutil.Rev(t, dir, "HEAD")
	recipe := writeRecipe(t, "recipe.toml", `
[[operations]]
pattern = "(?i)acme"
replace = "client"
target = "paths"
`)
	stdout, stderr, code := runSafegitEnv(t, dir, scrubRunEnv,
		"--dry-run", "--json", "--approve-consequential", "scrub", "run", "--reason", "preview", "--entire-history", recipe)
	if code != 0 {
		t.Fatalf("scrub run --dry-run failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	var result struct {
		Operations []struct {
			BlobMatches int      `json:"blob_matches"`
			PathMatches []string `json:"path_matches"`
		} `json:"operations"`
	}
	if err := json.Unmarshal([]byte(jsonPayload(t, stdout)), &result); err != nil {
		t.Fatalf("parsing the preview: %v\n%s", err, stdout)
	}
	ops := result.Operations
	if len(ops) != 1 || strings.Join(ops[0].PathMatches, ",") != "docs/acme-notes.md,examples/acme" || ops[0].BlobMatches != 0 {
		t.Fatalf("the preview reported %+v", ops)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != head {
		t.Errorf("the preview moved HEAD")
	}
}

func TestScrubVerifyChecksThePathNamesOfAnOperationTargetingPaths(t *testing.T) {
	dir := pathsRepo(t)
	recipe := writeRecipe(t, "recipe.toml", `
[[operations]]
pattern = "acme-notes"
replace = "notes"
target = "paths"
`)
	stdout, stderr, code := runSafegitEnv(t, dir, scrubRunEnv, "scrub", "verify", recipe)
	if code == 0 || !strings.Contains(stdout+stderr, "acme-notes.md") {
		t.Fatalf("a path name the recipe names passed verification (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
}
