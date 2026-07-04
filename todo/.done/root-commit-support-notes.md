# Root commit support: investigation notes

Supplementary to `root-commit-support.md`. Documents findings from a code investigation session.

## Amend is already fixed

The original todo says amend fails on root commits. This is no longer true. `internal/commit/amend.go:123-128` already catches the `RevParse` error for `ref+"^"` and sets `parentSHA = ""`, matching the pattern from `reword`. There is a passing `TestAmendRootCommit` in `internal/test/amend_test.go`.

## Redo is also broken

The todo only mentions undo, but `redo.go:62` has the same `targetSHA == ""` rejection. Additionally:
- `TipSHA` extraction from the undo oplog entry returns `""`, causing redo to fail
- After undo deletes the branch ref, redo needs to recreate it, but `UpdateRef` doesn't support ref creation (needs zero-hash as old-value)
- `SyncMainIndex` with the restored SHA should work (non-empty), but needs verification

## Broader design questions raised

- Should redo exist at all? Alternative: undo with `--count n`, and if you undo too far, just re-commit. This would simplify the oplog (no forward/backward pairs needed).
- The `RefMutation` value type approach (Create/Update/Delete as a typed enum) would make root commit support trivial -- `Delete` and `Create` are just two more mutation types.
- A unified `IndexState` type (`IndexFromTree{SHA}`, `IndexEmpty`) would make the index sync after root-commit undo type-safe instead of string-based.
- After deleting the branch ref for root commit undo, `git read-tree --empty` should clear the index (matching `git init` state), not skip the sync (which leaves a stale index).

## Audit scope

The `targetSHA == ""` pattern should be audited across all oplog-consuming code paths, not just undo and redo. The CLAUDE.md rule: "investigate sibling functions, nearby files, and analogous code paths for the same pattern."

## Decision: deprioritized

This todo is shelved for now. The RewriteResult architecture (being built for the push-hint fix) will provide the foundation (RefMutation, IndexState) that makes root commit support straightforward when revisited.
