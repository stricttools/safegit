package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// These are RECORDED-FACT probes, kept as tests rather than as prose: they pin
// what git does when an operation is finished through RunPassthroughWithEnv with
// GIT_INDEX_FILE pointing at a copy of the shared index.
//
// NOTHING in safegit relies on it any more. The conclusion DELEGATION that did
// -- safegit checking a queued cherry-pick or revert and handing the rest of the
// queue to git's own --continue with that copy as its index -- is deleted: a
// queue is a state only raw git can create, and safegit's conclusions refuse it
// rather than author part of it. What the probes are now is the record of the
// git behavior that decision was made against, which is worth keeping precisely
// because the decision can be revisited: a future git that stopped honoring a
// substituted index would break the assumption the deleted feature rested on,
// and these say so rather than leaving it to memory.

// indexCopy copies the repository's shared index into a scratch file and
// returns its path.
func indexCopy(t *testing.T, repoDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoDir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "index-copy")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// unmergedCount reports how many unmerged entries an index holds.
func unmergedCount(t *testing.T, indexPath string) int {
	t.Helper()
	entries, err := UnmergedStages(context.Background(), indexPath)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// TestRevertContinueHonorsASubstitutedIndexFile is Phase 6.4's premise, probed
// rather than assumed: a queued revert stopped on a conflict is finished by
// `git revert --continue` reading safegit's own index copy.
//
// What git does, all four parts asserted below:
//
//   - it commits the resolution staged in the COPY (the shared index's
//     conflicted content is not what gets committed);
//   - it finishes the rest of the queue;
//   - it removes the whole sequencer state (REVERT_HEAD, AUTO_MERGE, MERGE_MSG,
//     .git/sequencer);
//   - it leaves the SHARED index untouched and therefore stale -- still
//     carrying the unmerged entries -- so a conclusion has to reconcile it
//     afterwards.
func TestRevertContinueHonorsASubstitutedIndexFile(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	testutil.WriteFile(t, dir, "f.txt", "l1\nl2\nl3\n")
	testutil.Git(t, dir, "add", "f.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "base")
	testutil.WriteFile(t, dir, "f.txt", "X\nl2\nl3\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "c1")
	first := testutil.Rev(t, dir, "HEAD")
	testutil.WriteFile(t, dir, "f.txt", "X\nl2\nY\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "c2")
	second := testutil.Rev(t, dir, "HEAD")
	testutil.WriteFile(t, dir, "f.txt", "Z\nl2\nY\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "c3")

	// Reverting c2 applies cleanly; reverting c1 then conflicts, so the queue
	// stops mid-sequence.
	if _, code := testutil.GitTry(t, dir, "revert", "--no-edit", second, first); code == 0 {
		t.Fatal("the fixture revert sequence was expected to conflict")
	}
	if !testutil.FileExists(filepath.Join(dir, ".git", "sequencer")) {
		t.Fatal("the fixture did not leave a sequencer queue")
	}

	copyPath := indexCopy(t, dir)
	testutil.WriteFile(t, dir, "f.txt", "RESOLVED\nl2\nY\n")
	if _, _, err := RunWithEnv(ctx, []string{"GIT_INDEX_FILE=" + copyPath}, "update-index", "--add", "f.txt"); err != nil {
		t.Fatal(err)
	}
	if n := unmergedCount(t, ""); n != 3 {
		t.Fatalf("the shared index holds %d unmerged entries before the delegation, want 3", n)
	}

	// Driven with RAW git rather than through internal/git, and that is the
	// probe being honest about what it measures: the fact recorded here is
	// GIT's, and safegit's execution boundary refuses a `revert --continue`
	// argv outright -- concluding a revert is safegit's own job, so no safegit
	// call site may build one.
	if out, code := testutil.GitTryEnv(t, dir,
		[]string{"GIT_INDEX_FILE=" + copyPath, "GIT_EDITOR=true"},
		"revert", "--continue", "--no-edit"); code != 0 {
		t.Fatalf("revert --continue with a substituted index file (exit %d):\n%s", code, out)
	}

	// The resolution staged in the COPY is what got committed.
	if got := testutil.MustShow(t, dir, "HEAD", "f.txt"); got != "RESOLVED\nl2\nY\n" {
		t.Errorf("HEAD:f.txt = %q, want the content staged in the index copy", got)
	}
	// The whole queue ran: both reverts are on the branch.
	subjects := testutil.GitOut(t, dir, "log", "--format=%s", "-3")
	for _, want := range []string{`Revert "c1"`, `Revert "c2"`} {
		if !strings.Contains(subjects, want) {
			t.Errorf("the queue did not complete; log holds:\n%s", subjects)
		}
	}
	// Git cleaned its own state.
	for _, name := range []string{"REVERT_HEAD", "AUTO_MERGE", "MERGE_MSG", "sequencer"} {
		if testutil.FileExists(filepath.Join(dir, ".git", name)) {
			t.Errorf(".git/%s survived the delegated conclusion", name)
		}
	}
	// And the shared index is stale, which is what makes the reconcile step
	// after a delegation mandatory rather than tidy.
	if n := unmergedCount(t, ""); n != 3 {
		t.Errorf("the shared index holds %d unmerged entries after the delegation, want the original 3 (a change here means git DID write it)", n)
	}
}

// TestRebaseContinueHonorsASubstitutedIndexFile records the feasibility fact
// for the rebase conclusion this campaign deliberately does not build: whether
// `git rebase --continue` can be pointed at an index copy the same way the
// sequencer verbs can.
//
// Nothing in the campaign depends on the answer. It is probed here so that
// whoever picks up the rebase extension starts from an assertion instead of an
// assumption.
func TestRebaseContinueHonorsASubstitutedIndexFile(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	testutil.WriteFile(t, dir, "f.txt", "l1\nl2\nl3\n")
	testutil.Git(t, dir, "add", "f.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "base")

	testutil.Git(t, dir, "switch", "-q", "-c", "topic")
	testutil.WriteFile(t, dir, "f.txt", "T\nl2\nl3\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "t1")
	testutil.WriteFile(t, dir, "f.txt", "T\nl2\nT2\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "t2")

	testutil.Git(t, dir, "switch", "-q", "main")
	testutil.WriteFile(t, dir, "f.txt", "M\nl2\nl3\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "m1")
	testutil.Git(t, dir, "switch", "-q", "topic")

	if _, code := testutil.GitTry(t, dir, "rebase", "main"); code == 0 {
		t.Fatal("the fixture rebase was expected to conflict")
	}

	copyPath := indexCopy(t, dir)
	testutil.WriteFile(t, dir, "f.txt", "RESOLVED\nl2\nl3\n")
	if _, _, err := RunWithEnv(ctx, []string{"GIT_INDEX_FILE=" + copyPath}, "update-index", "--add", "f.txt"); err != nil {
		t.Fatal(err)
	}
	sharedBefore := unmergedCount(t, "")

	// Raw git, for the same reason as the revert probe above: the fact is
	// git's. safegit's own rebase does pass an authoring argv to git -- it is
	// the ONE declared door -- but it does so through the effects-handle path,
	// which is not this one.
	if out, code := testutil.GitTryEnv(t, dir,
		[]string{"GIT_INDEX_FILE=" + copyPath, "GIT_EDITOR=true"},
		"rebase", "--continue"); code != 0 {
		t.Fatalf("rebase --continue with a substituted index file (exit %d):\n%s", code, out)
	}

	// ANSWER: yes. git rebase --continue honors the substitution exactly as the
	// sequencer verbs do -- it committed the resolution staged in the copy, ran
	// the remaining step on top of it, finished the rebase, and never wrote the
	// shared index.
	if got := testutil.MustShow(t, dir, "HEAD", "f.txt"); got != "RESOLVED\nl2\nT2\n" {
		t.Errorf("HEAD:f.txt = %q, want the copy's resolution with the remaining commit applied on top", got)
	}
	subjects := testutil.GitOut(t, dir, "log", "--format=%s", "-3")
	for _, want := range []string{"t1", "t2"} {
		if !strings.Contains(subjects, want) {
			t.Errorf("the rebase did not replay every commit; log holds:\n%s", subjects)
		}
	}
	if n := unmergedCount(t, ""); n != sharedBefore {
		t.Errorf("the shared index holds %d unmerged entries, want the original %d (a change means git DID write it)", n, sharedBefore)
	}
	for _, name := range []string{"rebase-merge", "rebase-apply", "MERGE_MSG"} {
		if testutil.FileExists(filepath.Join(dir, ".git", name)) {
			t.Errorf(".git/%s survived the rebase", name)
		}
	}
	// AUTO_MERGE, however, SURVIVES a concluded rebase. The control below shows
	// an ordinary `git rebase --continue` leaves it behind too, so this is
	// git's own behavior and not an effect of the substitution -- which makes
	// it a live case for anything that treats a leftover AUTO_MERGE as
	// evidence of an interrupted operation.
	if !testutil.FileExists(filepath.Join(dir, ".git", "AUTO_MERGE")) {
		t.Error("AUTO_MERGE no longer survives a concluded rebase; the leftover finding needs re-recording")
	}
}

// TestRebaseContinueControl is the attribution control for the probe above: the
// same fixture concluded the ORDINARY way, staging into the shared index.
// Whatever both runs leave behind is git's own behavior, not something the
// index substitution caused.
func TestRebaseContinueControl(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	testutil.WriteFile(t, dir, "f.txt", "l1\nl2\nl3\n")
	testutil.Git(t, dir, "add", "f.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "base")
	testutil.Git(t, dir, "switch", "-q", "-c", "topic")
	testutil.WriteFile(t, dir, "f.txt", "T\nl2\nl3\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "t1")
	testutil.WriteFile(t, dir, "f.txt", "T\nl2\nT2\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "t2")
	testutil.Git(t, dir, "switch", "-q", "main")
	testutil.WriteFile(t, dir, "f.txt", "M\nl2\nl3\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "m1")
	testutil.Git(t, dir, "switch", "-q", "topic")
	if _, code := testutil.GitTry(t, dir, "rebase", "main"); code == 0 {
		t.Fatal("the fixture rebase was expected to conflict")
	}

	testutil.WriteFile(t, dir, "f.txt", "RESOLVED\nl2\nl3\n")
	testutil.Git(t, dir, "add", "f.txt")
	out, code := testutil.GitTryEnv(t, dir, []string{"GIT_EDITOR=true"}, "rebase", "--continue")
	if code != 0 {
		t.Fatalf("the control rebase --continue failed: %s", out)
	}
	// The ordinary conclusion reconciles the shared index (nothing unmerged is
	// left), which is the one thing the delegated form does not do for itself.
	if n := unmergedCount(t, ""); n != 0 {
		t.Errorf("the control left %d unmerged entries in the shared index", n)
	}
	// And it leaves AUTO_MERGE behind exactly as the delegated form does.
	if !testutil.FileExists(filepath.Join(dir, ".git", "AUTO_MERGE")) {
		t.Error("an ordinary rebase --continue no longer leaves AUTO_MERGE behind; the attribution of that leftover needs re-recording")
	}
	for _, name := range []string{"rebase-merge", "rebase-apply", "MERGE_MSG"} {
		if testutil.FileExists(filepath.Join(dir, ".git", name)) {
			t.Errorf("the control left .git/%s behind", name)
		}
	}
}
