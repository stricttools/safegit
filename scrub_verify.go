package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/scan"
)

// verifyScrubbedFileContent is `scrub file`'s Tier A content check: it reads
// the target path out of every REWRITTEN commit and holds it to what the mode
// promised.
//
//   - delete: the path must be absent from every rewritten tree.
//   - replace: wherever the path is present it must hold the replacement blob,
//     and it must never hold one of the blobs the scrub replaced.
//
// The "never an old blob" half is the security property and is checked in both
// modes; the exact-content half is skipped for a target that a --remap-shas-in
// glob also rewrites, because the remap deliberately edits that file after the
// replacement and the two would contradict each other.
func verifyScrubbedFileContent(ctx context.Context, shaMap map[string]string, filePath, mode, newBlobSHA string, oldBlobSHAs map[string]bool, remapGlobs []string) error {
	remapped := matchAnyScope(remapGlobs, filePath)

	var failures []string
	for oldSHA, newSHA := range shaMap {
		if oldSHA == newSHA {
			continue
		}
		got := lookupBlobAtPath(ctx, newSHA, filePath)
		switch {
		case mode == "remove":
			if got != "" {
				failures = append(failures, fmt.Sprintf(
					"commit %s: %q is still present in the rewritten commit", shortSHA(oldSHA), filePath))
			}
		case got == "":
			// The path is not in this commit at all, which is what a commit
			// from before the file existed looks like.
		case oldBlobSHAs[got]:
			failures = append(failures, fmt.Sprintf(
				"commit %s: %q still holds the blob the scrub was replacing (%s)",
				shortSHA(oldSHA), filePath, shortSHA(got)))
		case got != newBlobSHA && !remapped:
			failures = append(failures, fmt.Sprintf(
				"commit %s: %q holds %s, not the replacement blob %s",
				shortSHA(oldSHA), filePath, shortSHA(got), shortSHA(newBlobSHA)))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	sort.Strings(failures)
	return fmt.Errorf("%s", strings.Join(failures, "\n  "))
}

// verifyPatternAbsentFromTips is the Tier A pattern check: it scans exactly the
// objects the rewritten history is made of -- reached from the tips the ref
// update plan is about to publish -- and refuses when the pattern is still
// there.
//
// It runs BEFORE any ref moves, which is the whole point: the rewritten
// commits are unreachable objects at that moment, so the whole-store scan
// cannot tell "the secret survived the rewrite" from "the secret is in the old
// history that has not been pruned yet". Scanning the new tips asks only about
// the history that is about to become the repository's, and a failure costs
// nothing because nothing has moved.
//
// When scope is set, blob matches outside it are expected to survive (the
// operation never claimed to touch them); commit messages and tag annotations
// are always in scope.
func verifyPatternAbsentFromTips(ctx context.Context, pattern *regexp.Regexp, scope *string, tips []string) error {
	if len(tips) == 0 {
		return nil
	}

	opts := scan.ScanOpts{Tips: tips}
	results, err := scan.ScanObjects(ctx, pattern, opts)
	if err != nil {
		return fmt.Errorf("scanning the rewritten history: %w", err)
	}
	if len(results.Matches) == 0 {
		return nil
	}

	if scope != nil {
		if err := scan.AddAttribution(ctx, results, opts); err != nil {
			return fmt.Errorf("attributing matches in the rewritten history: %w", err)
		}
	}

	var failures []scan.Match
	for _, m := range results.Matches {
		if scope != nil && m.ObjectType == "blob" && !matchScope(*scope, m.Path) {
			continue
		}
		failures = append(failures, m)
	}
	if len(failures) == 0 {
		return nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "the pattern is still present in %d object(s) of the rewritten history:", len(failures))
	for _, m := range failures {
		where := m.Path
		if where == "" {
			where = m.ObjectType
		}
		fmt.Fprintf(&sb, "\n    %s %s (%s, line %d): %s", m.ObjectType, shortSHA(m.SHA), where, m.Line, m.Context)
	}
	return fmt.Errorf("%s", sb.String())
}

// verifySecretRemoved re-scans all git objects for the given pattern after a
// scrub rewrite and cleanup. If any matches survive, it returns an error
// listing where they were found. A nil return means the secret is fully gone.
func verifySecretRemoved(ctx context.Context, pattern *regexp.Regexp) error {
	results, err := scan.ScanObjects(ctx, pattern, scan.ScanOpts{EntireHistory: true})
	if err != nil {
		return fmt.Errorf("re-scan failed: %w", err)
	}
	if len(results.Matches) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("secret still present in %d object(s):\n", len(results.Matches)))
	for _, m := range results.Matches {
		reachable := "unreachable"
		if m.Reachable {
			reachable = "reachable"
		}
		sb.WriteString(fmt.Sprintf("  %s %s (%s, line %d): %s\n",
			m.ObjectType, shortSHA(m.SHA), reachable, m.Line, m.Context))
	}
	return fmt.Errorf("%s", sb.String())
}

// verifyOldBlobsRemoved checks that each of the given old blob SHAs no longer
// exists in the object store, unless it is still reachable from a current ref
// (which happens when the scrub range didn't cover all commits that reference
// the blob). Returns an error listing any unreachable-but-surviving blobs, or
// nil if all are gone or accounted for.
func verifyOldBlobsRemoved(ctx context.Context, oldBlobSHAs []string) error {
	// Build the set of all blob SHAs reachable from current refs.
	reachableBlobs, err := buildReachableBlobSet(ctx)
	if err != nil {
		return fmt.Errorf("building reachable blob set: %w", err)
	}

	var surviving []string
	for _, sha := range oldBlobSHAs {
		// If the blob is still reachable from a current ref (e.g., a branch
		// outside the scrub range), it's expected to survive.
		if reachableBlobs[sha] {
			continue
		}
		// Check if the object still exists in the store.
		_, _, err := git.Run(ctx, "cat-file", "-e", sha)
		if err == nil {
			// Object exists but is unreachable -- it should have been pruned.
			surviving = append(surviving, sha)
		}
	}
	if len(surviving) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d old blob(s) still present in object store:\n", len(surviving)))
	for _, sha := range surviving {
		sb.WriteString(fmt.Sprintf("  blob %s\n", shortSHA(sha)))
	}
	return fmt.Errorf("%s", sb.String())
}

// buildReachableBlobSet returns the set of all blob SHAs reachable from any
// ref. Uses git rev-list --all --objects which lists all reachable objects
// with their paths (blobs have paths, commits/trees don't always).
func buildReachableBlobSet(ctx context.Context) (map[string]bool, error) {
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
		// Lines are "<sha>" (commits/trees) or "<sha> <path>" (blobs).
		sha := line
		if idx := strings.IndexByte(line, ' '); idx > 0 {
			sha = line[:idx]
		}
		set[sha] = true
	}
	return set, nil
}
