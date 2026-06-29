package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/scan"
)

// verifyScrub checks that a scrub rewrite preserved everything except the
// target file's blob. It returns a list of failure messages (empty means all
// checks passed) and the total number of individual checks performed.
func verifyScrub(ctx context.Context, shaMap map[string]string, filePath string, verbose bool) ([]string, int) {
	var failures []string
	checks := 0

	// Collect rewritten commits (where old SHA != new SHA).
	var rewrittenOld []string
	for old, new_ := range shaMap {
		if old != new_ {
			rewrittenOld = append(rewrittenOld, old)
		}
	}

	// --- Check 1: Commit count preserved ---
	// Count all reachable commits before (using old refs that are still in
	// the object store) is not possible after updateRefs already ran. Instead,
	// verify that the total reachable commit count equals what we expect:
	// the shaMap should account for every commit in the rewritten range, and
	// the global count should be unchanged. We use rev-list --count --all on
	// the current (post-rewrite) state and compare against the shaMap size
	// plus any commits outside the rewritten range.
	//
	// Simpler approach: count all commits now, and count the shaMap entries.
	// Every shaMap entry (identity or rewritten) represents one commit in the
	// range. The total should equal the post-rewrite count minus commits
	// outside the range. Since we can't easily count "outside range" commits
	// after the rewrite, we use a different strategy: verify that every
	// rewritten new SHA is reachable.
	countOut, _, err := git.Run(ctx, "rev-list", "--count", "--all")
	if err != nil {
		failures = append(failures, fmt.Sprintf("check 1 (commit count): failed to count commits: %v", err))
	} else {
		var postCount int
		fmt.Sscanf(strings.TrimSpace(countOut), "%d", &postCount)
		// Every new SHA in the shaMap should be reachable. The total reachable
		// count should be >= len(shaMap) (there may be commits outside the range).
		// More importantly, identity-mapped + rewritten should sum to len(shaMap).
		checks++
		if postCount < len(shaMap) {
			failures = append(failures, fmt.Sprintf(
				"check 1 (commit count): post-rewrite reachable commits (%d) < shaMap size (%d)",
				postCount, len(shaMap)))
		}
	}

	// --- Checks 2-5: parse each rewritten commit pair once ---
	for _, oldSHA := range rewrittenOld {
		newSHA := shaMap[oldSHA]

		oldInfo, err := git.ParseCommit(ctx, oldSHA)
		if err != nil {
			failures = append(failures, fmt.Sprintf(
				"checks 2-5: failed to parse old commit %s: %v", oldSHA[:12], err))
			continue
		}
		newInfo, err := git.ParseCommit(ctx, newSHA)
		if err != nil {
			failures = append(failures, fmt.Sprintf(
				"checks 2-5: failed to parse new commit %s: %v", newSHA[:12], err))
			continue
		}

		// --- Check 2: Commit messages preserved ---
		checks++
		if oldInfo.Message != newInfo.Message {
			failures = append(failures, fmt.Sprintf(
				"check 2 (messages): commit %s message changed: old=%q, new=%q",
				oldSHA[:12], truncate(oldInfo.Message, 60), truncate(newInfo.Message, 60)))
		}

		// --- Check 3: Author/committer preserved ---
		checks++
		if oldInfo.Author != newInfo.Author {
			failures = append(failures, fmt.Sprintf(
				"check 3 (author/committer): commit %s author changed: old=%v, new=%v",
				oldSHA[:12], oldInfo.Author, newInfo.Author))
		}
		checks++
		if oldInfo.Committer != newInfo.Committer {
			failures = append(failures, fmt.Sprintf(
				"check 3 (author/committer): commit %s committer changed: old=%v, new=%v",
				oldSHA[:12], oldInfo.Committer, newInfo.Committer))
		}

		// --- Check 4: Parent topology preserved ---
		checks++
		if len(oldInfo.Parents) != len(newInfo.Parents) {
			failures = append(failures, fmt.Sprintf(
				"check 4 (parent topology): commit %s parent count changed: old=%d, new=%d",
				oldSHA[:12], len(oldInfo.Parents), len(newInfo.Parents)))
		} else {
			for i, oldParent := range oldInfo.Parents {
				newParent := newInfo.Parents[i]
				// The new parent should be the remapped version of the old parent.
				expectedParent := oldParent
				if mapped, ok := shaMap[oldParent]; ok {
					expectedParent = mapped
				}
				checks++
				if newParent != expectedParent {
					failures = append(failures, fmt.Sprintf(
						"check 4 (parent topology): commit %s parent[%d] mismatch: expected %s, got %s",
						oldSHA[:12], i, expectedParent[:12], newParent[:12]))
				}
			}
		}

		// --- Check 5: Only target file changed ---
		// If trees are identical, nothing to check (commit was inherited-only).
		if oldInfo.Tree == newInfo.Tree {
			checks++
			continue
		}

		oldEntries, err := git.LsTreeAll(ctx, oldInfo.Tree)
		if err != nil {
			failures = append(failures, fmt.Sprintf(
				"check 5 (only target changed): failed to ls-tree old commit %s: %v", oldSHA[:12], err))
			continue
		}
		newEntries, err := git.LsTreeAll(ctx, newInfo.Tree)
		if err != nil {
			failures = append(failures, fmt.Sprintf(
				"check 5 (only target changed): failed to ls-tree new commit %s: %v", newSHA[:12], err))
			continue
		}

		// Build maps from path -> blob SHA for comparison.
		oldMap := make(map[string]string, len(oldEntries))
		for _, e := range oldEntries {
			oldMap[e.Path] = e.SHA
		}
		newMap := make(map[string]string, len(newEntries))
		for _, e := range newEntries {
			newMap[e.Path] = e.SHA
		}

		checks++

		// Check that no non-target file's blob changed.
		for path, oldBlob := range oldMap {
			if path == filePath {
				continue // target file is expected to change
			}
			newBlob, ok := newMap[path]
			if !ok {
				failures = append(failures, fmt.Sprintf(
					"check 5 (only target changed): commit %s: non-target file %q was removed",
					oldSHA[:12], path))
			} else if oldBlob != newBlob {
				failures = append(failures, fmt.Sprintf(
					"check 5 (only target changed): commit %s: non-target file %q blob changed: %s -> %s",
					oldSHA[:12], path, oldBlob[:12], newBlob[:12]))
			}
		}

		// Check that no new non-target file appeared.
		for path := range newMap {
			if path == filePath {
				continue
			}
			if _, ok := oldMap[path]; !ok {
				failures = append(failures, fmt.Sprintf(
					"check 5 (only target changed): commit %s: unexpected new file %q appeared",
					oldSHA[:12], path))
			}
		}
	}

	// --- Check 6: Tag names preserved ---
	// Capture tag names after rewrite and compare to what we can infer.
	// Since we don't have a pre-rewrite snapshot, we check that all tags
	// point to valid commits and that tag names are preserved by verifying
	// the tag list is non-empty if shaMap is non-empty (a basic sanity check).
	// A more thorough check: verify that every tag whose target was in shaMap
	// now points to the remapped SHA.
	tagOut, _, err := git.Run(ctx, "for-each-ref", "--format=%(refname:short) %(objecttype) %(*objectname) %(objectname)", "refs/tags/")
	if err != nil {
		failures = append(failures, fmt.Sprintf("check 6 (tag names): failed to list tags: %v", err))
	} else {
		tagLines := git.SplitNonEmpty(tagOut)
		for _, line := range tagLines {
			parts := strings.Fields(line)
			if len(parts) < 3 {
				continue
			}
			tagName := parts[0]
			objType := parts[1]
			var targetSHA string
			if objType == "tag" && len(parts) >= 4 {
				// Annotated tag: deref target is parts[2], tag object is parts[3]
				targetSHA = parts[2]
			} else {
				// Lightweight tag: commit SHA is the last field
				targetSHA = parts[len(parts)-1]
			}

			checks++
			// Verify the target commit is reachable.
			_, _, err := git.Run(ctx, "cat-file", "-t", targetSHA)
			if err != nil {
				failures = append(failures, fmt.Sprintf(
					"check 6 (tag names): tag %q points to unreachable object %s", tagName, targetSHA[:12]))
			}

			// Stale-pointer detection: shaMap keys are OLD (pre-rewrite) SHAs,
			// values are NEW (post-rewrite) SHAs. After updateRefs, every tag
			// should point to a NEW SHA. If a tag's target is a key that maps
			// to a different value, the tag still points to the old commit.
			checks++
			if mapped, ok := shaMap[targetSHA]; ok && mapped != targetSHA {
				failures = append(failures, fmt.Sprintf(
					"check 6 (tag stale pointer): tag %q points to old SHA %s, should point to remapped SHA %s",
					tagName, targetSHA[:12], mapped[:12]))
			}
		}
	}

	// --- Check 7: Branch refs remapped ---
	// Every branch should point to a post-rewrite SHA. If a branch's target
	// appears as a key in shaMap that maps to a different value, the branch
	// still points to a pre-rewrite commit.
	branchOut, _, err := git.Run(ctx, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	if err != nil {
		failures = append(failures, fmt.Sprintf("check 7 (branch refs): failed to list branches: %v", err))
	} else {
		branchLines := git.SplitNonEmpty(branchOut)
		for _, line := range branchLines {
			parts := strings.Fields(line)
			if len(parts) != 2 {
				continue
			}
			refname := parts[0]
			commitSHA := parts[1]

			checks++
			if mapped, ok := shaMap[commitSHA]; ok && mapped != commitSHA {
				failures = append(failures, fmt.Sprintf(
					"check 7 (branch stale pointer): branch %q points to old SHA %s, should point to remapped SHA %s",
					refname, commitSHA[:12], mapped[:12]))
			}
		}
	}

	return failures, checks
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

	// Exclude matches from the tracked policy file, which legitimately
	// contains pattern strings.
	policyBlobs, _ := buildScopedBlobSet(ctx, ".safegit/scrub-policies.jsonl")
	var matches []scan.Match
	for _, m := range results.Matches {
		if m.ObjectType == "blob" && len(policyBlobs) > 0 && policyBlobs[m.SHA] {
			continue
		}
		matches = append(matches, m)
	}
	if len(matches) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("secret still present in %d object(s):\n", len(matches)))
	for _, m := range matches {
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

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
