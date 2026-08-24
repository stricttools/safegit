package test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// Every operation safegit performs records the same three facts a commit entry
// records -- the full ref name, the tip it moved from, and the tip it moved to
// -- plus how it ended. Two things follow, and they are what these tests pin:
//
//   - an operation git REFUSED is recorded as refused, with no new tip, so the
//     audit trail never reads as though it happened;
//   - the fail-closed readers of those entries (oplog.LastRefUpdate, doctor's
//     bypass check, undo's per-ref filter) see the true position afterwards, so
//     bypass detection stops misfiring on operations safegit itself performed --
//     and keeps firing on the ones it did not.
//
// The HEAD-moving operations are the exception the third and fourth tests pin:
// a branch switch and a bisect step move HEAD without moving any branch ref, so
// recording their positions in the commit-entry spelling would RESET the
// bypass-detection baseline and mask an out-of-band commit made before them.
// They record their positions under a spelling those readers do not consume.

// doctorLine returns the doctor output line naming the given check, or "".
func doctorLine(stdout, check string) string {
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, check) {
			return line
		}
	}
	return ""
}

// TestFailedMergeRecordsARefusedOutcome: a merge git stopped on a conflict made
// no commit and moved no ref. Its oplog entry has to say so.
func TestFailedMergeRecordsARefusedOutcome(t *testing.T) {
	dir := newDivergedBranchRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegit(t, dir, "merge", "feature")
	if code == 0 {
		t.Fatalf("fixture is wrong: the conflicting merge succeeded\nstdout=%s\nstderr=%s", stdout, stderr)
	}

	entries := oplogEntries(t, dir, "merge")
	if len(entries) != 1 {
		t.Fatalf("expected exactly one merge entry after the refused merge, got %d", len(entries))
	}
	extra := oplogExtra(entries[0])
	if extra == nil {
		t.Fatal("the merge entry carries no extra object at all")
	}
	// The ref and the position it did NOT move from.
	if got, _ := oplogExtraString(extra, "ref"); got != "refs/heads/main" {
		t.Errorf("the refused merge entry records ref %q, want refs/heads/main; extra=%v", got, extra)
	}
	if got, _ := oplogExtraString(extra, "parent"); got != tip {
		t.Errorf("the refused merge entry records old tip %q, want %q; extra=%v", got, tip, extra)
	}
	// No new tip: nothing was created, and an empty one keeps the entry
	// invisible to the fail-closed readers.
	if got, key := oplogExtraString(extra, "sha", "to", "result"); got != "" {
		t.Errorf("the refused merge entry records a new tip %q (under %q); nothing was committed; extra=%v",
			got, key, extra)
	}
	// And the outcome itself, stated rather than inferred from the absence.
	if extra["outcome"] == nil {
		t.Errorf("the refused merge entry records no outcome; extra=%v", extra)
	}
}

// TestBypassDetectIsQuietAfterACleanSafegitMerge: the merge entry recorded no
// ref and no new tip, so doctor read past it to the last commit entry and
// reported the branch as diverged from its own baseline. The merge was
// safegit's; there is no bypass to report.
func TestBypassDetectIsQuietAfterACleanSafegitMerge(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "base.txt", "base\n")
	safegitCommit(t, dir, "base", "base.txt")
	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "feature")
	testutil.WriteFile(t, dir, "feature.txt", "feature\n")
	safegitCommit(t, dir, "feature side", "feature.txt")
	testutil.Git(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "main.txt", "main\n")
	safegitCommit(t, dir, "main side", "main.txt")

	if stdout, stderr, code := runSafegit(t, dir, "merge", "feature"); code != 0 {
		t.Fatalf("fixture is wrong: the merge failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	stdout, stderr, code := runSafegit(t, dir, "doctor", "--action", "diagnose")
	if code != 0 {
		t.Fatalf("doctor exited %d after a merge safegit performed\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	line := doctorLine(stdout, "bypass_detect")
	if !strings.HasPrefix(line, "[OK]") {
		t.Errorf("doctor reported a bypass after a merge safegit itself performed: %q\nfull output:\n%s", line, stdout)
	}
}

// TestBypassDetectStillFlagsAnOutOfBandCommitMadeBeforeASwitch is the other end
// of the navigation rule. A branch switch moves HEAD and no branch ref, so its
// entry must NOT become the bypass baseline: an out-of-band commit made before
// it is still an out-of-band commit afterwards.
func TestBypassDetectStillFlagsAnOutOfBandCommitMadeBeforeASwitch(t *testing.T) {
	dir := newRepo(t)
	testutil.Git(t, dir, "branch", "other")

	testutil.WriteFile(t, dir, "a.txt", "one\n")
	safegitCommit(t, dir, "safegit commit", "a.txt")

	// Out of band: raw git moves refs/heads/main and safegit records nothing.
	cmd := exec.Command("git", "commit", "--allow-empty", "-m", "raw bypass")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("raw git commit failed: %v\n%s", err, out)
	}

	// Navigate away and back. Neither move touches refs/heads/main.
	if _, stderr, code := runSafegit(t, dir, "checkout", "other"); code != 0 {
		t.Fatalf("checkout other failed (code %d): %s", code, stderr)
	}
	if _, stderr, code := runSafegit(t, dir, "checkout", "main"); code != 0 {
		t.Fatalf("checkout main failed (code %d): %s", code, stderr)
	}

	stdout, _, _ := runSafegit(t, dir, "doctor", "--action", "diagnose")
	line := doctorLine(stdout, "bypass_detect")
	if !strings.Contains(line, "diverged") {
		t.Errorf("the out-of-band commit is no longer reported after a branch switch: %q\nfull output:\n%s",
			line, stdout)
	}
}
