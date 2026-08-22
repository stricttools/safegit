package trailer

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// nastyNames are the byte strings a path grammar has to survive: whitespace of
// every kind, the quoting characters themselves, control bytes, the pair
// separator's own shape, and byte sequences no encoding claims.
var nastyNames = []string{
	"",
	"plain.txt",
	"src/pkg/file.go",
	"two words.txt",
	" leading",
	"trailing ",
	"tab\there",
	"newline\nhere",
	"carriage\rreturn",
	"bell\a",
	"vertical\vtab",
	"formfeed\f",
	"backspace\b",
	"nul\x00byte",
	"del\x7f",
	`quote"inside`,
	`back\slash`,
	`both"\and`,
	"arrow->inside",
	"a -> b",
	"->",
	" -> ",
	"héllo",
	"日本語/ファイル.txt",
	"emoji-\U0001f600",
	"\xff\xfe invalid utf8",
	"\x80lone continuation",
	"dir with space/sub\tdir/x",
	"\"already quoted\"",
	"digits\\1234",
	"octalish\x01" + "234",
}

func TestQuoteTokenRoundTripsNastyNames(t *testing.T) {
	for _, name := range nastyNames {
		quoted := quoteToken(name)
		back, err := unquoteToken(quoted)
		if err != nil {
			t.Errorf("unquoteToken(%q) from %q: %v", quoted, name, err)
			continue
		}
		if back != name {
			t.Errorf("round trip of %q via %q gave %q", name, quoted, back)
		}
	}
}

// TestQuotingTriggerIsExhaustive walks every byte value: a token holding it is
// either quoted, or bare AND made only of bytes that cannot be ambiguous.
func TestQuotingTriggerIsExhaustive(t *testing.T) {
	for b := 0; b < 256; b++ {
		name := "a" + string([]byte{byte(b)}) + "z"
		quoted := quoteToken(name)
		isQuoted := strings.HasPrefix(quoted, `"`)
		mustQuote := b <= 0x20 || b >= 0x7f || byte(b) == '"' || byte(b) == '\\'
		if mustQuote && !isQuoted {
			t.Errorf("byte 0x%02x was written bare as %q", b, quoted)
		}
		if !mustQuote && isQuoted {
			t.Errorf("byte 0x%02x was quoted as %q with no trigger for it", b, quoted)
		}
		back, err := unquoteToken(quoted)
		if err != nil || back != name {
			t.Errorf("byte 0x%02x: round trip of %q gave (%q, %v)", b, name, back, err)
		}
	}
	// The arrow token is a trigger of its own: it holds no byte that needs
	// quoting, and is quoted anyway so a bare token can never carry the
	// separator's shape.
	if q := quoteToken("a->b"); !strings.HasPrefix(q, `"`) {
		t.Errorf("a token containing the arrow was written bare as %q", q)
	}
	// So is the empty token, which is invisible written bare.
	if q := quoteToken(""); q != `""` {
		t.Errorf("the empty token encoded as %q, want %q", q, `""`)
	}
}

// TestTokenRoundTripsRandomByteStrings is the byte-completeness property: ANY
// byte string, with no exceptions and no semantics attached, survives the
// encoder and the decoder unchanged.
func TestTokenRoundTripsRandomByteStrings(t *testing.T) {
	// A fixed seed: this is a property test, not a flake generator. A failure
	// has to reproduce for whoever reads it.
	rng := rand.New(rand.NewSource(0x5AFE617))
	for i := 0; i < 8000; i++ {
		name := randomName(rng)
		quoted := quoteToken(name)
		back, err := unquoteToken(quoted)
		if err != nil {
			t.Fatalf("unquoteToken(%q) from %q: %v", quoted, name, err)
		}
		if back != name {
			t.Fatalf("round trip of %q via %q gave %q", name, quoted, back)
		}
	}
}

// TestPairRoundTripsRandomByteStrings is the same property one level up, over
// pairs the grammar accepts: the two sides come back separated exactly where
// they were separated, whatever bytes they hold.
func TestPairRoundTripsRandomByteStrings(t *testing.T) {
	rng := rand.New(rand.NewSource(0x5AFE617))
	for i := 0; i < 4000; i++ {
		old := randomName(rng)
		new := randomName(rng)
		// The grammar's own rules, which are not what this property is about:
		// a pair names two different paths, and the subtree form marks BOTH
		// sides. Pairs that break them are refused by design and tested for
		// separately.
		if old == new || old == "" || new == "" ||
			strings.HasSuffix(old, "/") != strings.HasSuffix(new, "/") ||
			old == "/" || new == "/" {
			continue
		}
		encoded := EncodePair(old, new)
		gotOld, gotNew, err := ParsePair(encoded)
		if err != nil {
			t.Fatalf("ParsePair(%q) from (%q, %q): %v", encoded, old, new, err)
		}
		if gotOld != old || gotNew != new {
			t.Fatalf("round trip of (%q, %q) via %q gave (%q, %q)", old, new, encoded, gotOld, gotNew)
		}
	}
}

// randomName draws a byte string from the whole byte range, with the bytes the
// grammar cares about over-represented so the interesting cases actually occur.
func randomName(rng *rand.Rand) string {
	interesting := []byte{' ', '\t', '\n', '\r', '"', '\\', '-', '>', '/', 0x00, 0x7f, 0x80, 0xff}
	n := rng.Intn(12)
	b := make([]byte, n)
	for i := range b {
		switch rng.Intn(3) {
		case 0:
			b[i] = interesting[rng.Intn(len(interesting))]
		case 1:
			b[i] = byte('a' + rng.Intn(26))
		default:
			b[i] = byte(rng.Intn(256))
		}
	}
	return string(b)
}

func TestParsePairAcceptsUnquotedEverydayPaths(t *testing.T) {
	// The grammar a person types. Nothing here needs quoting, including the
	// non-ASCII path -- the ENCODER quotes it, but the decoder must not demand
	// it, or a hand-written --moved would be refused for holding an accent.
	cases := []struct{ in, old, new string }{
		{"a.txt -> b.txt", "a.txt", "b.txt"},
		{"src/a.go -> src/b.go", "src/a.go", "src/b.go"},
		{"héllo.txt -> wörld.txt", "héllo.txt", "wörld.txt"},
		{"old/ -> new/", "old/", "new/"},
		{"  a.txt  ->  b.txt  ", "a.txt", "b.txt"},
		{`"two words.txt" -> plain.txt`, "two words.txt", "plain.txt"},
		{`a.txt -> "with \" quote.txt"`, "a.txt", `with " quote.txt`},
		{`"a -> b" -> c`, "a -> b", "c"},
	}
	for _, c := range cases {
		old, new, err := ParsePair(c.in)
		if err != nil {
			t.Errorf("ParsePair(%q): %v", c.in, err)
			continue
		}
		if old != c.old || new != c.new {
			t.Errorf("ParsePair(%q) = (%q, %q), want (%q, %q)", c.in, old, new, c.old, c.new)
		}
	}
}

func TestParsePairRefusals(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a.txt", "needs two paths"},
		{"a -> b -> c", "separators"},
		{`"unterminated -> b`, "never closed"},
		{"a -> ", "leaves one of them empty"},
		{"old/ -> new", "ends only one"},
		{"a.txt -> a.txt", "names one path twice"},
	}
	for _, c := range cases {
		_, _, err := ParsePair(c.in)
		if err == nil {
			t.Errorf("ParsePair(%q) was accepted", c.in)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("ParsePair(%q) said %q, wanted it to mention %q", c.in, err, c.want)
		}
	}
}

func TestRecordRoundTripsThroughItsTrailerLine(t *testing.T) {
	for _, name := range nastyNames {
		if name == "" {
			continue
		}
		r, err := NewRecord(name, "dest/"+name+".moved")
		if err != nil {
			t.Fatalf("NewRecord(%q): %v", name, err)
		}
		line := RecordLine(r)
		if strings.Contains(line, "\n") {
			t.Fatalf("record line for %q holds a newline: %q", name, line)
		}
		key, value, ok := splitTrailerLine(line)
		if !ok || key != MovedKey {
			t.Fatalf("record line %q did not read back as a %s trailer", line, MovedKey)
		}
		back, err := ParseRecord(value)
		if err != nil {
			t.Fatalf("ParseRecord(%q): %v", value, err)
		}
		if back != r {
			t.Fatalf("record %+v round-tripped to %+v", r, back)
		}
	}
}

func TestParseRecordRefusalsAndIDPlacement(t *testing.T) {
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, err := ParseRecord("a.txt -> b.txt"); err == nil {
		t.Error("a record with no id was accepted")
	}
	if _, err := ParseRecord("not-an-id a.txt -> b.txt"); err == nil {
		t.Error("a record with a malformed id was accepted")
	}
	if _, err := ParseRecord(id); err == nil {
		t.Error("a record that is nothing but an id was accepted")
	}
	// The id is the LEADING token of the value, so the whole record is one
	// trailer line and cannot be separated from its id by anything.
	r, err := ParseRecord(id + " a.txt -> b.txt")
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	if r.ID != id || r.Old != "a.txt" || r.New != "b.txt" {
		t.Errorf("parsed %+v", r)
	}
}

func TestSubtreeForm(t *testing.T) {
	r, err := NewRecord("src/old/", "src/new/")
	if err != nil {
		t.Fatalf("NewRecord: %v", err)
	}
	if !r.Subtree() {
		t.Error("a pair with trailing slashes is not reading as a subtree record")
	}
	if r.OldPrefix() != "src/old" || r.NewPrefix() != "src/new" {
		t.Errorf("prefixes are %q and %q", r.OldPrefix(), r.NewPrefix())
	}
	file, err := NewRecord("src/old", "src/new")
	if err != nil {
		t.Fatalf("NewRecord: %v", err)
	}
	if file.Subtree() {
		t.Error("a pair with no trailing slashes is reading as a subtree record")
	}
}

func TestTrailersParsesKeysValuesAndContinuations(t *testing.T) {
	msg := "subject\n\nbody line\n\nKey-One: first\nKey-Two: second\n  continued\nKey-Three: third\n"
	got := Trailers(msg)
	want := []KV{
		{"Key-One", "first"},
		{"Key-Two", "second\n  continued"},
		{"Key-Three", "third"},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d trailers, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("trailer %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(Trailers("no trailers here")) != 0 {
		t.Error("a message with no trailer block produced trailers")
	}
}

func TestReadMovesSeparatesRecordsRetractionsAndGarbage(t *testing.T) {
	idA, _ := NewID()
	idB, _ := NewID()
	msg := "subject\n\n" +
		MovedKey + ": " + idA + " a.txt -> b.txt\n" +
		MovedRetractKey + ": " + idB + "\n" +
		MovedKey + ": this is not a record\n" +
		MovedRetractKey + ": neither is this\n" +
		"Unrelated-Key: value\n"
	m := ReadMoves(msg)
	if len(m.Records) != 1 || m.Records[0].ID != idA || m.Records[0].New != "b.txt" {
		t.Errorf("records = %+v", m.Records)
	}
	if len(m.Retractions) != 1 || m.Retractions[0] != idB {
		t.Errorf("retractions = %+v", m.Retractions)
	}
	if len(m.Malformed) != 2 {
		t.Errorf("malformed = %+v, want the two unparseable values", m.Malformed)
	}
	// An unrelated trailer is neither a record nor garbage: it is not this
	// package's business at all.
	for _, bad := range m.Malformed {
		if strings.Contains(bad, "Unrelated-Key") {
			t.Errorf("an unrelated trailer was reported as a malformed record: %q", bad)
		}
	}
}

func TestMovedLinesCarriesRecordsVerbatim(t *testing.T) {
	id, _ := NewID()
	other, _ := NewID()
	msg := "subject\n\nbody\n\n" +
		"Signed-off-by: Someone <s@example.com>\n" +
		MovedKey + ": " + id + " \"two words.txt\" -> plain.txt\n" +
		MovedRetractKey + ": " + other + "\n" +
		"Claude-Code-Session-Id: abc\n"
	lines := MovedLines(msg)
	want := []string{
		MovedKey + ": " + id + " \"two words.txt\" -> plain.txt",
		MovedRetractKey + ": " + other,
	}
	if fmt.Sprint(lines) != fmt.Sprint(want) {
		t.Errorf("MovedLines = %q, want %q", lines, want)
	}
	if len(MovedLines("subject\n\nno trailers")) != 0 {
		t.Error("a message with no trailers produced move lines")
	}
}
