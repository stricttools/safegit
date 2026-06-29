package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/scan"
)

// executeScrubRecipe runs the shared execution pipeline for recipe-based scrub
// operations (used by `scrub run` and `scrub match`). It handles:
//   - Range-scoped scanning (fromSHA or entireHistory; scanEntireHistory overrides for scanning)
//   - Blob map construction via buildRecipeBlobMap
//   - walkAndRewrite with per-operation target filtering for commit messages
//   - RewriteResult construction and Finalize call
//
// The caller is responsible for JSON/text output using the returned RewriteResult
// (which includes BlobsReplaced, MessagesModified, TagsRewrittenCount, and
// AnnotationTagRewrites).
//
// Parameters:
//   - opName: operation name for the oplog ("scrub-run", "scrub-match")
//   - baseOplogExtra: caller-provided oplog fields (executeScrubRecipe merges in computed fields);
//     nil means use a default map with just "reason" and "operations"
//   - scanEntireHistory: when true, the scan uses EntireHistory regardless of fromSHA/entireHistory;
//     the walk range still uses fromSHA/entireHistory
//
// Returns the exit code and a pointer to the RewriteResult (nil if no rewrite
// was performed, e.g., no matches found).
func executeScrubRecipe(
	ctx context.Context,
	flags globalFlags,
	cmd string,
	recipe *ParsedRecipe,
	reason string,
	fromSHA string,
	entireHistory bool,
	scope *string,
	gitDir string,
	sgDir string,
	gitlinkMap map[string]string,
	opName string,
	baseOplogExtra map[string]interface{},
	scanEntireHistory bool,
) (int, *RewriteResult) {
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

	// Scan for matching blobs. When scanEntireHistory is set, use EntireHistory
	// for scanning to find ALL matching blobs regardless of the walk range.
	// Otherwise use the user's fromSHA/entireHistory for range-scoped scanning.
	infof(flags, "Scanning objects...\n")
	scanOpts := scan.ScanOpts{
		FromSHA:       fromSHA,
		EntireHistory: entireHistory,
	}
	if scanEntireHistory {
		scanOpts = scan.ScanOpts{EntireHistory: true}
	}
	results, err := scan.ScanObjects(ctx, combinedPattern, scanOpts)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("scanning objects: %v", err))
	}

	if len(results.Matches) == 0 && len(gitlinkMap) == 0 {
		infof(flags, "No matches found. Nothing to rewrite.\n")
		return 0, nil
	}

	// Exclude matches from the tracked policy file. The policy file
	// (.safegit/scrub-policies.jsonl) contains pattern strings by design
	// and must not be rewritten during scrub operations.
	policyBlobSHAs, policyBlobErr := buildScopedBlobSet(ctx, ".safegit/scrub-policies.jsonl")
	if policyBlobErr == nil && len(policyBlobSHAs) > 0 {
		var filtered []scan.Match
		for _, m := range results.Matches {
			if m.ObjectType == "blob" && policyBlobSHAs[m.SHA] {
				continue
			}
			filtered = append(filtered, m)
		}
		results.Matches = filtered

		if len(results.Matches) == 0 && len(gitlinkMap) == 0 {
			infof(flags, "No matches found (only policy file matches excluded). Nothing to rewrite.\n")
			return 0, nil
		}
	}

	// When --scope is set, build a scoped blob set to filter blobs by path.
	var scopedBlobSHAs map[string]bool
	if scope != nil {
		scopedBlobSHAs, err = buildScopedBlobSet(ctx, *scope)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("building scoped blob set: %v", err))
		}
	}

	// Collect unique blob SHAs from matches, filtering by scope when set.
	uniqueBlobSHAs := make(map[string]bool)
	var commitMatchCount, tagMatchCount int
	for _, m := range results.Matches {
		switch m.ObjectType {
		case "blob":
			if scope != nil && !scopedBlobSHAs[m.SHA] {
				continue
			}
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

	if len(blobMap) == 0 && commitMatchCount == 0 && tagMatchCount == 0 && len(gitlinkMap) == 0 {
		infof(flags, "No replacements needed. Nothing to rewrite.\n")
		return 0, nil
	}

	// Confirmation prompt
	if !confirmOrAbort(flags, "This will rewrite history using %d recipe operations. This cannot be undone. Proceed?", len(recipe.Operations)) {
		infof(flags, "Aborted.\n")
		return 0, nil
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
		newTreeSHA, err := replaceInTreeByBlobMap(ctx, info.Tree, blobMap, gitlinkMap, treeCache)
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

	// Oplog extra: use caller-provided base or build a default.
	oplogExtra := baseOplogExtra
	if oplogExtra == nil {
		oplogExtra = map[string]interface{}{
			"reason":     reason,
			"operations": len(recipe.Operations),
		}
	}
	oplogExtra["blobsReplaced"] = len(blobMap)
	oplogExtra["messagesModified"] = messagesModified

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

	// Build policy data for single-operation recipes.
	var policyData *ScrubPolicy
	if len(recipe.Operations) == 1 {
		op := recipe.Operations[0]
		policy := ScrubPolicy{
			Type:        "match",
			Pattern:     op.Pattern,
			Reason:      reason,
			CreatedByOp: opName,
		}
		if op.Scope != nil {
			policy.Scope = *op.Scope
		}
		policyData = &policy
	}

	// Resolve repo root for tracked policy storage.
	repoRoot, err := git.RepoRoot(ctx)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("resolving repo root: %v", err))
	}

	result := RewriteResult{
		ShaMap:         shaMap,
		RewrittenCount: rewrittenCount,
		OldHeadSHA:     oldHeadSHA,
		SgDir:          sgDir,
		RepoRoot:       repoRoot,
		OpName:         opName,
		OplogExtra:     oplogExtra,
		PolicyData:     policyData,
	}
	if err := result.Finalize(ctx, flags, cmd, parentAnnotFunc, parentVerifyFunc); err != nil {
		die(flags, cmd, 1, err.Error())
	}

	// Populate post-execution metrics for callers.
	result.BlobsReplaced = len(blobMap)
	result.MessagesModified = messagesModified
	result.TagsRewrittenCount = tagsRewritten
	result.AnnotationTagRewrites = annotationTagRewrites

	return exitCode, &result
}

// TagBodyTransformFunc transforms the body of an annotated tag. It receives the
// tag's refname, full header text, and body text. It returns the new body (or
// the same body if no change is needed) and any error.
type TagBodyTransformFunc func(refname, header, body string) (newBody string, err error)

// forEachAnnotatedTag enumerates all annotated tags, splits each into header
// and body, calls fn for body transformation, and writes the new tag object
// + updates the ref when the body changed. The shaMap is available for callers
// that need commit SHA remapping in the future but is currently unused.
//
// Returns the list of rewritten tags, count of tags rewritten, and any error.
// Errors from fn are propagated immediately (aborting further tags).
func forEachAnnotatedTag(ctx context.Context, shaMap map[string]string, fn TagBodyTransformFunc) ([]TagRewrite, int, error) {
	out, _, err := git.Run(ctx, "for-each-ref", "--format=%(refname) %(objecttype) %(objectname)", "refs/tags/")
	if err != nil {
		return nil, 0, fmt.Errorf("listing tags: %w", err)
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

		// Read the tag object.
		tagContent, _, err := git.Run(ctx, "cat-file", "-p", objectname)
		if err != nil {
			return tagRewrites, tagsRewritten, fmt.Errorf("reading tag object %s: %w", objectname, err)
		}

		// Split into header and body.
		headerEnd := strings.Index(tagContent, "\n\n")
		if headerEnd < 0 {
			// No body -- nothing to transform.
			continue
		}
		header := tagContent[:headerEnd]
		body := tagContent[headerEnd+2:]

		newBody, err := fn(refname, header, body)
		if err != nil {
			return tagRewrites, tagsRewritten, fmt.Errorf("transforming tag %s: %w", refname, err)
		}

		if newBody == body {
			continue
		}

		// Reconstruct tag object and write it.
		newContent := header + "\n\n" + newBody
		newTagSHA, _, err := git.RunWithEnvStdin(ctx, nil, []byte(newContent), "hash-object", "-t", "tag", "-w", "--stdin")
		if err != nil {
			return tagRewrites, tagsRewritten, fmt.Errorf("writing rewritten tag annotation for %s: %w", refname, err)
		}
		newTagSHA = strings.TrimSpace(newTagSHA)

		if err := git.UpdateRef(ctx, refname, newTagSHA, objectname); err != nil {
			return tagRewrites, tagsRewritten, fmt.Errorf("updating tag ref %s: %w", refname, err)
		}

		tagRewrites = append(tagRewrites, TagRewrite{Refname: refname, OldSHA: objectname, NewSHA: newTagSHA, Annotated: true})
		tagsRewritten++
	}

	return tagRewrites, tagsRewritten, nil
}
