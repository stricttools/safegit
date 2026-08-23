# Pipeline-authored rebase (non-interactive subset)

## Context

The single-authorship restructure (see `todo/campaign2-plan.md`) makes every
commit safegit creates pipeline-authored: trailered, undoable, covered by the
commit-msg hook, recorded in the oplog. One deliberate exception remains:
`safegit rebase` stays a guarded passthrough where git performs the replay and
git authors every replayed commit. The exception is uniform (every rebase
commit is git's, always -- no run-time split) and documented in
`docs/divergences.md`, but it is still the one door through which git-authored
commits enter a safegit-managed repository.

## Problem

A rebase is an engine, not a commit: it replays N commits onto a moving base,
committing as it goes, with no "compute but do not commit" seam to intercept.
Pipeline-authoring it therefore means safegit owning the whole replay loop,
including stopping at any step's conflict and resuming later -- which requires
persistent, tool-owned plan state ("step 2 of 3, onto X"). Until that exists,
rebased commits carry no trailers, are invisible to `safegit undo`, and skip
the commit-msg hook.

## Proposed solution

Implement rebase natively as safegit's own replay loop, scoped by the subset
law (see "The subset law" in CLAUDE.md):

- **Scope: non-interactive rebase only.** `safegit rebase <upstream>` (and
  possibly `--onto`). Interactive rebase (`-i` and its pick/squash/fixup/
  reword/edit/drop/exec protocol), `--autosquash`, `--exec`, and the apply
  backend are refused outright, each with a divergences entry. The interactive
  protocol serves humans doing curation; agents rewrite history via amend and
  scrub.
- **Mechanism:** resolve the ordered commit list (`upstream..HEAD`); for each
  commit, compute the pick via `merge-tree --write-tree --merge-base` and
  commit through the pipeline with the original author preserved (the
  cherry-pick authorship rule); a conflicting step stops the loop and is
  concluded through the existing conclusion machinery; a tool-owned plan file
  records progress so stop/resume works and git never sees a rebase in
  progress. The final step moves the branch ref by CAS.
- **Recorded feasibility facts:** probe tests already pin that git's own
  `rebase --continue` honors a substituted `GIT_INDEX_FILE`
  (internal/sequencer probes); the native loop does not delegate to git, but
  the probes bound what a hybrid could do. The multi-commit cherry-pick loop
  built by the restructure is the direct template for the replay loop.
- **The same plan-state machinery is the natural future host** for computing
  merges into a tool-owned plan (so git never sees a merge in progress
  either), an idea recorded as a declared destination during the first
  redesign campaign.

## Alternatives considered

1. **Keep the passthrough (status quo).** No work; one authorship exception
   forever, clearly documented. This is the accepted interim state.
2. **Hybrid: native when simple, git when interactive.** Rejected: a run-time
   authorship split behind one command name is exactly the silent-split
   disease the restructure removed.
3. **Full reimplementation including interactive rebase.** Rejected under the
   subset law: owning the `-i` protocol is a product of its own and serves
   human curation workflows, not agents.
4. **Drop rebase entirely.** Not taken without evidence of zero real usage;
   sessions do use the guarded rebase.

## Affected files

- `main.go` (rebase registration: passthrough becomes a real command with a
  refused-flag set)
- a new package (or an extension of `internal/sequencer`) for the tool-owned
  replay plan state
- `internal/commit` (pipeline reuse; author-preserving commits already exist)
- the conclusion machinery (per-step conflict stops)
- `internal/gitexec` classification rows; `internal/exitcode` (new refusals)
- `docs/divergences.md` (the rebase-is-git's entry retires; refusal entries
  for the omitted interactive surface arrive)

## Effort

Large -- a campaign phase of its own. The replay loop itself reuses existing
machinery (merge-tree computation, pipeline commits, conclusions), but the
persistent plan state, stop/resume semantics, and the empty/already-upstream
commit stances are new design surface.

## Preconditions

The single-authorship restructure (campaign 2) must be shipped first: it
provides the single-pick loop, the author-preserving pipeline path, and the
front-door refusal idiom this builds on.
