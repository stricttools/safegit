package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// FINDING: on the commit-stands paths a conclusion can end nonzero WITHOUT
// emitting the machine-mode envelope at all, and where it does emit one the
// payload does not say what happened.
//
// Two halves, both about the same family of outcomes -- the commit was created
// and something after it went wrong:
//
//   - finishConclusion's failures go through die(), which writes to stderr and
//     exits. Under --json that leaves stdout COMPLETELY EMPTY next to a commit
//     that exists, so a machine consumer is told nothing about a ref that moved.
//   - a conclusion whose autostash could not be applied stores the work as a
//     real stash entry, says so on stderr and exits nonzero -- but its PAYLOAD
//     is indistinguishable from a clean success once the commit's own identity
//     is set aside, so nothing in the document says the work is parked.
//
// RULED TARGET (the family-code ruling): the envelope is ALWAYS emitted on the
// commit-stands paths, with the payload carrying what happened.

// TestConclusionEmitsAnEnvelopeWhenThePostCommitStepFails: the working-tree
// write a conclusion owes cannot be made, because the path it must write is a
// non-empty DIRECTORY. The commit is real; the envelope must describe it.
func TestConclusionEmitsAnEnvelopeWhenThePostCommitStepFails(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true})
	before := testutil.Rev(t, fx.dir, "HEAD")

	// A directory where the resolved file has to be written. `ours` reads its
	// content from the index stage, so every check in front of the commit passes
	// and only the materialization afterwards can fail.
	abs := filepath.Join(fx.dir, "conflicted.txt")
	if err := os.Remove(abs); err != nil {
		t.Fatalf("removing the conflicted file: %v", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatalf("putting a directory in its place: %v", err)
	}
	testutil.WriteFileAt(t, filepath.Join(abs, "occupant.txt"), "this directory is not empty\n")

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"--json", "merge-continue", "--resolve", "conflicted.txt=ours")
	if code == 0 {
		t.Fatalf("the post-commit working-tree write cannot succeed here, so the run must exit nonzero\nstdout=%s\nstderr=%s", stdout, stderr)
	}

	head := testutil.Rev(t, fx.dir, "HEAD")
	if head == before {
		t.Fatalf("no commit was created, so this is not the commit-stands path the finding is about\nstderr=%s", stderr)
	}

	if strings.TrimSpace(stdout) == "" {
		t.Fatalf("machine mode produced NO envelope for a run that created commit %s and exited %d\nstderr=%s", head, code, stderr)
	}
	var envelope struct {
		ExitCode int                    `json:"exit_code"`
		Payload  map[string]interface{} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("stdout is not one JSON document (%v):\n%s", err, stdout)
	}
	if envelope.Payload == nil {
		t.Fatalf("the envelope carries no payload for a run that created commit %s:\n%s", head, stdout)
	}
	if sha, _ := envelope.Payload["sha"].(string); sha != head {
		t.Errorf("payload sha = %v, want the commit that was created (%s)", envelope.Payload["sha"], head)
	}
	if envelope.ExitCode == 0 {
		t.Errorf("the envelope reports exit_code 0 for a run that could not finish:\n%s", stdout)
	}
}

// identityMembers are the payload members that describe WHICH commit was made
// rather than WHAT HAPPENED. Two conclusions of two different fixtures always
// differ in these, so they are set aside before the documents are compared.
var identityMembers = []string{"sha", "head", "tree", "files", "parents", "ref", "attempts"}

// outcomeResidue is a conclusion payload with the commit's identity removed:
// what is left is the document's account of the outcome.
func outcomeResidue(t *testing.T, stdout string) string {
	t.Helper()
	var envelope struct {
		Payload map[string]interface{} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("stdout is not one JSON document (%v):\n%s", err, stdout)
	}
	if envelope.Payload == nil {
		t.Fatalf("the envelope carries no payload:\n%s", stdout)
	}
	for _, m := range identityMembers {
		delete(envelope.Payload, m)
	}
	// Go marshals a map with its keys sorted, so the rendering is stable.
	data, err := json.Marshal(envelope.Payload)
	if err != nil {
		t.Fatalf("re-rendering the payload: %v", err)
	}
	return string(data)
}

// TestStoredAutostashPayloadIsDistinguishableFromCleanSuccess: two conclusions
// of the same shape, one of which could not put the operator's work back and
// parked it as a stash entry instead. Their payloads must not say the same
// thing.
func TestStoredAutostashPayloadIsDistinguishableFromCleanSuccess(t *testing.T) {
	// The autostash applies cleanly: nothing is parked anywhere.
	applied := newAutostashMergeRepo(t, false)
	appliedOut, appliedErr, appliedCode := runSafegitEnv(t, applied.dir, conclusionSession,
		"--json", "merge-continue", "--resolve", "conflicted.txt=theirs")
	if appliedCode != 0 {
		t.Fatalf("the clean-success conclusion failed (code %d): %s", appliedCode, appliedErr)
	}

	// The autostash conflicts with the merge result: the commit stands, the work
	// is stored as stash@{0}, and the exit is nonzero.
	stored := newAutostashMergeRepo(t, true)
	storedOut, storedErr, storedCode := runSafegitEnv(t, stored.dir, conclusionSession,
		"--json", "merge-continue", "--resolve", "conflicted.txt=theirs")
	if storedCode == 0 {
		t.Fatalf("the conflicting autostash must not exit 0; the fixture no longer produces the case:\n%s", storedErr)
	}
	if strings.TrimSpace(testutil.Git(t, stored.dir, "stash", "list")) == "" {
		t.Fatalf("the fixture must park the work as a stash entry:\n%s", storedErr)
	}

	if got, want := outcomeResidue(t, storedOut), outcomeResidue(t, appliedOut); got == want {
		t.Errorf("the stored-autostash payload says exactly what a clean success says once the commit's identity is set aside;\n"+
			"nothing in the document tells a consumer the operator's work is parked in a stash.\n"+
			"  both: %s\n  (exit codes %d and %d are the only difference)", got, appliedCode, storedCode)
	}
}
