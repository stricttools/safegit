package trailer

import "strings"

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
func RewriteMessage(message string, transform func(string) string) string {
	body, block := SplitBodyTrailers(message)
	if block == "" {
		return transform(message)
	}

	lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	for i, line := range lines {
		if key, value, ok := splitTrailerLine(line); ok && key == MovedKey {
			if r, err := ParseRecord(value); err == nil {
				old, new := transform(r.Old), transform(r.New)
				if old != r.Old || new != r.New {
					lines[i] = RecordLine(Record{ID: r.ID, Old: old, New: new})
				}
				continue
			}
		}
		lines[i] = transform(line)
	}
	return transform(body) + strings.Join(lines, "\n") + "\n"
}
