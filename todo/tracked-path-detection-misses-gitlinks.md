# Tracked-path detection misses gitlinks (submodule entries)

## Problem

`safegit commit -m "..." -- <paths>` refuses a named path that is a gitlink
(a `160000 commit` tree entry, i.e. a submodule reference) with
`error: file <path> does not exist and is not tracked in <ref>` (exit 11) —
even though `git status --porcelain` reports the path as tracked (e.g. as a
staged/unstaged deletion) and a plain `git commit` handles it fine.

Observed live: a caller enumerated working-tree changes via porcelain, named
them to `safegit commit`, and one of the paths was a deleted submodule entry.
safegit refused the whole commit, leaving the caller's multi-step operation
half-completed.

## Expected

A gitlink path present in the index or the named ref's tree is "tracked" for
the purpose of the tracked-path check, in all its states (present, modified
SHA, deleted). The check should agree with `git ls-files` /
`git ls-tree <ref> -- <path>`, both of which list gitlinks.

## Affected

The tracked-file detection used by the commit path-validation (wherever
"does not exist and is not tracked" is decided). Likely a file-mode or
object-type filter that only accepts blobs.

## Effort

Small-medium: extend the tracked-path predicate to accept gitlink entries +
a regression test committing (1) a submodule addition, (2) a submodule SHA
bump, and (3) a submodule deletion by name.
