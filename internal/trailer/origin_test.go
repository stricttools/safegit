package trailer

import (
	"strings"
	"testing"
)

// The ORIGIN token: how a record says who established the claim it carries.
//
// A record is a claim, and until now every record was the same kind of claim --
// a person stating a move. safegit now mints records of its own, from what a
// commit's delta witnesses, and a reader has to be able to tell the two apart:
// the person's claim is a statement of intent, and safegit's is a reading of
// objects that the trees can be re-asked at any time.
//
// The token stands in the slot the grammar reserved for it, immediately after
// the id:
//
//	Moved: <id> observed <old> -> <new>
//
// Two rules make it total, and both are pinned below:
//
//   - ABSENCE MEANS DECLARED. Every record ever written carries no token, and
//     every one of them is a person's claim; a record safegit derived carries
//     `observed`. Nothing has to be migrated, and no reader has to guess.
//   - `declared` AND `derived` STAY RESERVED. Only `observed` becomes legal in
//     that slot. The other two are refused exactly as before -- the grammar
//     keeps the words, so a later version can use them without the ambiguity
//     the reservation exists to prevent.

// TestObservedRecordsCarryTheirToken: the encoder writes the token for an
// observed record and writes nothing extra for a declared one, so the shape of
// every record already in a repository is unchanged.
func TestObservedRecordsCarryTheirToken(t *testing.T) {
	observed := Record{ID: aValidRecordID, Old: "a.txt", New: "b.txt", Origin: OriginObserved}
	if got, want := EncodeRecord(observed), aValidRecordID+" observed a.txt -> b.txt"; got != want {
		t.Errorf("EncodeRecord(observed) = %q, want %q", got, want)
	}

	declared := Record{ID: aValidRecordID, Old: "a.txt", New: "b.txt"}
	if got, want := EncodeRecord(declared), aValidRecordID+" a.txt -> b.txt"; got != want {
		t.Errorf("EncodeRecord(declared) = %q, want %q", got, want)
	}
	if declared.Origin != OriginDeclared {
		t.Errorf("the zero Origin is %q, want the declared one (%q)", declared.Origin, OriginDeclared)
	}
}

// TestOriginRoundTripsThroughTheParser: what the encoder writes, the parser
// reads back -- token and paths both.
func TestOriginRoundTripsThroughTheParser(t *testing.T) {
	for _, origin := range []Origin{OriginDeclared, OriginObserved} {
		t.Run(string(origin.Name()), func(t *testing.T) {
			want := Record{ID: aValidRecordID, Old: "src/a.txt", New: "dst/b.txt", Origin: origin}
			got, err := ParseRecord(EncodeRecord(want))
			if err != nil {
				t.Fatalf("ParseRecord(%q): %v", EncodeRecord(want), err)
			}
			if got != want {
				t.Errorf("round trip produced %+v, want %+v", got, want)
			}
		})
	}
}

// TestOriginSurvivesReadMoves: the token reaches the reader that classifies a
// whole commit message, which is what every consumer of records actually calls.
func TestOriginSurvivesReadMoves(t *testing.T) {
	message := "subject\n\n" +
		RecordLine(Record{ID: aValidRecordID, Old: "a.txt", New: "b.txt", Origin: OriginObserved}) + "\n" +
		RecordLine(Record{ID: "01JQ0000000000000000000001", Old: "c.txt", New: "d.txt"}) + "\n"

	moves := ReadMoves(message)
	if len(moves.Records) != 2 {
		t.Fatalf("ReadMoves found %d records, want 2: %+v (malformed %+v)", len(moves.Records), moves.Records, moves.Malformed)
	}
	if moves.Records[0].Origin != OriginObserved {
		t.Errorf("the observed record came back as %q", moves.Records[0].Origin)
	}
	if moves.Records[1].Origin != OriginDeclared {
		t.Errorf("the declared record came back as %q", moves.Records[1].Origin)
	}
}

// TestOriginNamesItself: a payload and a message both need a WORD for the
// origin, and an absent token has to be named too -- absence is a value, not a
// missing one.
func TestOriginNamesItself(t *testing.T) {
	if got, want := OriginDeclared.Name(), "declared"; got != want {
		t.Errorf("OriginDeclared.Name() = %q, want %q", got, want)
	}
	if got, want := OriginObserved.Name(), "observed"; got != want {
		t.Errorf("OriginObserved.Name() = %q, want %q", got, want)
	}
}

// TestObservedPathNamedObservedIsStillWritable: the reservation cost the path
// nothing, and the origin token does not change that. `"observed"` quoted is
// the path; the bare word after the id is the origin. A record can carry both
// at once and still read back as itself.
func TestObservedPathNamedObservedIsStillWritable(t *testing.T) {
	want := Record{ID: aValidRecordID, Old: "observed", New: "b.txt", Origin: OriginObserved}
	line := EncodeRecord(want)
	if got, expect := line, aValidRecordID+` observed "observed" -> b.txt`; got != expect {
		t.Errorf("EncodeRecord = %q, want %q", got, expect)
	}
	got, err := ParseRecord(line)
	if err != nil {
		t.Fatalf("ParseRecord(%q): %v", line, err)
	}
	if got != want {
		t.Errorf("round trip produced %+v, want %+v", got, want)
	}
}

// TestRewriteMessageKeepsTheOriginToken is the pin that keeps every history
// rewrite from quietly demoting safegit's own records to declared ones: a
// scrub transforms the DECODED paths and re-encodes the record, and a re-encode
// that dropped the token would rewrite the claim's origin along with the path.
func TestRewriteMessageKeepsTheOriginToken(t *testing.T) {
	message := "subject\n\n" +
		RecordLine(Record{ID: aValidRecordID, Old: "secret/a.txt", New: "secret/b.txt", Origin: OriginObserved}) + "\n"

	got, err := RewriteMessage(message, func(s string) string {
		return strings.ReplaceAll(s, "secret", "public")
	})
	if err != nil {
		t.Fatalf("RewriteMessage: %v", err)
	}

	moves := ReadMoves(got)
	if len(moves.Records) != 1 {
		t.Fatalf("the rewrite produced %d records, want 1; message:\n%s", len(moves.Records), got)
	}
	r := moves.Records[0]
	if r.Origin != OriginObserved {
		t.Errorf("the rewrite dropped the origin token: %q; message:\n%s", r.Origin, got)
	}
	if r.Old != "public/a.txt" || r.New != "public/b.txt" {
		t.Errorf("the rewrite produced %q -> %q; message:\n%s", r.Old, r.New, got)
	}
}
