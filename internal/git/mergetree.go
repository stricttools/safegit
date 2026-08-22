package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/smm-h/safegit/internal/gitversion"
)

// MergeTreeResult is what one `git merge-tree --write-tree` computed.
type MergeTreeResult struct {
	// Tree is the resulting tree object. It exists whether or not the merge
	// conflicted: a conflicted merge's tree holds the marker-carrying files git
	// would have written into the working tree.
	Tree string
	// Conflicted reports that the merge did not resolve cleanly.
	Conflicted bool
	// Paths are the paths git left unmerged, in git's own order, each named
	// once however many stages it has.
	Paths []string
}

// MergeTree computes a three-way merge into the object store, touching neither
// the index nor the working tree.
//
// It is the engine behind the honest `--dry-run` of merge, cherry-pick and
// revert: replaying the operation this way gives the REAL answer (clean or
// conflicted, and which paths) instead of a guess, and the objects it writes go
// into the preview's quarantine and away with it.
//
// base is the merge base, and passing it is what makes a cherry-pick or a
// revert expressible as a merge -- the operation's whole difference from a
// branch merge is which commit stands as the base and which stands as the
// incoming side. Empty means "let git find the merge base itself", which is the
// branch-merge case.
//
// The version floor is checked here rather than at each caller:
// `--write-tree` is git 2.38, and on an older git the flag does not exist at
// all, so an unchecked call would fail with git's usage text instead of a
// sentence naming the floor.
func MergeTree(ctx context.Context, base, ours, theirs string, extra ...string) (MergeTreeResult, error) {
	if err := RequireFeature(ctx, gitversion.MergeTreeWriteTree); err != nil {
		return MergeTreeResult{}, err
	}

	args := []string{"merge-tree", "--write-tree", "-z", "--no-messages"}
	if base != "" {
		args = append(args, "--merge-base="+base)
	}
	args = append(args, extra...)
	args = append(args, ours, theirs)

	stdout, stderr, runErr := RunWithEnv(ctx, nil, args...)
	conflicted := false
	if runErr != nil {
		var exitErr *exec.ExitError
		// merge-tree exits 1 for a conflicted merge, which is an ANSWER rather
		// than a failure; anything else is a failure and carries git's own
		// message.
		if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 {
			conflicted = true
		} else {
			return MergeTreeResult{}, fmt.Errorf("computing the merge with git merge-tree: %w: %s", runErr, strings.TrimSpace(stderr))
		}
	}

	result, err := parseMergeTree(stdout)
	if err != nil {
		return MergeTreeResult{}, err
	}
	result.Conflicted = conflicted
	return result, nil
}

// parseMergeTree reads `merge-tree --write-tree -z --no-messages` output: one
// NUL-terminated record holding the tree, then one record per unmerged index
// slot, "<mode> <object> <stage>\t<path>".
//
// --no-messages is what keeps this parse total: with messages, the records
// after the conflicted-file section are free-form text in a shape that differs
// per message kind, and safegit reports the paths rather than git's prose.
func parseMergeTree(out string) (MergeTreeResult, error) {
	records := strings.Split(out, "\x00")
	var result MergeTreeResult
	seen := make(map[string]bool)

	for i, record := range records {
		if record == "" {
			continue
		}
		if i == 0 {
			result.Tree = strings.TrimSpace(record)
			continue
		}
		meta, path, found := strings.Cut(record, "\t")
		if !found {
			return MergeTreeResult{}, fmt.Errorf("unparseable merge-tree record (no path separator): %q", record)
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			return MergeTreeResult{}, fmt.Errorf("unparseable merge-tree record (want mode, object, stage): %q", record)
		}
		if _, err := strconv.Atoi(fields[2]); err != nil {
			return MergeTreeResult{}, fmt.Errorf("unparseable stage in merge-tree record %q: %w", record, err)
		}
		if !seen[path] {
			seen[path] = true
			result.Paths = append(result.Paths, path)
		}
	}
	if result.Tree == "" {
		return MergeTreeResult{}, fmt.Errorf("git merge-tree produced no tree")
	}
	return result, nil
}
