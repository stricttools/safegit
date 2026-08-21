package scan

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/smm-h/safegit/internal/git"
)

// ScanNonObjects scans non-git-object files for the given pattern.
// It checks:
//   - <gitDir>/config (may contain tokens in remote URLs)
//   - <gitDir>/hooks/* (hook scripts, excluding .sample files)
//   - <gitDir>/COMMIT_EDITMSG (last commit message edited)
//   - Working tree files (listed via `git ls-files`)
//
// Binary files are skipped (NUL in first 8KB). Non-existent files are skipped.
func ScanNonObjects(ctx context.Context, pattern *regexp.Regexp, gitDir string) ([]Match, error) {
	var matches []Match

	// Scan git internal files.
	gitFiles := []string{
		filepath.Join(gitDir, "config"),
		filepath.Join(gitDir, "COMMIT_EDITMSG"),
	}

	// Add hook files (skip .sample files and directories).
	hooksDir := filepath.Join(gitDir, "hooks")
	hookEntries, err := os.ReadDir(hooksDir)
	if err == nil {
		for _, entry := range hookEntries {
			if entry.IsDir() {
				continue
			}
			if strings.HasSuffix(entry.Name(), ".sample") {
				continue
			}
			gitFiles = append(gitFiles, filepath.Join(hooksDir, entry.Name()))
		}
	}

	for _, path := range gitFiles {
		fileMatches, err := scanFile(path, pattern)
		if err != nil {
			continue // Skip files that can't be read.
		}
		matches = append(matches, fileMatches...)
	}

	// Scan working tree files.
	worktreeMatches, err := scanWorktreeFiles(ctx, pattern)
	if err != nil {
		return matches, fmt.Errorf("scan working tree: %w", err)
	}
	matches = append(matches, worktreeMatches...)

	return matches, nil
}

// scanFile reads a single file and returns matches for the given pattern.
// Returns nil matches and an error if the file doesn't exist or can't be read.
// Returns nil matches (no error) if the file is binary.
func scanFile(path string, pattern *regexp.Regexp) ([]Match, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	if isBinary(data) {
		return nil, nil
	}

	var matches []Match
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		locs := pattern.FindAllStringIndex(line, -1)
		for _, loc := range locs {
			ctx := buildContext(line, loc[0], loc[1])
			matches = append(matches, Match{
				ObjectType: "file",
				Line:       lineNum,
				Path:       path,
				Context:    ctx,
			})
		}
	}

	return matches, scanner.Err()
}

// scanWorktreeFiles lists tracked files via `git ls-files` and scans each one.
//
// The listed paths are repo-relative, so each one is anchored to the repository
// root before being read: an os.ReadFile resolves against the PROCESS working
// directory, which from a subdirectory is not where git said the file is, and
// the scan would silently report no matches for files it never opened. The
// reported Path stays repo-relative -- that is the identifier a reader wants.
func scanWorktreeFiles(ctx context.Context, pattern *regexp.Regexp) ([]Match, error) {
	stdout, _, err := git.Run(ctx, "ls-files", "-z")
	if err != nil {
		return nil, err
	}

	root, err := git.AnchorRoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving repository root for the working-tree scan: %w", err)
	}

	var matches []Match
	for _, relPath := range strings.Split(stdout, "\x00") {
		if relPath == "" {
			continue
		}

		fileMatches, err := scanFile(git.Anchor(root, relPath), pattern)
		if err != nil {
			continue // Skip unreadable files.
		}
		for i := range fileMatches {
			fileMatches[i].Path = relPath
		}
		matches = append(matches, fileMatches...)
	}

	return matches, nil
}
