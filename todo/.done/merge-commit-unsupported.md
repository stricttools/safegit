# Merge commits are unrepresentable in safegit — forced a sanctioned raw-git bypass

## Context

During an orchestrated integration merge in a large monorepo (two long-lived branches deliberately merged with `git merge --no-ff --no-commit`, 27 conflicted files resolved by an agent), the session hit a hard deadlock between safegit's model and git's merge semantics:

1. The agent resolved all conflicts in the working tree, then tried `git add <file>` to mark resolutions — **blocked by the hook** ("Use 'safegit commit -m \"msg\" -- file1 file2' instead of 'git add'").
2. `safegit commit -m "merge: ..."` (no pathspec) → `error: no files specified (use -- file1 file2 ...)`, exit 2.
3. The pathspec form cannot work even in principle: git itself refuses partial commits mid-merge (`fatal: cannot do a partial commit during a merge`).

Net effect: **no safegit-conformant way exists to conclude ANY merge commit.** The only escape was the user explicitly sanctioning a one-time raw `git add` + `git commit --no-edit` — exactly the bypass safegit exists to prevent, and it had to happen on a production-bound integration branch. A second failure mode compounded it: because the agent's `git add` was blocked, a later `git commit && git push` chain half-succeeded — the commit refused (unmerged index) but the push created the remote branch at the WRONG commit (the pre-merge base), which had to be noticed and repaired.

## Problem statement

safegit's commit contract is pathspec-only, but merge conclusion is inherently whole-index. Any workflow that includes `git merge` with conflicts (integration branches, long-lived branch syncs) cannot be completed under safegit discipline.

## Possible solutions

### A. `safegit merge-continue` (new explicit verb) — recommended
Mirrors `git merge --continue`. Only valid when `MERGE_HEAD` exists (hard error otherwise). Behavior: verify every previously-conflicted path contains no conflict markers (`<<<<<<<`/`=======`/`>>>>>>>` scan — a real guardrail in the house style, hard error on hit), stage exactly the unmerged paths, conclude with the merge message (or `-m` override). Records the same audit metadata as `safegit commit`.
- Pros: explicit intent (verbose-over-shortcut philosophy), the marker scan adds safety raw git lacks, keeps `safegit commit`'s contract untouched.
- Cons: new subcommand surface; needs concurrency thought (merge state is per-worktree, so per-worktree locking should suffice).

### B. Auto-detect merge state inside `safegit commit`
When `MERGE_HEAD` exists, permit the pathspec-less form (and reject the pathspec form with a pointer, matching git's own refusal).
- Pros: no new verb; discoverable at exactly the moment of failure.
- Cons: makes `safegit commit`'s contract conditional/implicit — against the mandatory-flags-over-defaults philosophy.

### C. Do nothing, document the bypass procedure
- Pros: zero work.
- Cons: institutionalizes a raw-git escape hatch; agents will learn it; the half-committed push failure mode above shows how expensive that gets.

Also worth fixing regardless of the option chosen: the `git add`-blocking hook's message should mention the merge-conclusion path once one exists, since mid-merge is the one context where its suggested alternative ("use safegit commit -- files") is impossible to follow.

## Affected areas

- `safegit commit` argument validation (the `no files specified` refusal path)
- The git-add-blocking hook message
- New subcommand + tests if option A; red-green tests must cover: conclude-clean-merge, refuse-without-MERGE_HEAD, refuse-on-conflict-markers, and the pathspec-during-merge refusal pointing to the new verb

## Effort

S–M: the merge-state detection and marker scan are small; most of the work is tests and the concurrency review.
