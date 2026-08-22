// The mid-operation refusal, as a declared pipeline input.
package commit

import (
	"context"
	"fmt"

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
