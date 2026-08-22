# No way to untrack files that are now gitignored

## Context

A consumer repo had accidentally committed build artifacts (`__pycache__/*.pyc`). The
cleanup workflow every git user knows is: add the pattern to `.gitignore`, `git rm -r
--cached` the tracked copies, commit both. Under the safegit discipline ("never bypass
safegit with raw git"), this workflow is currently impossible to complete.

## Problem

Reproduction (any repo with tracked files that later become ignored):

1. `printf '__pycache__/\n' >> .gitignore`
2. `git rm -r --cached <dir>/__pycache__` (stages the deletions)
3. `safegit commit -m "untrack pycache" -- .gitignore <dir>/__pycache__`
   fails with: `error: file <dir>/__pycache__/<name>.pyc is gitignored`
   (check at `internal/commit/commit.go:389`)
4. Retrying with only the non-ignored path — `safegit commit -m "..." -- .gitignore` —
   commits `.gitignore` but the pre-staged deletions from step 2 are gone afterwards
   (`git status` shows the files back to tracked-and-modified, not staged-deleted).

Net result: the files stay tracked forever. The gitignored-path check is the right
guardrail for *adding* content, but it also blocks *removals from tracking*, where
"this path is gitignored" is not a mistake — it is the entire point of the operation.

Step 4 is worth a look of its own while in the area: if pre-staged index state outside
the pathspec is intentionally rebuilt/dropped by safegit's commit flow, that may be by
design (safegit owns the index), but silently discarding a user's staged deletions is
surprising; if it is not by design, it is a second bug. Split this file if the
maintainers want to track it separately.

## Solutions

### A. `safegit untrack <paths>` subcommand (recommended)

Removes paths from the index (equivalent of `git rm -r --cached`), requires them to be
currently tracked, and commits the removal with a provided `-m`. Explicitly permitted
to target gitignored paths — untracking ignored files is the canonical use.

- Pros: explicit intent, keeps the commit-path guardrail fully intact, self-documenting
  audit trail ("untrack" in history), symmetric with the tool's philosophy of
  verbose-over-terse operations.
- Cons: new command surface; needs its own concurrency handling consistent with commit.

### B. Allow gitignored paths in `safegit commit` when their effect is removal

At the check in `internal/commit/commit.go:389`, permit a gitignored path iff the
resulting change for that path is a deletion from the index (file may still exist on
disk). Blocks adding ignored content, permits untracking it.

- Pros: no new surface; fixes the semantic inconsistency directly.
- Cons: subtler rule; "commit" doing rm-cached semantics implicitly is less discoverable
  than a dedicated verb; harder to explain in error messages.

### C. Document the limitation only

- Pros: zero code.
- Cons: leaves no tool-mediated path for a routine workflow; consumers under the
  "never bypass with raw git" rule are stuck, which in practice pressures them into
  raw git — the exact outcome safegit exists to prevent.

## Affected files

- `internal/commit/commit.go` (the gitignored check at ~line 389; commit flow's index
  handling for the step-4 observation)
- New command wiring if solution A (CLI registration, docs, tests)

## Effort estimate

Small. A: ~a day with tests. B: hours, plus careful tests around the deletion
detection. Either should include a regression test for the step-4 staged-state
behavior, whatever the intended semantics are.
