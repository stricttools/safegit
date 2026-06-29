# Root commit support in amend and undo

## Problem

Two operations fail on root commits (first commit in a repo, no parent):

1. `safegit commit --amend` fails with: `cannot amend: refs/heads/main is a root commit (no parent)`
2. `safegit undo` fails with: `oplog entry for "commit" is missing "parent" field`

## Root cause

### amend

`internal/commit/amend.go` lines 123-127 explicitly reject root commits:
```go
parentSHA, err := git.RevParse(ctx, ref+"^")
if err != nil {
    return nil, false, fmt.Errorf("cannot amend: %s is a root commit (no parent)", ref)
}
```

The `reword` operation in the same file (lines 336-339) already handles root commits correctly by catching the error and setting `parentSHA = ""`. The `git.CommitTree` function (`internal/git/git.go` lines 130-140) already handles empty parent SHA by omitting the `-p` flag. The infrastructure supports root commits -- this code path just doesn't use it.

### undo

`undo.go` lines 80-84 treat empty parent SHA as a missing field:
```go
targetSHA, ok := entry.Extra[targetKey].(string)
if !ok || targetSHA == "" {
    die(...)
}
```

For root commits, the oplog correctly records `"parent": ""` (empty string), but the undo code rejects empty strings. For root commit undo, the correct behavior is to delete the branch ref (since there's no parent to reset to), restoring the repo to its pre-first-commit state.

## Fix

### amend

Match the pattern from `reword` -- catch the RevParse error and set `parentSHA = ""` instead of returning an error.

### undo

Distinguish between missing fields (`!ok`) and legitimate empty values (`targetSHA == ""`). When undoing a root commit, delete the branch ref via `git update-ref -d`.
