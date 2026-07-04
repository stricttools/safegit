# Fix undo for root commits

## Problem

`safegit undo` crashes on root commits. In `undo.go`, the empty parent SHA recorded in the oplog for root commits is rejected by the `targetSHA == ""` check (lines 80-84). The oplog correctly records `"parent": ""` for root commits, but the undo code treats empty strings as missing fields.

## What needs to happen

- Distinguish between missing fields (`!ok`) and legitimate empty values (`targetSHA == ""`) in `undo.go`
- When undoing a root commit, delete the branch ref via `DeleteRef` (since there's no parent to reset to), restoring the repo to its pre-first-commit state
- Use `ReadTreeEmpty` (or `git read-tree --empty`) to clear the index after deleting the branch ref, matching `git init` state rather than leaving a stale index

## Foundation

The RewriteResult architecture (RefMutation, IndexState) provides the foundation for this fix:

- `RefMutation` as a typed enum (Create/Update/Delete) makes root commit support trivial -- deleting a ref on undo and creating it on redo are just two more mutation types
- `IndexState` (`IndexFromTree{SHA}`, `IndexEmpty`) makes the index sync after root-commit undo type-safe instead of string-based

## Audit scope

The `targetSHA == ""` pattern should be audited across all oplog-consuming code paths, not just undo.

## References

- Investigation notes: `todo/root-commit-support-notes.md`
- Split from: `todo/.obsolete/root-commit-support.md`
