package main

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/scan"
	"github.com/smm-h/safegit/internal/trailer"
)

// ScanResult is the JSON output for `safegit scan`.
type ScanResult struct {
	Version        int             `json:"version"`
	Pattern        string          `json:"pattern"`
	Scope          string          `json:"scope,omitempty"`
	Target         string          `json:"target,omitempty"`
	ObjectsScanned int             `json:"objects_scanned"`
	BinarySkipped  int             `json:"binary_skipped"`
	TotalMatches   int             `json:"total_matches"`
	BlobMatches    []ScanMatchJSON `json:"blob_matches"`
	CommitMatches  []ScanMatchJSON `json:"commit_matches"`
	TagMatches     []ScanMatchJSON `json:"tag_matches"`
	TrailerMatches []ScanMatchJSON `json:"trailer_matches"`
	FileMatches    []ScanMatchJSON `json:"file_matches"`
}

// ScanMatchJSON is a single match in JSON output.
type ScanMatchJSON struct {
	SHA        string `json:"sha,omitempty"`
	ObjectType string `json:"object_type"`
	Path       string `json:"path,omitempty"`
	CommitSHA  string `json:"commit_sha,omitempty"`
	Line       int    `json:"line"`
	Reachable  bool   `json:"reachable"`
	Context    string `json:"context"`
}

// validTargets lists the allowed values for --target.
var validTargets = map[string]bool{
	"blobs":    true,
	"commits":  true,
	"tags":     true,
	"trailers": true,
	"files":    true,
}

// parseTargets parses and validates a comma-separated --target string.
// Returns nil (all targets) when raw is nil.
func parseTargets(flags globalFlags, raw interface{}) map[string]bool {
	if raw == nil {
		return nil
	}
	s := raw.(string)
	parts := strings.Split(s, ",")
	targets := make(map[string]bool, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !validTargets[p] {
			die(flags, "scan", 2, fmt.Sprintf("invalid --target value %q; valid values: blobs, commits, tags, trailers, files", p))
		}
		targets[p] = true
	}
	if len(targets) == 0 {
		die(flags, "scan", 2, "--target requires at least one value; valid values: blobs, commits, tags, trailers, files")
	}
	return targets
}

// targetIncluded returns true when the named category should be included
// in the output. When targets is nil (--target not specified), all
// categories are included.
func targetIncluded(targets map[string]bool, name string) bool {
	if targets == nil {
		return true
	}
	return targets[name]
}

// filterTrailerMatches separates commit matches into body-only and
// trailer-only subsets. For each unique commit SHA, it reads the commit
// object, extracts the body (everything after the header blank line),
// and uses trailer.SplitBodyTrailers to find the byte offset where
// the trailer block starts. Matches whose ByteOffset >= that offset are
// trailer matches; the rest are body matches.
func filterTrailerMatches(ctx context.Context, commitMatches []scan.Match) (bodyOnly, trailerOnly []scan.Match) {
	if len(commitMatches) == 0 {
		return nil, nil
	}

	// Cache commit bodies by SHA to avoid redundant cat-file calls.
	bodyCache := make(map[string]string)
	// trailerStartOffset[sha] = byte offset within the body where trailers begin,
	// or -1 if the commit has no trailers.
	trailerStartCache := make(map[string]int)

	for _, m := range commitMatches {
		if _, ok := trailerStartCache[m.SHA]; ok {
			// Already cached.
		} else {
			// Read the commit object and extract its body.
			content, err := git.CatFileBlob(ctx, m.SHA)
			if err != nil {
				// Cannot read commit; treat all its matches as body matches.
				trailerStartCache[m.SHA] = -1
			} else {
				body := extractCommitBody(content)
				bodyCache[m.SHA] = body
				_, trailerBlock := trailer.SplitBodyTrailers(body)
				if trailerBlock == "" {
					trailerStartCache[m.SHA] = -1
				} else {
					// The trailer block starts at len(body) - len(trailerBlock)
					// but SplitBodyTrailers returns body including the blank
					// separator line before trailers, so the trailer offset
					// within the original body string is len(bodyPart).
					bodyPart, _ := trailer.SplitBodyTrailers(body)
					trailerStartCache[m.SHA] = len(bodyPart)
				}
			}
		}

		trailerStart := trailerStartCache[m.SHA]
		if trailerStart >= 0 && m.ByteOffset >= trailerStart {
			trailerOnly = append(trailerOnly, m)
		} else {
			bodyOnly = append(bodyOnly, m)
		}
	}

	return bodyOnly, trailerOnly
}

// extractCommitBody returns the message body of a raw commit object
// (everything after the first blank line).
func extractCommitBody(raw []byte) string {
	idx := strings.Index(string(raw), "\n\n")
	if idx < 0 {
		return ""
	}
	return string(raw[idx+2:])
}

func runScan(flags globalFlags, kwargs map[string]interface{}) int {
	const cmd = "scan"

	pattern := kwargs["pattern"].(string)

	var scope *string
	if v := kwargs["scope"]; v != nil {
		s := v.(string)
		scope = &s
		if _, err := path.Match(s, ""); err != nil {
			die(flags, cmd, 2, fmt.Sprintf("invalid --scope glob: %v", err))
		}
	}

	var from *string
	if v := kwargs["from"]; v != nil {
		s := v.(string)
		from = &s
	}
	entireHistory := kwargs["entire_history"].(bool)

	// Parse --target filter.
	targets := parseTargets(flags, kwargs["target"])

	// Require a git repo.
	gitDir := mustGitDir(flags, cmd)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		die(flags, cmd, 4, err.Error())
	}

	ctx := context.Background()

	// Compile regex.
	compiledPattern, err := regexp.Compile(pattern)
	if err != nil {
		die(flags, cmd, 2, fmt.Sprintf("invalid regex pattern: %v", err))
	}

	// Resolve --from if provided.
	var fromSHA string
	if from != nil {
		fromSHA, err = git.RevParse(ctx, *from)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("resolving --from %q: %v", *from, err))
		}
		isAnc, err := git.IsAncestorOf(ctx, fromSHA, "HEAD")
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("checking ancestry of --from: %v", err))
		}
		if !isAnc {
			die(flags, cmd, 1, fmt.Sprintf("--from commit %s is not an ancestor of HEAD", *from))
		}
	}

	// Mutual exclusivity: --from and --entire-history cannot both be set.
	if from != nil && entireHistory {
		die(flags, cmd, 2, "--from and --entire-history are mutually exclusive")
	}

	// Default to entire-history when neither --from nor --entire-history is set.
	if from == nil && !entireHistory {
		entireHistory = true
	}

	// Scan git objects.
	infof(flags, "Scanning objects...\n")
	results, err := scan.ScanObjectsInRange(ctx, compiledPattern, fromSHA, entireHistory)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("scanning objects: %v", err))
	}

	// Enrich blob matches with file paths.
	if err := scan.AddAttribution(ctx, results); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("adding attribution: %v", err))
	}

	// Scan non-object files (working tree, .git/config, hooks).
	nonObjectMatches, err := scan.ScanNonObjects(ctx, compiledPattern, gitDir)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("scanning non-object files: %v", err))
	}

	// Categorize matches, applying scope filter to blobs.
	var blobMatches, commitMatches, tagMatches []scan.Match
	for _, m := range results.Matches {
		switch m.ObjectType {
		case "blob":
			if scope != nil && !matchScope(*scope, m.Path) {
				continue
			}
			blobMatches = append(blobMatches, m)
		case "commit":
			commitMatches = append(commitMatches, m)
		case "tag":
			tagMatches = append(tagMatches, m)
		}
	}

	// Split commit matches into body-only and trailer-only when --target
	// includes "trailers" (or when no --target is specified, for the
	// trailer_matches JSON field).
	var trailerMatches []scan.Match
	needTrailerSplit := targetIncluded(targets, "trailers")
	if needTrailerSplit {
		bodyOnly, trailerOnly := filterTrailerMatches(ctx, commitMatches)
		trailerMatches = trailerOnly
		// When --target includes "commits", keep all commit matches.
		// When --target includes only "trailers" (not "commits"), drop body-only matches.
		if !targetIncluded(targets, "commits") {
			commitMatches = nil
		} else {
			_ = bodyOnly // commits target includes all commit matches
		}
	}

	// Apply --target filtering: drop categories not in the target set.
	if !targetIncluded(targets, "blobs") {
		blobMatches = nil
	}
	if !targetIncluded(targets, "commits") && !targetIncluded(targets, "trailers") {
		commitMatches = nil
	}
	if !targetIncluded(targets, "tags") {
		tagMatches = nil
	}
	if !targetIncluded(targets, "trailers") {
		trailerMatches = nil
	}
	if !targetIncluded(targets, "files") {
		nonObjectMatches = nil
	}

	totalMatches := len(blobMatches) + len(commitMatches) + len(tagMatches) + len(trailerMatches) + len(nonObjectMatches)

	// Build the target string for JSON output.
	var targetStr string
	if targets != nil {
		var parts []string
		for _, t := range []string{"blobs", "commits", "tags", "trailers", "files"} {
			if targets[t] {
				parts = append(parts, t)
			}
		}
		targetStr = strings.Join(parts, ",")
	}

	// JSON output.
	if flags.json {
		result := ScanResult{
			Version:        1,
			Pattern:        pattern,
			ObjectsScanned: results.Scanned,
			BinarySkipped:  results.Skipped,
			TotalMatches:   totalMatches,
			BlobMatches:    matchesToJSON(blobMatches),
			CommitMatches:  matchesToJSON(commitMatches),
			TagMatches:     matchesToJSON(tagMatches),
			TrailerMatches: matchesToJSON(trailerMatches),
			FileMatches:    matchesToJSON(nonObjectMatches),
		}
		if scope != nil {
			result.Scope = *scope
		}
		if targetStr != "" {
			result.Target = targetStr
		}
		emitJSON(result)
		return 0
	}

	// Human-readable output.
	if totalMatches == 0 {
		infof(flags, "No matches found.\n")
		return 0
	}

	infof(flags, "Found %d matches:\n", totalMatches)

	if len(blobMatches) > 0 {
		infof(flags, "\nBlobs:\n")
		for _, m := range blobMatches {
			if m.Path != "" && m.CommitSHA != "" {
				infof(flags, "  %s in commit %s (line %d): %s\n", m.Path, shortSHA(m.CommitSHA), m.Line, m.Context)
			} else if m.Path != "" {
				infof(flags, "  %s (unreachable, line %d): %s\n", m.Path, m.Line, m.Context)
			} else if m.Reachable {
				infof(flags, "  blob %s (line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
			} else {
				infof(flags, "  blob %s (unreachable, line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
			}
		}
	}

	if len(commitMatches) > 0 {
		infof(flags, "\nCommit messages:\n")
		for _, m := range commitMatches {
			infof(flags, "  commit %s (line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
		}
	}

	if len(tagMatches) > 0 {
		infof(flags, "\nTag annotations:\n")
		for _, m := range tagMatches {
			infof(flags, "  tag %s (line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
		}
	}

	if len(trailerMatches) > 0 {
		infof(flags, "\nTrailers:\n")
		for _, m := range trailerMatches {
			infof(flags, "  commit %s (line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
		}
	}

	if len(nonObjectMatches) > 0 {
		infof(flags, "\nNon-object files:\n")
		for _, m := range nonObjectMatches {
			infof(flags, "  %s (line %d): %s\n", m.Path, m.Line, m.Context)
		}
	}

	infof(flags, "\nSummary: %d blob, %d commit message, %d tag annotation, %d trailer, %d file matches\n",
		len(blobMatches), len(commitMatches), len(tagMatches), len(trailerMatches), len(nonObjectMatches))
	if results.Skipped > 0 {
		infof(flags, "Binary blobs skipped: %d\n", results.Skipped)
	}

	return 0
}

// matchesToJSON converts scan.Match slices to ScanMatchJSON slices for JSON output.
func matchesToJSON(matches []scan.Match) []ScanMatchJSON {
	out := make([]ScanMatchJSON, len(matches))
	for i, m := range matches {
		out[i] = ScanMatchJSON{
			SHA:        m.SHA,
			ObjectType: m.ObjectType,
			Path:       m.Path,
			CommitSHA:  m.CommitSHA,
			Line:       m.Line,
			Reachable:  m.Reachable,
			Context:    m.Context,
		}
	}
	return out
}
