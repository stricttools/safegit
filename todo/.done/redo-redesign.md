# Redo: fix or remove

## Problem

Redo is broken on root commits, with the same pattern as undo plus additional failures:

- `redo.go:62` has the same `targetSHA == ""` rejection as undo
- `TipSHA` extraction from the undo oplog entry returns `""`, causing redo to fail before it even reaches the ref update
- After undo deletes the branch ref, redo needs to recreate it, but `UpdateRef` doesn't support ref creation (needs zero-hash as old-value)

## Design question: should redo exist at all?

Alternative: `undo --count N` to undo multiple steps, and if you undo too far, just re-commit. This would simplify the system:

- The oplog no longer needs forward/backward entry pairs
- No need for the one-shot redo guard (which prevents oscillation)
- Fewer oplog-consuming code paths to maintain
- Root commit support only needs to be fixed in undo, not in both undo and redo

If redo is kept, it needs the same RefMutation/IndexState foundation as the undo fix (ref creation, empty index state).

## References

- Investigation notes: `todo/root-commit-support-notes.md`
- Split from: `todo/.obsolete/root-commit-support.md`
