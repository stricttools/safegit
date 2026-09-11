# `--moved 'old -> new'` records the move but leaves the old path in the tree

## Context

`safegit commit --moved 'old -> new' -- new` is the documented way to declare
that content moved. In practice the old path, which is gone from disk, is not
removed from the index unless it is also named as a file argument, so the
resulting commit carries both the new files and the untouched old ones. The
next `--amend` that names the old path repairs it, and re-declaring the same
move is refused as already recorded, which is correct but leaves no obvious
path back.

## Problem

A move declaration that does not remove the old path produces a commit that
contradicts its own `Moved:` trailer: the trailer says the content moved,
the tree says it was copied. Callers only notice when they inspect the tree.

## Solutions

1. When `--moved` names an old path that is tracked in the parent and absent
   on disk, stage its removal as part of the same commit, without requiring
   it as a file argument. Most correct: the declaration already asserts
   exactly that state.
   - Pros: one invocation, the commit matches its trailer.
   - Cons: none identified; the precondition (old path gone from disk) is
     already checked.
2. Refuse the commit when a declared old path is still tracked and not named
   as a file argument, naming the remedy. Safer than silently recording a
   copy, but pushes the extra argument onto every caller.

Option 1 is the recommendation, with a test that the old path is absent from
the committed tree and a test that `git mv` before the commit is not needed.

## Affected files

- the commit command's move handling and its tests
- docs for `--moved`

## Effort

Small.
