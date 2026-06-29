package main

import (
	"context"
	"fmt"
	"path"
	"regexp"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/scan"
)

// ScanResult is the JSON output for `safegit scan`.
type ScanResult struct {
	Version        int              `json:"version"`
	Pattern        string           `json:"pattern"`
	Scope          string           `json:"scope,omitempty"`
	ObjectsScanned int              `json:"objects_scanned"`
	BinarySkipped  int              `json:"binary_skipped"`
	TotalMatches   int              `json:"total_matches"`
	BlobMatches    []ScanMatchJSON  `json:"blob_matches"`
	CommitMatches  []ScanMatchJSON  `json:"commit_matches"`
	TagMatches     []ScanMatchJSON  `json:"tag_matches"`
	FileMatches    []ScanMatchJSON  `json:"file_matches"`
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

	totalMatches := len(blobMatches) + len(commitMatches) + len(tagMatches) + len(nonObjectMatches)

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
			FileMatches:    matchesToJSON(nonObjectMatches),
		}
		if scope != nil {
			result.Scope = *scope
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

	if len(nonObjectMatches) > 0 {
		infof(flags, "\nNon-object files:\n")
		for _, m := range nonObjectMatches {
			infof(flags, "  %s (line %d): %s\n", m.Path, m.Line, m.Context)
		}
	}

	infof(flags, "\nSummary: %d blob, %d commit message, %d tag annotation, %d file matches\n",
		len(blobMatches), len(commitMatches), len(tagMatches), len(nonObjectMatches))
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
