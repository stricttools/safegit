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
	SHA           string // object SHA
	ObjectType    string // "blob", "commit", "tag"
	Line          int    // 1-based line number of match
	ByteOffset    int    // 0-based byte offset of match start within the scanned content
	Path          string // file path (for blobs; empty for commits/tags) -- reserved for future attribution
	CommitSHA     string // which commit contains this blob -- reserved for future attribution
	Reachable     bool   // whether this object is reachable from current refs
	Context       string // surrounding text with match replaced by <MATCH>
	IsBinary      bool   // true if this was a binary blob (skipped)
	SubmodulePath string // relative path of the submodule this match belongs to; empty for parent repo
	// InGitDir marks a non-object match found inside GIT'S OWN STATE rather
	// than in the work tree -- config, COMMIT_EDITMSG, the hook directories,
	// everything safegit keeps under .git/safegit. Every other coordinate in a
	// scan is repo-relative, so without this flag a reader cannot tell
	// `hooks/pre-commit` inside .git from a tracked file of that name.
	//
	// It is a statement about WHERE the file was found, not a promise about the
	// shape of Path. Path is relative to the git directory whenever it sits
	// under it, which is the ordinary case; a location git's own resolution
	// puts OUTSIDE the git dir -- a core.hooksPath pointing somewhere else --
	// is still git's state and still carries this flag, but its Path stays
	// ABSOLUTE, because a relative coordinate would name a file that is not
	// there.
	InGitDir bool
}

// ScanResults holds the aggregate output of a scan across all git objects.
type ScanResults struct {
	Matches []Match
	Skipped int // count of binary blobs skipped
	Scanned int // count of objects scanned
}

// ScanOpts configures which objects to scan and where to find them.
//
// Exactly one of EntireHistory, FromSHA and Tips selects the object set;
// declaring none, or more than one, is an error rather than a precedence rule.
type ScanOpts struct {
	GitDir        string // empty = use CWD's git dir
	WorkTree      string // empty = use CWD's work tree
	SubmodulePath string // set on all returned matches; empty for parent repo
	FromSHA       string // scan the objects rev-list reaches in FromSHA..HEAD
	EntireHistory bool   // true = cat-file --batch-all-objects (includes unreachable)

	// Tips scans exactly the objects reachable from the given commits or tag
	// objects, whether or not any ref points at them. It is how a rewrite is
	// checked BEFORE its refs move: the rewritten commits exist only as
	// unreachable objects at that moment, so neither the reachable-set walk
	// nor a FromSHA..HEAD range can see them, while `rev-list --objects` given
	// their SHAs walks exactly the new history (a tag object listed here brings
	// itself and everything it points at).
	Tips []string
}

// selection reports which of the three object-set selectors this ScanOpts
// elects, refusing an ambiguous or empty declaration.
func (o ScanOpts) selection() (string, error) {
	elected := ""
	count := 0
	if o.EntireHistory {
		elected, count = "entire-history", count+1
	}
	if o.FromSHA != "" {
		elected, count = "from-sha", count+1
	}
	if len(o.Tips) > 0 {
		elected, count = "tips", count+1
	}
	switch count {
	case 1:
		return elected, nil
	case 0:
		return "", fmt.Errorf("ScanOpts: one of EntireHistory, FromSHA or Tips must be set")
	default:
		return "", fmt.Errorf("ScanOpts: EntireHistory, FromSHA and Tips are alternatives; %d were set", count)
	}
}

// contextRadius is the number of characters to include before and after
// a match in the Context snippet.
const contextRadius = 40

// binaryCheckSize is how many bytes to inspect for NUL to detect binary content.
const binaryCheckSize = 8192

// ScanObjectsMulti iterates every object in the git object store once and
// searches for matches against multiple patterns simultaneously. Returns one
// ScanResults per pattern (same order as input). The reachable set and object
// iteration happen only once regardless of how many patterns are provided.
// Uses the same opts as ScanObjects; only EntireHistory mode is supported.
func ScanObjectsMulti(ctx context.Context, patterns []*regexp.Regexp, opts ScanOpts) ([]*ScanResults, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	if len(patterns) == 1 {
		r, err := ScanObjects(ctx, patterns[0], opts)
		if err != nil {
			return nil, err
		}
		return []*ScanResults{r}, nil
	}

	if !opts.EntireHistory {
		return nil, fmt.Errorf("ScanObjectsMulti only supports EntireHistory mode")
	}

	hasDir := opts.GitDir != ""

	reachable, err := buildReachableSet(ctx, opts, hasDir)
	if err != nil {
		return nil, fmt.Errorf("build reachable set: %w", err)
	}

	var iter *git.ObjectIterator
	if hasDir {
		iter, err = git.CatFileBatchAllWithDir(ctx, opts.GitDir)
	} else {
		iter, err = git.CatFileBatchAll(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("cat-file batch-all: %w", err)
	}
	defer iter.Close()

	// Initialize per-pattern results.
	allResults := make([]*ScanResults, len(patterns))
	for i := range allResults {
		allResults[i] = &ScanResults{}
	}

	for {
		entry, err := iter.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("iterate objects: %w", err)
		}

		isReachable := reachable[entry.SHA]

		// Determine scannable content based on object type.
		var content []byte
		var objType string
		switch entry.Type {
		case "blob":
			if isBinary(entry.Content) {
				for _, r := range allResults {
					r.Scanned++
					r.Skipped++
				}
				continue
			}
			content = entry.Content
			objType = "blob"
		case "commit":
			body := extractBody(entry.Content)
			if len(body) == 0 {
				for _, r := range allResults {
					r.Scanned++
				}
				continue
			}
			content = body
			objType = "commit"
		case "tag":
			body := extractBody(entry.Content)
			if len(body) == 0 {
				for _, r := range allResults {
					r.Scanned++
				}
				continue
			}
			content = body
			objType = "tag"
		default:
			for _, r := range allResults {
				r.Scanned++
			}
			continue
		}

		// Test each pattern against the same content.
		for i, pat := range patterns {
			allResults[i].Scanned++
			matches := findMatches(entry.SHA, objType, content, pat, isReachable)
			for j := range matches {
				matches[j].SubmodulePath = opts.SubmodulePath
			}
			allResults[i].Matches = append(allResults[i].Matches, matches...)
		}
	}

	return allResults, nil
}

// ScanObjects iterates git objects and searches for pattern matches. The opts
// parameter controls which objects are scanned:
//   - EntireHistory=true: uses cat-file --batch-all-objects (all objects including
//     unreachable loose objects). Builds a reachable set to mark each match.
//   - FromSHA set: uses rev-list --objects FromSHA..HEAD (reachable only). All
//     matches are marked reachable by construction.
//   - Tips set: uses rev-list --objects on those commits or tag objects, so an
//     object set no ref points at yet can be scanned. All matches are marked
//     reachable by construction (they are reachable from the given tips).
//   - None set: returns an error.
//
// When GitDir is set, commands target that git directory instead of CWD.
// SubmodulePath is set on every returned Match.
func ScanObjects(ctx context.Context, pattern *regexp.Regexp, opts ScanOpts) (*ScanResults, error) {
	selection, err := opts.selection()
	if err != nil {
		return nil, err
	}

	hasDir := opts.GitDir != ""

	if selection == "entire-history" {
		return scanEntireHistory(ctx, pattern, opts, hasDir)
	}
	return scanRange(ctx, pattern, opts, hasDir)
}

// revListSelector renders the rev-list arguments for the elected object set.
// It is the one place the three selectors become argv, so scanning and blob
// attribution can never walk different object sets for the same opts.
func revListSelector(opts ScanOpts) []string {
	if len(opts.Tips) > 0 {
		return append([]string{}, opts.Tips...)
	}
	return []string{opts.FromSHA + "..HEAD"}
}

// scanEntireHistory implements the cat-file --batch-all-objects path.
func scanEntireHistory(ctx context.Context, pattern *regexp.Regexp, opts ScanOpts, hasDir bool) (*ScanResults, error) {
	reachable, err := buildReachableSet(ctx, opts, hasDir)
	if err != nil {
		return nil, fmt.Errorf("build reachable set: %w", err)
	}

	var iter *git.ObjectIterator
	if hasDir {
		iter, err = git.CatFileBatchAllWithDir(ctx, opts.GitDir)
	} else {
		iter, err = git.CatFileBatchAll(ctx)
	}
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

		scanEntry(entry, pattern, isReachable, opts.SubmodulePath, results)
	}

	return results, nil
}

// scanRange implements the rev-list --objects path, for both the FromSHA range
// and an explicit set of tips.
func scanRange(ctx context.Context, pattern *regexp.Regexp, opts ScanOpts, hasDir bool) (*ScanResults, error) {
	selector := revListSelector(opts)
	args := append([]string{"rev-list", "--objects"}, selector...)

	var stdout string
	var err error
	if hasDir {
		stdout, _, err = git.RunWithGitDir(ctx, opts.GitDir, opts.WorkTree, args...)
	} else {
		stdout, _, err = git.Run(ctx, args...)
	}
	if err != nil {
		return nil, fmt.Errorf("rev-list --objects %s: %w", strings.Join(selector, " "), err)
	}

	shas := parseSHAs(stdout)

	// Empty range (from == HEAD): return empty results.
	if len(shas) == 0 {
		return &ScanResults{}, nil
	}

	var iter *git.ObjectIterator
	if hasDir {
		iter, err = git.CatFileBatchSHAsWithDir(ctx, opts.GitDir, shas)
	} else {
		iter, err = git.CatFileBatchSHAs(ctx, shas)
	}
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
		// Everything from rev-list is reachable by construction.
		scanEntry(entry, pattern, true, opts.SubmodulePath, results)
	}

	return results, nil
}

// scanEntry processes a single object entry, appending any matches to results.
func scanEntry(entry *git.ObjectEntry, pattern *regexp.Regexp, reachable bool, submodulePath string, results *ScanResults) {
	switch entry.Type {
	case "blob":
		if isBinary(entry.Content) {
			results.Skipped++
			return
		}
		matches := findMatches(entry.SHA, "blob", entry.Content, pattern, reachable)
		for i := range matches {
			matches[i].SubmodulePath = submodulePath
		}
		results.Matches = append(results.Matches, matches...)

	case "commit":
		body := extractBody(entry.Content)
		if len(body) == 0 {
			return
		}
		matches := findMatches(entry.SHA, "commit", body, pattern, reachable)
		for i := range matches {
			matches[i].SubmodulePath = submodulePath
		}
		results.Matches = append(results.Matches, matches...)

	case "tag":
		body := extractBody(entry.Content)
		if len(body) == 0 {
			return
		}
		matches := findMatches(entry.SHA, "tag", body, pattern, reachable)
		for i := range matches {
			matches[i].SubmodulePath = submodulePath
		}
		results.Matches = append(results.Matches, matches...)
	}
}

// parseSHAs extracts SHA values from rev-list --objects output.
// Each line is "<sha>" or "<sha> <path>".
func parseSHAs(stdout string) []string {
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
	return shas
}

// buildReachableSet runs git rev-list --all --objects and collects every
// SHA into a set, optionally targeting a specific git directory.
func buildReachableSet(ctx context.Context, opts ScanOpts, hasDir bool) (map[string]bool, error) {
	var stdout string
	var err error
	if hasDir {
		stdout, _, err = git.RunWithGitDir(ctx, opts.GitDir, opts.WorkTree, "rev-list", "--all", "--objects")
	} else {
		stdout, _, err = git.Run(ctx, "rev-list", "--all", "--objects")
	}
	if err != nil {
		return nil, err
	}

	return parseSHASet(stdout), nil
}

// parseSHASet parses rev-list --objects output into a set of SHAs.
func parseSHASet(stdout string) map[string]bool {
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
	return set
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
			ByteOffset: loc[0],
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
