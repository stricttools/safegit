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
	// ExecutionMode names which form of `commit` ran where the argv alone does
	// not say: "amend" or "reword" on the --amend path, null on a plain commit.
	// The two are one command and one authorship, and the difference is reported
	// HERE and nowhere else -- no stderr line announces it.
	ExecutionMode *string `json:"execution_mode"`
	// Residue is every step this commit owed after its ref update and did not
	// finish. It is declared on every form and never null: a run that finished
	// everything reports an empty list, which is what tells a consumer the
	// question was answered rather than not asked.
	Residue []struct {
		Step   string `json:"step"`
		Detail string `json:"detail"`
	} `json:"residue"`
	DryRun bool `json:"dry_run"`
	// MovedRecords is every move record THIS operation put on the commit: the
	// caller's declarations and the records safegit minted from the commit's own
	// delta, each naming which of the two it is. A record carried across from a
	// message being replaced is not one of them.
	MovedRecords []struct {
		ID     string `json:"id"`
		Old    string `json:"old"`
		New    string `json:"new"`
		Origin string `json:"origin"`
	} `json:"moved_records"`
	// RefusedMoves is every candidate the delta suggested and a fence declined,
	// with the paths on each side and the reason.
	RefusedMoves []struct {
		Old    []string `json:"old"`
		New    []string `json:"new"`
		Reason string   `json:"reason"`
	} `json:"refused_moves"`
	// MovesOverCap is how many moves the delta witnessed when the cap turned all
	// of them down, and zero otherwise.
	MovesOverCap int `json:"moves_over_cap"`
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
	if doc.ExecutionMode != nil {
		t.Errorf("execution_mode = %q; a plain commit replaces nothing and reports none", *doc.ExecutionMode)
	}
	if doc.DryRun {
		t.Error("dry_run = true for an executing run")
	}
}

// TestEveryCommitFormCarriesTheResidueMember: the payload of a command that
// moves a ref says what it owed afterwards and did not finish -- on the clean
// run too, as an empty list.
//
// The member is what the commit-stands exit code (26) refers to, and it was
// declared only on the conclusion commands' payloads. `commit`, its two --amend
// forms and `mv` reach exactly the same aftercare failures (the index reconcile,
// the parent's gitlink) and reported nothing about them, so an envelope carrying
// that code from one of them named no step at all.
func TestEveryCommitFormCarriesTheResidueMember(t *testing.T) {
	// The map decode: an ABSENT array member and a present empty one both
	// decode into an empty slice through the struct, and the point here is that
	// the member is declared.
	residueOf := func(t *testing.T, dir string, args ...string) (interface{}, bool) {
		t.Helper()
		stdout, stderr, code := runSafegit(t, dir, append([]string{"--json"}, args...)...)
		if code != 0 {
			t.Fatalf("safegit %s --json failed (%d): stdout=%s stderr=%s", strings.Join(args, " "), code, stdout, stderr)
		}
		var doc map[string]interface{}
		if err := json.Unmarshal(decodeEnvelope(t, stdout).Payload, &doc); err != nil {
			t.Fatalf("payload does not decode: %v\nstdout: %s", err, stdout)
		}
		v, present := doc["residue"]
		return v, present
	}

	for _, tc := range []struct {
		name string
		run  func(t *testing.T, dir string) (interface{}, bool)
	}{
		{"commit", func(t *testing.T, dir string) (interface{}, bool) {
			testutil.WriteFile(t, dir, "b.txt", "new\n")
			return residueOf(t, dir, "commit", "-m", "second", "--", "b.txt")
		}},
		{"amend", func(t *testing.T, dir string) (interface{}, bool) {
			testutil.WriteFile(t, dir, "a.txt", "amended\n")
			return residueOf(t, dir, "commit", "--amend", "-m", "reworded and amended", "--", "a.txt")
		}},
		{"reword", func(t *testing.T, dir string) (interface{}, bool) {
			return residueOf(t, dir, "commit", "--amend", "-m", "a new message")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			testutil.WriteFile(t, dir, "a.txt", "one\n")
			safegitCommit(t, dir, "seed", "a.txt")

			value, present := tc.run(t, dir)
			if !present {
				t.Fatalf("the %s payload declares no residue member; an envelope exiting the commit-stands code from it names no step", tc.name)
			}
			list, isList := value.([]interface{})
			if !isList {
				t.Fatalf("residue = %#v, want a list (never null)", value)
			}
			if len(list) != 0 {
				t.Errorf("residue = %v on a run that finished everything it owed", list)
			}
		})
	}
}

// TestExecutionModeNamesTheAmendForm is the whole of what `commit --amend`
// announces about which of its two forms ran.
//
// `--amend` resolves to an AMEND when files, hunks or untrack targets are named
// and to a REWORD when none are -- one command line, two things done, and
// nothing on the argv says which. The two are identical in authorship and in
// safety (both are the pipeline's own commit, both move the ref under
// compare-and-swap, both are undoable), so the difference is reported in the
// machine payload alone: no stderr line, no human-mode announcement.
//
// The member is present on every form, per the schema's own nullable
// convention: a consumer reads it rather than inferring the form from the
// absence of a key.
func TestExecutionModeNamesTheAmendForm(t *testing.T) {
	// The map decode, not the struct: a member that is ABSENT and one that is
	// present-and-null both arrive as a nil pointer, and the schema declares
	// this one present on every form.
	modeOf := func(t *testing.T, dir string, args ...string) (interface{}, bool) {
		t.Helper()
		stdout, stderr, code := runSafegit(t, dir, append([]string{"--json"}, args...)...)
		if code != 0 {
			t.Fatalf("safegit %s --json failed (%d): %s", strings.Join(args, " "), code, stderr)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(decodeEnvelope(t, stdout).Payload, &payload); err != nil {
			t.Fatalf("payload does not decode: %v\n%s", err, stdout)
		}
		v, present := payload["execution_mode"]
		return v, present
	}

	t.Run("a plain commit reports none", func(t *testing.T) {
		dir := newRepo(t)
		testutil.WriteFile(t, dir, "a.txt", "one\n")
		mode, present := modeOf(t, dir, "commit", "-m", "plain", "--", "a.txt")
		if !present {
			t.Fatal("execution_mode is absent from a plain commit's payload; the schema declares it on every form")
		}
		if mode != nil {
			t.Errorf("execution_mode = %v, want null: a plain commit is neither an amend nor a reword", mode)
		}
	})

	t.Run("an amend names itself", func(t *testing.T) {
		dir := newRepo(t)
		testutil.WriteFile(t, dir, "tip.txt", "tip\n")
		safegitCommit(t, dir, "tip", "tip.txt")
		testutil.WriteFile(t, dir, "extra.txt", "extra\n")
		mode, present := modeOf(t, dir, "commit", "--amend", "-m", "tip plus extra", "--", "extra.txt")
		if !present {
			t.Fatal("execution_mode is absent from an amend's payload")
		}
		if mode != "amend" {
			t.Errorf("execution_mode = %v, want \"amend\"", mode)
		}
	})

	t.Run("a reword names itself", func(t *testing.T) {
		dir := newRepo(t)
		testutil.WriteFile(t, dir, "tip.txt", "tip\n")
		safegitCommit(t, dir, "tip", "tip.txt")
		mode, present := modeOf(t, dir, "commit", "--amend", "-m", "reworded")
		if !present {
			t.Fatal("execution_mode is absent from a reword's payload")
		}
		if mode != "reword" {
			t.Errorf("execution_mode = %v, want \"reword\"", mode)
		}
	})

	t.Run("a previewed amend reports the same form", func(t *testing.T) {
		dir := newRepo(t)
		testutil.WriteFile(t, dir, "tip.txt", "tip\n")
		safegitCommit(t, dir, "tip", "tip.txt")
		testutil.WriteFile(t, dir, "extra.txt", "extra\n")
		mode, _ := modeOf(t, dir, "--dry-run", "commit", "--amend", "-m", "preview", "--", "extra.txt")
		if mode != "amend" {
			t.Errorf("execution_mode = %v under --dry-run, want \"amend\": a preview previews a form", mode)
		}
	})
}

// TestNoStderrLineAnnouncesTheAmendForm: the amend/reword split is a payload
// member and nothing else. A stderr line would be an announcement of a
// difference that changes neither authorship nor safety.
func TestNoStderrLineAnnouncesTheAmendForm(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "tip.txt", "tip\n")
	safegitCommit(t, dir, "tip", "tip.txt")

	_, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "reworded")
	if code != 0 {
		t.Fatalf("reword failed (%d): %s", code, stderr)
	}
	for _, unwanted := range []string{"execution_mode", "execution mode", "resolved to"} {
		if strings.Contains(stderr, unwanted) {
			t.Errorf("stderr announces the amend form (%q):\n%s", unwanted, stderr)
		}
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
	if doc.ExecutionMode == nil || *doc.ExecutionMode != "amend" {
		t.Errorf("execution_mode = %v, want \"amend\"", doc.ExecutionMode)
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
	if doc.ExecutionMode == nil || *doc.ExecutionMode != "reword" {
		t.Errorf("execution_mode = %v, want \"reword\"", doc.ExecutionMode)
	}
}
