# A strict scrub mode: refuse while the secret survives on unwalked refs

Deferred by user ruling (2026-08-22, redesign-campaign review): "the
recommended subset now, the rest todo'd for way later."

## Context

As shipped by the 2026-08 campaign, `scrub match`/`scrub run` verify
pattern absence over the tips the rewrite actually walked; when the
pattern ALSO survives on refs the rewrite never touched (another branch,
a stale remote-tracking ref), the rewrite completes and exits 31 with
the surviving refs named, plus an unconditional scope line stating that
other refs were not rewritten and the standing rotation notice.

## The deferred idea

A second, stricter stance offered as a REQUIRED member-spelled selector
(fleet style: force the choice, no default), e.g.:

- `--rewrite-anyway` — today's behavior: complete, exit 31 naming
  survivors elsewhere.
- `--require-clean-elsewhere` — before anything moves, scan every ref
  OUTSIDE the walk for the pattern; any match refuses at exit 30
  listing the refs ("scrub these first"), original history untouched.

## Why it was deferred

The strict mode's user is hypothetical today; the rotation notice is
the true remedy for a pushed secret either way; and the campaign was
converging. The known cost of deferring: introducing a REQUIRED
selector later churns every consumer invocation (acceptable pre-1.0 as
a minor bump with breaking-type changelog entries, but real churn — the
campaign's own test suite carries ~50 scrub invocations).

## Implementation sketch

The pre-ruling accidental implementation already computed most of the
strict half (it scanned unwalked refs' histories via the tips list);
the work is: a ChoiceDecl selector pair on match/run (mirror the
substitution and range selectors), the other-refs scan placed in Tier A
(pre-refs, pre-journal), refusal wording that does NOT call unwalked
objects "rewritten history", tests for both modes, and doc updates.
`scrub file` participation: decide whether a file scrub (path-scoped,
not pattern-scoped) can honestly offer the strict stance at all.

## Effort

Medium: selector + scan placement + wording + ~50 invocation updates +
docs.
