# Export the git verb table so other programs can import safegit's knowledge of git

## Context

On 2026-09-15 a design round decided that a Go runtime enforcing declared
effects will bound child processes by an argv vocabulary owned by the
program that knows the child best, and that git's vocabulary is safegit's
to own: safegit already classifies every git verb it will run by the
effects the invocation has on a repository. Other programs that run git —
release tooling, documentation generators, anything that calls `git log` or
`git status` — would import that table rather than each keeping a private
list of which git commands are observe-only.

Line references are as of 2026-09-15; verify before acting.

## What exists

`internal/gitexec/classify.go` already is the table: an `Effect` bit set
(objects, refs, index, worktree, remote), a `Verb` record with conditional
effects per flag, the `verbs` slice, `Lookup`, `EffectsOf(args)`,
`WritesObjects`, `WritesWorktree`, `IsObserveOnly` and `ObservePrefixes()`.
It is under `internal/`, so no module outside safegit can import it.

## Problem

A second program that wants to know whether `git rev-parse --show-toplevel`
writes anything has to reinvent the classification, and its copy drifts from
safegit's the first time either changes. The subprocess inventory that
prompted this found git the most-run child across the fleet's Go tools, with
each caller carrying its own implicit assumption about which verbs are
reads.

## Fix

Move the classification to an exported package — `pkg/gitverbs` or the
module's root package, whichever the module already uses for its public
surface — with the same API and the same table, and have safegit's own
callers import it from there. The package is pure data plus pure functions
over argv: no execution, no repository access, so it carries no dependency
on the rest of safegit. Export exactly what an external caller needs:

- `Verbs()`, `Lookup(name)`, `EffectsOf(args)`, `IsObserveOnly(args)`,
  `ObservePrefixes()`.
- The `Effect` type and its constants, so a caller can ask `Has(Remote)`.

Keep `Validate(door, args)` and anything that mentions safegit's own doors
internal; the door concept is safegit's, the verb table is git's.

Red-green: a test in the exported package that asserts every verb safegit
itself invokes anywhere in its source (found by the existing argv-scanning
test, `exit_site_guard_test.go` or its sibling) is present in the table, so
the export cannot lag safegit's own usage; and a compile-only test module
outside the main module that imports the package, proving it is reachable
without `internal/`.

## Affected

`internal/gitexec/classify.go` (moved), every importer under the root
package and `internal/` (`scrub_preview.go`, `switch_cmd.go`, `backup.go`
and the rest), the docs page that lists the verb table if one is generated
from it.

## Effort

Small: a package move with an import rewrite and two tests.
