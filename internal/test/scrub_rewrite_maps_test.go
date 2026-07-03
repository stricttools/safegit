package test

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// rewriteMapLine is a generic parsed line from rewrite-maps.jsonl.
type rewriteMapLine map[string]interface{}

// readRewriteMaps parses .git/safegit/rewrite-maps.jsonl in the given repo.
func readRewriteMaps(t *testing.T, dir string) []rewriteMapLine {
	t.Helper()
	path := filepath.Join(dir, ".git", "safegit", "rewrite-maps.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("opening %s: %v", path, err)
	}
	defer f.Close()

	var lines []rewriteMapLine
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var m rewriteMapLine
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("parsing rewrite map line %q: %v", raw, err)
		}
		lines = append(lines, m)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanning %s: %v", path, err)
	}
	return lines
}

// gitCmd runs a git command in dir, failing the test on error.
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// scrubFileJSON mirrors the scrub file --json output fields used by tests.
type scrubFileJSON struct {
	Rewrites          map[string]string `json:"rewrites"`
	Tags              []tagRewriteJSON  `json:"tags"`
	OldHead           string            `json:"old_head"`
	NewHead           string            `json:"new_head"`
	PreRewriteRemotes map[string]string `json:"pre_rewrite_remotes"`
	CleanupOK         *bool             `json:"cleanup_ok"`
	CleanupErrors     []string          `json:"cleanup_errors"`
}

type tagRewriteJSON struct {
	Refname   string `json:"refname"`
	OldSHA    string `json:"old_sha"`
	NewSHA    string `json:"new_sha"`
	Annotated bool   `json:"annotated"`
}

// TestScrubFileRewriteMapsPersisted runs a scrub file and verifies the full
// rewrite-map record: commit mappings written before refs move, tag rewrites,
// pre-rewrite remote-tracking state, and cleanup status.
func TestScrubFileRewriteMapsPersisted(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "hunter2 v1\n", "add secret v1")
	c2 := commitFileEnv(t, dir, scrubEnv, "secret.txt", "hunter2 v2\n", "update secret v2")

	shas := revListReverse(t, dir)
	initialSHA := shas[0]

	// Tags on a commit that will be rewritten: one annotated, one lightweight.
	gitCmd(t, dir, "tag", "-a", "v-annot", "-m", "annotated tag", c2)
	gitCmd(t, dir, "tag", "v-light", c2)

	// Remote-tracking ref simulating a previously-fetched remote state.
	oldHead := commitFileEnv(t, dir, scrubEnv, "secret.txt", "REDACTED\n", "commit replacement")
	gitCmd(t, dir, "update-ref", "refs/remotes/origin/main", oldHead)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--json", "scrub", "file",
		"--from", initialSHA, "--reason", "test rewrite maps", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	var result scrubFileJSON
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("parsing scrub JSON: %v\n%s", err, stdout)
	}

	// JSON output must expose the new keys.
	if result.CleanupOK == nil {
		t.Fatal("JSON output missing cleanup_ok")
	}
	if !*result.CleanupOK {
		t.Errorf("cleanup_ok = false, errors: %v", result.CleanupErrors)
	}
	if result.PreRewriteRemotes == nil {
		t.Fatal("JSON output missing pre_rewrite_remotes")
	}
	if result.PreRewriteRemotes["refs/remotes/origin/main"] != oldHead {
		t.Errorf("JSON pre_rewrite_remotes[origin/main] = %q, want %q",
			result.PreRewriteRemotes["refs/remotes/origin/main"], oldHead)
	}
	if len(result.Rewrites) == 0 {
		t.Fatal("expected non-empty rewrites map")
	}

	// The remote-tracking ref was rewritten locally; only the persisted
	// record retains the pre-rewrite value.
	remoteNow := gitCmd(t, dir, "rev-parse", "refs/remotes/origin/main")
	if remoteNow == oldHead {
		t.Error("refs/remotes/origin/main was not rewritten; expected updateRefs to move it")
	}

	lines := readRewriteMaps(t, dir)
	if len(lines) != 3 {
		t.Fatalf("expected 3 rewrite map lines, got %d: %v", len(lines), lines)
	}
	start, refs, complete := lines[0], lines[1], lines[2]

	if start["phase"] != "start" || refs["phase"] != "refs" || complete["phase"] != "complete" {
		t.Fatalf("unexpected phases: %v %v %v", start["phase"], refs["phase"], complete["phase"])
	}
	if start["id"] != refs["id"] || start["id"] != complete["id"] {
		t.Errorf("phase records do not share an id: %v %v %v", start["id"], refs["id"], complete["id"])
	}
	if start["op"] != "scrub-file" {
		t.Errorf("start op = %v, want scrub-file", start["op"])
	}
	if start["reason"] != "test rewrite maps" {
		t.Errorf("start reason = %v, want test rewrite maps", start["reason"])
	}
	if start["old_head"] != oldHead {
		t.Errorf("start old_head = %v, want %v", start["old_head"], oldHead)
	}

	// Every non-identity commit mapping from the JSON output must be in the
	// persisted commit map, and vice versa.
	commitMap, ok := start["commit_map"].(map[string]interface{})
	if !ok {
		t.Fatalf("start commit_map is %T", start["commit_map"])
	}
	if len(commitMap) != len(result.Rewrites) {
		t.Errorf("persisted commit map has %d entries, JSON rewrites has %d", len(commitMap), len(result.Rewrites))
	}
	for old, new_ := range result.Rewrites {
		if commitMap[old] != new_ {
			t.Errorf("commit_map[%s] = %v, want %s", old, commitMap[old], new_)
		}
	}

	remotes, ok := start["pre_rewrite_remotes"].(map[string]interface{})
	if !ok {
		t.Fatalf("start pre_rewrite_remotes is %T", start["pre_rewrite_remotes"])
	}
	if remotes["refs/remotes/origin/main"] != oldHead {
		t.Errorf("persisted pre_rewrite_remotes[origin/main] = %v, want %v",
			remotes["refs/remotes/origin/main"], oldHead)
	}

	// Tag rewrites: both the annotated and lightweight tags must be recorded.
	tagRewrites, ok := refs["tag_rewrites"].([]interface{})
	if !ok {
		t.Fatalf("refs tag_rewrites is %T", refs["tag_rewrites"])
	}
	seen := map[string]bool{}
	for _, tr := range tagRewrites {
		m := tr.(map[string]interface{})
		seen[m["refname"].(string)] = true
	}
	for _, want := range []string{"refs/tags/v-annot", "refs/tags/v-light"} {
		if !seen[want] {
			t.Errorf("persisted tag rewrites missing %s (got %v)", want, seen)
		}
	}

	if complete["new_head"] != result.NewHead {
		t.Errorf("complete new_head = %v, want %v", complete["new_head"], result.NewHead)
	}
	if complete["cleanup_ok"] != true {
		t.Errorf("complete cleanup_ok = %v, want true (errors: %v)", complete["cleanup_ok"], complete["cleanup_errors"])
	}
}

// TestScrubMatchRewriteMapsIncludeAnnotationPassTagRewrites verifies that tag
// rewrites produced by the annotation pass (tag BODY rewriting, which happens
// after updateRefs) are persisted in the rewrite map, not just returned to the
// caller closures.
func TestScrubMatchRewriteMapsIncludeAnnotationPassTagRewrites(t *testing.T) {
	dir := newRepo(t)

	c1 := commitFileEnv(t, dir, scrubEnv, "notes.txt", "clean content\n", "add notes")

	// Annotated tag whose BODY contains the secret; the tag target commit has
	// clean content, so only the annotation pass rewrites this tag's body.
	gitCmd(t, dir, "tag", "-a", "leaky-tag", "-m", "release with SECRETXYZ inside", c1)

	// A separate file carries the secret so blobs are rewritten too.
	commitFileEnv(t, dir, scrubEnv, "config.env", "token=SECRETXYZ\n", "add config")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--json", "scrub", "match",
		"--pattern", "SECRETXYZ", "--replace", "GONE", "--reason", "test annotation persistence",
		"--entire-history")
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}

	var result struct {
		Tags          []tagRewriteJSON `json:"tags"`
		TagsRewritten int              `json:"tags_rewritten"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("parsing scrub match JSON: %v\n%s", err, stdout)
	}
	if result.TagsRewritten < 1 {
		t.Fatalf("expected at least 1 tag annotation rewritten, got %d", result.TagsRewritten)
	}

	// The final tag object SHA (after the annotation pass) must appear as a
	// new_sha in the persisted refs record.
	finalTagSHA := gitCmd(t, dir, "rev-parse", "refs/tags/leaky-tag")
	tagBody := gitCmd(t, dir, "cat-file", "-p", finalTagSHA)
	if !strings.Contains(tagBody, "GONE") || strings.Contains(tagBody, "SECRETXYZ") {
		t.Fatalf("tag body not rewritten: %s", tagBody)
	}

	lines := readRewriteMaps(t, dir)
	if len(lines) != 3 {
		t.Fatalf("expected 3 rewrite map lines, got %d", len(lines))
	}
	refs := lines[1]
	tagRewrites, ok := refs["tag_rewrites"].([]interface{})
	if !ok {
		t.Fatalf("refs tag_rewrites is %T", refs["tag_rewrites"])
	}
	found := false
	for _, tr := range tagRewrites {
		m := tr.(map[string]interface{})
		if m["refname"] == "refs/tags/leaky-tag" && m["new_sha"] == finalTagSHA {
			found = true
		}
	}
	if !found {
		t.Errorf("annotation-pass tag rewrite (new_sha %s) not persisted; got %v", finalTagSHA, tagRewrites)
	}

	// JSON tags must also include the annotation-pass rewrite (unchanged
	// pre-existing behavior).
	jsonFound := false
	for _, tr := range result.Tags {
		if tr.Refname == "refs/tags/leaky-tag" && tr.NewSHA == finalTagSHA {
			jsonFound = true
		}
	}
	if !jsonFound {
		t.Errorf("JSON tags missing annotation-pass rewrite for leaky-tag: %v", result.Tags)
	}
}

// TestScrubRunRewriteMapsPersisted checks that scrub run also persists the
// rewrite map and exposes the new JSON keys.
func TestScrubRunRewriteMapsPersisted(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubEnv, "config.env", "token=RUNSECRET\n", "add config")

	recipePath := filepath.Join(dir, "recipe.toml")
	recipe := "[[operations]]\npattern = \"RUNSECRET\"\nreplace = \"SCRUBBED\"\n"
	if err := os.WriteFile(recipePath, []byte(recipe), 0644); err != nil {
		t.Fatal(err)
	}
	// Commit the recipe so the tree is clean.
	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "commit", "-m", "add recipe", "--", "recipe.toml")
	if code != 0 {
		t.Fatalf("committing recipe failed: %s", stderr)
	}

	stdout, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--json", "scrub", "run",
		"--reason", "test scrub run maps", "--entire-history", "recipe.toml")
	if code != 0 {
		t.Fatalf("scrub run failed (code %d): %s", code, stderr)
	}

	var result struct {
		Rewrites  map[string]string `json:"rewrites"`
		NewHead   string            `json:"new_head"`
		CleanupOK *bool             `json:"cleanup_ok"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("parsing scrub run JSON: %v\n%s", err, stdout)
	}
	if result.CleanupOK == nil || !*result.CleanupOK {
		t.Errorf("cleanup_ok missing or false in scrub run JSON")
	}

	lines := readRewriteMaps(t, dir)
	if len(lines) != 3 {
		t.Fatalf("expected 3 rewrite map lines, got %d", len(lines))
	}
	if lines[0]["op"] != "scrub-run" {
		t.Errorf("start op = %v, want scrub-run", lines[0]["op"])
	}
	commitMap := lines[0]["commit_map"].(map[string]interface{})
	if len(commitMap) != len(result.Rewrites) {
		t.Errorf("persisted commit map has %d entries, JSON rewrites has %d", len(commitMap), len(result.Rewrites))
	}
	if lines[2]["new_head"] != result.NewHead {
		t.Errorf("complete new_head = %v, want %v", lines[2]["new_head"], result.NewHead)
	}
}
