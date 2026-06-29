package main

import (
	"bytes"
	"context"
	crypto_rand "crypto/rand"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/scan"
	"github.com/smm-h/safegit/internal/submodule"
)

// ScrubMatchResult is the JSON output for `scrub match` in execute mode.
type ScrubMatchResult struct {
	Version          int               `json:"version"`
	DryRun           bool              `json:"dry_run"`
	Rewrites         map[string]string `json:"rewrites"`
	Tags             []TagRewrite      `json:"tags"`
	CommitsRewritten int               `json:"commits_rewritten"`
	BlobsReplaced    int               `json:"blobs_replaced"`
	MessagesModified int               `json:"messages_modified"`
	TagsRewritten    int               `json:"tags_rewritten"`
	OldHead          string            `json:"old_head"`
	NewHead          string            `json:"new_head"`
}

// ScrubMatchDryRunResult is the JSON output for `scrub match --dry-run`.
type ScrubMatchDryRunResult struct {
	Version          int    `json:"version"`
	DryRun           bool   `json:"dry_run"`
	Pattern          string `json:"pattern"`
	Scope            string `json:"scope,omitempty"`
	ScopeFilter      string `json:"scope_filter,omitempty"`
	ObjectsScanned   int    `json:"objects_scanned"`
	BinarySkipped    int    `json:"binary_skipped"`
	TotalMatches     int    `json:"total_matches"`
	BlobMatches      int    `json:"blob_matches"`
	CommitMatches    int    `json:"commit_matches"`
	TagMatches       int    `json:"tag_matches"`
	FileMatches      int    `json:"file_matches"`
	EstimatedCommits int    `json:"estimated_commits,omitempty"`
}

func runScrubMatch(flags globalFlags, kwargs map[string]interface{}) int {
	const cmd = "scrub match"

	// Flag extraction
	pattern := kwargs["pattern"].(string)
	var replace string
	var mangleMode bool
	if kwargs["replace"] != nil {
		replace = kwargs["replace"].(string)
	} else {
		mangleMode = true
	}
	reason := kwargs["reason"].(string)

	var scope *string
	if v := kwargs["scope"]; v != nil {
		s := v.(string)
		scope = &s
		// Validate the glob pattern at parse time.
		if _, err := path.Match(s, ""); err != nil {
			die(flags, cmd, 2, fmt.Sprintf("invalid --scope glob: %v", err))
		}
	}

	var from *string
	if v := kwargs["from"]; v != nil {
		s := v.(string)
		from = &s
	}
	entireHistory := kwargs["entire_history"].(bool)

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
	lk, err := lock.Acquire(sharedDir, sgDir, "safegit/rewrite", "scrub-match", timeout)
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

	// Compile regex
	compiledPattern, err := regexp.Compile(pattern)
	if err != nil {
		die(flags, cmd, 2, fmt.Sprintf("invalid regex pattern: %v", err))
	}

	// Dry-run mode
	if flags.dryRun {
		return scrubMatchDryRun(ctx, flags, cmd, compiledPattern, scope, gitDir, pattern, fromSHA, entireHistory)
	}

	// Execution mode
	return scrubMatchExecute(ctx, flags, cmd, compiledPattern, replace, mangleMode, reason, fromSHA, entireHistory, scope, gitDir, sgDir)
}

// scrubMatchDryRun scans all objects and non-object files, prints categorized
// output, and returns 0. When scope is non-nil, only blob matches whose path
// matches the glob are shown.
func scrubMatchDryRun(ctx context.Context, flags globalFlags, cmd string, compiledPattern *regexp.Regexp, scope *string, gitDir string, pattern string, fromSHA string, entireHistory bool) int {
	infof(flags, "Scanning all objects...\n")
	scanOpts := scan.ScanOpts{FromSHA: fromSHA, EntireHistory: entireHistory}
	results, err := scan.ScanObjects(ctx, compiledPattern, scanOpts)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("scanning objects: %v", err))
	}

	if err := scan.AddAttribution(ctx, results, scanOpts); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("adding attribution: %v", err))
	}

	nonObjectMatches, err := scan.ScanNonObjects(ctx, compiledPattern, gitDir)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("scanning non-object files: %v", err))
	}

	// Scan submodules for matches.
	type subScanResult struct {
		sub     submodule.SubmoduleInfo
		results *scan.ScanResults
	}
	var subResults []subScanResult

	subs, err := submodule.Enumerate(ctx, gitDir)
	if err != nil {
		// Non-fatal: warn and continue with parent-only results.
		fmt.Fprintf(os.Stderr, "warning: enumerating submodules: %v\n", err)
	} else {
		for _, sub := range subs {
			if !sub.Initialized {
				continue
			}
			subOpts := scan.ScanOpts{GitDir: sub.GitDir, WorkTree: sub.WorkTreePath, SubmodulePath: sub.RelativePath, EntireHistory: true}
			subScan, err := scan.ScanObjects(ctx, compiledPattern, subOpts)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: scanning submodule %s: %v\n", sub.RelativePath, err)
				continue
			}
			if err := scan.AddAttribution(ctx, subScan, subOpts); err != nil {
				fmt.Fprintf(os.Stderr, "warning: adding attribution for submodule %s: %v\n", sub.RelativePath, err)
			}
			if len(subScan.Matches) > 0 {
				subResults = append(subResults, subScanResult{sub: sub, results: subScan})
			}
		}
	}

	// Categorize parent matches, applying scope filter to blobs.
	var blobMatches, commitMatches, tagMatches []scan.Match
	for _, m := range results.Matches {
		switch m.ObjectType {
		case "blob":
			if scope != nil && !matchScope(*scope, m.Path) {
				continue
			}
			blobMatches = append(blobMatches, m)
		case "commit":
			commitMatches = append(commitMatches, m)
		case "tag":
			tagMatches = append(tagMatches, m)
		}
	}

	totalMatches := len(results.Matches) + len(nonObjectMatches)
	for _, sr := range subResults {
		totalMatches += len(sr.results.Matches)
	}

	// Count unique objects
	uniqueObjects := make(map[string]bool)
	for _, m := range results.Matches {
		uniqueObjects[m.SHA] = true
	}
	for _, sr := range subResults {
		for _, m := range sr.results.Matches {
			uniqueObjects[m.SHA] = true
		}
	}

	infof(flags, "Found %d matches in %d objects:\n", totalMatches, len(uniqueObjects)+len(nonObjectMatches))

	// Print parent repo header only if submodules have matches too.
	hasSubMatches := len(subResults) > 0
	if hasSubMatches {
		infof(flags, "\nParent repo:\n")
	}

	if len(blobMatches) > 0 {
		infof(flags, "\nBlobs:\n")
		for _, m := range blobMatches {
			if m.Path != "" && m.CommitSHA != "" {
				infof(flags, "  %s in commit %s (line %d): %s\n", m.Path, shortSHA(m.CommitSHA), m.Line, m.Context)
			} else if m.Path != "" {
				infof(flags, "  %s (unreachable, line %d): %s\n", m.Path, m.Line, m.Context)
			} else if m.Reachable {
				infof(flags, "  blob %s (line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
			} else {
				infof(flags, "  blob %s (unreachable, line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
			}
		}
	}

	if len(commitMatches) > 0 {
		infof(flags, "\nCommit messages:\n")
		for _, m := range commitMatches {
			infof(flags, "  commit %s (line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
		}
	}

	if len(tagMatches) > 0 {
		infof(flags, "\nTag annotations:\n")
		for _, m := range tagMatches {
			infof(flags, "  tag %s (line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
		}
	}

	if len(nonObjectMatches) > 0 {
		infof(flags, "\nNon-object files:\n")
		for _, m := range nonObjectMatches {
			infof(flags, "  %s (line %d): %s\n", m.Path, m.Line, m.Context)
		}
	}

	// Print submodule results grouped by submodule.
	for _, sr := range subResults {
		infof(flags, "\n[%s]:\n", sr.sub.RelativePath)
		var subBlobs, subCommits, subTags []scan.Match
		for _, m := range sr.results.Matches {
			switch m.ObjectType {
			case "blob":
				// Apply scope filtering for submodule blobs: strip submodule
				// prefix from scope before matching.
				if scope != nil {
					subScope := scopeForSubmodule(*scope, sr.sub.RelativePath)
					if subScope == "" || !matchScope(subScope, m.Path) {
						continue
					}
				}
				subBlobs = append(subBlobs, m)
			case "commit":
				subCommits = append(subCommits, m)
			case "tag":
				subTags = append(subTags, m)
			}
		}
		if len(subBlobs) > 0 {
			infof(flags, "  Blobs:\n")
			for _, m := range subBlobs {
				if m.Path != "" && m.CommitSHA != "" {
					infof(flags, "    %s in commit %s (line %d): %s\n", m.Path, shortSHA(m.CommitSHA), m.Line, m.Context)
				} else if m.Path != "" {
					infof(flags, "    %s (unreachable, line %d): %s\n", m.Path, m.Line, m.Context)
				} else {
					infof(flags, "    blob %s (line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
				}
			}
		}
		if len(subCommits) > 0 {
			infof(flags, "  Commit messages:\n")
			for _, m := range subCommits {
				infof(flags, "    commit %s (line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
			}
		}
		if len(subTags) > 0 {
			infof(flags, "  Tag annotations:\n")
			for _, m := range subTags {
				infof(flags, "    tag %s (line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
			}
		}
	}

	if flags.json {
		// Estimate commit count for the rewrite range.
		estimatedCommits := 0
		if entireHistory {
			out, _, err := git.Run(ctx, "rev-list", "--count", "HEAD")
			if err == nil {
				fmt.Sscanf(strings.TrimSpace(out), "%d", &estimatedCommits)
			}
		} else if fromSHA != "" {
			out, _, err := git.Run(ctx, "rev-list", "--count", fromSHA+"..HEAD")
			if err == nil {
				count := 0
				fmt.Sscanf(strings.TrimSpace(out), "%d", &count)
				estimatedCommits = count + 1 // inclusive of fromSHA
			}
		}
		scopeStr := "entire_history"
		if fromSHA != "" {
			scopeStr = "range"
		}
		scopeFilter := ""
		if scope != nil {
			scopeFilter = *scope
		}
		result := ScrubMatchDryRunResult{
			Version:          1,
			DryRun:           true,
			Pattern:          pattern,
			Scope:            scopeStr,
			ScopeFilter:      scopeFilter,
			ObjectsScanned:   results.Scanned,
			BinarySkipped:    results.Skipped,
			TotalMatches:     totalMatches,
			BlobMatches:      len(blobMatches),
			CommitMatches:    len(commitMatches),
			TagMatches:       len(tagMatches),
			FileMatches:      len(nonObjectMatches),
			EstimatedCommits: estimatedCommits,
		}
		emitJSON(result)
		return 0
	}

	infof(flags, "\nSummary: %d blob matches, %d message matches, %d tag matches, %d file matches\n",
		len(blobMatches), len(commitMatches), len(tagMatches), len(nonObjectMatches))
	if results.Skipped > 0 {
		infof(flags, "Binary blobs skipped: %d\n", results.Skipped)
	}

	return 0
}

// scopeForSubmodule checks if a scope pattern applies to a submodule. If the
// scope path starts with the submodule's relative path, the prefix is stripped
// and the remaining path is returned as the scope within the submodule. If the
// scope doesn't match the submodule's prefix, returns empty string (meaning
// this scope doesn't apply to this submodule). A bare glob like "*.env" applies
// to all repos (parent and submodules).
func scopeForSubmodule(scope, subRelPath string) string {
	// Bare globs (no path separator) apply everywhere.
	if !strings.Contains(scope, "/") {
		return scope
	}
	// Check if scope starts with the submodule path prefix.
	prefix := subRelPath + "/"
	if strings.HasPrefix(scope, prefix) {
		return scope[len(prefix):]
	}
	// Also check using filepath separator in case of OS-specific paths.
	fpPrefix := filepath.ToSlash(subRelPath) + "/"
	if strings.HasPrefix(scope, fpPrefix) {
		return scope[len(fpPrefix):]
	}
	return ""
}

// submoduleScrubResult holds the computed rewrite state for a single submodule
// before any refs are updated. All computation is deferred so that ref updates
// can be applied atomically after all submodules + parent succeed.
type submoduleScrubResult struct {
	sub              submodule.SubmoduleInfo
	shaMap           map[string]string
	rewrittenCount   int
	blobMap          map[string]string
	messagesModified int
}

// scrubMatchExecute performs the actual rewrite: handles submodule processing,
// then delegates parent repo rewriting to executeScrubRecipe. When scope is
// non-nil, only blobs that appear at paths matching the glob are included.
func scrubMatchExecute(
	ctx context.Context,
	flags globalFlags,
	cmd string,
	compiledPattern *regexp.Regexp,
	replace string,
	mangleMode bool,
	reason string,
	fromSHA string,
	entireHistory bool,
	scope *string,
	gitDir string,
	sgDir string,
) int {
	pattern := compiledPattern.String()

	// Scan to find all matches (always uses EntireHistory to find ALL matching
	// blobs, even those introduced before --from).
	infof(flags, "Scanning objects...\n")
	parentOpts := scan.ScanOpts{EntireHistory: true}
	results, err := scan.ScanObjects(ctx, compiledPattern, parentOpts)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("scanning objects: %v", err))
	}

	// Enumerate submodules (4.4)
	subs, subErr := submodule.Enumerate(ctx, gitDir)
	if subErr != nil {
		fmt.Fprintf(os.Stderr, "warning: enumerating submodules: %v\n", subErr)
		subs = nil
	}
	// Ensure safegit dir exists for each initialized submodule.
	for _, sub := range subs {
		if sub.Initialized {
			if err := repo.EnsureInitialized(sub.GitDir); err != nil {
				fmt.Fprintf(os.Stderr, "warning: initializing safegit for submodule %s: %v\n", sub.RelativePath, err)
			}
		}
	}

	if len(results.Matches) == 0 {
		// Check submodules too before declaring no matches.
		anySubMatches := false
		for _, sub := range subs {
			if !sub.Initialized {
				continue
			}
			subCheckOpts := scan.ScanOpts{GitDir: sub.GitDir, WorkTree: sub.WorkTreePath, SubmodulePath: sub.RelativePath, EntireHistory: true}
			subResults, err := scan.ScanObjects(ctx, compiledPattern, subCheckOpts)
			if err != nil {
				continue
			}
			if len(subResults.Matches) > 0 {
				anySubMatches = true
				break
			}
		}
		if !anySubMatches {
			infof(flags, "No matches found. Nothing to rewrite.\n")
			return 0
		}
	}

	// When --scope is set, build a blob SHA -> paths index so we can filter
	// blobs by the paths they appear at.
	var scopedBlobSHAs map[string]bool
	if scope != nil {
		scopedBlobSHAs, err = buildScopedBlobSet(ctx, *scope)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("building scoped blob set: %v", err))
		}
	}

	// Count matches by type for summary
	var blobMatchCount, commitMatchCount, tagMatchCount int
	for _, m := range results.Matches {
		switch m.ObjectType {
		case "blob":
			if scope != nil && !scopedBlobSHAs[m.SHA] {
				continue
			}
			blobMatchCount++
		case "commit":
			commitMatchCount++
		case "tag":
			tagMatchCount++
		}
	}

	// Scan submodules for matches (4.5)
	type subScanInfo struct {
		sub         submodule.SubmoduleInfo
		uniqueBlobs map[string]bool
		blobCount   int
		commitCount int
		tagCount    int
	}
	var subScans []subScanInfo
	for _, sub := range subs {
		if !sub.Initialized {
			continue
		}
		// Determine if this submodule is in scope.
		if scope != nil {
			subScope := scopeForSubmodule(*scope, sub.RelativePath)
			if subScope == "" {
				continue
			}
		}
		subScanOpts := scan.ScanOpts{GitDir: sub.GitDir, WorkTree: sub.WorkTreePath, SubmodulePath: sub.RelativePath, EntireHistory: true}
		subResults, err := scan.ScanObjects(ctx, compiledPattern, subScanOpts)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("scanning submodule %s: %v", sub.RelativePath, err))
		}
		if len(subResults.Matches) == 0 {
			continue
		}

		// Build scoped blob set for submodule if scope applies.
		var subScopedBlobs map[string]bool
		if scope != nil {
			subScope := scopeForSubmodule(*scope, sub.RelativePath)
			if subScope != "" {
				subScopedBlobs, err = buildScopedBlobSetWithDir(ctx, subScope, sub.GitDir, sub.WorkTreePath)
				if err != nil {
					die(flags, cmd, 1, fmt.Sprintf("building scoped blob set for submodule %s: %v", sub.RelativePath, err))
				}
			}
		}

		info := subScanInfo{sub: sub, uniqueBlobs: make(map[string]bool)}
		for _, m := range subResults.Matches {
			switch m.ObjectType {
			case "blob":
				if subScopedBlobs != nil && !subScopedBlobs[m.SHA] {
					continue
				}
				info.blobCount++
				info.uniqueBlobs[m.SHA] = true
			case "commit":
				info.commitCount++
			case "tag":
				info.tagCount++
			}
		}
		if info.blobCount > 0 || info.commitCount > 0 || info.tagCount > 0 {
			subScans = append(subScans, info)
		}
	}

	// If scope filtered out all blob matches and there are no message/tag matches,
	// treat as no matches.
	totalSubBlobCount := 0
	totalSubCommitCount := 0
	totalSubTagCount := 0
	for _, si := range subScans {
		totalSubBlobCount += si.blobCount
		totalSubCommitCount += si.commitCount
		totalSubTagCount += si.tagCount
	}
	if blobMatchCount == 0 && commitMatchCount == 0 && tagMatchCount == 0 &&
		totalSubBlobCount == 0 && totalSubCommitCount == 0 && totalSubTagCount == 0 {
		infof(flags, "No matches found within scope. Nothing to rewrite.\n")
		return 0
	}

	totalBlobs := blobMatchCount + totalSubBlobCount
	totalCommits := commitMatchCount + totalSubCommitCount
	totalTags := tagMatchCount + totalSubTagCount
	infof(flags, "Found %d matches (%d in blobs, %d in commit messages, %d in tag annotations)\n",
		totalBlobs+totalCommits+totalTags, totalBlobs, totalCommits, totalTags)
	if len(subScans) > 0 {
		infof(flags, "  Submodules with matches: %d\n", len(subScans))
	}

	// Confirmation prompt (skipped with --yes)
	if !confirmOrAbort(flags, "This will rewrite history to replace pattern matches. This cannot be undone. Proceed?") {
		infof(flags, "Aborted.\n")
		return 0
	}

	// Phase 1: Process submodules (blob map, walkAndRewrite, Finalize per submodule).
	replaceBytes := []byte(replace)
	var subScrubResults []submoduleScrubResult
	for _, si := range subScans {
		infof(flags, "Scrubbing submodule [%s]...\n", si.sub.RelativePath)

		// Context-scoped git directory targeting: all git commands using
		// subCtx will target the submodule's repo without os.Chdir.
		subCtx := git.WithDir(ctx, si.sub.GitDir, si.sub.WorkTreePath)

		// Build blob replacement map for this submodule.
		subBlobMap := make(map[string]string, len(si.uniqueBlobs))
		for blobSHA := range si.uniqueBlobs {
			content, err := git.CatFileBlob(subCtx, blobSHA)
			if err != nil {
				die(flags, cmd, 1, fmt.Sprintf("submodule %s: reading blob %s: %v", si.sub.RelativePath, blobSHA, err))
			}
			if isBinaryContent(content) {
				continue
			}
			var modified []byte
			if mangleMode {
				modified = compiledPattern.ReplaceAllFunc(content, mangleBytes)
			} else {
				modified = compiledPattern.ReplaceAll(content, []byte(replace))
			}
			if bytes.Equal(content, modified) {
				continue
			}
			newSHA, err := git.HashObjectWriteBytes(subCtx, modified)
			if err != nil {
				die(flags, cmd, 1, fmt.Sprintf("submodule %s: writing replaced blob: %v", si.sub.RelativePath, err))
			}
			subBlobMap[blobSHA] = newSHA
			if flags.verbose {
				fmt.Fprintf(os.Stderr, "  [%s] blob %s -> %s\n", si.sub.RelativePath, shortSHA(blobSHA), shortSHA(newSHA))
			}
		}

		// Determine commit range for the submodule.
		var subSHAs []string
		if entireHistory {
			out, _, err := git.Run(subCtx, "rev-list", "--topo-order", "--reverse", "HEAD")
			if err != nil {
				die(flags, cmd, 1, fmt.Sprintf("submodule %s: listing commits: %v", si.sub.RelativePath, err))
			}
			subSHAs = git.SplitNonEmpty(out)
		} else {
			// For submodules with --from, use entire history since we don't know
			// the corresponding commit boundary in the submodule.
			out, _, err := git.Run(subCtx, "rev-list", "--topo-order", "--reverse", "HEAD")
			if err != nil {
				die(flags, cmd, 1, fmt.Sprintf("submodule %s: listing commits: %v", si.sub.RelativePath, err))
			}
			subSHAs = git.SplitNonEmpty(out)
		}

		subMessagesModified := 0
		subTreeCache := make(map[string]string)
		subShaMap, subRewrittenCount, err := walkAndRewrite(subCtx, subSHAs, func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string) (CommitTransform, error) {
			var xform CommitTransform

			newTreeSHA, err := replaceInTreeByBlobMap(ctx, info.Tree, subBlobMap, nil, subTreeCache)
			if err != nil {
				return CommitTransform{}, fmt.Errorf("replacing blobs in tree for commit %s: %w", sha, err)
			}
			if newTreeSHA != info.Tree {
				xform.TreeSHA = newTreeSHA
			}

			if compiledPattern.Match([]byte(info.Message)) {
				var newMessage string
				if mangleMode {
					newMessage = compiledPattern.ReplaceAllStringFunc(info.Message, func(s string) string {
						return string(mangleBytes([]byte(s)))
					})
				} else {
					newMessage = compiledPattern.ReplaceAllString(info.Message, replace)
				}
				if newMessage != info.Message {
					xform.Message = newMessage
					subMessagesModified++
				}
			}

			return xform, nil
		}, flags.verbose)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("submodule %s: walk and rewrite: %v", si.sub.RelativePath, err))
		}

		subScrubResults = append(subScrubResults, submoduleScrubResult{
			sub:              si.sub,
			shaMap:           subShaMap,
			rewrittenCount:   subRewrittenCount,
			blobMap:          subBlobMap,
			messagesModified: subMessagesModified,
		})

		infof(flags, "  [%s] %d commits rewritten, %d blobs replaced\n",
			si.sub.RelativePath, subRewrittenCount, len(subBlobMap))
	}

	// Apply submodule ref updates (Finalize per submodule).
	for _, sr := range subScrubResults {
		infof(flags, "Updating refs for submodule [%s]...\n", sr.sub.RelativePath)

		// Context-scoped git directory for this submodule's ref updates.
		subCtx := git.WithDir(ctx, sr.sub.GitDir, sr.sub.WorkTreePath)

		// Capture old HEAD before refs are updated.
		subOldHead, _ := git.RevParse(subCtx, "HEAD")

		// Annotation rewrite closure for submodule tags.
		subAnnotFunc := func(ctx context.Context, shaMap map[string]string) error {
			_, subTagsRewritten := rewriteTagAnnotations(ctx, flags, cmd, compiledPattern, replaceBytes, mangleMode, shaMap)
			if subTagsRewritten > 0 && flags.verbose {
				fmt.Fprintf(os.Stderr, "  [%s] %d tag annotations rewritten\n", sr.sub.RelativePath, subTagsRewritten)
			}
			return nil
		}

		// Oplog extra for submodule (ref, oldHead, sha, rewritten are
		// added by Finalize).
		subOplogExtra := map[string]interface{}{
			"pattern":          pattern,
			"reason":           reason,
			"blobsReplaced":    len(sr.blobMap),
			"messagesModified": sr.messagesModified,
			"parentScrub":      true,
		}
		if mangleMode {
			subOplogExtra["mode"] = "mangle"
		} else {
			subOplogExtra["replace"] = replace
		}

		// Build policy data for this submodule.
		subPolicy := ScrubPolicy{
			Type:        "match",
			Pattern:     pattern,
			Reason:      reason,
			CreatedByOp: "scrub-match",
		}
		if scope != nil {
			ss := scopeForSubmodule(*scope, sr.sub.RelativePath)
			if ss != "" {
				subPolicy.Scope = ss
			}
		}

		subResult := RewriteResult{
			ShaMap:         sr.shaMap,
			RewrittenCount: sr.rewrittenCount,
			OldHeadSHA:     subOldHead,
			SgDir:          sr.sub.SafegitDir,
			OpName:         "scrub-match",
			OplogExtra:     subOplogExtra,
			PolicyData:     &subPolicy,
		}
		if err := subResult.Finalize(subCtx, flags, cmd, subAnnotFunc, nil); err != nil {
			die(flags, cmd, 1, fmt.Sprintf("submodule %s: %v", sr.sub.RelativePath, err))
		}
	}

	// Build the combined gitlink map from all submodule SHA mappings.
	var gitlinkMap map[string]string
	if len(subScrubResults) > 0 {
		gitlinkMap = make(map[string]string)
		for _, sr := range subScrubResults {
			for old, new_ := range sr.shaMap {
				if old != new_ {
					gitlinkMap[old] = new_
				}
			}
		}
		if len(gitlinkMap) == 0 {
			gitlinkMap = nil
		}
	}

	// Phase 2: Construct a single-operation ParsedRecipe and delegate parent
	// repo rewriting to executeScrubRecipe.
	var recipeReplace *string
	if !mangleMode {
		recipeReplace = &replace
	}
	recipe := &ParsedRecipe{
		Operations: []RecipeOperation{{
			Pattern: pattern,
			Replace: recipeReplace,
			Mangle:  mangleMode,
			Scope:   scope,
		}},
		Patterns:  []*regexp.Regexp{compiledPattern},
		TopoOrder: []int{0},
	}

	// Build oplog extra with scrub-match specific fields.
	parentOplogExtra := map[string]interface{}{
		"pattern":    pattern,
		"reason":     reason,
		"submodules": len(subScrubResults),
	}
	if scope != nil {
		parentOplogExtra["scope"] = *scope
	}
	if mangleMode {
		parentOplogExtra["mode"] = "mangle"
	} else {
		parentOplogExtra["replace"] = replace
	}

	// Skip confirmation in executeScrubRecipe (already confirmed above).
	execFlags := flags
	execFlags.yes = true

	exitCode, result := executeScrubRecipe(ctx, execFlags, cmd, recipe, reason, fromSHA, entireHistory, scope, gitDir, sgDir, gitlinkMap, "scrub-match", parentOplogExtra, true)

	// Post-execution submodule verification.
	if result != nil && len(subScrubResults) > 0 {
		for _, sr := range subScrubResults {
			subScope := scope
			if scope != nil {
				ss := scopeForSubmodule(*scope, sr.sub.RelativePath)
				if ss != "" {
					subScope = &ss
				} else {
					subScope = nil
				}
			}
			subVerifyOpts := scan.ScanOpts{GitDir: sr.sub.GitDir, WorkTree: sr.sub.WorkTreePath, SubmodulePath: sr.sub.RelativePath, EntireHistory: true}
			subResults, scanErr := scan.ScanObjects(ctx, compiledPattern, subVerifyOpts)
			if scanErr != nil {
				fmt.Fprintf(os.Stderr, "CRITICAL: re-scan submodule %s failed: %v\n", sr.sub.RelativePath, scanErr)
				exitCode = 1
				continue
			}
			if len(subResults.Matches) > 0 {
				var remaining []scan.Match
				for _, m := range subResults.Matches {
					if !m.Reachable {
						continue
					}
					remaining = append(remaining, m)
				}
				if subScope != nil && len(remaining) > 0 {
					if err := scan.AddAttribution(ctx, subResults, subVerifyOpts); err == nil {
						var filtered []scan.Match
						for _, m := range remaining {
							if m.ObjectType == "blob" && !matchScope(*subScope, m.Path) {
								continue
							}
							filtered = append(filtered, m)
						}
						remaining = filtered
					}
				}
				if len(remaining) > 0 {
					fmt.Fprintf(os.Stderr, "CRITICAL: secret still present in submodule %s (%d matches)\n",
						sr.sub.RelativePath, len(remaining))
					exitCode = 1
				}
			}
		}

		// Verify gitlinks in parent's rewritten history resolve to valid commits.
		if err := verifyGitlinksAfterScrub(ctx, result.ShaMap, subScrubResults); err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: gitlink verification: %v\n", err)
		}
	}

	// No rewrite was performed (e.g., no parent matches but submodules had them).
	if result == nil {
		return exitCode
	}

	// Combine ref-level tag rewrites with annotation tag rewrites.
	allTagRewrites := append(result.TagRewrites, result.AnnotationTagRewrites...)

	// JSON output
	if flags.json {
		rewrites := make(map[string]string)
		for old, new_ := range result.ShaMap {
			if old != new_ {
				rewrites[old] = new_
			}
		}
		combinedTagRewrites := make([]TagRewrite, 0, len(allTagRewrites))
		combinedTagRewrites = append(combinedTagRewrites, allTagRewrites...)
		jsonResult := ScrubMatchResult{
			Version:          1,
			DryRun:           false,
			Rewrites:         rewrites,
			Tags:             combinedTagRewrites,
			CommitsRewritten: result.RewrittenCount,
			BlobsReplaced:    result.BlobsReplaced,
			MessagesModified: result.MessagesModified,
			TagsRewritten:    result.TagsRewrittenCount,
			OldHead:          result.OldHeadSHA,
			NewHead:          result.NewHeadSHA,
		}
		if jsonResult.Tags == nil {
			jsonResult.Tags = []TagRewrite{}
		}
		emitJSON(jsonResult)
		return exitCode
	}

	// Summary (push hint already printed by Finalize inside executeScrubRecipe)
	infof(flags, "\nScrub complete:\n")
	infof(flags, "  %d commits rewritten\n", result.RewrittenCount)
	infof(flags, "  %d blobs replaced\n", result.BlobsReplaced)
	infof(flags, "  %d commit messages modified\n", result.MessagesModified)
	infof(flags, "  %d tag annotations rewritten\n", result.TagsRewrittenCount)
	if len(subScrubResults) > 0 {
		totalSubRewritten := 0
		totalSubBlobs := 0
		for _, sr := range subScrubResults {
			totalSubRewritten += sr.rewrittenCount
			totalSubBlobs += len(sr.blobMap)
		}
		infof(flags, "  %d submodule commits rewritten\n", totalSubRewritten)
		infof(flags, "  %d submodule blobs replaced\n", totalSubBlobs)
	}
	infof(flags, "  Old HEAD: %s\n", result.OldHeadSHA[:12])
	infof(flags, "  New HEAD: %s\n", result.NewHeadSHA[:12])

	return exitCode
}

// verifyGitlinksAfterScrub checks that every gitlink in the parent's rewritten
// history resolves to a valid commit in the corresponding submodule.
func verifyGitlinksAfterScrub(ctx context.Context, parentShaMap map[string]string, subResults []submoduleScrubResult) error {
	// Build a set of all valid new submodule commit SHAs.
	validSubCommits := make(map[string]bool)
	for _, sr := range subResults {
		for _, newSHA := range sr.shaMap {
			validSubCommits[newSHA] = true
		}
	}

	// Check HEAD's tree for gitlinks.
	headSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("resolving HEAD: %w", err)
	}
	headInfo, err := git.ParseCommit(ctx, headSHA)
	if err != nil {
		return fmt.Errorf("parsing HEAD commit: %w", err)
	}
	entries, err := git.LsTree(ctx, headInfo.Tree)
	if err != nil {
		return fmt.Errorf("ls-tree HEAD: %w", err)
	}

	var failures []string
	for _, e := range entries {
		if e.ObjectType == "commit" {
			// This is a gitlink. Check that it resolves.
			if !validSubCommits[e.SHA] {
				// It might be an unmodified submodule commit (not in the rewrite set).
				// Check that the SHA exists in one of the submodule git dirs.
				found := false
				for _, sr := range subResults {
					if sr.sub.RelativePath == e.Path {
						// Check if this SHA is valid in the submodule.
						_, _, catErr := git.RunWithGitDir(ctx, sr.sub.GitDir, sr.sub.WorkTreePath, "cat-file", "-t", e.SHA)
						if catErr == nil {
							found = true
						}
						break
					}
				}
				if !found {
					failures = append(failures, fmt.Sprintf("gitlink %s at %s does not resolve to a valid commit", shortSHA(e.SHA), e.Path))
				}
			}
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("%d gitlink(s) invalid:\n  %s", len(failures), strings.Join(failures, "\n  "))
	}
	return nil
}

// buildScopedBlobSetWithDir enumerates all blob-to-path mappings across all
// refs in a specific git directory and returns the set of blob SHAs that appear
// at paths matching the scope glob.
func buildScopedBlobSetWithDir(ctx context.Context, scope, gitDir, workTree string) (map[string]bool, error) {
	stdout, _, err := git.RunWithGitDir(ctx, gitDir, workTree, "rev-list", "--all", "--objects")
	if err != nil {
		return nil, fmt.Errorf("rev-list --all --objects: %w", err)
	}

	result := make(map[string]bool)
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
		if matchScope(scope, filePath) {
			result[sha] = true
		}
	}
	return result, nil
}

// rewriteTagAnnotations does a second pass over annotated tags after updateRefs,
// checking each tag's annotation body for the pattern and rewriting if matched.
// Returns the count of tags whose annotations were rewritten.
func rewriteTagAnnotations(ctx context.Context, flags globalFlags, cmd string, compiledPattern *regexp.Regexp, replaceBytes []byte, mangleMode bool, shaMap map[string]string) ([]TagRewrite, int) {
	tagRewrites, tagsRewritten, err := forEachAnnotatedTag(ctx, shaMap, func(refname, header, body string) (string, error) {
		if !compiledPattern.MatchString(body) {
			return body, nil
		}
		var newBody string
		if mangleMode {
			newBody = compiledPattern.ReplaceAllStringFunc(body, func(s string) string {
				return string(mangleBytes([]byte(s)))
			})
		} else {
			newBody = compiledPattern.ReplaceAllString(body, string(replaceBytes))
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

// shortSHA returns the first 8 characters of a SHA.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// isBinaryContent checks if content is binary (NUL byte in first 8KB).
func isBinaryContent(content []byte) bool {
	limit := len(content)
	if limit > 8192 {
		limit = 8192
	}
	return bytes.ContainsRune(content[:limit], 0)
}

// verifySecretRemovedScoped re-scans git objects for the given pattern after a
// scrub rewrite. When scope is non-nil, only blob matches at paths matching the
// scope glob are considered failures; out-of-scope blobs are expected to still
// contain the pattern. Non-blob matches (commit messages, tags) are always
// checked regardless of scope.
func verifySecretRemovedScoped(ctx context.Context, pattern *regexp.Regexp, scope *string) error {
	if scope == nil {
		return verifySecretRemoved(ctx, pattern)
	}

	verifyOpts := scan.ScanOpts{EntireHistory: true}
	results, err := scan.ScanObjects(ctx, pattern, verifyOpts)
	if err != nil {
		return fmt.Errorf("re-scan failed: %w", err)
	}
	if len(results.Matches) == 0 {
		return nil
	}

	// Exclude matches from the tracked policy file, which legitimately
	// contains pattern strings.
	policyBlobs, _ := buildScopedBlobSet(ctx, ".safegit/scrub-policies.jsonl")
	if len(policyBlobs) > 0 {
		var filtered []scan.Match
		for _, m := range results.Matches {
			if m.ObjectType == "blob" && policyBlobs[m.SHA] {
				continue
			}
			filtered = append(filtered, m)
		}
		results.Matches = filtered
		if len(results.Matches) == 0 {
			return nil
		}
	}

	// Add attribution so blob matches have paths.
	if err := scan.AddAttribution(ctx, results, verifyOpts); err != nil {
		return fmt.Errorf("adding attribution for verification: %w", err)
	}

	// Also build the scoped blob set to check blobs by SHA (some blobs may
	// appear at multiple paths, and attribution only records one).
	scopedBlobs, err := buildScopedBlobSet(ctx, *scope)
	if err != nil {
		return fmt.Errorf("building scoped blob set for verification: %w", err)
	}

	var failures []scan.Match
	for _, m := range results.Matches {
		switch m.ObjectType {
		case "blob":
			// Only report blobs that are in scope (by path match or by SHA).
			if matchScope(*scope, m.Path) || scopedBlobs[m.SHA] {
				failures = append(failures, m)
			}
		default:
			// Commit messages and tags are always checked.
			failures = append(failures, m)
		}
	}

	if len(failures) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("secret still present in %d object(s):\n", len(failures)))
	for _, m := range failures {
		reachable := "unreachable"
		if m.Reachable {
			reachable = "reachable"
		}
		sb.WriteString(fmt.Sprintf("  %s %s (%s, line %d): %s\n",
			m.ObjectType, shortSHA(m.SHA), reachable, m.Line, m.Context))
	}
	return fmt.Errorf("%s", sb.String())
}

// matchScope checks whether a file path matches a glob scope pattern.
// It tries path.Match against both the full path and the basename, so
// patterns like "*.env" match "config/secret.env" as well as "secret.env".
func matchScope(scope, filePath string) bool {
	if filePath == "" {
		return false
	}
	// Try matching against the full path first (supports patterns like "config/**").
	if matched, _ := path.Match(scope, filePath); matched {
		return true
	}
	// Try matching against just the filename (supports patterns like "*.env").
	base := path.Base(filePath)
	if matched, _ := path.Match(scope, base); matched {
		return true
	}
	return false
}

// buildScopedBlobSet enumerates all blob-to-path mappings across all refs
// using `git rev-list --all --objects` and returns the set of blob SHAs that
// appear at paths matching the scope glob.
func buildScopedBlobSet(ctx context.Context, scope string) (map[string]bool, error) {
	stdout, _, err := git.Run(ctx, "rev-list", "--all", "--objects")
	if err != nil {
		return nil, fmt.Errorf("rev-list --all --objects: %w", err)
	}

	result := make(map[string]bool)
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		spaceIdx := strings.IndexByte(line, ' ')
		if spaceIdx < 0 {
			// No space: this is a commit SHA, skip.
			continue
		}
		sha := line[:spaceIdx]
		filePath := line[spaceIdx+1:]
		if matchScope(scope, filePath) {
			result[sha] = true
		}
	}
	return result, nil
}

// mangleBytes replaces each non-whitespace byte in match with a random
// printable ASCII character (0x21-0x7E). Whitespace characters (space, tab,
// newline, carriage return) are preserved to maintain formatting structure.
// Uses crypto/rand for security.
func mangleBytes(match []byte) []byte {
	const printable = "!\"#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~"
	result := make([]byte, len(match))
	for i, b := range match {
		switch b {
		case ' ', '\t', '\n', '\r':
			result[i] = b
		default:
			idx := make([]byte, 1)
			crypto_rand.Read(idx)
			result[i] = printable[int(idx[0])%len(printable)]
		}
	}
	return result
}

