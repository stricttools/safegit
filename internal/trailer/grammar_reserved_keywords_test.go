package trailer

import (
	"strings"
	"testing"
)

// FINDING 4 (second-campaign adversarial review), ruled to ride the new plan:
// the move-record grammar reserves three origin keywords.
//
// A record's value is `<id> <old> -> <new>` today, and the plan adds an ORIGIN
// token to it: how the claim was established -- observed, declared, derived.
// That token goes in the slot immediately after the id, which is exactly where
// a bare path token can also stand today. So the three words have to be
// reserved BEFORE the slot exists, and reserved rather than guessed at:
//
//   - A reader must never have to decide whether the token after the id is an
//     origin or a path whose name happens to be `observed`. If both spellings
//     were legal the grammar would be ambiguous at its own first token, and no
//     amount of later disambiguation could repair records already written.
//   - A path that really is named `observed` is a legitimate path and must stay
//     writable. It stays writable by being QUOTED: `"observed"` is a path,
//     `observed` is an origin, and nothing else has to change.
//   - The refusal must be a refusal, not a reinterpretation. A hand-written line
//     carrying a bare keyword where a path is expected is classified Malformed,
//     which is the grammar's existing way of saying "this line is not something
//     I will act on" without discarding it or failing the whole read.
//
// Both halves are RED today: needsQuoting answers false for a plain ASCII word,
// so the encoder writes the keyword bare and produces exactly the ambiguous
// spelling; and the parser, being lenient about unquoted tokens, reads a bare
// keyword as a path.
//
// RULED TARGET: reservedOriginKeywords is the grammar's own list; quoting is
// triggered by membership in it, and the parser refuses a bare member where a
// path token is expected.

// reservedOriginKeywords is the set the origin slot will draw from. It is
// spelled out here rather than imported from the production code on purpose:
// these tests are the specification, and a test that read the implementation's
// own list could not detect the list shrinking.
var reservedOriginKeywords = []string{"observed", "declared", "derived"}

// aValidRecordID is a well-formed id in the Crockford alphabet, written out so
// the malformed lines below are hand-written in full, the way a foreign tool or
// a person would write them.
const aValidRecordID = "01JQ0000000000000000000000"

// TestReservedOriginKeywordsAreQuotedAsPaths: a path whose entire name is a
// reserved keyword must be written in its quoted form, in either position of a
// pair and in a whole record, so nothing safegit itself writes is ambiguous.
func TestReservedOriginKeywordsAreQuotedAsPaths(t *testing.T) {
	if !ValidID(aValidRecordID) {
		t.Fatalf("the test's own id %q is not well formed", aValidRecordID)
	}

	for _, keyword := range reservedOriginKeywords {
		t.Run(keyword, func(t *testing.T) {
			quoted := `"` + keyword + `"`

			if got := quoteToken(keyword); got != quoted {
				t.Errorf("quoteToken(%q) = %q, want the quoted spelling %q", keyword, got, quoted)
			}

			// Both positions: the old path is the one that shares a slot with
			// the coming origin token, and the new path is quoted with it so
			// the two sides of a pair are never spelled by different rules.
			if got, want := EncodePair(keyword, "b.txt"), quoted+" -> b.txt"; got != want {
				t.Errorf("EncodePair(%q, \"b.txt\") = %q, want %q", keyword, got, want)
			}
			if got, want := EncodePair("a.txt", keyword), "a.txt -> "+quoted; got != want {
				t.Errorf("EncodePair(\"a.txt\", %q) = %q, want %q", keyword, got, want)
			}

			record := Record{ID: aValidRecordID, Old: keyword, New: "b.txt"}
			if got, want := EncodeRecord(record), aValidRecordID+" "+quoted+" -> b.txt"; got != want {
				t.Errorf("EncodeRecord(%+v) = %q, want %q", record, got, want)
			}

			// The quoted spelling still means the bare word: reserving the
			// keyword must not cost the path its name.
			old, new, err := ParsePair(quoted + " -> b.txt")
			if err != nil {
				t.Fatalf("ParsePair(%q): %v", quoted+" -> b.txt", err)
			}
			if old != keyword || new != "b.txt" {
				t.Errorf("the quoted keyword decoded to %q -> %q, want %q -> \"b.txt\"", old, new, keyword)
			}
		})
	}
}

// TestBareReservedKeywordAfterTheIdIsMalformed: a hand-written line that puts a
// bare keyword where the origin token will go is not a record. Two shapes, and
// the second is the one that matters most: `<id> observed -> y` parses today as
// a move FROM a path called observed, which is precisely the reading the
// reservation exists to make impossible.
func TestBareReservedKeywordAfterTheIdIsMalformed(t *testing.T) {
	for _, keyword := range reservedOriginKeywords {
		for _, tc := range []struct {
			name  string
			value string
		}{
			{"keyword before a pair", keyword + " x.txt -> y.txt"},
			{"keyword as the old path", keyword + " -> y.txt"},
		} {
			t.Run(keyword+"/"+tc.name, func(t *testing.T) {
				value := aValidRecordID + " " + tc.value

				if record, err := ParseRecord(value); err == nil {
					t.Errorf("ParseRecord(%q) returned the record %+v; a bare reserved keyword must be refused, never read as a path", value, record)
				}

				// And the refusal reaches the reader that classifies whole
				// commit messages: the line is kept, verbatim, as Malformed.
				msg := "subject\n\n" + MovedKey + ": " + value + "\n"
				moves := ReadMoves(msg)
				if len(moves.Records) != 0 {
					t.Errorf("ReadMoves accepted %q as a record: %+v", value, moves.Records)
				}
				if len(moves.Malformed) != 1 || !strings.Contains(moves.Malformed[0], keyword) {
					t.Errorf("the line must be classified Malformed and kept verbatim, got %+v", moves.Malformed)
				}
			})
		}
	}
}
