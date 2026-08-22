package trailer

import (
	"fmt"
	"strings"
)

// Move records: what a commit says it moved, written where it survives every
// rewrite, every clone and every tool that has never heard of safegit -- the
// commit message.
//
// A record is a CLAIM. It is written by whoever performed the move, from what
// that caller declared rather than from anything guessed out of blob equality,
// and it is read back against the trees the repository actually holds (see
// project.go). Nothing about the format asks the reader to trust it.
//
// The grammar is one line:
//
//	Moved: <id> <old> -> <new>
//
// with each path token quoted when it needs to be (cquote.go) and a trailing
// slash on BOTH sides meaning "everything under this prefix". The retraction is
// the same shape with nothing but an id:
//
//	Moved-Retract: <id>
//
// The id is a LEADING TOKEN inside the value, not a key of its own. A separate
// `Moved-Id:` line would have to stay adjacent to its `Moved:` line to mean
// anything, and adjacency is the one property a trailer block does not have: a
// rewriting hook, a rebase, an editor or a later amend may reorder, wrap or
// interleave the lines, and an id that has drifted away from its record names
// nothing at all.
const (
	// MovedKey is the trailer key one move record is written under.
	MovedKey = "Moved"
	// MovedRetractKey is the trailer key a retraction is written under. Its
	// value is the id of the record being retracted and nothing else.
	//
	// Retraction is the ONLY correction: a record already written is never
	// edited, because a record only exists on a commit and editing that commit
	// rewrites history. A replacement is a retraction plus a new record in one
	// commit.
	MovedRetractKey = "Moved-Retract"
)

// Record is one declared move.
//
// Old and New are canonical repo-relative paths. A trailing slash on both marks
// the SUBTREE form, which claims a move of everything under the prefix rather
// than of one file: the per-file answers are derived when the record is read
// and validated against the trees then, so a record written for a directory
// stays one line however many files the directory holds and however many of
// them a later reader finds.
type Record struct {
	ID  string
	Old string
	New string
}

// Subtree reports whether this record claims a whole prefix rather than one
// path.
func (r Record) Subtree() bool { return strings.HasSuffix(r.Old, "/") }

// OldPrefix is Old without the subtree form's trailing slash.
func (r Record) OldPrefix() string { return strings.TrimSuffix(r.Old, "/") }

// NewPrefix is New without the subtree form's trailing slash.
func (r Record) NewPrefix() string { return strings.TrimSuffix(r.New, "/") }

// arrowSeparator is the pair separator as it appears between two tokens.
const arrowSeparator = " " + arrowToken + " "

// EncodePair renders one "old -> new" token pair: the grammar `--moved` takes,
// the grammar `safegit mv` takes, and the tail of every written record. One
// encoder, so the three cannot drift apart.
func EncodePair(old, new string) string {
	return quoteToken(old) + arrowSeparator + quoteToken(new)
}

// ParsePair reads one "old -> new" token pair.
//
// The separator is found OUTSIDE quoted regions, so a path that holds the
// arrow's own shape parses correctly once it is quoted -- and a value carrying
// two unquoted separators is refused rather than split at a guess.
//
// An unquoted token is taken verbatim after its surrounding spaces are
// trimmed, which is what lets a person type `--moved 'src/a.go -> src/b.go'`
// (and even a non-ASCII path) without quoting anything. A token that really
// does carry a space, a quote, a backslash or a control byte has to be quoted,
// because nothing else could tell the two sides apart. One consequence worth
// stating: an unquoted token containing a lone double quote opens a quoted
// region that never closes, and the refusal says so rather than guessing.
func ParsePair(s string) (old, new string, err error) {
	left, right, err := splitOnArrow(s)
	if err != nil {
		return "", "", err
	}
	old, err = unquoteToken(strings.TrimSpace(left))
	if err != nil {
		return "", "", fmt.Errorf("left side of %q: %w", s, err)
	}
	new, err = unquoteToken(strings.TrimSpace(right))
	if err != nil {
		return "", "", fmt.Errorf("right side of %q: %w", s, err)
	}
	if err := ValidatePair(old, new); err != nil {
		return "", "", err
	}
	return old, new, nil
}

// ValidatePair applies the grammar's own rules to a decoded pair: the rules
// that hold wherever the pair came from, as opposed to the repository-dependent
// ones (is the old path tracked, is the new one there) that only a caller
// holding a tree can answer.
func ValidatePair(old, new string) error {
	if old == "" || new == "" {
		return fmt.Errorf("a move names two paths; %q -> %q leaves one of them empty", old, new)
	}
	oldSubtree := strings.HasSuffix(old, "/")
	newSubtree := strings.HasSuffix(new, "/")
	if oldSubtree != newSubtree {
		return fmt.Errorf("a subtree move ends BOTH paths with a slash; %q -> %q ends only one, "+
			"so it is neither a file move nor a subtree move", old, new)
	}
	if old == new {
		return fmt.Errorf("%q -> %q names one path twice; a move goes from one path to another", old, new)
	}
	if oldSubtree && (old == "/" || new == "/") {
		return fmt.Errorf("a subtree move names a prefix; %q -> %q names the repository root", old, new)
	}
	return nil
}

// splitOnArrow finds the one separator that is not inside a quoted token.
func splitOnArrow(s string) (left, right string, err error) {
	var at []int
	inQuote := false
	for i := 0; i < len(s); {
		c := s[i]
		if inQuote {
			if c == '\\' {
				i += 2
				continue
			}
			if c == '"' {
				inQuote = false
			}
			i++
			continue
		}
		if c == '"' {
			inQuote = true
			i++
			continue
		}
		if strings.HasPrefix(s[i:], arrowSeparator) {
			at = append(at, i)
			i += len(arrowSeparator)
			continue
		}
		i++
	}
	if inQuote {
		return "", "", fmt.Errorf("%q opens a quoted path that is never closed", s)
	}
	switch len(at) {
	case 0:
		return "", "", fmt.Errorf("%q is not a move: it needs two paths separated by %q, as in 'old%snew'",
			s, arrowSeparator, arrowSeparator)
	case 1:
		return s[:at[0]], s[at[0]+len(arrowSeparator):], nil
	default:
		return "", "", fmt.Errorf("%q holds %d unquoted %q separators; quote a path that contains one",
			s, len(at), arrowSeparator)
	}
}

// EncodeRecord renders the VALUE of one Moved: trailer -- the id, then the
// pair.
func EncodeRecord(r Record) string {
	return r.ID + " " + EncodePair(r.Old, r.New)
}

// RecordLine renders a whole Moved: trailer line, without its newline.
func RecordLine(r Record) string {
	return MovedKey + ": " + EncodeRecord(r)
}

// RetractLine renders a whole Moved-Retract: trailer line, without its newline.
func RetractLine(id string) string {
	return MovedRetractKey + ": " + id
}

// ParseRecord reads the value of one Moved: trailer.
func ParseRecord(value string) (Record, error) {
	space := strings.IndexByte(value, ' ')
	if space < 0 {
		return Record{}, fmt.Errorf("move record %q carries no id followed by a path pair", value)
	}
	id := value[:space]
	if !ValidID(id) {
		return Record{}, fmt.Errorf("move record %q begins with %q, which is not a %d-character record id",
			value, id, idLength)
	}
	old, new, err := ParsePair(value[space+1:])
	if err != nil {
		return Record{}, err
	}
	return Record{ID: id, Old: old, New: new}, nil
}

// NewRecord mints an id and returns the record for one declared pair. The pair
// is validated first, so a record never exists for a pair the grammar refuses.
func NewRecord(old, new string) (Record, error) {
	if err := ValidatePair(old, new); err != nil {
		return Record{}, err
	}
	id, err := NewID()
	if err != nil {
		return Record{}, err
	}
	return Record{ID: id, Old: old, New: new}, nil
}

// Moves is everything one commit message declares about moves.
//
// Malformed carries the values under the two keys that do not parse, verbatim.
// They are neither dropped in silence nor turned into a hard error: a reader
// asking about a path must not be stopped by an unrelated commit somebody's
// tool mangled, and a tool auditing the repository must be able to find it.
type Moves struct {
	Records     []Record
	Retractions []string
	Malformed   []string
}

// ReadMoves parses one commit message's move declarations.
func ReadMoves(message string) Moves {
	var m Moves
	for _, kv := range Trailers(message) {
		switch kv.Key {
		case MovedKey:
			record, err := ParseRecord(kv.Value)
			if err != nil {
				m.Malformed = append(m.Malformed, kv.Key+": "+kv.Value)
				continue
			}
			m.Records = append(m.Records, record)
		case MovedRetractKey:
			id := strings.TrimSpace(kv.Value)
			if !ValidID(id) {
				m.Malformed = append(m.Malformed, kv.Key+": "+kv.Value)
				continue
			}
			m.Retractions = append(m.Retractions, id)
		}
	}
	return m
}

// MovedLines returns the message's Moved: and Moved-Retract: trailer lines
// exactly as they are written, continuation lines included.
//
// It is what an amend or a reword re-appends when a new -m replaces the
// message: dropping a record is a RETRACTION, never the side effect of
// rewording the commit that carries it, so the lines are carried across
// verbatim rather than re-encoded from a parse (which would silently normalize
// -- or lose -- a line this version does not understand).
func MovedLines(message string) []string {
	_, block := SplitBodyTrailers(message)
	if block == "" {
		return nil
	}
	var out []string
	keeping := false
	for _, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
		if key, _, ok := splitTrailerLine(line); ok {
			keeping = key == MovedKey || key == MovedRetractKey
			if keeping {
				out = append(out, line)
			}
			continue
		}
		// A continuation line belongs to whichever trailer preceded it.
		if keeping {
			out = append(out, line)
		}
	}
	return out
}
