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

	// Build a combined regex that matches ANY operation's pattern. This is used
	// to find candidate blobs efficiently in one pass.
	combinedPatternParts := make([]string, len(recipe.Operations))
	for i, op := range recipe.Operations {
		combinedPatternParts[i] = "(?:" + op.Pattern + ")"
	}
	combinedPattern, err := regexp.Compile(strings.Join(combinedPatternParts, "|"))
	if err != nil {
		die(flags, cmd, 2, fmt.Sprintf("compiling combined pattern: %v", err))
	}

	// Scan for matching blobs using the combined pattern
	infof(flags, "Scanning objects...\n")
	results, err := scan.ScanObjects(ctx, combinedPattern)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("scanning objects: %v", err))
	}

	if len(results.Matches) == 0 {
		infof(flags, "No matches found. Nothing to rewrite.\n")
		return 0
	}

	// Collect unique blob SHAs from matches, respecting per-operation scope filters.
	uniqueBlobSHAs := make(map[string]bool)
	var commitMatchCount, tagMatchCount int
	for _, m := range results.Matches {
		switch m.ObjectType {
		case "blob":
			uniqueBlobSHAs[m.SHA] = true
		case "commit":
			commitMatchCount++
		case "tag":
			tagMatchCount++
		}
	}

	blobSHAList := make([]string, 0, len(uniqueBlobSHAs))
	for sha := range uniqueBlobSHAs {
		blobSHAList = append(blobSHAList, sha)
	}

	// Build the combined blob map via the recipe.
	infof(flags, "Building blob replacement map (%d candidate blobs)...\n", len(blobSHAList))
	blobMap, err := buildRecipeBlobMap(ctx, recipe, blobSHAList)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("building blob map: %v", err))
	}

	infof(flags, "Found %d blobs to replace, %d commit message matches, %d tag matches\n",
		len(blobMap), commitMatchCount, tagMatchCount)

	// --diff preview mode: show diffs without modifying anything
	if diffMode {
		return scrubRunDiff(ctx, flags, cmd, recipe, blobMap, fromSHA, entireHistory, limit)
	}

	if len(blobMap) == 0 && commitMatchCount == 0 && tagMatchCount == 0 {
		infof(flags, "No replacements needed. Nothing to rewrite.\n")
		return 0
	}

	// Confirmation prompt
	if !confirmOrAbort(flags, "This will rewrite history using %d recipe operations. This cannot be undone. Proceed?", len(recipe.Operations)) {
		infof(flags, "Aborted.\n")
		return 0
	}

	// Capture old HEAD
	oldHeadSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("resolving HEAD: %v", err))
	}

	// Determine commit range
	var shas []string
	if entireHistory {
		out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", "HEAD")
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("listing commits: %v", err))
		}
		shas = git.SplitNonEmpty(out)
	} else {
		out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", fromSHA+"..HEAD")
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("listing commits: %v", err))
		}
		shas = append([]string{fromSHA}, git.SplitNonEmpty(out)...)
	}

	commitCount := len(shas)
	infof(flags, "Rewriting %d commits...\n", commitCount)

	messagesModified := 0
	treeCache := make(map[string]string)

	shaMap, rewrittenCount, err := walkAndRewrite(ctx, shas, func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string) (CommitTransform, error) {
		var xform CommitTransform

		// Replace blobs in tree
		newTreeSHA, err := replaceInTreeByBlobMap(ctx, info.Tree, blobMap, nil, treeCache)
		if err != nil {
			return CommitTransform{}, fmt.Errorf("replacing blobs in tree for commit %s: %w", sha, err)
		}
		if newTreeSHA != info.Tree {
			xform.TreeSHA = newTreeSHA
		}

		// Apply recipe operations to commit messages in topo order,
		// respecting per-op target filters.
		newMessage := info.Message
		for _, idx := range recipe.TopoOrder {
			op := recipe.Operations[idx]
			// Skip if this op doesn't target commits
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
			xform.Message = newMessage
			messagesModified++
		}

		return xform, nil
	}, flags.verbose)
	if err != nil {
		die(flags, cmd, 1, err.Error())
	}

	// Oplog extra
	oplogExtra := map[string]interface{}{
		"recipe":           recipePath,
		"reason":           reason,
		"operations":       len(recipe.Operations),
		"blobsReplaced":    len(blobMap),
		"messagesModified": messagesModified,
	}

	// Annotation rewrite closure: applies recipe operations to tag annotations,
	// respecting per-op target filters for "tags".
	var annotationTagRewrites []TagRewrite
	var tagsRewritten int
	parentAnnotFunc := func(ctx context.Context, shaMap map[string]string) error {
		annotationTagRewrites, tagsRewritten = rewriteTagAnnotationsRecipe(ctx, flags, cmd, recipe, shaMap)
		oplogExtra["tagsRewritten"] = tagsRewritten
		return nil
	}

	// Verification closure: re-scan for each operation's pattern.
	exitCode := 0
	parentVerifyFunc := func(ctx context.Context) error {
		infof(flags, "Verifying secret removal...\n")
		for i, op := range recipe.Operations {
			pat := recipe.Patterns[i]
			verifyErr := verifySecretRemovedScoped(ctx, pat, opScope(&op))
			if verifyErr != nil {
				fmt.Fprintf(os.Stderr, "CRITICAL (operation %d, pattern %q): %v\n", i, op.Pattern, verifyErr)
				exitCode = 1
			}
		}
		if exitCode == 0 {
			infof(flags, "Verification passed: no matches found in object stores.\n")
		} else {
			fmt.Fprintln(os.Stderr, "Run 'git reflog expire --expire=now --all && git gc --prune=now' to force cleanup.")
		}
		return nil
	}

	result := RewriteResult{
		ShaMap:         shaMap,
		RewrittenCount: rewrittenCount,
		OldHeadSHA:     oldHeadSHA,
		SgDir:          sgDir,
		OpName:         "scrub-run",
		OplogExtra:     oplogExtra,
	}
	if err := result.Finalize(ctx, flags, cmd, parentAnnotFunc, parentVerifyFunc); err != nil {
		die(flags, cmd, 1, err.Error())
	}

	// Append scrub policies for each operation
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

	allTagRewrites := append(result.TagRewrites, annotationTagRewrites...)

	// JSON output
	if flags.json {
		rewrites := make(map[string]string)
		for old, new_ := range shaMap {
			if old != new_ {
				rewrites[old] = new_
			}
		}
		combinedTagRewrites := make([]TagRewrite, 0, len(allTagRewrites))
		combinedTagRewrites = append(combinedTagRewrites, allTagRewrites...)
		jsonResult := ScrubRunResult{
			Version:          1,
			DryRun:           false,
			Rewrites:         rewrites,
			Tags:             combinedTagRewrites,
			CommitsRewritten: rewrittenCount,
			BlobsReplaced:    len(blobMap),
			MessagesModified: messagesModified,
			TagsRewritten:    tagsRewritten,
			OperationCount:   len(recipe.Operations),
			OldHead:          oldHeadSHA,
			NewHead:          result.NewHeadSHA,
		}
		if jsonResult.Tags == nil {
			jsonResult.Tags = []TagRewrite{}
		}
		emitJSON(jsonResult)
		return exitCode
	}

	// Summary
	infof(flags, "\nScrub complete:\n")
	infof(flags, "  %d operations applied\n", len(recipe.Operations))
	infof(flags, "  %d commits rewritten\n", rewrittenCount)
	infof(flags, "  %d blobs replaced\n", len(blobMap))
	infof(flags, "  %d commit messages modified\n", messagesModified)
	infof(flags, "  %d tag annotations rewritten\n", tagsRewritten)
	infof(flags, "  Old HEAD: %s\n", oldHeadSHA[:12])
	infof(flags, "  New HEAD: %s\n", result.NewHeadSHA[:12])

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
	out, _, err := git.Run(ctx, "for-each-ref", "--format=%(refname) %(objecttype) %(objectname)", "refs/tags/")
	if err != nil {
		if flags.verbose {
			fmt.Fprintf(os.Stderr, "warning: failed to list tags for annotation rewriting: %v\n", err)
		}
		return nil, 0
	}

	var tagRewrites []TagRewrite
	tagsRewritten := 0
	lines := git.SplitNonEmpty(out)
	for _, line := range lines {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			continue
		}
		refname, objecttype, objectname := parts[0], parts[1], parts[2]

		if objecttype != "tag" {
			continue
		}

		// Read the tag object
		tagContent, _, err := git.Run(ctx, "cat-file", "-p", objectname)
		if err != nil {
			if flags.verbose {
				fmt.Fprintf(os.Stderr, "warning: failed to read tag object %s: %v\n", objectname, err)
			}
			continue
		}

		// Split into header and body
		headerEnd := strings.Index(tagContent, "\n\n")
		if headerEnd < 0 {
			continue
		}
		header := tagContent[:headerEnd]
		body := tagContent[headerEnd+2:]

		// Apply recipe operations to tag body in topo order
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

		if newBody == body {
			continue
		}

		// Reconstruct tag object and write it
		newContent := header + "\n\n" + newBody
		newTagSHA, _, err := git.RunWithEnvStdin(ctx, nil, []byte(newContent), "hash-object", "-t", "tag", "-w", "--stdin")
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("writing rewritten tag annotation for %s: %v", refname, err))
		}
		newTagSHA = strings.TrimSpace(newTagSHA)

		if err := git.UpdateRef(ctx, refname, newTagSHA, objectname); err != nil {
			die(flags, cmd, 1, fmt.Sprintf("updating tag ref %s after annotation rewrite: %v", refname, err))
		}

		tagRewrites = append(tagRewrites, TagRewrite{Refname: refname, OldSHA: objectname, NewSHA: newTagSHA, Annotated: true})
		tagsRewritten++
		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  tag annotation %s: %s -> %s\n", refname, shortSHA(objectname), shortSHA(newTagSHA))
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
