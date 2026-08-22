package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/submodule"
	"github.com/smm-h/strictcli/go/strictcli"
)

// ScrubFileResult is what `scrub file` reports -- in both modes and in both
// renderings. There is no separate dry-run struct: a preview and an execution
// answer the same questions about the same rewrite, and every figure here is
// the one the human summary prints. The fields only an executed rewrite can
// know are pointers or omitempty, so a preview omits them rather than
// publishing a zero that reads as a fact.
type ScrubFileResult struct {
	Version     int    `json:"version"`
	DryRun      bool   `json:"dry_run"`
	File        string `json:"file"`
	Mode        string `json:"mode"`
	From        string `json:"from"`
	CommitCount int    `json:"commit_count"`
	OldHead     string `json:"old_head"`
	NewBlobSHA  string `json:"new_blob_sha,omitempty"`

	// Execute-only.
	Rewrites          map[string]string `json:"rewrites,omitempty"`
	Tags              []TagRewrite      `json:"tags,omitempty"`
	CommitsRewritten  *int              `json:"commits_rewritten,omitempty"`
	NewHead           string            `json:"new_head,omitempty"`
	PreRewriteRemotes map[string]string `json:"pre_rewrite_remotes,omitempty"`
	CleanupOK         *bool             `json:"cleanup_ok,omitempty"`
	CleanupErrors     []string          `json:"cleanup_errors,omitempty"`
}

// scrubFilePayloadSchema declares what `scrub file` puts in the envelope's
// payload. The execute-only members are declared but not required: a preview
// carries the range it would rewrite, an execution carries what it did.
var scrubFilePayloadSchema = strictcli.SchemaObject(
	map[string]interface{}{
		"version":             strictcli.SchemaType("integer"),
		"dry_run":             strictcli.SchemaType("boolean"),
		"file":                strictcli.SchemaType("string"),
		"mode":                strictcli.SchemaEnum("replace", "remove"),
		"from":                strictcli.SchemaType("string"),
		"commit_count":        strictcli.SchemaType("integer"),
		"old_head":            strictcli.SchemaType("string"),
		"new_blob_sha":        strictcli.SchemaType("string"),
		"rewrites":            scrubRewritesSchema,
		"tags":                scrubTagsSchema,
		"commits_rewritten":   strictcli.SchemaType("integer"),
		"new_head":            strictcli.SchemaType("string"),
		"pre_rewrite_remotes": scrubRewritesSchema,
		"cleanup_ok":          strictcli.SchemaType("boolean"),
		"cleanup_errors":      strictcli.SchemaArray(strictcli.SchemaType("string")),
	},
	[]string{"version", "dry_run", "file", "mode", "from", "commit_count", "old_head"},
	false,
)

func runScrubFile(flags globalFlags, kwargs map[string]interface{}) int {
	const cmd = "scrub file"

	from := kwargs["from"].(string)
	reason := kwargs["reason"].(string)
	filePath := kwargs["file"].(string)
	remapGlobs := kwargsStrSlice(kwargs["remap_shas_in"])
	validateRemapGlobs(remapGlobs)

	// Validation
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		die(exitcode.NotInitialized, err.Error())
	}

	ctx := flags.ctx()

	// The clean-tree requirement belongs to the execute path only, so it is
	// checked after the dry-run branch below (and after the submodule
	// delegation, which has its own execute path). A preview is exactly what a
	// dirty working tree is for.

	sgDir := repo.SafegitDir(gitDir)

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
		return runScrubFileInSubmodule(ctx, flags, cmd, filePath, subFilePath, targetSub, from, reason, remapGlobs, gitDir, sgDir)
	}

	// Resolve --from to a full SHA
	fromSHA, err := git.RevParse(ctx, from)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("resolving --from %q: %v", from, err))
	}

	// Ancestry guard: --from must be an ancestor of (or equal to) HEAD
	isAnc, err := git.IsAncestorOf(ctx, fromSHA, "HEAD")
	if err != nil {
		die(exitcode.General, fmt.Sprintf("checking ancestry of --from: %v", err))
	}
	if !isAnc {
		die(exitcode.General, fmt.Sprintf("--from commit %s is not an ancestor of HEAD", from))
	}

	// Capture old HEAD before any changes
	oldHeadSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		die(exitcode.General, fmt.Sprintf("resolving HEAD: %v", err))
	}

	// Determine replacement blob: if file exists on disk, compute its SHA
	// (read-only, no write to object store) for dry-run output. The blob is
	// written later only on the execute path.
	var newBlobSHA string
	var mode string
	if _, err := os.Stat(filePath); err == nil {
		newBlobSHA, err = git.HashObject(ctx, filePath)
		if err != nil {
			die(exitcode.General, fmt.Sprintf("hashing file %q: %v", filePath, err))
		}
		mode = "replace"
	} else {
		mode = "remove"
	}

	// Count commits to be rewritten (inclusive of --from)
	countOut, _, err := git.Run(ctx, "rev-list", "--count", fromSHA+"..HEAD")
	if err != nil {
		die(exitcode.General, fmt.Sprintf("counting commits: %v", err))
	}
	exclusiveCount, err := strconv.Atoi(strings.TrimSpace(countOut))
	if err != nil {
		die(exitcode.General, fmt.Sprintf("parsing commit count: %v", err))
	}
	commitCount := exclusiveCount + 1 // inclusive of fromSHA

	// The one computation both renderings read: the human summary below and the
	// payload state the same file, mode, range and commit count.
	result := ScrubFileResult{
		Version:     1,
		DryRun:      flags.dryRun,
		File:        filePath,
		Mode:        mode,
		From:        fromSHA,
		CommitCount: commitCount,
		OldHead:     oldHeadSHA,
		NewBlobSHA:  newBlobSHA,
	}

	// Summary
	infof(flags, "Scrub summary:\n")
	infof(flags, "  File:    %s\n", result.File)
	infof(flags, "  Mode:    %s\n", result.Mode)
	infof(flags, "  From:    %s\n", result.From[:12])
	infof(flags, "  Commits: %d\n", result.CommitCount)
	infof(flags, "  Reason:  %s\n", reason)

	// `scrub file` declares itself consequential, so the framework's confirm
	// protocol already took deliberate consent for this rewrite before dispatch.
	// The summary above says what and how much; a second prompt only asked the
	// question the framework had just had answered.
	if !flags.dryRun {
		infof(flags, "Rewriting %d commits. This cannot be undone.\n", result.CommitCount)
	}

	// Dry-run check: purely read-only, no lock needed. The rewrite is minted
	// through the effects handle, so the framework renders it -- as the would-do
	// log in human mode, as the envelope's preview member in machine mode.
	if flags.dryRun {
		recordHistoryRewrite(ctx, flags, oldHeadSHA)
		flags.payload(result)
		infof(flags, "Dry run: no changes made.\n")
		return 0
	}

	// Execute path only: a rewrite of a dirty tree would lose the uncommitted work.
	requireCleanTree(ctx)

	// Acquire rewrite lock to prevent concurrent scrub operations (execute path only).
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("loading config: %v", err))
	}
	timeout := time.Duration(cfg.Lock.AcquireTimeoutSeconds) * time.Second
	sharedDir := repo.SharedSafegitDir(ctx, gitDir)
	lk, err := lock.Acquire(sharedDir, sgDir, lock.RewriteRef, "scrub-file", timeout)
	if err != nil {
		// The real error, not a fixed sentence: it names the ref and the
		// process still holding it, which is the only thing that tells the
		// operator what to look at. A timeout gets its own exit code so a
		// caller can tell contention apart from every other lock failure.
		if lock.IsTimeout(err) {
			die(exitcode.LockTimeout, err.Error())
		}
		die(exitcode.General, fmt.Sprintf("acquiring rewrite lock: %v", err))
	}
	defer lk.Release()

	// Write the replacement blob to the object store (execute path only).
	if mode == "replace" {
		newBlobSHA, err = git.HashObjectWrite(ctx, filePath)
		if err != nil {
			die(exitcode.General, fmt.Sprintf("writing blob for %q: %v", filePath, err))
		}
	}

	// Commit walker: topo-order, parents before children (inclusive of fromSHA)
	out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", fromSHA+"..HEAD")
	if err != nil {
		die(exitcode.General, fmt.Sprintf("listing commits: %v", err))
	}
	shas := append([]string{fromSHA}, git.SplitNonEmpty(out)...)

	// Track old blob SHAs that get replaced, for post-cleanup verification.
	oldBlobSHAs := make(map[string]bool)

	var remap *remapState
	if len(remapGlobs) > 0 {
		remap = newRemapState(remapGlobs, shas)
	}

	// What this walk decides to change, per commit. targetSeen answers a
	// different question from "did anything change": a target that is present
	// in history and already holds the replacement content changes nothing and
	// is a legitimate no-op, while a target present in NO commit is a typo, and
	// only the second is an error.
	intent := PerPathIntent()
	targetSeen := false

	treeCache := make(map[string]string)
	shaMap, rewrittenCount, err := walkAndRewrite(ctx, shas, func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string, shaMap map[string]string) (CommitTransform, error) {
		// Look up the old blob SHA at the target path before replacing. This is
		// what makes the declaration independent of the rewrite: the decision is
		// read off the ORIGINAL tree, not off the tree the rewrite produced.
		oldBlobSHA := lookupBlobAtPath(ctx, info.Tree, filePath)
		if oldBlobSHA != "" {
			targetSeen = true
			if oldBlobSHA != newBlobSHA {
				intent.Declare(sha, []string{filePath}, false)
				oldBlobSHAs[oldBlobSHA] = true
			}
		}

		newTreeSHA, err := replaceInTree(ctx, info.Tree, filePath, newBlobSHA, treeCache)
		if err != nil {
			return CommitTransform{}, fmt.Errorf("replacing in tree for commit %s: %w", sha, err)
		}
		// Remap full commit hashes in glob-matched files against the
		// growing SHA map (time-varying: no shared tree cache).
		if remap != nil {
			remappedTreeSHA, remappedPaths, err := remap.remapTree(ctx, newTreeSHA, "", shaMap)
			if err != nil {
				return CommitTransform{}, fmt.Errorf("commit %s: %w", sha, err)
			}
			newTreeSHA = remappedTreeSHA
			intent.Declare(sha, remappedPaths, false)
		}
		var xform CommitTransform
		if newTreeSHA != info.Tree {
			xform.TreeSHA = newTreeSHA
		}
		return xform, nil
	}, flags.verbose)
	if err != nil {
		die(exitcode.General, err.Error())
	}
	remap.reportStale(flags)

	// Populate RewriteResult for the shared post-rewrite pipeline.
	rewriteResult := RewriteResult{
		ShaMap:         shaMap,
		RewrittenCount: rewrittenCount,
		Intent:         intent,
		OldHeadSHA:     oldHeadSHA,
		SgDir:          sgDir,
		Reason:         reason,
		OpName:         "scrub-file",
		OplogExtra: map[string]interface{}{
			"file":   filePath,
			"from":   fromSHA,
			"reason": reason,
			"mode":   mode,
		},
	}

	// Tier A: the target was found somewhere, and the rewritten history says
	// what the mode asked for at every one of the commits it touched. Both are
	// refusals -- nothing has moved yet.
	tierA := func(ctx context.Context, plan *RefUpdatePlan) error {
		if !targetSeen {
			return fmt.Errorf("%q is not in any of the %d commits this command would rewrite, "+
				"so there was nothing to scrub; check the path (it is repository-relative, "+
				"not relative to your working directory) and the --from commit",
				filePath, len(shas))
		}
		infof(flags, "Checking the rewritten commits...\n")
		return verifyScrubbedFileContent(ctx, shaMap, filePath, mode, newBlobSHA, oldBlobSHAs, remapGlobs)
	}

	// Tier B: the old blobs are gone from the object store. It can only run
	// after cleanup, so a finding never aborts -- it exits nonzero naming what
	// survived, with the rewrite standing.
	tierB := func(ctx context.Context) error {
		if len(oldBlobSHAs) == 0 {
			return nil
		}
		infof(flags, "Verifying old blobs removed...\n")
		oldBlobList := make([]string, 0, len(oldBlobSHAs))
		for sha := range oldBlobSHAs {
			oldBlobList = append(oldBlobList, sha)
		}
		if err := verifyOldBlobsRemoved(ctx, oldBlobList); err != nil {
			fmt.Fprintln(os.Stderr, "Old file content may still be present in the local object store.")
			fmt.Fprintln(os.Stderr, "Run 'git reflog expire --expire=now --all && git gc --prune=now' to force cleanup.")
			return err
		}
		infof(flags, "Verification passed: all old blobs removed from object store.\n")
		return nil
	}

	if err := rewriteResult.Finalize(ctx, flags, cmd, RewriteHooks{TierA: tierA, TierB: tierB}); err != nil {
		dieFinalize("", err)
	}

	// The executed rewrite's own figures, added to the same struct the preview
	// would have carried.
	rewrites := make(map[string]string)
	for old, new_ := range shaMap {
		if old != new_ {
			rewrites[old] = new_
		}
	}
	tags := rewriteResult.TagRewrites
	if tags == nil {
		tags = []TagRewrite{}
	}
	result.Rewrites = rewrites
	result.Tags = tags
	result.CommitsRewritten = intPtr(rewrittenCount)
	result.NewHead = rewriteResult.NewHeadSHA
	result.PreRewriteRemotes = nonNilStringMap(rewriteResult.PreRewriteRemotes)
	result.CleanupOK = boolPtr(rewriteResult.CleanupOK)
	result.CleanupErrors = nonNilStrings(rewriteResult.CleanupErrors)
	flags.payload(result)

	// Summary
	infof(flags, "\nScrub complete:\n")
	infof(flags, "  %d commits rewritten\n", *result.CommitsRewritten)
	infof(flags, "  Old HEAD: %s\n", result.OldHead[:12])
	infof(flags, "  New HEAD: %s\n", result.NewHead[:12])

	return rewriteResult.TierBExit(exitcode.OK)
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
	remapGlobs []string,
	gitDir string,
	sgDir string,
) int {
	infof(flags, "File %q is inside submodule [%s], scrubbing as %q within submodule.\n",
		fullPath, sub.RelativePath, subFilePath)

	// Ensure safegit is initialized for the submodule.
	if err := ensureInitialized(flags, sub.GitDir); err != nil {
		die(exitcode.General, fmt.Sprintf("initializing safegit for submodule %s: %v", sub.RelativePath, err))
	}

	// Context-scoped git directory targeting: all git commands using subCtx
	// will target the submodule's repo without needing os.Chdir.
	subCtx := git.WithDir(ctx, sub.GitDir, sub.WorkTreePath)

	// Resolve --from within the submodule. Since the user's --from likely
	// refers to the parent repo, we use the submodule's entire history.
	// The parent's --from will be used when rewriting parent gitlinks.
	subFromSHA, subFromErr := git.RevParse(subCtx, from)
	useEntireSubHistory := subFromErr != nil

	if !useEntireSubHistory {
		// Verify it's an ancestor of submodule HEAD.
		isAnc, err := git.IsAncestorOf(subCtx, subFromSHA, "HEAD")
		if err != nil || !isAnc {
			useEntireSubHistory = true
		}
	}

	// Determine replacement blob within the submodule context.
	// Use absolute path for os.Stat since CWD is the parent repo.
	// HashObject (read-only) computes the SHA for dry-run output; the blob
	// is written later only on the execute path.
	var newBlobSHA string
	var mode string
	absSubFilePath := filepath.Join(sub.WorkTreePath, subFilePath)
	if _, err := os.Stat(absSubFilePath); err == nil {
		newBlobSHA, err = git.HashObject(subCtx, subFilePath)
		if err != nil {
			die(exitcode.General, fmt.Sprintf("hashing file %q in submodule: %v", subFilePath, err))
		}
		mode = "replace"
	} else {
		mode = "remove"
	}

	// Get submodule commit range.
	var subSHAs []string
	if useEntireSubHistory {
		out, _, err := git.Run(subCtx, "rev-list", "--topo-order", "--reverse", "HEAD")
		if err != nil {
			die(exitcode.General, fmt.Sprintf("submodule: listing commits: %v", err))
		}
		subSHAs = git.SplitNonEmpty(out)
	} else {
		out, _, err := git.Run(subCtx, "rev-list", "--topo-order", "--reverse", subFromSHA+"..HEAD")
		if err != nil {
			die(exitcode.General, fmt.Sprintf("submodule: listing commits: %v", err))
		}
		subSHAs = append([]string{subFromSHA}, git.SplitNonEmpty(out)...)
	}

	subCommitCount := len(subSHAs)

	// The one computation both renderings read, exactly as on the non-submodule
	// path. commit_count is the submodule commit count -- the number the summary
	// line prints -- and the parent's gitlink commits follow from it.
	result := ScrubFileResult{
		Version:     1,
		DryRun:      flags.dryRun,
		File:        fullPath,
		Mode:        mode,
		From:        from,
		CommitCount: subCommitCount,
		NewBlobSHA:  newBlobSHA,
	}

	// Summary
	infof(flags, "Scrub summary:\n")
	infof(flags, "  File:       %s (in submodule %s)\n", subFilePath, sub.RelativePath)
	infof(flags, "  Mode:       %s\n", result.Mode)
	infof(flags, "  Sub commits: %d\n", result.CommitCount)
	infof(flags, "  Reason:     %s\n", reason)

	// Same as the non-submodule path: consent for the rewrite was taken by the
	// framework before dispatch. The wider blast radius -- the parent moves too,
	// because its gitlink has to follow the submodule -- is a consequence of the
	// path the caller named, so it is stated rather than asked.
	if !flags.dryRun {
		infof(flags, "Rewriting %d submodule commits, and the parent history that points at them. This cannot be undone.\n", subCommitCount)
	}

	if flags.dryRun {
		// old_head stays empty here: the parent's HEAD is resolved on the
		// execute path below, and a preview states nothing it has not read.
		recordHistoryRewrite(ctx, flags, result.OldHead)
		flags.payload(result)
		infof(flags, "Dry run: no changes made.\n")
		return 0
	}

	// Execute path only, and the caller's check was moved past its own dry-run
	// branch: the parent's tree must still be clean before anything is rewritten.
	requireCleanTree(ctx)

	// Write the replacement blob to the submodule's object store (execute path only).
	if mode == "replace" {
		var writeErr error
		newBlobSHA, writeErr = git.HashObjectWrite(subCtx, subFilePath)
		if writeErr != nil {
			die(exitcode.General, fmt.Sprintf("writing blob for %q in submodule: %v", subFilePath, writeErr))
		}
	}

	// Capture old submodule HEAD before rewriting.
	oldSubHeadSHA, err := git.RevParse(subCtx, "HEAD")
	if err != nil {
		die(exitcode.General, fmt.Sprintf("resolving submodule HEAD: %v", err))
	}

	// Walk and rewrite submodule commits. Note: --remap-shas-in is not
	// applied inside submodule histories (documented limitation).
	oldSubBlobSHAs := make(map[string]bool)
	subTreeCache := make(map[string]string)
	subIntent := PerPathIntent()
	subTargetSeen := false
	subShaMap, subRewrittenCount, err := walkAndRewrite(subCtx, subSHAs, func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string, shaMap map[string]string) (CommitTransform, error) {
		oldBlobSHA := lookupBlobAtPath(ctx, info.Tree, subFilePath)
		if oldBlobSHA != "" {
			subTargetSeen = true
			if oldBlobSHA != newBlobSHA {
				subIntent.Declare(sha, []string{subFilePath}, false)
				oldSubBlobSHAs[oldBlobSHA] = true
			}
		}
		newTreeSHA, err := replaceInTree(ctx, info.Tree, subFilePath, newBlobSHA, subTreeCache)
		if err != nil {
			return CommitTransform{}, fmt.Errorf("replacing in tree for commit %s: %w", sha, err)
		}
		var xform CommitTransform
		if newTreeSHA != info.Tree {
			xform.TreeSHA = newTreeSHA
		}
		return xform, nil
	}, flags.verbose)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("submodule walk and rewrite: %v", err))
	}

	// Finalize submodule rewrite via shared pipeline.
	infof(flags, "Finalizing submodule [%s] rewrite...\n", sub.RelativePath)
	subResult := RewriteResult{
		ShaMap:         subShaMap,
		RewrittenCount: subRewrittenCount,
		Intent:         subIntent,
		OldHeadSHA:     oldSubHeadSHA,
		SgDir:          sub.SafegitDir,
		Reason:         reason,
		OpName:         "scrub-file",
		OplogExtra: map[string]interface{}{
			"file":   subFilePath,
			"reason": reason,
			"mode":   mode,
		},
	}
	subTierA := func(ctx context.Context, plan *RefUpdatePlan) error {
		if !subTargetSeen {
			return fmt.Errorf("%q is not in any of the %d submodule commits this command would rewrite, "+
				"so there was nothing to scrub; check the path (it is relative to the submodule root)",
				subFilePath, len(subSHAs))
		}
		return verifyScrubbedFileContent(ctx, subShaMap, subFilePath, mode, newBlobSHA, oldSubBlobSHAs, nil)
	}
	if err := subResult.Finalize(subCtx, flags, cmd, RewriteHooks{TierA: subTierA}); err != nil {
		dieFinalize(fmt.Sprintf("submodule %s", sub.RelativePath), err)
	}
	subTagRewrites := subResult.TagRewrites

	infof(flags, "  [%s] %d commits rewritten\n", sub.RelativePath, subRewrittenCount)

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
		die(exitcode.General, fmt.Sprintf("resolving parent HEAD: %v", err))
	}

	// Parent commit range: use entire history since --from is a submodule SHA
	// that doesn't exist in the parent repo.
	out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", "HEAD")
	if err != nil {
		die(exitcode.General, fmt.Sprintf("listing parent commits: %v", err))
	}
	parentSHAs := git.SplitNonEmpty(out)

	infof(flags, "Rewriting %d parent commits (gitlink updates)...\n", len(parentSHAs))

	// Walk parent, updating gitlinks (no blob changes, no message changes)
	// and remapping commit hashes in glob-matched parent files when
	// --remap-shas-in is set.
	var parentRemap *remapState
	if len(remapGlobs) > 0 {
		parentRemap = newRemapState(remapGlobs, parentSHAs)
	}
	parentTreeCache := make(map[string]treeRewrite)
	parentIntent := PerPathIntent()
	parentShaMap, parentRewrittenCount, err := walkAndRewrite(ctx, parentSHAs, func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string, shaMap map[string]string) (CommitTransform, error) {
		newTreeSHA, changedPaths, err := replaceInTreeByBlobMap(ctx, info.Tree, nil, gitlinkMap, parentTreeCache)
		if err != nil {
			return CommitTransform{}, fmt.Errorf("updating gitlinks in tree for commit %s: %w", sha, err)
		}
		intentDeclare := changedPaths
		if parentRemap != nil {
			remappedTreeSHA, remappedPaths, err := parentRemap.remapTree(ctx, newTreeSHA, "", shaMap)
			if err != nil {
				return CommitTransform{}, fmt.Errorf("commit %s: %w", sha, err)
			}
			newTreeSHA = remappedTreeSHA
			intentDeclare = append(intentDeclare, remappedPaths...)
		}
		parentIntent.Declare(sha, intentDeclare, false)
		var xform CommitTransform
		if newTreeSHA != info.Tree {
			xform.TreeSHA = newTreeSHA
		}
		return xform, nil
	}, flags.verbose)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("parent walk and rewrite: %v", err))
	}
	parentRemap.reportStale(flags)

	// Finalize parent rewrite via shared pipeline. Its Tier B hook checks that
	// old submodule blobs are no longer reachable.
	parentResult := RewriteResult{
		ShaMap:         parentShaMap,
		RewrittenCount: parentRewrittenCount,
		Intent:         parentIntent,
		OldHeadSHA:     oldHeadSHA,
		SgDir:          sgDir,
		Reason:         reason,
		OpName:         "scrub-file",
		OplogExtra: map[string]interface{}{
			"file":      fullPath,
			"from":      from,
			"reason":    reason,
			"mode":      mode,
			"submodule": sub.RelativePath,
		},
	}

	parentTierB := func(ctx context.Context) error {
		if len(oldSubBlobSHAs) == 0 {
			return nil
		}
		infof(flags, "Verifying old blobs unreachable in submodule...\n")
		// Use subCtx to target the submodule's object store without chdir.
		reachableBlobs, err := buildReachableBlobSet(subCtx)
		if err != nil {
			return fmt.Errorf("could not read the submodule's reachable objects to verify the old blobs are gone: %v", err)
		}
		var surviving []string
		for sha := range oldSubBlobSHAs {
			if reachableBlobs[sha] {
				surviving = append(surviving, shortSHA(sha))
			}
		}
		if len(surviving) > 0 {
			sort.Strings(surviving)
			return fmt.Errorf("%d old blob(s) still reachable in submodule %s: %s",
				len(surviving), sub.RelativePath, strings.Join(surviving, ", "))
		}
		infof(flags, "Verification passed: old blobs unreachable in submodule.\n")
		return nil
	}

	if err := parentResult.Finalize(ctx, flags, cmd, RewriteHooks{TierB: parentTierB}); err != nil {
		dieFinalize("parent", err)
	}

	// The executed rewrite's own figures, added to the same struct the preview
	// carried.
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
	result.Rewrites = rewrites
	result.Tags = allTagRewrites
	result.CommitsRewritten = intPtr(parentRewrittenCount + subRewrittenCount)
	result.OldHead = oldHeadSHA
	result.NewHead = parentResult.NewHeadSHA
	result.PreRewriteRemotes = nonNilStringMap(parentResult.PreRewriteRemotes)
	result.CleanupOK = boolPtr(parentResult.CleanupOK)
	result.CleanupErrors = nonNilStrings(parentResult.CleanupErrors)
	flags.payload(result)

	// Summary.
	infof(flags, "\nScrub complete:\n")
	infof(flags, "  %d submodule commits rewritten\n", subRewrittenCount)
	infof(flags, "  %d parent commits rewritten (gitlink updates)\n", parentRewrittenCount)
	infof(flags, "  Old HEAD: %s\n", result.OldHead[:12])
	infof(flags, "  New HEAD: %s\n", result.NewHead[:12])

	// A submodule-side finding and a parent-side one are the same verdict: the
	// rewrite stands and something after it did not complete.
	prior := exitcode.OK
	if len(subResult.TierBFailures) > 0 {
		prior = exitcode.RewriteIncomplete
	}
	return parentResult.TierBExit(prior)
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
