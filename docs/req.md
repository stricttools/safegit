---
description: "Requirements for safegit: multi-agent concurrency safety, lock-free design, standard Git compatibility, CLI and hooks support, and crash recovery."
---

# safegit Requirements

> **Historical document.** This is the original requirements list, kept as the
> record of what safegit was asked for. It is not a description of the built
> tool, and where the two differ the implementation is the authority -- see
> [Architecture](architecture.md) and the [Concurrency Guide](concurrency-guide.md).
> One difference is worth naming here, because the requirement below states the
> opposite: **the lock does poll.** Waiters use exponential-backoff polling
> (10ms to a 1s cap, bounded by `lock.acquireTimeoutSeconds`), with the backoff
> reset whenever the lock is observed to have changed hands. No notification
> mechanism was built; the requirement's real goal -- that no human has to
> intervene -- is met by automatic stale-lock reclamation.

## Core: Multi-Agent Safety

- Multiple AI agent sessions must be able to work on the same repo concurrently without worktrees
- No shared mutable staging area -- agents must not be able to stage or commit each other's files
- Commits must be serialized or isolated so that concurrent commits never produce corrupt or mixed results
- If two agents edit the same file, the conflict must be surfaced as data, not silently lost or merged

## Concurrency Mechanism

- Lock-free or minimally-locking design preferred over a global mutex
- If a queue/lock is used, it must notify waiting agents automatically (no polling, no human intervention) *(as built: polling with backoff, no notification -- see the note at the top)*
- Agent crash must not leave the repo in a permanently locked state (stale lock recovery within 30 seconds)

## Git Compatibility

- Must read and write standard `.git` repositories
- Commits must be normal git commits (same SHA, same format) visible to any git tool
- Push/pull to GitHub, GitLab, and any standard git remote must work
- Teammates using plain `git` must see normal branches and commits
- CI/CD pipelines and code review tools must work without modification

## CLI

- Must be usable as a CLI tool (no GUI dependency)
- Must support non-interactive / headless operation for agent consumption
- Structured output (JSON) for agent parsing is preferred

## Hooks

- Pre-pre-push hooks: ability to run hooks before a connection to the remote is even established (e.g. validate, lint, or gate before any network I/O to GitHub)
- Git's built-in `pre-push` hook fires after the remote connection is already open -- this is too late for some use cases (e.g. smoke tests that take a long time will cause the already-open SSH connection to timeout)

## Reliability

- Every mutating operation should be undoable
- No operation should silently lose work
- Crash recovery must be automatic or trivial
