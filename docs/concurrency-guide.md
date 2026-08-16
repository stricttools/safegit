---
title: Concurrency Guide
description: "How safegit enables multiple AI agent sessions to share a single git worktree without corrupting each other's commits or leaking files."
order: 3
---

# Concurrency Guide

This guide explains safegit's concurrency model: what problems arise when multiple AI sessions share a git worktree, how safegit solves them, and what guarantees it provides.

## The problem: multiple agents, one worktree

When multiple Claude Code sessions (or any concurrent processes) work in the same git repository, standard git commands race on the shared `.git/index` file. The index is a single mutable staging area that every `git add` and `git commit` reads and writes. Two sessions running these commands at the same time can produce commits containing files from both sessions, silently leaking one session's work into another's commit.

This is not a theoretical concern. AI agent orchestration systems routinely run multiple sessions against the same checkout, and the race window is wide enough that it triggers regularly under normal workloads.

## Two-phase commit pipeline

safegit splits the commit operation into two distinct phases to achieve both parallelism and correctness. Phase A builds the commit object using a private temporary index, fully isolated from other sessions. Phase B acquires a per-branch lock and updates the ref via compare-and-swap.

:-: ref path="internal/commit" lang="go"

### Phase A: parallel-safe object construction

Every `safegit commit` invocation creates its own temporary index file in a unique directory under `.git/safegit/tmp/`, completely isolated from the shared `.git/index` and from every other concurrent invocation, so multiple sessions can stage files simultaneously without interference.

:-: ref path="internal/index" lang="go"

1. **Create a private temporary index.** A directory is created at `.git/safegit/tmp/<pid>-<random>/` containing its own `index` file. The `<pid>` prefix enables garbage collection of leaked directories from crashed processes. The random suffix (4 bytes of `crypto/rand`) prevents collisions when the same PID is reused.

2. **Seed from the branch tip.** The temporary index is populated from the current branch tip via `git read-tree`, giving the invocation a snapshot of the committed state. All subsequent staging happens against this private copy.

3. **Stage only the specified files.** Files listed after `--` are staged into the temporary index. Untracked files are added; deleted files are removed. No other files can leak in because no other process writes to this index.

4. **Build the tree and commit objects.** `git write-tree` produces a tree SHA from the temporary index, and `git commit-tree` creates a commit object pointing to that tree with the resolved parent. Both are content-addressed and idempotent -- multiple processes creating the same objects simultaneously is harmless.

At the end of Phase A, a valid commit object exists in the object store, but no ref points to it. If the process crashes here, the commit is unreachable and will eventually be garbage collected. Nothing is corrupted.

### Phase B: serialized ref update

Phase B acquires a per-branch lock file using atomic exclusive creation, verifies the branch tip has not moved since Phase A via compare-and-swap, and atomically updates the ref to point at the new commit object.

:-: ref path="internal/lock" lang="go"

5. **Acquire the ref lock.** An exclusive lock file is created at `.git/safegit/locks/refs/heads/<branch>.lock` using `O_CREAT|O_EXCL` (atomic exclusive creation). Only one process can hold this lock at a time.

6. **CAS check.** With the lock held, the branch tip is re-resolved. If it matches the parent used in Phase A, the commit is valid. If it has moved (another session committed between Phase A and Phase B), the commit is stale -- this is a CAS (compare-and-swap) miss.

7. **Update the ref.** `git update-ref` advances the branch to the new commit, passing the expected old value for a git-level CAS as belt-and-suspenders protection.

8. **Release the lock and record the operation.** The lock file is removed, and the operation is appended to the oplog.

### CAS retry on miss

When the branch tip moves between Phase A and Phase B, the entire pipeline retries from Phase A: a new temporary index is seeded from the updated branch tip, files are re-staged, and new tree and commit objects are built. This retry loop runs up to `commit.casMaxAttempts` times (default 5, configurable up to 200). Random jitter (1-10ms) is injected between retries to break thundering-herd stampedes.

The stress tests verify that 100 parallel commits to the same branch all succeed with linear history and no lost files.

## Locking strategy

safegit uses per-ref file locks, not a global repository lock. This means commits to different branches proceed in parallel with zero contention -- each branch has its own lock file under `.git/safegit/locks/refs/heads/`.

### Lock file format

Each lock file is a plain text file recording the holder's identity with PID, timestamp, operation type, and hostname fields that enable liveness checks and diagnostics when a lock appears stale or is held longer than expected:

```
pid=12345
ts=2026-04-26T11:39:42.123Z
op=commit
host=myhost
```

The `pid` and `host` fields enable liveness checks. The `op` field is informational for diagnostics.

### Stale lock recovery

When a process crashes while holding a lock (killed by the OS, power failure, or OOM), the lock file persists on disk and blocks all other sessions from committing to that branch. safegit detects stale locks and recovers from them automatically using PID liveness checks, host verification, and PID reuse detection:

1. **PID liveness check.** On each poll iteration, the lock holder's PID is checked via `kill(pid, 0)`. If the process is dead, the lock is stale.

2. **Host check.** If the lock file contains a `host=` field that differs from the local hostname, the PID check is skipped -- the PID belongs to a different machine's namespace (relevant for NFS/shared filesystems).

3. **PID reuse detection (Linux).** On Linux, if `/proc/<pid>` was created after the lock file, the PID was recycled by the kernel and the lock is stale despite the PID appearing alive.

4. **Corrupt lock files.** A zero-length or unparseable lock file (from a crash mid-write) is treated as stale.

Stale lock recovery is logged to the oplog as a `lock_recovered` event.

### Polling and backoff

Waiters use exponential backoff polling: 10ms, 20ms, 50ms, 100ms, 200ms, 500ms, capped at 1s. The total wait is bounded by `lock.acquireTimeoutSeconds` (default 30s). Past the timeout, `safegit commit` exits with an error identifying the lock holder.

### Signal handling

Lock files are registered for cleanup on SIGINT and SIGTERM. If safegit is interrupted while holding a lock, the signal handler removes the lock file before exiting. This prevents the most common source of stale locks in interactive use.

## What makes it safe vs regular git

| Concern | Regular `git commit` | `safegit commit` |
|---|---|---|
| Index isolation | All sessions share `.git/index` | Each invocation gets a private temporary index |
| File leakage | Session A's staged files appear in Session B's commit | Impossible -- staging is isolated per-invocation |
| Branch tip race | `git commit` reads HEAD, stages, commits non-atomically | Two-phase pipeline with per-ref lock and CAS verification |
| Concurrent same-branch commits | Undefined behavior, potential corruption | Serialized via lock + CAS retry with guaranteed linear history |
| Crash recovery | `.git/index.lock` left behind, requires manual `rm` | Stale locks auto-recovered via PID liveness checks |
| Concurrent different-branch commits | Possible but fragile (index is shared) | Fully parallel -- separate lock files per branch |
| Untracked file handling | Requires `git add` (mutates shared index) | Files listed after `--` are staged atomically in the private index |
| Index lock contention | `git` takes `.git/index.lock` for many operations | `--no-optional-locks` flag prevents git from taking advisory index locks |

### The `--no-optional-locks` flag

Every git command safegit invokes is prefixed with `--no-optional-locks`, which prevents git from refreshing the shared `.git/index` as a side effect of read-only operations like `git status` or `git diff`. Without this flag, even read operations can contend on `.git/index.lock` with concurrent writers.

:-: ref path="internal/git" lang="go"

## Operation log and undo

Every mutating operation is recorded in the oplog at `.git/safegit/log`, an append-only JSONL file. Writes use `O_APPEND` with flock-guarded appends, and each entry is kept under 4096 bytes to preserve POSIX atomic append guarantees. This means concurrent oplog writes from parallel commits never produce corrupted or interleaved lines.

:-: ref path="internal/oplog" lang="go"

The oplog enables:

- **Session-scoped undo.** `safegit undo` rolls back the last commit, amend, or reword by reading the oplog and restoring the previous ref value. Undo is scoped to the current session (identified by `CLAUDE_CODE_SESSION_ID`), so one session's undo never affects another's commits.

- **Bypass detection.** `safegit doctor` compares the oplog's last known ref state against the actual branch tip. If they diverge, someone committed via raw `git commit`, bypassing safegit's isolation guarantees.

- **Audit trail.** Every commit, amend, undo, and lock recovery is timestamped and attributed to a PID and session.

## Coordination guards for tree-mutating operations

Not all git operations can be safely parallelized. Commands that mutate the working tree -- checkout, merge, rebase, reset, pull -- can clobber uncommitted work from other sessions. safegit wraps these commands with a coordination guard that checks whether the working tree is clean before proceeding.

:-: ref path="internal/coord" lang="go"

If any tracked file is modified or any untracked file exists, the guarded command is refused with exit code 5 and a suggestion to commit the outstanding changes first. This prevents one session from running `safegit checkout other-branch` while another session has uncommitted edits in the working tree.

The guard uses `git diff HEAD` (not `git status`, which depends on the potentially stale main index) to detect modifications, ensuring accuracy even when the shared index is out of sync with the actual committed state.

## Session attribution via trailers

Each commit created by safegit includes a `Claude-Code-Session-Id` trailer (when the environment variable is set), enabling post-hoc attribution of which session created which commit. This is not a concurrency mechanism -- it is an audit trail that makes it possible to trace commit ownership in multi-session repositories.

:-: ref path="internal/trailer" lang="go"

## Worktree support

Git worktrees allow multiple checkouts of the same repository. safegit handles this by placing lock files under the common `.git` directory (the one shared by all worktrees), not under each worktree's local `.git` file. This ensures that commits to the same branch from different worktrees are properly serialized.

The `SharedSafegitDir` function resolves the common git directory at runtime, so lock files always land in the shared location regardless of which worktree initiated the commit.

:-: ref path="internal/repo" lang="go"

## The scrub system

History rewriting (`safegit scrub`) is an inherently non-concurrent operation -- it rewrites every commit in a range, changing SHAs throughout the history. safegit handles this with a dedicated rewrite lock and a crash-safe record trail.

### Rewrite lock

Scrub operations acquire a repository-wide coordination lock on `safegit/rewrite` (not a per-ref lock like commits use) before modifying any refs. This prevents two scrub operations from running simultaneously and producing inconsistent history, since history rewriting changes every commit SHA downstream of the rewrite point.

### Crash-safe rewrite maps

Every scrub persists a three-phase record to `.git/safegit/rewrite-maps.jsonl`, a flock-guarded JSONL file that enables crash recovery and post-scrub orchestration by recording the full old-to-new commit SHA mapping, tag rewrites, and cleanup status:

1. **`start` record.** Written before any refs move. Contains the full old-to-new commit SHA mapping and the pre-rewrite state of all remote-tracking refs. If the process crashes after this point, the mapping is recoverable.

2. **`refs` record.** Written after refs and tags have been updated. Contains all tag rewrite records.

3. **`complete` record.** Written after cleanup (reflog expiry, object pruning) and HEAD resolution. Contains the new HEAD and cleanup status.

These three phases ensure that no matter when a crash occurs, an orchestrator (like `rlsbl release scrub`) can determine exactly what state the repository is in and resume or roll back appropriately.

### Rewrites in release-managed repositories

A history rewrite invalidates metadata that lives outside the commit graph: changelog entries that name commit hashes, remote tags, and the forge releases attached to them. safegit does not try to prevent that by refusing the rewrite. It performs the rewrite and records the full old-to-new mapping in the journal, and the release tooling repairs the damage afterwards: its changelog hash-resolution check fails loudly on the dangling hashes, `rlsbl changelog remap --from-journal` rewrites them from this journal, and `rlsbl release reconcile` re-pushes the moved tags and recreates their GitHub Releases. Detection and repair are the contract; the journal is the interface.

### The rewrite walk

The scrub walker processes commits in topological order (parents before children), applying a transform function to each commit. Parent SHAs are remapped through the growing old-to-new map, so descendant commits automatically inherit rewritten parents. When the transform changes a commit's tree, message, or author, a new commit object is created; otherwise the original SHA is preserved as an identity mapping.

After the walk, the shared finalization pipeline updates all branch and tag refs to point at rewritten commits, syncs the main index with the rewritten HEAD, expires tainted reflog entries, and prunes old objects.

## Common concurrent workflows

### Multiple sessions editing different files on the same branch

This is the most common case. Each session runs `safegit commit -m "message" -- file1 file2` with its own files. The commits are serialized by the per-branch lock, and CAS retry ensures each commit builds on the latest branch tip. All commits land in linear order with no lost files.

### Multiple sessions working on different branches

Commits to different branches proceed in full parallel with zero lock contention, since each branch has its own independent lock file under `.git/safegit/locks/refs/heads/`. This is the ideal workflow for multi-agent orchestration where each session can be assigned its own feature branch for maximum throughput.

### Cross-branch commits

A session can commit to a branch other than the one currently checked out using `--branch <name>`. This does not move HEAD or modify the working tree -- it only updates the target branch's ref. The commit uses the target branch's tip as its parent and acquires the target branch's lock, so it serializes correctly with other commits to that branch.

### Amend while another session is committing

`safegit commit --amend` uses the same two-phase pipeline and per-ref lock as regular commits. The amended commit replaces the branch tip atomically. If another session commits between the amend's Phase A and Phase B, the CAS check catches the conflict and retries.

### Cleanup after crashes

`safegit doctor --action fix` performs three cleanup tasks relevant to concurrency: removing orphan temporary index directories left by crashed processes via PID liveness checks, releasing stale lock files whose owning processes are no longer alive, and detecting raw git commits that bypassed safegit's isolation guarantees by comparing the oplog against actual branch ref state:

- **Orphan tmp directories.** Temporary index directories from crashed processes are identified by checking PID liveness and removed.
- **Stale lock files.** Lock files held by dead processes are removed.
- **Bypass detection.** Commits made via raw `git commit` (bypassing safegit) are detected by comparing the oplog against actual ref state.

## Temporary index garbage collection

Each invocation cleans up its own temporary index directory via `defer` on normal exit. When a process is killed, the directory leaks. The index garbage collector scans `.git/safegit/tmp/` for directories whose owning PID (encoded in the directory name) is no longer alive, and removes them.

:-: ref path="internal/index" lang="go"

This runs automatically during `safegit doctor --action fix` and can also be triggered manually. The garbage collector never removes directories belonging to live processes, so it is safe to run while other sessions are actively committing.
