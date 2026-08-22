package test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// rewriteMapLine is a generic parsed line from rewrite-maps.jsonl.
type rewriteMapLine map[string]interface{}

// readRewriteMaps parses .git/safegit/rewrite-maps.jsonl in the given repo.
func readRewriteMaps(t *testing.T, dir string) []rewriteMapLine {
	t.Helper()
	return readRewriteMapsAt(t, filepath.Join(dir, ".git", "safegit"))
}

// readRewriteMapsAt parses the rewrite journal held in an explicit safegit
// directory. A submodule's safegit directory is under the parent's
// .git/modules/<name>/, which no repo-root-relative path reaches.
func readRewriteMapsAt(t *testing.T, sgDir string) []rewriteMapLine {
	t.Helper()
	path := filepath.Join(sgDir, "rewrite-maps.jsonl")
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
	testutil.Git(t, dir, "tag", "-a", "v-annot", "-m", "annotated tag", c2)
	testutil.Git(t, dir, "tag", "v-light", c2)

	// Remote-tracking ref simulating a previously-fetched remote state.
	oldHead := commitFileEnv(t, dir, scrubEnv, "secret.txt", "REDACTED\n", "commit replacement")
	testutil.Git(t, dir, "update-ref", "refs/remotes/origin/main", oldHead)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "--json", "scrub", "file", "--replace-with", "secret.txt",
		"--from", initialSHA, "--reason", "test rewrite maps", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	var result scrubFileJSON
	if err := json.Unmarshal([]byte(jsonPayload(t, stdout)), &result); err != nil {
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
	remoteNow := testutil.Git(t, dir, "rev-parse", "refs/remotes/origin/main")
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
	testutil.Git(t, dir, "tag", "-a", "leaky-tag", "-m", "release with SECRETXYZ inside", c1)

	// A separate file carries the secret so blobs are rewritten too.
	commitFileEnv(t, dir, scrubEnv, "config.env", "token=SECRETXYZ\n", "add config")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "--json", "scrub", "match",
		"--pattern", "SECRETXYZ", "--replace", "GONE", "--reason", "test annotation persistence",
		"--entire-history")
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}

	var result struct {
		Tags          []tagRewriteJSON `json:"tags"`
		TagsRewritten int              `json:"tags_rewritten"`
	}
	if err := json.Unmarshal([]byte(jsonPayload(t, stdout)), &result); err != nil {
		t.Fatalf("parsing scrub match JSON: %v\n%s", err, stdout)
	}
	if result.TagsRewritten < 1 {
		t.Fatalf("expected at least 1 tag annotation rewritten, got %d", result.TagsRewritten)
	}

	// The final tag object SHA (after the annotation pass) must appear as a
	// new_sha in the persisted refs record.
	finalTagSHA := testutil.Git(t, dir, "rev-parse", "refs/tags/leaky-tag")
	tagBody := testutil.Git(t, dir, "cat-file", "-p", finalTagSHA)
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

// TestScrubMatchTagAnnotationOnlyRewriteWritesRewriteMaps: when the secret
// appears ONLY in a tag annotation body, the commit map is all-identity but
// the annotation pass still rewrites the tag object and moves the tag ref.
// Refs must never move unrecorded: the rewrite map must contain the full
// start+refs+complete sequence, with an empty commit map in the start record
// and the tag rewrite in the refs record.
func TestScrubMatchTagAnnotationOnlyRewriteWritesRewriteMaps(t *testing.T) {
	dir := newRepo(t)

	c1 := commitFileEnv(t, dir, scrubEnv, "notes.txt", "clean content\n", "add notes")
	testutil.Git(t, dir, "tag", "-a", "leaky-tag", "-m", "release with TAGONLYSECRET inside", c1)

	headBefore := testutil.Git(t, dir, "rev-parse", "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "--json", "scrub", "match",
		"--pattern", "TAGONLYSECRET", "--replace", "GONE", "--reason", "tag-annotation-only rewrite",
		"--entire-history")
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}

	var result struct {
		Rewrites      map[string]string `json:"rewrites"`
		TagsRewritten int               `json:"tags_rewritten"`
	}
	if err := json.Unmarshal([]byte(jsonPayload(t, stdout)), &result); err != nil {
		t.Fatalf("parsing scrub match JSON: %v\n%s", err, stdout)
	}
	if len(result.Rewrites) != 0 {
		t.Fatalf("expected all-identity commit map, got rewrites: %v", result.Rewrites)
	}
	if result.TagsRewritten != 1 {
		t.Fatalf("expected 1 tag annotation rewritten, got %d", result.TagsRewritten)
	}

	// The tag ref moved and its body was scrubbed.
	finalTagSHA := testutil.Git(t, dir, "rev-parse", "refs/tags/leaky-tag")
	tagBody := testutil.Git(t, dir, "cat-file", "-p", finalTagSHA)
	if !strings.Contains(tagBody, "GONE") || strings.Contains(tagBody, "TAGONLYSECRET") {
		t.Fatalf("tag body not rewritten: %s", tagBody)
	}

	// HEAD is untouched (no commit was rewritten).
	if headAfter := testutil.Git(t, dir, "rev-parse", "HEAD"); headAfter != headBefore {
		t.Errorf("HEAD moved (%s -> %s) despite identity commit map", headBefore, headAfter)
	}

	// The ref movement must be recorded: start (empty commit map) + refs
	// (carrying the tag rewrite) + complete.
	lines := readRewriteMaps(t, dir)
	if len(lines) != 3 {
		t.Fatalf("expected 3 rewrite map lines for tag-annotation-only rewrite, got %d: %v", len(lines), lines)
	}
	start, refs, complete := lines[0], lines[1], lines[2]
	if start["phase"] != "start" || refs["phase"] != "refs" || complete["phase"] != "complete" {
		t.Fatalf("unexpected phases: %v %v %v", start["phase"], refs["phase"], complete["phase"])
	}
	if start["id"] != refs["id"] || start["id"] != complete["id"] {
		t.Errorf("phase records do not share an id: %v %v %v", start["id"], refs["id"], complete["id"])
	}
	commitMap, ok := start["commit_map"].(map[string]interface{})
	if !ok {
		t.Fatalf("start commit_map is %T (want empty object, not null)", start["commit_map"])
	}
	if len(commitMap) != 0 {
		t.Errorf("start commit_map should be empty, got %v", commitMap)
	}
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
		t.Errorf("refs record missing tag rewrite to %s: %v", finalTagSHA, tagRewrites)
	}
	if complete["new_head"] != headBefore {
		t.Errorf("complete new_head = %v, want unchanged HEAD %v", complete["new_head"], headBefore)
	}
}

// TestScrubFilePureNoOpWritesNoRewriteMaps: a scrub whose commit map is
// all-identity AND that rewrites no tags must stay recordless -- the
// rewrite-map log records ref movement, and a pure no-op moves nothing.
func TestScrubFilePureNoOpWritesNoRewriteMaps(t *testing.T) {
	dir := newRepo(t)

	// The on-disk content equals the committed content, so the replacement
	// blob is identical and every commit maps to itself.
	commitFileEnv(t, dir, scrubEnv, "clean.txt", "already clean\n", "add clean file")
	headSHA := testutil.Git(t, dir, "rev-parse", "HEAD")

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "scrub", "file", "--replace-with", "clean.txt",
		"--from", headSHA, "--reason", "pure no-op", "clean.txt")
	if code != 0 {
		t.Fatalf("no-op scrub failed (code %d): %s", code, stderr)
	}

	if lines := readRewriteMaps(t, dir); len(lines) != 0 {
		t.Errorf("pure no-op scrub must write no rewrite map records, got %d: %v", len(lines), lines)
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

	stdout, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "--json", "scrub", "run",
		"--reason", "test scrub run maps", "--entire-history", "recipe.toml")
	if code != 0 {
		t.Fatalf("scrub run failed (code %d): %s", code, stderr)
	}

	var result struct {
		Rewrites  map[string]string `json:"rewrites"`
		NewHead   string            `json:"new_head"`
		CleanupOK *bool             `json:"cleanup_ok"`
	}
	if err := json.Unmarshal([]byte(jsonPayload(t, stdout)), &result); err != nil {
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

// Two submodules of one parent routinely carry the SAME remote-tracking
// refnames -- `refs/remotes/origin/main` is what `git submodule add` leaves in
// every one of them. The submodule-only payload used to flat-merge every
// submodule's remotes into one object keyed by refname, so the second
// submodule's `refs/remotes/origin/main` overwrote the first's and the record of
// where one of the two started was simply gone.
//
// The remotes are nested one entry per submodule path instead, which is the
// only key that distinguishes them. The parent's own `pre_rewrite_remotes` stays
// what it always was and is absent here, because on this branch the parent's
// refs never moved.
//
// The fixture puts the secret in each submodule's annotated TAG MESSAGE and
// nowhere else. That is what reaches this branch: the submodule's commits all
// map to themselves, so no gitlink changes and the parent has nothing to
// rewrite, while the submodule's own refs still move because the tag object is
// rewritten -- which is exactly when its pre-rewrite remote state is captured.
func TestSubmoduleOnlyScrubMatchNestsPreRewriteRemotesPerSubmodule(t *testing.T) {
	const secret = "TWOSUB_SECRET_XYZ"
	parentDir := newTwoSubmodulesWithTaggedSecret(t, secret)
	sub1Dir := filepath.Join(parentDir, "sub1")
	sub2Dir := filepath.Join(parentDir, "sub2")

	// Both submodules were cloned by `git submodule add`, so both already carry
	// refs/remotes/origin/main -- the collision this test is about. Their SHAs
	// differ, which is what makes an overwrite observable.
	sub1Before := testutil.Rev(t, sub1Dir, "refs/remotes/origin/main")
	sub2Before := testutil.Rev(t, sub2Dir, "refs/remotes/origin/main")
	if sub1Before == "" || sub2Before == "" {
		t.Fatal("the fixture's submodules carry no origin/main remote-tracking ref")
	}
	if sub1Before == sub2Before {
		t.Fatal("both submodules point origin/main at the same commit; the overwrite would be invisible")
	}

	// The parent's own files hold no secret, so the parent has nothing to
	// rewrite and the run takes the submodule-only payload branch.
	stdout, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv,
		"--approve-consequential", "--json", "scrub", "match",
		"--pattern", "TWOSUB_SECRET_XYZ",
		"--replace", "REDACTED",
		"--reason", "nested remotes",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	var result struct {
		PreRewriteRemotes          map[string]string            `json:"pre_rewrite_remotes"`
		SubmodulePreRewriteRemotes map[string]map[string]string `json:"submodule_pre_rewrite_remotes"`
		NewHead                    string                       `json:"new_head"`
	}
	if err := json.Unmarshal([]byte(jsonPayload(t, stdout)), &result); err != nil {
		t.Fatalf("parsing scrub match JSON: %v\n%s", err, stdout)
	}
	if result.NewHead != "" {
		t.Errorf("new_head is %q; the parent was not rewritten on this branch", result.NewHead)
	}
	if len(result.PreRewriteRemotes) != 0 {
		t.Errorf("the parent's own pre_rewrite_remotes is %v; the parent's refs never moved here",
			result.PreRewriteRemotes)
	}

	nested := result.SubmodulePreRewriteRemotes
	if len(nested) != 2 {
		t.Fatalf("submodule_pre_rewrite_remotes holds %d submodule(s), want 2: %v", len(nested), nested)
	}
	for path, want := range map[string]string{"sub1": sub1Before, "sub2": sub2Before} {
		remotes, ok := nested[path]
		if !ok {
			t.Errorf("submodule_pre_rewrite_remotes has no entry for %s: %v", path, nested)
			continue
		}
		if got := remotes["refs/remotes/origin/main"]; got != want {
			t.Errorf("%s: origin/main recorded as %q, want %q", path, got, want)
		}
	}
}

// newTwoSubmodulesWithTaggedSecret builds a parent with submodules "sub1" and
// "sub2" whose only occurrence of secret is in an annotated tag's MESSAGE. No
// blob, no commit message and nothing in the parent holds it, which is what
// leaves every submodule commit mapping to itself.
func newTwoSubmodulesWithTaggedSecret(t *testing.T, secret string) string {
	t.Helper()
	base := evalTempDir(t)

	origin := func(name string) string {
		dir := filepath.Join(base, name+"-origin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		testutil.Git(t, dir, "init", "-q", "--initial-branch=main")
		testutil.Git(t, dir, "config", "user.email", "test@test.com")
		testutil.Git(t, dir, "config", "user.name", "Test")
		// The content is clean and the commit subject names the submodule, so
		// the two origins' commits have different SHAs.
		testutil.WriteFile(t, dir, "clean.txt", "nothing sensitive here\n")
		testutil.Git(t, dir, "add", "clean.txt")
		testutil.Git(t, dir, "commit", "-q", "-m", name+" initial")
		testutil.Git(t, dir, "tag", "-a", name+"-v1", "-m", "release note mentioning "+secret)
		return dir
	}
	sub1Origin := origin("sub1")
	sub2Origin := origin("sub2")

	parentDir := filepath.Join(base, "parent")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, parentDir, "init", "-q", "--initial-branch=main")
	testutil.Git(t, parentDir, "config", "user.email", "test@test.com")
	testutil.Git(t, parentDir, "config", "user.name", "Test")
	testutil.WriteFile(t, parentDir, "seed.txt", "seed\n")
	testutil.Git(t, parentDir, "add", "seed.txt")
	testutil.Git(t, parentDir, "commit", "-q", "-m", "initial")
	testutil.Git(t, parentDir, "config", "protocol.file.allow", "always")
	testutil.Git(t, parentDir, "submodule", "add", "-q", sub1Origin, "sub1")
	testutil.Git(t, parentDir, "submodule", "add", "-q", sub2Origin, "sub2")
	testutil.Git(t, parentDir, "commit", "-q", "-m", "add submodules")

	for _, name := range []string{"sub1", "sub2"} {
		sub := filepath.Join(parentDir, name)
		testutil.Git(t, sub, "checkout", "-q", "main")
		testutil.Git(t, sub, "config", "user.email", "test@test.com")
		testutil.Git(t, sub, "config", "user.name", "Test")
	}
	return parentDir
}
