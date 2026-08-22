# Three small defects from the 0.26.0 visit

Found during the 2026-08-07 visit; none fixed there (out of scope). Split this
file if the items get picked up separately.

## 1. `hook install <path>` can install an undiscoverable hook

`hooks.PlanInstall` copies the source under its own basename into the hooks
directory, but `hooks.Discover` only recognizes `pre-pre-push` or entries
under `pre-pre-push.d/`. `safegit hook install my-check.sh` reports success
and the hook never runs — a silent no-op. Fix: install into the discoverable
namespace (rename into `pre-pre-push.d/` by default) or hard-error on a
basename discovery cannot see. Red-green.

## 2. Hooks-directory docs drift

`CLAUDE.md`/docs say hooks live under `.git/safegit/hooks`; the code uses
`.git/hooks`. One of the two is wrong — verify which location is intended
(the code's, presumably), fix the docs (or the code) and add the path to a
test so prose and code cannot disagree silently.

## 3. `safegit commit -m A -m B` joins paragraphs with `\n`, not `\n\n`

Multiple `-m` values should form subject + body separated by a blank line
(git's own `-m` semantics). safegit joins with a single newline, so
`git log --oneline` renders the whole message as the subject. Cosmetic but
fleet-visible in history. Red-green against git's own `-m -m` output shape.

## Effort

All small; item 1 needs the small design pick (rename vs refuse).
