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

0. **Resolve the parent first.** The tip of the target ref is read BEFORE the index exists, so the tree and the parent always describe the same starting point. Resolving it afterwards is the ordering safegit deliberately rejects: another agent's commit landing in between would produce a commit whose tree is based on the old tip but whose parent is the new one, silently dropping that agent's files.

1. **Create a private temporary index.** A directory is created at `.git/safegit/tmp/<pid>-<random>/` containing its own `index` file. The `<pid>` prefix enables garbage collection of leaked directories from crashed processes. The random suffix (4 bytes of `crypto/rand`) prevents collisions when the same PID is reused. A dry run creates it inside a throwaway preview area outside the repository instead, so `.git/safegit` is never touched -- not even created.

2. **Seed from the resolved parent.** The temporary index is populated from that commit via `git read-tree`, giving the invocation a snapshot of the committed state. All subsequent staging happens against this private copy. (A conclusion of a merge, cherry-pick or revert asks for the SHARED index as its base instead, because the thing being committed IS that staged result. The choice is an explicit input, never inferred.)

3. **Stage only the specified files.** Files listed after `--` are staged into the temporary index. Untracked files are added; deleted files are removed. No other files can leak in because no other process writes to this index.

4. **Run the repository's `pre-commit` hook** against that index, so it sees exactly what this commit stages -- safegit builds commits from plumbing, so it runs the commit family itself rather than letting `git commit` do it. The hook runs once per operation, not once per CAS attempt.

5. **Build the tree and commit objects.** `git write-tree` produces a tree SHA from the temporary index; `git diff-tree` against the parent's tree is then what safegit reports as the commit's contents (never the arguments); and after the refusals -- an argument that contributed nothing, an unchanged tree -- the `commit-msg` hook runs and `git commit-tree` creates the commit object. `write-tree` and `commit-tree` are content-addressed and idempotent, so multiple processes creating the same objects simultaneously is harmless.

At the end of Phase A, a valid commit object exists in the object store, but no ref points to it. If the process crashes here, the commit is unreachable and will eventually be garbage collected. Nothing is corrupted.

### Phase B: serialized ref update

Phase B acquires a per-branch lock file by atomic publication, verifies the branch tip has not moved since Phase A via compare-and-swap, and atomically updates the ref to point at the new commit object. A dry run does none of it: it takes no lock, makes no re-read, and records the ref update instead of performing it.

:-: ref path="internal/lock" lang="go"

6. **Acquire the ref lock.** A lock file is published at `.git/safegit/locks/refs/heads/<branch>.lock` by writing the holder's record into a temporary sibling and `link(2)`-ing it into place. `link` fails with `EEXIST` when the path exists, so exactly one process wins -- the same one-winner property an exclusive create gives, plus one an exclusive create does not: the published file is already complete, so no contender can read a half-made lock and mistake it for a crashed holder's leftover.

7. **CAS check.** With the lock held, the branch tip is re-resolved. If it matches the parent used in Phase A, the commit is valid. If it has moved (another session committed between Phase A and Phase B), the commit is stale -- this is a CAS (compare-and-swap) miss.

8. **Update the ref.** `git update-ref` advances the branch to the new commit, passing the expected old value for a git-level CAS as belt-and-braces protection. A root commit passes the all-zero SHA, so creating a branch is conditional too.

9. **Record the operation, still holding the lock.** The oplog append happens under the lock and before the shared index is reconciled, so a failure of that reconcile still leaves a commit `safegit undo` can reverse. Then the shared index is reconciled (only when committing to the checked-out branch), `post-commit` runs, and the lock is released last.

### CAS retry on miss

When the branch tip moves between Phase A and Phase B, the entire pipeline retries from Phase A: a new temporary index is seeded from the updated branch tip, files are re-staged, and new tree and commit objects are built. This retry loop runs up to `commit.casMaxAttempts` times (default 5; any positive integer, with no upper bound). Random jitter (1-10ms) is injected between retries to break thundering-herd stampedes. The repository's hooks are NOT re-run per attempt, and the move records the commit declares keep the identifiers they were minted with, so every attempt writes the same commit message.

The stress tests verify that 100 parallel commits to the same branch all succeed with linear history and no lost files.

## Locking strategy

safegit uses per-ref file locks, not a global repository lock. This means commits to different branches proceed in parallel with zero contention -- each branch has its own lock file under `.git/safegit/locks/refs/heads/`.

### Lock file format

Each lock file is a plain text file recording the holder's identity with PID, timestamp, operation type, hostname, and process start time -- the fields that enable liveness checks and diagnostics when a lock appears stale or is held longer than expected:

```
pid=12345
ts=2026-04-26T11:39:42.123Z
op=commit
host=myhost
start=736936933
started=2026-04-26T11:39:42.120Z
```

The `pid`, `host` and `start` fields enable liveness checks. `start` is the holder's start time in clock ticks since boot, read from `/proc/<pid>/stat`; it distinguishes the holder from a later process that inherits the same PID. The `op` and `started` fields are informational for diagnostics (`started` is `start` rendered as wall-clock time and is never compared). On platforms that cannot report a process start time, `start` and `started` are absent.

### Atomic publication

A lock is not created empty and then filled in. The holder's record is written into a temporary sibling -- `.<name>.lock.tmp-<random>`, a dot-file, so no scan of the locks subtree mistakes it for a lock -- and that complete file is published with `link(2)`, which fails with `EEXIST` when the path already exists.

That gives the same one-winner guarantee an exclusive create gives, and one it does not: **no reader ever sees a half-made lock.** Creating the file first and writing the record afterwards left it existing-but-empty for an instant, and the rule "a zero-length lock file is stale" then condemned a lock whose owner was very much alive. This is why lock acquisition **requires hard-link support** on the filesystem holding `.git`.

### Stale lock reclamation

When a process crashes while holding a lock (killed by the OS, power failure, or OOM), the lock file persists on disk and blocks other sessions. safegit reclaims such a lock automatically -- but judging a lock stale is not what authorizes removing it:

1. **The judgement.** A lock is stale when its holder's PID is dead (`kill(pid, 0)`), or when its file is zero-length, corrupt or unreadable. Two conditions withhold that verdict rather than granting it: a `host=` that differs from the local hostname (the PID belongs to another machine's namespace) means the lock is never judged stale, and PID reuse is decided ONLY by comparing the `start` identity recorded at acquire time against the current start time of whatever holds that PID now -- a mismatch means the kernel recycled the PID, a match means the original holder is still running whatever the file timestamps say. When either side is unavailable (no `start` field, or a platform that cannot report start times) the check fails closed and the lock is left alone.

2. **The authorization.** The judgement above is a cheap pre-filter that keeps the common contended case off the slow path. Removal happens only after the contender opens the lock file, takes an exclusive `flock(2)` on it, re-stats the path and confirms it still names the exact inode it holds, and re-judges staleness from that descriptor rather than from a fresh read of the path. Without that, two contenders could both judge the same lock stale and both remove it -- the second deleting the fresh lock the first had already published, leaving two processes believing they held the same ref.

3. **What a failure means.** Anything other than a clean verdict -- another contender mid-reclaim, a permission error, a filesystem without `flock` -- leaves the lock alone. That is the fail-closed direction: **stale-lock reclamation requires a working `flock(2)`**, and where it does not work, contenders simply time out (exit 8) and `safegit unlock <name>` is the recovery path.

A successful reclamation is logged to the oplog as a `lock_recovered` event.

### Release is identity-checked too

A holder removes its lock file only while that path still names the exact file it published. The case that makes this necessary is reachable: an operator force-releases a lock this process still holds, a third process wins the free path and publishes its own lock there, and the original process then finishes -- a blind removal would delete the newcomer's live lock. A mismatch means our lock is already gone: there is nothing to remove, and nothing to report.

The identity is two halves, and the second is the one that decides. Comparing the stat -- device plus inode -- is not proof, because a filesystem may hand a freed inode number straight back out: ext4 recycles, btrfs never does, so the newcomer's lock can land on the very inode the previous holder had and the stat comparison then says "ours" about somebody else's live lock. The owner record is therefore compared too, byte-for-byte against the bytes this process wrote. A lock file is written before publication and never modified after, so the record is a stable identity that inode recycling cannot forge.

### Polling and backoff

Waiters use exponential backoff polling: 10ms, 20ms, 50ms, 100ms, 200ms, 500ms, capped at 1s. The total wait is bounded by `lock.acquireTimeoutSeconds` (default 30s). Past the timeout, safegit exits **8** with an error identifying the lock holder.

A waiter that sees a DIFFERENT lock file at the path than it saw last poll -- a new inode, which every publication produces -- resets its backoff to the first step. Without that, every waiter escalates to the 1s cap within six polls and stays there, so a lock held for 30ms at a time sits idle most of every second and the queue drains at about one waiter per second however many are waiting; fifty concurrent commits then take fifty seconds and time out on a lock whose total work is under two. The escalation still does its job where it was meant to: a lock one process holds for minutes never changes hands.

### Signal handling

Lock files are registered for cleanup on SIGINT and SIGTERM: the handler releases what this process published before exiting, which prevents the most common source of stale locks in interactive use. Signal exits report 128 + the signal number, the Unix convention. A refusal that exits through safegit's own error path releases pending locks too -- `os.Exit` runs no deferred function, so a command that had taken a lock and then died on an unrelated error would otherwise leave its lock file behind.

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

That is a property of a single boundary rather than of discipline: every git subprocess safegit builds is constructed by one package (`internal/gitexec`), which prepends the flag and checks the subcommand against one classification table. Nothing else in the codebase shells out to git, and the arguments an OPERATOR types for a guarded passthrough travel through the same boundary.

:-: ref path="internal/git" lang="go"

## Operation log and undo

Every mutating operation is recorded in the oplog at `.git/safegit/log`, an append-only JSONL file. Each append is made under an exclusive `flock(2)` held across the whole write, and **that** is what makes concurrent appends atomic -- not the POSIX `PIPE_BUF` guarantee, which only covers writes under 4096 bytes. Entries therefore have no size limit: an oversized one is written whole rather than refused. Concurrent oplog writes from parallel commits never produce corrupted or interleaved lines.

The log is never rotated or truncated, and there is no size setting: an audit trail that silently discards its oldest entries is not one. Reading it reports how many unparseable lines were skipped, and a consumer that needs a complete log -- `undo`, bypass detection -- fails closed on a nonzero count rather than acting on a partial reading; `safegit doctor` reports it as an error-severity finding.

:-: ref path="internal/oplog" lang="go"

The oplog enables:

- **Session-scoped undo.** `safegit undo` rolls back the last operation the commit pipeline AUTHORED -- a commit, amend, reword, `mv`, a `merge`, `pull`, `cherry-pick` or `revert` that ended in a commit, or one of the three conclusion commands -- by reading the oplog and restoring the previous ref value. What it will not roll back is decided by the entry rather than by the op name: an entry carrying an `outcome` records what GIT did to the branch (a fast-forward, a parked or up-to-date operation, a refusal), the pipeline never writes that key, and undo reverses only the entries without one. Undo is scoped to the current session (identified by `CLAUDE_CODE_SESSION_ID`), so one session's undo never affects another's commits, and it refuses outright rather than rolling a branch back over a commit safegit did not create.

- **Bypass detection.** `safegit doctor` compares the oplog's last known ref state against the actual branch tip. If they diverge, someone committed via raw `git commit`, bypassing safegit's isolation guarantees.

- **Audit trail.** Every commit, amend, undo, and lock recovery is timestamped and attributed to a PID and session.

## Coordination guards for tree-mutating operations

Not all git operations can be safely parallelized. Commands that mutate the working tree -- switch, pull, merge, rebase, reset, bisect, cherry-pick, revert -- can clobber uncommitted work from other sessions. safegit wraps these commands with a coordination guard that checks whether the working tree is clean before proceeding.

:-: ref path="internal/coord" lang="go"

If any tracked file is modified or any untracked file exists, the guarded command is refused with exit code 5 and a suggestion to commit the outstanding changes first. This prevents one session from running `safegit switch other-branch` while another session has uncommitted edits in the working tree.

Two commands narrow the check to the forms that actually write to the working tree: `reset` runs it for `--hard`, `--merge` and `--keep`, and `bisect` for its stepping subcommands. Neither narrowing is re-derived at the call site -- both read `internal/gitexec`'s classification table, the single authority over what a git invocation does, and an argv the table does not declare is refused rather than assumed harmless. Neither narrowing touches the operation lock, which every invocation of both takes unconditionally.

The guard uses `git diff HEAD` (not `git status`, which depends on the potentially stale main index) to detect modifications, ensuring accuracy even when the shared index is out of sync with the actual committed state. On an UNBORN branch -- a repository between `git init` and its first commit, where `git diff HEAD` is fatal -- the comparison is against the EMPTY TREE instead, which is exactly what such a repository holds, so a staged addition is reported there in the same shape.

Where git has an operation in flight, the same dirt is that operation's conflict markers and staged result, and "commit your work" is advice nobody can follow -- `safegit commit` is pathspec-only and refuses mid-merge. So the refusal names the operation and the command that ends it instead, rendered from one authority so no two refusals can name different commands for the same state. An in-flight operation does NOT by itself refuse a guarded command through THESE two guards: they are how an operator reaches `rebase --continue` and `merge --abort`, and refusing on state alone would refuse the way out.

Some of the guarded commands add a third check that IS about the state, and it is scoped so the way out still works. Every form that COMPUTES an operation -- `merge`, `pull`, and `cherry-pick` and `revert` in their own form as well as in the forwarded `-n`/`--no-commit` form, which asks git for the same computation -- refuses over anything git has in flight. `rebase` refuses over an in-flight state that is not itself a REBASE: it authors nothing of safegit's, but a rebase over a parked revert on a clean tree exits 0 and strands that revert's state files behind it, blocking every later commit. Scoping that predicate to the kind is what keeps `rebase --continue`, `--abort` and `--skip` working: mid-rebase state reports the rebase kind, so they pass without an exemption list of their own. `--abort` and `--quit` on the other verbs are never refused over the state they exist to clear.

### The worktree operation lock

The dirty-tree guard answers "is it safe to start?" at one instant. The operation lock answers "is anyone else already working here?" for the whole operation, and it is what makes the first answer worth anything: without it, another process could put the repository mid-merge in the window between one process's check and its ref update.

Every command that mutates a worktree takes it first: the guarded commands (switch, pull, merge, rebase, reset, bisect, cherry-pick, revert), `commit` (including `--amend` and reword), `mv`, the three conclusion commands (`merge-continue`, `cherry-pick-continue`, `revert-continue`), and `undo`. `mv` is the one whose ORDER inside the lock is worth stating: it checks for an in-flight git operation inside the lock and before its first move, because reaching the commit pipeline's own check afterwards would have moved every file and then refused to commit them. It lives at `safegit/operation` in the **worktree-local** safegit directory, so two worktrees of one repository work independently while two processes in one worktree serialize.

Lock ordering is fixed: the operation lock is **outermost**, and the per-ref locks the commit pipeline and undo take are acquired inside it. Nothing takes them the other way round, which is the whole deadlock argument. One acquisition crosses repositories without closing a cycle: the submodule auto-bump spawns `safegit commit` in the **parent** worktree while still holding the submodule's own operation lock, an edge that only ever runs from child to parent -- the observable consequence being that two sibling submodules bumping one parent serialize on the parent's operation lock, and the one that waits out `lock.acquireTimeoutSeconds` fails its bump with exit 8 after its own commit has already been made.

A guarded command holds the lock for the **full duration** of the git command it wraps -- including an interactive `rebase -i`'s editor session. A second safegit process in that worktree waits `lock.acquireTimeoutSeconds` and then exits 8, naming the holder; it never runs concurrently. Two notes on the interactive case, both measured rather than assumed (`testdata/experiments/exp-passthrough-editor-stdin.sh`):

- The editor does run. A terminal editor (vim, nano, `emacs -nw`) opens `/dev/tty` and works normally.
- `switch`, `pull`, `merge`, `rebase`, `reset` and `bisect` reach git through the effects handle, which gives the child no stdin: anything that reads standard input sees EOF immediately. `cherry-pick` and `revert` exec git directly and inherit stdin whole.

A dry run takes no lock: it performs no mutation, and acquiring one would mean a command that promises to change nothing writing a file into `.git/safegit`.

`safegit unlock safegit/operation` releases a stale operation lock left by a crashed process, and `safegit doctor` reports it by name.

## Session attribution via trailers

Each commit created by safegit includes a `Claude-Code-Session-Id` trailer (when the environment variable is set), enabling post-hoc attribution of which session created which commit. This is not a concurrency mechanism -- it is an audit trail that makes it possible to trace commit ownership in multi-session repositories.

:-: ref path="internal/trailer" lang="go"

## Worktree support

Git worktrees allow multiple checkouts of the same repository. safegit places **ref** locks and the repository-wide rewrite lock under the common `.git` directory (the one shared by all worktrees), not under each worktree's local `.git` file. This ensures that commits to the same branch from different worktrees are properly serialized.

The `SharedSafegitDir` function resolves the common git directory at runtime, so those lock files always land in the shared location regardless of which worktree initiated the commit.

The **operation** lock is the deliberate exception: it lives in the worktree-local safegit directory, because it protects one worktree's tree rather than a ref every worktree shares. Two worktrees therefore switch, merge and rebase independently, while two processes in one worktree serialize. `safegit doctor` scans both trees.

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

This is the most common case. Each session runs `safegit commit -m "message" -- file1 file2` with its own files. In one worktree the commits are serialized twice over -- by the worktree operation lock and then by the per-branch lock -- and CAS retry ensures each commit builds on the latest branch tip. All commits arrive in linear order with no lost files.

Same-worktree commits therefore do not overlap with each other, which is broader than the race strictly requires (only commit-vs-passthrough exclusion is necessary). It is measured as fine -- roughly 30 sequential commits a second -- and a reader-writer design that let commits proceed in parallel is deliberately left as future work, contingent on a measured demonstration of real contention.

### Multiple sessions working on different branches

Commits to different branches proceed in full parallel with zero lock contention, since each branch has its own independent lock file under `.git/safegit/locks/refs/heads/`. This is the ideal workflow for multi-agent orchestration where each session can be assigned its own feature branch for maximum throughput.

### Cross-branch commits

A session can commit to a branch other than the one currently checked out using `--branch <name>`. This does not move HEAD or modify the working tree -- it only updates the target branch's ref. The commit uses the target branch's tip as its parent and acquires the target branch's lock, so it serializes correctly with other commits to that branch.

### Amend while another session is committing

`safegit commit --amend` uses the same two-phase pipeline and per-ref lock as regular commits. The amended commit replaces the branch tip atomically. If another session commits between the amend's Phase A and Phase B, the CAS check catches the conflict and retries.

### Cleanup after crashes

`safegit doctor --action fix` performs the cleanup tasks relevant to concurrency, over both lock trees -- the shared one and this worktree's own -- and over each submodule's safegit directory:

- **Orphan tmp directories.** Temporary index directories from crashed processes are identified by checking PID liveness and removed.
- **Stale lock files.** Locks whose holder is genuinely gone are reclaimed through the same flock-and-identity path a contender uses, never a bare judge-then-remove: doctor sweeps unattended and by the hundred, which is exactly where a blind removal would delete a live lock that had been published in the meantime.
- **Orphaned publication temporaries.** A kill between writing a lock's record and publishing it leaves a `.<name>.lock.tmp-*` file. It is not a lock and blocks nothing, but it is swept.
- **Bypass detection.** Commits made via raw `git commit` (bypassing safegit) are detected by comparing the oplog against actual ref state.

## Temporary index garbage collection

Each invocation cleans up its own temporary index directory via `defer` on normal exit. When a process is killed, the directory leaks. The index garbage collector scans `.git/safegit/tmp/` for directories whose owning PID (encoded in the directory name) is no longer alive, and removes them.

:-: ref path="internal/index" lang="go"

`safegit doctor --action fix` is what runs it: there is no separate garbage-collection command, and nothing sweeps in the background. `--dry-run` reports what it would remove. The collector never removes a directory belonging to a live process, so it is safe to run while other sessions are actively committing, and it sweeps each submodule's safegit directory the same way.
