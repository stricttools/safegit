// The two refusals a commit makes about state it did not create: the operation
// git has in flight, and the conflict left in the shared index. Both are
// declared pipeline inputs -- a caller that IS the conclusion of the operation
// says so, and every other caller is refused.
package commit

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/smm-h/safegit/internal/coord"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
)

// guardSequencer refuses the operation when git has something in flight that
// the caller has not declared itself the conclusion of.
//
// It lives in the PIPELINE rather than in the command handlers so that every
// route into a commit is covered by one check -- including the submodule
// auto-bump, which reaches the pipeline through a safegit it spawns in the
// parent repository, and every future caller that constructs a request
// directly.
//
// The refusal exits exitcode.CoordinationBusy: an operation owns this working
// tree and safegit will not write over it. That is the same verdict, and the
// same code, the passthrough guard gives for the same reason.
//
// A state that cannot be read is also a refusal. A MERGE_HEAD safegit cannot
// parse is not evidence that no merge is in flight, and the dangerous direction
// is the permissive one: a commit built against a repository git considers
// mid-merge silently drops the merge's second parent and every path the
// pathspec does not name.
// git.GitDir answers ABSOLUTELY, which this check depends on: GuardInFlight
// probes state files with Go filesystem calls, and a relative git directory
// would resolve against the process working directory rather than against the
// repository -- so from a subdirectory the probe would find nothing and the
// refusal would silently switch itself off.
func guardSequencer(ctx context.Context, declared *coord.SequencerContext, operation string) error {
	gitDir, err := git.GitDir(ctx)
	if err != nil {
		return fmt.Errorf("resolving git dir: %w", err)
	}
	if err := coord.GuardInFlight(gitDir, operation, declared); err != nil {
		return &CommitError{Code: exitcode.CoordinationBusy, Message: err.Error(), Err: err}
	}
	return nil
}

// concludesInFlightOperation reports whether a request is the conclusion of an
// operation git has in flight, which is the ONE thing that exempts a commit from
// the unmerged-index refusal below.
//
// Two independent declarations say it, and either one is enough: the sequencer
// context, which names the operation being concluded, and the shared-index base,
// which says the commit's content IS what that index holds. The conclusion path
// sets both; asking for either keeps the exemption from turning on a single
// field that a future caller might set for an unrelated reason.
func concludesInFlightOperation(declared *coord.SequencerContext, base IndexBase) bool {
	return declared != nil || base == IndexBaseSharedIndex
}

// guardUnmergedIndex refuses when the repository's SHARED index still carries
// unmerged entries and this commit is not the conclusion that resolves them.
//
// PARITY, deliberately carrying NO divergences entry: this refusal makes safegit
// agree with git, and the catalog records the places the two disagree. safegit's
// own mid-operation refusal reads git's STATE FILES, so a repository whose state
// files are gone -- a crashed operation, a hand-deleted MERGE_HEAD, one of the
// several ways `git merge --abort` leaves half its work behind -- reads as idle
// while the index still holds stage 1/2/3 entries. git refuses every commit in
// that repository; without this guard safegit committed the marker-laden working
// tree over it and reported success. The parity is deliberate.
//
// It reads the shared index (an empty index path), not the commit's temporary
// one: the temporary index is seeded from the parent tree and is quiet by
// construction, so the fact this refusal is about is only visible in the index
// git left behind.
func guardUnmergedIndex(ctx context.Context, declared *coord.SequencerContext, base IndexBase) error {
	if concludesInFlightOperation(declared, base) {
		return nil
	}
	entries, err := git.UnmergedStages(ctx, "")
	if err != nil {
		return fmt.Errorf("reading the index's unmerged entries: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}

	seen := map[string]bool{}
	var paths []string
	for _, e := range entries {
		if !seen[e.Path] {
			seen[e.Path] = true
			paths = append(paths, "  "+e.Path)
		}
	}
	sort.Strings(paths)

	return &CommitError{
		Code: exitcode.UnmergedIndex,
		Message: fmt.Sprintf("the index carries unmerged entries, so no commit can be built beside them:\n%s\n"+
			"  git refuses every commit in this state (\"Committing is not possible because you have\n"+
			"  unmerged files\") and so does safegit: the commit would record a conflict nobody\n"+
			"  resolved. Resolve each path and stage it, or run `safegit doctor --action fix`, which\n"+
			"  re-stages the working tree's own content when no operation is in flight.",
			strings.Join(paths, "\n")),
	}
}
