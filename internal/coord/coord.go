// Package coord implements the coordination layer that prevents concurrent agents from corrupting the working tree by guarding tree-mutating operations.
// It checks whether the working tree is clean before allowing switch, merge, rebase, reset, and pull to proceed.
//
// It also owns the other half of that coordination: what safegit does when git
// itself has an operation in flight. sequencer.Read reports the state and holds
// no policy; this package decides which commands may run against it
// (GuardInFlight) and what the operator is told when one may not (WayOutOf,
// RefuseInFlight). Both refusal paths -- the commit pipeline's and the
// passthrough guard's -- render their advice from here, so they cannot name
// different commands for the same state.
package coord

import (
	"context"
	"fmt"
	"strings"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/sequencer"
)

// DirtyState describes why the working tree is not clean.
type DirtyState struct {
	ModifiedFiles []string // status code + path from git status --porcelain

	// Sequencer is what git had in flight when the check ran. Mid-operation the
	// working tree is dirty by construction -- a conflicted merge writes
	// conflict markers into the tree -- so the ordinary "commit your work"
	// advice is impossible to follow there and the refusal says so instead.
	//
	// It is a fact carried alongside the dirt, not part of the verdict: an
	// in-flight operation does NOT by itself make a clean tree dirty, so a
	// state-control passthrough over a clean tree -- an interactive rebase
	// parked at `edit` or `break`, where `safegit rebase --continue` is the way
	// on -- still runs.
	//
	// What that does not do is relax the check for the usual mid-operation
	// case. A conflicted or parked operation dirties the tree by construction:
	// the conflict markers, or the staged result, ARE the dirt. So `safegit
	// merge --abort` and a `rebase --continue` over staged resolutions are
	// refused at exit 5 like any other dirty-tree command. The refusal changes
	// SHAPE rather than relaxing -- it names the operation in flight and the
	// commands that conclude or abandon it, instead of advice to commit the
	// conflict, which is advice nobody can follow.
	Sequencer sequencer.State
}

// Check inspects the working tree of the repository whose git directory is
// gitDir. Returns nil if clean.
func Check(ctx context.Context, gitDir string) (*DirtyState, error) {
	var ds DirtyState

	state, err := sequencer.Read(gitDir)
	if err != nil {
		return nil, fmt.Errorf("reading git's in-flight operation state: %w", err)
	}
	ds.Sequencer = state

	// 1. Check for tracked modifications by diffing working tree against HEAD directly.
	// This avoids relying on the main .git/index which may be stale after safegit commits.
	stdout, _, err := git.Run(ctx, "diff", "HEAD", "--name-status")
	if err != nil {
		return nil, fmt.Errorf("running git diff HEAD: %w", err)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		ds.ModifiedFiles = append(ds.ModifiedFiles, line)
	}

	// 2. Check for untracked files
	untracked, _, err := git.Run(ctx, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("running git ls-files: %w", err)
	}
	for _, line := range strings.Split(untracked, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ds.ModifiedFiles = append(ds.ModifiedFiles, "? "+line)
	}

	if len(ds.ModifiedFiles) == 0 {
		return nil, nil
	}
	return &ds, nil
}

// Refuse formats a refusal message from a DirtyState.
//
// The advice depends on WHY the tree is dirty. Ordinarily the dirt is the
// operator's own uncommitted work and committing it is the way forward. While
// git has an operation in flight the same dirt is the operation's conflict
// markers and staged result: committing it is exactly what safegit refuses to
// do (it would drop the operation's other parent and everything the pathspec
// does not name), so the message names the operation and the command that ends
// it instead of advice no one can follow.
func (d *DirtyState) Refuse(operation string) string {
	var b strings.Builder

	if d.Sequencer.InProgress() {
		fmt.Fprintf(&b, "safegit: %s\n", RefuseInFlight(operation, d.Sequencer))
		d.writeModifiedFiles(&b)
		return b.String()
	}

	fmt.Fprintf(&b, "safegit: working tree is not clean; refusing %s to avoid clobbering uncommitted work.\n", operation)
	d.writeModifiedFiles(&b)

	b.WriteString("\nSuggestion:\n")
	b.WriteString("  safegit commit -m \"<msg>\" -- <files>\n")

	return b.String()
}

// writeModifiedFiles appends the dirty-path listing both refusal shapes carry.
func (d *DirtyState) writeModifiedFiles(b *strings.Builder) {
	if len(d.ModifiedFiles) == 0 {
		return
	}
	b.WriteString("\nModified files:\n")
	for _, f := range d.ModifiedFiles {
		fmt.Fprintf(b, "  %s\n", f)
	}
}
