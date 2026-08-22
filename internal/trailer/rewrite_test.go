package trailer

import (
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
	got := RewriteMessage(message, func(s string) string {
		return pat.ReplaceAllString(s, "X")
	})

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
	got := RewriteMessage(message, func(s string) string {
		return strings.ReplaceAll(s, "SECRET", "X")
	})
	if got != "subject\n\nSigned-off-by: A X <a@b>\n" {
		t.Errorf("non-move trailer rewrite is %q", got)
	}

	// A message with no trailer block is transformed whole, exactly as it was
	// before the trailer split existed.
	plain := RewriteMessage("only a SECRET subject\n", func(s string) string {
		return strings.ReplaceAll(s, "SECRET", "X")
	})
	if plain != "only a X subject\n" {
		t.Errorf("plain message rewrite is %q", plain)
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
