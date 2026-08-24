package test

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// The other side of the crash-window recognition: a conclusion that recognizes
// a commit it did NOT make would throw work away.
//
// The recognition reads safegit's own op log: the last entry for the branch
// names an op that concludes this kind of operation and records the commit HEAD
// stands at. After ANY concluded cherry-pick that is still true -- the branch
// tip is that very commit -- so a SECOND pick, conflicted and waiting to be
// concluded, would be read as "already concluded", have its state removed and
// never be committed. The evidence has to say the commit concluded THIS
// operation, not merely that safegit made it.

// TestASecondPickIsNotMistakenForTheFirst: pick one commit cleanly, then
// conclude a conflicted pick of another. The second must produce its own commit.
func TestASecondPickIsNotMistakenForTheFirst(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "a.txt", "base a\n")
	testutil.WriteFile(t, dir, "b.txt", "l1\nbase\nl3\n")
	safegitCommitEnv(t, dir, conclusionSession, "base", "a.txt", "b.txt")

	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "-q", "feature")
	testutil.WriteFile(t, dir, "a.txt", "feature a\n")
	first := safegitCommitEnv(t, dir, conclusionSession, "feature edits a", "a.txt")
	testutil.WriteFile(t, dir, "b.txt", "l1\nfeature\nl3\n")
	second := safegitCommitEnv(t, dir, conclusionSession, "feature edits b", "b.txt")

	testutil.Git(t, dir, "switch", "-q", "main")
	testutil.WriteFile(t, dir, "b.txt", "l1\nmain\nl3\n")
	safegitCommitEnv(t, dir, conclusionSession, "main edits b", "b.txt")

	// The first pick is clean, so safegit concludes it on the spot and the op
	// log's last entry for this branch names it.
	if stdout, stderr, code := runSafegitEnv(t, dir, conclusionSession, "cherry-pick", first); code != 0 {
		t.Fatalf("the clean pick failed (code %d): %s\n%s", code, stderr, stdout)
	}
	afterFirst := testutil.Rev(t, dir, "HEAD")

	// The second conflicts and parks; nothing about the branch tip changes.
	stdout, stderr, code := runSafegitEnv(t, dir, conclusionSession, "cherry-pick", second)
	if code == 0 {
		t.Fatalf("the second pick was expected to conflict\nstdout=%s stderr=%s", stdout, stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != afterFirst {
		t.Fatalf("the parked pick moved HEAD to %s (was %s)", head, afterFirst)
	}

	stdout, stderr, code = runSafegitEnv(t, dir, conclusionSession,
		"cherry-pick-continue", "--resolve", "b.txt=theirs")
	if code != 0 {
		t.Fatalf("the conclusion of the second pick failed (code %d): %s\n%s", code, stderr, stdout)
	}

	head := testutil.Rev(t, dir, "HEAD")
	if head == afterFirst {
		t.Fatalf("HEAD did not move: the conclusion read the FIRST pick's commit as this pick's own and committed nothing\n%s%s", stdout, stderr)
	}
	if got := testutil.MustShow(t, dir, "HEAD", "b.txt"); got != "l1\nfeature\nl3\n" {
		t.Errorf("b.txt in the concluded commit = %q, want the picked side's content", got)
	}
	if subject := strings.TrimSpace(testutil.Git(t, dir, "log", "-1", "--format=%s", head)); !strings.Contains(subject, "feature edits b") {
		t.Errorf("the concluded commit's subject is %q, want the second pick's own message", subject)
	}
	assertNoSequencerResidue(t, dir, "the conclusion of a second pick")
}
