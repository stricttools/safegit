# `safegit commit` silently drops symlinks (and miscounts committed files)

## Context

Git supports committing symlinks as first-class objects (mode 120000, blob = link target path). `safegit commit -m "..." -- <files>` is the mandated commit path for all sessions, so anything it cannot commit is effectively uncommittable.

## Problem

Two related defects, observed in a consumer project when committing a relative symlink (`<pkg>/assets -> ../assets`):

1. **Symlinks are resolved away before staging.** `internal/commit/commit.go` runs `resolveSymlinks()` (via `filepath.EvalSymlinks`) on every file spec. A symlink argument collapses to its *target* path; if the target's content is already committed, the commit fails with "nothing to commit (tree unchanged)" even though the symlink itself is a new, uncommitted object. There is no way to commit a symlink through safegit at all.
2. **The success message lies about the file count.** In a mixed commit (N regular files + 1 symlink), the symlink is silently dropped from the staged set but the output still reports "N+1 file(s) committed" — the count uses `len(files)` (the input spec list), not the actually-committed set. The caller has no signal that anything was dropped.

Consequence in practice: an agent following the rules cannot ship a symlink-based layout and, worse, a mixed commit *appears* to have fully succeeded. The consumer project had to redesign around the missing symlink.

## Solutions

1. **Support symlinks properly (correct fix).** Detect symlink file specs with `os.Lstat` before any `EvalSymlinks` call and stage them as the symlink object itself (git handles this natively; the resolution step must simply be skipped for links). EvalSymlinks may still be appropriate for *parent directory* normalization — but never for the final path component of an explicitly listed file.
2. **At minimum, hard-error.** If any argument is a symlink and cannot be committed as such, refuse the whole commit with a clear message — never silently drop it. (Aligned with the hard-errors-over-warnings philosophy; but option 1 is the correct end state since symlinks are legitimate repo content.)

Independently of 1 vs 2: the "committed" summary must be derived from what was actually staged (`git diff --cached --name-only` before committing), not from the input list, so the count can never lie.

## Red-green requirement

- Test: repo with `file.txt` committed; create `link -> file.txt`; `safegit commit -- link` must create a commit containing the symlink (mode 120000). Currently fails with "nothing to commit".
- Test: mixed commit of one regular file + one symlink; assert both are in the commit and the reported count is 2. Currently commits 1 and reports 2.

## Affected files

- `internal/commit/commit.go` (`resolveSymlinks` and the staging + summary paths)
- Commit tests

## Effort

Small-medium — the fix is localized; the care is in not breaking the existing path-normalization behavior for regular files and directories.
