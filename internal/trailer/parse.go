package trailer

import "strings"

// Reading a trailer block as key-value pairs.
//
// SplitBodyTrailers finds WHERE the trailer block is; this is what says what is
// IN it. The two are separate because they answer different questions and
// because the split is the harder one: a trailer block is defined by its
// position in the message, and only once it has been located does asking a line
// for its key mean anything.

// KV is one trailer line: its key and everything after the "Key: ".
//
// A continuation line -- an indented line following a trailer -- is appended to
// the preceding value with its newline and its indentation preserved, which is
// how git reads one too. safegit's own structured trailers never produce one (a
// path holding a newline is escaped, not wrapped), so a continuation in
// practice comes from somebody else's tool and is carried rather than
// interpreted.
type KV struct {
	Key   string
	Value string
}

// Trailers parses the trailer block of a commit message. A message with no
// trailer block yields nothing.
func Trailers(message string) []KV {
	_, block := SplitBodyTrailers(message)
	return ParseTrailerBlock(block)
}

// ParseTrailerBlock parses an already-located trailer block.
func ParseTrailerBlock(block string) []KV {
	if block == "" {
		return nil
	}
	var out []KV
	for _, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
		if key, value, ok := splitTrailerLine(line); ok {
			out = append(out, KV{Key: key, Value: value})
			continue
		}
		if len(out) > 0 {
			out[len(out)-1].Value += "\n" + line
		}
	}
	return out
}

// splitTrailerLine reads one line as a trailer, reporting whether it is one at
// all. The recognized shape is trailerLine's: a key of letters, digits and
// hyphens, a colon, and whitespace before the value.
func splitTrailerLine(line string) (key, value string, ok bool) {
	if !trailerLine.MatchString(line) {
		return "", "", false
	}
	colon := strings.IndexByte(line, ':')
	return line[:colon], strings.TrimLeft(line[colon+1:], " \t"), true
}
