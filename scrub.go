package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/submodule"
)

// ScrubFileResult is the JSON output for `scrub file` in execute mode.
type ScrubFileResult struct {
	Version          int               `json:"version"`
	DryRun           bool              `json:"dry_run"`
	File             string            `json:"file"`
	Mode             string            `json:"mode"`
	Rewrites         map[string]string `json:"rewrites"`
	Tags             []TagRewrite      `json:"tags"`
	CommitsRewritten int               `json:"commits_rewritten"`
	OldHead          string            `json:"old_head"`
	NewHead          string            `json:"new_head"`
}

// ScrubFileDryRunResult is the JSON output for `scrub file --dry-run`.
type ScrubFileDryRunResult struct {
	Version     int    `json:"version"`
	DryRun      bool   `json:"dry_run"`
	File        string `json:"file"`
	Mode        string `json:"mode"`
	From        string `json:"from"`
	CommitCount int    `json:"commit_count"`
	OldHead     string `json:"old_head"`
	NewBlobSHA  string `json:"new_blob_sha,omitempty"`
}

func runScrubFile(flags globalFlags, kwargs map[string]interface{}) int {
	const cmd = "scrub file"

	from := kwargs["from"].(string)
	reason := kwargs["reason"].(string)
	filePath := kwargs["file"].(string)

	// Validation
	gitDir := mustGitDir(flags, cmd)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		die(flags, cmd, 4, err.Error())
	}

	ctx := context.Background()
	requireCleanTree(ctx, flags, cmd)

	sgDir := repo.SafegitDir(gitDir)

	// Acquire rewrite lock to prevent concurrent scrub operations
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("loading config: %v", err))
	}
	timeout := time.Duration(cfg.Lock.AcquireTimeoutSeconds) * time.Second
	sharedDir := repo.SharedSafegitDir(ctx, gitDir)
	lk, err := lock.Acquire(sharedDir, sgDir, "safegit/rewrite", "scrub-file", timeout)
	if err != nil {
		die(flags, cmd, 1, "another rewrite operation is in progress")
	}
	defer lk.Release()

	// Enumerate submodules to detect if file path targets a submodule.
	subs, subErr := submodule.Enumerate(ctx, gitDir)
	if subErr != nil {
		// Non-fatal: continue without submodule support.
		subs = nil
	}

	// Check if the file path starts with a submodule's relative path.
	var targetSub *submodule.SubmoduleInfo
	var subFilePath string
	for i, sub := range subs {
		if !sub.Initialized {
			continue
		}
		prefix := sub.RelativePath + "/"
		if strings.HasPrefix(filePath, prefix) {
			targetSub = &subs[i]
			subFilePath = filePath[len(prefix):]
			break
		}
	}

	if targetSub != nil {
		return runScrubFileInSubmodule(ctx, flags, cmd, filePath, subFilePath, targetSub, from, reason, gitDir, sgDir)
	}

	// Resolve --from to a full SHA
	fromSHA, err := git.RevParse(ctx, from)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("resolving --from %q: %v", from, err))
	}

	// Ancestry guard: --from must be an ancestor of (or equal to) HEAD
	isAnc, err := git.IsAncestorOf(ctx, fromSHA, "HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("checking ancestry of --from: %v", err))
	}
	if !isAnc {
		die(flags, cmd, 1, fmt.Sprintf("--from commit %s is not an ancestor of HEAD", from))
	}

	// Capture old HEAD before any changes
	oldHeadSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("resolving HEAD: %v", err))
	}

	// Determine replacement blob: if file exists on disk, hash it as the
	// replacement. Otherwise, empty string signals removal mode.
	var newBlobSHA string
	var mode string
	if _, err := os.Stat(filePath); err == nil {
		newBlobSHA, err = git.HashObjectWrite(ctx, filePath)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("hashing file %q: %v", filePath, err))
		}
		mode = "replace"
	} else {
		mode = "remove"
	}

	// Count commits to be rewritten (inclusive of --from)
	countOut, _, err := git.Run(ctx, "rev-list", "--count", fromSHA+"..HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("counting commits: %v", err))
	}
	exclusiveCount, err := strconv.Atoi(strings.TrimSpace(countOut))
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("parsing commit count: %v", err))
	}
	commitCount := exclusiveCount + 1 // inclusive of fromSHA

	// Summary
	infof(flags, "Scrub summary:\n")
	infof(flags, "  File:    %s\n", filePath)
	infof(flags, "  Mode:    %s\n", mode)
	infof(flags, "  From:    %s\n", fromSHA[:12])
	infof(flags, "  Commits: %d\n", commitCount)
	infof(flags, "  Reason:  %s\n", reason)

	// Confirmation prompt (skipped with --yes)
	if !confirmOrAbort(flags, "This will rewrite %d commits. This cannot be undone. Proceed?", commitCount) {
		infof(flags, "Aborted.\n")
		return 0
	}

	// Dry-run check
	if flags.dryRun {
		if flags.json {
			result := ScrubFileDryRunResult{
				Version:     1,
				DryRun:      true,
				File:        filePath,
				Mode:        mode,
				From:        fromSHA,
				CommitCount: commitCount,
				OldHead:     oldHeadSHA,
				NewBlobSHA:  newBlobSHA,
			}
			emitJSON(result)
		}
		infof(flags, "Dry run: no changes made.\n")
		return 0
	}

	// Commit walker: topo-order, parents before children (inclusive of fromSHA)
	out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", fromSHA+"..HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("listing commits: %v", err))
	}
	shas := append([]string{fromSHA}, git.SplitNonEmpty(out)...)

	// Track old blob SHAs that get replaced, for post-cleanup verification.
	oldBlobSHAs := make(map[string]bool)

	treeCache := make(map[string]string)
	shaMap, rewrittenCount, err := walkAndRewrite(ctx, shas, func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string) (CommitTransform, error) {
		// Look up the old blob SHA at the target path before replacing.
		oldBlobSHA := lookupBlobAtPath(ctx, info.Tree, filePath)

		newTreeSHA, err := replaceInTree(ctx, info.Tree, filePath, newBlobSHA, treeCache)
		if err != nil {
			return CommitTransform{}, fmt.Errorf("replacing in tree for commit %s: %w", sha, err)
		}
		var xform CommitTransform
		if newTreeSHA != info.Tree {
			xform.TreeSHA = newTreeSHA
			// Tree changed, so the old blob was replaced. Track it.
			if oldBlobSHA != "" && oldBlobSHA != newBlobSHA {
				oldBlobSHAs[oldBlobSHA] = true
			}
		}
		return xform, nil
	}, flags.verbose)
	if err != nil {
		die(flags, cmd, 1, err.Error())
	}

	// Populate RewriteResult for the shared post-rewrite pipeline.
	exitCode := 0
	result := RewriteResult{
		ShaMap:         shaMap,
		RewrittenCount: rewrittenCount,
		OldHeadSHA:     oldHeadSHA,
		SgDir:          sgDir,
		OpName:         "scrub-file",
		OplogExtra: map[string]interface{}{
			"file":   filePath,
			"from":   fromSHA,
			"reason": reason,
			"mode":   mode,
		},
	}

	// VerifyFunc: runs verifyScrub (structural integrity) and
	// verifyOldBlobsRemoved (old blobs pruned). Neither is fatal -- failures
	// set exitCode but allow the pipeline to continue.
	verifyFunc := func(ctx context.Context) error {
		infof(flags, "Verifying...\n")
		verifyFailures, verifyChecks := verifyScrub(ctx, shaMap, filePath, flags.verbose)
		if len(verifyFailures) > 0 {
			fmt.Fprintf(os.Stderr, "Verification warnings (%d failures):\n", len(verifyFailures))
			for _, f := range verifyFailures {
				fmt.Fprintf(os.Stderr, "  WARN: %s\n", f)
			}
		} else {
			infof(flags, "Verification passed: %d checks across %d rewritten commits\n", verifyChecks, rewrittenCount)
		}

		if len(oldBlobSHAs) > 0 {
			infof(flags, "Verifying old blobs removed...\n")
			oldBlobList := make([]string, 0, len(oldBlobSHAs))
			for sha := range oldBlobSHAs {
				oldBlobList = append(oldBlobList, sha)
			}
			if err := verifyOldBlobsRemoved(ctx, oldBlobList); err != nil {
				fmt.Fprintf(os.Stderr, "CRITICAL: %v\n", err)
				fmt.Fprintln(os.Stderr, "Old file content may still be present in the local object store.")
				fmt.Fprintln(os.Stderr, "Run 'git reflog expire --expire=now --all && git gc --prune=now' to force cleanup.")
				exitCode = 1
			} else {
				infof(flags, "Verification passed: all old blobs removed from object store.\n")
			}
		}
		return nil // non-fatal: exitCode tracks failures
	}

	if err := result.Finalize(ctx, flags, cmd, nil, verifyFunc); err != nil {
		die(flags, cmd, 1, err.Error())
	}

	// JSON output
	if flags.json {
		rewrites := make(map[string]string)
		for old, new_ := range shaMap {
			if old != new_ {
				rewrites[old] = new_
			}
		}
		tags := result.TagRewrites
		if tags == nil {
			tags = []TagRewrite{}
		}
		jsonResult := ScrubFileResult{
			Version:          1,
			DryRun:           false,
			File:             filePath,
			Mode:             mode,
			Rewrites:         rewrites,
			Tags:             tags,
			CommitsRewritten: rewrittenCount,
			OldHead:          oldHeadSHA,
			NewHead:          result.NewHeadSHA,
		}
		emitJSON(jsonResult)
		return exitCode
	}

	// Summary
	infof(flags, "\nScrub complete:\n")
	infof(flags, "  %d commits rewritten\n", rewrittenCount)
	infof(flags, "  Old HEAD: %s\n", oldHeadSHA[:12])
	infof(flags, "  New HEAD: %s\n", result.NewHeadSHA[:12])

	return exitCode
}

// runScrubFileInSubmodule handles `scrub file` when the target path lives inside
// a submodule. It scrubs the file within the submodule's git history, collects
// the commit SHA mapping, then rewrites the parent's gitlinks to point to the
// new submodule commits.
func runScrubFileInSubmodule(
	ctx context.Context,
	flags globalFlags,
	cmd string,
	fullPath string, // original path as user provided (e.g., "vendor/sub/secret.env")
	subFilePath string, // path within the submodule (e.g., "secret.env")
	sub *submodule.SubmoduleInfo,
	from string,
	reason string,
	gitDir string,
	sgDir string,
) int {
	infof(flags, "File %q is inside submodule [%s], scrubbing as %q within submodule.\n",
		fullPath, sub.RelativePath, subFilePath)

	// Ensure safegit is initialized for the submodule.
	if err := repo.EnsureInitialized(sub.GitDir); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("initializing safegit for submodule %s: %v", sub.RelativePath, err))
	}

	// Save parent cwd.
	parentDir, err := os.Getwd()
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("getting cwd: %v", err))
	}

	// Chdir into the submodule.
	if err := os.Chdir(sub.WorkTreePath); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("chdir to submodule %s: %v", sub.RelativePath, err))
	}

	// Resolve --from within the submodule. Since the user's --from likely
	// refers to the parent repo, we use the submodule's entire history.
	// The parent's --from will be used when rewriting parent gitlinks.
	subFromSHA, subFromErr := git.RevParse(ctx, from)
	useEntireSubHistory := subFromErr != nil

	if !useEntireSubHistory {
		// Verify it's an ancestor of submodule HEAD.
		isAnc, err := git.IsAncestorOf(ctx, subFromSHA, "HEAD")
		if err != nil || !isAnc {
			useEntireSubHistory = true
		}
	}

	// Determine replacement blob within the submodule context.
	var newBlobSHA string
	var mode string
	if _, err := os.Stat(subFilePath); err == nil {
		newBlobSHA, err = git.HashObjectWrite(ctx, subFilePath)
		if err != nil {
			os.Chdir(parentDir)
			die(flags, cmd, 1, fmt.Sprintf("hashing file %q in submodule: %v", subFilePath, err))
		}
		mode = "replace"
	} else {
		mode = "remove"
	}

	// Get submodule commit range.
	var subSHAs []string
	if useEntireSubHistory {
		out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", "HEAD")
		if err != nil {
			os.Chdir(parentDir)
			die(flags, cmd, 1, fmt.Sprintf("submodule: listing commits: %v", err))
		}
		subSHAs = git.SplitNonEmpty(out)
	} else {
		out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", subFromSHA+"..HEAD")
		if err != nil {
			os.Chdir(parentDir)
			die(flags, cmd, 1, fmt.Sprintf("submodule: listing commits: %v", err))
		}
		subSHAs = append([]string{subFromSHA}, git.SplitNonEmpty(out)...)
	}

	subCommitCount := len(subSHAs)

	// Summary
	infof(flags, "Scrub summary:\n")
	infof(flags, "  File:       %s (in submodule %s)\n", subFilePath, sub.RelativePath)
	infof(flags, "  Mode:       %s\n", mode)
	infof(flags, "  Sub commits: %d\n", subCommitCount)
	infof(flags, "  Reason:     %s\n", reason)

	// Confirmation prompt (skipped with --yes)
	if !confirmOrAbort(flags, "This will rewrite submodule + parent history. This cannot be undone. Proceed?") {
		infof(flags, "Aborted.\n")
		os.Chdir(parentDir)
		return 0
	}

	if flags.dryRun {
		if flags.json {
			result := ScrubFileDryRunResult{
				Version:     1,
				DryRun:      true,
				File:        fullPath,
				Mode:        mode,
				From:        from,
				CommitCount: subCommitCount,
				OldHead:     "", // not yet resolved in submodule dry-run
				NewBlobSHA:  newBlobSHA,
			}
			emitJSON(result)
		}
		infof(flags, "Dry run: no changes made.\n")
		os.Chdir(parentDir)
		return 0
	}

	// Capture old submodule HEAD before rewriting.
	oldSubHeadSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		os.Chdir(parentDir)
		die(flags, cmd, 1, fmt.Sprintf("resolving submodule HEAD: %v", err))
	}

	// Walk and rewrite submodule commits.
	oldSubBlobSHAs := make(map[string]bool)
	subTreeCache := make(map[string]string)
	subShaMap, subRewrittenCount, err := walkAndRewrite(ctx, subSHAs, func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string) (CommitTransform, error) {
		oldBlobSHA := lookupBlobAtPath(ctx, info.Tree, subFilePath)
		newTreeSHA, err := replaceInTree(ctx, info.Tree, subFilePath, newBlobSHA, subTreeCache)
		if err != nil {
			return CommitTransform{}, fmt.Errorf("replacing in tree for commit %s: %w", sha, err)
		}
		var xform CommitTransform
		if newTreeSHA != info.Tree {
			xform.TreeSHA = newTreeSHA
			if oldBlobSHA != "" && oldBlobSHA != newBlobSHA {
				oldSubBlobSHAs[oldBlobSHA] = true
			}
		}
		return xform, nil
	}, flags.verbose)
	if err != nil {
		os.Chdir(parentDir)
		die(flags, cmd, 1, fmt.Sprintf("submodule walk and rewrite: %v", err))
	}

	// Finalize submodule rewrite via shared pipeline.
	infof(flags, "Finalizing submodule [%s] rewrite...\n", sub.RelativePath)
	subResult := RewriteResult{
		ShaMap:         subShaMap,
		RewrittenCount: subRewrittenCount,
		OldHeadSHA:     oldSubHeadSHA,
		SgDir:          sub.SafegitDir,
		OpName:         "scrub-file",
		OplogExtra: map[string]interface{}{
			"file":   subFilePath,
			"reason": reason,
			"mode":   mode,
		},
	}
	if err := subResult.Finalize(ctx, flags, cmd, nil, nil); err != nil {
		os.Chdir(parentDir)
		die(flags, cmd, 1, fmt.Sprintf("submodule finalize: %v", err))
	}
	subTagRewrites := subResult.TagRewrites

	infof(flags, "  [%s] %d commits rewritten\n", sub.RelativePath, subRewrittenCount)

	// Chdir back to parent.
	if err := os.Chdir(parentDir); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("chdir back to parent: %v", err))
	}

	// Build gitlink map from submodule SHA mappings.
	gitlinkMap := make(map[string]string)
	for old, new_ := range subShaMap {
		if old != new_ {
			gitlinkMap[old] = new_
		}
	}

	if len(gitlinkMap) == 0 {
		infof(flags, "No submodule commits were rewritten; parent history unchanged.\n")
		return 0
	}

	// Capture old parent HEAD.
	oldHeadSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("resolving parent HEAD: %v", err))
	}

	// Parent commit range: use entire history since --from is a submodule SHA
	// that doesn't exist in the parent repo.
	out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", "HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("listing parent commits: %v", err))
	}
	parentSHAs := git.SplitNonEmpty(out)

	infof(flags, "Rewriting %d parent commits (gitlink updates)...\n", len(parentSHAs))

	// Walk parent, only updating gitlinks (no blob changes, no message changes).
	parentTreeCache := make(map[string]string)
	parentShaMap, parentRewrittenCount, err := walkAndRewrite(ctx, parentSHAs, func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string) (CommitTransform, error) {
		newTreeSHA, err := replaceInTreeByBlobMap(ctx, info.Tree, nil, gitlinkMap, parentTreeCache)
		if err != nil {
			return CommitTransform{}, fmt.Errorf("updating gitlinks in tree for commit %s: %w", sha, err)
		}
		var xform CommitTransform
		if newTreeSHA != info.Tree {
			xform.TreeSHA = newTreeSHA
		}
		return xform, nil
	}, flags.verbose)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("parent walk and rewrite: %v", err))
	}

	// Finalize parent rewrite via shared pipeline. The VerifyFunc checks
	// that old submodule blobs are no longer reachable.
	exitCode := 0
	parentResult := RewriteResult{
		ShaMap:         parentShaMap,
		RewrittenCount: parentRewrittenCount,
		OldHeadSHA:     oldHeadSHA,
		SgDir:          sgDir,
		OpName:         "scrub-file",
		OplogExtra: map[string]interface{}{
			"file":      fullPath,
			"from":      from,
			"reason":    reason,
			"mode":      mode,
			"submodule": sub.RelativePath,
		},
	}

	parentVerifyFunc := func(ctx context.Context) error {
		if len(oldSubBlobSHAs) == 0 {
			return nil
		}
		os.Chdir(sub.WorkTreePath)
		defer os.Chdir(parentDir)

		infof(flags, "Verifying old blobs unreachable in submodule...\n")
		oldBlobList := make([]string, 0, len(oldSubBlobSHAs))
		for sha := range oldSubBlobSHAs {
			oldBlobList = append(oldBlobList, sha)
		}
		reachableBlobs, err := buildReachableBlobSet(ctx)
		if err != nil {
			return nil // can't verify, not fatal
		}
		for _, sha := range oldBlobList {
			if reachableBlobs[sha] {
				fmt.Fprintf(os.Stderr, "CRITICAL: old blob %s still reachable in submodule\n", shortSHA(sha))
				exitCode = 1
			}
		}
		if exitCode == 0 {
			infof(flags, "Verification passed: old blobs unreachable in submodule.\n")
		}
		return nil // non-fatal: exitCode tracks failures
	}

	if err := parentResult.Finalize(ctx, flags, cmd, nil, parentVerifyFunc); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("parent finalize: %v", err))
	}

	// JSON output
	if flags.json {
		rewrites := make(map[string]string)
		for old, new_ := range parentShaMap {
			if old != new_ {
				rewrites[old] = new_
			}
		}
		allTagRewrites := append(subTagRewrites, parentResult.TagRewrites...)
		if allTagRewrites == nil {
			allTagRewrites = []TagRewrite{}
		}
		jsonResult := ScrubFileResult{
			Version:          1,
			DryRun:           false,
			File:             fullPath,
			Mode:             mode,
			Rewrites:         rewrites,
			Tags:             allTagRewrites,
			CommitsRewritten: parentRewrittenCount + subRewrittenCount,
			OldHead:          oldHeadSHA,
			NewHead:          parentResult.NewHeadSHA,
		}
		emitJSON(jsonResult)
		return exitCode
	}

	// Summary.
	infof(flags, "\nScrub complete:\n")
	infof(flags, "  %d submodule commits rewritten\n", subRewrittenCount)
	infof(flags, "  %d parent commits rewritten (gitlink updates)\n", parentRewrittenCount)
	infof(flags, "  Old HEAD: %s\n", oldHeadSHA[:12])
	infof(flags, "  New HEAD: %s\n", parentResult.NewHeadSHA[:12])

	return exitCode
}

// untrackProtectedPaths runs "git rm --cached" for each path that was
// protected (tracked+gitignored) during SyncMainIndexWithWorktree, so future
// read-tree calls cannot overwrite them. Reports the list to stdout.
func untrackProtectedPaths(ctx context.Context, flags globalFlags, protectedPaths []string) {
	if len(protectedPaths) == 0 {
		return
	}
	for _, p := range protectedPaths {
		if _, _, err := git.Run(ctx, "rm", "--cached", "--", p); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to untrack gitignored file %s: %v\n", p, err)
		}
	}
	infof(flags, "Preserved %d tracked+gitignored file(s) (untracked from index):\n", len(protectedPaths))
	for _, p := range protectedPaths {
		infof(flags, "  %s\n", p)
	}
}

// runScrubMatch is in scrub_match.go
