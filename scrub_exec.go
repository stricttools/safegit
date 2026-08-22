package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
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

// scrubFileMode reads the `mode` selector `scrub file` declares and returns the
// two values the rest of the command takes: the payload's mode spelling
// ("replace" or "remove") and, for a replacement, the operator-supplied source
// path.
//
// The two vocabularies are deliberately different. The flags name what the
// OPERATOR does (--delete, --replace-with FILE); the mode names what the
// rewrite does to each tree, which is the question a machine reading the
// payload is asking, and which has been "replace"/"remove" since the payload
// existed.
func scrubFileMode(kwargs map[string]interface{}) (mode string, replacementPath string) {
	elected := strictcli.GetElected(kwargs, "mode")
	if elected.Is(scrubDeleteChoice) {
		return "remove", ""
	}
	return "replace", strictcli.Get[string](elected.Fields, "value")
}

// readReplacementSource reads the replacement file at the OPERATOR's current
// directory. Anchoring is the whole point: the target argument is
// repository-relative and resolves against the pinned repository root, while
// this path is one the operator typed at a shell prompt and means relative to
// where they are standing.
func readReplacementSource(path string) []byte {
	content, err := os.ReadFile(path)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("reading the replacement file %q (paths given to --replace-with resolve against your current directory): %v", path, err))
	}
	return content
}

// scrubFileModeSummary spells the elected mode for the human summary, naming
// the replacement source so the operator can see which file was read.
func scrubFileModeSummary(mode, replacementPath string) string {
	if mode == "remove" {
		return "remove (delete the file from every commit in range)"
	}
	return fmt.Sprintf("replace (with the contents of %s)", replacementPath)
}

// scrubRangeSummary spells the elected range for the human summary.
func scrubRangeSummary(fromSHA string, entireHistory bool) string {
	if entireHistory {
		return "entire history"
	}
	if len(fromSHA) >= 12 {
		return fromSHA[:12] + "..HEAD (inclusive)"
	}
	return fromSHA + "..HEAD (inclusive)"
}

// printRotationNotice states the one thing a successful scrub does NOT do, and
// the one thing the operator still has to do about it.
//
// Rewriting history removes the secret from THIS repository's objects. It does
// nothing to a copy anyone already fetched, to a fork, to a CI cache, or to
// whatever read the value while it was published -- so a secret that was ever
// pushed is still leaked after a perfectly successful scrub, and the only fix
// for that is rotating the credential.
//
// recheck is the exact command that re-asks whether the content has come back.
// Verification keeps no state between runs, so the command has to carry what it
// checks, and printing it here is what makes the check reachable later without
// the operator reconstructing the invocation from memory.
func printRotationNotice(flags globalFlags, recheck string) {
	infof(flags, "\nRotate the credential. Rewriting history does not un-leak a secret that was\n")
	infof(flags, "ever pushed: every clone, fork, fetch and cache that already has the old\n")
	infof(flags, "history still holds it, and this command cannot reach any of them.\n")
	infof(flags, "To re-check this repository later:\n")
	infof(flags, "  %s\n", recheck)
}

// recheckCommandForPatterns builds the `scrub verify` invocation that re-checks
// the given patterns.
func recheckCommandForPatterns(patterns ...string) string {
	var b strings.Builder
	b.WriteString("safegit scrub verify")
	for _, p := range patterns {
		b.WriteString(" --pattern " + shellSingleQuote(p))
	}
	return b.String()
}

// recheckCommandForRecipe builds the `scrub verify` invocation that re-checks
// every operation of a recipe file.
func recheckCommandForRecipe(recipePath string) string {
	return "safegit scrub verify " + shellSingleQuote(recipePath)
}

// recheckCommandForRemovedContent is the re-check command a `scrub file`
// prints. A file scrub names a path, not a pattern, so there is no regex to
// hand back: the command is spelled with the placeholder the operator fills in.
func recheckCommandForRemovedContent() string {
	return "safegit scrub verify --pattern '<a regex matching the content you removed>'"
}

// shellSingleQuote wraps s so a shell passes it through unchanged. The strings
// it quotes are regexes and file paths an operator typed, both of which
// routinely contain characters a bare word would not survive.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// scrubFileCommitCount counts the commits the elected range covers, dying on a
// failure: the count is a figure `scrub file` prints and puts in its payload,
// and printing a wrong one is worse than refusing.
func scrubFileCommitCount(ctx context.Context, fromSHA string, entireHistory bool) int {
	n, err := commitCountInRange(ctx, fromSHA, entireHistory)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("counting the commits in range: %v", err))
	}
	return n
}

// scrubFileCommitRange lists the commits to walk, oldest first, for the elected
// range.
func scrubFileCommitRange(ctx context.Context, fromSHA string, entireHistory bool) []string {
	if entireHistory {
		out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", "HEAD")
		if err != nil {
			die(exitcode.General, fmt.Sprintf("listing commits: %v", err))
		}
		return git.SplitNonEmpty(out)
	}
	out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", fromSHA+"..HEAD")
	if err != nil {
		die(exitcode.General, fmt.Sprintf("listing commits: %v", err))
	}
	return append([]string{fromSHA}, git.SplitNonEmpty(out)...)
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
//   - companions: rewrites in OTHER repositories whose objects already exist and
//     whose refs have not moved -- the submodule rewrites a `scrub match` walked
//     before delegating the parent here. They are verified alongside this
//     rewrite and published before it, so a parent-side verification failure
//     leaves every submodule's refs untouched, and a crash between the two
//     leaves a parent whose gitlinks name commits that exist.
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
	companions []*pendingRewrite,
) (int, *RewriteResult) {
	// When this repository turns out to have nothing to rewrite, the companions
	// are still real rewrites with real objects behind them: they are verified
	// and published on their own. There is no primary plan to hold them back
	// for, so the both-before-either ordering is vacuous in this branch.
	publishCompanionsAlone := func() int {
		if len(companions) == 0 {
			return 0
		}
		if label, err := prepareAll(flags, companions); err != nil {
			dieFinalize(label, err)
		}
		if label, err := publishAll(flags, cmd, companions); err != nil {
			dieFinalize(label, err)
		}
		return exitcode.OK
	}

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
		return publishCompanionsAlone(), nil
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
		return publishCompanionsAlone(), nil
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
			if err := verifyPatternAbsentFromTips(ctx, pat, opScope(&op), plan.WalkedTips); err != nil {
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

	result := RewriteResult{
		ShaMap:         shaMap,
		RewrittenCount: rewrittenCount,
		Intent:         intent,
		OldHeadSHA:     oldHeadSHA,
		SgDir:          sgDir,
		Reason:         reason,
		OpName:         opName,
		OplogExtra:     oplogExtra,
	}

	// Objects before refs, across every repository the operation touches: the
	// companions and this rewrite are all verified first, and only then
	// published -- companions first (see the parameter's documentation).
	all := make([]*pendingRewrite, 0, len(companions)+1)
	all = append(all, companions...)
	all = append(all, &pendingRewrite{
		Ctx:    ctx,
		Result: &result,
		Hooks: RewriteHooks{
			AnnotateTag: recipeTagBodyTransform(recipe),
			TierA:       tierA,
			TierB:       tierB,
		},
	})
	if label, err := prepareAll(flags, all); err != nil {
		dieFinalize(label, err)
	}
	if label, err := publishAll(flags, cmd, all); err != nil {
		dieFinalize(label, err)
	}

	// Populate post-execution metrics for callers. (TagsRewrittenCount and
	// AnnotationTagRewrites are populated by Finalize.)
	result.BlobsReplaced = len(blobMap)
	result.MessagesModified = messagesModified

	return result.TierBExit(exitcode.OK), &result
}

// commitCountInRange counts the commits an elected range covers: every commit
// reachable from HEAD for the whole history, or fromSHA..HEAD plus one for a
// range, because --from is inclusive. It is the single answer to "how many
// commits does this rewrite cover", which the preview and the execution both
// print.
func commitCountInRange(ctx context.Context, fromSHA string, entireHistory bool) (int, error) {
	spec := "HEAD"
	inclusive := 0
	if !entireHistory {
		if fromSHA == "" {
			return 0, fmt.Errorf("no --from commit to count from")
		}
		spec = fromSHA + "..HEAD"
		inclusive = 1
	}
	out, _, err := git.Run(ctx, "rev-list", "--count", spec)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parsing the commit count %q: %w", strings.TrimSpace(out), err)
	}
	return n + inclusive, nil
}

// estimateCommitCount is the preview's reading of the same count: a figure it
// reports about a rewrite it is not performing, so a failure yields 0 rather
// than aborting a read-only command.
func estimateCommitCount(ctx context.Context, fromSHA string, entireHistory bool) int {
	n, err := commitCountInRange(ctx, fromSHA, entireHistory)
	if err != nil {
		return 0
	}
	return n
}

// TagBodyTransformFunc transforms the body of an annotated tag. It receives the
// tag's refname, full header text, and body text. It returns the new body (or
// the same body if no change is needed) and any error.
//
// A rewrite hands one of these to Finalize, which applies it while it plans the
// ref updates: the new tag objects are written before Tier A verification runs,
// and the refs that point at them move with every other ref afterwards.
type TagBodyTransformFunc func(refname, header, body string) (newBody string, err error)
