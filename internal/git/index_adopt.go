package git

import (
	"context"
	"fmt"
	"strings"
)

// AdoptIndexFrom makes the shared .git/index hold exactly what the index file
// at srcIndexPath holds, unmerged stages included.
//
// It answers a different question from ReconcileMainIndex, which is why it is a
// separate function rather than another mode of it. ReconcileMainIndex is asked
// "the ref moved; rebuild the shared index and keep whatever the pre-operation
// tip does not explain", and it deliberately PRESERVES unmerged stages, because
// a conflict another session is resolving has to survive somebody else's
// commit. A conclusion DELEGATED to git is not that question: safegit hands git
// an index copy as THE index for the operation (GIT_INDEX_FILE), git commits
// from it, advances its queue and stops, so whatever that file holds afterwards
// is what this repository's index now means. Two concrete readings follow, and
// both are wrong under the reconcile contract:
//
//   - after git finishes the queue, the stages still sitting in the shared
//     index are this operation's own, already-answered conflict; preserving
//     them would leave the repository reporting a conflict git resolved;
//   - when git stops at the NEXT conflict of a queue, the new stages exist
//     ONLY in the copy, so rebuilding the shared index from HEAD would hide a
//     conflict that is genuinely in flight.
//
// The adoption is one `read-tree --empty` followed by one
// `update-index --index-info` batch. No removal lines are needed: the empty
// read-tree leaves no stage-0 entry for an unmerged path to collide with.
// skip-worktree flags are re-applied afterwards, since they live in the index
// and not in any tree.
//
// Every failure is HARD, for the same reason ReconcileMainIndex's are: a
// half-adopted index is a corrupted view of the repository, and reporting that
// as a warning is exactly how staged state disappears in silence.
func AdoptIndexFrom(ctx context.Context, srcIndexPath string) error {
	entries, err := IndexEntries(ctx, srcIndexPath)
	if err != nil {
		return fmt.Errorf("reading the index git was given (%s): %w", srcIndexPath, err)
	}
	skipWorktree, err := ListSkipWorktreeFiles(ctx)
	if err != nil {
		return fmt.Errorf("reading skip-worktree flags before adopting %s: %w", srcIndexPath, err)
	}

	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, indexInfoLine(indexSlot{Mode: e.Mode, SHA: e.SHA, Stage: e.Stage, Path: e.Path}))
	}

	if _, _, err := Run(ctx, "read-tree", "--empty"); err != nil {
		return fmt.Errorf("clearing the shared index before adopting %s: %w", srcIndexPath, err)
	}
	if len(lines) > 0 {
		stdin := []byte(strings.Join(lines, ""))
		if _, _, err := RunWithEnvStdin(ctx, nil, stdin, "update-index", "--index-info"); err != nil {
			return fmt.Errorf("writing %d entries of %s into the shared index "+
				"(the shared index no longer describes the repository): %w", len(lines), srcIndexPath, err)
		}
	}

	return restoreSkipWorktree(ctx, skipWorktree)
}
