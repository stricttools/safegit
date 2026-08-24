package test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// `safegit pull` is fetch plus the pipeline-authored merge.
//
// It was already a declared command composed of the two steps, but its second
// step was a plain `git merge` -- so a pull that could not fast-forward ended
// in a commit GIT authored: no safegit trailers, no commit-msg handling,
// nothing `safegit undo` could reverse, and git's AUTO_MERGE left behind. The
// merge step is now the same one `safegit merge` runs, so the commit a pull
// makes is safegit's.
//
// --merge-strategy stays required and has no default, and its three values map
// onto that merge directly: ff fast-forwards where it can and makes a merge
// commit where it cannot, ff-only refuses anything but a fast-forward, and
// no-ff always makes a merge commit.

var pullSession = []string{"CLAUDE_CODE_SESSION_ID=pull-restructure-test"}

// newDivergedFromRemoteRepo builds a repository whose origin carries a commit
// the local branch does not, and whose local branch carries one origin does
// not -- so a pull cannot fast-forward and has to make a merge commit.
func newDivergedFromRemoteRepo(t *testing.T) string {
	t.Helper()
	dir, _ := newRepoWithRemote(t)

	testutil.WriteFile(t, dir, "base.txt", "base\n")
	safegitCommitEnv(t, dir, pullSession, "base", "base.txt")
	testutil.Git(t, dir, "push", "origin", "main")

	// A commit that goes to origin and is then rolled off the local branch.
	testutil.WriteFile(t, dir, "remote.txt", "remote\n")
	safegitCommitEnv(t, dir, pullSession, "the remote side", "remote.txt")
	testutil.Git(t, dir, "push", "origin", "main")
	testutil.Git(t, dir, "reset", "--hard", "HEAD~1")

	// And a local commit origin has never seen, so the two have diverged.
	testutil.WriteFile(t, dir, "local.txt", "local\n")
	safegitCommitEnv(t, dir, pullSession, "the local side", "local.txt")
	return dir
}

// TestPullMergeIsPipelineAuthored is the headline: a pull that cannot
// fast-forward produces ONE safegit commit, with both parents, safegit's
// trailers, exactly one oplog entry under the command's own op name, and
// nothing of git's operation state left behind.
func TestPullMergeIsPipelineAuthored(t *testing.T) {
	dir := newDivergedFromRemoteRepo(t)
	before := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, pullSession,
		"pull", "--merge-strategy", "ff", "origin", "main")
	if code != exitcode.OK {
		t.Fatalf("a diverged pull failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	head := testutil.Rev(t, dir, "HEAD")
	parents := testutil.Parents(t, dir, head)
	if len(parents) != 2 || parents[0] != before {
		t.Fatalf("the pull's commit has parents %v, want a merge commit whose first parent is %s", parents, before)
	}

	msg := commitMessageOf(t, dir, "HEAD")
	if !strings.Contains(msg, "Claude-Code-Session-Id: pull-restructure-test") {
		t.Errorf("the pull's merge commit carries no session trailer, so git authored it:\n%s", msg)
	}

	// Both sides' files are in the tree.
	paths := testutil.TreePaths(t, dir, head)
	for _, want := range []string{"local.txt", "remote.txt"} {
		if !testutil.Contains(paths, want) {
			t.Errorf("the pull dropped %s (tree: %v)", want, paths)
		}
	}

	// One entry, under the COMMAND's own op name, carrying the branch positions.
	entries := oplogEntries(t, dir, "pull")
	if len(entries) != 1 {
		t.Fatalf("expected exactly one pull oplog entry, got %d: %v", len(entries), entries)
	}
	extra := oplogExtra(entries[0])
	if got, _ := oplogExtraString(extra, "ref"); got != "refs/heads/main" {
		t.Errorf("the pull entry records ref %q, want refs/heads/main; extra=%v", got, extra)
	}
	if got, _ := oplogExtraString(extra, "parent"); got != before {
		t.Errorf("the pull entry records old tip %q, want %q; extra=%v", got, before, extra)
	}
	if got, _ := oplogExtraString(extra, "sha", "to", "result"); got != head {
		t.Errorf("the pull entry records new tip %q, want %q; extra=%v", got, head, extra)
	}
	if n := len(oplogEntries(t, dir, "merge")); n != 0 {
		t.Errorf("the pull recorded %d merge entries; it records under its OWN name", n)
	}

	assertNoSequencerResidue(t, dir, "pipeline-authored pull")

	// And it is reversible, which is what authorship buys.
	if _, stderr, code := runSafegitEnv(t, dir, pullSession, "undo"); code != 0 {
		t.Fatalf("undo of a pipeline-authored pull failed (code %d): %s", code, stderr)
	}
	if back := testutil.Rev(t, dir, "HEAD"); back != before {
		t.Errorf("undo left HEAD at %s, want the pre-pull tip %s", back, before)
	}
}

// TestPullFastForwardMovesTheRefAndSyncs: where the branch is strictly behind,
// the pull fast-forwards -- safegit decides that itself, moves the ref under
// compare-and-swap, and puts the index and the working tree in step with it.
func TestPullFastForwardMovesTheRefAndSyncs(t *testing.T) {
	dir := newBehindRemoteRepo(t)
	before := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, pullSession,
		"pull", "--merge-strategy", "ff", "origin", "main")
	if code != exitcode.OK {
		t.Fatalf("the fast-forwarding pull failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	head := testutil.Rev(t, dir, "HEAD")
	if head == before {
		t.Fatalf("the pull did not move the branch off %s", before)
	}
	if parents := testutil.Parents(t, dir, head); len(parents) != 1 {
		t.Errorf("a fast-forwarding pull must create no merge commit; HEAD has %d parent(s): %v", len(parents), parents)
	}
	if status := strings.TrimSpace(testutil.Git(t, dir, "status", "--porcelain")); status != "" {
		t.Errorf("the fast-forward left the index or the working tree out of step:\n%s", status)
	}

	entries := oplogEntries(t, dir, "pull")
	if len(entries) != 1 {
		t.Fatalf("expected exactly one pull oplog entry, got %d: %v", len(entries), entries)
	}
	extra := oplogExtra(entries[0])
	if outcome, _ := extra["outcome"].(string); !strings.Contains(outcome, "fast-forward") {
		t.Errorf("the entry records outcome %q, which does not say it was a fast-forward; extra=%v", outcome, extra)
	}
	if got, _ := oplogExtraString(extra, "remote"); got != "origin" {
		t.Errorf("the pull entry does not record the remote it fetched from; extra=%v", extra)
	}

	// The commits the branch moved onto are git's, so undo refuses rather than
	// walking the ref back over them.
	if _, undoErr, undoCode := runSafegitEnv(t, dir, pullSession, "undo"); undoCode == 0 {
		t.Fatalf("undo reversed a fast-forwarding pull; those commits are not safegit's:\n%s", undoErr)
	}
	if now := testutil.Rev(t, dir, "HEAD"); now != head {
		t.Errorf("the refused undo moved HEAD to %s (was %s)", now, head)
	}
}

// TestPullFfOnlyRefusesADivergedBranch: `--merge-strategy ff-only` declares
// that the operator wants no merge commit, so a diverged branch is refused and
// nothing moves.
func TestPullFfOnlyRefusesADivergedBranch(t *testing.T) {
	dir := newDivergedFromRemoteRepo(t)
	before := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, pullSession,
		"pull", "--merge-strategy", "ff-only", "origin", "main")
	if code == 0 {
		t.Fatalf("an ff-only pull of a diverged branch succeeded\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "fast-forward") {
		t.Errorf("the refusal does not say the pull is not a fast-forward:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != before {
		t.Errorf("the refused pull moved HEAD to %s (was %s)", head, before)
	}
	assertNoSequencerResidue(t, dir, "refused ff-only pull")
}

// TestPullNoFfElectsAMergeCommit: `--merge-strategy no-ff` makes a merge commit
// even where a fast-forward was possible, and that commit is safegit's.
func TestPullNoFfElectsAMergeCommit(t *testing.T) {
	dir := newBehindRemoteRepo(t)
	before := testutil.Rev(t, dir, "HEAD")

	if _, stderr, code := runSafegitEnv(t, dir, pullSession,
		"pull", "--merge-strategy", "no-ff", "origin", "main"); code != 0 {
		t.Fatalf("a no-ff pull failed (code %d): %s", code, stderr)
	}
	parents := testutil.Parents(t, dir, testutil.Rev(t, dir, "HEAD"))
	if len(parents) != 2 || parents[0] != before {
		t.Fatalf("--merge-strategy no-ff produced parents %v, want a merge commit onto %s", parents, before)
	}
	if msg := commitMessageOf(t, dir, "HEAD"); !strings.Contains(msg, "Claude-Code-Session-Id: pull-restructure-test") {
		t.Errorf("the no-ff pull's commit carries no session trailer, so git authored it:\n%s", msg)
	}
}

// TestPullCarriesAPayload: a machine-mode pull says what it fetched and what
// the merge did, rather than emitting a null payload beside a commit it made.
func TestPullCarriesAPayload(t *testing.T) {
	dir := newDivergedFromRemoteRepo(t)

	stdout, stderr, code := runSafegitEnv(t, dir, pullSession,
		"--json", "pull", "--merge-strategy", "ff", "origin", "main")
	if code != exitcode.OK {
		t.Fatalf("safegit --json pull failed (code %d): %s", code, stderr)
	}
	var payload struct {
		Operation string  `json:"operation"`
		Remote    string  `json:"remote"`
		Branch    *string `json:"branch"`
		FetchHead *string `json:"fetch_head"`
		Merge     struct {
			Operation string   `json:"operation"`
			Outcome   string   `json:"outcome"`
			Ref       string   `json:"ref"`
			SHA       *string  `json:"sha"`
			Parents   []string `json:"parents"`
		} `json:"merge"`
	}
	if err := json.Unmarshal(decodeEnvelope(t, stdout).Payload, &payload); err != nil {
		t.Fatalf("the pull payload does not parse: %v\nstdout=%s", err, stdout)
	}
	if payload.Operation != "pull" {
		t.Errorf("payload operation = %q, want pull", payload.Operation)
	}
	if payload.Remote != "origin" {
		t.Errorf("payload remote = %q, want origin", payload.Remote)
	}
	if payload.Branch == nil || *payload.Branch != "main" {
		t.Errorf("payload branch = %v, want main", payload.Branch)
	}
	if payload.FetchHead == nil || *payload.FetchHead == "" {
		t.Error("payload fetch_head is null; the pull fetched something")
	}
	if payload.Merge.Outcome != "merge-commit" {
		t.Errorf("payload merge.outcome = %q, want merge-commit", payload.Merge.Outcome)
	}
	if payload.Merge.SHA == nil || *payload.Merge.SHA != testutil.Rev(t, dir, "HEAD") {
		t.Errorf("payload merge.sha = %v, want the commit that was created", payload.Merge.SHA)
	}
	if len(payload.Merge.Parents) != 2 {
		t.Errorf("payload merge.parents = %v, want the merge commit's two", payload.Merge.Parents)
	}
}

// TestPullRefusesRebase: rebasing is its own command and its own door, so
// `--rebase` is refused with the two steps named rather than accepted as a
// third strategy.
func TestPullRefusesRebase(t *testing.T) {
	dir := newDivergedFromRemoteRepo(t)
	before := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, pullSession,
		"pull", "--rebase", "--merge-strategy", "ff", "origin", "main")
	if code == 0 {
		t.Fatalf("pull --rebase was accepted\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "safegit rebase") {
		t.Errorf("the refusal does not name the rebase command:\n%s", stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != before {
		t.Errorf("the refused pull moved HEAD to %s (was %s)", head, before)
	}
}

// TestPullUpToDateReportsAndRecordsIt: a pull with nothing to bring in moves
// nothing, says so, and records the outcome.
func TestPullUpToDateReportsAndRecordsIt(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	testutil.WriteFile(t, dir, "base.txt", "base\n")
	safegitCommitEnv(t, dir, pullSession, "base", "base.txt")
	testutil.Git(t, dir, "push", "origin", "main")
	before := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, pullSession,
		"pull", "--merge-strategy", "ff", "origin", "main")
	if code != exitcode.OK {
		t.Fatalf("an up-to-date pull failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != before {
		t.Errorf("the up-to-date pull moved HEAD to %s (was %s)", head, before)
	}
	entries := oplogEntries(t, dir, "pull")
	if len(entries) != 1 {
		t.Fatalf("expected exactly one pull oplog entry, got %d: %v", len(entries), entries)
	}
	if outcome, _ := oplogExtra(entries[0])["outcome"].(string); outcome != "up-to-date" {
		t.Errorf("the entry records outcome %q, want up-to-date", outcome)
	}
}

// TestPullDryRunRecordsTheFetchAndSaysWhatItCannotCompute: a preview performs
// nothing, so it has not fetched -- and what the merge step would then do
// depends entirely on commits it did not bring in. The fetch is recorded, and
// the rest is stated rather than invented.
func TestPullDryRunRecordsTheFetchAndSaysWhatItCannotCompute(t *testing.T) {
	dir := newDivergedFromRemoteRepo(t)
	before := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, pullSession,
		"--dry-run", "pull", "--merge-strategy", "ff", "origin", "main")
	if code != 0 {
		t.Fatalf("pull --dry-run failed (code %d): %s", code, stderr)
	}
	log := wouldDoLog(stdout)
	if !strings.Contains(log, "fetch origin main") {
		t.Errorf("the would-do log does not record the fetch:\n%s", log)
	}
	if !strings.Contains(stdout, "fetch") || !strings.Contains(stdout, "cannot") {
		t.Errorf("the preview does not say what it cannot compute without fetching:\n%s", stdout)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != before {
		t.Errorf("the preview moved HEAD to %s (was %s)", head, before)
	}
	if n := len(oplogEntries(t, dir, "pull")); n != 0 {
		t.Errorf("a preview appended %d pull oplog entry(ies)", n)
	}
}
