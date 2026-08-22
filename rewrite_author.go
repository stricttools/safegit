package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/trailer"
	"github.com/smm-h/strictcli/go/strictcli"
)

func runRewriteAuthor(flags globalFlags, kwargs map[string]interface{}) int {
	const cmd = "author rewrite"

	// Extract optional string flags (nil when not provided).
	var oldName, newName, oldEmail, newEmail string
	if v := kwargs["old_name"]; v != nil {
		oldName = v.(string)
	}
	if v := kwargs["new_name"]; v != nil {
		newName = v.(string)
	}
	if v := kwargs["old_email"]; v != nil {
		oldEmail = v.(string)
	}
	if v := kwargs["new_email"]; v != nil {
		newEmail = v.(string)
	}
	// The two all-or-none pairs and the at-least-one over them are declared
	// constraints now (contract §26.7), so a command line naming neither pair,
	// or half of one, is refused by the framework before dispatch. The hand
	// guard that printed "at least one of --old-name or --old-email is
	// required" and returned 2 is gone with them.

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		die(exitcode.NotInitialized, err.Error())
	}

	sgDir := repo.SafegitDir(gitDir)
	ctx := flags.ctx()

	// The clean-tree requirement belongs to the execute path only, so it is
	// checked after the dry-run branch below. A preview is exactly what a dirty
	// working tree is for.

	// Dry-run mode: purely read-only, so it returns before the config load and
	// before contending for the rewrite lock (same shape as the scrub siblings).
	if flags.dryRun {
		dryArgs := append([]string{"rev-list", "--topo-order", "--reverse"}, refGlobs...)
		out, _, err := git.Run(ctx, dryArgs...)
		if err != nil {
			die(exitcode.General, fmt.Sprintf("listing commits: %v", err))
		}
		shas := git.SplitNonEmpty(out)

		var affected []string
		for _, sha := range shas {
			info, err := git.ParseCommit(ctx, sha)
			if err != nil {
				die(exitcode.General, fmt.Sprintf("parsing commit %s: %v", sha, err))
			}
			nameMatch := oldName != "" && (info.Author.Name == oldName || info.Committer.Name == oldName)
			emailMatch := oldEmail != "" && (info.Author.Email == oldEmail || info.Committer.Email == oldEmail)
			var commitMatches bool
			if oldName != "" && oldEmail != "" {
				commitMatches = nameMatch && emailMatch
			} else {
				commitMatches = nameMatch || emailMatch
			}
			if commitMatches {
				affected = append(affected, sha)
			}
		}

		// The one computation both renderings read: the preview's two counts
		// are the two numbers the line below prints.
		result := RewriteAuthorResult{
			Version:        1,
			DryRun:         true,
			CommitsToCheck: intPtr(len(shas)),
			CommitsMatched: intPtr(len(affected)),
		}
		if oldName != "" {
			result.OldName = oldName
			result.NewName = newName
		}
		if oldEmail != "" {
			result.OldEmail = oldEmail
			result.NewEmail = newEmail
		}
		// The rewrite this preview describes, minted so the framework renders
		// it: the would-do log in human mode, the envelope's preview member in
		// machine mode.
		oldHeadSHA, _ := git.RevParse(ctx, "HEAD")
		recordHistoryRewrite(ctx, flags, oldHeadSHA)
		flags.payload(result)

		if flags.json {
			return 0
		}

		var rewriteDesc string
		switch {
		case oldName != "" && oldEmail != "":
			rewriteDesc = fmt.Sprintf("name: %s -> %s, email: %s -> %s", oldName, newName, oldEmail, newEmail)
		case oldName != "":
			rewriteDesc = fmt.Sprintf("name: %s -> %s", oldName, newName)
		default:
			rewriteDesc = fmt.Sprintf("email: %s -> %s", oldEmail, newEmail)
		}
		fmt.Printf("Would rewrite %d of %d commits (%s)\n",
			*result.CommitsMatched, *result.CommitsToCheck, rewriteDesc)
		limit := len(affected)
		if limit > 5 {
			limit = 5
		}
		for _, sha := range affected[:limit] {
			info, _ := git.ParseCommit(ctx, sha)
			fmt.Printf("  %s  author=%s committer=%s\n", sha[:12], info.Author.Name, info.Committer.Name)
		}
		if len(affected) > 5 {
			fmt.Printf("  ... and %d more\n", len(affected)-5)
		}
		return 0
	}

	// Execute path only: a rewrite of a dirty tree would lose the uncommitted work.
	requireCleanTree(ctx)

	// Acquire rewrite lock to prevent concurrent history rewriting (execute path only)
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("loading config: %v", err))
	}
	timeout := time.Duration(cfg.Lock.AcquireTimeoutSeconds) * time.Second
	sharedDir := repo.SharedSafegitDir(ctx, gitDir)
	lk, err := lock.Acquire(sharedDir, sgDir, lock.RewriteRef, "rewrite-author", timeout)
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

	// The framework's confirm protocol already obtained deliberate consent for
	// this act: `author rewrite` declares itself consequential, so nothing
	// reaches here unconsented. The scale is still worth stating, so it is a
	// notice rather than a second prompt asking what was just answered.
	{
		countArgs := append([]string{"rev-list", "--topo-order", "--reverse"}, refGlobs...)
		out, _, err := git.Run(ctx, countArgs...)
		if err != nil {
			die(exitcode.General, fmt.Sprintf("listing commits: %v", err))
		}
		total := len(git.SplitNonEmpty(out))
		infof(flags, "Rewriting %d commits. This cannot be undone.\n", total)
	}

	// Capture old HEAD before rewrite
	oldHeadSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		die(exitcode.General, fmt.Sprintf("resolving HEAD: %v", err))
	}

	// Actual rewrite
	infof(flags, "Capturing pre-rewrite snapshot...\n")
	before, err := captureSnapshot(ctx)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("capturing pre-rewrite snapshot: %v", err))
	}

	infof(flags, "Rewriting commits...\n")
	intent := IdentityIntent()
	shaMap, nameChanged, err := rewriteCommits(ctx, oldName, newName, oldEmail, newEmail, intent, flags.verbose)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("rewriting commits: %v", err))
	}

	// Summary counts (computed before Finalize for JSON output)
	identity := 0
	for k, v := range shaMap {
		if k == v {
			identity++
		}
	}
	total := len(shaMap)
	parentOnly := total - nameChanged - identity

	// Build RewriteResult
	result := &RewriteResult{
		ShaMap:         shaMap,
		RewrittenCount: total,
		OldHeadSHA:     oldHeadSHA,
		SgDir:          sgDir,
		TaggerOldName:  oldName,
		TaggerNewName:  newName,
		TaggerOldEmail: oldEmail,
		TaggerNewEmail: newEmail,
		OpName:         "rewrite-author",
		OplogExtra: map[string]interface{}{
			"oldName":          oldName,
			"newName":          newName,
			"oldEmail":         oldEmail,
			"newEmail":         newEmail,
			"commitsRewritten": total,
			"nameChanged":      nameChanged,
		},
	}

	// Tier B: captures the pre-rewrite snapshot in the closure, takes the
	// post-rewrite snapshot after cleanup has run (inside Finalize), then
	// compares. It sits in the Tier B slot because it can only run once the
	// refs have moved -- the snapshot it compares against is a snapshot of what
	// the refs reach.
	tierB := func(ctx context.Context) error {
		infof(flags, "Capturing post-rewrite snapshot...\n")
		after, err := captureSnapshot(ctx)
		if err != nil {
			return fmt.Errorf("capturing post-rewrite snapshot: %v", err)
		}

		infof(flags, "Verifying...\n")
		failures := compareSnapshots(before, after, oldName, newName, oldEmail, true)

		// Also verify working tree is clean after rewrite
		statusOut, _, err := git.Run(ctx, "status", "--porcelain")
		if err != nil {
			failures = append(failures, fmt.Sprintf("checking working tree after rewrite: %v", err))
		} else if strings.TrimSpace(statusOut) != "" {
			failures = append(failures, "working tree is dirty after rewrite")
		}

		if len(failures) > 0 {
			for _, f := range failures {
				fmt.Fprintf(os.Stderr, "  FAIL: %s\n", f)
			}
			return fmt.Errorf("the rewritten history does not match the pre-rewrite snapshot in %d respect(s)", len(failures))
		}

		infof(flags, "Verification passed: all checks OK\n")
		return nil
	}

	// Finalize: verify, move refs, sync, cleanup, verify again, oplog, push hint.
	// The intent is identity-only: every commit's tree must come through
	// untouched and its message too, except at the commits where an
	// identity-bearing trailer was rewritten, which the walk declared.
	result.Intent = intent
	if err := result.Finalize(ctx, flags, cmd, RewriteHooks{TierB: tierB}); err != nil {
		dieFinalize("", err)
	}

	// The one computation both renderings read: the three counts below are the
	// three numbers the sentence prints.
	rewrites := make(map[string]string)
	for old, new_ := range shaMap {
		if old != new_ {
			rewrites[old] = new_
		}
	}
	tagRewrites := result.TagRewrites
	if tagRewrites == nil {
		tagRewrites = []TagRewrite{}
	}
	payload := RewriteAuthorResult{
		Version:          1,
		DryRun:           false,
		Rewrites:         rewrites,
		Tags:             tagRewrites,
		CommitsRewritten: intPtr(total),
		NameChanged:      intPtr(nameChanged),
		ParentOnly:       intPtr(parentOnly),
		OldHead:          oldHeadSHA,
		NewHead:          result.NewHeadSHA,
	}
	if oldName != "" {
		payload.OldName = oldName
		payload.NewName = newName
	}
	if oldEmail != "" {
		payload.OldEmail = oldEmail
		payload.NewEmail = newEmail
	}
	flags.payload(payload)

	if parentOnly == 1 {
		infof(flags, "Rewrote %d commits (%d had name changes, %d was inherited (ancestors changed))\n",
			total, nameChanged, parentOnly)
	} else {
		infof(flags, "Rewrote %d commits (%d had name changes, %d were inherited (ancestors changed))\n",
			total, nameChanged, parentOnly)
	}

	return result.TierBExit(exitcode.OK)
}

// RewriteAuthorResult is what `author rewrite` reports -- in both modes and in
// both renderings. There is no separate dry-run struct: the preview's counts
// and the execution's counts are members of one result, each present exactly
// when it was measured.
type RewriteAuthorResult struct {
	Version  int    `json:"version"`
	DryRun   bool   `json:"dry_run"`
	OldName  string `json:"old_name,omitempty"`
	NewName  string `json:"new_name,omitempty"`
	OldEmail string `json:"old_email,omitempty"`
	NewEmail string `json:"new_email,omitempty"`

	// Preview-only: the walk behind the preview.
	CommitsToCheck *int `json:"commits_to_check,omitempty"`
	CommitsMatched *int `json:"commits_matched,omitempty"`

	// Execute-only: what the rewrite did.
	Rewrites         map[string]string `json:"rewrites,omitempty"`
	Tags             []TagRewrite      `json:"tags,omitempty"`
	CommitsRewritten *int              `json:"commits_rewritten,omitempty"`
	NameChanged      *int              `json:"name_changed,omitempty"`
	ParentOnly       *int              `json:"parent_only,omitempty"`
	OldHead          string            `json:"old_head,omitempty"`
	NewHead          string            `json:"new_head,omitempty"`
}

// rewriteAuthorPayloadSchema declares what `author rewrite` puts in the
// envelope's payload.
var rewriteAuthorPayloadSchema = strictcli.SchemaObject(
	map[string]interface{}{
		"version":           strictcli.SchemaType("integer"),
		"dry_run":           strictcli.SchemaType("boolean"),
		"old_name":          strictcli.SchemaType("string"),
		"new_name":          strictcli.SchemaType("string"),
		"old_email":         strictcli.SchemaType("string"),
		"new_email":         strictcli.SchemaType("string"),
		"commits_to_check":  strictcli.SchemaType("integer"),
		"commits_matched":   strictcli.SchemaType("integer"),
		"rewrites":          scrubRewritesSchema,
		"tags":              scrubTagsSchema,
		"commits_rewritten": strictcli.SchemaType("integer"),
		"name_changed":      strictcli.SchemaType("integer"),
		"parent_only":       strictcli.SchemaType("integer"),
		"old_head":          strictcli.SchemaType("string"),
		"new_head":          strictcli.SchemaType("string"),
	},
	[]string{"version", "dry_run"},
	false,
)

// rewriteSnapshot captures the state of a repository's history for
// verification before and after an author-rewrite operation. Fields
// ordered by topo-order are parallel slices -- index i in each slice
// corresponds to the same commit.
type rewriteSnapshot struct {
	CommitCount    int
	TagCount       int
	BranchNames    []string
	TagNames       []string
	Messages       []string          // full commit messages, topo-order
	AuthorDates    []string          // topo-order
	CommitterDates []string          // topo-order
	AuthorEmails   []string          // topo-order
	AuthorNames    []string          // topo-order
	CommitterNames []string          // topo-order
	TreeHashes     []string          // topo-order
	ParentCounts   []int             // topo-order
	TagToMessage   map[string]string // tag name -> subject of the commit it points to
}

// refGlobs limits rev-list/log walks to branches, tags, and remote tracking
// refs, excluding refs/stash, refs/notes, and other non-standard refs.
var refGlobs = []string{"--glob=refs/heads", "--glob=refs/tags", "--glob=refs/remotes"}

// captureSnapshot collects all verification data points from the current
// repository state using git plumbing commands. Each data type uses a
// separate git log call for straightforward parsing.
func captureSnapshot(ctx context.Context) (rewriteSnapshot, error) {
	var snap rewriteSnapshot

	// Commit count
	args := append([]string{"rev-list", "--count"}, refGlobs...)
	out, _, err := git.Run(ctx, args...)
	if err != nil {
		return snap, fmt.Errorf("counting commits: %w", err)
	}
	count := 0
	fmt.Sscanf(strings.TrimSpace(out), "%d", &count)
	snap.CommitCount = count

	// Tree hashes
	snap.TreeHashes, err = logLines(ctx, "%T")
	if err != nil {
		return snap, fmt.Errorf("reading tree hashes: %w", err)
	}

	// Author dates
	snap.AuthorDates, err = logLines(ctx, "%ai")
	if err != nil {
		return snap, fmt.Errorf("reading author dates: %w", err)
	}

	// Committer dates
	snap.CommitterDates, err = logLines(ctx, "%ci")
	if err != nil {
		return snap, fmt.Errorf("reading committer dates: %w", err)
	}

	// Author emails
	snap.AuthorEmails, err = logLines(ctx, "%ae")
	if err != nil {
		return snap, fmt.Errorf("reading author emails: %w", err)
	}

	// Author names
	snap.AuthorNames, err = logLines(ctx, "%an")
	if err != nil {
		return snap, fmt.Errorf("reading author names: %w", err)
	}

	// Committer names
	snap.CommitterNames, err = logLines(ctx, "%cn")
	if err != nil {
		return snap, fmt.Errorf("reading committer names: %w", err)
	}

	// Parent hashes (space-separated per line) -> parent counts.
	// Root commits produce empty lines (no parents), so we must NOT use
	// logLines (which drops empty lines via splitNonEmpty). Instead, split
	// raw output preserving empty entries.
	parentArgs := append([]string{"log", "--topo-order", "--format=%P"}, refGlobs...)
	out, _, err = git.Run(ctx, parentArgs...)
	if err != nil {
		return snap, fmt.Errorf("reading parent hashes: %w", err)
	}
	parentRaw := strings.TrimRight(out, "\n")
	if parentRaw == "" {
		snap.ParentCounts = nil
	} else {
		parentLines := strings.Split(parentRaw, "\n")
		snap.ParentCounts = make([]int, len(parentLines))
		for i, line := range parentLines {
			if line == "" {
				snap.ParentCounts[i] = 0
			} else {
				snap.ParentCounts[i] = len(strings.Fields(line))
			}
		}
	}

	// Full commit messages: use \x01 as a record separator before each
	// message body (%B can contain newlines). Uses refGlobs (not --all) to
	// match the same ref scope as the walker and all other snapshot queries.
	msgArgs := append([]string{"log", "--topo-order", "--format=%x01%B"}, refGlobs...)
	out, _, err = git.Run(ctx, msgArgs...)
	if err != nil {
		return snap, fmt.Errorf("reading commit messages: %w", err)
	}
	snap.Messages = splitMessages(out)

	// Branch names
	out, _, err = git.Run(ctx, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return snap, fmt.Errorf("reading branch names: %w", err)
	}
	snap.BranchNames = splitSorted(out)

	// Tag names
	out, _, err = git.Run(ctx, "tag", "-l")
	if err != nil {
		return snap, fmt.Errorf("reading tag names: %w", err)
	}
	snap.TagNames = splitSorted(out)
	snap.TagCount = len(snap.TagNames)

	// Tag-to-message map
	snap.TagToMessage, err = buildTagToMessage(ctx, snap.TagNames)
	if err != nil {
		return snap, fmt.Errorf("building tag-to-message map: %w", err)
	}

	return snap, nil
}

// logLines runs git log --all --topo-order with the given format and
// returns non-empty output lines.
func logLines(ctx context.Context, format string) ([]string, error) {
	args := append([]string{"log", "--topo-order", "--format=" + format}, refGlobs...)
	out, _, err := git.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return git.SplitNonEmpty(out), nil
}

// splitSorted splits on newlines, drops empties, and sorts the result.
func splitSorted(s string) []string {
	lines := git.SplitNonEmpty(s)
	sort.Strings(lines)
	return lines
}

// splitMessages parses the output of git log --format=%x01%B into
// individual commit messages. Each record starts with \x01, followed
// by the full message body (which may span multiple lines).
func splitMessages(raw string) []string {
	parts := strings.Split(raw, "\x01")
	var msgs []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		msgs = append(msgs, trimmed)
	}
	return msgs
}

// buildTagToMessage maps each tag name to the subject line of the
// commit it ultimately points to (dereferencing annotated tags).
func buildTagToMessage(ctx context.Context, tagNames []string) (map[string]string, error) {
	m := make(map[string]string, len(tagNames))
	for _, tag := range tagNames {
		// Dereference to commit in case of annotated tags.
		sha, _, err := git.Run(ctx, "rev-parse", tag+"^{commit}")
		if err != nil {
			// Tag might not point to a commit (e.g. a tag on a blob); skip.
			continue
		}
		sha = strings.TrimSpace(sha)
		subject, _, err := git.Run(ctx, "log", "-1", "--format=%s", sha)
		if err != nil {
			continue
		}
		m[tag] = strings.TrimSpace(subject)
	}
	return m, nil
}

// compareSnapshots compares a before and after snapshot to verify that a
// history rewrite preserved everything except author/committer names.
// oldName is the author name that should no longer appear after the
// rewrite. Returns a slice of failure descriptions; an empty slice
// means all checks passed.
func compareSnapshots(before, after rewriteSnapshot, oldName, newName, oldEmail string, ignoreTrailers bool) []string {
	var failures []string

	// 1. Commit count
	if before.CommitCount != after.CommitCount {
		failures = append(failures, fmt.Sprintf(
			"commit count changed: before=%d, after=%d",
			before.CommitCount, after.CommitCount))
	}

	// 2. Tag count
	if before.TagCount != after.TagCount {
		failures = append(failures, fmt.Sprintf(
			"tag count changed: before=%d, after=%d",
			before.TagCount, after.TagCount))
	}

	// 3. Branch names
	if msg := compareStringSlices("branch names", before.BranchNames, after.BranchNames); msg != "" {
		failures = append(failures, msg)
	}

	// 4. Tag names
	if msg := compareStringSlices("tag names", before.TagNames, after.TagNames); msg != "" {
		failures = append(failures, msg)
	}

	// 5. Old name must not appear in author or committer names
	//    (skip when oldName is empty or old and new names are identical).
	//    When AND matching (oldEmail != ""), commits with oldName but a
	//    different email are intentionally preserved -- only check that no
	//    commit has BOTH the old name AND the old email.
	if oldName != "" && oldName != newName {
		if oldEmail != "" {
			// AND matching: only flag commits where both name and email match.
			for i, name := range after.AuthorNames {
				if name == oldName && i < len(after.AuthorEmails) && after.AuthorEmails[i] == oldEmail {
					failures = append(failures, fmt.Sprintf(
						"old author name %q with old email %q still present at commit index %d", oldName, oldEmail, i))
					break
				}
			}
			// CommitterNames don't have a parallel CommitterEmails slice in
			// the snapshot, so skip the AND check for committers.
		} else {
			// Name-only matching: oldName must not appear at all.
			for i, name := range after.AuthorNames {
				if name == oldName {
					failures = append(failures, fmt.Sprintf(
						"old author name %q still present in author at commit index %d", oldName, i))
					break
				}
			}
			for i, name := range after.CommitterNames {
				if name == oldName {
					failures = append(failures, fmt.Sprintf(
						"old author name %q still present in committer at commit index %d", oldName, i))
					break
				}
			}
		}
	}

	// 6. Messages
	if ignoreTrailers {
		// Strip trailers before comparing so that trailer rewrites
		// (e.g., Signed-off-by identity changes) don't cause false failures.
		beforeBodies := make([]string, len(before.Messages))
		afterBodies := make([]string, len(after.Messages))
		for i, m := range before.Messages {
			body, _ := trailer.SplitBodyTrailers(m)
			beforeBodies[i] = body
		}
		for i, m := range after.Messages {
			body, _ := trailer.SplitBodyTrailers(m)
			afterBodies[i] = body
		}
		if msg := compareStringSlices("messages (body only)", beforeBodies, afterBodies); msg != "" {
			failures = append(failures, msg)
		}
	} else {
		if msg := compareStringSlices("messages", before.Messages, after.Messages); msg != "" {
			failures = append(failures, msg)
		}
	}

	// 7. Author dates
	if msg := compareStringSlices("author dates", before.AuthorDates, after.AuthorDates); msg != "" {
		failures = append(failures, msg)
	}

	// 8. Committer dates
	if msg := compareStringSlices("committer dates", before.CommitterDates, after.CommitterDates); msg != "" {
		failures = append(failures, msg)
	}

	// 9. Author emails
	if oldEmail != "" {
		for i, email := range after.AuthorEmails {
			if email == oldEmail {
				failures = append(failures, fmt.Sprintf(
					"old email %q still present in author at commit index %d", oldEmail, i))
				break
			}
		}
	} else {
		if msg := compareStringSlices("author emails", before.AuthorEmails, after.AuthorEmails); msg != "" {
			failures = append(failures, msg)
		}
	}

	// 10. Tree hashes
	if msg := compareStringSlices("tree hashes", before.TreeHashes, after.TreeHashes); msg != "" {
		failures = append(failures, msg)
	}

	// 11. Parent counts
	if msg := compareIntSlices("parent counts", before.ParentCounts, after.ParentCounts); msg != "" {
		failures = append(failures, msg)
	}

	// 12. Tag-to-message mapping
	for tag, beforeMsg := range before.TagToMessage {
		afterMsg, ok := after.TagToMessage[tag]
		if !ok {
			failures = append(failures, fmt.Sprintf(
				"tag %q missing from after snapshot", tag))
			continue
		}
		if beforeMsg != afterMsg {
			failures = append(failures, fmt.Sprintf(
				"tag %q message changed: before=%q, after=%q", tag, beforeMsg, afterMsg))
		}
	}
	for tag := range after.TagToMessage {
		if _, ok := before.TagToMessage[tag]; !ok {
			failures = append(failures, fmt.Sprintf(
				"tag %q present in after snapshot but missing from before", tag))
		}
	}

	return failures
}

// compareStringSlices reports the first difference between two string slices.
// Returns an empty string if they are identical.
func compareStringSlices(label string, before, after []string) string {
	if len(before) != len(after) {
		return fmt.Sprintf("%s: length differs: before=%d, after=%d", label, len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			return fmt.Sprintf("%s: differ at index %d: before=%q, after=%q",
				label, i, before[i], after[i])
		}
	}
	return ""
}

// compareIntSlices reports the first difference between two int slices.
// Returns an empty string if they are identical.
func compareIntSlices(label string, before, after []int) string {
	if len(before) != len(after) {
		return fmt.Sprintf("%s: length differs: before=%d, after=%d", label, len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			return fmt.Sprintf("%s: differ at index %d: before=%d, after=%d",
				label, i, before[i], after[i])
		}
	}
	return ""
}

// rewriteCommits walks all commits in dependency order (parents before
// children) and creates new commit objects wherever the author/committer
// name matches oldName or a parent SHA was remapped by an earlier rewrite.
// Returns the old-to-new SHA mapping, the count of commits whose name was
// actually changed, and any error.
// rewriteCommits rewrites the author and committer identity across history. It
// fills intent with the one thing about a commit's CONTENT this rewrite may
// change: an identity-bearing trailer in the message. Tier A holds it to that
// -- trees identical everywhere, messages changed only where a trailer was
// rewritten.
func rewriteCommits(ctx context.Context, oldName, newName, oldEmail, newEmail string, intent *RewriteIntent, verbose bool) (map[string]string, int, error) {
	// Get all commits in topo-order with parents before children.
	args := append([]string{"rev-list", "--topo-order", "--reverse"}, refGlobs...)
	out, _, err := git.Run(ctx, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing commits: %w", err)
	}

	shas := git.SplitNonEmpty(out)
	nameChanged := 0

	shaMap, _, err := walkAndRewrite(ctx, shas, func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string, shaMap map[string]string) (CommitTransform, error) {
		author := info.Author
		committer := info.Committer
		thisNameChanged := false

		nameMatch := oldName != "" && (author.Name == oldName || committer.Name == oldName)
		emailMatch := oldEmail != "" && (author.Email == oldEmail || committer.Email == oldEmail)

		var commitMatches bool
		if oldName != "" && oldEmail != "" {
			commitMatches = nameMatch && emailMatch // AND when both specified
		} else {
			commitMatches = nameMatch || emailMatch // OR when only one specified
		}

		if commitMatches {
			if oldName != "" && author.Name == oldName {
				author.Name = newName
				thisNameChanged = true
			}
			if oldName != "" && committer.Name == oldName {
				committer.Name = newName
				thisNameChanged = true
			}
			if oldEmail != "" && author.Email == oldEmail {
				author.Email = newEmail
				thisNameChanged = true
			}
			if oldEmail != "" && committer.Email == oldEmail {
				committer.Email = newEmail
				thisNameChanged = true
			}
		}

		if thisNameChanged {
			nameChanged++
		}

		var xform CommitTransform
		if thisNameChanged {
			xform.Author = author
			xform.Committer = committer

			// Also rewrite identity-bearing trailers (Signed-off-by, Co-authored-by, etc.)
			newMsg := trailer.ReplaceIdentity(info.Message, oldName, newName, oldEmail, newEmail)
			if newMsg != info.Message {
				xform.Message = newMsg
				intent.Declare(sha, nil, true)
			}
		}
		return xform, nil
	}, verbose)
	if err != nil {
		return nil, 0, err
	}

	return shaMap, nameChanged, nil
}

// rewriteAnnotatedTag rewrites an annotated tag object if its target commit
// was remapped or its tagger name matches oldName. Returns the new tag object
// SHA, or the original SHA if nothing changed.
func rewriteAnnotatedTag(ctx context.Context, tagObjectSHA string, shaMap map[string]string, oldName, newName, oldEmail, newEmail string) (string, error) {
	out, _, err := git.Run(ctx, "cat-file", "-p", tagObjectSHA)
	if err != nil {
		return "", fmt.Errorf("reading tag object %s: %w", tagObjectSHA, err)
	}

	// Split into header and body at the first blank line.
	headerEnd := strings.Index(out, "\n\n")
	var headerSection, body string
	if headerEnd < 0 {
		headerSection = out
	} else {
		headerSection = out[:headerEnd]
		body = out[headerEnd+2:]
	}

	lines := strings.Split(headerSection, "\n")
	changed := false

	for i, line := range lines {
		spIdx := strings.IndexByte(line, ' ')
		if spIdx < 0 {
			continue
		}
		key := line[:spIdx]
		val := line[spIdx+1:]

		switch key {
		case "object":
			if newSHA, ok := shaMap[val]; ok && newSHA != val {
				lines[i] = "object " + newSHA
				changed = true
			}
		case "tagger":
			// Format: "Name <email> timestamp timezone"
			// Parse tagger name and email for AND/OR matching.
			ltIdx := strings.LastIndex(val, " <")
			if ltIdx >= 0 {
				taggerName := val[:ltIdx]
				rest := val[ltIdx:] // " <email> timestamp timezone"
				gtIdx := strings.IndexByte(rest, '>')
				var taggerEmail string
				if gtIdx >= 0 {
					taggerEmail = rest[2:gtIdx] // extract email between < and >
				}

				taggerNameMatch := oldName != "" && taggerName == oldName
				taggerEmailMatch := oldEmail != "" && taggerEmail == oldEmail

				var taggerMatches bool
				if oldName != "" && oldEmail != "" {
					taggerMatches = taggerNameMatch && taggerEmailMatch
				} else {
					taggerMatches = taggerNameMatch || taggerEmailMatch
				}

				if taggerMatches {
					newTaggerName := taggerName
					if taggerNameMatch && oldName != "" {
						newTaggerName = newName
					}
					newRest := rest
					if taggerEmailMatch && oldEmail != "" && gtIdx >= 0 {
						newRest = " <" + newEmail + rest[gtIdx:]
					}
					lines[i] = "tagger " + newTaggerName + newRest
					changed = true
				}
			}
		}
	}

	if !changed {
		return tagObjectSHA, nil
	}

	// Reconstruct the full tag object content.
	content := strings.Join(lines, "\n") + "\n\n" + body

	newSHA, err := git.HashObjectWriteTag(ctx, []byte(content))
	if err != nil {
		return "", fmt.Errorf("writing rewritten tag object: %w", err)
	}
	return newSHA, nil
}
