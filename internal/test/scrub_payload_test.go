package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// Under --json every payload-producing command emits exactly one envelope with
// its own document in the payload member. A command that concludes successfully
// and returns before setting one leaves a machine consumer an envelope whose
// payload is null -- indistinguishable, from the outside, from a command that
// has no payload to give.
//
// The scrub commands have several such early returns: nothing matched, or the
// parent repository had nothing to rewrite while a submodule did. Each of them
// is a successful outcome with something to say, so each says it.

// scrubMatchPayload is as much of `scrub match`'s payload as this file reads.
type scrubMatchPayload struct {
	Version          int               `json:"version"`
	DryRun           bool              `json:"dry_run"`
	Pattern          string            `json:"pattern"`
	Rewrites         map[string]string `json:"rewrites"`
	CommitsRewritten *int              `json:"commits_rewritten"`
	TagsRewritten    *int              `json:"tags_rewritten"`
	OldHead          string            `json:"old_head"`
	NewHead          string            `json:"new_head"`
	CleanupOK        *bool             `json:"cleanup_ok"`
}

// scrubRunPayload is as much of `scrub run`'s payload as this file reads.
type scrubRunPayload struct {
	Version          int               `json:"version"`
	DryRun           bool              `json:"dry_run"`
	OperationCount   int               `json:"operation_count"`
	Rewrites         map[string]string `json:"rewrites"`
	CommitsRewritten *int              `json:"commits_rewritten"`
	OldHead          string            `json:"old_head"`
	NewHead          string            `json:"new_head"`
}

// newRepoWithSubmoduleTagSecret builds a parent repository whose submodule
// carries the pattern in ONE annotated tag's message and nowhere else.
//
// That is what makes the submodule rewritable while every commit maps to
// itself: the tag annotation is rewritten, no commit SHA moves, no gitlink
// follows, and the parent repository has nothing to rewrite at all.
func newRepoWithSubmoduleTagSecret(t *testing.T, secret string) (parentDir, subDir string) {
	t.Helper()
	parentDir, _, subDir = newRepoWithSubmoduleSecret(t, "HARMLESS_CONTENT", "note.txt")
	testutil.Git(t, subDir, "tag", "-a", "annotated", "-m", "release notes with "+secret+" inside")
	return parentDir, subDir
}

// TestScrubMatchReportsAPayloadWhenOnlyASubmoduleWasRewritten covers the branch
// where the parent has nothing to rewrite and a submodule does: the submodule's
// rewrite is verified and published on its own, the command succeeds, and the
// envelope must carry the payload that says so.
//
// new_head stays ABSENT: echoing old_head into it would claim the parent was
// rewritten to the commit it already sat on.
func TestScrubMatchReportsAPayloadWhenOnlyASubmoduleWasRewritten(t *testing.T) {
	const secret = "TAGONLY_MATCH_SECRET_1"
	parentDir, subDir := newRepoWithSubmoduleTagSecret(t, secret)

	parentHeadBefore := testutil.Rev(t, parentDir, "HEAD")
	subHeadBefore := testutil.Rev(t, subDir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv,
		"--json", "--approve-consequential", "scrub", "match",
		"--pattern", secret, "--replace", "REDACTED",
		"--reason", "tag annotation only", "--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	env := decodeEnvelope(t, stdout)
	if len(env.Payload) == 0 || string(env.Payload) == "null" {
		t.Fatalf("the envelope carries no payload for a run that concluded successfully:\n%s", stdout)
	}
	var payload scrubMatchPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("payload is not a scrub match result: %v\npayload: %s", err, env.Payload)
	}

	if payload.Version != 1 {
		t.Errorf("payload version = %d, want 1", payload.Version)
	}
	if payload.DryRun {
		t.Error("payload says dry_run for an executed rewrite")
	}
	if payload.Pattern != secret {
		t.Errorf("payload pattern = %q, want %q", payload.Pattern, secret)
	}
	if payload.OldHead != parentHeadBefore {
		t.Errorf("payload old_head = %q, want the parent's head %q", payload.OldHead, parentHeadBefore)
	}
	if payload.NewHead != "" {
		t.Errorf("payload new_head = %q, want it absent: the parent was not rewritten", payload.NewHead)
	}
	if len(payload.Rewrites) != 0 {
		t.Errorf("payload rewrites = %v, want empty (no commit moved)", payload.Rewrites)
	}
	if payload.CommitsRewritten == nil {
		t.Error("payload carries no commits_rewritten for an executed rewrite")
	} else if *payload.CommitsRewritten != 0 {
		t.Errorf("payload commits_rewritten = %d, want 0", *payload.CommitsRewritten)
	}
	if payload.TagsRewritten == nil || *payload.TagsRewritten < 1 {
		t.Errorf("payload tags_rewritten = %v, want the submodule's rewritten tag annotation", payload.TagsRewritten)
	}
	if payload.CleanupOK == nil {
		t.Error("payload carries no cleanup_ok for an executed rewrite")
	}

	// The payload's claim is the repositories' state.
	if got := testutil.Rev(t, parentDir, "HEAD"); got != parentHeadBefore {
		t.Errorf("the parent's HEAD moved: %s -> %s", parentHeadBefore, got)
	}
	if got := testutil.Rev(t, subDir, "HEAD"); got != subHeadBefore {
		t.Errorf("the submodule's HEAD moved: %s -> %s", subHeadBefore, got)
	}
}

// TestScrubMatchReportsAPayloadWhenNothingMatched: a search that matches
// nothing is a successful answer, and it is an answer the machine mode has to
// be able to read.
func TestScrubMatchReportsAPayloadWhenNothingMatched(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, scrubVerifyEnv, "a.txt", "harmless\n", "seed")
	head := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"--json", "--approve-consequential", "scrub", "match",
		"--pattern", "NEVER_PRESENT_PATTERN_ZZ", "--replace", "R",
		"--reason", "no match", "--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	env := decodeEnvelope(t, stdout)
	if len(env.Payload) == 0 || string(env.Payload) == "null" {
		t.Fatalf("the envelope carries no payload for a successful no-match run:\n%s", stdout)
	}
	var payload scrubMatchPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("payload is not a scrub match result: %v\npayload: %s", err, env.Payload)
	}
	if payload.NewHead != "" {
		t.Errorf("payload new_head = %q, want it absent: nothing was rewritten", payload.NewHead)
	}
	if len(payload.Rewrites) != 0 {
		t.Errorf("payload rewrites = %v, want empty", payload.Rewrites)
	}
	if payload.OldHead != head {
		t.Errorf("payload old_head = %q, want %q", payload.OldHead, head)
	}
	if payload.CommitsRewritten == nil || *payload.CommitsRewritten != 0 {
		t.Errorf("payload commits_rewritten = %v, want 0", payload.CommitsRewritten)
	}
}

// TestScrubRunReportsAPayloadWhenNothingMatched is the recipe twin.
func TestScrubRunReportsAPayloadWhenNothingMatched(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, scrubVerifyEnv, "a.txt", "harmless\n", "seed")
	head := testutil.Rev(t, dir, "HEAD")

	// The recipe lives OUTSIDE the repository: an untracked file inside it
	// would dirty the tree, and a tracked one would contain the pattern and
	// make the run match something.
	recipe := filepath.Join(t.TempDir(), "recipe.toml")
	if err := os.WriteFile(recipe, []byte("[[operations]]\npattern = \"NEVER_PRESENT_PATTERN_ZZ\"\nreplace = \"R\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"--json", "--approve-consequential", "scrub", "run",
		"--reason", "no match", "--entire-history", recipe,
	)
	if code != 0 {
		t.Fatalf("scrub run failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	env := decodeEnvelope(t, stdout)
	if len(env.Payload) == 0 || string(env.Payload) == "null" {
		t.Fatalf("the envelope carries no payload for a successful no-match run:\n%s", stdout)
	}
	var payload scrubRunPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("payload is not a scrub run result: %v\npayload: %s", err, env.Payload)
	}
	if payload.OperationCount != 1 {
		t.Errorf("payload operation_count = %d, want 1", payload.OperationCount)
	}
	if payload.NewHead != "" {
		t.Errorf("payload new_head = %q, want it absent: nothing was rewritten", payload.NewHead)
	}
	if len(payload.Rewrites) != 0 {
		t.Errorf("payload rewrites = %v, want empty", payload.Rewrites)
	}
	if payload.OldHead != head {
		t.Errorf("payload old_head = %q, want %q", payload.OldHead, head)
	}
	if payload.CommitsRewritten == nil || *payload.CommitsRewritten != 0 {
		t.Errorf("payload commits_rewritten = %v, want 0", payload.CommitsRewritten)
	}
}
