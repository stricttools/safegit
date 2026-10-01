package main

import (
	"context"
	"fmt"

	"github.com/stricttools/safegit/internal/exitcode"
	"github.com/stricttools/safegit/internal/git"
)

// The friendly pre-flight refusals for an UNBORN branch.
//
// An unborn branch -- a repository between `git init` and its first commit, or
// one `safegit undo` of a root commit has emptied -- supports most of what
// safegit does: `commit` roots, `switch` moves, `merge` and `pull`
// fast-forward, `cherry-pick` produces a root commit, `reset` works. The forms
// that cannot be served there are these, and each says so here BEFORE git runs,
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
// One of them is a DELIBERATE DIVERGENCE rather than a friendlier wording
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
// `safegit pull`, which has no parking form at all. wayOut is the same thing
// for the advice, because "re-run without that flag" is not how a pull asks for
// a fast-forward -- its merge strategy is a required declaration. A refusal
// naming a flag the operator did not type, or advice they cannot follow, is a
// refusal they cannot act on -- the same rule the fast-forward-only refusal
// beside it follows.
func refuseUnbornMergeForm(flags globalFlags, ctx context.Context, noFF, park bool, noFFFlag, parkFlag, wayOut string) int {
	if !noFF && !park {
		return 0
	}
	if !git.HeadIsUnborn(ctx) {
		return 0
	}

	asked := noFFFlag
	why := []string{
		"asks for a merge commit, and a merge commit needs a first parent to record.",
	}
	if park {
		asked = parkFlag
		why = []string{
			"asks for the merge to be computed and left parked, and safegit parks a",
			"merge by computing it with --no-ff underneath.",
		}
	}
	report := fmt.Sprintf("this branch is unborn -- it has no commits yet -- so a merge into it can only be a fast-forward\n"+
		"  %s %s", asked, why[0])
	for _, line := range why[1:] {
		report += "\n  " + line
	}
	errorf(flags, "%s\n"+
		"  git's own words for the attempt: \"Non-fast-forward commit does not make sense into an\n"+
		"  empty head\".\n"+
		"%s"+
		"  safegit implements a deliberate subset of git; see docs/divergences.md.", report, wayOut)
	return exitcode.General
}

// The way-out lines the two callers give refuseUnbornMergeForm. They are
// constants rather than literals at the call sites because `merge` has two of
// those -- its real run and its preview -- and a preview that gave different
// advice from the run it previews would be a second answer to one question.
const (
	unbornMergeWayOut = "  Take the fast-forward instead by leaving that flag off, or make a commit on this branch\n  first and then merge.\n"
	unbornPullWayOut  = "  Take the fast-forward instead with --merge-strategy ff, or make a commit on this branch\n  first and then pull.\n"
)

// refuseUnbornRebase refuses a rebase on an unborn branch.
//
// It is a SEPARATE refusal from refuseRebaseOverAnotherOperation, which is
// about somebody else's in-flight state: this one is about where this branch
// stands, both are asked before git runs, and both stay.
func refuseUnbornRebase(flags globalFlags, ctx context.Context, upstream string) int {
	if !git.HeadIsUnborn(ctx) {
		return 0
	}
	errorf(flags, "this branch is unborn -- it has no commits yet -- so there is nothing to rebase\n"+
		"  a rebase replays the commits this branch has that the upstream does not, and this branch\n"+
		"  has none. git's own message here is \"Could not resolve HEAD to a commit\".\n"+
		"  To start this branch from %s, fast-forward onto it: safegit merge %s", unbornUpstreamName(upstream), unbornUpstreamName(upstream))
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
func refuseUnbornBisect(flags globalFlags, ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] != "start" {
		return 0
	}
	if !git.HeadIsUnborn(ctx) {
		return 0
	}
	errorf(flags, "this branch is unborn -- it has no commits yet -- so there is nothing to bisect\n"+
		"  a bisect searches a range of commits, and this branch has none. git's own message here\n"+
		"  is \"bad HEAD - strange symbolic ref\", which is about the ref rather than the range.\n"+
		"  Switch to a branch that has commits first: safegit switch <branch>")
	return exitcode.General
}
