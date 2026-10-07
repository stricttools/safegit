package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/stricttools/safegit/internal/exitcode"
	"github.com/stricttools/safegit/internal/git"
	"github.com/stricttools/safegit/internal/repo"
	"github.com/stricttools/strictcli/go/strictcli"
)

// `scrub squash` folds one first-parent range of HEAD's history into a single
// commit. It is the history rewrite a repository's declassification runs: the
// commits of a confidential period become one commit whose message names
// nothing of what they did, while the history before and after the period is
// kept, rewritten only onto the new parent.
//
// The squash commit carries the tree of the range's last commit, the parents
// of its first, the author and committer of its last, and the --message given.
// Every commit after the range is rewritten onto it with its own tree,
// message, and identity unchanged. Refs pointing at a folded commit move to
// the squash commit, and the rewrite journal records every folded commit
// against it, so the release tooling can follow each one.

// ScrubSquashResult is what `scrub squash` reports, in both modes and in both
// renderings. The execute-only members are pointers or omitempty, so a preview
// omits them rather than publishing a zero that reads as a fact.
type ScrubSquashResult struct {
	Version int  `json:"version"`
	DryRun  bool `json:"dry_run"`
	// First and Last are the resolved ends of the range, both folded.
	First string `json:"first"`
	Last  string `json:"last"`
	// Squashed is every folded commit, oldest first.
	Squashed []string `json:"squashed"`
	Message  string   `json:"message"`
	OldHead  string   `json:"old_head"`

	// Execute-only.
	SquashCommit      string            `json:"squash_commit,omitempty"`
	Rewrites          map[string]string `json:"rewrites,omitempty"`
	Tags              []TagRewrite      `json:"tags,omitempty"`
	CommitsRewritten  *int              `json:"commits_rewritten,omitempty"`
	NewHead           string            `json:"new_head,omitempty"`
	PreRewriteRemotes map[string]string `json:"pre_rewrite_remotes,omitempty"`
	CleanupOK         *bool             `json:"cleanup_ok,omitempty"`
	CleanupErrors     []string          `json:"cleanup_errors,omitempty"`
	SyncSkipped       bool              `json:"sync_skipped,omitempty"`
}

// scrubSquashPayloadSchema declares what `scrub squash` puts in the envelope's
// payload.
var scrubSquashPayloadSchema = strictcli.SchemaObject(
	map[string]interface{}{
		"version":             strictcli.SchemaType("integer"),
		"dry_run":             strictcli.SchemaType("boolean"),
		"first":               strictcli.SchemaType("string"),
		"last":                strictcli.SchemaType("string"),
		"squashed":            strictcli.SchemaArray(strictcli.SchemaType("string")),
		"message":             strictcli.SchemaType("string"),
		"old_head":            strictcli.SchemaType("string"),
		"squash_commit":       strictcli.SchemaType("string"),
		"rewrites":            scrubRewritesSchema,
		"tags":                scrubTagsSchema,
		"commits_rewritten":   strictcli.SchemaType("integer"),
		"new_head":            strictcli.SchemaType("string"),
		"pre_rewrite_remotes": scrubRewritesSchema,
		"cleanup_ok":          strictcli.SchemaType("boolean"),
		"cleanup_errors":      strictcli.SchemaArray(strictcli.SchemaType("string")),
		"sync_skipped":        strictcli.SchemaType("boolean"),
	},
	[]string{"version", "dry_run", "first", "last", "squashed", "message", "old_head"},
	false,
)

// squashPlan is a validated squash: the folded commits and the commits the
// rewrite walks.
type squashPlan struct {
	// members are the folded commits, oldest first.
	members []string
	// walk is every commit the rewrite visits, parents before children: the
	// range and everything HEAD reaches after it.
	walk []string
}

// planSquash resolves --first and --last and checks the range they bound: both
// on HEAD's first-parent history, --first no newer than --last, and no merge
// commit inside. Every failure is a refusal naming the commit at fault.
func planSquash(ctx context.Context, firstArg, lastArg string) (squashPlan, error) {
	first, err := git.RevParse(ctx, firstArg+"^{commit}")
	if err != nil {
		return squashPlan{}, fmt.Errorf("resolving --first %q to a commit: %v", firstArg, err)
	}
	last, err := git.RevParse(ctx, lastArg+"^{commit}")
	if err != nil {
		return squashPlan{}, fmt.Errorf("resolving --last %q to a commit: %v", lastArg, err)
	}

	out, _, err := git.Run(ctx, "rev-list", "--first-parent", "HEAD")
	if err != nil {
		return squashPlan{}, fmt.Errorf("listing HEAD's first-parent history: %v", err)
	}
	chain := git.SplitNonEmpty(out) // newest first
	position := func(sha string) int {
		for i, c := range chain {
			if c == sha {
				return i
			}
		}
		return -1
	}
	iFirst, iLast := position(first), position(last)
	switch {
	case iFirst < 0:
		return squashPlan{}, fmt.Errorf("--first %s is not on HEAD's first-parent history; a squash folds a range of that history only", shortSHA(first))
	case iLast < 0:
		return squashPlan{}, fmt.Errorf("--last %s is not on HEAD's first-parent history; a squash folds a range of that history only", shortSHA(last))
	case iFirst < iLast:
		return squashPlan{}, fmt.Errorf("--first %s is newer than --last %s on HEAD's first-parent history; --first names the oldest commit of the range",
			shortSHA(first), shortSHA(last))
	}

	members := make([]string, 0, iFirst-iLast+1)
	for i := iFirst; i >= iLast; i-- {
		members = append(members, chain[i])
	}
	var firstParents []string
	for i, m := range members {
		info, err := git.ParseCommit(ctx, m)
		if err != nil {
			return squashPlan{}, fmt.Errorf("reading commit %s: %v", shortSHA(m), err)
		}
		if len(info.Parents) > 1 {
			return squashPlan{}, fmt.Errorf("commit %s inside the range is a merge commit; a squash folds a range without merges, "+
				"because folding a merge would drop the history it merged in. Choose a range that ends before it or starts after it", shortSHA(m))
		}
		if i == 0 {
			firstParents = info.Parents
		}
	}

	walkRange := "HEAD"
	if len(firstParents) == 1 {
		walkRange = firstParents[0] + "..HEAD"
	}
	out, _, err = git.Run(ctx, "rev-list", "--topo-order", "--reverse", walkRange)
	if err != nil {
		return squashPlan{}, fmt.Errorf("listing the commits to rewrite: %v", err)
	}
	return squashPlan{members: members, walk: git.SplitNonEmpty(out)}, nil
}

// squashWalk writes the squash commit and rewrites every later commit onto
// it. It returns the old-to-new map (identity entries included, every folded
// commit mapped to the squash commit), the number of commit objects written,
// and the squash commit.
//
// The squash commit is written the moment the walk reaches the range's first
// commit, and every folded commit is mapped to it then, so a commit that
// branched off from inside the range is rewritten onto the squash commit like
// every other descendant.
func squashWalk(flags globalFlags, ctx context.Context, plan squashPlan, message string) (map[string]string, int, string, error) {
	members := plan.members
	folded := make(map[string]bool, len(members))
	for _, m := range members {
		folded[m] = true
	}
	first, last := members[0], members[len(members)-1]

	shaMap := make(map[string]string, len(plan.walk))
	written := 0
	squash := ""
	remap := func(parents []string) ([]string, bool) {
		out := make([]string, len(parents))
		moved := false
		for i, p := range parents {
			out[i] = p
			if mapped, ok := shaMap[p]; ok && mapped != p {
				out[i] = mapped
				moved = true
			}
		}
		return out, moved
	}

	for _, sha := range plan.walk {
		if folded[sha] {
			if sha != first {
				continue
			}
			firstInfo, err := git.ParseCommit(ctx, first)
			if err != nil {
				return nil, 0, "", fmt.Errorf("parsing commit %s: %w", first, err)
			}
			lastInfo, err := git.ParseCommit(ctx, last)
			if err != nil {
				return nil, 0, "", fmt.Errorf("parsing commit %s: %w", last, err)
			}
			parents, _ := remap(firstInfo.Parents)
			squash, err = git.CommitTree(ctx, lastInfo.Tree, parents, message,
				&git.CommitIdentity{Author: lastInfo.Author, Committer: lastInfo.Committer})
			if err != nil {
				return nil, 0, "", fmt.Errorf("writing the squash commit: %w", err)
			}
			written++
			for _, m := range members {
				shaMap[m] = squash
			}
			if flags.verbose {
				debugf(flags, "  %s..%s -> %s  (squashed, %d commits)", shortSHA(first), shortSHA(last), shortSHA(squash), len(members))
			}
			continue
		}

		info, err := git.ParseCommit(ctx, sha)
		if err != nil {
			return nil, 0, "", fmt.Errorf("parsing commit %s: %w", sha, err)
		}
		parents, moved := remap(info.Parents)
		if !moved {
			shaMap[sha] = sha
			continue
		}
		newSHA, err := git.CommitTree(ctx, info.Tree, parents, info.Message,
			&git.CommitIdentity{Author: info.Author, Committer: info.Committer})
		if err != nil {
			return nil, 0, "", fmt.Errorf("creating rewritten commit for %s: %w", sha, err)
		}
		shaMap[sha] = newSHA
		written++
		if flags.verbose {
			debugf(flags, "  %s -> %s  (inherited)", shortSHA(sha), shortSHA(newSHA))
		}
	}

	if squash == "" {
		return nil, 0, "", fmt.Errorf("the walk never reached the range's first commit %s; this is a safegit bug", shortSHA(first))
	}
	return shaMap, written, squash, nil
}

func runScrubSquash(flags globalFlags, kwargs map[string]interface{}) int {
	const cmd = "scrub squash"

	reason := kwargs["reason"].(string)
	firstArg := kwargs["first"].(string)
	lastArg := kwargs["last"].(string)
	// git commit-tree -m ends the message with one newline of its own.
	message := strings.TrimRight(kwargs["message"].(string), "\n")
	if strings.TrimSpace(message) == "" {
		strictcli.ExitNow(exitcode.Usage, "--message is empty; the squash commit carries it as its whole message")
	}

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		strictcli.ExitNow(exitcode.NotInitialized, exitMessage(err))
	}
	ctx := flags.ctx()
	sgDir := repo.SafegitDir(gitDir)

	oldHeadSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		strictcli.ExitNow(exitcode.General, fmt.Sprintf("resolving HEAD: %v", err))
	}
	plan, err := planSquash(ctx, firstArg, lastArg)
	if err != nil {
		strictcli.ExitNow(exitcode.General, exitMessage(err))
	}

	result := ScrubSquashResult{
		Version:  1,
		DryRun:   flags.dryRun,
		First:    plan.members[0],
		Last:     plan.members[len(plan.members)-1],
		Squashed: plan.members,
		Message:  message,
		OldHead:  oldHeadSHA,
	}

	infof(flags, "Squash summary:")
	infof(flags, "  Range:   %s..%s (inclusive, %d commits)", shortSHA(result.First), shortSHA(result.Last), len(plan.members))
	infof(flags, "  Message: %s", firstLine(message))
	infof(flags, "  Reason:  %s", reason)

	if flags.dryRun {
		recordHistoryRewrite(ctx, flags, oldHeadSHA)
		flags.payload(result)
		infof(flags, "Dry run: no changes made.")
		return exitcode.OK
	}

	requireCleanTree(ctx)
	lk := acquireRewriteLock(ctx, flags, gitDir, sgDir, "scrub-squash")
	defer lk.Release()

	// The range was decided against the HEAD read above; a HEAD that moved
	// before the lock was taken is a different history.
	if head, err := git.RevParse(ctx, "HEAD"); err != nil || head != oldHeadSHA {
		strictcli.ExitNow(exitcode.General, fmt.Sprintf("HEAD moved from %s while the squash was being planned; nothing was changed, so run the command again", shortSHA(oldHeadSHA)))
	}

	infof(flags, "Folding %d commits into one. This cannot be undone.", len(plan.members))
	shaMap, written, squash, err := squashWalk(flags, ctx, plan, message)
	if err != nil {
		exitFinalize("", err)
	}

	rewriteResult := RewriteResult{
		ShaMap:         shaMap,
		RewrittenCount: written,
		Intent:         SquashIntent(plan.members, message),
		OldHeadSHA:     oldHeadSHA,
		SgDir:          sgDir,
		Reason:         reason,
		OpName:         "scrub-squash",
		OplogExtra: map[string]interface{}{
			"first":  result.First,
			"last":   result.Last,
			"squash": squash,
			"reason": reason,
		},
	}
	if err := rewriteResult.Finalize(ctx, flags, cmd, RewriteHooks{}); err != nil {
		exitFinalize("", err)
	}

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
	result.SquashCommit = squash
	result.Rewrites = rewrites
	result.Tags = tags
	result.CommitsRewritten = intPtr(written)
	result.NewHead = rewriteResult.NewHeadSHA
	result.PreRewriteRemotes = nonNilStringMap(rewriteResult.PreRewriteRemotes)
	result.CleanupOK = boolPtr(rewriteResult.CleanupOK)
	result.CleanupErrors = nonNilStrings(rewriteResult.CleanupErrors)
	result.SyncSkipped = rewriteResult.SyncSkipped
	flags.payload(result)

	infof(flags, "\nSquash complete:")
	infof(flags, "  %d commits folded into %s", len(plan.members), shortSHA(squash))
	infof(flags, "  %d commit objects written", written)
	infof(flags, "  Old HEAD: %s", shortSHA(result.OldHead))
	infof(flags, "  New HEAD: %s", shortSHA(result.NewHead))
	printScopeNotice(flags, rewriteResult.Ref)

	return rewriteResult.TierBExit(exitcode.OK)
}
