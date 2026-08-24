package trailer

import (
	"fmt"
	"strings"
)

// C-quoting for the tokens a structured trailer value is made of.
//
// A path is a byte string. It may hold a space, a newline, a quote, a
// backslash, a byte no encoding claims, or nothing at all -- and every one of
// those has to survive a round trip through a commit message and back, because
// the reader on the other side resolves the result against a tree. So the
// encoder here is TOTAL: every byte string has exactly one encoded form, and
// decoding that form returns the original bytes.
//
// The quoting TRIGGER is exhaustive rather than minimal. A token is written
// bare only when reading it back cannot possibly be ambiguous:
//
//	trigger                        why
//	-----------------------------  --------------------------------------------
//	the empty token                a bare empty token is invisible in the value
//	a space (0x20)                 the grammar separates tokens on whitespace
//	any control byte (< 0x20)      includes tab, newline and carriage return,
//	                               which would end the trailer LINE
//	DEL (0x7f)                     a control byte with no printable form
//	any non-ASCII byte (>= 0x80)   a lone continuation byte is not text, and a
//	                               reader must not have to guess an encoding
//	a double quote                 it is the quoting delimiter
//	a backslash                    it is the escape character
//	the literal arrow token "->"   the pair separator; quoting it means a bare
//	                               token can never contain the separator's shape
//	a reserved origin keyword      the token slot after a record's id is where
//	                               an origin word will stand; a bare keyword
//	                               there is an origin, never a path
//
// The escape SPELLING is git's own (\n, \t, \" and friends, everything else in
// exactly three octal digits), because these values sit in commit messages that
// people read next to git's own C-quoted paths, and because three digits are
// what makes a literal digit following an escape unabsorbable.
//
// Reuse assessment, stated rather than assumed: internal/git carries an
// index-info quoter (quotePathForIndexInfo) written for a different contract --
// it is unconditional (git's --index-info unquotes only a value that STARTS
// with a quote, so quoting always is what removes the ambiguity there) and it
// has no decoder, because git does the decoding. This one is conditional (a
// human types these values on a command line, and `--moved 'a -> b'` must not
// have to be written `--moved '"a" -> "b"'`) and needs both directions. Sharing
// one function would mean one of the two contracts bending to the other's
// needs; sharing the escape SPELLING, which is what a reader actually sees,
// costs nothing and is what is shared.
const arrowToken = "->"

// originSlotKeywords are the words the grammar keeps for the ORIGIN slot -- the
// token position immediately after a record's id, which today may hold a bare
// path and will later hold a word saying how the claim was established.
//
// Reserving them is what keeps the grammar unambiguous at its own first token:
// a reader must never have to decide whether `observed` after the id is an
// origin or a path that happens to carry that name. The reservation costs the
// path nothing -- `"observed"` is still the path, the quoted spelling decodes
// back to the bare word, and only the ambiguous BARE spelling is given up.
//
// Membership is exact. A path named `observed/a.txt` or `observedly` is not a
// keyword and is written bare as before.
//
// The list is named for the SLOT rather than spelled `reservedOriginKeywords`
// because the specification test that pins this behavior owns that name in this
// package, deliberately: it writes the list out itself so a shrinking
// production list cannot hide behind an import.
var originSlotKeywords = map[string]struct{}{
	"observed": {},
	"declared": {},
	"derived":  {},
}

// isReservedOriginKeyword reports whether a token is exactly one of the words
// the origin slot reserves.
func isReservedOriginKeyword(s string) bool {
	_, ok := originSlotKeywords[s]
	return ok
}

// needsQuoting reports whether a token has to be written in quoted form. See
// the trigger table above.
func needsQuoting(s string) bool {
	if s == "" {
		return true
	}
	if strings.Contains(s, arrowToken) {
		return true
	}
	if isReservedOriginKeyword(s) {
		return true
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= 0x20 || c >= 0x7f || c == '"' || c == '\\' {
			return true
		}
	}
	return false
}

// quoteToken renders one token: bare when nothing in the table above triggers,
// and C-quoted (delimiters included) when something does.
func quoteToken(s string) string {
	if !needsQuoting(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\a':
			b.WriteString(`\a`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\v':
			b.WriteString(`\v`)
		default:
			if c < 0x20 || c >= 0x7f {
				// Three octal digits, always: a decoder reads at most three, so
				// a literal digit that follows can never be absorbed.
				fmt.Fprintf(&b, "\\%03o", c)
				continue
			}
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// unquoteToken reads one token back. A token in quoted form (it starts with a
// double quote) is unescaped; anything else is its own value, which is what
// lets a person type an ordinary path without quoting it.
func unquoteToken(s string) (string, error) {
	if !strings.HasPrefix(s, `"`) {
		return s, nil
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 1
	for i < len(s) {
		c := s[i]
		if c == '"' {
			if i != len(s)-1 {
				return "", fmt.Errorf("quoted value %s ends before its last character", s)
			}
			return b.String(), nil
		}
		if c != '\\' {
			b.WriteByte(c)
			i++
			continue
		}
		i++
		if i >= len(s) {
			return "", fmt.Errorf("quoted value %s ends in a backslash", s)
		}
		switch e := s[i]; e {
		case '"', '\\':
			b.WriteByte(e)
			i++
		case 'a':
			b.WriteByte('\a')
			i++
		case 'b':
			b.WriteByte('\b')
			i++
		case 'f':
			b.WriteByte('\f')
			i++
		case 'n':
			b.WriteByte('\n')
			i++
		case 'r':
			b.WriteByte('\r')
			i++
		case 't':
			b.WriteByte('\t')
			i++
		case 'v':
			b.WriteByte('\v')
			i++
		case '0', '1', '2', '3', '4', '5', '6', '7':
			// One to three octal digits. The encoder always writes three; a
			// shorter run is accepted because a hand-written value may carry
			// one, and reading at most three keeps a following literal digit
			// out of the escape.
			val := 0
			digits := 0
			for digits < 3 && i < len(s) && s[i] >= '0' && s[i] <= '7' {
				val = val*8 + int(s[i]-'0')
				i++
				digits++
			}
			if val > 0xff {
				return "", fmt.Errorf("octal escape in %s is larger than one byte", s)
			}
			b.WriteByte(byte(val))
		default:
			return "", fmt.Errorf("unknown escape \\%c in %s", e, s)
		}
	}
	return "", fmt.Errorf("quoted value %s has no closing quote", s)
}
