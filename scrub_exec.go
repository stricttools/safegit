package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/scan"
	"github.com/smm-h/strictcli/go/strictcli"
)

// scrubRange reads the `range` selector both `scrub match` and `scrub run`
// declare and returns the two values the pipeline below takes: the first
// commit to rewrite from, or nil when the whole history was elected.
//
// The selector is required and elects exactly one member, so "neither elected"
// is unrepresentable rather than a state the handler has to refuse.
func scrubRange(kwargs map[string]interface{}) (*string, bool) {
	elected := strictcli.GetElected(kwargs, "range")
	if elected.Is(scrubEntireHistoryChoice) {
		return nil, true
	}
	s := strictcli.Get[string](elected.Fields, "value")
	return &s, false
}

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
	remapGlobs []string,
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
		die(exitcode.Usage, fmt.Sprintf("compiling combined pattern: %v", err))
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
		die(exitcode.General, fmt.Sprintf("scanning objects: %v", err))
	}

	// A search that matches nothing is a successful answer, not a failure --
	// but it is stated in the terms the operator asked in, so "it worked" and
	// "it found nothing" can never be confused for each other.
	if len(results.Matches) == 0 && len(gitlinkMap) == 0 {
		infof(flags, "0 commits contained the pattern. Nothing was rewritten and no history changed.\n")
		return 0, nil
	}

	// When --scope is set, build a scoped blob set to filter blobs by path.
	var scopedBlobSHAs map[string]bool
	if scope != nil {
		scopedBlobSHAs, err = buildScopedBlobSet(ctx, *scope)
		if err != nil {
			die(exitcode.General, fmt.Sprintf("building scoped blob set: %v", err))
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

	// Build per-operation scoped blob sets for operations that have a scope
	// field in the recipe TOML. This filters which operations apply to which
	// blobs based on the file paths where each blob appears.
	var blobAllowedOps map[string]map[int]bool
	hasPerOpScope := false
	for _, op := range recipe.Operations {
		if op.Scope != nil {
			hasPerOpScope = true
			break
		}
	}
	if hasPerOpScope {
		blobAllowedOps = make(map[string]map[int]bool)
		// Pre-populate: every blob gets an empty allowed set.
		for _, sha := range blobSHAList {
			blobAllowedOps[sha] = make(map[int]bool)
		}
		for i, op := range recipe.Operations {
			if op.Scope == nil {
				// Unscoped operation applies to all blobs.
				for _, sha := range blobSHAList {
					blobAllowedOps[sha][i] = true
				}
			} else {
				// Build the set of blob SHAs at paths matching this op's scope.
				opScopedBlobs, scopeErr := buildScopedBlobSet(ctx, *op.Scope)
				if scopeErr != nil {
					die(exitcode.General, fmt.Sprintf("building scoped blob set for operation %d (scope %q): %v", i, *op.Scope, scopeErr))
				}
				for _, sha := range blobSHAList {
					if opScopedBlobs[sha] {
						blobAllowedOps[sha][i] = true
					}
				}
			}
		}
	}

	// Build the combined blob map via the recipe.
	infof(flags, "Building blob replacement map (%d candidate blobs)...\n", len(blobSHAList))
	blobMap, err := buildRecipeBlobMap(ctx, recipe, blobSHAList, blobAllowedOps)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("building blob map: %v", err))
	}

	infof(flags, "Found %d blobs to replace, %d commit message matches, %d tag matches\n",
		len(blobMap), commitMatchCount, tagMatchCount)

	if len(blobMap) == 0 && commitMatchCount == 0 && tagMatchCount == 0 && len(gitlinkMap) == 0 {
		infof(flags, "0 commits contained the pattern within scope. Nothing was rewritten and no history changed.\n")
		return 0, nil
	}

	// `scrub run` declares itself consequential, so the framework's confirm
	// protocol already took deliberate consent for this rewrite before dispatch.
	// The scale is stated, not asked a second time.
	infof(flags, "Rewriting history using %d recipe operations. This cannot be undone.\n", len(recipe.Operations))

	// Capture old HEAD
	oldHeadSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		die(exitcode.General, fmt.Sprintf("resolving HEAD: %v", err))
	}

	// Determine commit range
	var shas []string
	if entireHistory {
		out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", "HEAD")
		if err != nil {
			die(exitcode.General, fmt.Sprintf("listing commits: %v", err))
		}
		shas = git.SplitNonEmpty(out)
	} else {
		out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", fromSHA+"..HEAD")
		if err != nil {
			die(exitcode.General, fmt.Sprintf("listing commits: %v", err))
		}
		shas = append([]string{fromSHA}, git.SplitNonEmpty(out)...)
	}

	commitCount := len(shas)
	infof(flags, "Rewriting %d commits...\n", commitCount)

	messagesModified := 0
	treeCache := make(map[string]treeRewrite)

	var remap *remapState
	if len(remapGlobs) > 0 {
		remap = newRemapState(remapGlobs, shas)
	}

	// What this walk decides to change, per commit, declared as it decides it.
	// Tier A checks the rewritten commits against it before any ref moves.
	intent := PerPathIntent()

	shaMap, rewrittenCount, err := walkAndRewrite(ctx, shas, func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string, shaMap map[string]string) (CommitTransform, error) {
		var xform CommitTransform

		// Replace blobs in tree
		newTreeSHA, changedPaths, err := replaceInTreeByBlobMap(ctx, info.Tree, blobMap, gitlinkMap, treeCache)
		if err != nil {
			return CommitTransform{}, fmt.Errorf("replacing blobs in tree for commit %s: %w", sha, err)
		}
		intent.Declare(sha, changedPaths, false)
		// Remap full commit hashes in glob-matched files against the growing
		// SHA map (time-varying transform: runs after the static blob map and
		// never shares the static tree cache).
		if remap != nil {
			remappedTreeSHA, remappedPaths, err := remap.remapTree(ctx, newTreeSHA, "", shaMap)
			if err != nil {
				return CommitTransform{}, fmt.Errorf("commit %s: %w", sha, err)
			}
			newTreeSHA = remappedTreeSHA
			intent.Declare(sha, remappedPaths, false)
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
			intent.Declare(sha, nil, true)
		}

		return xform, nil
	}, flags.verbose)
	if err != nil {
		die(exitcode.General, err.Error())
	}
	remap.reportStale(flags)

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

	// Tier A: every operation's pattern must be absent from the history that is
	// about to be published -- read from the tips the ref update plan carries,
	// which is the only way to ask the question before the refs move.
	tierA := func(ctx context.Context, plan *RefUpdatePlan) error {
		infof(flags, "Checking the rewritten history for surviving matches...\n")
		for i, op := range recipe.Operations {
			pat := recipe.Patterns[i]
			if err := verifyPatternAbsentFromTips(ctx, pat, opScope(&op), plan.NewTips); err != nil {
				return fmt.Errorf("operation %d (pattern %q): %v", i, op.Pattern, err)
			}
		}
		return nil
	}

	// Tier B: the whole object store, after cleanup. This one can see what the
	// rewrite does not cover -- a stash, a note, an unpruned pack -- and cannot
	// run before cleanup by design, so its findings never abort: they exit
	// nonzero with the rewrite standing.
	tierB := func(ctx context.Context) error {
		infof(flags, "Verifying secret removal...\n")
		var findings []string
		for i, op := range recipe.Operations {
			pat := recipe.Patterns[i]
			if verifyErr := verifySecretRemovedScoped(ctx, pat, opScope(&op)); verifyErr != nil {
				findings = append(findings, fmt.Sprintf("operation %d (pattern %q): %v", i, op.Pattern, verifyErr))
			}
		}
		if len(findings) == 0 {
			infof(flags, "Verification passed: no matches found in object stores.\n")
			return nil
		}
		defer fmt.Fprintln(os.Stderr, "Run 'git reflog expire --expire=now --all && git gc --prune=now' to force cleanup.")
		return fmt.Errorf("%s", strings.Join(findings, "\n  "))
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

	result := RewriteResult{
		ShaMap:         shaMap,
		RewrittenCount: rewrittenCount,
		Intent:         intent,
		OldHeadSHA:     oldHeadSHA,
		SgDir:          sgDir,
		Reason:         reason,
		OpName:         opName,
		OplogExtra:     oplogExtra,
		PolicyData:     policyData,
	}
	if err := result.Finalize(ctx, flags, cmd, RewriteHooks{
		AnnotateTag: recipeTagBodyTransform(recipe),
		TierA:       tierA,
		TierB:       tierB,
	}); err != nil {
		dieFinalize("", err)
	}

	// Populate post-execution metrics for callers. (TagsRewrittenCount and
	// AnnotationTagRewrites are populated by Finalize.)
	result.BlobsReplaced = len(blobMap)
	result.MessagesModified = messagesModified

	return result.TierBExit(exitcode.OK), &result
}

// estimateCommitCount returns the number of commits in the rewrite range.
// For entireHistory, it counts all commits reachable from HEAD. For range mode,
// it counts commits in fromSHA..HEAD plus one (inclusive of fromSHA).
// Returns 0 if the count cannot be determined.
func estimateCommitCount(ctx context.Context, fromSHA string, entireHistory bool) int {
	if entireHistory {
		out, _, err := git.Run(ctx, "rev-list", "--count", "HEAD")
		if err == nil {
			var n int
			fmt.Sscanf(strings.TrimSpace(out), "%d", &n)
			return n
		}
	} else if fromSHA != "" {
		out, _, err := git.Run(ctx, "rev-list", "--count", fromSHA+"..HEAD")
		if err == nil {
			var n int
			fmt.Sscanf(strings.TrimSpace(out), "%d", &n)
			return n + 1 // inclusive of fromSHA
		}
	}
	return 0
}

// TagBodyTransformFunc transforms the body of an annotated tag. It receives the
// tag's refname, full header text, and body text. It returns the new body (or
// the same body if no change is needed) and any error.
//
// A rewrite hands one of these to Finalize, which applies it while it plans the
// ref updates: the new tag objects are written before Tier A verification runs,
// and the refs that point at them move with every other ref afterwards.
type TagBodyTransformFunc func(refname, header, body string) (newBody string, err error)
