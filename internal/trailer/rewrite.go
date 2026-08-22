package trailer

import (
	"fmt"
	"strings"
)

// Rewriting a message that CARRIES move records.
//
// A history rewrite edits commit messages -- a scrub substitutes a pattern
// everywhere it appears, a file scrub erases a path from history -- and a
// message carrying a move record is not free text. The record's paths are
// C-quoted (cquote.go), so a substitution applied to the raw line can end a
// quoted region in the middle, absorb a delimiter, or leave a backslash with
// nothing after it: the record stops parsing, and what the rewrite produced is
// a claim nobody can read rather than a claim that was corrected.
//
// So the operations here work through the SAME encoder every writer uses.
// A path is decoded, transformed as a path, and re-encoded; a record that names
// an erased path is dropped as a whole line rather than edited into a
// half-truth. Nothing reaches into the middle of an encoded token.

// Names reports whether this record's pair names path.
//
// For the file form that is the two paths themselves and nothing else -- a
// file-form record says nothing about a descendant, which is the same rule the
// projection applies. For the subtree form it is anything at or under either
// prefix, on both sides: a record claiming `src/ -> lib/` references
// `lib/deep/one.txt` as surely as it references `lib` itself.
func (r Record) Names(path string) bool {
	if r.Subtree() {
		return under(r.OldPrefix(), path) || under(r.NewPrefix(), path)
	}
	return r.Old == path || r.New == path
}

// under reports whether path is prefix itself or sits inside it.
func under(prefix, path string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// Nests reports whether two paths are the same path or one is inside the
// other. It is the one answer to "do these two declarations speak about each
// other's paths", shared by the `--moved` overlap refusal and by `safegit mv`.
func Nests(a, b string) bool {
	if a == b {
		return true
	}
	return strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// Pair is one declared move's two paths, in either the file form or the subtree
// form. The trailing slash is trimmed wherever these paths are compared, so a
// caller may hand over whichever form it holds.
type Pair struct {
	Old string
	New string
}

func (p Pair) oldPrefix() string { return strings.TrimSuffix(p.Old, "/") }
func (p Pair) newPrefix() string { return strings.TrimSuffix(p.New, "/") }

// OverlapKind names how two declared moves speak about each other's paths.
type OverlapKind int

const (
	// NoOverlap: the two moves are about different paths entirely.
	NoOverlap OverlapKind = iota
	// SameSource: their source paths nest, so they state two fates for one
	// file.
	SameSource
	// SameDestination: their destination paths nest, so they describe a result
	// no move produces.
	SameDestination
	// Chained: one path is both a destination and a source, so the outcome
	// would depend on which move was performed first.
	Chained
)

// Overlap reports whether two declared moves can stand as one statement, and it
// is the ONE implementation of that question: `safegit mv` asks it of the pairs
// it is about to rename, and `--moved` asks it of the records a commit or an
// amend is about to write. The two spellings are the same declaration, so a
// command line either of them refuses is refused by both.
//
// Three ways two moves collide:
//
//   - NESTING ON THE SOURCE SIDE: `src/ -> lib/` alongside `src/one.txt -> x`
//     says two different things about one file. A reader could resolve that by
//     longest match; a writer guessing which the caller meant would be the
//     silent precedence rule this tool does not have.
//   - NESTING ON THE DESTINATION SIDE: two moves landing inside one another
//     describe a result no move produces.
//   - CHAINING: one path that is both a destination and a source, as in
//     `a -> b` beside `b -> c`. The result would depend on the order the moves
//     happened to be performed in, which is not something a caller stated.
//
// The two returned paths are the ones that nest, for a refusal to name.
func Overlap(a, b Pair) (kind OverlapKind, x, y string) {
	if Nests(a.oldPrefix(), b.oldPrefix()) {
		return SameSource, a.oldPrefix(), b.oldPrefix()
	}
	if Nests(a.newPrefix(), b.newPrefix()) {
		return SameDestination, a.newPrefix(), b.newPrefix()
	}
	if Nests(a.newPrefix(), b.oldPrefix()) {
		return Chained, a.newPrefix(), b.oldPrefix()
	}
	if Nests(b.newPrefix(), a.oldPrefix()) {
		return Chained, b.newPrefix(), a.oldPrefix()
	}
	return NoOverlap, "", ""
}

// RemoveMovedRecordsNaming drops every Moved: record whose pair names path,
// returning the new message and whether anything was dropped.
//
// It is what a rewrite that ERASES a path from history does to the records that
// reference it: the path is being removed from every tree, so a record still
// pointing at it is one more reference to the thing being erased. The record is
// removed rather than edited, because a record is a whole claim -- half of a
// move is not a smaller move, it is a malformed one.
//
// A retraction naming a removed record's id is left where it is. It names an id
// nothing carries any more, which the projection already treats as inert, and
// deleting it would be a second edit to a message for no gain.
func RemoveMovedRecordsNaming(message, path string) (string, bool) {
	body, block := SplitBodyTrailers(message)
	if block == "" {
		return message, false
	}

	var kept []string
	dropping := false
	changed := false
	for _, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
		if key, value, ok := splitTrailerLine(line); ok {
			dropping = false
			if key == MovedKey {
				if r, err := ParseRecord(value); err == nil && r.Names(path) {
					dropping = true
					changed = true
					continue
				}
			}
			kept = append(kept, line)
			continue
		}
		// A continuation line belongs to whichever trailer preceded it.
		if dropping {
			continue
		}
		kept = append(kept, line)
	}
	if !changed {
		return message, false
	}
	if len(kept) == 0 {
		// Every trailer was a record naming the path. The blank line that
		// separated the block from the body goes with it.
		trimmed := strings.TrimRight(body, "\n")
		if trimmed == "" {
			return "", true
		}
		return trimmed + "\n", true
	}
	return body + strings.Join(kept, "\n") + "\n", true
}

// RecordTransformError reports a transform that would turn a readable move
// record into one the decoder refuses.
//
// Re-encoding through the one encoder keeps the QUOTING readable whatever the
// substitution did, but the pair itself still has to be a move: two different
// paths, both or neither naming a subtree, neither of them empty. A replacement
// can map both sides onto one path, eat a subtree marker on one side only, or
// empty a token -- and the line that would be written is then a claim nobody
// can read, sitting inert in history where the rewrite meant to correct it.
//
// So the transform refuses rather than writing it, and the caller turns the
// refusal into a rewrite that never starts. The record is never edited into
// half a claim and never dropped in silence either: a record is a whole
// statement, and the only ways out are a replacement that keeps it one, erasing
// the path outright, or retracting the record in a commit of its own.
type RecordTransformError struct {
	// Line is the record's trailer line as the commit carries it, before the
	// transform.
	Line string
	// Result is the line the transform would have written.
	Result string
	// Old and New are the transformed pair.
	Old string
	New string
	// Err is ValidatePair's verdict on that pair.
	Err error
}

func (e *RecordTransformError) Error() string {
	return fmt.Sprintf("the transform turns %q into %q, which is not a move: %v", e.Line, e.Result, e.Err)
}

func (e *RecordTransformError) Unwrap() error { return e.Err }

// RewriteMessage applies a text transform to a commit message without breaking
// the quoting grammar of a move record.
//
// The body is transformed verbatim, which is what a pattern substitution has
// always done to a whole message. Inside a Moved: line only the DECODED path
// tokens are transformed, and the result is re-encoded through the one encoder,
// so the record that comes out parses however aggressively the transform
// rewrote the paths. The record's ID is never transformed: it is a generated
// name, not content, and a rewritten id names nothing.
//
// Every other trailer -- including a Moved: line that does not parse, which is
// somebody else's malformed line and not ours to normalize -- is transformed
// verbatim, exactly as before.
//
// One consequence worth stating: a pattern that matches only the ESCAPED
// spelling of a path (`\101` rather than `A`) matches nothing here, because the
// transform never sees the escaped form. The rewrite's own verification is what
// notices that the pattern survived, and it refuses the rewrite -- which is the
// honest outcome, and the alternative was a corrupt record.
//
// A transform whose result is no longer a MOVE is refused: the returned message
// is empty and the error is a *RecordTransformError, which the caller turns
// into a rewrite that never starts. See that type for why the record is neither
// written broken nor dropped in silence.
func RewriteMessage(message string, transform func(string) string) (string, error) {
	body, block := SplitBodyTrailers(message)
	if block == "" {
		return transform(message), nil
	}

	lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	for i, line := range lines {
		if key, value, ok := splitTrailerLine(line); ok && key == MovedKey {
			if r, err := ParseRecord(value); err == nil {
				old, new := transform(r.Old), transform(r.New)
				if old != r.Old || new != r.New {
					rewritten := Record{ID: r.ID, Old: old, New: new}
					if err := ValidatePair(old, new); err != nil {
						return "", &RecordTransformError{
							Line:   line,
							Result: RecordLine(rewritten),
							Old:    old,
							New:    new,
							Err:    err,
						}
					}
					lines[i] = RecordLine(rewritten)
				}
				continue
			}
		}
		lines[i] = transform(line)
	}
	return transform(body) + strings.Join(lines, "\n") + "\n", nil
}
