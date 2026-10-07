package test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stricttools/safegit/internal/testutil"
)

// scrubSquashJSON mirrors the scrub squash payload fields the tests read.
type scrubSquashJSON struct {
	First        string            `json:"first"`
	Last         string            `json:"last"`
	Squashed     []string          `json:"squashed"`
	OldHead      string            `json:"old_head"`
	SquashCommit string            `json:"squash_commit"`
	Rewrites     map[string]string `json:"rewrites"`
	NewHead      string            `json:"new_head"`
	CleanupOK    *bool             `json:"cleanup_ok"`
}

// runScrubSquash runs `scrub squash` with --json and decodes its payload,
// failing the test when it does not succeed.
func runScrubSquash(t *testing.T, dir, first, last, message string) scrubSquashJSON {
	t.Helper()
	stdout, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "--json", "scrub", "squash",
		"--first", first, "--last", last, "--message", message, "--reason", "squash test")
	if code != 0 {
		t.Fatalf("scrub squash failed (code %d): %s\n%s", code, stderr, stdout)
	}
	var result scrubSquashJSON
	if err := json.Unmarshal([]byte(jsonPayload(t, stdout)), &result); err != nil {
		t.Fatalf("parsing the scrub squash payload: %v\n%s", err, stdout)
	}
	return result
}

// squashJournalStarts returns the rewrite journal's start records written by
// scrub squash.
func squashJournalStarts(t *testing.T, dir string) []rewriteMapLine {
	t.Helper()
	var starts []rewriteMapLine
	for _, line := range readRewriteMaps(t, dir) {
		if line["phase"] == "start" && line["op"] == "scrub-squash" {
			starts = append(starts, line)
		}
	}
	return starts
}

func TestScrubSquashFoldsARangeAndJournalsEveryFoldedCommit(t *testing.T) {
	dir := newRepo(t)
	before := commitFileEnv(t, dir, scrubEnv, "public.txt", "public before\n", "public work before the period")
	p1 := commitFileEnv(t, dir, scrubEnv, "private.txt", "private v1\n", "private work one")
	testutil.Git(t, dir, "tag", "v-inside", p1)
	p2 := commitFileEnv(t, dir, scrubEnv, "private.txt", "private v2\n", "private work two")
	p3 := commitFileEnv(t, dir, scrubEnv, "other.txt", "other\n", "private work three")
	lastTree := testutil.Rev(t, dir, p3+"^{tree}")
	after := commitFileEnv(t, dir, scrubEnv, "after.txt", "public after\n", "public work after the period")
	afterTree := testutil.Rev(t, dir, after+"^{tree}")

	result := runScrubSquash(t, dir, p1, p3, "Squash the private period")

	squash := result.SquashCommit
	if squash == "" {
		t.Fatal("the payload names no squash commit")
	}
	if strings.Join(result.Squashed, " ") != strings.Join([]string{p1, p2, p3}, " ") {
		t.Errorf("squashed = %v, want %v", result.Squashed, []string{p1, p2, p3})
	}

	// The history: the public commit after the period, rewritten onto the
	// squash commit, which sits on the public commit before the period.
	head := testutil.Rev(t, dir, "HEAD")
	if got := testutil.Parents(t, dir, head); len(got) != 1 || got[0] != squash {
		t.Fatalf("HEAD's parents = %v, want the squash commit %s", got, squash)
	}
	if got := testutil.Parents(t, dir, squash); len(got) != 1 || got[0] != before {
		t.Errorf("the squash commit's parents = %v, want the untouched commit before the period %s", got, before)
	}
	if got := testutil.Rev(t, dir, squash+"^{tree}"); got != lastTree {
		t.Errorf("the squash commit's tree = %s, want the last folded commit's %s", got, lastTree)
	}
	if got := commitFormat(t, dir, "%B", squash); got != "Squash the private period" {
		t.Errorf("the squash commit's message = %q", got)
	}
	if got := testutil.Rev(t, dir, "HEAD^{tree}"); got != afterTree {
		t.Errorf("the commit after the period changed its tree: %s, want %s", got, afterTree)
	}
	if got := commitFormat(t, dir, "%s", "HEAD"); got != "public work after the period" {
		t.Errorf("the commit after the period changed its message: %q", got)
	}

	// A tag pointing into the period moves to the squash commit.
	if got := testutil.Rev(t, dir, "v-inside^{commit}"); got != squash {
		t.Errorf("the tag inside the period points at %s, want the squash commit %s", got, squash)
	}

	// The journal records every folded commit against the squash commit, the
	// rewritten commit after the period against its new self, and nothing
	// before the period.
	starts := squashJournalStarts(t, dir)
	if len(starts) != 1 {
		t.Fatalf("the journal holds %d scrub-squash start records, want 1", len(starts))
	}
	commitMap, ok := starts[0]["commit_map"].(map[string]interface{})
	if !ok {
		t.Fatalf("the start record has no commit map: %v", starts[0])
	}
	for _, folded := range []string{p1, p2, p3} {
		if commitMap[folded] != squash {
			t.Errorf("the journal maps folded commit %s to %v, want %s", folded, commitMap[folded], squash)
		}
	}
	if commitMap[after] != head {
		t.Errorf("the journal maps %s to %v, want the rewritten %s", after, commitMap[after], head)
	}
	if _, present := commitMap[before]; present {
		t.Errorf("the journal maps the untouched commit before the period: %v", commitMap[before])
	}
	if result.CleanupOK == nil || !*result.CleanupOK {
		t.Errorf("cleanup_ok is not true")
	}
}

func TestScrubSquashOfTwoPeriodsKeepsThePublicHistoryBetweenThem(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, scrubEnv, "public.txt", "one\n", "public one")
	a1 := commitFileEnv(t, dir, scrubEnv, "a.txt", "a1\n", "first period one")
	a2 := commitFileEnv(t, dir, scrubEnv, "a.txt", "a2\n", "first period two")
	middle := commitFileEnv(t, dir, scrubEnv, "public.txt", "two\n", "public two")
	b1 := commitFileEnv(t, dir, scrubEnv, "b.txt", "b1\n", "second period one")
	b2 := commitFileEnv(t, dir, scrubEnv, "b.txt", "b2\n", "second period two")
	commitFileEnv(t, dir, scrubEnv, "public.txt", "three\n", "public three")
	middleTree := testutil.Rev(t, dir, middle+"^{tree}")

	// Tags follow their commits through the first rewrite, which is how the
	// second period is named afterwards.
	testutil.Git(t, dir, "tag", "second-first", b1)
	testutil.Git(t, dir, "tag", "second-last", b2)

	first := runScrubSquash(t, dir, a1, a2, "Squash the first period")
	second := runScrubSquash(t, dir, "second-first", "second-last", "Squash the second period")

	log := strings.Split(strings.TrimRight(testutil.GitOut(t, dir, "log", "--first-parent", "--format=%s", "HEAD"), "\n"), "\n")
	want := []string{"public three", "Squash the second period", "public two", "Squash the first period", "public one", "initial"}
	if strings.Join(log, "|") != strings.Join(want, "|") {
		t.Errorf("the first-parent history is %q, want %q", log, want)
	}
	if got := testutil.Rev(t, dir, "HEAD~2^{tree}"); got != middleTree {
		t.Errorf("the public commit between the periods changed its tree: %s, want %s", got, middleTree)
	}
	if first.SquashCommit == second.SquashCommit {
		t.Errorf("both periods were folded into one commit")
	}
	if len(squashJournalStarts(t, dir)) != 2 {
		t.Errorf("the journal does not hold one start record per squash")
	}
}

func TestScrubSquashRefusesAMergeInsideTheRange(t *testing.T) {
	dir := newRepo(t)
	p1 := commitFileEnv(t, dir, scrubEnv, "a.txt", "a\n", "period one")
	testutil.Git(t, dir, "switch", "-c", "side")
	testutil.WriteFile(t, dir, "side.txt", "side\n")
	testutil.Git(t, dir, "add", "side.txt")
	testutil.Git(t, dir, "commit", "-m", "side work")
	testutil.Git(t, dir, "switch", "main")
	testutil.Git(t, dir, "merge", "--no-ff", "-m", "merge side", "side")
	merge := testutil.Rev(t, dir, "HEAD")
	last := commitFileEnv(t, dir, scrubEnv, "a.txt", "b\n", "period two")

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "scrub", "squash",
		"--first", p1, "--last", last, "--message", "squash", "--reason", "squash test")
	if code == 0 {
		t.Fatal("a squash over a merge commit was accepted")
	}
	if !strings.Contains(stderr, "merge commit") || !strings.Contains(stderr, merge[:8]) {
		t.Errorf("the refusal does not name the merge commit %s:\n%s", merge[:8], stderr)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != last {
		t.Errorf("a refused squash moved HEAD to %s", got)
	}
	if len(squashJournalStarts(t, dir)) != 0 {
		t.Errorf("a refused squash wrote a journal record")
	}
}

func TestScrubSquashRefusesARangeOffTheFirstParentHistory(t *testing.T) {
	dir := newRepo(t)
	testutil.Git(t, dir, "switch", "-c", "side")
	testutil.WriteFile(t, dir, "side.txt", "side\n")
	testutil.Git(t, dir, "add", "side.txt")
	testutil.Git(t, dir, "commit", "-m", "side work")
	sideCommit := testutil.Rev(t, dir, "HEAD")
	testutil.Git(t, dir, "switch", "main")
	testutil.Git(t, dir, "merge", "--no-ff", "-m", "merge side", "side")
	last := commitFileEnv(t, dir, scrubEnv, "a.txt", "a\n", "after the merge")

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "scrub", "squash",
		"--first", sideCommit, "--last", last, "--message", "squash", "--reason", "squash test")
	if code == 0 {
		t.Fatal("a squash starting off the first-parent history was accepted")
	}
	if !strings.Contains(stderr, "is not on HEAD's first-parent history") {
		t.Errorf("the refusal does not say why:\n%s", stderr)
	}
}

func TestScrubSquashRefusesFirstNewerThanLast(t *testing.T) {
	dir := newRepo(t)
	older := commitFileEnv(t, dir, scrubEnv, "a.txt", "a\n", "older")
	newer := commitFileEnv(t, dir, scrubEnv, "a.txt", "b\n", "newer")

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--approve-consequential", "scrub", "squash",
		"--first", newer, "--last", older, "--message", "squash", "--reason", "squash test")
	if code == 0 {
		t.Fatal("a squash with --first newer than --last was accepted")
	}
	if !strings.Contains(stderr, "is newer than --last") {
		t.Errorf("the refusal does not say why:\n%s", stderr)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != newer {
		t.Errorf("a refused squash moved HEAD to %s", got)
	}
}
