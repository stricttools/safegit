package scan

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/smm-h/safegit/internal/git"
)

// blobAttribution maps a blob SHA to the commit and path where it appears.
type blobAttribution struct {
	CommitSHA string
	Path      string
}

// AddAttribution enriches blob matches with commit and path information.
// For each match where ObjectType == "blob" and Path == "", it finds which
// commit(s) contain the blob and what file path it has. Uses
// `git rev-list --all --objects` to build a blob-to-path index.
// Unreachable blobs (not in rev-list output) keep empty Path/CommitSHA.
// When opts.GitDir is set, commands target that git directory.
func AddAttribution(ctx context.Context, results *ScanResults, opts ScanOpts) error {
	if results == nil {
		return nil
	}

	// Collect blob SHAs that need attribution.
	needAttribution := make(map[string]bool)
	for _, m := range results.Matches {
		if m.ObjectType == "blob" && m.Path == "" {
			needAttribution[m.SHA] = true
		}
	}
	if len(needAttribution) == 0 {
		return nil
	}

	// Build blob SHA -> attribution index from rev-list output.
	index, err := buildBlobIndex(ctx, needAttribution, opts)
	if err != nil {
		return fmt.Errorf("build blob index: %w", err)
	}

	// Apply attributions to matches.
	for i := range results.Matches {
		m := &results.Matches[i]
		if m.ObjectType == "blob" && m.Path == "" {
			if attr, ok := index[m.SHA]; ok {
				m.Path = attr.Path
				m.CommitSHA = attr.CommitSHA
			}
		}
	}

	return nil
}

// buildBlobIndex runs `git rev-list --all --objects` and parses the output
// to build a map from blob SHA to (commit, path). Only SHAs present in the
// wantSHAs set are indexed. When opts.GitDir is set, targets that git directory.
//
// rev-list outputs commits in reverse chronological order. Each commit SHA
// appears alone on a line, followed by the SHAs of objects it references
// in the format "<sha> <path>". The commit that "owns" a blob line is the
// most recent commit line that appeared before it.
func buildBlobIndex(ctx context.Context, wantSHAs map[string]bool, opts ScanOpts) (map[string]blobAttribution, error) {
	var stdout string
	var err error
	if opts.GitDir != "" {
		stdout, _, err = git.RunWithGitDir(ctx, opts.GitDir, opts.WorkTree, "rev-list", "--all", "--objects")
	} else {
		stdout, _, err = git.Run(ctx, "rev-list", "--all", "--objects")
	}
	if err != nil {
		return nil, err
	}

	index := make(map[string]blobAttribution)
	var currentCommit string

	scanner := bufio.NewScanner(strings.NewReader(stdout))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		spaceIdx := strings.IndexByte(line, ' ')
		if spaceIdx < 0 {
			// No space: this is a commit SHA.
			currentCommit = line
			continue
		}

		sha := line[:spaceIdx]
		path := line[spaceIdx+1:]

		if !wantSHAs[sha] {
			continue
		}

		// Only record the first attribution (most recent commit).
		if _, exists := index[sha]; !exists {
			index[sha] = blobAttribution{
				CommitSHA: currentCommit,
				Path:      path,
			}
		}
	}

	return index, scanner.Err()
}
