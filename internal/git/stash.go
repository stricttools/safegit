package git

import (
	"context"
	"fmt"
	"strings"
)

// The two stash operations safegit needs, and the only two it has.
//
// They exist for ONE caller: the merge conclusion, which has to consume the
// autostash `git merge --autostash` (or merge.autoStash, or `git pull
// --autostash`) recorded in .git/MERGE_AUTOSTASH. That file holds the object
// name of a stash-shaped commit -- the operator's uncommitted work, taken out
// of the way before the merge began -- and git's own `merge --continue` puts it
// back by applying that commit and, when the apply fails, storing it as a real
// stash entry so it is still reachable by name.
//
// Neither function creates a stash. safegit never stashes anything of its own:
// the working tree belongs to the operator and to whichever other session shares
// it, and taking their work out from under them is exactly what this tool exists
// to prevent.

// StashApply applies a stash-shaped commit to the working tree and index, the
// way `git stash apply <commit>` does.
//
// It returns git's own combined output, so a caller can show the operator what
// happened, and an error when the apply did not succeed -- most often because
// the stashed change conflicts with what the working tree now holds. A failed
// apply is NOT a no-op: git leaves the conflict in the working tree and the
// index, exactly as it does for a conflicting `git stash apply` an operator ran
// themselves.
func StashApply(ctx context.Context, commit string) (output string, err error) {
	stdout, stderr, err := Run(ctx, "stash", "apply", commit)
	combined := strings.TrimSpace(stdout + stderr)
	if err != nil {
		return combined, fmt.Errorf("applying the stash %s: %w", commit, err)
	}
	return combined, nil
}

// StashStore records an already-existing stash-shaped commit as an entry on
// refs/stash, the way `git stash store` does, without touching the working tree
// or the index.
//
// It is the recovery path for an apply that failed: the commit is real either
// way, but until it is on refs/stash the only name for it is a raw object name
// in a file that is about to be removed. Stored, it is `stash@{0}` and every
// ordinary stash command reaches it.
func StashStore(ctx context.Context, commit, message string) error {
	if _, stderr, err := Run(ctx, "stash", "store", "-m", message, commit); err != nil {
		return fmt.Errorf("storing %s as a stash entry: %w: %s", commit, err, strings.TrimSpace(stderr))
	}
	return nil
}
