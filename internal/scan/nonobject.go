package scan

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/hooks"
	"github.com/smm-h/safegit/internal/repo"
)

// rewriteJournalFile is the one file under .git/safegit the sweep skips.
//
// It is safegit's rewrite journal: old-SHA to new-SHA maps written by every
// history rewrite, so downstream tooling can repair references a rewrite moved.
// Every line of it is object names and nothing else -- it can hold no secret,
// because a scrub's replacement never reaches it -- and it grows without bound
// in a repository that rewrites often, so reading it on every scan is cost with
// no possible finding.
const rewriteJournalFile = "rewrite-maps.jsonl"

// nonObjectFile is one file to sweep, with the coordinate its matches are
// reported in.
type nonObjectFile struct {
	// abs is the path the file is read at.
	abs string
	// rel is how the match reports the file: repo-relative for anything in the
	// work tree, git-dir-relative for anything inside the git directory.
	rel string
	// inGitDir marks which of those two coordinate systems rel is in. Without
	// it a reader cannot tell `hooks/pre-commit` inside .git from a tracked
	// file of the same name in the work tree.
	inGitDir bool
}

// ScanNonObjects scans non-git-object files for the given pattern.
//
// The set it covers is everything a secret can sit in without being a git
// object:
//
//   - <gitDir>/config (tokens in remote URLs) and <gitDir>/COMMIT_EDITMSG;
//   - every hook, at any depth, in BOTH the directory git runs hooks from (git
//     resolves it, so core.hooksPath and linked worktrees are followed) and
//     safegit's own hook stores, which is the location enumerator's answer --
//     one authority, so a store the enumerator learns about is swept without
//     this file being told about it;
//   - everything under <gitDir>/safegit except the rewrite journal;
//   - the tracked working-tree files git lists.
//
// Binary files are skipped (NUL in first 8KB). Non-existent files are skipped.
func ScanNonObjects(ctx context.Context, pattern *regexp.Regexp, gitDir string) ([]Match, error) {
	root, err := git.AnchorRoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving repository root for the non-object scan: %w", err)
	}

	files, err := gitDirFiles(ctx, gitDir, root)
	if err != nil {
		return nil, err
	}

	worktree, err := worktreeFiles(ctx, gitDir, root)
	if err != nil {
		return files2matches(files, pattern), fmt.Errorf("scan working tree: %w", err)
	}
	files = append(files, worktree...)

	return files2matches(files, pattern), nil
}

// files2matches reads each file once and collects its matches, skipping paths
// that were listed twice (a hook store reachable through two enumerations) and
// files that cannot be read.
func files2matches(files []nonObjectFile, pattern *regexp.Regexp) []Match {
	var matches []Match
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f.abs] {
			continue
		}
		seen[f.abs] = true
		fileMatches, err := scanFile(f.abs, pattern)
		if err != nil {
			continue // Skip files that can't be read.
		}
		for i := range fileMatches {
			fileMatches[i].Path = f.rel
			fileMatches[i].InGitDir = f.inGitDir
		}
		matches = append(matches, fileMatches...)
	}
	return matches
}

// gitDirFiles lists the files to sweep inside the git directory, each with its
// git-dir-relative coordinate.
func gitDirFiles(ctx context.Context, gitDir, root string) ([]nonObjectFile, error) {
	var out []nonObjectFile
	add := func(abs string) {
		out = append(out, nonObjectFile{abs: abs, rel: relToBase(gitDir, abs), inGitDir: true})
	}

	add(filepath.Join(gitDir, "config"))
	add(filepath.Join(gitDir, "COMMIT_EDITMSG"))

	// The directory git runs ITS hooks from, which is not always <gitDir>/hooks:
	// core.hooksPath redirects it, and a linked worktree uses the common git
	// dir's. Sample hooks are git's own shipped examples and hold no secrets.
	hooksDir, err := git.HooksDir(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving git's hook directory: %w", err)
	}
	nativeHooks, err := walkFiles(hooksDir)
	if err != nil {
		return nil, err
	}
	for _, p := range nativeHooks {
		if strings.HasSuffix(p, ".sample") {
			continue
		}
		add(p)
	}

	// Everything else safegit keeps, minus the rewrite journal.
	safegitDir := filepath.Join(gitDir, "safegit")
	stateFiles, err := walkFiles(safegitDir)
	if err != nil {
		return nil, err
	}
	for _, p := range stateFiles {
		if filepath.Base(p) == rewriteJournalFile {
			continue
		}
		add(p)
	}

	// The hook stores as the enumerator sees them, keyed on the COMMON git dir:
	// the live store and the legacy location are repository-level, so a sweep
	// run from a linked worktree must reach the same files a push there runs.
	// They are largely covered above already; taking the union rather than a
	// subset is what keeps this sweep correct when the enumerator learns about
	// a new place.
	locations, err := hooks.Enumerate(hooks.Store{Worktree: root, SharedGitDir: repo.SharedGitDir(ctx, gitDir)})
	if err != nil {
		return nil, err
	}
	for _, loc := range locations {
		if loc.Origin == hooks.OriginTracked {
			continue // work-tree coordinate; added with the work tree below.
		}
		add(loc.Path)
	}

	return out, nil
}

// worktreeFiles lists the tracked files git reports plus the committed hook
// store, each with its repo-relative coordinate.
//
// The listing runs under the caller's context, so a scan started from a
// subdirectory still lists the whole repository, and each path is anchored to
// the repository root before being read: an os.ReadFile resolves against the
// PROCESS working directory, which from a subdirectory is not where git said
// the file is, and the scan would silently report no matches for files it never
// opened. The reported path stays repo-relative -- that is the identifier a
// reader wants.
func worktreeFiles(ctx context.Context, gitDir, root string) ([]nonObjectFile, error) {
	stdout, _, err := git.Run(ctx, "ls-files", "-z")
	if err != nil {
		return nil, err
	}

	var out []nonObjectFile
	for _, relPath := range strings.Split(stdout, "\x00") {
		if relPath == "" {
			continue
		}
		out = append(out, nonObjectFile{abs: git.Anchor(root, relPath), rel: relPath})
	}

	// The hook store the checkout provides, whose files run on every push. A
	// hook already committed is in the listing above; one that is not yet
	// committed is not, and it runs just the same -- membership in that store is
	// the directory, never git's tracking.
	locations, err := hooks.Enumerate(hooks.Store{Worktree: root, SharedGitDir: repo.SharedGitDir(ctx, gitDir)})
	if err != nil {
		return nil, err
	}
	for _, loc := range locations {
		if loc.Origin != hooks.OriginTracked {
			continue
		}
		out = append(out, nonObjectFile{abs: loc.Path, rel: relToBase(root, loc.Path)})
	}

	return out, nil
}

// walkFiles returns every file under root, recursively. An absent root is an
// empty list, not an error.
func walkFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking %s: %w", root, err)
	}
	return out, nil
}

// relToBase renders path relative to base, or returns the absolute path when it
// does not sit under base at all -- which a redirected core.hooksPath can do.
// An unrelated path reported relative would be a coordinate pointing at a file
// that is not there.
func relToBase(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return filepath.ToSlash(rel)
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
