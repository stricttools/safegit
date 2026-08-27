package main

import (
	"context"
	"fmt"
	"os"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
)

// The friendly pre-flight refusals for an UNBORN branch.
//
// An unborn branch -- a repository between `git init` and its first commit, or
// one `safegit undo` of a root commit has emptied -- supports most of what
// safegit does: `commit` roots, `switch` moves, `merge` and `pull`
// fast-forward, `cherry-pick` produces a root commit, `reset` works. Four forms
// cannot be served there, and this file is where each says so BEFORE git runs,
// naming the situation and the way forward, instead of letting the operator
// meet git's own message about something else:
//
//   - `merge --no-ff` and `merge --no-commit` (and `pull --merge-strategy
//     no-ff`, which is the same request through the other command). Both ask
//     for a merge commit onto a first parent that does not exist. git's own
//     words are "Non-fast-forward commit does not make sense into an empty
//     head", which names the flag nobody typed in the `--no-commit` case,
//     because safegit's parked merge is computed with `--no-ff` underneath.
//   - `rebase`, where git says "Could not resolve HEAD to a commit".
//   - `bisect start`, where git says "bad HEAD - strange symbolic ref".
//
// The refusals exit at the general code, which is what every safegit refusal
// with no more specific verdict uses: an unborn branch is not a coordination
// conflict, not a usage error in the command line as typed (the same command
// line is correct one commit later), and not one of the numbered situations the
// exit registry exists to distinguish.
//
// One of the four is a DELIBERATE DIVERGENCE rather than a friendlier wording
// of git's own refusal: raw `git merge --no-commit` into an unborn head
// fast-forwards and exits 0. safegit refuses it, because safegit's `--no-commit`
// PARKS in every case an operator can reach, and parking is computed with
// `--no-ff` -- which an unborn head cannot take. Cataloged in
// docs/divergences.md.

// refuseUnbornMergeForm refuses the merge forms an unborn branch cannot serve,
// and answers 0 for every other command line -- including every command line on
// a born branch, which is what makes it safe to call unconditionally.
//
// noFFFlag and parkFlag are the caller's OWN spelling of the two requests:
// `--no-ff` and `--no-commit` for `safegit merge`, `--merge-strategy no-ff` for
// `safegit pull`, which has no parking form at all. A refusal naming a flag the
// operator did not type is a refusal they cannot act on -- the same rule the
// fast-forward-only refusal beside it follows.
func refuseUnbornMergeForm(ctx context.Context, noFF, park bool, noFFFlag, parkFlag string) int {
	if !noFF && !park {
		return 0
	}
	if !git.HeadIsUnborn(ctx) {
		return 0
	}

	asked, why := noFFFlag, "asks for a merge commit, and a merge commit needs a first parent to record"
	if park {
		asked = parkFlag
		why = "asks for the merge to be computed and left parked, and safegit parks a merge by computing it with --no-ff"
	}
	fmt.Fprintf(os.Stderr, "error: this branch is unborn -- it has no commits yet -- so a merge into it can only be a fast-forward\n")
	fmt.Fprintf(os.Stderr, "  %s %s. git's own words for the attempt are \"Non-fast-forward commit\n", asked, why)
	fmt.Fprintf(os.Stderr, "  does not make sense into an empty head\".\n")
	fmt.Fprintf(os.Stderr, "  Re-run without %s to take the fast-forward, or make a commit on this branch first and\n", asked)
	fmt.Fprintf(os.Stderr, "  then merge. See docs/divergences.md.\n")
	return exitcode.General
}

// refuseUnbornRebase refuses a rebase on an unborn branch.
//
// It is a SEPARATE refusal from refuseRebaseOverAnotherOperation, which is
// about somebody else's in-flight state: this one is about where this branch
// stands, both are asked before git runs, and both stay.
func refuseUnbornRebase(ctx context.Context, upstream string) int {
	if !git.HeadIsUnborn(ctx) {
		return 0
	}
	fmt.Fprintf(os.Stderr, "error: this branch is unborn -- it has no commits yet -- so there is nothing to rebase\n")
	fmt.Fprintf(os.Stderr, "  a rebase replays the commits this branch has that the upstream does not, and this branch\n")
	fmt.Fprintf(os.Stderr, "  has none. git's own message here is \"Could not resolve HEAD to a commit\".\n")
	fmt.Fprintf(os.Stderr, "  To start this branch from %s, fast-forward onto it: safegit merge %s\n",
		unbornUpstreamName(upstream), unbornUpstreamName(upstream))
	return exitcode.General
}

// unbornUpstreamName renders the upstream for the refusal's advice, falling
// back to a placeholder when the command line named none in a readable form.
func unbornUpstreamName(upstream string) string {
	if upstream == "" {
		return "<upstream>"
	}
	return upstream
}

// refuseUnbornBisect refuses `bisect start` on an unborn branch.
//
// Only `start` is refused. Every other subcommand needs a bisect that is
// already running, and one cannot have been started here -- git's answer to
// those is about the missing bisect, which is the true thing to say.
func refuseUnbornBisect(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] != "start" {
		return 0
	}
	if !git.HeadIsUnborn(ctx) {
		return 0
	}
	fmt.Fprintf(os.Stderr, "error: this branch is unborn -- it has no commits yet -- so there is nothing to bisect\n")
	fmt.Fprintf(os.Stderr, "  a bisect searches a range of commits, and this branch has none. git's own message here\n")
	fmt.Fprintf(os.Stderr, "  is \"bad HEAD - strange symbolic ref\", which is about the ref rather than the range.\n")
	fmt.Fprintf(os.Stderr, "  Switch to a branch that has commits first: safegit switch <branch>\n")
	return exitcode.General
}
