package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// The commit-stands family, exit 26.
//
// Every member is a run in which safegit's OWN commit is real -- the ref moved,
// the object is the branch's tip -- and a step that can only happen after the
// ref update did not finish: reconciling the shared index, removing a concluded
// operation's state files, writing the resolutions into the working tree,
// bumping a parent's gitlink, putting an autostash back.
//
// The two things every member owes are asserted here for each shape:
//
//   - the exit code is the family code, not the undifferentiated General. A
//     caller that reads 1 cannot tell "the operation did not happen" from "the
//     operation happened and its aftercare did not", and the guess a script
//     makes by default -- retry -- is the expensive one.
//   - under --json the ENVELOPE is emitted, carrying the payload the run would
//     have carried. A ref moved; stdout saying nothing about it is the finding
//     sequencer_conclusion_envelope_test.go states.

// blockTheSharedIndex plants `.git/index.lock`, which is git's own exclusive
// lock over `.git/index`.
//
// It is the lever every reconcile test below turns, and it is chosen because of
// WHERE it bites. Reading the index is unaffected (safegit prefixes every git
// invocation with --no-optional-locks, so nothing it runs takes the lock to
// read), and so is writing objects, moving a ref and appending to the oplog --
// which is to say everything the commit pipeline does BEFORE the ref update.
// The first thing that needs to write the shared index is the reconciliation
// that follows the ref update, and that is exactly the step under test.
func blockTheSharedIndex(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, ".git", "index.lock")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("planting %s: %v", path, err)
	}
}

// familyEnvelope is the shared assertion: the run exited the family code, the
// commit it created is the branch tip, and the envelope on stdout names it.
//
// wantSHA is the SHA the payload must carry; an empty wantSHA means "whatever
// HEAD now is", which is what every shape except a root undo wants.
func familyEnvelope(t *testing.T, dir, stdout, stderr string, code int, member string) map[string]interface{} {
	t.Helper()
	if code != exitcode.CommitStands {
		t.Errorf("%s exited %d, want the commit-stands family code %d\nstderr=%s",
			member, code, exitcode.CommitStands, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatalf("%s produced NO envelope for a run whose ref moved (exit %d)\nstderr=%s", member, code, stderr)
	}
	var envelope struct {
		ExitCode int                    `json:"exit_code"`
		Payload  map[string]interface{} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("%s: stdout is not one JSON document (%v):\n%s", member, err, stdout)
	}
	if envelope.ExitCode != exitcode.CommitStands {
		t.Errorf("%s: envelope exit_code = %d, want %d\n%s", member, envelope.ExitCode, exitcode.CommitStands, stdout)
	}
	if envelope.Payload == nil {
		t.Fatalf("%s: the envelope carries no payload for a run whose ref moved:\n%s", member, stdout)
	}
	return envelope.Payload
}

// assertPayloadSHAIsHead checks the payload names the commit that is now the
// branch's tip: the whole point of the family is that the commit STANDS, so the
// document has to say which one.
func assertPayloadSHAIsHead(t *testing.T, dir string, payload map[string]interface{}, member string) {
	t.Helper()
	head := testutil.Rev(t, dir, "HEAD")
	if sha, _ := payload["sha"].(string); sha != head {
		t.Errorf("%s: payload sha = %v, want the commit that stands (%s)", member, payload["sha"], head)
	}
}

func TestCommitPostRefReconcileFailureStands(t *testing.T) {
	dir := newRepo(t)
	before := testutil.Rev(t, dir, "HEAD")
	testutil.WriteFile(t, dir, "a.txt", "a\n")

	blockTheSharedIndex(t, dir)
	stdout, stderr, code := runSafegit(t, dir, "--json", "commit", "-m", "add a", "--", "a.txt")

	if head := testutil.Rev(t, dir, "HEAD"); head == before {
		t.Fatalf("no commit was created, so this is not the commit-stands path\nstderr=%s", stderr)
	}
	payload := familyEnvelope(t, dir, stdout, stderr, code, "commit")
	assertPayloadSHAIsHead(t, dir, payload, "commit")
}

func TestAmendPostRefReconcileFailureStands(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "a\n")
	before := safegitCommit(t, dir, "add a", "a.txt")
	testutil.WriteFile(t, dir, "a.txt", "a2\n")

	blockTheSharedIndex(t, dir)
	stdout, stderr, code := runSafegit(t, dir, "--json", "commit", "--amend", "-m", "add a, twice", "--", "a.txt")

	if head := testutil.Rev(t, dir, "HEAD"); head == before {
		t.Fatalf("nothing was amended, so this is not the commit-stands path\nstderr=%s", stderr)
	}
	payload := familyEnvelope(t, dir, stdout, stderr, code, "amend")
	assertPayloadSHAIsHead(t, dir, payload, "amend")
}

func TestRewordPostRefReconcileFailureStands(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "a\n")
	before := safegitCommit(t, dir, "add a", "a.txt")

	blockTheSharedIndex(t, dir)
	stdout, stderr, code := runSafegit(t, dir, "--json", "commit", "--amend", "-m", "a better subject")

	if head := testutil.Rev(t, dir, "HEAD"); head == before {
		t.Fatalf("nothing was reworded, so this is not the commit-stands path\nstderr=%s", stderr)
	}
	payload := familyEnvelope(t, dir, stdout, stderr, code, "reword")
	assertPayloadSHAIsHead(t, dir, payload, "reword")
}

func TestMvPostRefReconcileFailureStands(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "a\n")
	before := safegitCommit(t, dir, "add a", "a.txt")

	blockTheSharedIndex(t, dir)
	stdout, stderr, code := runSafegit(t, dir, "--json", "mv", "-m", "move a", "a.txt -> b.txt")

	if head := testutil.Rev(t, dir, "HEAD"); head == before {
		t.Fatalf("nothing was committed, so this is not the commit-stands path\nstderr=%s", stderr)
	}
	payload := familyEnvelope(t, dir, stdout, stderr, code, "mv")
	assertPayloadSHAIsHead(t, dir, payload, "mv")
}

// TestUndoPostRefReconcileFailureStands: undo moves a REF, and the same family
// verdict applies to it -- the ref is back where the oplog says, and the index
// could not be put in step with it. undo declares no payload schema of its own
// yet, so what is pinned here is the exit code and the ref having moved.
func TestUndoPostRefReconcileFailureStands(t *testing.T) {
	session := []string{"CLAUDE_CODE_SESSION_ID=commit-stands-test"}
	dir := newRepo(t)
	base := testutil.Rev(t, dir, "HEAD")
	testutil.WriteFile(t, dir, "a.txt", "a\n")
	safegitCommitEnv(t, dir, session, "add a", "a.txt")

	blockTheSharedIndex(t, dir)
	_, stderr, code := runSafegitEnv(t, dir, session, "undo")

	if head := testutil.Rev(t, dir, "HEAD"); head != base {
		t.Fatalf("the ref did not move back, so this is not the ref-moved path (head=%s, base=%s)\nstderr=%s", head, base, stderr)
	}
	if code != exitcode.CommitStands {
		t.Errorf("undo exited %d, want the commit-stands family code %d\nstderr=%s", code, exitcode.CommitStands, stderr)
	}
}

// TestConclusionPostCommitFailureExitsTheFamilyCode is the exit-code half of
// TestConclusionEmitsAnEnvelopeWhenThePostCommitStepFails, which asserts only
// that the code is nonzero. The fixture is the same: the working-tree write the
// conclusion owes cannot be made, because a non-empty DIRECTORY sits where the
// resolved file has to go.
func TestConclusionPostCommitFailureExitsTheFamilyCode(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true})
	before := testutil.Rev(t, fx.dir, "HEAD")

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

	if head := testutil.Rev(t, fx.dir, "HEAD"); head == before {
		t.Fatalf("no commit was created, so this is not the commit-stands path\nstderr=%s", stderr)
	}
	payload := familyEnvelope(t, fx.dir, stdout, stderr, code, "merge-continue")
	assertPayloadSHAIsHead(t, fx.dir, payload, "merge-continue")

	// The residue member says WHICH step did not finish, so a consumer does not
	// have to read the stderr line to find out.
	residue, ok := payload["residue"].([]interface{})
	if !ok {
		t.Fatalf("the payload carries no residue list: %v", payload["residue"])
	}
	if len(residue) == 0 {
		t.Errorf("the residue list is empty for a run whose working-tree write failed:\n%s", stdout)
	}
}

// TestStoredAutostashExitsTheFamilyCode: the conclusion could not put the
// operator's uncommitted work back and parked it as a stash entry. The commit
// stands, so the exit is the family code rather than the undifferentiated
// General, and the payload's autostash member says where the work went.
func TestStoredAutostashExitsTheFamilyCode(t *testing.T) {
	fx := newAutostashMergeRepo(t, true)
	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"--json", "merge-continue", "--resolve", "conflicted.txt=theirs")

	if strings.TrimSpace(testutil.Git(t, fx.dir, "stash", "list")) == "" {
		t.Fatalf("the fixture must park the work as a stash entry:\n%s", stderr)
	}
	payload := familyEnvelope(t, fx.dir, stdout, stderr, code, "merge-continue (stored autostash)")

	autostash, ok := payload["autostash"].(map[string]interface{})
	if !ok {
		t.Fatalf("the payload carries no autostash member: %v", payload["autostash"])
	}
	if state, _ := autostash["state"].(string); state != "stored" {
		t.Errorf("autostash state = %v, want \"stored\"\n%s", autostash["state"], stdout)
	}
	if sha, _ := autostash["stash"].(string); sha == "" {
		t.Errorf("autostash carries no stash commit, but one was stored:\n%s", stdout)
	}
}

// TestCleanConclusionReportsNoResidue is the other side of the family: a
// conclusion that finished everything it owes exits 0, reports the autostash as
// absent and the residue as empty. Without it the members above could be
// satisfied by a payload that always claims residue.
func TestCleanConclusionReportsNoResidue(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession})
	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"--json", "merge-continue", "--resolve", "conflicted.txt=theirs")
	if code != 0 {
		t.Fatalf("the clean conclusion failed (code %d): %s", code, stderr)
	}
	var envelope struct {
		Payload map[string]interface{} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("stdout is not one JSON document (%v):\n%s", err, stdout)
	}
	residue, ok := envelope.Payload["residue"].([]interface{})
	if !ok {
		t.Fatalf("the payload carries no residue list: %v", envelope.Payload["residue"])
	}
	if len(residue) != 0 {
		t.Errorf("a clean conclusion reports residue: %v", residue)
	}
	autostash, ok := envelope.Payload["autostash"].(map[string]interface{})
	if !ok {
		t.Fatalf("the payload carries no autostash member: %v", envelope.Payload["autostash"])
	}
	if state, _ := autostash["state"].(string); state != "none" {
		t.Errorf("autostash state = %v, want \"none\" where no autostash was in flight", autostash["state"])
	}
	if autostash["stash"] != nil {
		t.Errorf("autostash names a stash commit where there was none: %v", autostash["stash"])
	}
}
