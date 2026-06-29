// Package scan iterates all git objects (reachable and unreachable) and matches patterns against their textual content for history scrubbing.
// Blobs, commit messages, and tag annotations are searched. Binary blobs (NUL in first 8KB) are skipped.
package scan

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/smm-h/safegit/internal/git"
)

// Match records a single pattern hit inside a git object.
type Match struct {
	SHA            string // object SHA
	ObjectType     string // "blob", "commit", "tag"
	Line           int    // 1-based line number of match
	Path           string // file path (for blobs; empty for commits/tags) -- reserved for future attribution
	CommitSHA      string // which commit contains this blob -- reserved for future attribution
	Reachable      bool   // whether this object is reachable from current refs
	Context        string // surrounding text with match replaced by <MATCH>
	IsBinary       bool   // true if this was a binary blob (skipped)
	SubmodulePath  string // relative path of the submodule this match belongs to; empty for parent repo
}

// ScanResults holds the aggregate output of a scan across all git objects.
type ScanResults struct {
	Matches []Match
	Skipped int // count of binary blobs skipped
	Scanned int // count of objects scanned
}

// contextRadius is the number of characters to include before and after
// a match in the Context snippet.
const contextRadius = 40

// binaryCheckSize is how many bytes to inspect for NUL to detect binary content.
const binaryCheckSize = 8192

// ScanObjects iterates every object in the git object store and searches
// for pattern matches. Binary blobs are detected and skipped. Commit and
// tag bodies (everything after the first blank line) are searched.
func ScanObjects(ctx context.Context, pattern *regexp.Regexp) (*ScanResults, error) {
	// Build the reachable set: all object SHAs reachable from any ref.
	reachable, err := buildReachableSet(ctx)
	if err != nil {
		return nil, fmt.Errorf("build reachable set: %w", err)
	}

	iter, err := git.CatFileBatchAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("cat-file batch-all: %w", err)
	}
	defer iter.Close()

	results := &ScanResults{}

	for {
		entry, err := iter.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("iterate objects: %w", err)
		}

		results.Scanned++
		isReachable := reachable[entry.SHA]

		switch entry.Type {
		case "blob":
			if isBinary(entry.Content) {
				results.Skipped++
				continue
			}
			matches := findMatches(entry.SHA, "blob", entry.Content, pattern, isReachable)
			results.Matches = append(results.Matches, matches...)

		case "commit":
			body := extractBody(entry.Content)
			if len(body) == 0 {
				continue
			}
			matches := findMatches(entry.SHA, "commit", body, pattern, isReachable)
			results.Matches = append(results.Matches, matches...)

		case "tag":
			body := extractBody(entry.Content)
			if len(body) == 0 {
				continue
			}
			matches := findMatches(entry.SHA, "tag", body, pattern, isReachable)
			results.Matches = append(results.Matches, matches...)
		}
	}

	return results, nil
}

// ScanObjectsInRange scans only objects reachable from a commit range. When
// entireHistory is true, it delegates to ScanObjects (full store scan). When
// fromSHA is set, it enumerates objects in the range fromSHA..HEAD via rev-list
// and feeds them to cat-file --batch. Everything from rev-list is reachable by
// construction, so no reachable-set computation is needed.
func ScanObjectsInRange(ctx context.Context, pattern *regexp.Regexp, fromSHA string, entireHistory bool) (*ScanResults, error) {
	if entireHistory {
		return ScanObjects(ctx, pattern)
	}

	// Enumerate objects reachable from the range.
	stdout, _, err := git.Run(ctx, "rev-list", "--objects", fromSHA+"..HEAD")
	if err != nil {
		return nil, fmt.Errorf("rev-list --objects %s..HEAD: %w", fromSHA, err)
	}

	// Parse SHAs from rev-list output. Each line is "<sha>" or "<sha> <path>".
	var shas []string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sha := line
		if idx := strings.IndexByte(line, ' '); idx > 0 {
			sha = line[:idx]
		}
		shas = append(shas, sha)
	}

	// Empty range (from == HEAD): return empty results.
	if len(shas) == 0 {
		return &ScanResults{}, nil
	}

	iter, err := git.CatFileBatchSHAs(ctx, shas)
	if err != nil {
		return nil, fmt.Errorf("cat-file batch (range): %w", err)
	}
	defer iter.Close()

	results := &ScanResults{}

	for {
		entry, err := iter.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("iterate range objects: %w", err)
		}

		results.Scanned++

		switch entry.Type {
		case "blob":
			if isBinary(entry.Content) {
				results.Skipped++
				continue
			}
			matches := findMatches(entry.SHA, "blob", entry.Content, pattern, true)
			results.Matches = append(results.Matches, matches...)

		case "commit":
			body := extractBody(entry.Content)
			if len(body) == 0 {
				continue
			}
			matches := findMatches(entry.SHA, "commit", body, pattern, true)
			results.Matches = append(results.Matches, matches...)

		case "tag":
			body := extractBody(entry.Content)
			if len(body) == 0 {
				continue
			}
			matches := findMatches(entry.SHA, "tag", body, pattern, true)
			results.Matches = append(results.Matches, matches...)
		}
	}

	return results, nil
}

// ScanObjectsWithDir iterates every object in a specific git directory's object
// store and searches for pattern matches. submodulePath is set on each returned
// Match to identify which repo the match comes from (empty for parent repo).
func ScanObjectsWithDir(ctx context.Context, pattern *regexp.Regexp, gitDir, workTree, submodulePath string) (*ScanResults, error) {
	// Build the reachable set using the target git dir.
	reachable, err := buildReachableSetWithDir(ctx, gitDir, workTree)
	if err != nil {
		return nil, fmt.Errorf("build reachable set: %w", err)
	}

	iter, err := git.CatFileBatchAllWithDir(ctx, gitDir)
	if err != nil {
		return nil, fmt.Errorf("cat-file batch-all: %w", err)
	}
	defer iter.Close()

	results := &ScanResults{}

	for {
		entry, err := iter.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("iterate objects: %w", err)
		}

		results.Scanned++
		isReachable := reachable[entry.SHA]

		switch entry.Type {
		case "blob":
			if isBinary(entry.Content) {
				results.Skipped++
				continue
			}
			matches := findMatches(entry.SHA, "blob", entry.Content, pattern, isReachable)
			for i := range matches {
				matches[i].SubmodulePath = submodulePath
			}
			results.Matches = append(results.Matches, matches...)

		case "commit":
			body := extractBody(entry.Content)
			if len(body) == 0 {
				continue
			}
			matches := findMatches(entry.SHA, "commit", body, pattern, isReachable)
			for i := range matches {
				matches[i].SubmodulePath = submodulePath
			}
			results.Matches = append(results.Matches, matches...)

		case "tag":
			body := extractBody(entry.Content)
			if len(body) == 0 {
				continue
			}
			matches := findMatches(entry.SHA, "tag", body, pattern, isReachable)
			for i := range matches {
				matches[i].SubmodulePath = submodulePath
			}
			results.Matches = append(results.Matches, matches...)
		}
	}

	return results, nil
}

// ScanObjectsInRangeWithDir scans objects in a specific git directory, either
// across the entire history or scoped to a commit range. When entireHistory is
// true it delegates to ScanObjectsWithDir (full store scan). When entireHistory
// is false and fromSHA is set, it enumerates objects in fromSHA..HEAD via
// rev-list targeting the given git dir and feeds them to cat-file --batch.
// submodulePath is set on each returned Match to identify the source repo.
func ScanObjectsInRangeWithDir(ctx context.Context, pattern *regexp.Regexp, gitDir, workTree, submodulePath string, fromSHA string, entireHistory bool) (*ScanResults, error) {
	if entireHistory {
		return ScanObjectsWithDir(ctx, pattern, gitDir, workTree, submodulePath)
	}

	// Enumerate objects reachable from the range using the submodule's git dir.
	stdout, _, err := git.RunWithGitDir(ctx, gitDir, workTree, "rev-list", "--objects", fromSHA+"..HEAD")
	if err != nil {
		return nil, fmt.Errorf("rev-list --objects %s..HEAD (dir): %w", fromSHA, err)
	}

	// Parse SHAs from rev-list output. Each line is "<sha>" or "<sha> <path>".
	var shas []string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sha := line
		if idx := strings.IndexByte(line, ' '); idx > 0 {
			sha = line[:idx]
		}
		shas = append(shas, sha)
	}

	// Empty range (from == HEAD in the submodule): return empty results.
	if len(shas) == 0 {
		return &ScanResults{}, nil
	}

	iter, err := git.CatFileBatchSHAsWithDir(ctx, gitDir, shas)
	if err != nil {
		return nil, fmt.Errorf("cat-file batch (dir range): %w", err)
	}
	defer iter.Close()

	results := &ScanResults{}

	for {
		entry, err := iter.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("iterate range objects (dir): %w", err)
		}

		results.Scanned++

		switch entry.Type {
		case "blob":
			if isBinary(entry.Content) {
				results.Skipped++
				continue
			}
			matches := findMatches(entry.SHA, "blob", entry.Content, pattern, true)
			for i := range matches {
				matches[i].SubmodulePath = submodulePath
			}
			results.Matches = append(results.Matches, matches...)

		case "commit":
			body := extractBody(entry.Content)
			if len(body) == 0 {
				continue
			}
			matches := findMatches(entry.SHA, "commit", body, pattern, true)
			for i := range matches {
				matches[i].SubmodulePath = submodulePath
			}
			results.Matches = append(results.Matches, matches...)

		case "tag":
			body := extractBody(entry.Content)
			if len(body) == 0 {
				continue
			}
			matches := findMatches(entry.SHA, "tag", body, pattern, true)
			for i := range matches {
				matches[i].SubmodulePath = submodulePath
			}
			results.Matches = append(results.Matches, matches...)
		}
	}

	return results, nil
}

// buildReachableSet runs git rev-list --all --objects and collects every
// SHA into a set.
func buildReachableSet(ctx context.Context) (map[string]bool, error) {
	stdout, _, err := git.Run(ctx, "rev-list", "--all", "--objects")
	if err != nil {
		return nil, err
	}

	set := make(map[string]bool)
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Lines are "<sha>" or "<sha> <path>" (for blobs).
		sha := line
		if idx := strings.IndexByte(line, ' '); idx > 0 {
			sha = line[:idx]
		}
		set[sha] = true
	}
	return set, nil
}

// buildReachableSetWithDir runs git rev-list --all --objects against a specific
// git directory and work tree, collecting every SHA into a set.
func buildReachableSetWithDir(ctx context.Context, gitDir, workTree string) (map[string]bool, error) {
	stdout, _, err := git.RunWithGitDir(ctx, gitDir, workTree, "rev-list", "--all", "--objects")
	if err != nil {
		return nil, err
	}

	set := make(map[string]bool)
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sha := line
		if idx := strings.IndexByte(line, ' '); idx > 0 {
			sha = line[:idx]
		}
		set[sha] = true
	}
	return set, nil
}

// isBinary returns true if any byte in the first binaryCheckSize bytes is NUL.
func isBinary(content []byte) bool {
	limit := len(content)
	if limit > binaryCheckSize {
		limit = binaryCheckSize
	}
	return bytes.ContainsRune(content[:limit], 0)
}

// extractBody returns the body portion of a commit or tag object -- everything
// after the first blank line ("\n\n"). Returns nil if there is no body.
func extractBody(content []byte) []byte {
	idx := bytes.Index(content, []byte("\n\n"))
	if idx < 0 {
		return nil
	}
	return content[idx+2:]
}

// findMatches searches content for pattern matches, running the regex on the
// full content so that multi-line patterns (e.g., (?s)foo.*bar) work correctly.
// Line numbers are back-computed from byte offsets.
func findMatches(sha, objType string, content []byte, pattern *regexp.Regexp, reachable bool) []Match {
	locs := pattern.FindAllIndex(content, -1)
	if len(locs) == 0 {
		return nil
	}

	var matches []Match
	for _, loc := range locs {
		// Back-compute 1-based line number: count newlines before the match start.
		line := 1 + bytes.Count(content[:loc[0]], []byte("\n"))

		ctx := buildContextFromContent(content, loc[0], loc[1])
		matches = append(matches, Match{
			SHA:        sha,
			ObjectType: objType,
			Line:       line,
			Reachable:  reachable,
			Context:    ctx,
		})
	}
	return matches
}

// buildContextFromContent extracts a context snippet from full content bytes.
// It finds the line containing the match start (the enclosing \n boundaries)
// and uses that line for context. For multi-line matches, the first line is used.
func buildContextFromContent(content []byte, matchStart, matchEnd int) string {
	// Find the start of the line containing matchStart.
	lineStart := matchStart
	for lineStart > 0 && content[lineStart-1] != '\n' {
		lineStart--
	}

	// Find the end of the line containing matchStart (not matchEnd, so
	// multi-line matches get the first line's context).
	lineEnd := matchStart
	for lineEnd < len(content) && content[lineEnd] != '\n' {
		lineEnd++
	}

	line := string(content[lineStart:lineEnd])

	// Adjust match offsets to be relative to the line.
	relStart := matchStart - lineStart
	relEnd := matchEnd - lineStart
	if relEnd > len(line) {
		relEnd = len(line) // clamp for multi-line matches
	}

	return buildContext(line, relStart, relEnd)
}

// buildContext extracts a snippet around a match, replacing the matched text
// with "<MATCH>" to avoid printing secrets.
func buildContext(line string, matchStart, matchEnd int) string {
	// Compute the window boundaries.
	ctxStart := matchStart - contextRadius
	if ctxStart < 0 {
		ctxStart = 0
	}
	ctxEnd := matchEnd + contextRadius
	if ctxEnd > len(line) {
		ctxEnd = len(line)
	}

	before := line[ctxStart:matchStart]
	after := line[matchEnd:ctxEnd]

	var sb strings.Builder
	if ctxStart > 0 {
		sb.WriteString("...")
	}
	sb.WriteString(before)
	sb.WriteString("<MATCH>")
	sb.WriteString(after)
	if ctxEnd < len(line) {
		sb.WriteString("...")
	}
	return sb.String()
}
