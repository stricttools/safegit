// Package trailer injects git trailers (key-value metadata lines) into commit messages for AI agent traceability and session attribution.
package trailer

import (
	"os"
	"regexp"
	"strings"
)

// SessionKey is the git trailer key used to record the Claude Code session ID.
const SessionKey = "Claude-Code-Session-Id"

const envVar = "CLAUDE_CODE_SESSION_ID"

// trailerLine matches lines of the form "Key-Name: value".
var trailerLine = regexp.MustCompile(`^[A-Za-z0-9][-A-Za-z0-9]*:\s`)

// identityTrailerKey matches trailer keys ending in "-by" (case-insensitive),
// which are the conventional identity-bearing trailers: Signed-off-by,
// Co-authored-by, Reviewed-by, Acked-by, etc.
var identityTrailerKey = regexp.MustCompile(`(?i)^[A-Za-z0-9][-A-Za-z0-9]*-[Bb][Yy]:\s`)

// Inject reads CLAUDE_CODE_SESSION_ID from the environment and appends
// a Claude-Code-Session-Id trailer to the commit message if present.
// For amend: deduplicates if the same session ID already exists as a trailer;
// keeps both if a different session's trailer is present.
func Inject(message string) string {
	sessionID := os.Getenv(envVar)
	if sessionID == "" {
		return message
	}

	trailer := SessionKey + ": " + sessionID

	// Check if this exact trailer already exists (dedup for same-session amend)
	for _, line := range strings.Split(message, "\n") {
		if strings.TrimSpace(line) == trailer {
			return message
		}
	}

	// Find whether the message already ends with a trailer block.
	// A trailer block is one or more consecutive trailer-format lines at the end,
	// preceded by an empty line (or at the start of the message).
	trimmed := strings.TrimRight(message, "\n")
	lines := strings.Split(trimmed, "\n")

	if endsWithTrailerBlock(lines) {
		// Append directly to the existing trailer block
		return trimmed + "\n" + trailer + "\n"
	}

	// No existing trailer block: add blank line separator before trailer
	return trimmed + "\n\n" + trailer + "\n"
}

// AppendCustom appends user-provided trailers to the commit message.
// Each trailer should be in "Key: Value" format. If trailers is empty,
// the message is returned unchanged. Follows the same format as Inject:
// appends to an existing trailer block, or adds a blank line separator first.
func AppendCustom(message string, trailers []string) string {
	if len(trailers) == 0 {
		return message
	}

	trimmed := strings.TrimRight(message, "\n")
	lines := strings.Split(trimmed, "\n")

	var b strings.Builder
	if endsWithTrailerBlock(lines) {
		b.WriteString(trimmed)
		b.WriteByte('\n')
	} else {
		b.WriteString(trimmed)
		b.WriteString("\n\n")
	}
	for _, t := range trailers {
		b.WriteString(t)
		b.WriteByte('\n')
	}
	return b.String()
}

// SplitBodyTrailers splits a commit message into the body (everything
// before the trailer block) and the trailer block (trailing Key: Value
// lines preceded by a blank line). Continuation lines (indented lines
// following a trailer) are included in the trailer block.
//
// If the message has no trailers, body is the entire message and
// trailerBlock is empty. If the entire message consists of trailer-
// format lines with no blank-line separator, body is empty and
// trailerBlock is the entire message.
func SplitBodyTrailers(message string) (body, trailerBlock string) {
	if message == "" {
		return "", ""
	}

	trimmed := strings.TrimRight(message, "\n")
	lines := strings.Split(trimmed, "\n")

	// Walk backwards from the end to find the trailer block.
	// A trailer line matches the trailerLine regex. Continuation lines
	// (starting with whitespace) are part of the preceding trailer.
	i := len(lines) - 1
	for i >= 0 {
		line := lines[i]
		if trailerLine.MatchString(line) {
			i--
			continue
		}
		// Continuation line: starts with space/tab, non-empty after trim
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') && strings.TrimSpace(line) != "" {
			i--
			continue
		}
		break
	}

	trailerStart := i + 1
	trailerCount := len(lines) - trailerStart
	if trailerCount == 0 {
		// No trailer lines found
		return message, ""
	}

	// The first line of the trailer block must itself be a trailer line
	// (not a continuation line), otherwise it's not a valid block.
	if !trailerLine.MatchString(lines[trailerStart]) {
		return message, ""
	}

	// All-trailer message: every line is part of the trailer block
	if trailerStart == 0 {
		return "", trimmed + "\n"
	}

	// The line immediately before the trailer block must be blank
	if strings.TrimSpace(lines[trailerStart-1]) != "" {
		return message, ""
	}

	// Split: body includes everything up to and including the blank line
	bodyLines := lines[:trailerStart]
	body = strings.Join(bodyLines, "\n") + "\n"
	trailerBlock = strings.Join(lines[trailerStart:], "\n") + "\n"
	return body, trailerBlock
}

// ReplaceIdentity replaces author identity in identity-bearing trailers
// (lines whose key ends in "-by", such as Signed-off-by, Co-authored-by,
// Reviewed-by, Acked-by). Within those trailer lines, it replaces
// "oldName <oldEmail>" with "newName <newEmail>". When only one of
// name/email is changing (the other old value is empty), only the
// provided part is replaced. Non-identity trailers and the message body
// are never modified. If no changes are made, the original message is
// returned unchanged.
func ReplaceIdentity(message, oldName, newName, oldEmail, newEmail string) string {
	body, trailerBlock := SplitBodyTrailers(message)
	if trailerBlock == "" {
		return message
	}

	lines := strings.Split(strings.TrimRight(trailerBlock, "\n"), "\n")
	changed := false

	for i, line := range lines {
		// Only process identity trailers (keys ending in -by).
		if !identityTrailerKey.MatchString(line) {
			continue
		}

		newLine := replaceIdentityInLine(line, oldName, newName, oldEmail, newEmail)
		if newLine != line {
			lines[i] = newLine
			changed = true
		}
	}

	if !changed {
		return message
	}

	return body + strings.Join(lines, "\n") + "\n"
}

// replaceIdentityInLine replaces identity occurrences in a single trailer
// line. It handles three cases:
//   - Both name and email provided: replace "oldName <oldEmail>" with "newName <newEmail>"
//   - Only email provided (oldName == ""): replace "<oldEmail>" with "<newEmail>"
//   - Only name provided (oldEmail == ""): replace "oldName" with "newName" when
//     it appears as the name portion before a " <" email delimiter
func replaceIdentityInLine(line, oldName, newName, oldEmail, newEmail string) string {
	// Extract the value portion (after "Key: ")
	colonIdx := strings.Index(line, ":")
	if colonIdx < 0 {
		return line
	}
	key := line[:colonIdx+1]
	value := line[colonIdx+1:]

	// Trim leading space from value for processing, but track it.
	trimmedValue := strings.TrimLeft(value, " ")
	leadingSpace := value[:len(value)-len(trimmedValue)]

	newValue := trimmedValue

	switch {
	case oldName != "" && oldEmail != "":
		// Replace "oldName <oldEmail>" with "newName <newEmail>"
		old := oldName + " <" + oldEmail + ">"
		repl := newName + " <" + newEmail + ">"
		newValue = strings.Replace(trimmedValue, old, repl, 1)

	case oldEmail != "":
		// Replace "<oldEmail>" with "<newEmail>"
		old := "<" + oldEmail + ">"
		repl := "<" + newEmail + ">"
		newValue = strings.Replace(trimmedValue, old, repl, 1)

	case oldName != "":
		// Replace the name portion: "oldName <" -> "newName <"
		old := oldName + " <"
		repl := newName + " <"
		newValue = strings.Replace(trimmedValue, old, repl, 1)
	}

	if newValue == trimmedValue {
		return line
	}

	return key + leadingSpace + newValue
}

// endsWithTrailerBlock returns true if the lines end with a block of
// trailer-format lines preceded by a blank line (or if the entire message
// is trailer lines, which happens with single-line messages that look like trailers).
func endsWithTrailerBlock(lines []string) bool {
	if len(lines) == 0 {
		return false
	}

	// Walk backwards from the end to find consecutive trailer lines
	i := len(lines) - 1
	for i >= 0 && trailerLine.MatchString(lines[i]) {
		i--
	}

	trailerCount := len(lines) - 1 - i
	if trailerCount == 0 {
		return false
	}

	// The line before the trailer block must be blank (standard git trailer convention)
	if i >= 0 && strings.TrimSpace(lines[i]) == "" {
		return true
	}

	return false
}
