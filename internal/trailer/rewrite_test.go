package trailer

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

// The three operations a history rewrite performs ON a move record, all of them
// working through the ONE encoder rather than through the raw line: does this
// record name a path, remove the records that name one, and rewrite a message
// without breaking a record's quoting.

func TestRecordNamesItsOwnSides(t *testing.T) {
	file := Record{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Old: "a.txt", New: "sub/b.txt"}
	for _, path := range []string{"a.txt", "sub/b.txt"} {
		if !file.Names(path) {
			t.Errorf("file-form record does not name %q", path)
		}
	}
	// A file-form record speaks for exactly two paths and nothing under them.
	for _, path := range []string{"a.txt/deeper", "sub", "sub/c.txt", "other"} {
		if file.Names(path) {
			t.Errorf("file-form record claims to name %q", path)
		}
	}

	subtree := Record{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Old: "src/", New: "lib/"}
	for _, path := range []string{"src", "src/one.txt", "src/deep/two.txt", "lib", "lib/one.txt"} {
		if !subtree.Names(path) {
			t.Errorf("subtree record does not name %q", path)
		}
	}
	for _, path := range []string{"srcx", "srcx/one.txt", "library/one.txt"} {
		if subtree.Names(path) {
			t.Errorf("subtree record claims to name %q", path)
		}
	}
}

func TestRemoveMovedRecordsNaming(t *testing.T) {
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	const other = "01BX5ZZKBKACTAV9WEVGEMMVRZ"

	message := "a subject\n\nsome body\n\n" +
		"Moved: " + id + " secret.env -> config/secret.env\n" +
		"Moved: " + other + " a.txt -> b.txt\n" +
		"Claude-Code-Session-Id: s\n"

	got, changed := RemoveMovedRecordsNaming(message, "config/secret.env")
	if !changed {
		t.Fatalf("removal reported no change:\n%s", got)
	}
	if strings.Contains(got, id) {
		t.Errorf("the record naming the path survived:\n%s", got)
	}
	if !strings.Contains(got, "Moved: "+other+" a.txt -> b.txt") {
		t.Errorf("an unrelated record was dropped:\n%s", got)
	}
	if !strings.Contains(got, "Claude-Code-Session-Id: s") {
		t.Errorf("a non-move trailer was dropped:\n%s", got)
	}
	if !strings.HasPrefix(got, "a subject\n\nsome body\n") {
		t.Errorf("the body did not survive:\n%s", got)
	}

	// A path no record names leaves the message byte-identical.
	if again, changed := RemoveMovedRecordsNaming(message, "untouched.txt"); changed || again != message {
		t.Errorf("removing a path nothing names changed the message:\n%s", again)
	}

	// A subtree record is removed when the scrubbed path is anywhere under it.
	subtreeMsg := "s\n\nMoved: " + id + " src/ -> lib/\n"
	if got, changed := RemoveMovedRecordsNaming(subtreeMsg, "lib/deep/one.txt"); !changed || strings.Contains(got, "Moved:") {
		t.Errorf("a subtree record covering the path survived:\n%s", got)
	}
}

func TestRemoveMovedRecordsNamingKeepsTheBodyWhenEveryTrailerGoes(t *testing.T) {
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	message := "just a subject\n\nMoved: " + id + " a.txt -> b.txt\n"
	got, changed := RemoveMovedRecordsNaming(message, "a.txt")
	if !changed {
		t.Fatal("removal reported no change")
	}
	if strings.Contains(got, "Moved:") {
		t.Errorf("the record survived:\n%q", got)
	}
	if got != "just a subject\n" {
		t.Errorf("message is %q, want %q", got, "just a subject\n")
	}

	// A message that is NOTHING but the record leaves nothing behind. The empty
	// answer is the honest one; whether a caller can write it is the caller's
	// problem to state (see removeScrubbedMoveRecords).
	onlyRecord := "Moved: " + id + " a.txt -> b.txt\n"
	if got, changed := RemoveMovedRecordsNaming(onlyRecord, "a.txt"); !changed || got != "" {
		t.Errorf("removing the only trailer of a body-less message gave %q (changed=%v)", got, changed)
	}
}

// TestRewriteMessageKeepsQuotingIntact is the whole point of the trailer-aware
// transform: a pattern that overlaps a C-quoted path must not be allowed to
// break the record's grammar. The rewrite happens inside the DECODED value and
// is re-encoded, so the result parses whatever the pattern did.
func TestRewriteMessageKeepsQuotingIntact(t *testing.T) {
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	original, err := ParseRecord(id + ` "two words.txt" -> "renamed \"quoted\".txt"`)
	if err != nil {
		t.Fatalf("fixture record does not parse: %v", err)
	}
	message := "subject SECRET\n\nbody SECRET\n\n" + RecordLine(original) + "\n"

	pat := regexp.MustCompile(`words|quoted|SECRET`)
	got, err := RewriteMessage(message, func(s string) string {
		return pat.ReplaceAllString(s, "X")
	})
	if err != nil {
		t.Fatalf("a transform whose result is still a move must not be refused: %v", err)
	}

	moves := ReadMoves(got)
	if len(moves.Malformed) != 0 {
		t.Fatalf("the rewrite produced a malformed record %v in:\n%s", moves.Malformed, got)
	}
	if len(moves.Records) != 1 {
		t.Fatalf("expected one record in:\n%s", got)
	}
	r := moves.Records[0]
	if r.ID != id {
		t.Errorf("the record's id was rewritten: %q", r.ID)
	}
	if r.Old != "two X.txt" {
		t.Errorf("old side is %q, want %q", r.Old, "two X.txt")
	}
	if r.New != `renamed "X".txt` {
		t.Errorf("new side is %q, want %q", r.New, `renamed "X".txt`)
	}
	if strings.Contains(got, "SECRET") {
		t.Errorf("the body was not rewritten:\n%s", got)
	}
}

func TestRewriteMessageLeavesEverythingElseVerbatim(t *testing.T) {
	message := "subject\n\nSigned-off-by: A SECRET <a@b>\n"
	got, err := RewriteMessage(message, func(s string) string {
		return strings.ReplaceAll(s, "SECRET", "X")
	})
	if err != nil {
		t.Fatalf("rewriting a message with no move record must not be refused: %v", err)
	}
	if got != "subject\n\nSigned-off-by: A X <a@b>\n" {
		t.Errorf("non-move trailer rewrite is %q", got)
	}

	// A message with no trailer block is transformed whole, exactly as it was
	// before the trailer split existed.
	plain, err := RewriteMessage("only a SECRET subject\n", func(s string) string {
		return strings.ReplaceAll(s, "SECRET", "X")
	})
	if err != nil {
		t.Fatalf("rewriting a message with no trailer block must not be refused: %v", err)
	}
	if plain != "only a X subject\n" {
		t.Errorf("plain message rewrite is %q", plain)
	}
}

// TestRewriteMessageRefusesATransformThatBreaksTheRecord is the other half of
// the trailer-aware transform. Re-encoding through the one encoder keeps the
// QUOTING readable whatever the substitution did, but the pair itself still has
// to be a move: two different paths, both or neither naming a subtree, neither
// of them empty. A transform can break each of those, and the record it would
// write is one the decoder refuses -- so the transform refuses instead, and the
// caller turns that into a rewrite that never starts.
func TestRewriteMessageRefusesATransformThatBreaksTheRecord(t *testing.T) {
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

	cases := []struct {
		name    string
		record  Record
		pattern string
		replace string
		wantOld string
		wantNew string
	}{
		{
			name:    "both paths become one",
			record:  Record{ID: id, Old: "z.txt", New: "a.txt"},
			pattern: `[az]\.txt`,
			replace: "q.txt",
			wantOld: "q.txt",
			wantNew: "q.txt",
		},
		{
			name:    "the subtree marker survives on one side only",
			record:  Record{ID: id, Old: "src/", New: "lib/"},
			pattern: `src/`,
			replace: "src",
			wantOld: "src",
			wantNew: "lib/",
		},
		{
			name:    "a token is emptied",
			record:  Record{ID: id, Old: "z.txt", New: "a.txt"},
			pattern: `z\.txt`,
			replace: "",
			wantOld: "",
			wantNew: "a.txt",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line := RecordLine(c.record)
			message := "a subject\n\n" + line + "\n"
			pat := regexp.MustCompile(c.pattern)

			got, err := RewriteMessage(message, func(s string) string {
				return pat.ReplaceAllString(s, c.replace)
			})
			if err == nil {
				t.Fatalf("the transform produced %q instead of refusing", got)
			}
			if got != "" {
				t.Errorf("a refused rewrite must produce no message, got %q", got)
			}

			var bad *RecordTransformError
			if !errors.As(err, &bad) {
				t.Fatalf("error is %T (%v), want a *RecordTransformError", err, err)
			}
			if bad.Line != line {
				t.Errorf("the refusal names the record as %q, want %q", bad.Line, line)
			}
			if bad.Old != c.wantOld || bad.New != c.wantNew {
				t.Errorf("the transformed pair is %q -> %q, want %q -> %q", bad.Old, bad.New, c.wantOld, c.wantNew)
			}
			if !strings.Contains(bad.Result, id) {
				t.Errorf("the refusal does not carry the line the transform would have written: %q", bad.Result)
			}
			if bad.Err == nil || ValidatePair(c.wantOld, c.wantNew) == nil {
				t.Errorf("the fixture pair %q -> %q is valid; it cannot demonstrate a refusal", c.wantOld, c.wantNew)
			}
			// The message the caller holds is untouched: nothing was written
			// half-transformed for a later reader to find.
			if moves := ReadMoves(message); len(moves.Records) != 1 || len(moves.Malformed) != 0 {
				t.Errorf("the original message no longer reads as one record: %+v", moves)
			}
		})
	}
}

func TestNests(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"src", "src", true},
		{"src", "src/one.txt", true},
		{"src/one.txt", "src", true},
		{"src", "src2", false},
		{"src2", "src", false},
		{"src", "srcx", false},
		{"src/a", "src/b", false},
		{"a", "b", false},
		{"", "", true},
	}
	for _, c := range cases {
		if got := Nests(c.a, c.b); got != c.want {
			t.Errorf("Nests(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestOverlap pins the one implementation both spellings of a declared move ask
// -- `safegit mv`'s pairs and `--moved`'s records -- so a command line either of
// them refuses is refused by both.
func TestOverlap(t *testing.T) {
	cases := []struct {
		name string
		a, b Pair
		want OverlapKind
		x, y string
	}{
		{
			name: "unrelated moves",
			a:    Pair{Old: "a", New: "b"},
			b:    Pair{Old: "c", New: "d"},
			want: NoOverlap,
		},
		{
			name: "a subtree source holding the other's source",
			a:    Pair{Old: "src/", New: "lib/"},
			b:    Pair{Old: "src/one.txt", New: "other.txt"},
			want: SameSource, x: "src", y: "src/one.txt",
		},
		{
			name: "two moves landing inside one another",
			a:    Pair{Old: "a.txt", New: "lib/a.txt"},
			b:    Pair{Old: "b.txt", New: "lib/"},
			want: SameDestination, x: "lib/a.txt", y: "lib",
		},
		{
			name: "the first move's destination is the second's source",
			a:    Pair{Old: "p.txt", New: "q.txt"},
			b:    Pair{Old: "q.txt", New: "r.txt"},
			want: Chained, x: "q.txt", y: "q.txt",
		},
		{
			name: "the second move's destination is the first's source",
			a:    Pair{Old: "q.txt", New: "r.txt"},
			b:    Pair{Old: "p.txt", New: "q.txt"},
			want: Chained, x: "q.txt", y: "q.txt",
		},
		{
			name: "a chain through a subtree prefix",
			a:    Pair{Old: "one/", New: "two/"},
			b:    Pair{Old: "two/deep.txt", New: "three.txt"},
			want: Chained, x: "two", y: "two/deep.txt",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, x, y := Overlap(c.a, c.b)
			if kind != c.want {
				t.Fatalf("Overlap(%+v, %+v) = %v, want %v", c.a, c.b, kind, c.want)
			}
			if kind != NoOverlap && (x != c.x || y != c.y) {
				t.Errorf("the refusal would name %q and %q, want %q and %q", x, y, c.x, c.y)
			}
		})
	}
}
