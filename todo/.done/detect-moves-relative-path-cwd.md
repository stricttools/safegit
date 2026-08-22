# detect-moves resolves relative path arguments against repo root instead of invoking cwd

## Context

`safegit commit -m "msg" -- <files>` accepts path arguments and runs its
detect-moves logic over them before committing. Like git itself, safegit can be
invoked from any subdirectory of a repository, and git resolves relative path
arguments against the invoking cwd. safegit's detect-moves path resolution does
not.

## Problem

When `safegit commit` is invoked from a repository subdirectory with a relative
path argument, and that path has no pending changes (nothing staged or modified
for it), the detect-moves logic resolves the relative path against the
repository root instead of the invoking cwd. The resulting repo-root-relative
path does not exist in the index, and the underlying `git rm --cached`
invocation fails.

## Reproduction (abstract)

1. In any git repository, create a subdirectory `sub/` containing a tracked,
   committed file `unchanged.txt` and a second tracked file `edited.txt`.
2. Modify only `sub/edited.txt`.
3. From inside `sub/`, run:
   `safegit commit -m "msg" -- edited.txt unchanged.txt`
4. safegit fails with exit code 128. Verbatim error shape:

   ```
   git rm --cached ... did not match any files
   ```

   (the `...` is the relative path as resolved against the repo root, e.g.
   `unchanged.txt` looked up at the root instead of `sub/unchanged.txt`).

5. Retrying the same command WITHOUT the unchanged file succeeds:
   `safegit commit -m "msg" -- edited.txt`

The trigger is the combination of (a) invocation from a subdirectory, (b) a
relative path argument, and (c) that path having no pending changes — files
with pending changes take a different code path and commit fine.

## Expected behavior

Relative path arguments must be resolved against the invoking cwd (exactly as
`git add`/`git commit` do), everywhere in the commit flow — including the
detect-moves logic and any `git rm --cached` calls it issues. A path argument
with no pending changes should be a no-op for move detection (or a clear
"nothing to commit for <path>" report), never an exit-128 failure from a
mis-resolved path.

## Affected component

detect-moves path resolution inside the `safegit commit` flow (the code that
feeds candidate paths to `git rm --cached`).

## Effort estimate

Small: normalize path arguments to repo-relative form using the invoking cwd
(git exposes the prefix via `git rev-parse --show-prefix`) at the entry of the
commit flow, before detect-moves runs. Add a red-green regression test that
commits from a subdirectory with an unchanged relative path argument.
