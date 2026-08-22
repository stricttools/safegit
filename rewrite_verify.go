package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/smm-h/safegit/internal/git"
)

// This file holds the verification a history rewrite runs against ITSELF, in
// the two tiers Finalize executes.
//
// Tier A runs while the rewritten commits are still nothing but unreachable
// objects: no ref has moved, no journal record exists, and the repository's
// history is exactly what it was. Every Tier A failure is therefore a hard
// refusal that costs nothing -- the operator re-runs the command.
//
// Tier B runs after the refs have moved and cleanup has swept the object
// store. Nothing there can be undone by refusing, so a Tier B failure is a
// nonzero exit that NAMES what survived, with the rewrite standing.

// verifyIntendedChanges is Tier A's preservation check: it compares every
// rewritten commit against what the operation declared it would change.
//
// One diff-tree per old/new pair answers the path question. The alternative --
// materializing both trees with `ls-tree -r` and comparing maps -- reads two
// whole trees per pair to compute a delta git computes directly.
//
// Returns one failure string per disagreement; empty means the rewrite did
// exactly what it said.
func verifyIntendedChanges(ctx context.Context, shaMap map[string]string, intent *RewriteIntent) []string {
	var failures []string

	rewritten := make(map[string]bool)
	oldSHAs := make([]string, 0, len(shaMap))
	for old := range shaMap {
		oldSHAs = append(oldSHAs, old)
	}
	sort.Strings(oldSHAs)

	for _, oldSHA := range oldSHAs {
		newSHA := shaMap[oldSHA]
		if newSHA == oldSHA {
			continue
		}
		rewritten[oldSHA] = true

		oldInfo, err := git.ParseCommit(ctx, oldSHA)
		if err != nil {
			failures = append(failures, fmt.Sprintf("reading original commit %s: %v", shortSHA(oldSHA), err))
			continue
		}
		newInfo, err := git.ParseCommit(ctx, newSHA)
		if err != nil {
			failures = append(failures, fmt.Sprintf("reading rewritten commit %s: %v", shortSHA(newSHA), err))
			continue
		}

		failures = append(failures, verifyParentTopology(oldSHA, oldInfo, newInfo, shaMap)...)

		switch intent.Kind {
		case IntentIdentityOnly:
			// An identity rewrite never touches content: every tree must come
			// through byte-identical. A message may change only where the walk
			// declared it -- an identity-bearing trailer it rewrote.
			if oldInfo.Tree != newInfo.Tree {
				failures = append(failures, fmt.Sprintf(
					"commit %s: the rewrite changes identity headers only, but its tree changed (%s -> %s)",
					shortSHA(oldSHA), shortSHA(oldInfo.Tree), shortSHA(newInfo.Tree)))
			}
			failures = append(failures, verifyMessageAgainstIntent(oldSHA, oldInfo, newInfo, intent.For(oldSHA))...)
		case IntentPerPath:
			declared := intent.For(oldSHA)
			failures = append(failures, verifyIdentityPreserved(oldSHA, oldInfo, newInfo)...)
			failures = append(failures, verifyMessageAgainstIntent(oldSHA, oldInfo, newInfo, declared)...)
			pathFailures, err := verifyPathsAgainstIntent(ctx, oldSHA, oldInfo, newInfo, declared)
			if err != nil {
				failures = append(failures, err.Error())
			}
			failures = append(failures, pathFailures...)
		}
	}

	// The rewrote-count tripwire: a commit the operation decided to change must
	// actually have been rewritten. A declared change that produced no new
	// commit means the walk dropped it.
	for _, sha := range intent.ChangedCommits() {
		if !rewritten[sha] {
			declared := intent.For(sha)
			failures = append(failures, fmt.Sprintf(
				"commit %s: the operation declared changes (%s) but no rewritten commit was produced for it",
				shortSHA(sha), declared.PathList()))
		}
	}

	return failures
}

// verifyParentTopology checks that the rewritten commit has the same parents,
// in the same order, each one remapped through the SHA map.
func verifyParentTopology(oldSHA string, oldInfo, newInfo git.CommitInfo, shaMap map[string]string) []string {
	if len(oldInfo.Parents) != len(newInfo.Parents) {
		return []string{fmt.Sprintf(
			"commit %s: parent count changed (%d -> %d)",
			shortSHA(oldSHA), len(oldInfo.Parents), len(newInfo.Parents))}
	}
	var failures []string
	for i, oldParent := range oldInfo.Parents {
		expected := oldParent
		if mapped, ok := shaMap[oldParent]; ok {
			expected = mapped
		}
		if newInfo.Parents[i] != expected {
			failures = append(failures, fmt.Sprintf(
				"commit %s: parent %d is %s, want the remapped %s",
				shortSHA(oldSHA), i, shortSHA(newInfo.Parents[i]), shortSHA(expected)))
		}
	}
	return failures
}

// verifyIdentityPreserved checks that a content rewrite left author and
// committer alone. Only `author rewrite` may change them, and it declares
// IntentIdentityOnly.
func verifyIdentityPreserved(oldSHA string, oldInfo, newInfo git.CommitInfo) []string {
	var failures []string
	if oldInfo.Author != newInfo.Author {
		failures = append(failures, fmt.Sprintf(
			"commit %s: author changed (%s <%s> -> %s <%s>)", shortSHA(oldSHA),
			oldInfo.Author.Name, oldInfo.Author.Email, newInfo.Author.Name, newInfo.Author.Email))
	}
	if oldInfo.Committer != newInfo.Committer {
		failures = append(failures, fmt.Sprintf(
			"commit %s: committer changed (%s <%s> -> %s <%s>)", shortSHA(oldSHA),
			oldInfo.Committer.Name, oldInfo.Committer.Email, newInfo.Committer.Name, newInfo.Committer.Email))
	}
	return failures
}

// verifyMessageAgainstIntent checks the message against the declaration in both
// directions: a message that changed without being declared, and a declared
// message change that did not happen.
func verifyMessageAgainstIntent(oldSHA string, oldInfo, newInfo git.CommitInfo, declared IntendedChange) []string {
	changed := oldInfo.Message != newInfo.Message
	switch {
	case changed && !declared.MessageChanged:
		return []string{fmt.Sprintf(
			"commit %s: its message was rewritten, which no operation asked for", shortSHA(oldSHA))}
	case !changed && declared.MessageChanged:
		return []string{fmt.Sprintf(
			"commit %s: the operation decided to rewrite its message, but the message is unchanged", shortSHA(oldSHA))}
	}
	return nil
}

// verifyPathsAgainstIntent diffs the old and new commit and compares the paths
// that actually changed against the paths the operation declared.
func verifyPathsAgainstIntent(ctx context.Context, oldSHA string, oldInfo, newInfo git.CommitInfo, declared IntendedChange) ([]string, error) {
	actual := make(map[string]bool)
	if oldInfo.Tree != newInfo.Tree {
		changed, err := git.DiffTree(ctx, oldInfo.Tree, newInfo.Tree)
		if err != nil {
			return nil, fmt.Errorf("commit %s: comparing the original and rewritten trees: %v", shortSHA(oldSHA), err)
		}
		for _, c := range changed {
			actual[c.Path] = true
		}
	}

	var unexpected, missing []string
	for p := range actual {
		if !declared.Paths[p] {
			unexpected = append(unexpected, p)
		}
	}
	for p := range declared.Paths {
		if !actual[p] {
			missing = append(missing, p)
		}
	}
	sort.Strings(unexpected)
	sort.Strings(missing)

	var failures []string
	if len(unexpected) > 0 {
		failures = append(failures, fmt.Sprintf(
			"commit %s: %s changed, which no operation asked for (the operation declared: %s)",
			shortSHA(oldSHA), strings.Join(unexpected, ", "), declared.PathList()))
	}
	if len(missing) > 0 {
		failures = append(failures, fmt.Sprintf(
			"commit %s: the operation decided to change %s, but the rewritten commit does not",
			shortSHA(oldSHA), strings.Join(missing, ", ")))
	}
	return failures, nil
}

// verifyRefsRemapped is Tier B's stale-pointer check: after the refs have
// moved, no branch or tag may still point at a pre-rewrite commit, and every
// tag must point at an object that exists.
func verifyRefsRemapped(ctx context.Context, shaMap map[string]string) []string {
	var failures []string

	tagOut, _, err := git.Run(ctx, "for-each-ref", "--format=%(refname:short) %(objecttype) %(*objectname) %(objectname)", "refs/tags/")
	if err != nil {
		failures = append(failures, fmt.Sprintf("listing tags to check for stale pointers: %v", err))
	} else {
		for _, line := range git.SplitNonEmpty(tagOut) {
			parts := strings.Fields(line)
			if len(parts) < 3 {
				continue
			}
			tagName, objType := parts[0], parts[1]
			targetSHA := parts[len(parts)-1]
			if objType == "tag" && len(parts) >= 4 {
				// Annotated tag: the dereferenced target is the third field.
				targetSHA = parts[2]
			}

			if _, _, err := git.Run(ctx, "cat-file", "-e", targetSHA); err != nil {
				failures = append(failures, fmt.Sprintf(
					"tag %q points at %s, which is not in the object store", tagName, shortSHA(targetSHA)))
			}
			if mapped, ok := shaMap[targetSHA]; ok && mapped != targetSHA {
				failures = append(failures, fmt.Sprintf(
					"tag %q still points at the pre-rewrite commit %s (should be %s)",
					tagName, shortSHA(targetSHA), shortSHA(mapped)))
			}
		}
	}

	branchOut, _, err := git.Run(ctx, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	if err != nil {
		failures = append(failures, fmt.Sprintf("listing branches to check for stale pointers: %v", err))
		return failures
	}
	for _, line := range git.SplitNonEmpty(branchOut) {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		refname, commitSHA := parts[0], parts[1]
		if mapped, ok := shaMap[commitSHA]; ok && mapped != commitSHA {
			failures = append(failures, fmt.Sprintf(
				"branch %q still points at the pre-rewrite commit %s (should be %s)",
				refname, shortSHA(commitSHA), shortSHA(mapped)))
		}
	}
	return failures
}

// foreignWorktreeState reports working-tree, index and untracked state that a
// rewrite did not put there, measured against the commit the rewrite STARTED
// from.
//
// Three questions, asked with the tool that answers each one honestly:
//
//   - the working tree against the INDEX -- `git status`'s second column,
//     which is what it is regardless of where HEAD points. It has to be status
//     rather than a plain `diff-index <baseline>`, because diff-index trusts
//     the index's stat cache and safegit's commit pipeline never writes that
//     cache (it commits through a per-invocation temporary index), so a
//     perfectly clean tree reports every file as modified;
//   - the INDEX against the pre-rewrite HEAD -- `diff-index --cached`, which
//     compares recorded content and has no stat cache to be stale about;
//   - untracked files, from the same status listing.
//
// The baseline is the pre-rewrite HEAD rather than the current one, and that is
// the design. A rewrite refuses to start on a dirty tree, so a difference from
// the pre-rewrite HEAD is by definition work that appeared while the rewrite
// was running -- another session in the same worktree, an editor, a hook.
// Asking `git status` about HEAD instead would be useless the moment the refs
// move: HEAD then holds rewritten content the working tree has not been synced
// to yet, so every scrubbed file would read as a foreign modification.
//
// Tier A calls it before any ref moves and refuses; the pre-sync check calls it
// again immediately before `read-tree --reset -u` and skips the sync rather
// than overwriting whatever showed up.
//
// Submodule state is deliberately excluded: a submodule scrub moves the
// submodule's HEAD on purpose, which makes the parent's gitlink differ from its
// committed value at exactly this moment. That is the rewrite's own doing, not
// foreign work.
func foreignWorktreeState(ctx context.Context, baseline string) ([]string, error) {
	if baseline == "" {
		return nil, nil
	}

	var found []string

	// Working tree against the index, plus untracked files. --no-renames keeps
	// every record one path, and -z leaves paths unquoted.
	out, _, err := git.Run(ctx, "status", "--porcelain", "-z", "--no-renames", "--ignore-submodules=all")
	if err != nil {
		return nil, fmt.Errorf("re-checking the working tree: %w", err)
	}
	for _, record := range strings.Split(out, "\x00") {
		if len(record) < 4 {
			continue
		}
		x, y, path := record[0], record[1], record[3:]
		switch {
		case x == '?' && y == '?':
			found = append(found, "untracked: "+path)
		case y != ' ':
			found = append(found, "modified on disk: "+path)
		}
	}

	// The index against the commit the rewrite started from.
	staged, _, err := git.Run(ctx, "diff-index", "--cached", "--name-only", "--no-renames", "--ignore-submodules=all", baseline, "--")
	if err != nil {
		return nil, fmt.Errorf("re-checking the index against %s: %w", shortSHA(baseline), err)
	}
	for _, path := range git.SplitNonEmpty(staged) {
		found = append(found, "staged: "+path)
	}

	sort.Strings(found)
	return found, nil
}
