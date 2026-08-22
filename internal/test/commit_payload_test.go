package test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// What `commit` reports, in both renderings, is derived from the objects it
// created -- never counted from the arguments it was given. The two disagree
// routinely: a named directory expands, a gitignored path under one is skipped,
// and a named path that changes nothing is refused outright.
//
// These tests read the machine payload, which is the same derivation the human
// line's count comes from.

// commitPayloadDoc mirrors the payload `commit` declares. It is spelled out
// here rather than imported so the test asserts the wire shape a consumer sees,
// not the producer's own struct.
type commitPayloadDoc struct {
	Ref            string   `json:"ref"`
	Parents        []string `json:"parents"`
	Tree           string   `json:"tree"`
	SHA            *string  `json:"sha"`
	OldSHA         *string  `json:"old_sha"`
	Files          []string `json:"files"`
	SkippedIgnored []string `json:"skipped_ignored"`
	Attempts       int      `json:"attempts"`
	DryRun         bool     `json:"dry_run"`
}

// commitPayloadOf runs safegit in machine mode and returns the decoded payload.
func commitPayloadOf(t *testing.T, dir string, args ...string) commitPayloadDoc {
	t.Helper()
	stdout, stderr, code := runSafegit(t, dir, append([]string{"--json"}, args...)...)
	if code != 0 {
		t.Fatalf("safegit %s --json failed (%d): stdout=%s stderr=%s", strings.Join(args, " "), code, stdout, stderr)
	}
	var doc commitPayloadDoc
	if err := json.Unmarshal(decodeEnvelope(t, stdout).Payload, &doc); err != nil {
		t.Fatalf("commit payload does not decode: %v\nstdout: %s", err, stdout)
	}
	return doc
}

// TestCommitPayloadShape: every member is present and answers for the commit
// that was actually made.
func TestCommitPayloadShape(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "one\n")
	parent := safegitCommit(t, dir, "seed", "a.txt")

	testutil.WriteFile(t, dir, "a.txt", "two\n")
	testutil.WriteFile(t, dir, "b.txt", "new\n")

	doc := commitPayloadOf(t, dir, "commit", "-m", "second", "--", "a.txt", "b.txt")

	if doc.Ref != "refs/heads/main" {
		t.Errorf("ref = %q, want refs/heads/main", doc.Ref)
	}
	if len(doc.Parents) != 1 || doc.Parents[0] != parent {
		t.Errorf("parents = %v, want [%s]", doc.Parents, parent)
	}
	if doc.SHA == nil || *doc.SHA != testutil.Rev(t, dir, "HEAD") {
		t.Errorf("sha = %v, want the new tip %s", doc.SHA, testutil.Rev(t, dir, "HEAD"))
	}
	if doc.OldSHA != nil {
		t.Errorf("old_sha = %v; a plain commit replaces nothing", *doc.OldSHA)
	}
	if doc.Tree != testutil.Rev(t, dir, "HEAD^{tree}") {
		t.Errorf("tree = %q, want %q", doc.Tree, testutil.Rev(t, dir, "HEAD^{tree}"))
	}
	if strings.Join(doc.Files, ",") != "a.txt,b.txt" {
		t.Errorf("files = %v, want [a.txt b.txt]", doc.Files)
	}
	if len(doc.SkippedIgnored) != 0 {
		t.Errorf("skipped_ignored = %v, want empty", doc.SkippedIgnored)
	}
	if doc.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", doc.Attempts)
	}
	if doc.DryRun {
		t.Error("dry_run = true for an executing run")
	}
}

// TestCommitPayloadCountsWhatTheCommitHolds: the payload's file list and the
// human line's count are one derivation, and neither is the argument count. A
// directory argument expands to the paths it holds.
func TestCommitPayloadCountsWhatTheCommitHolds(t *testing.T) {
	dir := newRepo(t)
	intakeExpMkdir(t, dir, "pkg")
	testutil.WriteFile(t, dir, "pkg/one.txt", "one\n")
	testutil.WriteFile(t, dir, "pkg/two.txt", "two\n")

	doc := commitPayloadOf(t, dir, "commit", "-m", "add pkg", "--", "pkg")
	if strings.Join(doc.Files, ",") != "pkg/one.txt,pkg/two.txt" {
		t.Fatalf("files = %v, want the two expanded paths", doc.Files)
	}

	// The human rendering of the same commit reports the same number, which is
	// two, not the one argument that was typed.
	dir2 := newRepo(t)
	intakeExpMkdir(t, dir2, "pkg")
	testutil.WriteFile(t, dir2, "pkg/one.txt", "one\n")
	testutil.WriteFile(t, dir2, "pkg/two.txt", "two\n")
	stdout, stderr, code := runSafegit(t, dir2, "commit", "-m", "add pkg", "--", "pkg")
	if code != 0 {
		t.Fatalf("commit failed (%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "2 file(s) committed") {
		t.Errorf("expected the human line to report 2 files, got:\n%s", stdout)
	}
}

// TestCommitPayloadCarriesSkippedIgnoredPaths: a gitignored path under an
// expanded directory produces no stderr line, so the payload is where a machine
// consumer learns it was passed over.
func TestCommitPayloadCarriesSkippedIgnoredPaths(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, ".gitignore", "*.log\n")
	safegitCommit(t, dir, "add gitignore", ".gitignore")

	intakeExpMkdir(t, dir, "app")
	testutil.WriteFile(t, dir, "app/main.txt", "source\n")
	testutil.WriteFile(t, dir, "app/debug.log", "noise\n")

	doc := commitPayloadOf(t, dir, "commit", "-m", "add app", "--", "app")
	if strings.Join(doc.Files, ",") != "app/main.txt" {
		t.Errorf("files = %v, want just app/main.txt", doc.Files)
	}
	if strings.Join(doc.SkippedIgnored, ",") != "app/debug.log" {
		t.Errorf("skipped_ignored = %v, want [app/debug.log]", doc.SkippedIgnored)
	}
}

// TestCommitPayloadUnderDryRunReportsNoSHA: a preview builds an object to
// compute the tree honestly, but no commit exists at that name, so the payload
// reports none.
func TestCommitPayloadUnderDryRunReportsNoSHA(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "one\n")
	head := testutil.Rev(t, dir, "HEAD")

	doc := commitPayloadOf(t, dir, "--dry-run", "commit", "-m", "preview", "--", "a.txt")
	if doc.SHA != nil {
		t.Errorf("sha = %q under --dry-run; a preview creates no commit", *doc.SHA)
	}
	if !doc.DryRun {
		t.Error("dry_run = false under --dry-run")
	}
	if strings.Join(doc.Files, ",") != "a.txt" {
		t.Errorf("files = %v, want [a.txt]: a preview still reports what it would contain", doc.Files)
	}
	if now := testutil.Rev(t, dir, "HEAD"); now != head {
		t.Errorf("the preview moved HEAD: %s -> %s", head, now)
	}
}

// TestAmendPayloadShape: the amend form fills old_sha, and its file list is
// what the amend changed about the tip it replaced -- not the whole content of
// the amended commit.
func TestAmendPayloadShape(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "kept.txt", "kept\n")
	safegitCommit(t, dir, "seed", "kept.txt")
	testutil.WriteFile(t, dir, "tip.txt", "tip\n")
	tip := safegitCommit(t, dir, "tip", "tip.txt")

	testutil.WriteFile(t, dir, "extra.txt", "extra\n")
	doc := commitPayloadOf(t, dir, "commit", "--amend", "-m", "tip plus extra", "--", "extra.txt")

	if doc.OldSHA == nil || *doc.OldSHA != tip {
		t.Errorf("old_sha = %v, want the replaced tip %s", doc.OldSHA, tip)
	}
	if doc.SHA == nil || *doc.SHA != testutil.Rev(t, dir, "HEAD") {
		t.Errorf("sha = %v, want the new tip", doc.SHA)
	}
	if strings.Join(doc.Files, ",") != "extra.txt" {
		t.Errorf("files = %v, want [extra.txt]: the delta against the replaced tip", doc.Files)
	}
	if doc.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", doc.Attempts)
	}
}

// TestAmendReportsItsFileCount: the amend line used to say only that something
// was amended, with no count at all.
func TestAmendReportsItsFileCount(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "tip.txt", "tip\n")
	safegitCommit(t, dir, "tip", "tip.txt")

	testutil.WriteFile(t, dir, "one.txt", "one\n")
	testutil.WriteFile(t, dir, "two.txt", "two\n")
	stdout, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "tip plus two", "--", "one.txt", "two.txt")
	if code != 0 {
		t.Fatalf("amend failed (%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "2 file(s) amended") {
		t.Errorf("expected the amend line to report 2 files, got:\n%s", stdout)
	}
}

// TestRewordPayloadShape: a reword replaces a message, so its changed-path list
// is empty -- and the payload says so rather than omitting the member.
func TestRewordPayloadShape(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "tip.txt", "tip\n")
	tip := safegitCommit(t, dir, "tip", "tip.txt")

	doc := commitPayloadOf(t, dir, "commit", "--amend", "-m", "reworded")

	if doc.OldSHA == nil || *doc.OldSHA != tip {
		t.Errorf("old_sha = %v, want the replaced tip %s", doc.OldSHA, tip)
	}
	if len(doc.Files) != 0 {
		t.Errorf("files = %v, want empty: a reword changes no path", doc.Files)
	}
	if doc.Tree != testutil.Rev(t, dir, "HEAD^{tree}") {
		t.Errorf("tree = %q, want the unchanged tree %q", doc.Tree, testutil.Rev(t, dir, "HEAD^{tree}"))
	}
}
