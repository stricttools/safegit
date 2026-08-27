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

// A commit-msg hook may rewrite the message, and a rewriting hook that strips
// the record lines leaves a commit that carries no records at all. The payload
// answers for the COMMITTED message: what this run minted, narrowed to what
// survived the hook, so a consumer is never told about a record it cannot find
// on the commit.
//
// The hook runs on both origins alike -- it is text, and the declared record is
// as strippable as the observed one -- so the whole member empties.
func TestCommitPayloadReportsOnlyTheRecordsTheCommittedMessageCarries(t *testing.T) {
	dir := newRepo(t)
	installHook(t, dir, "commit-msg", stripMovedLinesHook)

	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	testutil.WriteFile(t, dir, "c.txt", "other content nothing else holds\n")
	safegitCommit(t, dir, "seed", "a.txt", "c.txt")

	moveOnDisk(t, dir, "a.txt", "b.txt")
	moveOnDisk(t, dir, "c.txt", "d.txt")
	doc := commitPayloadOf(t, dir, "commit", "-m", "move both",
		"--moved", "a.txt -> b.txt", "--", "a.txt", "b.txt", "c.txt", "d.txt")

	msg := commitMessageOf(t, dir, "HEAD")
	if strings.Contains(msg, "Moved: ") {
		t.Fatalf("the fixture's hook did not strip the records; message:\n%s", msg)
	}
	if len(doc.MovedRecords) != 0 {
		t.Errorf("moved_records = %+v, but the committed message carries no record at all:\n%s",
			doc.MovedRecords, msg)
	}
}

// The same, on the amend arm: its payload answers for the message the amended
// commit ended up with.
func TestAmendPayloadReportsOnlyTheRecordsTheCommittedMessageCarries(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	safegitCommit(t, dir, "seed", "a.txt")
	// The amend judges its declaration against the tip's FIRST PARENT, so the
	// tip being replaced has to be a commit above the one that tracks a.txt.
	testutil.WriteFile(t, dir, "other.txt", "unrelated\n")
	safegitCommit(t, dir, "second", "other.txt")

	installHook(t, dir, "commit-msg", stripMovedLinesHook)

	moveOnDisk(t, dir, "a.txt", "b.txt")
	doc := commitPayloadOf(t, dir, "commit", "--amend", "--moved", "a.txt -> b.txt",
		"--", "a.txt", "b.txt")

	msg := commitMessageOf(t, dir, "HEAD")
	if strings.Contains(msg, "Moved: ") {
		t.Fatalf("the fixture's hook did not strip the records; message:\n%s", msg)
	}
	if len(doc.MovedRecords) != 0 {
		t.Errorf("moved_records = %+v, but the amended commit's message carries no record at all:\n%s",
			doc.MovedRecords, msg)
	}
}

// A DRY RUN runs no hook at all, so nothing strips anything and the preview
// reports exactly what the real run would have written before the hook saw it.
// The narrowing is against the message this run composed, never against the
// commit that would come out of some other run.
func TestCommitPreviewStillReportsTheRecordsItWouldMint(t *testing.T) {
	dir := newRepo(t)
	installHook(t, dir, "commit-msg", stripMovedLinesHook)

	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	safegitCommit(t, dir, "seed", "a.txt")

	moveOnDisk(t, dir, "a.txt", "b.txt")
	doc := commitPayloadOf(t, dir, "commit", "-m", "move a", "--dry-run", "--", "a.txt", "b.txt")

	if len(doc.MovedRecords) != 1 {
		t.Fatalf("moved_records = %+v, want the record the preview would have written", doc.MovedRecords)
	}
	if got := doc.MovedRecords[0]; got.Old != "a.txt" || got.New != "b.txt" || got.Origin != "observed" {
		t.Errorf("moved_records[0] = %+v, want a.txt -> b.txt (observed)", got)
	}
}

// The REWORD arm. A reword takes no files and changes no tree, but it still
// mints records -- `--moved` is judged against the reworded commit's own first
// parent -- and its message goes past the same commit-msg hook. So its payload
// owes the same answer: what the committed message carries, not what this run
// composed before the hook rewrote it.
func TestRewordPayloadReportsOnlyTheRecordsTheCommittedMessageCarries(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	safegitCommit(t, dir, "seed", "a.txt")
	// The reword judges its declaration against the tip's FIRST PARENT, so the
	// tip being reworded has to be a commit above the one that tracks a.txt.
	testutil.WriteFile(t, dir, "other.txt", "unrelated\n")
	safegitCommit(t, dir, "second", "other.txt")

	installHook(t, dir, "commit-msg", stripMovedLinesHook)

	moveOnDisk(t, dir, "a.txt", "b.txt")
	// No pathspec at all: that is what makes this a reword rather than an amend.
	doc := commitPayloadOf(t, dir, "commit", "--amend", "-m", "reworded",
		"--moved", "a.txt -> b.txt")

	if doc.ExecutionMode == nil || *doc.ExecutionMode != "reword" {
		t.Fatalf("execution_mode = %v, want reword; the fixture did not take the reword arm", doc.ExecutionMode)
	}
	msg := commitMessageOf(t, dir, "HEAD")
	if strings.Contains(msg, "Moved: ") {
		t.Fatalf("the fixture's hook did not strip the records; message:\n%s", msg)
	}
	if len(doc.MovedRecords) != 0 {
		t.Errorf("moved_records = %+v, but the reworded commit's message carries no record at all:\n%s",
			doc.MovedRecords, msg)
	}
}

// mvMovedRecordsDoc is the one member of mv's payload these two tests read,
// spelled as a consumer sees it on the wire. The slice is a POINTER so the three
// answers stay distinguishable: an absent member and a null one both decode to a
// nil pointer and are failures, while the member the command owes -- present and
// an array -- decodes to a non-nil pointer whatever its length.
type mvMovedRecordsDoc struct {
	MovedRecords *[]struct {
		ID     string `json:"id"`
		Old    string `json:"old"`
		New    string `json:"new"`
		Origin string `json:"origin"`
	} `json:"moved_records"`
}

// The MV arm. `safegit mv` mints a record per pair and hands them to the same
// pipeline, so the same hook strips them out of the same message -- and mv's
// payload member is the same member `commit` declares, rendered from the
// pipeline's own record list, so the answer is the committed message's rather
// than the pair list's.
//
// Three things are pinned at once, which is why the member is decoded through a
// pointer: it is present under the name `commit` uses, it is an ARRAY rather
// than null (the renderer's non-nil make -- a null here would be a second
// renderer having crept in), and it is EMPTY, because the hook removed every
// record from the message the commit ended up with.
func TestMvPayloadReportsOnlyTheRecordsTheCommittedMessageCarries(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	safegitCommit(t, dir, "seed", "a.txt")

	installHook(t, dir, "commit-msg", stripMovedLinesHook)

	stdout, stderr, code := runSafegit(t, dir, "--json", "mv", "a.txt -> b.txt", "-m", "move a")
	if code != 0 {
		t.Fatalf("safegit mv failed (%d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	var doc mvMovedRecordsDoc
	if err := json.Unmarshal(decodeEnvelope(t, stdout).Payload, &doc); err != nil {
		t.Fatalf("mv payload does not decode: %v\nstdout: %s", err, stdout)
	}

	msg := commitMessageOf(t, dir, "HEAD")
	if strings.Contains(msg, "Moved: ") {
		t.Fatalf("the fixture's hook did not strip the records; message:\n%s", msg)
	}
	if doc.MovedRecords == nil {
		t.Fatalf("moved_records is absent or null; mv owes the member commit owes, and it is never null\nstdout: %s", stdout)
	}
	if len(*doc.MovedRecords) != 0 {
		t.Errorf("moved_records = %+v, but the commit's message carries no record at all:\n%s", *doc.MovedRecords, msg)
	}
}

// The mv preview runs no hook, so it still reports the record it would write --
// the same honesty the commit arm's preview has -- and it reports the record's
// ORIGIN with it: every pair `mv` is given is a person's statement, so the
// entry says `declared`, the same word the commit arm's declarations carry.
func TestMvPreviewStillReportsTheRecordItWouldMint(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "content nothing else holds\n")
	safegitCommit(t, dir, "seed", "a.txt")

	installHook(t, dir, "commit-msg", stripMovedLinesHook)

	stdout, stderr, code := runSafegit(t, dir, "--json", "--dry-run", "mv", "a.txt -> b.txt", "-m", "move a")
	if code != 0 {
		t.Fatalf("safegit mv --dry-run failed (%d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	var doc mvMovedRecordsDoc
	if err := json.Unmarshal(decodeEnvelope(t, stdout).Payload, &doc); err != nil {
		t.Fatalf("mv payload does not decode: %v\nstdout: %s", err, stdout)
	}
	if doc.MovedRecords == nil || len(*doc.MovedRecords) != 1 {
		t.Fatalf("moved_records = %v, want the one record the preview would have written\nstdout: %s",
			doc.MovedRecords, stdout)
	}
	got := (*doc.MovedRecords)[0]
	if got.Old != "a.txt" || got.New != "b.txt" || got.Origin != "declared" {
		t.Errorf("moved_records[0] = %+v, want a.txt -> b.txt (declared)", got)
	}
	if got.ID == "" {
		t.Errorf("moved_records[0] carries no id: %+v", got)
	}
}

// stripMovedLinesHook is a commit-msg hook that removes every record line, the
// way a real message-policy hook rewrites a message it does not like. It exits
// zero whatever it finds, so the commit itself is never refused.
const stripMovedLinesHook = "#!/bin/sh\ngrep -v '^Moved: ' \"$1\" > \"$1.kept\"\nmv \"$1.kept\" \"$1\"\nexit 0\n"
