# safegit commit fails on saferm-staged deletions when given a directory path

## Context

Observed during a real fleet operation: a session deleted ~45 files under a directory via
`saferm delete -r` (which pre-stages the deletions in the git index), then ran a commit of
that directory path (via a wrapper that delegates to `safegit commit ... -- <dir>/`).

## Problem

`safegit commit` with a *directory* path argument failed with exit 128. The apparent
mechanism: safegit's move/deletion detection re-ran `git rm` (or equivalent staging) on
paths that were already deleted from the working tree and already staged as deletions by
saferm — git then errored on the already-gone paths.

Workaround that succeeded: passing the explicit *file* paths (not the directory) to
`safegit commit` directly.

## Expected

Committing saferm-staged deletions should work identically whether the paths are given as
individual files or as a containing directory — directory arguments should expand against
the index state (including staged deletions), not just the working tree.

## Repro sketch

1. In a repo, create `dir/a.txt` + `dir/b.txt`, commit.
2. `saferm delete -r --description "test" dir/`
3. `safegit commit -m "remove dir" -- dir/`
4. Observe exit 128 (expected: a commit containing both deletions).

## Effort

Small-medium: adjust path expansion / staging logic to tolerate already-staged deletions;
red-green with the repro above.
