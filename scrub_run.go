package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/scan"
)

// ScrubRunResult is the JSON output for `scrub run` in execute mode.
type ScrubRunResult struct {
	Version          int               `json:"version"`
	DryRun           bool              `json:"dry_run"`
	Rewrites         map[string]string `json:"rewrites"`
	Tags             []TagRewrite      `json:"tags"`
	CommitsRewritten int               `json:"commits_rewritten"`
	BlobsReplaced    int               `json:"blobs_replaced"`
	MessagesModified int               `json:"messages_modified"`
	TagsRewritten    int               `json:"tags_rewritten"`
	OperationCount   int               `json:"operation_count"`
	OldHead          string            `json:"old_head"`
	NewHead          string            `json:"new_head"`
}

// ScrubRunDiffEntry is a single blob diff in --diff preview output.
type ScrubRunDiffEntry struct {
	OldSHA string   `json:"old_sha"`
	NewSHA string   `json:"new_sha"`
	Paths  []string `json:"paths"`
	Diff   string   `json:"diff"`
}

// ScrubRunDiffResult is the JSON output for `scrub run --diff`.
type ScrubRunDiffResult struct {
	Version        int                 `json:"version"`
	Diff           bool                `json:"diff"`
	OperationCount int                 `json:"operation_count"`
	BlobDiffs      []ScrubRunDiffEntry `json:"blob_diffs"`
	MessageDiffs   []MessageDiffEntry  `json:"message_diffs,omitempty"`
	TotalBlobs     int                 `json:"total_blobs"`
	Shown          int                 `json:"shown"`
	Truncated      bool                `json:"truncated"`
}

// MessageDiffEntry is a commit message diff in --diff preview output.
type MessageDiffEntry struct {
	CommitSHA  string `json:"commit_sha"`
	OldMessage string `json:"old_message"`
	NewMessage string `json:"new_message"`
}

func runScrubRun(flags globalFlags, kwargs map[string]interface{}) int {
	const cmd = "scrub run"

	recipePath := kwargs["recipe"].(string)
	reason := kwargs["reason"].(string)
	diffMode := kwargs["diff"].(bool)

	var from *string
	if v := kwargs["from"]; v != nil {
		s := v.(string)
		from = &s
	}
	entireHistory := kwargs["entire_history"].(bool)

	var limit int
	if v := kwargs["limit"]; v != nil {
		limit = v.(int)
	} else {
		limit = 50
	}

	// Validation
	gitDir := mustGitDir(flags, cmd)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		die(flags, cmd, 4, err.Error())
	}

	ctx := context.Background()
	requireCleanTree(ctx, flags, cmd)

	// Parse recipe
	recipe, err := parseRecipe(recipePath)
	if err != nil {
		die(flags, cmd, 2, fmt.Sprintf("parsing recipe: %v", err))
	}

	infof(flags, "Recipe loaded: %d operations\n", len(recipe.Operations))

	sgDir := repo.SafegitDir(gitDir)

	// Acquire rewrite lock
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("loading config: %v", err))
	}
	timeout := time.Duration(cfg.Lock.AcquireTimeoutSeconds) * time.Second
	sharedDir := repo.SharedSafegitDir(ctx, gitDir)
	lk, err := lock.Acquire(sharedDir, sgDir, "safegit/rewrite", "scrub-run", timeout)
	if err != nil {
		die(flags, cmd, 1, "another rewrite operation is in progress")
	}
	defer lk.Release()

	// Resolve --from if provided
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

	// Neither --from nor --entire-history provided
	if from == nil && !entireHistory {
		die(flags, cmd, 2, "one of --from or --entire-history is required")
	}

	// --diff preview mode: build blob map via range-scoped scan, then show diffs.
	if diffMode {
		combinedPatternParts := make([]string, len(recipe.Operations))
		for i, op := range recipe.Operations {
			combinedPatternParts[i] = "(?:" + op.Pattern + ")"
		}
		combinedPattern, err := regexp.Compile(strings.Join(combinedPatternParts, "|"))
		if err != nil {
			die(flags, cmd, 2, fmt.Sprintf("compiling combined pattern: %v", err))
		}

		infof(flags, "Scanning objects...\n")
		results, err := scan.ScanObjects(ctx, combinedPattern, scan.ScanOpts{
			FromSHA:       fromSHA,
			EntireHistory: entireHistory,
		})
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("scanning objects: %v", err))
		}

		if len(results.Matches) == 0 {
			infof(flags, "No matches found. Nothing to rewrite.\n")
			return 0
		}

		uniqueBlobSHAs := make(map[string]bool)
		for _, m := range results.Matches {
			if m.ObjectType == "blob" {
				uniqueBlobSHAs[m.SHA] = true
			}
		}
		blobSHAList := make([]string, 0, len(uniqueBlobSHAs))
		for sha := range uniqueBlobSHAs {
			blobSHAList = append(blobSHAList, sha)
		}

		blobMap, err := buildRecipeBlobMap(ctx, recipe, blobSHAList)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("building blob map: %v", err))
		}

		return scrubRunDiff(ctx, flags, cmd, recipe, blobMap, fromSHA, entireHistory, limit)
	}

	// Execute the recipe via the shared pipeline.
	exitCode, _ := executeScrubRecipe(ctx, flags, cmd, recipe, reason, fromSHA, entireHistory, nil, gitDir, sgDir, nil)

	// Append scrub policies for each operation.
	for _, op := range recipe.Operations {
		policy := ScrubPolicy{
			Type:        "match",
			Pattern:     op.Pattern,
			Reason:      reason,
			CreatedByOp: "scrub-run",
		}
		if op.Scope != nil {
			policy.Scope = *op.Scope
		}
		if err := appendScrubPolicy(sgDir, policy); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to append scrub policy: %v\n", err)
		}
	}

	return exitCode
}

// opScope returns the scope pointer for a recipe operation, or nil if unset.
func opScope(op *RecipeOperation) *string {
	return op.Scope
}

// rewriteTagAnnotationsRecipe rewrites tag annotations using recipe operations.
// Each operation is applied in topological order, and only operations targeting
// "tags" (or all targets) are applied.
func rewriteTagAnnotationsRecipe(ctx context.Context, flags globalFlags, cmd string, recipe *ParsedRecipe, shaMap map[string]string) ([]TagRewrite, int) {
	tagRewrites, tagsRewritten, err := forEachAnnotatedTag(ctx, shaMap, func(refname, header, body string) (string, error) {
		newBody := body
		for _, idx := range recipe.TopoOrder {
			op := recipe.Operations[idx]
			// Skip if this op doesn't target tags
			if op.Target != nil && *op.Target != "tags" {
				continue
			}
			pat := recipe.Patterns[idx]
			if !pat.MatchString(newBody) {
				continue
			}
			if op.Mangle {
				newBody = pat.ReplaceAllStringFunc(newBody, func(s string) string {
					return string(mangleBytes([]byte(s)))
				})
			} else {
				newBody = pat.ReplaceAllString(newBody, *op.Replace)
			}
		}
		return newBody, nil
	})
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("rewriting tag annotations: %v", err))
	}
	if tagsRewritten > 0 && flags.verbose {
		for _, tr := range tagRewrites {
			fmt.Fprintf(os.Stderr, "  tag annotation %s: %s -> %s\n", tr.Refname, shortSHA(tr.OldSHA), shortSHA(tr.NewSHA))
		}
	}
	return tagRewrites, tagsRewritten
}

// scrubRunDiff previews what a recipe would change without modifying any objects.
// It reads old and new blob content, produces unified diffs, and also shows
// commit message diffs for commits in the rewrite range.
func scrubRunDiff(ctx context.Context, flags globalFlags, cmd string, recipe *ParsedRecipe, blobMap map[string]string, fromSHA string, entireHistory bool, limit int) int {
	// Add attribution to identify file paths for each blob
	// We do this by collecting all old blob SHAs and finding their paths.
	oldBlobSHAs := make([]string, 0, len(blobMap))
	for oldSHA := range blobMap {
		oldBlobSHAs = append(oldBlobSHAs, oldSHA)
	}

	// Build path attribution: SHA -> []paths
	blobPaths, err := buildBlobPathMap(ctx)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("building blob path map: %v", err))
	}

	// Produce blob diffs
	var blobDiffs []ScrubRunDiffEntry
	shown := 0
	truncated := false
	for oldSHA, newSHA := range blobMap {
		if shown >= limit {
			truncated = true
			break
		}

		oldContent, err := git.CatFileBlob(ctx, oldSHA)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: reading old blob %s: %v\n", shortSHA(oldSHA), err)
			continue
		}

		// For --diff, we already have the new SHA in the blobMap; the new blob
		// was written by buildRecipeBlobMap. Read it.
		newContent, err := git.CatFileBlob(ctx, newSHA)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: reading new blob %s: %v\n", shortSHA(newSHA), err)
			continue
		}

		if isBinaryContent(oldContent) {
			continue
		}

		paths := blobPaths[oldSHA]
		pathLabel := shortSHA(oldSHA)
		if len(paths) > 0 {
			pathLabel = paths[0]
		}

		diff := unifiedDiff(pathLabel, string(oldContent), string(newContent))

		entry := ScrubRunDiffEntry{
			OldSHA: oldSHA,
			NewSHA: newSHA,
			Paths:  paths,
			Diff:   diff,
		}
		blobDiffs = append(blobDiffs, entry)
		shown++

		if !flags.json {
			if len(paths) > 0 {
				infof(flags, "--- %s (blob %s)\n", strings.Join(paths, ", "), shortSHA(oldSHA))
			} else {
				infof(flags, "--- blob %s\n", shortSHA(oldSHA))
			}
			infof(flags, "+++ blob %s\n", shortSHA(newSHA))
			infof(flags, "%s\n", diff)
		}
	}

	// Collect commit message diffs
	var messageDiffs []MessageDiffEntry
	var shas []string
	if entireHistory {
		out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", "HEAD")
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("listing commits for diff: %v", err))
		}
		shas = git.SplitNonEmpty(out)
	} else if fromSHA != "" {
		out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", fromSHA+"..HEAD")
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("listing commits for diff: %v", err))
		}
		shas = append([]string{fromSHA}, git.SplitNonEmpty(out)...)
	}

	for _, sha := range shas {
		info, err := git.ParseCommit(ctx, sha)
		if err != nil {
			continue
		}

		newMessage := info.Message
		for _, idx := range recipe.TopoOrder {
			op := recipe.Operations[idx]
			if op.Target != nil && *op.Target != "commits" {
				continue
			}
			pat := recipe.Patterns[idx]
			if !pat.MatchString(newMessage) {
				continue
			}
			if op.Mangle {
				newMessage = pat.ReplaceAllStringFunc(newMessage, func(s string) string {
					return string(mangleBytes([]byte(s)))
				})
			} else {
				newMessage = pat.ReplaceAllString(newMessage, *op.Replace)
			}
		}

		if newMessage != info.Message {
			entry := MessageDiffEntry{
				CommitSHA:  sha,
				OldMessage: info.Message,
				NewMessage: newMessage,
			}
			messageDiffs = append(messageDiffs, entry)

			if !flags.json {
				infof(flags, "--- commit %s message\n", shortSHA(sha))
				diff := unifiedDiff("commit-message", info.Message, newMessage)
				infof(flags, "%s\n", diff)
			}
		}
	}

	if !flags.json {
		infof(flags, "\nDiff preview: %d blobs would change", len(blobMap))
		if truncated {
			infof(flags, " (showing %d of %d)", shown, len(blobMap))
		}
		infof(flags, "\n")
		if len(messageDiffs) > 0 {
			infof(flags, "  %d commit messages would change\n", len(messageDiffs))
		}
		infof(flags, "No objects were modified.\n")
	}

	if flags.json {
		result := ScrubRunDiffResult{
			Version:        1,
			Diff:           true,
			OperationCount: len(recipe.Operations),
			BlobDiffs:      blobDiffs,
			MessageDiffs:   messageDiffs,
			TotalBlobs:     len(blobMap),
			Shown:          shown,
			Truncated:      truncated,
		}
		if result.BlobDiffs == nil {
			result.BlobDiffs = []ScrubRunDiffEntry{}
		}
		emitJSON(result)
	}

	return 0
}

// buildBlobPathMap uses `git rev-list --all --objects` to build a map from
// blob SHA to the list of file paths where that blob appears.
func buildBlobPathMap(ctx context.Context) (map[string][]string, error) {
	stdout, _, err := git.Run(ctx, "rev-list", "--all", "--objects")
	if err != nil {
		return nil, fmt.Errorf("rev-list --all --objects: %w", err)
	}

	result := make(map[string][]string)
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		spaceIdx := strings.IndexByte(line, ' ')
		if spaceIdx < 0 {
			continue
		}
		sha := line[:spaceIdx]
		filePath := line[spaceIdx+1:]
		// Deduplicate paths per SHA
		existing := result[sha]
		found := false
		for _, p := range existing {
			if p == filePath {
				found = true
				break
			}
		}
		if !found {
			result[sha] = append(existing, filePath)
		}
	}
	return result, nil
}

// unifiedDiff produces a simple unified diff between old and new content.
// Uses a line-by-line comparison to generate -/+ hunks.
func unifiedDiff(label, oldContent, newContent string) string {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	var buf bytes.Buffer

	// Simple line-by-line diff: walk both sides and emit changes.
	// This is a basic O(n) diff for cases where lines are mostly aligned.
	// For a more sophisticated diff, we would use an LCS algorithm, but for
	// preview purposes this is sufficient since recipe replacements typically
	// change content within lines rather than rearranging them.
	maxLen := len(oldLines)
	if len(newLines) > maxLen {
		maxLen = len(newLines)
	}

	i, j := 0, 0
	for i < len(oldLines) || j < len(newLines) {
		if i < len(oldLines) && j < len(newLines) {
			if oldLines[i] == newLines[j] {
				buf.WriteString(" " + oldLines[i] + "\n")
				i++
				j++
			} else {
				// Find how many lines differ before they re-sync
				buf.WriteString("-" + oldLines[i] + "\n")
				buf.WriteString("+" + newLines[j] + "\n")
				i++
				j++
			}
		} else if i < len(oldLines) {
			buf.WriteString("-" + oldLines[i] + "\n")
			i++
		} else {
			buf.WriteString("+" + newLines[j] + "\n")
			j++
		}
	}

	return buf.String()
}
