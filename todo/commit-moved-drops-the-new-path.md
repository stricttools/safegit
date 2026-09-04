# `safegit commit --moved` commits the deletion but not the new path

## Problem

During a large module-rename sweep in a consuming repo, this invocation:

    safegit commit --moved 'old/path.py -> new/path.py' -m "..." -- old/path.py new/path.py

produced a commit containing the OLD path's deletion but NOT the new
path's addition — the renamed file was absent from the commit entirely,
despite being named in the file list. The working tree had the new file;
the commit did not. Recovery was `safegit commit --amend -- <new paths>`.

The same rename committed correctly when performed as a plain `mv`
followed by `safegit commit` naming both paths WITHOUT `--moved`. The
suspicion: whatever `--moved` does to suppress safegit's own reading of
the rename delta also suppresses staging of the new path.

Adjacent observation from the same session (possibly separate): after a
`git mv`, the index held the pre-edit blob under the new name while the
working tree had the edited content, and a `safegit reset --mixed HEAD`
was needed to bring the index back in step before committing. Worth
checking whether `--moved` interacts badly with an index that already
carries rename entries.

## Suggested reproduction

1. `git mv a.py b.py` (or `mv` + edits to b.py)
2. `safegit commit --moved 'a.py -> b.py' -m x -- a.py b.py`
3. Inspect the commit: does it contain b.py?

## Expected

Either the commit carries both sides of the rename, or the invocation is
refused loudly. A commit that silently drops the named new path is a
data-integrity defect in the tool whose whole purpose is safe commits.

## Effort

Small-medium (reproduce, red-green, audit other `--moved` paths for the
same staging suppression).
