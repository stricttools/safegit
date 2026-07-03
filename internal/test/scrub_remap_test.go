package test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var hex40Re = regexp.MustCompile(`[0-9a-f]{40,}`)

// fullShasIn extracts all exactly-40-hex runs from content.
func fullShasIn(content string) []string {
	var out []string
	for _, m := range hex40Re.FindAllString(content, -1) {
		if len(m) == 40 {
			out = append(out, m)
		}
	}
	return out
}

// appendChangelogLine appends an rlsbl-style JSONL line referencing sha to
// path and commits it, returning the new HEAD SHA.
func appendChangelogLine(t *testing.T, dir, path, sha string) string {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	existing, _ := os.ReadFile(full)
	line := fmt.Sprintf("{\"commits\":[%q],\"user_facing\":false}\n", sha)
	if err := os.WriteFile(full, append(existing, []byte(line)...), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "commit", "-m", "changelog: cover "+sha[:8], "--", path)
	if code != 0 {
		t.Fatalf("committing changelog failed: %s", stderr)
	}
	return revParseHEAD(t, dir)
}

// assertChangelogSelfConsistent walks every commit in the rewritten history
// and asserts that every full 40-hex hash in the file at path resolves to a
// commit that exists in the rewritten repository and is never one of the old
// (pre-rewrite) SHAs.
func assertChangelogSelfConsistent(t *testing.T, dir, path string, oldToNew map[string]string) {
	t.Helper()
	oldSHAs := make(map[string]bool, len(oldToNew))
	newSHAs := make(map[string]bool, len(oldToNew))
	for old, new_ := range oldToNew {
		oldSHAs[old] = true
		newSHAs[new_] = true
	}
	for _, sha := range revListReverse(t, dir) {
		content, ok := gitShow(t, dir, sha, path)
		if !ok {
			continue
		}
		for _, ref := range fullShasIn(content) {
			if oldSHAs[ref] {
				t.Errorf("commit %s: %s still references pre-rewrite SHA %s", sha[:12], path, ref)
				continue
			}
			if !newSHAs[ref] {
				t.Errorf("commit %s: %s references %s, which is neither a rewritten SHA nor expected", sha[:12], path, ref)
			}
		}
	}
}

// TestScrubFileRemapShas: N commits where a JSONL changelog references earlier
// commit SHAs; after scrub file with --remap-shas-in, every historical version
// of the changelog references the rewritten SHAs.
func TestScrubFileRemapShas(t *testing.T) {
	dir := newRepo(t)

	c1 := commitFileEnv(t, dir, scrubEnv, "secret.txt", "hunter2 v1\n", "add secret v1")
	appendChangelogLine(t, dir, "changes/changelog.jsonl", c1)
	c3 := commitFileEnv(t, dir, scrubEnv, "secret.txt", "hunter2 v2\n", "update secret v2")
	appendChangelogLine(t, dir, "changes/changelog.jsonl", c3)

	initialSHA := revListReverse(t, dir)[0]
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "REDACTED\n", "commit replacement")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--json", "scrub", "file",
		"--from", initialSHA, "--reason", "remap test",
		"--remap-shas-in", "changes/changelog.jsonl", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	var result scrubFileJSON
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("parsing JSON: %v\n%s", err, stdout)
	}
	if _, ok := result.Rewrites[c1]; !ok {
		t.Fatalf("c1 %s not in rewrites map", c1)
	}
	if _, ok := result.Rewrites[c3]; !ok {
		t.Fatalf("c3 %s not in rewrites map", c3)
	}

	assertChangelogSelfConsistent(t, dir, "changes/changelog.jsonl", result.Rewrites)

	// Final version references exactly the rewritten SHAs.
	finalContent, ok := gitShow(t, dir, "HEAD", "changes/changelog.jsonl")
	if !ok {
		t.Fatal("changelog missing at HEAD")
	}
	for _, old := range []string{c1, c3} {
		if !strings.Contains(finalContent, result.Rewrites[old]) {
			t.Errorf("final changelog missing rewritten SHA for %s: %s", old[:12], finalContent)
		}
	}
	// Every referenced SHA must resolve to a commit in the rewritten repo.
	for _, ref := range fullShasIn(finalContent) {
		gitCmd(t, dir, "rev-parse", "--verify", ref+"^{commit}")
	}
}

// TestScrubFileRemapPartialRange: references to pre-range ancestors are left
// untouched and cause no error.
func TestScrubFileRemapPartialRange(t *testing.T) {
	dir := newRepo(t)

	c1 := commitFileEnv(t, dir, scrubEnv, "secret.txt", "hunter2 v1\n", "add secret v1")
	appendChangelogLine(t, dir, "changelog.jsonl", c1)
	c3 := commitFileEnv(t, dir, scrubEnv, "secret.txt", "hunter2 v2\n", "update secret v2")
	appendChangelogLine(t, dir, "changelog.jsonl", c3)
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "REDACTED\n", "commit replacement")

	// Scrub only from c3: c1 (referenced by the changelog) is a pre-range
	// ancestor and must be left untouched.
	stdout, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--json", "scrub", "file",
		"--from", c3, "--reason", "partial range remap",
		"--remap-shas-in", "changelog.jsonl", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	var result scrubFileJSON
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("parsing JSON: %v\n%s", err, stdout)
	}
	newC3, ok := result.Rewrites[c3]
	if !ok {
		t.Fatalf("c3 not rewritten: %v", result.Rewrites)
	}

	finalContent, ok := gitShow(t, dir, "HEAD", "changelog.jsonl")
	if !ok {
		t.Fatal("changelog missing at HEAD")
	}
	if !strings.Contains(finalContent, c1) {
		t.Errorf("pre-range ancestor reference %s should be untouched: %s", c1[:12], finalContent)
	}
	if !strings.Contains(finalContent, newC3) {
		t.Errorf("in-range reference should be remapped to %s: %s", newC3[:12], finalContent)
	}
	if strings.Contains(finalContent, c3) {
		t.Errorf("old in-range SHA %s still referenced: %s", c3[:12], finalContent)
	}
	// The pre-range commit still exists.
	gitCmd(t, dir, "rev-parse", "--verify", c1+"^{commit}")
}

// TestScrubFileRemapStaleHash: an unresolvable 40-hex hash is left untouched,
// counted, and reported without failing the scrub.
func TestScrubFileRemapStaleHash(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "hunter2\n", "add secret")
	stale := strings.Repeat("deadbeef", 5)
	appendChangelogLine(t, dir, "changelog.jsonl", stale)

	initialSHA := revListReverse(t, dir)[0]
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "REDACTED\n", "commit replacement")

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "scrub", "file",
		"--from", initialSHA, "--reason", "stale hash remap",
		"--remap-shas-in", "changelog.jsonl", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub with stale hash should succeed, got code %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "unresolvable") {
		t.Errorf("expected stale-hash report mentioning 'unresolvable', got: %s", stderr)
	}

	finalContent, ok := gitShow(t, dir, "HEAD", "changelog.jsonl")
	if !ok {
		t.Fatal("changelog missing at HEAD")
	}
	if !strings.Contains(finalContent, stale) {
		t.Errorf("stale hash should be left untouched: %s", finalContent)
	}
}

// TestScrubFileRemapLeavesAbbreviatedAndLongerHex: abbreviated (sub-40) hashes
// and longer hex runs (e.g. SHA-256) are never touched.
func TestScrubFileRemapLeavesAbbreviatedAndLongerHex(t *testing.T) {
	dir := newRepo(t)

	c1 := commitFileEnv(t, dir, scrubEnv, "secret.txt", "hunter2\n", "add secret")
	longHex := strings.Repeat("ab", 32) // 64 hex chars
	content := fmt.Sprintf("full: %s\nabbrev: %s\nsha256: %s\n", c1, c1[:12], longHex)
	commitFileEnv(t, dir, scrubEnv, "notes.txt", content, "add notes")

	initialSHA := revListReverse(t, dir)[0]
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "REDACTED\n", "commit replacement")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--json", "scrub", "file",
		"--from", initialSHA, "--reason", "abbrev remap",
		"--remap-shas-in", "notes.txt", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	var result scrubFileJSON
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("parsing JSON: %v\n%s", err, stdout)
	}
	newC1 := result.Rewrites[c1]

	finalContent, ok := gitShow(t, dir, "HEAD", "notes.txt")
	if !ok {
		t.Fatal("notes.txt missing at HEAD")
	}
	if !strings.Contains(finalContent, "full: "+newC1+"\n") {
		t.Errorf("full hash should be remapped to %s: %s", newC1[:12], finalContent)
	}
	if !strings.Contains(finalContent, "abbrev: "+c1[:12]+"\n") {
		t.Errorf("abbreviated hash must be untouched: %s", finalContent)
	}
	if !strings.Contains(finalContent, "sha256: "+longHex+"\n") {
		t.Errorf("64-hex run must be untouched: %s", finalContent)
	}
}

// TestScrubMatchRemapShas exercises --remap-shas-in on scrub match.
func TestScrubMatchRemapShas(t *testing.T) {
	dir := newRepo(t)

	c1 := commitFileEnv(t, dir, scrubEnv, "config.env", "token=MATCHSECRET\n", "add config")
	appendChangelogLine(t, dir, "changelog.jsonl", c1)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--json", "scrub", "match",
		"--pattern", "MATCHSECRET", "--replace", "GONE", "--reason", "match remap",
		"--entire-history", "--remap-shas-in", "changelog.jsonl")
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}

	var result struct {
		Rewrites map[string]string `json:"rewrites"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("parsing JSON: %v\n%s", err, stdout)
	}
	newC1, ok := result.Rewrites[c1]
	if !ok {
		t.Fatalf("c1 not rewritten: %v", result.Rewrites)
	}

	assertChangelogSelfConsistent(t, dir, "changelog.jsonl", result.Rewrites)
	finalContent, ok := gitShow(t, dir, "HEAD", "changelog.jsonl")
	if !ok {
		t.Fatal("changelog missing at HEAD")
	}
	if !strings.Contains(finalContent, newC1) || strings.Contains(finalContent, c1) {
		t.Errorf("changelog should reference %s and not %s: %s", newC1[:12], c1[:12], finalContent)
	}
}

// TestScrubRunRemapShas exercises --remap-shas-in on scrub run (recipe mode).
func TestScrubRunRemapShas(t *testing.T) {
	dir := newRepo(t)

	c1 := commitFileEnv(t, dir, scrubEnv, "config.env", "token=RECIPESECRET\n", "add config")
	appendChangelogLine(t, dir, "changelog.jsonl", c1)

	recipePath := filepath.Join(dir, "recipe.toml")
	if err := os.WriteFile(recipePath, []byte("[[operations]]\npattern = \"RECIPESECRET\"\nreplace = \"GONE\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "commit", "-m", "add recipe", "--", "recipe.toml")
	if code != 0 {
		t.Fatalf("committing recipe failed: %s", stderr)
	}

	stdout, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--json", "scrub", "run",
		"--reason", "recipe remap", "--entire-history",
		"--remap-shas-in", "changelog.jsonl", "recipe.toml")
	if code != 0 {
		t.Fatalf("scrub run failed (code %d): %s", code, stderr)
	}

	var result struct {
		Rewrites map[string]string `json:"rewrites"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("parsing JSON: %v\n%s", err, stdout)
	}
	newC1, ok := result.Rewrites[c1]
	if !ok {
		t.Fatalf("c1 not rewritten: %v", result.Rewrites)
	}

	assertChangelogSelfConsistent(t, dir, "changelog.jsonl", result.Rewrites)
	finalContent, ok := gitShow(t, dir, "HEAD", "changelog.jsonl")
	if !ok {
		t.Fatal("changelog missing at HEAD")
	}
	if !strings.Contains(finalContent, newC1) {
		t.Errorf("changelog should reference %s: %s", newC1[:12], finalContent)
	}
}

// TestScrubFileRemapSkipsBinary: a binary file matched by the glob is skipped
// safely (content unchanged, scrub succeeds).
func TestScrubFileRemapSkipsBinary(t *testing.T) {
	dir := newRepo(t)

	c1 := commitFileEnv(t, dir, scrubEnv, "secret.txt", "hunter2\n", "add secret")
	binary := "BIN\x00HEADER " + c1 + " trailer"
	commitFileEnv(t, dir, scrubEnv, "data.bin", binary, "add binary")

	initialSHA := revListReverse(t, dir)[0]
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "REDACTED\n", "commit replacement")

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "scrub", "file",
		"--from", initialSHA, "--reason", "binary remap",
		"--remap-shas-in", "*.bin", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub with binary glob match should succeed, got code %d: %s", code, stderr)
	}

	finalContent, ok := gitShow(t, dir, "HEAD", "data.bin")
	if !ok {
		t.Fatal("data.bin missing at HEAD")
	}
	if finalContent != binary {
		t.Errorf("binary content must be unchanged, got %q want %q", finalContent, binary)
	}
}

// TestScrubRemapInvalidGlob: malformed globs are rejected at flag parse time
// with a usage error on all three commands.
func TestScrubRemapInvalidGlob(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "hunter2\n", "add secret")
	initialSHA := revListReverse(t, dir)[0]
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "REDACTED\n", "commit replacement")

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "scrub", "file",
		"--from", initialSHA, "--reason", "bad glob",
		"--remap-shas-in", "[", "secret.txt")
	if code != 2 {
		t.Fatalf("scrub file: expected exit code 2 for invalid glob, got %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "remap-shas-in") {
		t.Errorf("error should mention the flag: %s", stderr)
	}

	_, stderr, code = runSafegitEnv(t, dir, scrubEnv, "--yes", "scrub", "match",
		"--pattern", "hunter2", "--replace", "GONE", "--reason", "bad glob",
		"--entire-history", "--remap-shas-in", "[")
	if code != 2 {
		t.Fatalf("scrub match: expected exit code 2 for invalid glob, got %d: %s", code, stderr)
	}

	// scrub run shares the same validation path; a valid committed recipe
	// ensures the invalid glob is the only failure cause.
	recipePath := filepath.Join(dir, "recipe.toml")
	if err := os.WriteFile(recipePath, []byte("[[operations]]\npattern = \"hunter2\"\nreplace = \"GONE\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegitEnv(t, dir, scrubEnv, "commit", "-m", "add recipe", "--", "recipe.toml")
	if code != 0 {
		t.Fatalf("committing recipe failed: %s", stderr)
	}
	_, stderr, code = runSafegitEnv(t, dir, scrubEnv, "--yes", "scrub", "run",
		"--reason", "bad glob", "--entire-history",
		"--remap-shas-in", "[", "recipe.toml")
	if code != 2 {
		t.Fatalf("scrub run: expected exit code 2 for invalid glob, got %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "remap-shas-in") {
		t.Errorf("scrub run error should mention the flag: %s", stderr)
	}
}

// TestScrubFileInSubmoduleRemapShas: scrubbing a file inside a submodule with
// --remap-shas-in remaps hash references in the PARENT's glob-matched files
// (the parent history is rewritten for gitlink updates), while the
// submodule's own glob-matched files are NOT remapped -- the documented
// limitation -- and the whole operation completes cleanly.
func TestScrubFileInSubmoduleRemapShas(t *testing.T) {
	parentDir, _, subDir := newRepoWithSubmoduleSecret(t, "SUBREMAP_SECRET", "secret.txt")

	subSHAs := revListReverse(t, subDir)
	firstSubCommit := subSHAs[0]

	// Submodule changelog references the submodule's secret commit. Committed
	// with raw git (the submodule checkout has its own identity config).
	if err := os.WriteFile(filepath.Join(subDir, "changelog.jsonl"),
		[]byte(fmt.Sprintf("{\"commits\":[%q],\"user_facing\":false}\n", firstSubCommit)), 0644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, subDir, "add", "changelog.jsonl")
	gitCmd(t, subDir, "commit", "-m", "sub changelog")

	// Parent changelog references the parent's "add submodule" commit, which
	// carries the old gitlink and will therefore be rewritten.
	parentSHAs := revListReverse(t, parentDir)
	addSubCommit := parentSHAs[1]
	appendChangelogLine(t, parentDir, "changelog.jsonl", addSubCommit)

	// Commit the replacement content in the submodule, then record the new
	// gitlink in the parent so both trees are clean.
	if err := os.WriteFile(filepath.Join(subDir, "secret.txt"), []byte("CLEANED\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, subDir, "add", "secret.txt")
	gitCmd(t, subDir, "commit", "-m", "commit replacement")
	gitCmd(t, parentDir, "add", "mysub")
	gitCmd(t, parentDir, "commit", "-m", "update submodule ref")

	stdout, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv,
		"--yes", "--json", "scrub", "file",
		"mysub/secret.txt",
		"--from", firstSubCommit,
		"--reason", "submodule remap test",
		"--remap-shas-in", "changelog.jsonl")
	if code != 0 {
		t.Fatalf("scrub file in submodule failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		Rewrites map[string]string `json:"rewrites"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("parsing JSON: %v\n%s", err, stdout)
	}
	newAddSub, ok := result.Rewrites[addSubCommit]
	if !ok {
		t.Fatalf("parent commit %s (old gitlink) not rewritten: %v", addSubCommit[:12], result.Rewrites)
	}

	// Parent's matched file is remapped: references the rewritten parent
	// commit, never the pre-rewrite one, at every historical version.
	assertChangelogSelfConsistent(t, parentDir, "changelog.jsonl", result.Rewrites)
	parentFinal, ok := gitShow(t, parentDir, "HEAD", "changelog.jsonl")
	if !ok {
		t.Fatal("parent changelog missing at HEAD")
	}
	if !strings.Contains(parentFinal, newAddSub) || strings.Contains(parentFinal, addSubCommit) {
		t.Errorf("parent changelog should reference %s and not %s: %s",
			newAddSub[:12], addSubCommit[:12], parentFinal)
	}

	// Submodule history was scrubbed (secret gone everywhere)...
	for i, sha := range revListReverse(t, subDir) {
		content, ok := gitShow(t, subDir, sha, "secret.txt")
		if ok && strings.Contains(content, "SUBREMAP_SECRET") {
			t.Errorf("submodule commit %d (%s): secret still present", i, sha[:12])
		}
	}
	// ...but its changelog was NOT remapped: it still carries the old
	// (pre-rewrite, now stale) submodule SHA. Documented limitation.
	subFinal, ok := gitShow(t, subDir, "HEAD", "changelog.jsonl")
	if !ok {
		t.Fatal("submodule changelog missing at HEAD")
	}
	if !strings.Contains(subFinal, firstSubCommit) {
		t.Errorf("submodule changelog should still reference the old SHA %s (remap must not apply inside submodules): %s",
			firstSubCommit[:12], subFinal)
	}
}
