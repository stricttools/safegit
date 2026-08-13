package test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The history rewrites' previews used to be hand-rolled: a human branch and a
// machine branch computing DIFFERENT numbers, and no effects minted at all, so
// the framework's would-do log printed its header over an empty body. These
// tests pin the port: one computation, two renderings, and a preview whose log
// has a real body.

// previewEnv is the controlled environment the scrub tests already use.
var previewEnv = confirmEnv

// wouldDoBody returns the would-do log's numbered lines (its body), which is
// what an empty preview lacked.
func wouldDoBody(t *testing.T, stdout string) []string {
	t.Helper()
	log := wouldDoLog(stdout)
	if log == "" {
		t.Fatalf("dry mode rendered no would-do log at all:\n%s", stdout)
	}
	var body []string
	for _, line := range strings.Split(log, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "DRY RUN") {
			continue
		}
		body = append(body, trimmed)
	}
	return body
}

// payloadInt reads one integer member out of a payload object.
func payloadInt(t *testing.T, payload string, key string) int {
	t.Helper()
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		t.Fatalf("payload is not an object: %v\n%s", err, payload)
	}
	v, ok := obj[key]
	if !ok {
		t.Fatalf("payload has no %q member:\n%s", key, payload)
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("payload member %q is not a number: %v", key, v)
	}
	return int(f)
}

// humanNumber pulls the first number out of the human line matching pattern.
func humanNumber(t *testing.T, stdout, pattern string) int {
	t.Helper()
	re := regexp.MustCompile(pattern)
	m := re.FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("no line matching %q in:\n%s", pattern, stdout)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("match %q is not a number: %v", m[1], err)
	}
	return n
}

// TestScrubFilePreviewRendersTheRewrite: a `scrub file --dry-run` mints the
// rewrite through the effects handle, so the would-do log has a body naming the
// ref move and the object-store cleanup that follows it.
func TestScrubFilePreviewRendersTheRewrite(t *testing.T) {
	dir, initialSHA := newSecretRepo(t)

	stdout, stderr, code := runSafegitEnv(t, dir, previewEnv, "--dry-run", "scrub", "file",
		"--from", initialSHA, "--reason", "preview", "secret.txt")
	if code != 0 {
		t.Fatalf("dry run failed (%d): %s", code, stderr)
	}
	body := wouldDoBody(t, stdout)
	if len(body) == 0 {
		t.Fatalf("the would-do log has no body -- the rewrite minted no effects:\n%s", stdout)
	}
	joined := strings.Join(body, "\n")
	for _, want := range []string{"update-ref", "reflog expire", "repack", "prune"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the would-do log does not name %q:\n%s", want, joined)
		}
	}
	if !secretSurvives(t, dir) {
		t.Error("a dry run must not rewrite history")
	}
}

// TestScrubFilePreviewFiguresAgree: the human summary's commit count and the
// payload's commit_count are the same number, because they are the same
// variable.
func TestScrubFilePreviewFiguresAgree(t *testing.T) {
	dir, initialSHA := newSecretRepo(t)

	human, stderr, code := runSafegitEnv(t, dir, previewEnv, "--dry-run", "scrub", "file",
		"--from", initialSHA, "--reason", "preview", "secret.txt")
	if code != 0 {
		t.Fatalf("human dry run failed (%d): %s", code, stderr)
	}
	machine, stderr, code := runSafegitEnv(t, dir, previewEnv, "--json", "--dry-run", "scrub", "file",
		"--from", initialSHA, "--reason", "preview", "secret.txt")
	if code != 0 {
		t.Fatalf("machine dry run failed (%d): %s", code, stderr)
	}

	env := decodeEnvelope(t, machine)
	payload := string(env.Payload)
	if got, want := payloadInt(t, payload, "commit_count"), humanNumber(t, human, `Commits: (\d+)`); got != want {
		t.Errorf("commit_count = %d but the human summary says %d", got, want)
	}
	if len(env.Preview) == 0 {
		t.Errorf("the envelope's preview is empty for a rewrite preview:\n%s", machine)
	}
	// The two renderings of the same records: the log's body in human mode, the
	// preview member in machine mode.
	if got, want := len(env.Preview), len(wouldDoBody(t, human)); got != want {
		t.Errorf("the envelope carries %d effect records but the would-do log rendered %d lines", got, want)
	}
}

// TestScrubMatchPreviewFiguresAgree: every number in the human preview is a
// payload member, INCLUDING the two that used to live in disjoint branches --
// the "in N objects" denominator (which is objects_matched, never
// objects_scanned) and the estimated commit count (which the human branch never
// printed at all).
func TestScrubMatchPreviewFiguresAgree(t *testing.T) {
	dir, _ := newSecretRepo(t)

	human, stderr, code := runSafegitEnv(t, dir, previewEnv, "--dry-run", "scrub", "match",
		"--pattern", "hunter2", "--replace", "GONE", "--reason", "preview", "--entire-history")
	if code != 0 {
		t.Fatalf("human dry run failed (%d): %s", code, stderr)
	}
	machine, stderr, code := runSafegitEnv(t, dir, previewEnv, "--json", "--dry-run", "scrub", "match",
		"--pattern", "hunter2", "--replace", "GONE", "--reason", "preview", "--entire-history")
	if code != 0 {
		t.Fatalf("machine dry run failed (%d): %s", code, stderr)
	}

	env := decodeEnvelope(t, machine)
	payload := string(env.Payload)

	total := humanNumber(t, human, `Found (\d+) matches in \d+ objects`)
	objects := humanNumber(t, human, `Found \d+ matches in (\d+) objects`)
	estimated := humanNumber(t, human, `Estimated commits in range: (\d+)`)

	if got := payloadInt(t, payload, "total_matches"); got != total {
		t.Errorf("total_matches = %d but the human line says %d", got, total)
	}
	if got := payloadInt(t, payload, "objects_matched"); got != objects {
		t.Errorf("objects_matched = %d but the human line says %d objects", got, objects)
	}
	if got := payloadInt(t, payload, "estimated_commits"); got != estimated {
		t.Errorf("estimated_commits = %d but the human line says %d", got, estimated)
	}
	// objects_scanned counts something else entirely and is reported under its
	// own name, never as the denominator the human line prints.
	if payloadInt(t, payload, "objects_scanned") == 0 {
		t.Error("objects_scanned is missing from the preview payload")
	}
	if len(env.Preview) == 0 {
		t.Errorf("the envelope's preview is empty for a rewrite preview:\n%s", machine)
	}
	if len(wouldDoBody(t, human)) != len(env.Preview) {
		t.Errorf("the would-do log and the envelope's preview disagree on how many effects the rewrite has")
	}
}

// TestScrubRunPreviewFiguresAgree: the recipe preview's totals agree line for
// member, and its rewrite is minted like the other two.
func TestScrubRunPreviewFiguresAgree(t *testing.T) {
	dir, _ := newSecretRepo(t)
	recipe := "recipe.toml"
	writeFile(t, dir, recipe, fmt.Sprintf(`
[[operations]]
pattern = %q
replace = "GONE"
`, "hunter2"))

	human, stderr, code := runSafegitEnv(t, dir, previewEnv, "--dry-run", "scrub", "run",
		recipe, "--reason", "preview", "--entire-history")
	if code != 0 {
		t.Fatalf("human dry run failed (%d): %s", code, stderr)
	}
	machine, stderr, code := runSafegitEnv(t, dir, previewEnv, "--json", "--dry-run", "scrub", "run",
		recipe, "--reason", "preview", "--entire-history")
	if code != 0 {
		t.Fatalf("machine dry run failed (%d): %s", code, stderr)
	}

	env := decodeEnvelope(t, machine)
	payload := string(env.Payload)
	if got, want := payloadInt(t, payload, "total_blob_matches"), humanNumber(t, human, `Total: (\d+) blob`); got != want {
		t.Errorf("total_blob_matches = %d but the human summary says %d", got, want)
	}
	if got, want := payloadInt(t, payload, "estimated_commits"), humanNumber(t, human, `Estimated commits in range: (\d+)`); got != want {
		t.Errorf("estimated_commits = %d but the human summary says %d", got, want)
	}
	if len(env.Preview) == 0 {
		t.Errorf("the envelope's preview is empty for a rewrite preview:\n%s", machine)
	}
}

// TestAuthorRewritePreviewFiguresAgree: same for `author rewrite`, whose
// preview counted commits in one branch and reported them in another.
func TestAuthorRewritePreviewFiguresAgree(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", "a.txt"); code != 0 {
		t.Fatalf("seeding commit failed: %s", stderr)
	}

	human, stderr, code := runSafegit(t, dir, "--dry-run", "author", "rewrite",
		"--old-name", "Test User", "--new-name", "Renamed")
	if code != 0 {
		t.Fatalf("human dry run failed (%d): %s", code, stderr)
	}
	machine, stderr, code := runSafegit(t, dir, "--json", "--dry-run", "author", "rewrite",
		"--old-name", "Test User", "--new-name", "Renamed")
	if code != 0 {
		t.Fatalf("machine dry run failed (%d): %s", code, stderr)
	}

	env := decodeEnvelope(t, machine)
	payload := string(env.Payload)
	matched := humanNumber(t, human, `Would rewrite (\d+) of \d+ commits`)
	checked := humanNumber(t, human, `Would rewrite \d+ of (\d+) commits`)
	if got := payloadInt(t, payload, "commits_matched"); got != matched {
		t.Errorf("commits_matched = %d but the human line says %d", got, matched)
	}
	if got := payloadInt(t, payload, "commits_to_check"); got != checked {
		t.Errorf("commits_to_check = %d but the human line says %d", got, checked)
	}
	if len(env.Preview) == 0 {
		t.Errorf("the envelope's preview is empty for a rewrite preview:\n%s", machine)
	}
}
