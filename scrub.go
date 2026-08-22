package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/submodule"
	"github.com/smm-h/safegit/internal/trailer"
	"github.com/smm-h/strictcli/go/strictcli"
)

// ScrubFileResult is what `scrub file` reports -- in both modes and in both
// renderings. There is no separate dry-run struct: a preview and an execution
// answer the same questions about the same rewrite, and every figure here is
// the one the human summary prints. The fields only an executed rewrite can
// know are pointers or omitempty, so a preview omits them rather than
// publishing a zero that reads as a fact.
type ScrubFileResult struct {
	Version int    `json:"version"`
	DryRun  bool   `json:"dry_run"`
	File    string `json:"file"`
	// Mode keeps the payload spellings "replace" and "remove" the flags no
	// longer use: --replace-with elects "replace", --delete elects "remove".
	// The flag names say what the operator does; these say what the rewrite
	// does to each tree, which is what a machine reader is asking about.
	Mode string `json:"mode"`
	// Range says which selector member was elected: "range" for --from,
	// "entire_history" for --entire-history. From carries the resolved --from
	// commit and is empty for an entire-history scrub.
	Range       string `json:"range"`
	From        string `json:"from"`
	CommitCount int    `json:"commit_count"`
	OldHead     string `json:"old_head"`
	NewBlobSHA  string `json:"new_blob_sha,omitempty"`

	// Execute-only.
	Rewrites         map[string]string `json:"rewrites,omitempty"`
	Tags             []TagRewrite      `json:"tags,omitempty"`
	CommitsRewritten *int              `json:"commits_rewritten,omitempty"`
	// MessagesModified counts the commit messages this rewrite edited. A file
	// scrub edits one thing in a message and only in --delete mode: the move
	// records that name the erased path (see removeScrubbedMoveRecords).
	MessagesModified  *int              `json:"messages_modified,omitempty"`
	NewHead           string            `json:"new_head,omitempty"`
	PreRewriteRemotes map[string]string `json:"pre_rewrite_remotes,omitempty"`
	CleanupOK         *bool             `json:"cleanup_ok,omitempty"`
	CleanupErrors     []string          `json:"cleanup_errors,omitempty"`
	// SyncSkipped is true when the working-tree sync was deliberately not
	// performed because work that was not this rewrite's appeared while the
	// refs were moving. The refs still moved; the working tree was left alone.
	SyncSkipped bool `json:"sync_skipped,omitempty"`
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
		"range":               strictcli.SchemaEnum("range", "entire_history"),
		"from":                strictcli.SchemaType("string"),
		"commit_count":        strictcli.SchemaType("integer"),
		"old_head":            strictcli.SchemaType("string"),
		"new_blob_sha":        strictcli.SchemaType("string"),
		"rewrites":            scrubRewritesSchema,
		"tags":                scrubTagsSchema,
		"commits_rewritten":   strictcli.SchemaType("integer"),
		"messages_modified":   strictcli.SchemaType("integer"),
		"new_head":            strictcli.SchemaType("string"),
		"pre_rewrite_remotes": scrubRewritesSchema,
		"cleanup_ok":          strictcli.SchemaType("boolean"),
		"cleanup_errors":      strictcli.SchemaArray(strictcli.SchemaType("string")),
		"sync_skipped":        strictcli.SchemaType("boolean"),
	},
	[]string{"version", "dry_run", "file", "mode", "range", "from", "commit_count", "old_head"},
	false,
)

// removeScrubbedMoveRecords is the message half of a file scrub: the move
// records naming the path, removed in the SAME rewrite that erases it.
//
// The two modes ask opposite things of a record, and the difference is not a
// policy choice but what each mode does to the path:
//
//   - --delete ERASES the path from every tree in range. A record naming it --
//     as its old side, its new side, or anywhere inside a subtree prefix that
//     covers it -- is then a reference to something the rewrite is removing,
//     sitting in the one place a tree rewrite does not reach. It is removed
//     with the path, whole (see trailer.RemoveMovedRecordsNaming: half a move
//     is not a smaller move, it is a malformed one).
//   - --replace-with KEEPS the path and changes what it holds. A record saying
//     content moved to that path is still exactly as true afterwards as it was
//     before, so nothing is edited. Rewriting a message here would be the
//     rewrite changing something nothing asked it to change.
//
// The caller declares the resulting message change per commit, which is what
// keeps the rewrite's own Tier A verification -- "a rewrite may change what it
// said it would change, and nothing else" -- able to account for it.
func removeScrubbedMoveRecords(mode, message, filePath string) (string, bool) {
	if mode != "remove" {
		return message, false
	}
	stripped, removed := trailer.RemoveMovedRecordsNaming(message, filePath)
	if removed && stripped == "" {
		// The records were the WHOLE message, so what is left is nothing. The
		// walker reads an empty CommitTransform.Message as "keep the original",
		// and keeping the original here would leave the record standing after
		// the walk had already declared it gone -- which the rewrite's own
		// verification then refuses, taking the whole rewrite down over a commit
		// whose message happened to be one trailer. A bare newline is the
		// emptiest message the sentinel can express.
		stripped = "\n"
	}
	return stripped, removed
}

func runScrubFile(flags globalFlags, kwargs map[string]interface{}) int {
	const cmd = "scrub file"

	// Both selectors are required and elect exactly one member, so "neither was
	// given" is unrepresentable rather than something this handler refuses.
	mode, replacementPath := scrubFileMode(kwargs)
	from, entireHistory := scrubRange(kwargs)
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
		// A submodule-path scrub rewrites BOTH repositories, so it takes both
		// rewrite locks: the parent's here, before the delegation, and the
		// submodule's own inside the delegated flow (see acquireRewriteLock for
		// the ordering declaration). Taking the parent's first is what makes the
		// order total and the pair deadlock-free.
		//
		// A preview takes neither: it performs no mutation, and the delegated
		// flow has its own dry-run branch.
		if !flags.dryRun {
			lk := acquireRewriteLock(ctx, flags, gitDir, sgDir, "scrub-file")
			defer lk.Release()
		}
		return runScrubFileInSubmodule(ctx, flags, cmd, filePath, subFilePath, targetSub, from, entireHistory, mode, replacementPath, reason, remapGlobs, gitDir, sgDir)
	}

	// Resolve the elected range: a --from commit that must be an ancestor of
	// HEAD, or the whole history.
	var fromSHA string
	if from != nil {
		var err error
		fromSHA, err = git.RevParse(ctx, *from)
		if err != nil {
			die(exitcode.General, fmt.Sprintf("resolving --from %q: %v", *from, err))
		}
		isAnc, err := git.IsAncestorOf(ctx, fromSHA, "HEAD")
		if err != nil {
			die(exitcode.General, fmt.Sprintf("checking ancestry of --from: %v", err))
		}
		if !isAnc {
			die(exitcode.General, fmt.Sprintf("--from commit %s is not an ancestor of HEAD", *from))
		}
	}

	// Capture old HEAD before any changes
	oldHeadSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		die(exitcode.General, fmt.Sprintf("resolving HEAD: %v", err))
	}

	// Read the replacement source, if there is one. It is read HERE, through
	// the operating system, because the path is the operator's: it resolves
	// against the directory the command was typed in, while the target argument
	// is repository-relative. Handing the path to git hash-object would resolve
	// it against the pinned repository root instead, which is a different file.
	var replacement []byte
	var newBlobSHA string
	if mode == "replace" {
		replacement = readReplacementSource(replacementPath)
		newBlobSHA, err = git.HashObjectBytes(ctx, replacement)
		if err != nil {
			die(exitcode.General, fmt.Sprintf("hashing the replacement file %q: %v", replacementPath, err))
		}
	}

	// Count commits to be rewritten (inclusive of --from).
	commitCount := scrubFileCommitCount(ctx, fromSHA, entireHistory)

	rangeKind := "range"
	if entireHistory {
		rangeKind = "entire_history"
	}

	// The one computation both renderings read: the human summary below and the
	// payload state the same file, mode, range and commit count.
	result := ScrubFileResult{
		Version:     1,
		DryRun:      flags.dryRun,
		File:        filePath,
		Mode:        mode,
		Range:       rangeKind,
		From:        fromSHA,
		CommitCount: commitCount,
		OldHead:     oldHeadSHA,
		NewBlobSHA:  newBlobSHA,
	}

	// Summary
	infof(flags, "Scrub summary:\n")
	infof(flags, "  File:    %s\n", result.File)
	infof(flags, "  Mode:    %s\n", scrubFileModeSummary(mode, replacementPath))
	infof(flags, "  Range:   %s\n", scrubRangeSummary(result.From, entireHistory))
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
	lk := acquireRewriteLock(ctx, flags, gitDir, sgDir, "scrub-file")
	defer lk.Release()

	// Write the replacement blob to the object store (execute path only).
	if mode == "replace" {
		newBlobSHA, err = git.HashObjectWriteBytes(ctx, replacement)
		if err != nil {
			die(exitcode.General, fmt.Sprintf("writing the replacement blob from %q: %v", replacementPath, err))
		}
	}

	// Commit walker: topo-order, parents before children (inclusive of fromSHA)
	shas := scrubFileCommitRange(ctx, fromSHA, entireHistory)

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
	messagesModified := 0

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
		if newMessage, removed := removeScrubbedMoveRecords(mode, info.Message, filePath); removed {
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
			"range":  rangeKind,
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
	result.SyncSkipped = rewriteResult.SyncSkipped
	result.MessagesModified = intPtr(messagesModified)
	flags.payload(result)

	// Summary
	infof(flags, "\nScrub complete:\n")
	infof(flags, "  %d commits rewritten\n", *result.CommitsRewritten)
	infof(flags, "  %d commit messages modified (move records naming the erased path)\n", messagesModified)
	infof(flags, "  Old HEAD: %s\n", result.OldHead[:12])
	infof(flags, "  New HEAD: %s\n", result.NewHead[:12])
	printScopeNotice(flags, rewriteResult.Ref)
	printRotationNotice(flags, recheckCommandForRemovedContent())

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
	from *string, // the elected --from commit, nil for --entire-history
	entireHistory bool,
	mode string, // "replace" or "remove", elected by the caller
	replacementPath string, // operator-relative source path, empty in remove mode
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

	// Resolve the elected range INSIDE the submodule. A --from commit is a
	// commit of whichever history it names, and a parent-repo hash names
	// nothing here -- so a --from that the submodule cannot resolve, or that is
	// not an ancestor of its HEAD, is refused.
	//
	// It used to silently fall back to rewriting the submodule's ENTIRE
	// history: a bounded request quietly became an unbounded rewrite, and the
	// operator was told only how many commits were rewritten, never that the
	// boundary they asked for had been dropped.
	var subFromSHA string
	if !entireHistory {
		// "^{commit}" is what makes this a real existence check: bare rev-parse
		// echoes any 40-hex string back unchanged, so a parent-repository hash
		// would "resolve" here and fail confusingly one step later.
		resolved, err := git.RevParse(subCtx, *from+"^{commit}")
		if err != nil {
			die(exitcode.General, fmt.Sprintf(
				"--from %q does not name a commit in submodule %s (a parent-repository commit hash means nothing inside a submodule).\n"+
					"Pass --from with a commit from the submodule's own history, or pass --entire-history to rewrite all of it deliberately.",
				*from, sub.RelativePath))
		}
		isAnc, err := git.IsAncestorOf(subCtx, resolved, "HEAD")
		if err != nil {
			die(exitcode.General, fmt.Sprintf("checking ancestry of --from inside submodule %s: %v", sub.RelativePath, err))
		}
		if !isAnc {
			die(exitcode.General, fmt.Sprintf(
				"--from commit %s is not an ancestor of submodule %s's HEAD.\n"+
					"Pass --from with a commit from the submodule's own history, or pass --entire-history to rewrite all of it deliberately.",
				*from, sub.RelativePath))
		}
		subFromSHA = resolved
	}

	// Read the replacement source at the OPERATOR's current directory, exactly
	// as the non-submodule path does. HashObjectBytes computes the SHA for the
	// preview without writing; the blob is written to the SUBMODULE's object
	// store later, on the execute path only.
	var replacement []byte
	var newBlobSHA string
	if mode == "replace" {
		replacement = readReplacementSource(replacementPath)
		var err error
		newBlobSHA, err = git.HashObjectBytes(subCtx, replacement)
		if err != nil {
			die(exitcode.General, fmt.Sprintf("hashing the replacement file %q: %v", replacementPath, err))
		}
	}

	// Get submodule commit range.
	subSHAs := scrubFileCommitRange(subCtx, subFromSHA, entireHistory)

	subCommitCount := len(subSHAs)

	// The one computation both renderings read, exactly as on the non-submodule
	// path. commit_count is the submodule commit count -- the number the summary
	// line prints -- and the parent's gitlink commits follow from it.
	rangeKind := "range"
	if entireHistory {
		rangeKind = "entire_history"
	}
	result := ScrubFileResult{
		Version:     1,
		DryRun:      flags.dryRun,
		File:        fullPath,
		Mode:        mode,
		Range:       rangeKind,
		From:        subFromSHA,
		CommitCount: subCommitCount,
		NewBlobSHA:  newBlobSHA,
	}

	// Summary
	infof(flags, "Scrub summary:\n")
	infof(flags, "  File:       %s (in submodule %s)\n", subFilePath, sub.RelativePath)
	infof(flags, "  Mode:       %s\n", scrubFileModeSummary(mode, replacementPath))
	infof(flags, "  Range:      %s\n", scrubRangeSummary(subFromSHA, entireHistory))
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

	// The submodule is a repository of its own -- its own object store, its own
	// refs, its own .git/safegit -- and this flow rewrites its history, so it
	// contends on its own rewrite lock. The caller holds the parent's already;
	// this is the inner half of the parent-then-submodule order declared at
	// acquireRewriteLock. Both are held through the verification and the
	// publication of both repositories, which is what makes the cleanliness
	// re-check exclusive on the submodule side too.
	subLk := acquireRewriteLock(subCtx, flags, sub.GitDir, sub.SafegitDir, "scrub-file")
	defer subLk.Release()

	// Write the replacement blob to the submodule's object store (execute path only).
	if mode == "replace" {
		var writeErr error
		newBlobSHA, writeErr = git.HashObjectWriteBytes(subCtx, replacement)
		if writeErr != nil {
			die(exitcode.General, fmt.Sprintf("writing the replacement blob from %q into submodule %s: %v", replacementPath, sub.RelativePath, writeErr))
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
	subMessagesModified := 0
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
		// A submodule's commits carry their own move records, naming paths
		// relative to the submodule root -- which is the path this walk is
		// erasing. Same treatment, same declaration.
		if newMessage, removed := removeScrubbedMoveRecords(mode, info.Message, subFilePath); removed {
			xform.Message = newMessage
			subMessagesModified++
			subIntent.Declare(sha, nil, true)
		}
		return xform, nil
	}, flags.verbose)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("submodule walk and rewrite: %v", err))
	}

	// The submodule's rewritten commits now exist as unreachable objects. They
	// are NOT published yet: the parent walk below builds its new commits
	// against these SHAs, and both histories are verified before either
	// repository's refs move, so a failure on the parent side cannot leave a
	// published submodule behind.
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
	subPending := &pendingRewrite{
		Label:  fmt.Sprintf("submodule %s", sub.RelativePath),
		Ctx:    subCtx,
		Result: &subResult,
		Hooks:  RewriteHooks{TierA: subTierA},
	}

	infof(flags, "  [%s] %d commits rewritten\n", sub.RelativePath, subRewrittenCount)

	// Build gitlink map from submodule SHA mappings.
	gitlinkMap := make(map[string]string)
	for old, new_ := range subShaMap {
		if old != new_ {
			gitlinkMap[old] = new_
		}
	}

	// Capture old parent HEAD. It is read here, before the no-gitlink-moved
	// branch below, because that branch reports it too: a run that leaves the
	// parent alone still has to say which commit the parent is at.
	oldHeadSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		die(exitcode.General, fmt.Sprintf("resolving parent HEAD: %v", err))
	}
	result.OldHead = oldHeadSHA

	if len(gitlinkMap) == 0 {
		// No submodule commit moved, so the parent has nothing to follow. The
		// submodule still gets verified and published on its own: its tag
		// annotations or tagger identity may have been rewritten even when every
		// commit maps to itself.
		infof(flags, "No submodule commits were rewritten; parent history unchanged.\n")
		only := []*pendingRewrite{subPending}
		if label, err := prepareAll(flags, only); err != nil {
			dieFinalize(label, err)
		}
		if label, err := publishAll(flags, cmd, only); err != nil {
			dieFinalize(label, err)
		}

		// The same struct the other exits carry, filled with what THIS run did:
		// the submodule was the only repository published, so its figures are
		// the ones that exist. rewrites is empty by construction (it is the map
		// this branch tested), and new_head stays absent because the parent
		// published no new head -- old_head above already says where it is.
		subTags := subResult.TagRewrites
		if subTags == nil {
			subTags = []TagRewrite{}
		}
		result.Rewrites = map[string]string{}
		result.Tags = subTags
		result.CommitsRewritten = intPtr(subRewrittenCount)
		result.MessagesModified = intPtr(subMessagesModified)
		result.PreRewriteRemotes = nonNilStringMap(subResult.PreRewriteRemotes)
		result.CleanupOK = boolPtr(subResult.CleanupOK)
		result.CleanupErrors = nonNilStrings(subResult.CleanupErrors)
		result.SyncSkipped = subResult.SyncSkipped
		flags.payload(result)

		return subResult.TierBExit(exitcode.OK)
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
			"range":     rangeKind,
			"from":      subFromSHA,
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

	// Objects before refs, across both repositories. Both histories are now
	// written as unreachable objects, so prepare verifies BOTH before publish
	// moves anything: a refusal on either side leaves the submodule's refs and
	// the parent's refs exactly where they were.
	//
	// Publication order is submodule first. A crash between the two leaves a
	// parent whose gitlink still names a submodule commit that exists, which is
	// the recoverable half of the window; the reverse would leave the parent
	// pointing at submodule commits no ref keeps alive.
	both := []*pendingRewrite{
		subPending,
		{
			Label:  "parent",
			Ctx:    ctx,
			Result: &parentResult,
			Hooks:  RewriteHooks{TierB: parentTierB},
		},
	}
	infof(flags, "Verifying the submodule and parent rewrites before either is published...\n")
	if label, err := prepareAll(flags, both); err != nil {
		dieFinalize(label, err)
	}
	if label, err := publishAll(flags, cmd, both); err != nil {
		dieFinalize(label, err)
	}
	subTagRewrites := subResult.TagRewrites

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
	// The parent walk only moves gitlinks and never edits a message, so every
	// message this command changed is one of the submodule's.
	result.MessagesModified = intPtr(subMessagesModified)
	result.NewHead = parentResult.NewHeadSHA
	result.PreRewriteRemotes = nonNilStringMap(parentResult.PreRewriteRemotes)
	result.CleanupOK = boolPtr(parentResult.CleanupOK)
	result.CleanupErrors = nonNilStrings(parentResult.CleanupErrors)
	// One command, one outcome: a skipped sync is reported whichever of the two
	// repositories left its working tree alone. The payload carries no
	// per-repository record to hang it on, so it is the disjunction, exactly as
	// the exit code is.
	result.SyncSkipped = parentResult.SyncSkipped || subResult.SyncSkipped
	flags.payload(result)

	// Summary.
	infof(flags, "\nScrub complete:\n")
	infof(flags, "  %d submodule commits rewritten\n", subRewrittenCount)
	infof(flags, "  %d submodule commit messages modified (move records naming the erased path)\n", subMessagesModified)
	infof(flags, "  %d parent commits rewritten (gitlink updates)\n", parentRewrittenCount)
	infof(flags, "  Old HEAD: %s\n", result.OldHead[:12])
	infof(flags, "  New HEAD: %s\n", result.NewHead[:12])
	printScopeNotice(flags, parentResult.Ref, fmt.Sprintf("%s in submodule [%s]", subResult.Ref, sub.RelativePath))
	printRotationNotice(flags, recheckCommandForRemovedContent())

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
