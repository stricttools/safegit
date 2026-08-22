package conflict

import (
	"bytes"
)

// Conflict-region parsing: finding the marked-up blocks git writes into a file,
// as bytes and as line numbers.
//
// This is mechanism, not policy. Whether a block is an error, which paths are
// exempt and what a conclusion should refuse are decided by the conclusion
// engine's marker verification; what lives here is the single answer to "which
// byte ranges of this content are conflict regions, and where do they start".

// Region is one complete conflict block: an opening marker line, the sides, and
// a closing marker line.
//
// Bytes is the block verbatim, including both marker lines and the newline that
// ends the closing one when the content has it. Verbatim is what the caller
// needs: attributing a block to something git wrote, or to a blob a parent
// already carried, is a byte comparison, never a similarity judgment.
type Region struct {
	// StartLine and EndLine are 1-based line numbers of the opening and closing
	// marker lines, so a message can name the place an operator has to look.
	StartLine int
	EndLine   int
	Bytes     []byte
}

// Regions finds every complete conflict block in content.
//
// COMPLETE is the operative word: an opening marker with no separator, or a
// separator with no closing marker, is not a region. A file can hold such a
// fragment for reasons that have nothing to do with a conflict (a document
// about conflict markers, a test fixture, a diff quoted in a comment), and
// treating a fragment as a conflict would refuse work over prose.
//
// The marker length is a MINIMUM, matching how git's own conflict-marker
// detection reads a line (`git diff --check`): a run of at least markerSize
// identical marker characters, ending the line or followed by a space. An
// operator who lengthened a marker line while editing has still left a
// conflict, and a shorter run is not one.
//
// A nested opening marker inside a block is deliberately not treated as a new
// region: git never writes one, and the bytes are covered by the enclosing
// block either way.
func Regions(content []byte, markerSize int) []Region {
	if markerSize <= 0 {
		markerSize = DefaultMarkerSize
	}
	lines, offsets := splitLinesKeepingOffsets(content)

	var regions []Region
	for i := 0; i < len(lines); i++ {
		if !isMarkerLine(lines[i], '<', markerSize) {
			continue
		}
		sep := -1
		for j := i + 1; j < len(lines); j++ {
			if isMarkerLine(lines[j], '=', markerSize) {
				sep = j
				break
			}
			if isMarkerLine(lines[j], '>', markerSize) {
				// A closing marker before any separator: this opener starts no
				// region, and the scan resumes at the next line rather than
				// pairing an opener with a closer across a missing separator.
				break
			}
		}
		if sep < 0 {
			continue
		}
		end := -1
		for j := sep + 1; j < len(lines); j++ {
			if isMarkerLine(lines[j], '>', markerSize) {
				end = j
				break
			}
		}
		if end < 0 {
			continue
		}
		start, stop := offsets[i], offsets[end]+len(lines[end])
		if stop < len(content) && content[stop] == '\n' {
			stop++
		}
		regions = append(regions, Region{
			StartLine: i + 1,
			EndLine:   end + 1,
			Bytes:     content[start:stop],
		})
		i = end
	}
	return regions
}

// ContainsRegion reports whether content holds a block byte-identical to the
// given one, and the 1-based line it starts on.
//
// It is a substring search anchored to a line boundary, because a block that
// appears in the middle of a line is not the block: the marker lines that make
// it one have to start where a line starts.
func ContainsRegion(content, block []byte) (line int, found bool) {
	if len(block) == 0 {
		return 0, false
	}
	for offset := 0; ; {
		idx := bytes.Index(content[offset:], block)
		if idx < 0 {
			return 0, false
		}
		at := offset + idx
		if at == 0 || content[at-1] == '\n' {
			return bytes.Count(content[:at], []byte("\n")) + 1, true
		}
		offset = at + 1
	}
}

// isMarkerLine reports whether a line opens, separates or closes a conflict
// region with the given marker character.
//
// A trailing carriage return counts as the end of the line, so a checkout with
// CRLF line endings still has its regions FOUND. Attribution is a different
// question and stays byte-exact: a block that a parent's blob holds with LF
// endings is not the same bytes as the same block with CRLF endings, so a
// repository that both filters line endings and legitimately carries
// marker-shaped content needs the declared exemption.
func isMarkerLine(line []byte, marker byte, markerSize int) bool {
	run := 0
	for run < len(line) && line[run] == marker {
		run++
	}
	if run < markerSize {
		return false
	}
	rest := line[run:]
	return len(rest) == 0 || rest[0] == ' ' || string(rest) == "\r"
}

// splitLinesKeepingOffsets splits content into lines without their terminating
// newline, alongside each line's byte offset, so a region can be cut out of the
// original bytes exactly as it was stored.
func splitLinesKeepingOffsets(content []byte) (lines [][]byte, offsets []int) {
	start := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			lines = append(lines, content[start:i])
			offsets = append(offsets, start)
			start = i + 1
		}
	}
	if start < len(content) {
		lines = append(lines, content[start:])
		offsets = append(offsets, start)
	}
	return lines, offsets
}
