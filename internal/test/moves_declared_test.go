package test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// `--moved 'old -> new'` is how a caller STATES a move themselves, and what
// safegit does with the statement is check it against the repository and then
// write it into the commit message as a record. (safegit also mints records for
// what a commit's own delta witnesses -- moves_inferred_test.go -- while still
// detecting no renames and staging nothing from blob equality, which
// moves_test.go pins.)
//
// These tests are about the whole path from a command line to a raw commit
// object: the record that appears, the id it carries, the quoting it survives,
// the four ways a declaration is refused, and what an amend does to a record
// somebody else's -m would otherwise have thrown away.

// commitMessageOf reads the raw commit object's message -- not `git log`'s
// rendering, which strips and re-wraps. A record is bytes in a commit, and that
// is what has to be asserted.
func commitMessageOf(t *testing.T, dir, rev string) string {
	t.Helper()
	raw := testutil.GitOut(t, dir, "cat-file", "commit", rev)
	blank := strings.Index(raw, "\n\n")
	if blank < 0 {
		t.Fatalf("commit %s has no message: %q", rev, raw)
	}
	return raw[blank+2:]
}

// movedRecordPattern matches one Moved: trailer and captures its id, its
// ORIGIN TOKEN where it carries one, and its pair -- so a test can assert each
// part without hard-coding an id nothing can predict.
//
// The token is optional because ABSENCE IS THE DECLARED ORIGIN: a record a
// person stated carries no token at all, which is the shape every record had
// before safegit minted any of its own.
var movedRecordPattern = regexp.MustCompile(`(?m)^Moved: ([0-9A-HJKMNP-TV-Z]{26})(?: (observed))? (.*)$`)

// movedRecordsIn returns the (id, pair) of every Moved: trailer in a message.
// The origin token, where there is one, is not part of the pair; ask
// movedOriginsIn for it.
func movedRecordsIn(t *testing.T, message string) [][2]string {
	t.Helper()
	var out [][2]string
	for _, m := range movedRecordPattern.FindAllStringSubmatch(message, -1) {
		out = append(out, [2]string{m[1], m[3]})
	}
	return out
}

// movedOriginsIn returns the origin of every Moved: trailer in a message, in
// the same order movedRecordsIn returns them, naming an absent token
// "declared" -- which is what an absent token means.
func movedOriginsIn(t *testing.T, message string) []string {
	t.Helper()
	var out []string
	for _, m := range movedRecordPattern.FindAllStringSubmatch(message, -1) {
		if m[2] == "" {
			out = append(out, "declared")
			continue
		}
		out = append(out, m[2])
	}
	return out
}

// assertOrigins fails unless the message's records carry exactly these origins,
// in order.
func assertOrigins(t *testing.T, message string, want ...string) {
	t.Helper()
	got := movedOriginsIn(t, message)
	if len(got) != len(want) {
		t.Fatalf("the message carries %d record(s) with origins %v, want %d %v; message:\n%s",
			len(got), got, len(want), want, message)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record %d has origin %q, want %q; message:\n%s", i, got[i], want[i], message)
		}
	}
}

// seedMove builds the state a declaration acts on: one committed file, moved on
// disk and not yet committed at its new name.
func seedMove(t *testing.T, old, new string) string {
	t.Helper()
	dir := newRepo(t)
	testutil.WriteFile(t, dir, old, "content that does not change\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", old); code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, new)), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", new, err)
	}
	if err := os.Rename(filepath.Join(dir, old), filepath.Join(dir, new)); err != nil {
		t.Fatalf("rename %s -> %s: %v", old, new, err)
	}
	return dir
}

func TestDeclaredFileMoveIsRecordedWithAnID(t *testing.T) {
	dir := seedMove(t, "a.txt", "sub/b.txt")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "move a into sub",
		"--moved", "a.txt -> sub/b.txt", "--", "a.txt", "sub/b.txt")
	if code != 0 {
		t.Fatalf("declared move commit failed (code %d): %s", code, stderr)
	}

	msg := commitMessageOf(t, dir, "HEAD")
	records := movedRecordsIn(t, msg)
	if len(records) != 1 {
		t.Fatalf("expected exactly one record in:\n%s", msg)
	}
	if records[0][1] != "a.txt -> sub/b.txt" {
		t.Errorf("record pair is %q", records[0][1])
	}
	// The id is a leading token INSIDE the value, so the record is one line and
	// nothing can separate a record from its id.
	if !strings.Contains(msg, "Moved: "+records[0][0]+" a.txt -> sub/b.txt\n") {
		t.Errorf("the record is not one line with its id leading:\n%s", msg)
	}
	if len(records[0][0]) != 26 {
		t.Errorf("record id %q is not 26 characters", records[0][0])
	}

	// The record is the ONLY thing the declaration added: it stages nothing and
	// changes no path.
	diffTree := testutil.Git(t, dir, "diff-tree", "--no-commit-id", "--no-renames", "-r", "--name-status", "HEAD")
	if !strings.Contains(diffTree, "A\tsub/b.txt") || !strings.Contains(diffTree, "D\ta.txt") {
		t.Errorf("the commit's paths are:\n%s", diffTree)
	}
}

func TestDeclaredMoveSitsBeforeTheSessionTrailer(t *testing.T) {
	dir := seedMove(t, "a.txt", "b.txt")
	_, stderr, code := runSafegitEnv(t, dir, []string{"CLAUDE_CODE_SESSION_ID=session-for-moves"},
		"commit", "-m", "move", "--moved", "a.txt -> b.txt", "--", "a.txt", "b.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	msg := commitMessageOf(t, dir, "HEAD")
	record := strings.Index(msg, "Moved: ")
	session := strings.Index(msg, "Claude-Code-Session-Id: ")
	if record < 0 || session < 0 {
		t.Fatalf("expected both trailers in:\n%s", msg)
	}
	// A move record is the CALLER's own content, so it goes on with the
	// caller's trailers -- before the commit-msg hook runs and therefore before
	// safegit's session trailer, which is injected after the hook precisely so
	// a rewriting hook cannot strip it.
	if record > session {
		t.Errorf("the move record was written after the session trailer:\n%s", msg)
	}
}

func TestDeclaredSubtreeMoveIsOneRecord(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "src/one.txt", "1\n")
	testutil.WriteFile(t, dir, "src/deep/two.txt", "2\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", "src"); code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}
	if err := os.Rename(filepath.Join(dir, "src"), filepath.Join(dir, "lib")); err != nil {
		t.Fatalf("rename src -> lib: %v", err)
	}

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "move the whole directory",
		"--moved", "src/ -> lib/", "--", "src", "lib")
	if code != 0 {
		t.Fatalf("subtree move commit failed (code %d): %s", code, stderr)
	}

	msg := commitMessageOf(t, dir, "HEAD")
	records := movedRecordsIn(t, msg)
	if len(records) != 1 || records[0][1] != "src/ -> lib/" {
		t.Fatalf("expected one subtree record, got %v in:\n%s", records, msg)
	}
	// Two files moved and the record is still one line: the per-file answers
	// are derived when it is read, not enumerated when it is written.
	files := testutil.Git(t, dir, "diff-tree", "--no-commit-id", "--no-renames", "-r", "--name-only", "HEAD")
	if len(strings.Fields(files)) != 4 {
		t.Errorf("expected four changed paths (two removed, two added), got:\n%s", files)
	}
}

func TestDeclaredMoveQuotesAPathThatNeedsIt(t *testing.T) {
	dir := seedMove(t, "two words.txt", "renamed \"quoted\".txt")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "move an awkward name",
		"--moved", `"two words.txt" -> "renamed \"quoted\".txt"`,
		"--", "two words.txt", `renamed "quoted".txt`)
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	msg := commitMessageOf(t, dir, "HEAD")
	records := movedRecordsIn(t, msg)
	if len(records) != 1 {
		t.Fatalf("expected one record in:\n%s", msg)
	}
	want := `"two words.txt" -> "renamed \"quoted\".txt"`
	if records[0][1] != want {
		t.Errorf("record pair is %q, want %q", records[0][1], want)
	}
	// One line, whatever the paths hold: a record that wrapped would not be a
	// trailer any more.
	if strings.Count(strings.TrimRight(msg, "\n"), "Moved: ") != 1 {
		t.Errorf("the record did not stay on one line:\n%s", msg)
	}
}

func TestDeclaredMoveRefusesAnUntrackedOldPath(t *testing.T) {
	dir := seedMove(t, "a.txt", "b.txt")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "move",
		"--moved", "never-existed.txt -> b.txt", "--", "a.txt", "b.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	if !strings.Contains(stderr, "not tracked") {
		t.Errorf("refusal does not say the old path is untracked: %s", stderr)
	}
	assertNoCommitHappened(t, dir, "seed")
}

func TestDeclaredMoveRefusesAnOldPathStillOnDisk(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "a\n")
	testutil.WriteFile(t, dir, "b.txt", "b\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", "a.txt", "b.txt"); code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}
	testutil.WriteFile(t, dir, "b.txt", "changed\n")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "claim a move that did not happen",
		"--moved", "a.txt -> b.txt", "--", "b.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	if !strings.Contains(stderr, "still on disk") {
		t.Errorf("refusal does not say the old path is still there: %s", stderr)
	}
	assertNoCommitHappened(t, dir, "seed")
}

func TestDeclaredMoveRefusesAnAbsentNewPath(t *testing.T) {
	dir := seedMove(t, "a.txt", "b.txt")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "move",
		"--moved", "a.txt -> nowhere.txt", "--", "a.txt", "b.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	if !strings.Contains(stderr, "neither disk nor") {
		t.Errorf("refusal does not say the new path is nowhere: %s", stderr)
	}
	assertNoCommitHappened(t, dir, "seed")
}

func TestDeclaredMoveRefusesNestedDeclarations(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "src/one.txt", "1\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", "src"); code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}
	if err := os.Rename(filepath.Join(dir, "src"), filepath.Join(dir, "lib")); err != nil {
		t.Fatalf("rename: %v", err)
	}

	// Two declarations, one inside the other: two different fates for
	// lib/one.txt. A reader could resolve it by longest match; a writer
	// guessing which the caller meant is the silent precedence rule this tool
	// does not have.
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "move",
		"--moved", "src/ -> lib/", "--moved", "src/one.txt -> lib/one.txt",
		"--", "src", "lib")
	if code != exitcode.Usage {
		t.Fatalf("exit %d, want %d (Usage): %s", code, exitcode.Usage, stderr)
	}
	if !strings.Contains(stderr, "the same source") {
		t.Errorf("refusal does not name the overlap: %s", stderr)
	}

	// The destination side is refused the same way.
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "move",
		"--moved", "src/ -> lib/", "--moved", "gone.txt -> lib/gone.txt",
		"--", "src", "lib")
	if code != exitcode.Usage {
		t.Fatalf("destination overlap: exit %d, want %d: %s", code, exitcode.Usage, stderr)
	}
	if !strings.Contains(stderr, "the same destination") {
		t.Errorf("refusal does not name the destination overlap: %s", stderr)
	}
	assertNoCommitHappened(t, dir, "seed")
}

// TestDeclaredMoveRefusesChainedDeclarations: one path that is both a
// destination and a source -- `p.txt -> q.txt` beside `q.txt -> r.txt` -- says
// something whose outcome depends on the order the two are read in, which is
// not something the caller stated. `safegit mv` has always refused that shape,
// and `--moved` is the same declaration by another spelling, so it goes through
// the same check and gets the same verdict.
func TestDeclaredMoveRefusesChainedDeclarations(t *testing.T) {
	seed := func(t *testing.T) string {
		t.Helper()
		dir := newRepo(t)
		testutil.WriteFile(t, dir, "p.txt", "p\n")
		testutil.WriteFile(t, dir, "q.txt", "q\n")
		if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", "p.txt", "q.txt"); code != 0 {
			t.Fatalf("seed commit failed (code %d): %s", code, stderr)
		}
		return dir
	}

	assertChainRefusal := func(t *testing.T, stderr string, code int) {
		t.Helper()
		if code != exitcode.Usage {
			t.Fatalf("exit %d, want %d (Usage): %s", code, exitcode.Usage, stderr)
		}
		if !strings.Contains(stderr, "moves a path the other moves away from") {
			t.Errorf("refusal does not name the chain: %s", stderr)
		}
	}

	t.Run("commit", func(t *testing.T) {
		dir := seed(t)
		_, stderr, code := runSafegit(t, dir, "commit", "-m", "chained",
			"--moved", "p.txt -> q.txt", "--moved", "q.txt -> r.txt", "--", "p.txt")
		assertChainRefusal(t, stderr, code)
		assertNoCommitHappened(t, dir, "seed")
	})

	t.Run("amend", func(t *testing.T) {
		dir := seed(t)
		before := testutil.Rev(t, dir, "HEAD")
		_, stderr, code := runSafegit(t, dir, "commit", "--amend",
			"--moved", "p.txt -> q.txt", "--moved", "q.txt -> r.txt")
		assertChainRefusal(t, stderr, code)
		if got := testutil.Rev(t, dir, "HEAD"); got != before {
			t.Errorf("a refused amend rewrote the commit: HEAD is %s, want %s", got, before)
		}
	})
}

func TestDeclaredMoveRefusesAMalformedPair(t *testing.T) {
	dir := seedMove(t, "a.txt", "b.txt")
	for _, pair := range []string{"no arrow at all", "a -> b -> c", "src/ -> lib", "a.txt -> a.txt"} {
		_, stderr, code := runSafegit(t, dir, "commit", "-m", "move", "--moved", pair, "--", "a.txt", "b.txt")
		if code == 0 {
			t.Errorf("--moved %q was accepted", pair)
			continue
		}
		if strings.Contains(stderr, "panic") {
			t.Errorf("--moved %q panicked: %s", pair, stderr)
		}
	}
	assertNoCommitHappened(t, dir, "seed")
}

// assertNoCommitHappened fails unless the tip is still the commit with the
// given subject: a refused declaration must leave the branch where it was.
func assertNoCommitHappened(t *testing.T, dir, subject string) {
	t.Helper()
	if got := testutil.Git(t, dir, "log", "-1", "--format=%s"); got != subject {
		t.Errorf("the tip is %q; a refused declaration must commit nothing", got)
	}
}

func TestAmendWithAMessagePreservesMoveRecords(t *testing.T) {
	dir := seedMove(t, "a.txt", "b.txt")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "the move",
		"--moved", "a.txt -> b.txt", "--", "a.txt", "b.txt"); code != 0 {
		t.Fatalf("move commit failed (code %d): %s", code, stderr)
	}
	original := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))
	if len(original) != 1 {
		t.Fatalf("fixture did not record a move: %v", original)
	}

	// An amend that replaces the message: the record survives, id and all.
	// Dropping a record is a retraction the caller states, never a side effect
	// of rewording the commit that carries it.
	testutil.WriteFile(t, dir, "b.txt", "amended content\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "a better subject", "--", "b.txt"); code != 0 {
		t.Fatalf("amend failed (code %d): %s", code, stderr)
	}
	msg := commitMessageOf(t, dir, "HEAD")
	if !strings.HasPrefix(msg, "a better subject") {
		t.Errorf("the amend did not replace the message:\n%s", msg)
	}
	after := movedRecordsIn(t, msg)
	if len(after) != 1 || after[0] != original[0] {
		t.Fatalf("records after the amend are %v, want %v", after, original)
	}

	// A reword, which is the same operation with no files: same answer.
	if _, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "a third subject"); code != 0 {
		t.Fatalf("reword failed (code %d): %s", code, stderr)
	}
	msg = commitMessageOf(t, dir, "HEAD")
	reworded := movedRecordsIn(t, msg)
	if len(reworded) != 1 || reworded[0] != original[0] {
		t.Fatalf("records after the reword are %v, want %v", reworded, original)
	}
	if strings.Count(msg, "Moved: ") != 1 {
		t.Errorf("the record was duplicated by carrying it forward:\n%s", msg)
	}
}

func TestAmendPreservesARetractionToo(t *testing.T) {
	dir := seedMove(t, "a.txt", "b.txt")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "the move",
		"--moved", "a.txt -> b.txt", "--", "a.txt", "b.txt"); code != 0 {
		t.Fatalf("move commit failed (code %d): %s", code, stderr)
	}
	id := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))[0][0]

	// A later commit retracts it. Retraction has no flag of its own yet; it is
	// a trailer, and --trailer writes one.
	testutil.WriteFile(t, dir, "c.txt", "c\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "that record was wrong",
		"--trailer", "Moved-Retract: "+id, "--", "c.txt"); code != 0 {
		t.Fatalf("retraction commit failed (code %d): %s", code, stderr)
	}

	testutil.WriteFile(t, dir, "c.txt", "c amended\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "reworded", "--", "c.txt"); code != 0 {
		t.Fatalf("amend failed (code %d): %s", code, stderr)
	}
	msg := commitMessageOf(t, dir, "HEAD")
	if !strings.Contains(msg, "Moved-Retract: "+id) {
		t.Errorf("the amend dropped a retraction:\n%s", msg)
	}
	if strings.Count(msg, "Moved-Retract: ") != 1 {
		t.Errorf("the retraction was duplicated:\n%s", msg)
	}
}

func TestAmendCanAddARecordToACommitThatLacksOne(t *testing.T) {
	dir := seedMove(t, "a.txt", "b.txt")
	// The content changes on the way, so the commit's own delta witnesses no
	// move and mints no record: the file's blob at the new path is not the blob
	// that left the old one. That is what leaves the commit without a record for
	// the amend to add -- the ordinary mistake.
	testutil.WriteFile(t, dir, "b.txt", "content that DID change\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "move a to b", "--", "a.txt", "b.txt"); code != 0 {
		t.Fatalf("move commit failed (code %d): %s", code, stderr)
	}
	if records := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD")); len(records) != 0 {
		t.Fatalf("the fixture recorded a move nobody declared: %v", records)
	}
	before := testutil.Git(t, dir, "rev-parse", "HEAD^{tree}")

	// The amend declares it. The declaration is judged against the FIRST PARENT
	// of the commit being amended, not against that commit -- the amended
	// commit is exactly where a.txt stopped being tracked.
	_, stderr, code := runSafegit(t, dir, "commit", "--amend", "--moved", "a.txt -> b.txt")
	if code != 0 {
		t.Fatalf("amend with only a declaration failed (code %d): %s", code, stderr)
	}
	msg := commitMessageOf(t, dir, "HEAD")
	if records := movedRecordsIn(t, msg); len(records) != 1 || records[0][1] != "a.txt -> b.txt" {
		t.Fatalf("expected the declared record in:\n%s", msg)
	}
	if !strings.HasPrefix(msg, "move a to b") {
		t.Errorf("an amend with no -m must keep the message:\n%s", msg)
	}
	if after := testutil.Git(t, dir, "rev-parse", "HEAD^{tree}"); after != before {
		t.Errorf("an amend that only declares a move changed the tree (%s -> %s)", before, after)
	}
}

// A pair the commit being amended ALREADY declares cannot be declared again.
// Two records for one move are two claims where the caller made one, and the
// second carries a fresh id, so nothing downstream can tell they are the same
// statement. The refusal names the id already carrying it, which is the id a
// caller would need for --moved-retract if the record is what they meant to
// replace.
func TestAmendRefusesAPairTheCommitAlreadyDeclares(t *testing.T) {
	dir := seedMove(t, "a.txt", "b.txt")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "the move",
		"--moved", "a.txt -> b.txt", "--", "a.txt", "b.txt"); code != 0 {
		t.Fatalf("move commit failed (code %d): %s", code, stderr)
	}
	id := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))[0][0]
	tipBefore := testutil.Rev(t, dir, "HEAD")

	// An amend that keeps the message: the record is already in the message the
	// amend reuses.
	_, stderr, code := runSafegit(t, dir, "commit", "--amend", "--moved", "a.txt -> b.txt")
	if code != exitcode.Usage {
		t.Fatalf("re-declaring on an amend exited %d, want %d (Usage): %s", code, exitcode.Usage, stderr)
	}
	if !strings.Contains(stderr, id) {
		t.Errorf("the refusal does not name the record already carrying the pair (%s): %s", id, stderr)
	}

	// An amend that REPLACES the message: the record is in the preserved block
	// rather than in the message itself, and the answer is the same.
	_, stderr, code = runSafegit(t, dir, "commit", "--amend", "-m", "reworded",
		"--moved", "a.txt -> b.txt")
	if code != exitcode.Usage {
		t.Fatalf("re-declaring on a reword exited %d, want %d (Usage): %s", code, exitcode.Usage, stderr)
	}
	if !strings.Contains(stderr, id) {
		t.Errorf("the reword refusal does not name the existing record (%s): %s", id, stderr)
	}

	if got := testutil.Rev(t, dir, "HEAD"); got != tipBefore {
		t.Errorf("a refused re-declaration replaced the tip (%s -> %s)", tipBefore, got)
	}
	if n := strings.Count(commitMessageOf(t, dir, "HEAD"), "Moved: "); n != 1 {
		t.Errorf("the tip carries %d records, want 1", n)
	}
}

// The refusal is about an un-retracted record. Once the record is retracted the
// pair is no longer claimed by anything, so declaring it again is a caller
// making the statement afresh -- which is exactly what a retraction followed by
// a re-declaration is for.
func TestAmendAllowsRedeclaringAPairWhoseRecordIsRetracted(t *testing.T) {
	dir := seedMove(t, "a.txt", "b.txt")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "the move",
		"--moved", "a.txt -> b.txt", "--", "a.txt", "b.txt"); code != 0 {
		t.Fatalf("move commit failed (code %d): %s", code, stderr)
	}
	id := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))[0][0]

	// The retraction goes on the same commit, through the open --trailer
	// spelling: --moved-retract cannot reach a record the commit being amended
	// declared itself.
	if _, stderr, code := runSafegit(t, dir, "commit", "--amend", "-m", "the move",
		"--trailer", "Moved-Retract: "+id); code != 0 {
		t.Fatalf("retracting on the amend failed (code %d): %s", code, stderr)
	}

	_, stderr, code := runSafegit(t, dir, "commit", "--amend", "--moved", "a.txt -> b.txt")
	if code != 0 {
		t.Fatalf("re-declaring a retracted pair exited %d, want 0: %s", code, stderr)
	}
	records := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))
	if len(records) != 2 {
		t.Fatalf("expected the retracted record plus the new one, got %v", records)
	}
	if records[0][0] == records[1][0] {
		t.Errorf("the re-declaration reused the retracted id: %v", records)
	}
}

func TestDeclaredMoveResolvesFromASubdirectory(t *testing.T) {
	dir := seedMove(t, "sub/a.txt", "sub/b.txt")
	sub := filepath.Join(dir, "sub")

	// Run from inside sub/: the paths a caller types are relative to the
	// caller's own directory, exactly as positional paths are, and the record
	// still names the repo-relative path a tree can answer for.
	_, stderr, code := runSafegit(t, sub, "commit", "-m", "move inside sub",
		"--moved", "a.txt -> b.txt", "--", "a.txt", "b.txt")
	if code != 0 {
		t.Fatalf("commit from a subdirectory failed (code %d): %s", code, stderr)
	}
	records := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))
	if len(records) != 1 || records[0][1] != "sub/a.txt -> sub/b.txt" {
		t.Fatalf("record is %v, want the repo-relative pair", records)
	}
}

func TestDeclaredMoveRefusedOnARootCommit(t *testing.T) {
	dir := newEmptyRepo(t)
	testutil.WriteFile(t, dir, "b.txt", "b\n")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "first", "--moved", "a.txt -> b.txt", "--", "b.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	if !strings.Contains(stderr, "no parent") {
		t.Errorf("refusal does not say the commit has no parent: %s", stderr)
	}
}

func TestDeclaredMoveIsRecordedUnderDryRunWithoutCommitting(t *testing.T) {
	dir := seedMove(t, "a.txt", "b.txt")
	_, stderr, code := runSafegit(t, dir, "--dry-run", "commit", "-m", "move",
		"--moved", "a.txt -> b.txt", "--", "a.txt", "b.txt")
	if code != 0 {
		t.Fatalf("dry-run commit failed (code %d): %s", code, stderr)
	}
	assertNoCommitHappened(t, dir, "seed")

	// A preview of a declaration the repository contradicts is the REFUSAL, not
	// a rehearsal of a commit that would be refused.
	_, stderr, code = runSafegit(t, dir, "--dry-run", "commit", "-m", "move",
		"--moved", "never-existed.txt -> b.txt", "--", "a.txt", "b.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("dry-run exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
	}
}
