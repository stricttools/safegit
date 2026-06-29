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

All safegit-specific state lives under `.git/safegit/`. Nothing escapes that directory, so a `rm -rf .git/safegit` returns the repository to the equivalent of "vanilla git". safegit does NOT install enforcement git hooks (see the Pre-pre-push Hook Contract section for the rationale and the layered enforcement story).

### Directory layout

```
.git/
  safegit/
    config.json              global safegit config (schema version, defaults)
    log                      append-only operation log (JSONL)
    locks/
      refs/
        heads/<branch>.lock  active ref lock (PID + ts + op_kind)
      objects.lock           optional coarse lock for pack writes (rare; usually unused)
    tmp/
      <pid>-<random>/        per-invocation scratch (index file, synthetic patches)
                             deleted when the invocation exits
  hooks/
    pre-pre-push             single-file hook (executable, optional)
    pre-pre-push.d/          directory of hooks (run in lexical order, optional)
```

There is no `.safegit/` repo-tracked directory. All hooks live in `.git/hooks/` only.

### Per-invocation tmp index

Each safegit invocation creates an isolated temporary index file seeded from HEAD, performs all staging against that private index, and deletes it on exit. There are no persistent sessions, no session IDs, and no shared staging area between invocations. This isolation is the core mechanism that prevents concurrent agents from leaking files into each other's commits.

1. Create `.git/safegit/tmp/<pid>-<random>/` where `<random>` is a short hex suffix (4 bytes) chosen to avoid PID-reuse races within the same second.
2. Seed the tmp index from `HEAD`:

    ```
    GIT_INDEX_FILE=.git/safegit/tmp/<pid>-<random>/index \
      git --no-optional-locks read-tree HEAD
    ```

3. Apply the requested staging operations against the tmp index (see the Hunk Staging section).
4. Build the tree, the commit, and update the ref (see the Commit Pipeline section).
5. Delete `.git/safegit/tmp/<pid>-<random>/` on exit (success or failure) via `defer`.

There is no incremental staging across safegit invocations. Each `safegit commit` takes a complete file list and produces one commit. There is no `safegit session start`, no `SAFEGIT_SID`, no agent registration, no session GC.

If the process is killed mid-invocation, the tmp directory leaks. `safegit doctor --fix` removes any tmp directory whose PID is dead.

### Operation log schema

The operation log at `.git/safegit/log` is an append-only JSON-lines file recording every mutating operation safegit performs. Each line captures a timestamp, process ID, operation type, and operation-specific metadata such as ref names and commit SHAs. This log serves as the source of truth for `safegit undo`, `safegit redo`, and `safegit doctor`, enabling reliable rollback and bypass detection.

```jsonl
{"ts":"2026-04-26T11:39:42.123Z","pid":12345,"op":"commit","extra":{"ref":"refs/heads/main","tree":"<sha>","parent":"<sha>","sha":"<sha>","attempts":1}}
{"ts":"2026-04-26T11:39:43.001Z","pid":12346,"op":"commit","extra":{"ref":"refs/heads/main","tree":"<sha>","parent":"<sha>","sha":"<sha>","attempts":3}}
{"ts":"2026-04-26T11:39:45.900Z","pid":12348,"op":"push","extra":{"remote":"origin","refs":[{"localRef":"refs/heads/main","localSha":"<sha>","remoteRef":"refs/heads/main","remoteSha":"<sha>"}],"hooksRun":2}}
```

Required fields: `ts`, `pid`, `op`. Other fields are op-specific. Writes use `O_APPEND` on Linux/macOS, which guarantees atomic append for writes <= `PIPE_BUF` (4096 bytes). Lines longer than 4096 bytes are rejected; commit messages are not logged, only their SHA.

### Lock file format

Lock files at `locks/refs/heads/<branch>.lock` are short text files created atomically via `O_CREAT|O_EXCL` (exclusive create) to serialize ref updates on a per-branch basis. Each lock file records the holder's PID, hostname, timestamp, and operation type, providing the information needed for liveness checks, stale lock recovery, and diagnostic inspection by `safegit doctor`.

```
pid=12345
ts=2026-04-26T11:39:42.123Z
op=commit
host=hostname.local
```

`pid` and `host` are the keys for liveness. `op` is informational (for `safegit doctor` and operator inspection).

## Commit Pipeline

The full sequence for `safegit commit -m "msg" -- file1 file2 ...`. The pipeline is split into a parallel-safe phase (object construction) and a serialized phase (ref update). A complete file list is required (no implicit "stage everything").

### Phase A -- parallel-safe (no locks)

1. **Initialize tmp index.** Create `.git/safegit/tmp/<pid>-<random>/index` and seed from `HEAD`. Set `GIT_INDEX_FILE` to that path for all subsequent git invocations in this process.
2. **Validate file arguments.** For each `<file>` after `--`:
    - It must exist on disk OR be a known deletion.
    - It must be inside the working tree (no `../` escape).
3. **Stage files into tmp index.** Apply the staging logic from the Hunk Staging section to each file.
4. **Build the tree.**

    ```
    GIT_INDEX_FILE=.git/safegit/tmp/<pid>-<random>/index \
      git --no-optional-locks write-tree
    ```

    `write-tree` is content-addressed and parallel-safe -- multiple invocations writing the same tree converge on the same SHA and writes to the object DB are idempotent.
5. **Snapshot the parent.** Resolve the current tip of the target branch:

    ```
    git rev-parse --verify refs/heads/<branch>
    ```

    Call this `<parent-sha>`. (For a detached `HEAD`, the target ref is `HEAD` and the parent is the resolved commit.)
6. **Build the commit.**

    ```
    git commit-tree <tree-sha> -p <parent-sha> -m "msg"
    ```

    Produces `<new-commit-sha>`. Also parallel-safe.

At this point we have a valid commit object in the object DB but no ref points to it. If the process dies right now, the commit is unreachable and will be GC'd by `git gc` eventually; nothing is broken.

### Phase B -- serialized (per-ref lock + CAS)

7. **Acquire ref lock.** Attempt to atomically create `locks/refs/heads/<branch>.lock` via `O_CREAT|O_EXCL`. On success, we hold the lock.
8. **On lock contention,** poll with exponential backoff (10ms, 20ms, 50ms, 100ms, 200ms, 500ms, capped at 1s) until the lock is released or the timeout expires. On each poll, check if the lock holder is dead via `kill(pid, 0)`; if so, remove the stale lock and retry creation.
9. **Re-resolve parent.** Once we hold the lock, re-read `refs/heads/<branch>`. If it changed since step 5, we have a CAS miss.
    - **If parent unchanged:** proceed to step 10.
    - **If parent changed:** release the lock, GOTO step 5 (rebuild the commit with the new parent). After 5 attempts (configurable: `commit.casMaxAttempts`, default 5), exit with code 7 ("could not converge on branch tip; another writer is making faster progress; retry manually").
10. **Update the ref.**

    ```
    git update-ref refs/heads/<branch> <new-commit-sha> <parent-sha>
    ```

    `update-ref` itself takes the `<old-value>` argument so it performs an atomic CAS at the git level too. This is belt-and-suspenders: our lock prevents most contention, and `update-ref --old` catches any we missed.
11. **Release the ref lock.** Atomic `unlink(2)` of the lock file. Waiters detect lock release via exponential-backoff polling.
12. **Append to op log.** Write the `commit` JSONL line per the Operation log schema.
13. **Cleanup.** Delete `.git/safegit/tmp/<pid>-<random>/`.

### Sequence summary

| Step | Phase | Holds lock? | Touches global state? |
|---|---|---|---|
| 1. Init tmp index | A | no | no (only tmp dir) |
| 2. Validate args | A | no | reads only |
| 3. Stage to tmp index | A | no | no |
| 4. `git write-tree` | A | no | yes (writes objects, idempotent) |
| 5. Resolve parent ref | A | no | reads only |
| 6. `git commit-tree` | A | no | yes (writes one commit object) |
| 7. Acquire ref lock | B | acquiring | yes (creates lock file) |
| 8. Wait if held (polling) | B | waiting | no (read-only polling) |
| 9. Re-resolve parent / CAS check | B | held | reads only |
| 10. `git update-ref --old` | B | held | yes (writes ref) |
| 11. Release ref lock | B | releasing | yes (unlink only) |
| 12. Append op log | B | not held | yes (append-only line) |
| 13. Cleanup | B | not held | tmp dir only |

### Wakeup mechanism

Waiters use exponential-backoff polling (10ms, 20ms, 50ms, 100ms, 200ms, 500ms, capped at 1s). The poll loop is bounded by `lock.acquireTimeout` (default 30s); past that safegit exits with code 8 ("ref lock acquisition timed out").

On each poll iteration, the lock file is checked for liveness: if the holder PID is dead, the stale lock is removed and the waiter acquires it.

### Retry policy

| Failure | Retry? | Max attempts | Backoff |
|---|---|---|---|
| CAS miss (parent changed) | yes | `commit.casMaxAttempts` (default 5) | none -- retry immediately, lock will queue us |
| Ref lock contention | yes (waiting) | unbounded under `lock.acquireTimeout` (30s) | exponential-backoff polling |
| `git write-tree` fails | no | 0 | exit code 9 |
| `git commit-tree` fails | no | 0 | exit code 10 |
| `git update-ref` fails after lock held | yes | `commit.casMaxAttempts` (rare; usually means stale parent) | retry from step 5 |

## Hunk Staging

> **Note:** Standalone `safegit stage` and `safegit unstage` commands were not implemented. Hunk-level staging is available via `safegit commit -- file:hunk-spec` syntax (e.g., `safegit commit -m "msg" -- file.txt:1,3`).

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

Hunk-level staging is exposed inline at commit time via `file:hunk-spec` syntax rather than through standalone `stage`/`unstage` commands. The caller appends a colon and comma-separated 1-based hunk indices or ranges to the filename. The underlying mechanism -- extracting hunks, building a synthetic patch, and applying it to the tmp index -- is identical for all syntax forms.

| Operation | Selects |
|---|---|
| `safegit commit -- <file>` | Whole file (all hunks) |
| `safegit commit -- <file>:1,3,5` | Specific 1-based hunks |
| `safegit commit -- <file>:2-4` | Range (inclusive) |

### Synthetic patch mechanism

When a commit specifies individual hunks rather than whole files, safegit constructs a synthetic patch containing only the selected hunks and applies it to the temporary index via `git apply`. This reuses git's patch application logic, including three-way merge fallback, rather than implementing custom index manipulation. Line-number shifts from skipped hunks are handled via the `--recount` flag.

1. Extract hunks as described above.
2. Compute the selected-hunk subset.
3. Build a synthetic patch: header lines from the source diff + headers/bodies of selected hunks only.
4. `git apply --cached --index --whitespace=nowarn --recount` against `GIT_INDEX_FILE`.
    - `--cached` means apply to the index, not the working tree.
    - `--recount` makes `git apply` tolerant of slightly off line counts (caused by skipping intermediate hunks).
    - `--index` sanity-checks against the working tree.
5. On `git apply` failure, retry with `--3way` (uses blob sha info from the patch's `index` line). On second failure, exit code 12 with the apply stderr surfaced.

### Edge cases

| Case | Behavior |
|---|---|
| Hunk depends on earlier unselected hunk (line-number mismatch) | `git apply --recount` handles most cases; on failure, re-run with `--3way`; on second failure, exit 12 with `Hint: try staging earlier hunks first or use --patch`. |
| Working tree changed since diff was taken | Re-extract hunks. If hunk count or content changed, abort with exit code 13 ("file changed under us; re-run") -- callers should retry. |
| File is binary | Whole-file only; hunk spec rejected with exit 14. |
| File is new (intent-to-add) | Whole-file only; map to `git update-index --add --cacheinfo`. |
| File is deleted | Deletion is added to the index. |
| Symlink | Treated as a special file; whole-file only. |

### Inverse: unstage

Unstaging reverses the staging operation by building the same synthetic patch and applying it with the `--reverse` flag to the temporary index. The diff direction is inverted so that applying the patch in reverse removes previously staged changes. For whole-file unstaging, the simpler `git reset HEAD -- <file>` is used instead of patch reversal.

```
git apply --cached --index --reverse --recount <synthetic-patch>
```

Whole-file unstage is the simpler `git reset HEAD -- <file>` against the tmp index.

## Pre-pre-push Hook Contract

Git's built-in `pre-push` hook fires AFTER the network connection to the remote is open. For long-running validators (smoke tests, integration suites), this means the SSH connection times out before validation finishes. safegit owns a `pre-pre-push` phase that runs BEFORE any network I/O.

### No enforcement hooks

safegit does NOT install git hooks that block raw `git commit` / `git push` invocations. Enforcement is at the Claude Code `settings.json` layer (Bash permission rules) and via convention. Agents that bypass safegit by running `git` directly are responsible for the consequences; `safegit doctor` will surface bypasses by detecting `HEAD` movements that have no corresponding op log entry.

The only safegit-installed hook is `.git/hooks/pre-pre-push`, which is a wrapper that lets the user define long-running pre-push validators that run before the network connection opens. It is informational, not coercive.

### Hook discovery

safegit discovers pre-pre-push hooks from two locations: a single-file hook and a directory of hooks. All discovered hooks are executed in a deterministic order, and the first non-zero exit code aborts the push before any network connection is opened. Non-executable files in the directory are skipped with a warning that `safegit doctor` surfaces during health checks.

1. `.git/hooks/pre-pre-push` (single file)
2. `.git/hooks/pre-pre-push.d/*` (lexical order, `*` skips files starting with `.` or ending in `~`)

A hook must be executable (`chmod +x`). Non-executable files in `.d/` are skipped with a warning (`safegit doctor` reports them).

Standard git hooks (post-commit, prepare-commit-msg, etc.) live in `.git/hooks/` as usual and run normally because safegit shells out to git.

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
safegit push [<remote>] [<refspec>...]
  -> resolve refs to push (local-sha, remote-sha)
  -> build hook stdin
  -> for each discovered hook in order:
       run hook with stdin, captured stdout/stderr (streamed to user)
       enforce timeout: hooks.preprepush.timeoutSeconds (default 1800 = 30 min)
       if exit != 0: print stderr, abort, exit code 20
  -> ONLY NOW: open SSH/HTTPS transport via `git push <remote> <refspec>...`
       (git's built-in pre-push still fires AFTER connection; see below)
```

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
- Configurable globally: `hooks.preprepush.timeoutSeconds`.
- Per-hook override: a hook may print `# safegit: timeout=NNN` on its first stdout line (parsed before forwarding to user).
- On timeout: `SIGTERM` then 5s grace then `SIGKILL`. Exit code 21 ("hook timed out").

### Bypass

The `safegit push --no-pre-pre-push` flag skips the entire pre-pre-push hook phase and proceeds directly to the git push. This escape hatch exists for human operators who need to push urgently when a hook is broken or misconfigured. For AI agent environments, this flag should be disallowed via Claude Code permission settings to prevent agents from routinely bypassing validation checks.

## Failure Modes

This section catalogs every known failure mode in safegit's operation, covering process crashes, concurrent access races, network failures, disk exhaustion, and cross-machine usage. For each failure mode the design specifies the observable symptom, the detection mechanism, and the recovery procedure. Recovery is automatic unless explicitly noted as manual or out of scope.

### Process crashes mid-stage

- **Symptom:** orphan tmp directory at `.git/safegit/tmp/<pid>-<rand>/`.
- **Detection:** `safegit doctor` and `safegit doctor --fix` list tmp directories, parse the leading PID, and call `kill -0 pid`. Dead PID = orphan.
- **Recovery:** `safegit doctor --fix` removes the tmp directory. Object DB is unaffected (no orphan blobs from staging; staging only writes blobs for new tree contents and they're GC'd by `git gc` later if unreferenced).

### Process crashes mid-commit (lock held)

- **Symptom:** `locks/refs/heads/<branch>.lock` exists with a dead `pid`.
- **Detection:** any commit acquisition attempt reads the lock, parses `pid` and `host`, calls `kill -0 pid` (Unix) or checks `/proc/<pid>` (Linux). If `host` differs from local `hostname`, treat as alive (cross-machine fence; see Cross-machine / NFS below).
- **Recovery:** the stale lock file is removed via `os.Remove`, then a new lock is created with `O_CREAT|O_EXCL`. The new holder logs a `lock_recovered` op log entry. Corrupt or zero-length lock files (from crashes mid-create) are treated as stale.

### Two invocations commit to same branch simultaneously

- **Symptom:** both reach step 7 of the Commit Pipeline around the same time.
- **Detection:** `O_CREAT|O_EXCL` lock acquisition fails for the second; second waits via exponential-backoff polling.
- **Recovery:** lock orders them. After the first commits and releases, the second wakes, re-resolves parent (now the first invocation's commit), detects CAS miss, rebuilds commit with new parent, then succeeds. End result: linear history, no corruption, both commits land.

### Network failure mid-push

- **Symptom:** `git push` exits non-zero with a transport error after the pre-pre-push hooks have already run.
- **Detection:** non-zero exit from inner `git push`.
- **Recovery:** safegit retries the push up to `push.retryAttempts` (default 3) with exponential backoff (1s, 2s, 4s) for transient errors (DNS, connection reset, TLS handshake). NOT retried: HTTP 401/403, "non-fast-forward", "remote rejected". Pre-pre-push hooks are NOT re-run on retry (they already validated the local state). Op log records each attempt.

### Disk full during object write

- **Symptom:** `git write-tree` or `git commit-tree` fails with `ENOSPC`.
- **Detection:** non-zero exit + stderr scan for `ENOSPC` / "No space left".
- **Recovery:** abort the operation with exit code 9 or 10. The object DB may have partial loose objects -- these are harmless (git `gc` cleans them). No corruption. User must free disk and retry.

### Cross-machine / NFS use is unsupported

safegit supports same-machine concurrency only. Cross-machine concurrency on a shared filesystem (NFS, SSHFS, FUSE-over-network) is out of scope. Rationale: PID-based liveness is meaningless across hosts; `flock(2)` semantics on NFS are unreliable and silently break the safety guarantees.

- **Detection:** `safegit doctor` reads the device of `.git/` via `statfs(2)` and warns if the filesystem type is in a denylist (`nfs`, `nfs4`, `fuse.sshfs`, `cifs`, `smbfs`).
- **Detection at runtime:** lock files include `host`. If a lock holder's `host` differs from local `hostname`, we conservatively treat the lock as alive and wait. After `lock.acquireTimeout`, exit code 8. The user must run `safegit unlock` on the holder's host or accept that cross-machine use is unsupported.
- **Recovery:** documented as an explicit non-goal.

### Pre-pre-push hook hangs

- **Symptom:** hook process doesn't exit within `hooks.preprepush.timeoutSeconds`.
- **Detection:** Go context timeout fires.
- **Recovery:** `SIGTERM`, 5s grace, `SIGKILL`. Push aborts with exit code 21. Op log records `hook_timeout`. No partial state -- we never opened the network connection.

### Raw-git bypass

- **Symptom:** a user (or another tool) ran `git commit` directly. safegit does not install enforcement hooks, so this is allowed by design.
- **Detection:** on any safegit invocation, the op log's most recent ref-update entry is compared against the actual ref tip. If the tip has advanced without an entry, `safegit doctor` and a warning on the next mutating command surface the bypass:

    ```
    WARN: HEAD moved from <sha> to <sha> with no safegit op log entry.
    Possible bypass via raw 'git commit'.
    ```

- **Recovery:** none needed mechanically -- git semantics still hold. We just surface the bypass for transparency.
