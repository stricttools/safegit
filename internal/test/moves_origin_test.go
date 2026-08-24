package test

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// The ORIGIN of a move record, end to end.
//
// A record safegit MINTED from a commit's own delta says so: it carries the
// `observed` token in the slot after its id. A record a person stated carries
// no token at all, and absence is the declared origin -- so nothing already
// written has to change and no reader has to guess.
//
// What the token buys is one question a reader could not previously ask: did
// anybody VOUCH for this move, or did safegit read it off the objects? The
// answer changes nothing about the claim itself -- the trees remain the arbiter
// of every record whatever its origin -- but it changes what a person reviewing
// history is looking at, and it is what makes SUPERSEDING an observed record
// with a declared one a coherent operation rather than two claims about one
// move.

// originSession keeps these runs distinguishable in the op log.
var originSession = []string{"CLAUDE_CODE_SESSION_ID=move-origin-test"}

// TestMintedRecordsCarryTheObservedToken pins the two origins side by side in
// one commit: the declaration the caller made, and the move safegit read.
func TestMintedRecordsCarryTheObservedToken(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	testutil.WriteFile(t, dir, "c.txt", "other content nothing else holds\n")
	safegitCommitEnv(t, dir, originSession, "seed", "a.txt", "c.txt")

	moveOnDisk(t, dir, "a.txt", "b.txt")
	moveOnDisk(t, dir, "c.txt", "d.txt")
	_, stderr, code := runSafegitEnv(t, dir, originSession, "commit", "-m", "move both",
		"--moved", "a.txt -> b.txt", "--", "a.txt", "b.txt", "c.txt", "d.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	msg := commitMessageOf(t, dir, "HEAD")
	assertInferredPairs(t, dir, "a.txt -> b.txt", "c.txt -> d.txt")
	assertOrigins(t, msg, "declared", "observed")
}

// TestTheObservedTokenSurvivesAScrub is the pin without which every history
// rewrite would quietly demote safegit's own records to claims a person made:
// a scrub re-encodes each record it rewrites, and the origin has to come out
// the other side.
func TestTheObservedTokenSurvivesAScrub(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "secret.txt", "content nothing else holds\n")
	safegitCommitEnv(t, dir, originSession, "seed", "secret.txt")

	moveOnDisk(t, dir, "secret.txt", "config/secret.txt")
	if _, stderr, code := runSafegitEnv(t, dir, originSession, "commit", "-m", "move the file",
		"--", "secret.txt", "config/secret.txt"); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	assertOrigins(t, commitMessageOf(t, dir, "HEAD"), "observed")

	if _, stderr, code := runSafegitEnv(t, dir, originSession, "scrub", "match", "--approve-consequential",
		"--pattern", "secret", "--replace", "public", "--entire-history",
		"--reason", "the name leaked"); code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}

	sha := strings.TrimSpace(testutil.Git(t, dir, "log", "--format=%H", "--grep", "move the file"))
	if sha == "" {
		t.Fatal("the move commit is not in the rewritten history")
	}
	msg := commitMessageOf(t, dir, sha)
	assertOrigins(t, msg, "observed")
	if got := movedRecordsIn(t, msg); len(got) != 1 || got[0][1] != "public.txt -> config/public.txt" {
		t.Errorf("the rewritten record is %v; message:\n%s", got, msg)
	}
}

// TestAnObservedRecordIsRetractable: an origin is not a privilege. A record
// safegit minted is a claim like any other, so a later commit retracts it the
// same way it retracts a declared one -- which is the operator's answer when
// the delta witnessed something that was not a move.
func TestAnObservedRecordIsRetractable(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	safegitCommitEnv(t, dir, originSession, "seed", "a.txt")

	moveOnDisk(t, dir, "a.txt", "b.txt")
	if _, stderr, code := runSafegitEnv(t, dir, originSession, "commit", "-m", "move a",
		"--", "a.txt", "b.txt"); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	msg := commitMessageOf(t, dir, "HEAD")
	assertOrigins(t, msg, "observed")
	id := movedRecordsIn(t, msg)[0][0]

	testutil.WriteFile(t, dir, "unrelated.txt", "unrelated\n")
	if _, stderr, code := runSafegitEnv(t, dir, originSession, "commit", "-m", "retract that reading",
		"--moved-retract", id, "--", "unrelated.txt"); code != 0 {
		t.Fatalf("the retraction of an observed record was refused (code %d): %s", code, stderr)
	}
	if got := commitMessageOf(t, dir, "HEAD"); !strings.Contains(got, "Moved-Retract: "+id) {
		t.Errorf("the retraction is not on the commit:\n%s", got)
	}
}

// SUPERSEDE. A commit whose observed record the caller wants to state
// themselves -- because safegit's reading was right and they want it vouched
// for, or because they are about to restate it differently -- amends the
// commit with the declaration.
//
// The result is one commit carrying THREE lines about that move: the observed
// record (preserved, because a record is never edited), a retraction of it, and
// the declared record. That is the same shape every replacement has: a
// retraction plus a new record in one commit.
func TestDeclaringAPairAnObservedRecordAlreadyCarriesSupersedesIt(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	safegitCommitEnv(t, dir, originSession, "seed", "a.txt")

	moveOnDisk(t, dir, "a.txt", "b.txt")
	if _, stderr, code := runSafegitEnv(t, dir, originSession, "commit", "-m", "move a",
		"--", "a.txt", "b.txt"); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	observedID := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))[0][0]

	_, stderr, code := runSafegitEnv(t, dir, originSession, "commit", "--amend",
		"--moved", "a.txt -> b.txt")
	if code != 0 {
		t.Fatalf("the superseding amend was refused (code %d): %s", code, stderr)
	}

	msg := commitMessageOf(t, dir, "HEAD")
	records := movedRecordsIn(t, msg)
	if len(records) != 2 {
		t.Fatalf("the amended commit carries %d records, want the observed one and the declared one; message:\n%s",
			len(records), msg)
	}
	assertOrigins(t, msg, "observed", "declared")
	if records[0][0] != observedID {
		t.Errorf("the observed record was rewritten rather than preserved: %q, want %q", records[0][0], observedID)
	}
	if records[1][0] == observedID {
		t.Error("the declared record reused the observed record's id; a new claim is a new record")
	}
	if !strings.Contains(msg, "Moved-Retract: "+observedID) {
		t.Errorf("the observed record was not retracted; message:\n%s", msg)
	}
	for _, r := range records {
		if r[1] != "a.txt -> b.txt" {
			t.Errorf("a record states %q, want the same pair", r[1])
		}
	}
}

// The supersede is for an OBSERVED record and nothing else. Re-declaring a pair
// the commit's own DECLARED record already states is still a refusal naming the
// id: the caller already said it, saying it twice makes two ids for one
// statement, and nothing downstream could tell which is the live one.
func TestRedeclaringADeclaredPairIsStillRefused(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	safegitCommitEnv(t, dir, originSession, "seed", "a.txt")

	moveOnDisk(t, dir, "a.txt", "b.txt")
	if _, stderr, code := runSafegitEnv(t, dir, originSession, "commit", "-m", "move a",
		"--moved", "a.txt -> b.txt", "--", "a.txt", "b.txt"); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	msg := commitMessageOf(t, dir, "HEAD")
	assertOrigins(t, msg, "declared")
	declaredID := movedRecordsIn(t, msg)[0][0]

	_, stderr, code := runSafegitEnv(t, dir, originSession, "commit", "--amend",
		"--moved", "a.txt -> b.txt")
	if code != exitcode.Usage {
		t.Fatalf("re-declaring a declared pair exited %d, want %d; stderr: %s", code, exitcode.Usage, stderr)
	}
	if !strings.Contains(stderr, declaredID) {
		t.Errorf("the refusal does not name the record already carrying the pair: %s", stderr)
	}
	if got := commitMessageOf(t, dir, "HEAD"); got != msg {
		t.Errorf("the refused amend changed the commit anyway:\n%s", got)
	}
}

// The synthesized retraction is INTERNAL: it does not go through the
// --moved-retract resolver, whose base is the reachable history the commit is
// built on. That resolver cannot see a record on the tip being REPLACED -- the
// tip is not in its own base -- so routing the supersede through it would
// refuse every one of them with "names no move record".
//
// This is the pin for that: the same id, handed to --moved-retract in the same
// amend, is refused -- while the supersede above, which retracts exactly that
// record, succeeds.
func TestTheSupersedeRetractionBypassesTheRetractResolver(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	safegitCommitEnv(t, dir, originSession, "seed", "a.txt")

	moveOnDisk(t, dir, "a.txt", "b.txt")
	if _, stderr, code := runSafegitEnv(t, dir, originSession, "commit", "-m", "move a",
		"--", "a.txt", "b.txt"); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	observedID := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))[0][0]

	_, stderr, code := runSafegitEnv(t, dir, originSession, "commit", "--amend",
		"--moved-retract", observedID)
	if code == 0 {
		t.Fatalf("--moved-retract accepted an id the amended commit itself declares; stderr: %s", stderr)
	}
	if !strings.Contains(stderr, observedID) {
		t.Errorf("the refusal does not name the id: %s", stderr)
	}
}
