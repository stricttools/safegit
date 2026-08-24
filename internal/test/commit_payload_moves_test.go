package test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// The commit payload's MOVE members.
//
// A move record is the one thing a commit carries that the payload used to say
// nothing about: the records went into the message and a machine consumer had
// to re-read the commit object to find them. That was already a gap for
// DECLARED records, and inference widened it -- safegit now writes records
// nobody asked for, declines to write others, and refuses the lot past a cap,
// none of which a consumer could see without parsing stderr prose.
//
// Three members close it, and the run's own stderr notice stays exactly what it
// was: a human sentence pointing at --moved.

// TestCommitPayloadCarriesTheRecordsItMinted: both origins, with the ids the
// message really holds.
func TestCommitPayloadCarriesTheRecordsItMinted(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	testutil.WriteFile(t, dir, "c.txt", "other content nothing else holds\n")
	safegitCommit(t, dir, "seed", "a.txt", "c.txt")

	moveOnDisk(t, dir, "a.txt", "b.txt")
	moveOnDisk(t, dir, "c.txt", "d.txt")
	doc := commitPayloadOf(t, dir, "commit", "-m", "move both",
		"--moved", "a.txt -> b.txt", "--", "a.txt", "b.txt", "c.txt", "d.txt")

	if len(doc.MovedRecords) != 2 {
		t.Fatalf("moved_records = %+v, want the declared one and the observed one", doc.MovedRecords)
	}
	want := []struct{ old, new, origin string }{
		{"a.txt", "b.txt", "declared"},
		{"c.txt", "d.txt", "observed"},
	}
	for i, w := range want {
		got := doc.MovedRecords[i]
		if got.Old != w.old || got.New != w.new || got.Origin != w.origin {
			t.Errorf("moved_records[%d] = %+v, want %s -> %s (%s)", i, got, w.old, w.new, w.origin)
		}
		if got.ID == "" {
			t.Errorf("moved_records[%d] carries no id", i)
		}
	}

	// The ids are the message's own, not names invented for the payload.
	msg := commitMessageOf(t, dir, "HEAD")
	for _, r := range doc.MovedRecords {
		if !strings.Contains(msg, r.ID) {
			t.Errorf("moved_records names %s, which is not in the commit message:\n%s", r.ID, msg)
		}
	}
	if len(doc.RefusedMoves) != 0 {
		t.Errorf("refused_moves = %+v on a commit whose every candidate was recorded", doc.RefusedMoves)
	}
	if doc.MovesOverCap != 0 {
		t.Errorf("moves_over_cap = %d, want 0", doc.MovesOverCap)
	}
}

// TestCommitPayloadCarriesTheRefusedMoves: the candidates the fences declined,
// with the paths on each side and the reason -- the same facts the aggregate
// stderr notice only counts.
func TestCommitPayloadCarriesTheRefusedMoves(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "x1.txt", "identical\n")
	testutil.WriteFile(t, dir, "x2.txt", "identical\n")
	safegitCommit(t, dir, "seed", "x1.txt", "x2.txt")

	moveOnDisk(t, dir, "x1.txt", "y1.txt")
	moveOnDisk(t, dir, "x2.txt", "y2.txt")
	doc := commitPayloadOf(t, dir, "commit", "-m", "move both",
		"--", "x1.txt", "x2.txt", "y1.txt", "y2.txt")

	if len(doc.MovedRecords) != 0 {
		t.Errorf("moved_records = %+v; the ambiguous blob is recorded nowhere", doc.MovedRecords)
	}
	if len(doc.RefusedMoves) != 1 {
		t.Fatalf("refused_moves = %+v, want the one ambiguous candidate", doc.RefusedMoves)
	}
	r := doc.RefusedMoves[0]
	if strings.Join(r.Old, ",") != "x1.txt,x2.txt" || strings.Join(r.New, ",") != "y1.txt,y2.txt" {
		t.Errorf("refused_moves[0] names %v -> %v, want both paths on each side", r.Old, r.New)
	}
	if r.Reason == "" {
		t.Error("refused_moves[0] gives no reason")
	}
	if doc.MovesOverCap != 0 {
		t.Errorf("moves_over_cap = %d on a refusal the cap had nothing to do with", doc.MovesOverCap)
	}
}

// TestCommitPayloadReportsTheCap: past the cap the commit records none of the
// moves, and the payload says how many there were -- the fact the stderr notice
// states in prose.
func TestCommitPayloadReportsTheCap(t *testing.T) {
	const count = 21 // one past the cap

	dir := newRepo(t)
	var seed, paths []string
	for i := 0; i < count; i++ {
		old := fmt.Sprintf("old%02d.txt", i)
		testutil.WriteFile(t, dir, old, fmt.Sprintf("content number %d\n", i))
		seed = append(seed, old)
	}
	safegitCommit(t, dir, "seed", seed...)

	for i := 0; i < count; i++ {
		old := fmt.Sprintf("old%02d.txt", i)
		new := fmt.Sprintf("new%02d.dat", i)
		moveOnDisk(t, dir, old, new)
		paths = append(paths, old, new)
	}

	args := append([]string{"commit", "-m", "scatter", "--"}, paths...)
	doc := commitPayloadOf(t, dir, args...)

	if doc.MovesOverCap != count {
		t.Errorf("moves_over_cap = %d, want %d", doc.MovesOverCap, count)
	}
	if len(doc.MovedRecords) != 0 {
		t.Errorf("moved_records = %+v; past the cap a commit records none of them", doc.MovedRecords)
	}
	if len(doc.RefusedMoves) != count {
		t.Errorf("refused_moves holds %d entries, want all %d", len(doc.RefusedMoves), count)
	}
}

// TestAmendPayloadCarriesTheRecordsItMinted: the amend arm mints too, so its
// payload answers the same question. The record preserved from the message it
// replaced is NOT reported: this member is what THIS operation put on the
// commit, and the preserved one was reported when it was written.
func TestAmendPayloadCarriesTheRecordsItMinted(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	testutil.WriteFile(t, dir, "c.txt", "other content nothing else holds\n")
	safegitCommit(t, dir, "seed", "a.txt", "c.txt")

	moveOnDisk(t, dir, "a.txt", "b.txt")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "move a",
		"--moved", "a.txt -> b.txt", "--", "a.txt", "b.txt"); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	moveOnDisk(t, dir, "c.txt", "d.txt")
	doc := commitPayloadOf(t, dir, "commit", "--amend", "--", "c.txt", "d.txt")

	if len(doc.MovedRecords) != 1 {
		t.Fatalf("moved_records = %+v, want the one record the amend minted", doc.MovedRecords)
	}
	got := doc.MovedRecords[0]
	if got.Old != "c.txt" || got.New != "d.txt" || got.Origin != "observed" {
		t.Errorf("moved_records[0] = %+v, want c.txt -> d.txt (observed)", got)
	}
}

// TestEveryCommitFormCarriesTheMoveMembers: the three members are declared on
// the plain commit, the amend and the reword alike -- a consumer reads them
// rather than inferring their absence.
func TestEveryCommitFormCarriesTheMoveMembers(t *testing.T) {
	memberOf := func(t *testing.T, dir string, args ...string) map[string]interface{} {
		t.Helper()
		stdout, stderr, code := runSafegit(t, dir, append([]string{"--json"}, args...)...)
		if code != 0 {
			t.Fatalf("safegit %s --json failed (%d): stdout=%s stderr=%s", strings.Join(args, " "), code, stdout, stderr)
		}
		var doc map[string]interface{}
		if err := json.Unmarshal(decodeEnvelope(t, stdout).Payload, &doc); err != nil {
			t.Fatalf("payload does not decode: %v\nstdout: %s", err, stdout)
		}
		return doc
	}

	for _, tc := range []struct {
		name string
		run  func(t *testing.T, dir string) map[string]interface{}
	}{
		{"commit", func(t *testing.T, dir string) map[string]interface{} {
			testutil.WriteFile(t, dir, "b.txt", "new\n")
			return memberOf(t, dir, "commit", "-m", "second", "--", "b.txt")
		}},
		{"amend", func(t *testing.T, dir string) map[string]interface{} {
			testutil.WriteFile(t, dir, "a.txt", "amended\n")
			return memberOf(t, dir, "commit", "--amend", "-m", "amended", "--", "a.txt")
		}},
		{"reword", func(t *testing.T, dir string) map[string]interface{} {
			return memberOf(t, dir, "commit", "--amend", "-m", "a new message")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			testutil.WriteFile(t, dir, "a.txt", "one\n")
			safegitCommit(t, dir, "seed", "a.txt")

			doc := tc.run(t, dir)
			for _, member := range []string{"moved_records", "refused_moves", "moves_over_cap"} {
				value, present := doc[member]
				if !present {
					t.Fatalf("the %s payload declares no %s member", tc.name, member)
				}
				if value == nil {
					t.Errorf("%s = null on the %s form; an absent answer is an empty list, never null", member, tc.name)
				}
			}
		})
	}
}
