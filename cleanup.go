package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/stricttools/safegit/internal/git"
)

// cleanupAfterRewrite performs surgical post-rewrite cleanup: expires only
// tainted reflog entries (those referencing pre-rewrite SHAs), prunes
// unreachable objects, and warns about stash/notes/replace refs that still
// reference old commits.
//
// All failures are non-fatal (warnings), but each one is also collected into
// the returned cleanupErrors slice so callers can report cleanup status
// machine-readably. Orchestrators depend on old objects being pruned, so a
// non-empty cleanupErrors means "do not assume old SHAs are unresolvable."
//
// residue is the one finding that is more than a warning: pre-rewrite objects
// that survived the prune and that NO surviving ref reaches. The content the
// operator asked to remove is still readable by SHA, so the caller records it
// as a Tier B finding -- the rewrite stands and the command exits nonzero
// naming what is left. It is returned separately rather than fished back out of
// cleanupErrors, where it also appears so the payload's cleanup_ok keeps saying
// what it always said.
func cleanupAfterRewrite(ctx context.Context, flags globalFlags, cmd string, shaMap map[string]string, tagRewrites []TagRewrite, sgDir string) (cleanupErrors []string, residue string, err error) {
	// Build the set of old SHAs that were actually remapped (old != new).
	// Tag rewrites count too: the annotation pass can rewrite tag objects
	// even when every commit maps to itself, and the old tag object (which
	// may contain a scrubbed secret) must be pruned like any old commit.
	oldSHAs := make(map[string]bool)
	for old, new_ := range shaMap {
		if old != new_ {
			oldSHAs[old] = true
		}
	}
	for _, tr := range tagRewrites {
		if tr.OldSHA != tr.NewSHA {
			oldSHAs[tr.OldSHA] = true
		}
	}
	if len(oldSHAs) == 0 {
		return nil, "", nil // nothing was rewritten
	}

	// Step 1+2: Identify and delete tainted reflog entries.
	if err := expireTaintedReflogEntries(ctx, flags, oldSHAs); err != nil {
		// Non-fatal: warn and continue to pruning.
		warnf(flags, "reflog cleanup: %v", err)
		cleanupErrors = append(cleanupErrors, fmt.Sprintf("reflog cleanup: %v", err))
	}

	// Step 2b: Expire all remaining reflog entries. Surgical deletion (step 1+2)
	// removes entries whose "to" SHA matches an old commit, but reflog entries
	// also store a "from" SHA. Git considers both SHAs reachable, so entries
	// like "a6bca30 -> 49f563f" keep old commit a6bca30 alive even after the
	// "to" entry was deleted. A full expire is needed to clean up these
	// remaining references after a security-sensitive rewrite.
	if _, _, err := git.Run(ctx, "reflog", "expire", "--expire=now", "--all"); err != nil {
		warnf(flags, "reflog expire: %v", err)
		cleanupErrors = append(cleanupErrors, fmt.Sprintf("reflog expire: %v", err))
	}

	// Step 3: Prune unreachable objects. git prune only removes loose objects;
	// git repack -a -d --unpack-unreachable=now drops unreachable objects from
	// pack files as well. Both are needed because objects may be loose (newly
	// created) or packed (pre-existing).
	if flags.verbose {
		debugf(flags, "Pruning unreachable objects")
	}
	if _, _, err := git.Run(ctx, "repack", "-a", "-d", "--unpack-unreachable=now"); err != nil {
		warnf(flags, "git repack: %v", err)
		cleanupErrors = append(cleanupErrors, fmt.Sprintf("git repack: %v", err))
	}
	if _, _, err := git.Run(ctx, "prune", "--expire=now"); err != nil {
		warnf(flags, "git prune: %v", err)
		cleanupErrors = append(cleanupErrors, fmt.Sprintf("git prune: %v", err))
	}

	// Step 4: Check stash/notes/replace refs for old SHAs.
	checkStashForOldSHAs(flags, ctx, oldSHAs)
	checkNotesForOldSHAs(flags, ctx, oldSHAs)
	checkReplaceRefsForOldSHAs(flags, ctx, oldSHAs)

	// Step 5: Verify old objects are gone.
	surviving, sErr := verifyOldObjectsGone(ctx, flags, oldSHAs)
	switch {
	case sErr != nil:
		// Not knowing is not the same as knowing there is nothing: without the
		// reachable set the question cannot be answered, and answering it
		// wrongly in either direction is worse than saying so.
		residue = fmt.Sprintf("could not check whether pre-rewrite objects survived cleanup: %v", sErr)
	case len(surviving) > 0:
		short := make([]string, 0, len(surviving))
		for _, sha := range surviving {
			short = append(short, shortSHA(sha))
		}
		residue = fmt.Sprintf("%d pre-rewrite object(s) survived cleanup and no surviving ref reaches them: %s",
			len(surviving), strings.Join(short, ", "))
	}
	if residue != "" {
		cleanupErrors = append(cleanupErrors, residue)
	}

	return cleanupErrors, residue, nil
}

// reflogEntry holds a parsed reflog line.
type reflogEntry struct {
	sha       string // commit SHA
	qualifier string // e.g. "HEAD@{3}" or "refs/heads/main@{5}"
	ref       string // the ref portion, e.g. "HEAD" or "refs/heads/main"
	index     int    // the numeric index within the ref's reflog
}

// expireTaintedReflogEntries finds reflog entries whose SHA is in oldSHAs
// and deletes them in reverse index order (per ref) to avoid index shifting.
func expireTaintedReflogEntries(ctx context.Context, flags globalFlags, oldSHAs map[string]bool) error {
	// Get all reflog entries across all refs.
	out, _, err := git.Run(ctx, "reflog", "show", "--format=%H %gD", "--all")
	if err != nil {
		// Also try HEAD specifically — some repos don't have --all reflogs.
		out, _, err = git.Run(ctx, "reflog", "show", "--format=%H %gD")
		if err != nil {
			return fmt.Errorf("reading reflogs: %w", err)
		}
	}

	// Also get HEAD reflog entries (--all may not include HEAD).
	headOut, _, _ := git.Run(ctx, "reflog", "show", "--format=%H %gD", "HEAD")

	// Merge both outputs, dedup by qualifier.
	seen := make(map[string]bool)
	var tainted []reflogEntry

	for _, block := range []string{out, headOut} {
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, " ", 2)
			if len(parts) != 2 {
				continue
			}
			sha := parts[0]
			qualifier := parts[1]

			if seen[qualifier] {
				continue
			}
			seen[qualifier] = true

			if !oldSHAs[sha] {
				continue
			}

			// Parse qualifier to extract ref and index.
			// Format: "HEAD@{3}" or "refs/heads/main@{5}"
			ref, idx := parseQualifier(qualifier)
			if ref == "" {
				continue
			}

			tainted = append(tainted, reflogEntry{
				sha:       sha,
				qualifier: qualifier,
				ref:       ref,
				index:     idx,
			})
		}
	}

	if len(tainted) == 0 {
		return nil
	}

	if flags.verbose {
		debugf(flags, "Expiring %d reflog entries referencing pre-rewrite objects", len(tainted))
	}

	// Group by ref, then sort each group by index descending (reverse order
	// to avoid index shifting when deleting).
	byRef := make(map[string][]reflogEntry)
	for _, e := range tainted {
		byRef[e.ref] = append(byRef[e.ref], e)
	}

	for _, entries := range byRef {
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].index > entries[j].index // descending
		})
		for _, e := range entries {
			if _, _, err := git.Run(ctx, "reflog", "delete", e.qualifier); err != nil {
				if flags.verbose {
					debugf(flags, "  warning: failed to delete reflog entry %s: %v", e.qualifier, err)
				}
			}
		}
	}

	return nil
}

// parseQualifier extracts the ref name and numeric index from a reflog
// qualifier like "HEAD@{3}" or "refs/heads/main@{5}".
func parseQualifier(q string) (ref string, index int) {
	atIdx := strings.LastIndex(q, "@{")
	if atIdx < 0 {
		return "", 0
	}
	ref = q[:atIdx]
	idxStr := strings.TrimSuffix(q[atIdx+2:], "}")
	idx := 0
	for _, c := range idxStr {
		if c < '0' || c > '9' {
			return ref, 0
		}
		idx = idx*10 + int(c-'0')
	}
	return ref, idx
}

// checkStashForOldSHAs warns if any stash entry references a pre-rewrite commit.
func checkStashForOldSHAs(flags globalFlags, ctx context.Context, oldSHAs map[string]bool) {
	out, _, err := git.Run(ctx, "stash", "list", "--format=%H %gd")
	if err != nil {
		return // no stash or error — nothing to do
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		sha := parts[0]
		stashRef := parts[1] // e.g. "stash@{0}"
		if oldSHAs[sha] {
			// Extract the numeric index for the drop command.
			idx := strings.TrimPrefix(stashRef, "stash@{")
			idx = strings.TrimSuffix(idx, "}")
			warnf(flags, "stash entry %s references pre-rewrite commit %s\n"+
				"  run: git stash drop %s", stashRef, shortSHA(sha), idx)
		}
	}
}

// checkNotesForOldSHAs warns if any note references a pre-rewrite commit.
func checkNotesForOldSHAs(flags globalFlags, ctx context.Context, oldSHAs map[string]bool) {
	out, _, err := git.Run(ctx, "notes", "list")
	if err != nil {
		return // no notes or error
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "<note-blob-sha> <annotated-object-sha>"
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		objSHA := parts[1]
		if oldSHAs[objSHA] {
			warnf(flags, "note on %s may reference pre-rewrite objects\n"+
				"  run: git notes remove %s", shortSHA(objSHA), objSHA)
		}
	}
}

// checkReplaceRefsForOldSHAs warns if any replace ref references a pre-rewrite commit.
func checkReplaceRefsForOldSHAs(flags globalFlags, ctx context.Context, oldSHAs map[string]bool) {
	out, _, err := git.Run(ctx, "for-each-ref", "--format=%(refname) %(objectname)", "refs/replace/")
	if err != nil {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		refname := parts[0]
		sha := parts[1]
		if oldSHAs[sha] {
			warnf(flags, "replace ref %s references pre-rewrite commit %s\n"+
				"  run: git update-ref -d %s", refname, shortSHA(sha), refname)
		}
	}
}

// verifyOldObjectsGone returns the pre-rewrite objects that are STILL in the
// object store and that no surviving ref reaches, sorted.
//
// The reachability half is what keeps the answer honest on an ordinary
// repository. A branch outside the walked range keeps its own history alive,
// and every commit of it the rewrite mapped to a new SHA on the walked branch
// is a pre-rewrite object that prune is right to leave in place. Asking only
// "does the object still exist" reports that branch's history as residue, which
// is why this check could never be wired to an exit code before.
//
// A failure to read the reachable set is returned as an error rather than
// swallowed: an unanswerable question is not a clean bill of health.
func verifyOldObjectsGone(ctx context.Context, flags globalFlags, oldSHAs map[string]bool) ([]string, error) {
	reachable, err := buildReachableObjectSet(ctx)
	if err != nil {
		return nil, err
	}

	var surviving []string
	for sha := range oldSHAs {
		if reachable[sha] {
			continue
		}
		// git cat-file -e exits 0 if the object exists, non-zero if gone.
		if _, _, cerr := git.Run(ctx, "cat-file", "-e", sha); cerr == nil {
			surviving = append(surviving, sha)
			if flags.verbose {
				debugf(flags, "  pre-rewrite object %s still exists after cleanup and no ref reaches it", shortSHA(sha))
			}
		}
	}
	sort.Strings(surviving)
	return surviving, nil
}
