package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// scrubFilePayload is as much of `scrub file`'s payload as this file reads.
type scrubFilePayload struct {
	Version          int               `json:"version"`
	DryRun           bool              `json:"dry_run"`
	File             string            `json:"file"`
	Mode             string            `json:"mode"`
	Range            string            `json:"range"`
	CommitCount      int               `json:"commit_count"`
	OldHead          string            `json:"old_head"`
	Rewrites         map[string]string `json:"rewrites"`
	CommitsRewritten *int              `json:"commits_rewritten"`
	NewHead          string            `json:"new_head"`
	CleanupOK        *bool             `json:"cleanup_ok"`
}

// TestScrubFileInSubmoduleReportsAPayloadWhenNoGitlinkMoved covers the branch
// where a submodule-path `scrub file` finds nothing to move: every submodule
// commit maps to itself, so the parent has no gitlink to follow and its history
// is left alone. The submodule's rewrite is still verified and published on its
// own, and the command succeeds -- so machine mode must carry the payload that
// says what happened, not an envelope with an absent payload.
//
// The branch is reachable with a replacement whose content is byte-identical to
// what history already holds: the target is present (so it is not the typo
// refusal), and replacing it changes no tree.
func TestScrubFileInSubmoduleReportsAPayloadWhenNoGitlinkMoved(t *testing.T) {
	parentDir, _, subDir := newRepoWithSubmoduleSecret(t, "ALREADY_CLEAN", "secret.txt")

	// The replacement source is read at the operator's directory, so it lives
	// outside both repositories: writing it inside the parent would dirty the
	// tree the execute path requires to be clean.
	scratch := t.TempDir()
	replacement := filepath.Join(scratch, "replacement.txt")
	if err := os.WriteFile(replacement, []byte("ALREADY_CLEAN\n"), 0644); err != nil {
		t.Fatal(err)
	}

	parentHeadBefore := testutil.Rev(t, parentDir, "HEAD")
	subHeadBefore := testutil.Rev(t, subDir, "HEAD")
	gitlinkBefore := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))

	stdout, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv,
		"--json", "--approve-consequential", "scrub", "file",
		"--replace-with", replacement, "mysub/secret.txt",
		"--entire-history", "--reason", "replacement equals what history holds",
	)
	if code != 0 {
		t.Fatalf("scrub file failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	env := decodeEnvelope(t, stdout)
	if len(env.Payload) == 0 || string(env.Payload) == "null" {
		t.Fatalf("the envelope carries no payload for a run that concluded successfully:\n%s", stdout)
	}
	var payload scrubFilePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("payload is not a scrub file result: %v\npayload: %s", err, env.Payload)
	}

	if payload.Version != 1 {
		t.Errorf("payload version = %d, want 1", payload.Version)
	}
	if payload.DryRun {
		t.Error("payload says dry_run for an executed rewrite")
	}
	if payload.File != "mysub/secret.txt" {
		t.Errorf("payload file = %q, want the path the operator named", payload.File)
	}
	if payload.Mode != "replace" {
		t.Errorf("payload mode = %q, want replace", payload.Mode)
	}
	if payload.Range != "entire_history" {
		t.Errorf("payload range = %q, want entire_history", payload.Range)
	}
	if payload.CommitCount < 1 {
		t.Errorf("payload commit_count = %d, want the submodule commits the walk covered", payload.CommitCount)
	}
	// The parent is untouched, and the payload says so positively: its head is
	// the head it had, and no commit was remapped.
	if payload.OldHead != parentHeadBefore {
		t.Errorf("payload old_head = %q, want the parent's head %q", payload.OldHead, parentHeadBefore)
	}
	if len(payload.Rewrites) != 0 {
		t.Errorf("payload rewrites = %v, want empty (nothing moved)", payload.Rewrites)
	}
	if payload.CommitsRewritten == nil {
		t.Error("payload carries no commits_rewritten for an executed rewrite")
	} else if *payload.CommitsRewritten != 0 {
		t.Errorf("payload commits_rewritten = %d, want 0 (no commit changed)", *payload.CommitsRewritten)
	}
	if payload.CleanupOK == nil {
		t.Error("payload carries no cleanup_ok for an executed rewrite")
	}

	// Neither repository moved: the payload's claim is the repositories' state.
	if got := testutil.Rev(t, parentDir, "HEAD"); got != parentHeadBefore {
		t.Errorf("the parent's HEAD moved: %s -> %s", parentHeadBefore, got)
	}
	if got := testutil.Rev(t, subDir, "HEAD"); got != subHeadBefore {
		t.Errorf("the submodule's HEAD moved: %s -> %s", subHeadBefore, got)
	}
	if got := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub")); got != gitlinkBefore {
		t.Errorf("the parent gitlink moved: %s -> %s", gitlinkBefore, got)
	}
}
