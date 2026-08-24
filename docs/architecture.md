---
title: Architecture
description: "Design rationale and architectural specification for safegit: why it exists, how the commit pipeline works, and how failure modes are handled."
order: 2
---

# Architecture

This document captures safegit's design rationale and architectural specification: why it exists in its current shape, how the commit pipeline achieves concurrency safety, and how failure modes are detected and recovered.

## Decision Record

This section records the key design decisions behind safegit and why alternatives were rejected. Each entry captures the option considered, its strengths, its disqualifying weaknesses, and the rationale for the chosen approach. The goal is to prevent re-derivation of decisions that were already explored and resolved during initial design.

- **Why not Jujutsu (jj)**: jj eliminates the `.git/index` race architecturally (no index), but does not provide per-agent commit isolation -- multiple agents still share the working-copy commit `@` and must use `jj split` to separate their work. Adoption requires every agent and human to learn `jj` commands. jj's conflict format is binary in the git tree, unreadable by standard git tooling. Adopting jj imposes a new mental model and breaks teammate-side tools for a partial solution to our problem.

- **Why not GitButler (`but`)**: GitButler solves multi-agent isolation via virtual branches with native MCP and Claude Code hook support -- practically the closest fit. Rejected because: Fair Source license (becomes MIT after 2 years) introduces commercial roadmap risk, the virtual-branch state in `.git/gitbutler/` adds a parallel mental model, and we'd be a downstream consumer of someone else's evolving product rather than owning the surface.

- **Why build a thin Go wrapper instead**: a wrapper around git plumbing (write-tree, commit-tree, update-ref, apply --cached) gives ~80% of the safety properties of jj/GitButler at ~5% of their complexity, while keeping every existing git tool, CI pipeline, code-review system, and teammate workflow intact. We control the roadmap. Standard git commits go out on the wire. Failure mode if safegit breaks: drop to raw git, lose isolation, but the repo remains valid.

- **Why Go**: static single binary, native concurrency primitives (mutex, channels, goroutines for ref-lock contention handling), clean subprocess and JSON handling, no runtime dependency. Bash hits scaling limits past ~400 LOC. Python adds runtime dep and slow startup. Rust is overkill for a wrapper of this size.

## Data Model

safegit's own state lives under `.git/safegit/`, so removing that directory returns the repository to the equivalent of "vanilla git" -- git itself never reads anything safegit writes, and safegit installs no enforcement git hooks (see the Pre-pre-push Hook Contract section for the rationale and the layered enforcement story).

Two qualifications, both deliberate. A repository with linked worktrees has one such directory PER worktree plus the shared one in the common git dir, which is why `doctor --action uninstall` is a repository-wide operation that enumerates every path it removes. And the checkout-provided hook store, `.safegit/hooks/` in the work tree, is repository CONTENT rather than safegit state: it is versioned with the project, and removing safegit's state directory does not remove it (nothing runs it either, since only safegit ever does).

### Directory layout

```
<worktree>/
  .safegit/
    hooks/                   the CHECKOUT-PROVIDED hook store: repository content,
                             versioned with the project, run on push by safegit only
.git/                        (the COMMON git dir; a linked worktree's own git dir is
                             .git/worktrees/<name> and holds its own safegit/)
  safegit/
    config.json              safegit config (schema version, defaults)
    log                      append-only operation log (JSONL), flock-guarded appends
    rewrite-maps.jsonl       crash-safe old-to-new SHA journal, one group per rewrite
    locks/
      refs/
        heads/<branch>.lock  per-ref lock (pid + ts + op + host + start identity)
      safegit/
        rewrite.lock         repository-wide history-rewrite lock (shared dir)
        operation.lock       worktree operation lock (worktree-local dir)
      .<name>.lock.tmp-*     publication temporary; never a lock, swept by doctor
    tmp/
      <pid>-<random>/        per-invocation scratch (the temporary index),
                             deleted when the invocation exits
    hooks/                   the LIVE hook store, keyed on the COMMON git dir
      pre-pre-push           single-file hook (executable, optional)
      pre-pre-push.d/        directory of hooks (run in lexical order, optional)
  hooks/
    pre-pre-push             PRE-MIGRATION location; nothing runs from here
    pre-pre-push.d/          PRE-MIGRATION location; nothing runs from here
```

### The two hook stores

Pre-pre-push hooks live in two stores, and they are not interchangeable:

- The **live store**, `.git/safegit/hooks/`, is safegit's own. `safegit hook install` writes here, and it is keyed on the repository's **common** git dir -- the same anchor as the ref locks -- so a hook installed from a linked worktree is the hook every worktree of the repository runs.
- The **checkout-provided store**, `.safegit/hooks/` in the work tree, is repository content, so everyone who clones the repository gets it. It is per-worktree by nature, because it *is* the checkout. Membership is the directory on disk, never git's tracking: an uncommitted -- even gitignored -- executable file there runs on the next push exactly like a committed one, and a tracking probe would instead make a hook an operator just wrote silently invisible while it kept running.

The consequence, stated plainly: **cloning a repository and pushing from that checkout runs the repository's committed scripts.** Execution happens only on `safegit push` and `safegit hook run` -- an operator action with push intent -- never on clone, fetch, checkout or any inspection command, and `safegit hook list` names every location with its origin so the set can be read before anything is pushed.

Git's own `.git/hooks/` is the third location safegit knows about, and only as history: it is where safegit kept its pre-pre-push hooks before the live store existed. Nothing runs from there any more. `pre-pre-push` and `pre-pre-push.d/` sitting there make every push and every `hook run` refuse (exit 24) until `safegit hook migrate` relocates them, which it does unconditionally, since those two names are the only ones safegit ever wrote into git's directory. Because git's hook directory is common to every worktree, so are that refusal and that migration.

### Per-invocation tmp index

Each safegit invocation creates an isolated temporary index file seeded from the tip of the branch it is committing to, performs all staging against that private index, and deletes it on exit. There are no persistent sessions, no session IDs, and no shared staging area between invocations. This isolation is the core mechanism that prevents concurrent agents from leaking files into each other's commits.

1. Create `.git/safegit/tmp/<pid>-<random>/` where `<random>` is a short hex suffix (4 bytes) chosen to avoid PID-reuse races within the same second.
2. Seed the tmp index from the resolved parent commit -- the tip of the TARGET ref, which is not `HEAD` when `--branch` names another branch, and the empty tree for a root commit:

    ```
    GIT_INDEX_FILE=.git/safegit/tmp/<pid>-<random>/index \
      git --no-optional-locks read-tree <parent-sha>
    ```

    A caller that must commit the whole staged result rather than a pathspec -- a conclusion of a merge, a cherry-pick or a revert -- asks for the SHARED index as the base instead. That choice is an explicit input, never inferred.

3. Apply the requested staging operations against the tmp index (see the Hunk Staging section).
4. Build the tree, the commit, and update the ref (see the Commit Pipeline section).
5. Delete `.git/safegit/tmp/<pid>-<random>/` on exit (success or failure) via `defer`.

There is no incremental staging across safegit invocations. Each `safegit commit` takes a complete file list and produces one commit. There is no `safegit session start`, no `SAFEGIT_SID`, no agent registration, no session GC.

If the process is killed mid-invocation, the tmp directory leaks. `safegit doctor --action fix` removes any tmp directory whose PID is dead.

### Operation log schema

The operation log at `.git/safegit/log` is an append-only JSON-lines file recording every mutating operation safegit performs. Each line captures a timestamp, process ID, operation type, and operation-specific metadata such as ref names and commit SHAs. This log serves as the source of truth for `safegit undo` and `safegit doctor`, enabling reliable rollback and bypass detection.

```jsonl
{"ts":"2026-04-26T11:39:42.123Z","pid":12345,"op":"commit","extra":{"ref":"refs/heads/main","tree":"<sha>","parent":"<sha>","sha":"<sha>","attempts":1}}
{"ts":"2026-04-26T11:39:43.001Z","pid":12346,"op":"commit","extra":{"ref":"refs/heads/main","tree":"<sha>","parent":"<sha>","sha":"<sha>","attempts":3}}
{"ts":"2026-04-26T11:39:45.900Z","pid":12348,"op":"push","extra":{"remote":"origin","refs":[{"localRef":"refs/heads/main","localSha":"<sha>","remoteRef":"refs/heads/main","remoteSha":"<sha>"}],"hooksRun":2}}
```

Required fields: `ts`, `pid`, `op`. Other fields are op-specific. Each append is made under an exclusive `flock(2)` held across the whole write, and THAT is what makes a concurrent append atomic -- not the 4096-byte `PIPE_BUF` guarantee `O_APPEND` gives. Entries therefore have no size limit: an oversized one is written whole rather than rejected. Commit messages are still not logged, only their SHA.

The log is append-only and is never rotated or truncated. `oplog.Read` reports how many unparseable lines it skipped, and a consumer that needs a complete log -- `undo`, bypass detection -- fails closed on a nonzero count rather than acting on a partial reading; `safegit doctor` reports it as an error-severity finding.

### Lock file format

Lock files at `locks/refs/heads/<branch>.lock` are short text files recording the holder's PID, hostname, timestamp, operation type, and process start identity -- the information needed for liveness checks, stale-lock reclamation, and diagnostic inspection by `safegit doctor`.

**Publication is atomic in content, not only in existence.** The record is written into a temporary sibling (`.<name>.lock.tmp-<random>`, a dot-file so no scan mistakes it for a lock) and then published with `link(2)`. `link` fails with `EEXIST` when the path already exists, which gives exactly the one-winner property `O_CREAT|O_EXCL` gives -- and additionally means no reader ever sees a half-made lock. Writing the record in place after an exclusive create left the file existing-but-empty for an instant, and the staleness rule "corrupt or zero-length means stale" condemned a freshly created lock whose owner was very much alive.

Two environment requirements follow from that design, and they are the two ways a repository can be unable to lock properly:

- **Lock acquisition requires hard-link support** on the filesystem holding `.git`, because publication IS `link(2)`.
- **Reclaiming a stale lock requires a working `flock(2)`** (see below). Where flock does not work, nothing is ever reclaimed on a guess: contenders wait out `lock.acquireTimeoutSeconds` and exit 8 instead, and `safegit unlock <name>` -- which removes one named lock unconditionally after its own staleness check -- is the recovery path.

```
pid=12345
ts=2026-04-26T11:39:42.123Z
op=commit
host=hostname.local
start=736936933
started=2026-04-26T11:39:42.120Z
```

`pid`, `host` and `start` are the keys for liveness: `start` is the holder's start time in clock ticks since boot (from `/proc/<pid>/stat`), which pins the PID to one specific process instance so a recycled PID cannot pass as the original holder. `op` and `started` are informational (for `safegit doctor` and operator inspection).

## Commit Pipeline

The full sequence for `safegit commit -m "msg" -- file1 file2 ...`. The pipeline is split into a parallel-safe phase (object construction) and a serialized phase (ref update). A complete file list is required (no implicit "stage everything").

Six commands reach this pipeline and produce commits through it: `commit` (including `--amend` and reword), `mv`, the three conclusion commands (`merge-continue`, `cherry-pick-continue`, `revert-continue`), and a single-commit `revert`. They differ in what they put into the request -- index edits, extra parents, an author to pin, move records -- and share everything below.

### Before either phase (once per invocation)

The commands that own a working tree take the **worktree operation lock** first, outside everything here (see the Concurrency Guide). Inside the pipeline, once and before the retry loop:

- **Refuse if git has an operation in flight**, unless the caller declared itself the conclusion path for exactly that operation -- and the declaration is verified, not trusted. A commit built mid-merge would hand `commit-tree` a single parent and silently drop the merge's second parent and staged result.
- **Open the preview quarantine** when this is a dry run: a temporary directory outside the repository that every object-writing git subprocess writes into and that is deleted on exit.
- **Resolve the target ref**, canonicalize and expand the file arguments against the tip that ref names (which is not HEAD when `--branch` names another branch), and resolve the declared moves and retractions into records -- so a refusal happens before anything is staged and every attempt writes the same record with the same id.
- **Prepare the repository's own hooks**, so they run at most once each however many attempts the CAS loop takes.

### Phase A -- parallel-safe (no locks), per attempt

1. **Resolve the parent FIRST.** `git rev-parse <ref>`, before the index exists. Resolving it after building the tree is the ordering this pipeline deliberately rejects: another agent's commit landing in between would produce a commit whose tree is based on the old tip but whose parent is the new one, silently dropping that agent's files. A ref that does not resolve is a root commit.
2. **Create the per-invocation tmp index** under `.git/safegit/tmp/<pid>-<random>/` (under the preview area in a dry run), seeded from the resolved parent -- or from the empty tree for a root commit, or from the shared index where the caller asked for that (a conclusion commits the operation's whole staged result).
3. **Apply the caller's index edits**, if any: `mv`'s carried tree entries, a conclusion's declared resolutions. Inside the loop, because the index is created fresh on every attempt.
4. **Stage the named files** into the tmp index, whole or by hunk selection.
5. **Run `pre-commit`** against the tmp index, so the hook sees exactly what this commit stages. Skipped by a dry run; run only once across attempts.
6. **Build the tree** with `git write-tree` against the tmp index. Content-addressed and parallel-safe: concurrent invocations writing the same tree converge on the same SHA.
7. **Read what the commit actually contains** with `git diff-tree` against the parent's tree. Every count and path safegit reports comes from here, never from the arguments -- the arguments say what was asked for, the objects say what the commit holds. A named argument that contributed nothing is a refusal naming it (exit 11); an unchanged tree is the empty-commit refusal unless `--allow-empty`.
8. **Run `commit-msg`** on the message carrying the user's own trailers and the move records, adopting whatever the hook leaves; safegit's session trailer goes on afterwards, so a rewriting hook cannot strip it. It comes after the refusals above, which is git's own order -- git stops at "nothing to commit" before it asks for a message.
9. **Build the commit** with `git commit-tree`: the tip is parent 0 (and is what every CAS below is made against), a conclusion names the other side as extra parents, and an author is pinned where the operation calls for one.

At this point a valid commit object exists in the object store but no ref points to it. If the process dies right now, the commit is unreachable and `git gc` collects it; nothing is broken.

### Phase B -- serialized (per-ref lock + CAS)

A dry run takes NO lock and makes no re-read: there is nothing to protect, and a lock file written by a run that promises to change nothing would itself be a change. It joins the executing path at the ref update, which it records instead of performing.

10. **Acquire the ref lock** for the target ref by publishing `locks/refs/heads/<branch>.lock` (see Lock file format). On contention, poll with exponential backoff -- 10ms, 20ms, 50ms, 100ms, 200ms, 500ms, capped at 1s, and reset to the first step whenever the lock is observed to have changed hands, so a rapidly handed-over lock does not starve its queue. A lock whose holder is genuinely gone is reclaimed under the lock file's own `flock` (see Stale lock reclamation); past `lock.acquireTimeoutSeconds` safegit exits **8** naming the holder.
11. **Re-resolve the parent (the CAS check).** With the lock held, re-read the ref. Unchanged: proceed. Changed: this attempt is a CAS miss -- release, jitter, and start Phase A again from step 1 with the new tip. After `commit.casMaxAttempts` (default 5) attempts, exit **7**.
12. **Update the ref** with `git update-ref <ref> <new> <expected>`, which performs the compare-and-swap at the git level too -- belt and braces beside the lock. A root commit's expectation is the all-zero SHA ("this ref must not exist"), so creating one is conditional as well. This is the single point where the pipeline mints a mutation: in a preview it is recorded and nothing moves.
13. **Append to the oplog** -- *while the lock is still held*, and before the index reconcile, so that a fatal reconcile still leaves a commit `safegit undo` can reverse.
14. **Reconcile the shared index** with the new commit, preserving staged work the parent tip does not account for. Only when committing to the checked-out branch: a cross-branch commit must not touch this worktree's index at all.
15. **Run `post-commit`**, once the commit is real and nothing can take it back.
16. **Release the lock** (deferred, so it outlives every step above) and delete the tmp index directory.

### Sequence summary

| Step | Phase | Holds ref lock? | Touches global state? |
|---|---|---|---|
| 1. Resolve parent ref | A | no | reads only |
| 2. Init tmp index | A | no | no (only its own tmp dir) |
| 3. Apply index edits | A | no | writes objects (idempotent) |
| 4. Stage to tmp index | A | no | writes objects (idempotent) |
| 5. `pre-commit` hook | A | no | whatever the hook does |
| 6. `git write-tree` | A | no | writes objects (idempotent) |
| 7. `git diff-tree` / refusals | A | no | reads only |
| 8. `commit-msg` hook | A | no | whatever the hook does |
| 9. `git commit-tree` | A | no | writes one commit object |
| 10. Acquire ref lock | B | acquiring | yes (publishes the lock file) |
| 11. Re-resolve parent / CAS check | B | held | reads only |
| 12. `git update-ref <old>` | B | held | yes (moves the ref) |
| 13. Append oplog | B | **held** | yes (one appended line) |
| 14. Reconcile shared index | B | held | yes (this worktree's index) |
| 15. `post-commit` hook | B | held | whatever the hook does |
| 16. Release lock, clean up | B | releasing | lock file and tmp dir |

In a dry run, steps 10-16 do not happen at all: the recorded ref update ends the attempt.

### Wakeup mechanism

Waiters poll with exponential backoff (10ms, 20ms, 50ms, 100ms, 200ms, 500ms, capped at 1s), bounded by `lock.acquireTimeoutSeconds` (default 30). Past that safegit exits with code 8, and the message names the holder. There is no notification mechanism: a waiter learns the lock is free by looking.

A waiter that observes a DIFFERENT lock file at the path than it saw on its previous poll -- a new inode, which every publication produces -- resets its backoff to the first step. Without that reset every waiter escalates to the 1s cap within six polls and stays there, so a lock held for 30ms at a time sits idle for most of every second and the queue drains at roughly one waiter per second however many are waiting. The escalation still does its job where it was meant to: a lock one process holds for minutes never changes hands, so its waiters keep polling once a second.

### Stale lock reclamation

A lock whose holder is genuinely gone is reclaimed automatically -- but the judgement that a lock is stale is not what authorizes removing it, because two contenders can reach that judgement about the same file and the second would then delete the fresh lock the first had already published. Reclamation therefore happens under the lock file's own exclusive `flock(2)`, with the path inspected again through `stat(2)` and compared against the held descriptor's inode before anything is unlinked, and staleness re-judged from the descriptor's own contents rather than a fresh read of the path. Any condition other than a clean verdict -- another contender mid-reclaim, a filesystem without flock, a permission error -- leaves the lock alone, which is the fail-closed direction.

Staleness itself needs positive evidence:

- the holder's PID must be dead (a `kill(pid, 0)` probe);
- a lock recording a different `host=` is never judged stale, because a PID from another machine's namespace means nothing here;
- PID reuse is decided by comparing the `start=` identity recorded at acquire time against the current start time of whatever holds that PID now -- and when either side is unavailable, the check fails closed;
- an unreadable, corrupt or zero-length lock file is stale: that is what a crash mid-create leaves behind.

**Release is identity-checked too.** A holder removes the path only while it still names the exact file it published (`SameFile` against the identity recorded at publication time). The reachable case that makes this necessary: an operator force-releases a lock this process still holds, a third process wins the free path and publishes its own live lock there, and the original process then finishes -- a blind `unlink` would delete the newcomer's lock while the newcomer is working. A mismatch means our lock is already gone, so there is nothing to remove and nothing to report.

### Retry policy

| Failure | Retry? | Max attempts | Backoff |
|---|---|---|---|
| CAS miss (parent changed) | yes | `commit.casMaxAttempts` (default 5) | random jitter (1-10ms) between attempts |
| Ref lock contention | yes (waiting) | unbounded under `lock.acquireTimeoutSeconds` (30s) | exponential-backoff polling, reset on a change of holder |
| `git write-tree` fails | no | 0 | exit code 9 |
| `git commit-tree` fails | no | 0 | exit code 10 |
| `git update-ref` fails transiently after the lock is held | yes | `commit.casMaxAttempts` (rare; usually a stale parent) | retry the whole attempt |

### Concluding an operation git stopped

A merge, cherry-pick or revert that git parked on a conflict is finished by `merge-continue`, `cherry-pick-continue` or `revert-continue`, which reach the pipeline with three things set: `IndexBaseSharedIndex` (the thing being committed IS the operation's staged result, so the tmp index is seeded from the shared index rather than from the parent tree), the state file's commits as extra parents, and the declared resolutions as index edits. A verified `SequencerContext` is what lets them past the mid-operation refusal that every other commit hits; it is checked against the state on disk, never taken on trust.

Two checks stand in front of the pipeline, both refusing before anything is staged: the declared resolutions must name exactly the paths git left unmerged (exit 17), and the content they name must carry no conflict block that no side of the conflict -- and no base commit of the operation -- already had (exit 18, with a `safegit-conflict-markers` attribute read from the first parent's tree as the only exemption). Afterwards, in this order: the operation's whole state-file set is removed, the resolutions are applied to the shared index and it is reconciled with the new tip, and the working tree is written to match. The working tree goes last because its failure is the only one that leaves nothing inconsistent behind.

A third check stands with them: **the overwrite refusal** (exit 27). A declaration that materializes over a working-tree file compares that file against the conflict's own sides -- the three index stages, plus the blob git wrote there itself, read verbatim from `AUTO_MERGE` -- and a file matching none of them is a hand edit nothing else records, so the conclusion refuses rather than destroying it. `--discard-unmatched-worktree` elects the destruction.

**Two states are refused rather than concluded**, and both are ones safegit's own commands can no longer produce, so meeting one means raw git created it:

- a QUEUED cherry-pick or revert (`.git/sequencer` present). A conclusion removes the operation's whole state-file set -- which for a queue includes the queue -- so concluding one step natively would throw the remaining commands away. The former answer was to stage the resolutions into a copy of the index and hand the rest to git's own `--continue`; that produced commits under a safegit command name that git had authored, and the whole second authorship class is deleted. safegit either writes the commit or refuses.
- an octopus `MERGE_HEAD`, or a content conflict with no `AUTO_MERGE` (the non-default-strategy signature). The first has more sides than every check over a merge is written for; the second leaves the overwrite refusal with nothing to compare against.

Both refusals name git's own `--continue` and `--abort` as the way to finish what git began.

## Move records: declared and observed

safegit never infers a move from file CONTENTS -- no similarity scoring, no `diff -M`, and never a path staged that the caller did not name. A move reaches a commit message as a `Moved:` record carrying its own identifier and the two C-quoted paths, and the record says which of two things established it: a person DECLARED it, or safegit OBSERVED it in the commit's own raw delta.

- **`safegit commit --moved 'old -> new'`** states that a move already happened: the old path must be tracked in the commit's parent and gone from disk, the new one must be present. A declaration the repository does not bear out exits **19** and commits nothing.
- **An OBSERVED record is one safegit minted from the commit's own delta** -- the same blob leaving one path and arriving at another, where the modes are regular files, the pairing is one-to-one, and the blob sits at exactly one path on each side once the declared paths are taken out. It carries the `observed` token after its id; a declared record carries no token at all, which is what every record written before the token existed already means. A whole directory that moved collapses to one subtree record, and a commit carrying more scattered inferred moves than the cap records none of them and says so on stderr, pointing at `--moved`. Every candidate a fence declined rides the commit payload with its reason.
- **A declaration answers the question inference asks.** The paths it names leave the candidate sets and the fences' own tree listings before any pairing, so a pair is never stated twice -- and one declaration can un-block another judgement, since a blob at two deleted paths with one of them declared leaves the other free to be judged alone. Declaring a pair an OBSERVED record on the commit being amended already carries SUPERSEDES it: the amend writes a retraction of that record and the caller's own record together, which is the ordinary replacement shape.
- **`safegit mv 'old -> new'`** is the other half: it performs the move, mints the record for what it moved, and commits, in one invocation, so the move and its record cannot be out of step. It is one of the six commit-producing paths through the pipeline, and its shape is validate -> move -> commit: every pair is checked before the first filesystem mutation, the moves are minted through the effects handle (so a dry run records them), a failure part-way through puts back everything already moved, and the commit carries each moved path across as the exact blob the parent tree held -- the move and nothing else.
- **Both spellings share one grammar and one overlap check.** Two pairs may not nest and may not chain (`a -> b` beside `b -> c`), because the result would depend on the order they were performed in; both are argument-against-argument contradictions and exit 2.
- **A record is never edited, only retracted.** `--moved-retract <id>` writes a `Moved-Retract:` trailer after verifying the id names a record that exists and is not already retracted in the history the commit is built on. A replacement is a retraction plus a new declaration in one commit.
- **`safegit revert` mints the INVERSE of every record the reverted commit declared**, through both doors (a clean computed revert and one concluded after a conflict). Every inverse is OBSERVED whatever the reverted record's own origin was: nobody stated it, safegit derived it by turning another commit's record around. There is no queued form to except: `safegit revert` reverts one commit.
- **A rewrite treats records as records.** `scrub match` and `scrub run` substitute inside the DECODED paths and re-encode through the one encoder, so the output always parses; a substitution whose result is no longer a move is refused before any ref moves (exit 30). `scrub file --delete` removes the records naming the erased path, whole, in the same rewrite; `--replace-with` edits no message, because the path still exists and the record is still true.

## Hunk Staging

> **Note:** Standalone `safegit stage` and `safegit unstage` commands were not implemented. Hunk-level staging lives in the `--hunks` flag (e.g., `safegit commit -m "msg" --hunks 'file.txt:1,3'`). A positional path is always the literal name of a file, never a hunk selection -- see "Staging API surface" below.

### Hunk extraction

Hunk extraction produces a structured list of diff hunks between the tmp index and the working tree for a given file. The canonical hunk list is obtained by running `git diff` against the per-invocation temporary index, then parsing the output into header metadata and individual hunk objects with 1-based indices used for selective staging.

```
GIT_INDEX_FILE=.git/safegit/tmp/<pid>-<random>/index \
  git --no-optional-locks diff --no-color --no-ext-diff --no-renames -- <file>
```

against the working tree, where the "before" side is the content currently in the tmp index.

Parse the output:

- Header: lines starting with `diff --git`, `index `, `--- `, `+++ ` -- preserve verbatim, used to reconstruct synthetic patches.
- Hunks: each block beginning with `@@ -<old_start>,<old_count> +<new_start>,<new_count> @@`. Index hunks 1-based.

Hunk objects in memory:

```go
type Hunk struct {
    Index    int      // 1-based
    OldStart int; OldCount int
    NewStart int; NewCount int
    Header   string   // the @@ line
    Body     []string // the +/- /space lines
}
```

### Staging API surface

Hunk-level staging lives in ONE flag, `--hunks`, and a positional path is always the literal name of a file. There are no standalone `stage`/`unstage` commands.

| Operation | Selects |
|---|---|
| `safegit commit -- <file>` | Whole file (all hunks) |
| `safegit commit --hunks '<file>:1,3,5'` | Specific 1-based hunks |
| `safegit commit --hunks '<file>:2-4'` | Range (inclusive) |

The split inside a `--hunks` element is on its LAST colon, so `--hunks 'sprint:1:2,3'` selects hunks 2 and 3 of the file named `sprint:1`. Naming one path both as a positional and in `--hunks`, or twice in `--hunks`, is a hard error decided on the canonical paths rather than on how they were spelled.

It used to be the other way round: a positional argument was split into a path and a hunk selection when its tail looked numeric AND `os.Stat` could not see the whole string as a file. That made the grammar of a command line a function of disk state -- `notes:1` was a hunk selection from one directory and a filename from another -- and made a file whose name really ends in a colon plus digits impossible to delete, because once it was gone from disk the probe read its own name as a hunk selection. Nothing on disk is consulted to decide how to read a command line any more.

### Synthetic patch mechanism

When a commit specifies individual hunks rather than whole files, safegit constructs a synthetic patch containing only the selected hunks and applies it to the temporary index via `git apply`. This reuses git's patch application logic rather than implementing custom index manipulation. Line-number shifts from skipped hunks are handled via the `--recount` flag.

1. Extract hunks as described above.
2. Compute the selected-hunk subset.
3. Build a synthetic patch: header lines from the source diff + headers/bodies of selected hunks only.
4. `git apply --cached --recount --whitespace=nowarn` against `GIT_INDEX_FILE`, with the patch fed on stdin.
    - `--cached` means apply to the index, not the working tree.
    - `--recount` makes `git apply` tolerant of slightly off line counts (caused by skipping intermediate hunks).
5. On failure, that is the answer: there is exactly ONE apply, and a patch that does not apply is a hard error carrying git's own stderr, with the repo-relative path of the file it failed on attached. There used to be a silent retry with `--3way`, and it was deleted because it is not the same staging action -- the direct apply stages exactly the hunks the caller selected, while a three-way retry MERGES the patch into whatever the index holds, so a caller who asked for one hunk could be handed a merge result nobody named and nothing in the output would say a retry had happened.

### Edge cases

| Case | Behavior |
|---|---|
| Hunk depends on an earlier unselected hunk (line-number mismatch) | `--recount` handles most cases; anything it cannot handle is a hard error naming the file, with git's own stderr surfaced. |
| File is binary | Whole-file only; a hunk selection is refused with exit 14. |
| Symlink | Whole-file only; a hunk selection is refused with exit 15 -- a symlink has nothing to split. |
| File is untracked | Staged whole, as an addition. |
| File is deleted | The deletion is staged. |
| Path named but contributing nothing | Refused with exit 11, naming the argument. |

There is no unstage. The temporary index is created fresh for every attempt of every invocation and thrown away with it, so there is no accumulated staging to take back: what a commit contains is exactly what its command line named. The cleanup half of `git rm --cached` -- dropping a path from the index while leaving it on disk -- is `safegit commit --untrack <path>`, which is a commit, not a staging operation.

## Pre-pre-push Hook Contract

Git's built-in `pre-push` hook fires AFTER the network connection to the remote is open. For long-running validators (smoke tests, integration suites), this means the SSH connection times out before validation finishes. safegit owns a `pre-pre-push` phase that runs BEFORE any network I/O.

### No enforcement hooks

safegit does NOT install git hooks that block raw `git commit` / `git push` invocations. Enforcement is at the Claude Code `settings.json` layer (Bash permission rules) and via convention. Agents that bypass safegit by running `git` directly are responsible for the consequences; `safegit doctor` will surface bypasses by detecting `HEAD` movements that have no corresponding op log entry.

The hooks safegit does install are its own pre-pre-push scripts, in its own store (`.git/safegit/hooks/`, see "The two hook stores"): a place for the user to define long-running pre-push validators that run before the network connection opens. They are informational, not coercive, and they are not git hooks -- git never runs them.

### Hook discovery

safegit discovers pre-pre-push hooks by walking both stores in full, at any depth, in a deterministic order: the checkout-provided store first, then the live store, each sorted by its store-relative name. The first non-zero exit code aborts the push before any network connection is opened.

1. `.safegit/hooks/**` -- the checkout-provided store (work tree)
2. `.git/safegit/hooks/**` -- the live store (common git dir)

Within each store the traditional two shapes are just names: `pre-pre-push` is the single-file hook and `pre-pre-push.d/*` the directory of them, and anything else in the store is a hook too. Files starting with `.` or ending in `~` are not hooks by name and are never run.

A hook must be executable (`chmod +x`). A non-executable hook is a REFUSAL (exit 25) in BOTH stores, and `safegit hook run` refuses identically rather than reporting that there was nothing to run. A hook is disabled by removing it -- deleting the file in the live store, deleting it and committing that in the checkout-provided one -- never by dropping its mode, so a lost mode bit (a checkout on a filesystem without modes, a patch tool that dropped it) must not silently stop the repository's checks. `safegit hook list` still LISTS such a hook, marked `NOT EXECUTABLE`: the listing is the diagnostic, and the hook an operator is asking about is usually the one that is not running. `safegit doctor`'s `hook_perms` check reports the same condition at error severity, in either store.

Standard git hooks live in `.git/hooks/` as usual -- resolved through `git rev-parse --git-path hooks`, so `core.hooksPath` and linked worktrees are honored. safegit builds commits from git plumbing rather than by invoking `git commit`, so it runs the commit family itself: `pre-commit` against the per-invocation index, `commit-msg` on the composed message before safegit's own session trailer is added (a rewrite by the hook is adopted), and `post-commit` after the ref has moved. Each runs once per commit, amend or reword, whatever the compare-and-swap loop does, and a `--dry-run` runs none of them and says so. A refusal from either of the first two exits **16**. `prepare-commit-msg` never runs: safegit never opens an editor, so there is no message-preparation step for it to act on.

All hook output goes to stderr, because safegit's stdout is a structured channel -- the JSON envelope in machine mode, and a child's parsed result in the parent auto-bump.

One consequence of running the hooks once rather than per attempt: a `pre-commit` hook that stages content DIFFERING from the working tree loses that staging on a CAS retry, because the hook does not re-run and disk is re-staged. Formatter hooks rewrite disk, so the real exposure is negligible.

### Stdin contract

Pre-pre-push hooks receive their input on stdin in the same format as git's built-in `pre-push` hook, ensuring compatibility with existing hook scripts. Each line describes one ref being pushed, with four space-separated fields identifying the local ref, local SHA, remote ref, and remote SHA. This contract means hooks written for git's `pre-push` can be moved to `pre-pre-push` without modification.

```
<local-ref> SP <local-sha> SP <remote-ref> SP <remote-sha> LF
```

`<remote-sha>` is `0000000000000000000000000000000000000000` for new refs. Empty stdin on `--delete` pushes is also possible (matches git semantics).

### Environment

safegit sets several environment variables before executing pre-pre-push hooks, providing context about the push operation that hooks can use for validation decisions. These variables identify the remote being pushed to, the configured timeout for self-monitoring, and the current execution phase. All other environment variables from the parent process are inherited, so hooks have access to PATH and any user-configured variables.

| Var | Meaning |
|---|---|
| `SAFEGIT_REMOTE_NAME` | name of the remote being pushed to (e.g. `origin`) |
| `SAFEGIT_REMOTE_URL` | the URL we're about to dial |
| `SAFEGIT_PHASE` | `pre-pre-push` |
| `SAFEGIT_HOOK_TIMEOUT_S` | the configured timeout, for hook self-monitoring |

Inherits all other env (PATH, etc.).

### Execution flow

```
safegit push [<remote>]
  -> resolve the remote URL
  -> IF --force-with-lease: confirm at the terminal, BEFORE any network contact
       (--approve-consequential answers it in advance; a declined force
        contacts nothing at all)
  -> resolve the refs to push -- which READS the remote (git ls-remote), because
     each lease is pinned to the SHA safegit itself observed there
  -> build hook stdin from that exact ref set
  -> for each discovered hook in order:
       run hook with stdin, output to stderr
       enforce timeout: hooks.preprepush.timeoutSeconds (default 1800 = 30 min)
       if exit != 0: abort, exit code 20 (timeout: exit code 21)
  -> git push, with git's output CAPTURED (classification needs its stderr)
       retry on a transport error only: re-read the remote, re-pin every lease,
       and refuse (exit 40) if a LOCAL ref moved since the hooks saw it
  -> append ONE oplog entry, after the push succeeds
```

Note what this does and does not promise. "Before any network I/O" is about the HOOKS: they run before `git push` opens a transport, which is the whole point of the phase. The ref resolution that decides what to push -- and what to pin each lease to -- is itself a read of the remote, and it necessarily comes first. A `--dry-run` push performs that read too, and then records the push instead of performing it.

### Compose with git's built-in pre-push

git's built-in `pre-push` is still useful for things that genuinely need the remote sha resolved post-handshake (e.g. checking that you're not pushing over a force-push). safegit does NOT pass `--no-verify` to the inner `git push`; the built-in `pre-push` fires after the network handshake as usual.

So the full sequence is:

1. safegit's pre-pre-push runs (no network)
2. `git push` opens the connection
3. git's built-in pre-push runs (network open)
4. Pack negotiation and transfer

If both phases are needed, the user splits expensive checks (smoke tests) into `pre-pre-push` and lightweight checks (force-push protection) into `pre-push`.

### Timeout policy

- Default: 1800s (30 min) per hook.
- Configurable globally: `hooks.preprepush.timeoutSeconds`, and handed to the hook in `SAFEGIT_HOOK_TIMEOUT_S` so a script can monitor itself against it.
- There is NO per-hook override. A hook cannot raise or lower its own limit: a configured limit the limited script can rewrite for itself is a limit that is not one, and the repository's configuration is the single authority. A hook that genuinely needs longer says so by having `hooks.preprepush.timeoutSeconds` raised.
- On timeout: `SIGTERM` to the hook's whole process group, then 5s grace, then `SIGKILL`. Exit code 21 ("hook timed out").

### Bypass

`safegit push --no-pre-push-hook` skips the entire pre-pre-push phase and goes straight to the git push. (The flag is `--pre-push-hook` / `--no-pre-push-hook`; omitted, the hooks run.) This escape hatch exists for human operators who need to push urgently when a hook is broken or misconfigured. For AI agent environments, the negated form should be disallowed via Claude Code permission settings to prevent agents from routinely bypassing validation checks.

A `--dry-run` never runs the hooks whatever the flag says -- a hook is an arbitrary script, so running one is a mutation a preview may not perform -- and it says so on stderr and in the payload's `pre_pre_push_hooks_skipped` member. Where both hold, the payload reports `disabled` rather than `dry-run`: a preview of a run that turned the hooks off must report the operator's decision, because the real push will not run them either.

## Failure Modes

This section catalogs every known failure mode in safegit's operation, covering process crashes, concurrent access races, network failures, disk exhaustion, and cross-machine usage. For each failure mode the design specifies the observable symptom, the detection mechanism, and the recovery procedure. Recovery is automatic unless explicitly noted as manual or out of scope.

### Process crashes mid-stage

- **Symptom:** orphan tmp directory at `.git/safegit/tmp/<pid>-<rand>/`.
- **Detection:** `safegit doctor` and `safegit doctor --action fix` list tmp directories, parse the leading PID, and call `kill -0 pid`. Dead PID = orphan.
- **Recovery:** `safegit doctor --action fix` removes the tmp directory. The object store needs no repair: staging writes blobs, and any that no commit ended up referencing are ordinary unreachable objects that `git gc` collects. Nothing is corrupt and nothing is lost.

### Process crashes mid-commit (lock held)

- **Symptom:** `locks/refs/heads/<branch>.lock` exists with a dead `pid`.
- **Detection:** an acquisition attempt reads the lock as a cheap pre-filter -- `pid`, `host`, and the recorded `start` identity (see Stale lock reclamation). A `host` that differs from the local hostname is never judged stale (cross-machine fence; see Cross-machine / NFS below).
- **Recovery:** the pre-filter does not authorize anything. The contender opens the lock file, takes an exclusive `flock` on it, re-checks that the path still names that exact inode, re-judges staleness from the descriptor, and only then unlinks -- so two contenders facing the same stale lock cannot both "reclaim" it, with the second deleting the fresh lock the first just published. The winner then publishes its own lock and appends a `lock_recovered` oplog entry. Corrupt or zero-length lock files (a crash mid-create) count as stale. Where `flock(2)` does not work, nothing is reclaimed at all: contenders time out (exit 8) and `safegit unlock <name>` is the way out.
- **Also swept:** an orphaned publication temporary (`.<name>.lock.tmp-*`) left by a kill between the write and the `link`. It is not a lock by name, so it blocks nothing; `safegit doctor --action fix` removes it.

### Two invocations commit to same branch simultaneously

- **Symptom:** both reach Phase B of the Commit Pipeline around the same time.
- **Detection:** publication (`link(2)`) fails with `EEXIST` for the second, which waits via exponential-backoff polling.
- **Recovery:** the lock orders them. After the first commits and releases, the second wakes, re-resolves the parent (now the first invocation's commit), detects the CAS miss, rebuilds the commit on the new parent, and succeeds. End result: linear history, no corruption, both commits land.
- **Same worktree, additionally:** the worktree operation lock serializes the two invocations even before this, since `commit` takes it around the whole operation.

### Network failure mid-push

- **Symptom:** `git push` exits non-zero with a transport error after the pre-pre-push hooks have already run.
- **Detection:** non-zero exit from inner `git push`.
- **Detection:** git's own captured stderr is classified against multi-word transport phrases; the framework's error string never carries it, which is why the retry loop was dead until it read the captured stream.
- **Recovery:** safegit retries the push up to `push.retryAttempts` (default 3) with exponential backoff (1s, 2s, 4s) for transport errors (DNS, connection refused, connection reset, TLS handshake, broken pipe). NOT retried: HTTP 401/403, "non-fast-forward", "remote rejected" -- and above all a rejected `--force-with-lease`, which is TERMINAL (exit 41): retrying would re-observe the other session's ref, pin the lease to it, and perform exactly the overwrite the lease prevented. Every retry re-reads the remote and re-pins every lease, since an expectation describes the remote at a moment. Pre-pre-push hooks are NOT re-run, so a retry that finds a LOCAL ref has moved refuses (exit 40) rather than publishing on the strength of a hook run that never saw it. **The oplog records one entry, after the push succeeds** -- not one per attempt.

### Disk full during object write

- **Symptom:** `git write-tree` or `git commit-tree` fails with `ENOSPC`.
- **Detection:** non-zero exit + stderr scan for `ENOSPC` / "No space left".
- **Recovery:** abort the operation with exit code 9 or 10. The object DB may have partial loose objects -- these are harmless (git `gc` cleans them). No corruption. User must free disk and retry.

### Cross-machine / NFS use is unsupported

safegit supports same-machine concurrency only. Cross-machine concurrency on a shared filesystem (NFS, SSHFS, FUSE-over-network) is out of scope. Rationale: PID-based liveness is meaningless across hosts; `flock(2)` semantics on NFS are unreliable and silently break the safety guarantees.

- **Detection:** `safegit doctor` reads the device of `.git/` via `statfs(2)` and warns if the filesystem type is in a denylist (`nfs`, `nfs4`, `fuse.sshfs`, `cifs`, `smbfs`).
- **Detection at runtime:** lock files include `host`. If a lock holder's `host` differs from local `hostname`, we conservatively treat the lock as alive and wait. After `lock.acquireTimeoutSeconds`, exit code 8. The user must run `safegit unlock` on the holder's host or accept that cross-machine use is unsupported.
- **Recovery:** documented as an explicit non-goal.

### Pre-pre-push hook hangs

- **Symptom:** hook process doesn't exit within `hooks.preprepush.timeoutSeconds`.
- **Detection:** Go context timeout fires.
- **Recovery:** `SIGTERM`, 5s grace, `SIGKILL`. The push aborts with exit code 21, and no oplog entry is written at all -- the oplog records pushes that happened. No partial state: the transport was never opened.

### Raw-git bypass

- **Symptom:** a user (or another tool) ran `git commit` directly. safegit does not install enforcement hooks, so this is allowed by design.
- **Detection:** `safegit doctor`'s `bypass_detect` check, and only there -- no mutating command warns about it in passing. The check compares the oplog's most recent ref-update entry for the current branch against the branch's actual tip, and reports a warn-severity finding when they have diverged:

    ```
    tip of main (a1b2c3d4) diverged from last oplog entry (9e8f7a6b); raw git may have been used
    ```

    A divergence is warn-severity, so it does not by itself make `doctor` exit nonzero. Two conditions the check reports at ERROR severity instead, because each would otherwise disable the check silently just when it has something to say: an oplog it cannot read for this ref, and a ref the oplog records a tip for that no longer resolves at all -- a branch deleted or reset outside safegit. A detached HEAD reports nothing, since there is no branch to compare.
- **Recovery:** none needed mechanically -- git semantics still hold. The bypass is surfaced for transparency.
