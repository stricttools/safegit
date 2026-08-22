package test

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
	"github.com/smm-h/safegit/internal/trailer"
)

// Retraction is the only correction a move record has. A record already written
// is never edited, because editing the commit that carries it rewrites history,
// so the way to say "that record was wrong" is a new commit that says so.
//
// `--moved-retract <id>` is that statement with the repository checked behind
// it: the id has to name a record that exists and is not already retracted.
// The unchecked spelling -- `--trailer 'Moved-Retract: <id>'` -- stays legal and
// deliberately gets none of this, because a trailer is a trailer.

// seedRetractable commits a move and returns the repository and the id of the
// record it declared.
func seedRetractable(t *testing.T) (string, string) {
	t.Helper()
	dir := mvSeed(t)
	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "move a", "a.txt -> moved.txt"); code != 0 {
		t.Fatalf("mv failed (code %d): %s", code, stderr)
	}
	records := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))
	if len(records) != 1 {
		t.Fatalf("the fixture declared no record: %v", records)
	}
	return dir, records[0][0]
}

func TestMovedRetractWritesTheTrailerAndTheProjectionFoldsIt(t *testing.T) {
	dir, id := seedRetractable(t)

	testutil.WriteFile(t, dir, "b.txt", "changed\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "that record was wrong",
		"--moved-retract", id, "--", "b.txt"); code != 0 {
		t.Fatalf("retraction commit failed (code %d): %s", code, stderr)
	}

	msg := commitMessageOf(t, dir, "HEAD")
	if !strings.Contains(msg, "Moved-Retract: "+id+"\n") {
		t.Fatalf("the retraction trailer is not in:\n%s", msg)
	}

	// The projection folds the retraction: the record no longer applies, so the
	// old path does not follow the move any more.
	p := trailer.Forward("a.txt", projectionChain(t, dir))
	if len(p.Hops) != 0 {
		t.Errorf("a retracted record was still applied: %v", p.Hops)
	}
	if p.Path != "a.txt" {
		t.Errorf("a.txt projects to %q across a retracted move, want a.txt", p.Path)
	}
	if p.Present {
		t.Error("the projection reports a.txt present, but the move really did happen")
	}
}

func TestMovedRetractRefusesAnIdNothingDeclared(t *testing.T) {
	dir, _ := seedRetractable(t)
	const absent = "01BX5ZZKBKACTAV9WEVGEMMVRZ"

	testutil.WriteFile(t, dir, "b.txt", "changed\n")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "retract nothing",
		"--moved-retract", absent, "--", "b.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d (MoveNotBorneOut): %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	if !strings.Contains(stderr, absent) {
		t.Errorf("the refusal does not name the id: %s", stderr)
	}
	if !strings.Contains(stderr, "no move record") {
		t.Errorf("the refusal does not say what is wrong: %s", stderr)
	}
	assertNoCommitHappened(t, dir, "move a")
}

func TestMovedRetractRefusesARecordAlreadyRetracted(t *testing.T) {
	dir, id := seedRetractable(t)

	testutil.WriteFile(t, dir, "b.txt", "changed\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "retract it",
		"--moved-retract", id, "--", "b.txt"); code != 0 {
		t.Fatalf("first retraction failed (code %d): %s", code, stderr)
	}

	testutil.WriteFile(t, dir, "b.txt", "changed again\n")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "retract it twice",
		"--moved-retract", id, "--", "b.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	if !strings.Contains(stderr, "already retracted") {
		t.Errorf("the refusal does not say the record is already retracted: %s", stderr)
	}
	assertNoCommitHappened(t, dir, "retract it")
}

// A refusal names EVERY bad id, not just the first. A caller who typed three
// ids and got two wrong should be told both verdicts in one go rather than
// discover them one command at a time -- and the two verdicts are different
// verdicts, so naming only one would also hide which kind of wrong the other
// id is.
func TestMovedRetractNamesEveryBadIdInOneRefusal(t *testing.T) {
	dir, retractable := seedRetractable(t)
	const absent = "01BX5ZZKBKACTAV9WEVGEMMVRZ"

	// A second record, retracted straight away, so one of the ids in the
	// refusal below is wrong in the "already retracted" way rather than the
	// "names nothing" way.
	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "move b", "b.txt -> moved-b.txt"); code != 0 {
		t.Fatalf("second mv failed (code %d): %s", code, stderr)
	}
	alreadyRetracted := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))[0][0]
	testutil.WriteFile(t, dir, "c.txt", "c\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "retract the second",
		"--moved-retract", alreadyRetracted, "--", "c.txt"); code != 0 {
		t.Fatalf("first retraction failed (code %d): %s", code, stderr)
	}

	// One good id and two bad ones: the good one buys nothing, and both bad
	// ones are named.
	testutil.WriteFile(t, dir, "c.txt", "c again\n")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "retract three things",
		"--moved-retract", retractable,
		"--moved-retract", absent,
		"--moved-retract", alreadyRetracted,
		"--", "c.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d (MoveNotBorneOut): %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	for _, id := range []string{absent, alreadyRetracted} {
		if !strings.Contains(stderr, id) {
			t.Errorf("the refusal does not name %s: %s", id, stderr)
		}
	}
	if !strings.Contains(stderr, "no move record") {
		t.Errorf("the refusal does not carry the absent-id verdict: %s", stderr)
	}
	if !strings.Contains(stderr, "already retracted") {
		t.Errorf("the refusal does not carry the already-retracted verdict: %s", stderr)
	}
	if !strings.Contains(stderr, "2 of 3") {
		t.Errorf("the refusal does not count two bad ids out of three: %s", stderr)
	}
	assertNoCommitHappened(t, dir, "retract the second")
}

// A record the commit BEING AMENDED declared itself is not retractable in that
// same amend. The declarations and the retractions of an amend are judged
// against the FIRST PARENT of the tip being replaced -- that is the step the
// amended commit describes -- and the record is not in that history yet. An
// amend that wants the record gone drops it by not declaring it, which is what
// an amend is for.
func TestAmendCannotRetractARecordTheAmendedCommitDeclared(t *testing.T) {
	dir, id := seedRetractable(t)
	tipBefore := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegit(t, dir, "commit", "--amend", "--moved-retract", id)
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d (MoveNotBorneOut): %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	if !strings.Contains(stderr, id) {
		t.Errorf("the refusal does not name the id: %s", stderr)
	}
	if !strings.Contains(stderr, "no move record") {
		t.Errorf("the refusal does not say the id names nothing in the base: %s", stderr)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != tipBefore {
		t.Errorf("the refused amend replaced the tip (%s -> %s)", tipBefore, got)
	}
	if strings.Contains(commitMessageOf(t, dir, "HEAD"), "Moved-Retract:") {
		t.Error("the refused amend wrote the retraction anyway")
	}
}

func TestAmendAcceptsMovedRetract(t *testing.T) {
	dir, id := seedRetractable(t)

	testutil.WriteFile(t, dir, "b.txt", "changed\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "a commit that forgot the retraction", "--", "b.txt"); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	// An amend that names no file and only retracts: the content is already
	// right, and only the message is missing the statement.
	if _, stderr, code := runSafegit(t, dir, "commit", "--amend", "--moved-retract", id); code != 0 {
		t.Fatalf("amend with only a retraction failed (code %d): %s", code, stderr)
	}
	msg := commitMessageOf(t, dir, "HEAD")
	if !strings.Contains(msg, "Moved-Retract: "+id) {
		t.Fatalf("the amend did not write the retraction:\n%s", msg)
	}
	if !strings.HasPrefix(msg, "a commit that forgot the retraction") {
		t.Errorf("an amend with no -m must keep the message:\n%s", msg)
	}
}

// TestBareTrailerRetractionIsStillUnchecked pins the open convention: the flag
// checks, the trailer does not. A caller who means to write a retraction
// safegit would refuse can still write one, deliberately, through the flag
// whose whole contract is "put this line in the message".
func TestBareTrailerRetractionIsStillUnchecked(t *testing.T) {
	dir, _ := seedRetractable(t)
	const absent = "01BX5ZZKBKACTAV9WEVGEMMVRZ"

	testutil.WriteFile(t, dir, "b.txt", "changed\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "an unchecked retraction",
		"--trailer", "Moved-Retract: "+absent, "--", "b.txt"); code != 0 {
		t.Fatalf("the unchecked spelling was refused (code %d): %s", code, stderr)
	}
	if !strings.Contains(commitMessageOf(t, dir, "HEAD"), "Moved-Retract: "+absent) {
		t.Error("the unchecked retraction was not written")
	}
}

func TestMovedRetractRefusedOnARootCommit(t *testing.T) {
	dir := newEmptyRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "a\n")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "first",
		"--moved-retract", "01BX5ZZKBKACTAV9WEVGEMMVRZ", "--", "a.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	if !strings.Contains(stderr, "no parent") {
		t.Errorf("the refusal does not say the commit has no parent: %s", stderr)
	}
}
