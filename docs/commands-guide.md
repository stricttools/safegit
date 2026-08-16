---
title: Commands Guide
description: "Complete reference for every safegit command: commit, undo, push, pull, backup, scan, scrub, doctor and author, with flags, machine-mode output, examples and safety guarantees."
---

# Commands Guide

safegit is a concurrency-safe git wrapper providing atomic commits, oplog-based undo, history rewriting, and multi-agent coordination. This guide covers every command in detail.

## Global Flags

Every safegit command accepts these global flags, which control output verbosity, dry-run previewing, interactive prompt behavior, configuration file location, and machine-readable JSON output mode.

| Flag | Default | Description |
|------|---------|-------------|
| `--quiet` | `false` | Suppress informational output, only showing errors and results |
| `--verbose` | `false` | Enable verbose output with detailed progress and diagnostic info |
| `--dry-run` | `false` | Preview what would happen without writing any changes to disk |
| `--approve-consequential` | `false` | Approve a consequential command up front instead of being asked |
| `--json` | `false` | Select machine mode: stdout carries the framework's envelope and nothing else |
| `--config-file` | optional | Path to a custom safegit config file; omitted means the default location |

The first five are owned by the CLI framework, not by safegit. Three consequences follow:

- **They have no short forms.** `-q`, `-n` and `-y` are gone; write `--quiet`, `--dry-run` and `--approve-consequential`. The approval flag is deliberately unwieldy so it cannot decay into muscle memory.
- **They are recognized anywhere in the command line.** `safegit --dry-run push` and `safegit push --dry-run` are the same run. (`--config-file` is safegit's own and stays before the subcommand.)
- **Only *consequential* commands ask before they run.** Classification (`read_only` / `mutating`) decides what a dry run records; it does not decide what prompts. A command prompts only when it declares itself **consequential**, and in safegit exactly four do: `scrub file`, `scrub match`, `scrub run` and `author rewrite` -- the operations that rewrite history irreversibly. Each prompts `about to run consequential command '<name>'. Proceed? [y/N]` on a terminal, and refuses outright with `error: stdin is not interactive; a consequential command must be confirmed at a terminal` when there is no terminal to ask at. **Everything else -- `commit`, `push`, `pull`, `undo`, `config set` and the guarded passthroughs -- runs bare, with nothing added to the command line.**

`--json` does **not** imply `--quiet`, and it never implies approval. The two are independent: `--quiet` governs the human stream, and the envelope is not written through the writers `--quiet` can reach, so `--json --quiet` still emits the complete document. `--json` says how to answer; it says nothing about consent. Adding it to a command line can therefore never destroy or publish anything on its own.

Under `--dry-run` the framework writes a **would-do log** to stdout after the command's own output, listing every mutation the run would have performed:

```
DRY RUN — no changes were made. Would do:
  1. run: git push origin refs/heads/main:refs/heads/main (granted: push — publishing local refs to a remote is what this command is for)
```

The log is never suppressed by `--quiet`. In machine mode it is not printed as text at all: the same records ride the envelope's `preview` member, so a machine-readable dry run's stdout is still exactly one JSON document. Parse it whole.

## commit

Stage and commit specified files in a single atomic operation. This is safegit's core command -- it uses a per-invocation temporary index to isolate each commit from concurrent sessions, then updates the branch ref with compare-and-swap (CAS) retries.

### When to Use

Use `safegit commit` instead of `git add` + `git commit` whenever multiple sessions might share the same worktree. It prevents index races and file leaks between commits by staging files into an isolated temporary index and updating the branch ref with compare-and-swap retries, so concurrent commits never corrupt each other.

### Flags

| Flag | Short | Presence | Description |
|------|-------|----------|-------------|
| `-m` | `-m` | optional | Commit message line; repeatable for multi-line messages |
| `-F` | `-F` | optional | Read the full commit message body from a file (mutually exclusive with `-m`) |
| `--branch` | | optional | Commit onto a different branch without switching to it |
| `--amend` | | optional; omitted means a new commit | Amend the current HEAD commit by replacing it with updated content |
| `--allow-empty` | | optional; omitted means an empty commit is refused | Allow creating a commit even when no files have been changed |
| `--trailer` | | optional | Add a key-value trailer line to the commit message (repeatable) |

### Arguments

| Name | Required | Description |
|------|----------|-------------|
| `files` | No (variadic) | Files to commit; supports hunk specs like `file.go:1,3` |

### Examples

```bash
# Basic commit
safegit commit -m "fix bug" -- main.go utils.go

# Multi-line commit message
safegit commit -m "feat: add retry logic" -m "Implements exponential backoff for push retries." -- push.go

# Read commit message from a file
safegit commit -F commit-msg.txt -- main.go

# Commit to a different branch without switching
safegit commit -m "backport fix" --branch feature-branch -- fix.go

# Amend the last commit (add files, optionally change message)
safegit commit --amend -m "updated message" -- new-file.go

# Amend without changing the message (preserve existing message)
safegit commit --amend -- forgotten-file.go

# Reword the last commit message (no files)
safegit commit --amend -m "better commit message"

# Commit with a trailer
safegit commit -m "fix: resolve race condition" --trailer "Reviewed-by: Alice" -- lock.go

# Commit selected hunks from a file
safegit commit -m "partial stage" -- main.go:1,3

# Allow an empty commit (no file changes)
safegit commit --allow-empty -m "trigger CI rebuild"
```

### Safety Guarantees

- **Atomic staging**: Each commit uses a per-invocation temporary index. The shared `.git/index` is never written to during the staging phase, so concurrent commits cannot leak files into each other.
- **CAS ref updates**: Branch refs are updated using `git update-ref` with the expected old value. If another session committed between staging and ref update, the CAS fails and the operation retries (up to `commit.casMaxAttempts`, default 5).
- **Per-ref locking**: A lock file is acquired for the target ref before the CAS update, with PID liveness checks to detect and recover from stale locks left by crashed processes.
- **Oplog recording**: Every commit, amend, and reword is logged to an append-only operation log, enabling `safegit undo`.
- **Rename detection**: When a file is renamed (old path deleted, new path created), safegit auto-stages the deletion of the old path.

## undo

Reverse the last commit, amend, or reword operation by reading the append-only operation log (oplog) and restoring the previous branch ref value. Supports undoing multiple operations in one invocation and is session-scoped by default to prevent one session from accidentally rolling back another session's work.

### When to Use

Use `safegit undo` when you need to revert a recent commit, amend, or reword. It reads the oplog to find the correct rollback target, so it works even when multiple sessions have committed to the same branch.

### Flags

| Flag | Presence | Description |
|------|----------|-------------|
| `--bypass-session` | optional; omitted means only this session's operations are undone | Undo across all sessions by ignoring the session ID ownership check |
| `--count` | optional; omitted means one | Number of operations to undo |

### Examples

```bash
# Undo the last commit
safegit undo

# Undo the last 3 operations
safegit undo --count 3

# Undo operations from any session (not just the current one)
safegit undo --bypass-session

# Preview what would be undone
safegit --dry-run undo
```

### Safety Guarantees

- **Session isolation**: By default, only operations from the current session (identified by `CLAUDE_CODE_SESSION_ID`) can be undone. This prevents one session from accidentally undoing another session's work. Use `--bypass-session` to override.
- **CAS ref updates**: The undo uses the oplog's recorded tip SHA as the expected old value in `git update-ref`. If the branch has moved since the oplog entry was written (e.g., another session committed), the CAS fails and the undo is rejected.
- **History rewrite barrier**: If a `scrub` or `rewrite-author` operation is found in the oplog while scanning for undoable operations, the undo is blocked with an error. History rewrites invalidate all prior SHAs, making earlier oplog entries unsafe to undo.
- **Root commit undo**: Undoing the root commit (the first commit in the repo) deletes the branch ref entirely, leaving the branch in an unborn state.
- **Oplog recording**: The undo itself is logged to the oplog, enabling redo-like workflows and audit trails.

## push

Push refs to a remote with pre-pre-push hooks that run before any network I/O, automatic retry with exponential backoff on transient transport errors, and oplog recording of every push operation for audit purposes.

### When to Use

Use `safegit push` instead of `git push` to benefit from pre-pre-push hooks (custom checks that run before git's built-in pre-push hook), automatic retries for transient network failures with exponential backoff, submodule hook cascading from parent repositories, and full oplog recording of every push attempt and result.

### Flags

| Flag | Presence | Description |
|------|----------|-------------|
| `--pre-push-hook` / `--no-pre-push-hook` | optional; omitted means the hooks run | Run pre-pre-push hook scripts before pushing |
| `--force-with-lease` / `--no-force-with-lease` | optional; omitted means an ordinary push | Force push using `--force-with-lease` to prevent overwriting others' work |

### Required Choice: `--refs`

Exactly one value, and there is no default: a push that does not say which refs it publishes is refused.

| Value | Description |
|-------|-------------|
| `head` | Push only the current HEAD branch |
| `branches` | Push all local branches |
| `tags` | Push all local tags without pushing branches |
| `both` | Push all local branches and all tags |

### Arguments

| Name | Required | Default | Description |
|------|----------|---------|-------------|
| `remote` | No | `origin` | Name of the remote repository to push to |

### Examples

```bash
# Push current branch to origin
safegit push --refs head

# Push to a specific remote
safegit push --refs head upstream

# Push all tags
safegit push --refs tags

# Force push with lease (safe force push)
safegit push --refs head --force-with-lease

# Push without running pre-pre-push hooks
safegit push --refs head --no-pre-push-hook

# Push all branches and tags
safegit push --refs both
```

### Safety Guarantees

- **Pre-pre-push hooks**: Hooks installed in `.git/safegit/hooks/` (or `.git/hooks/pre-pre-push.d/`) run before any network I/O. A failing hook aborts the push (exit code 20). A timed-out hook aborts with exit code 21.
- **Submodule hook cascading**: When pushing from inside a submodule, hooks from the parent repo are discovered and run first, then the submodule's own hooks.
- **Automatic retry**: Transport errors (connection refused, DNS failure, TLS errors, broken pipe) trigger automatic retries with exponential backoff (1s, 2s, 4s). Non-transport errors (non-fast-forward, permission denied) are not retried. Default: 3 attempts, configurable via `push.retryAttempts`.
- **Force-with-lease**: The `--force-with-lease` flag uses git's built-in mechanism to refuse the push if the remote has commits the local repo does not know about.
- **Oplog recording**: Every push is logged with the pushed refs, remote, and hook results.

### Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 20 | Pre-pre-push hook failed |
| 21 | Pre-pre-push hook timed out |
| 40 | Git push failed |

## pull

Fetch from a remote and merge into the current branch, requiring an explicit merge strategy selection so the behavior is always predictable and never depends on git's default configuration settings.

### When to Use

Use `safegit pull` as a safer alternative to `git pull` when you need to incorporate upstream changes. It requires an explicit merge strategy flag, runs coordination guards to prevent pulling into a dirty worktree, and separates the fetch and merge into distinct steps for clarity and control.

### Flags

| Flag | Description |
|------|-------------|
| `--merge-strategy` | Fast-forward merge strategy: `ff`, `ff-only`, or `no-ff` (required) |

### Arguments

| Name | Required | Default | Description |
|------|----------|---------|-------------|
| `remote` | No | `origin` | Remote to pull from |
| `branch` | No | | Remote branch to fetch and merge |

### Examples

```bash
# Pull with fast-forward only (safest -- fails if not a fast-forward)
safegit pull --merge-strategy ff-only

# Pull with merge commit if needed
safegit pull --merge-strategy ff

# Pull a specific branch
safegit pull --merge-strategy ff-only origin main
```

### Safety Guarantees

- **Coordination guard**: Refuses to pull if another safegit operation is in progress on the worktree.
- **Explicit merge strategy**: No implicit default merge behavior -- you must choose `ff`, `ff-only`, or `no-ff`.
- **Two-phase**: Runs `git fetch` then `git merge` as separate steps for clarity and control.

## backup

Keep a copy of the current branch on a remote without touching `refs/heads`. Each branch gets exactly one slot, `refs/backups/<branch>`, in a namespace that only safegit writes to. The subcommands are `backup backup` (push the slot), `backup list` (see what is stored), and `backup restore` (fast-forward the branch back onto its slot).

### When to Use

Use it when work exists only on one machine and losing that machine would lose the work, but the work is not ready to publish on a branch. Backups never appear as branches or tags, so nothing about the repository's visible history changes; a backed-up branch is still an ordinary chain of commits that plain git can fetch and merge.

### Arguments

| Name | Required | Default | Description |
|------|----------|---------|-------------|
| `remote` | No | `origin` | Remote holding the backup slots |

### Flags

| Flag | Command | Presence | Description |
|------|---------|----------|-------------|
| `--overwrite-remote-backup` | `backup backup` | optional; omitted means such a slot is a hard error | Replace a slot whose commits are missing from the local history |
| `--allow-public-remote` | `backup backup` | optional; omitted means such a target is a question | Consent to backing up to a remote that is public, or whose visibility safegit cannot determine |

### Examples

```bash
# Back the current branch up to origin
safegit backup backup

# Back up to a different remote
safegit backup backup mymachines

# See every backed-up branch on the remote
safegit backup list

# Preview without pushing anything (local state only, no network contact)
safegit --dry-run backup backup

# Bring a lost branch back (fast-forward only)
safegit backup restore

# Replace a slot that was written from another machine (deliberate data loss)
safegit backup backup --overwrite-remote-backup

# Back up to a public repository, having said so deliberately
safegit backup backup --allow-public-remote
```

### Plain git equivalents

Nothing in a backup slot needs safegit to read back: the slot is an ordinary ref holding an ordinary commit chain, so any git client can fetch it, inspect it, and merge it. That property is deliberate -- if safegit is unavailable on the machine where the backup is needed, the three commands below recover the work by hand. The same operations in raw git are:

```bash
# What "backup backup" does
git ls-remote origin refs/backups/main            # observe the slot
git fetch origin refs/backups/main                # bring its objects local
git merge-base --is-ancestor FETCH_HEAD HEAD      # refuse if it holds foreign work
git push --force-with-lease=refs/backups/main:<observed-sha> \
    origin HEAD:refs/backups/main

# What "backup list" does
git ls-remote origin 'refs/backups/*'

# What "backup restore" does
git fetch origin refs/backups/main
git merge --ff-only FETCH_HEAD
```

### Safety Guarantees

- **Ancestry check before every backup**: the slot is fetched first, and a slot holding commits that are not reachable from the local HEAD is a hard error (exit code 22) naming both SHAs. Overwriting it requires `--overwrite-remote-backup`.
- **Leased push**: the push is pinned with `--force-with-lease` to the SHA observed moments earlier -- or, for a first backup, to "this ref must not exist". A backup pushed from another machine in between is rejected, never clobbered.
- **Public-remote confirmation**: a real backup to a public repository (or to a networked remote whose visibility cannot be determined) asks first, before any network contact. That question is about the target, not the command, and safegit only learns the answer by probing the remote at run time -- so only `--allow-public-remote` answers it. The blanket `--approve-consequential` does not, and under `--json` the backup refuses instead. A declined confirmation exits nonzero -- a refusal never reports success.
- **Dry runs never touch the network**: `--dry-run` builds its preview from local state alone -- no `ls-remote`, no `fetch`, no prompt -- so previewing against an unreachable remote succeeds. The slot's current SHA, the ancestry check against it, and the lease pinned to it are all resolved when the backup actually runs.
- **Hooks bypassed on purpose**: backup pushes run with `--no-verify`. `refs/backups` is a tool-owned namespace, and pre-push policies exist to police branches and tags.
- **Restore never discards work**: the restore is `merge --ff-only`, so a branch carrying commits the backup lacks is refused with the range to inspect.
- **Coordination guard on restore**: a restore refuses to run while another safegit operation holds the worktree.
- **Oplog recording**: every backup and restore is logged with the remote, slot ref, previous SHA, and new SHA.

### Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success (including a declined confirmation) |
| 22 | The remote slot holds work missing from the local history |
| 23 | The branch has no backup slot on the remote |
| 40 | Git push failed |

## scan

Search git history for regex pattern matches across all reachable objects and working tree files, covering blobs, commit messages, tag annotations, trailers, and non-object files like git config and hooks.

### When to Use

Use `safegit scan` to find secrets, credentials, or any pattern across blobs, commit messages, tag annotations, trailers, and non-object files (working tree, `.git/config`, hooks). This is typically a prerequisite to a `scrub` operation.

### Flags

| Flag | Presence | Description |
|------|----------|-------------|
| `--pattern` | required | Regular expression pattern to search for |
| `--scope` | optional | Glob pattern limiting which blob paths are included (e.g., `*.env`, `config/**`) |
| `--from` | optional | First commit hash to include (mutually exclusive with `--entire-history`) |
| `--entire-history` | default `false` | Scan all commits from root to HEAD (mutually exclusive with `--from`) |
| `--target` | optional; omitted means all match types | Comma-separated list of match types: `blobs`, `commits`, `tags`, `trailers`, `files` |

### Examples

```bash
# Scan entire history for an API key pattern
safegit scan --pattern "AKIA[0-9A-Z]{16}" --entire-history

# Scan only .env files
safegit scan --pattern "SECRET_KEY=" --scope "*.env" --entire-history

# Scan only commit messages and trailers
safegit scan --pattern "password" --target commits,trailers --entire-history

# Scan from a specific commit
safegit scan --pattern "aws_secret" --from abc1234

# JSON output for programmatic consumption
safegit --json scan --pattern "token" --entire-history
```

### Safety Guarantees

- **Read-only**: Scan never writes any objects or modifies history.
- **Full object store coverage**: Scans all reachable blobs, commit messages, tag annotations, and non-object files (working tree, git config, hooks).
- **Binary skip**: Binary blobs (containing NUL bytes in the first 8 KB) are automatically skipped.
- **Trailer separation**: When `--target` includes `trailers`, commit matches are split into body-only and trailer-only subsets.

## scrub file

Replace or remove a specific file across all commits in repository history, rewriting each affected commit tree to either substitute the file contents with a sanitized on-disk version or delete the file entirely from every historical snapshot.

### When to Use

Use `safegit scrub file` when you need to remove a leaked secret file (like `.env` or a key file) from every historical commit, or replace it with a sanitized version.

### Flags

| Flag | Presence | Description |
|------|----------|-------------|
| `--from` | required | First commit hash to include when rewriting history |
| `--reason` | required | Mandatory audit trail message explaining why this scrub is needed |
| `--remap-shas-in` | optional | Glob selecting files whose 40-character commit hashes are remapped during rewrite (repeatable) |

### Arguments

| Name | Required | Description |
|------|----------|-------------|
| `file` | Yes | Repository-relative path to the file to scrub from history |

### Examples

```bash
# Remove a file from all history (file does not exist on disk = removal)
safegit scrub file --from abc1234 --reason "leaked API key" -- secrets.env

# Replace a file with the current on-disk version (file exists = replacement)
safegit scrub file --from abc1234 --reason "sanitize credentials" -- config/database.yml

# Dry run to preview without making changes
safegit --dry-run scrub file --from abc1234 --reason "test" -- secrets.env

# With SHA remapping for changelog files
safegit scrub file --from abc1234 --reason "leaked key" --remap-shas-in "*.jsonl" -- .env
```

### Safety Guarantees

- **Clean tree required**: Refuses to run if the working tree has uncommitted changes.
- **Rewrite lock**: Acquires a repository-wide rewrite lock to prevent concurrent scrub operations.
- **Deliberate confirmation**: The framework's consequential gate takes consent before dispatch: on a terminal it prompts, and without one it refuses and names `--approve-consequential`. `--json` never answers it. safegit adds no second prompt behind the gate -- it prints the commit count and scope as a notice, so what the rewrite covers is stated rather than asked twice.
- **Structural verification**: After rewriting, verifies that commit messages, author/committer identity, parent topology, and non-target files are preserved. Only the target file should change.
- **Old blob verification**: Verifies that old (pre-scrub) blob objects are no longer reachable after cleanup.
- **Post-rewrite cleanup**: Expires tainted reflog entries, repacks objects, and prunes unreachable objects.
- **Rewrite maps**: Persists crash-safe rewrite maps to `.git/safegit/rewrite-maps.jsonl`.
- **Submodule support**: Automatically detects if the target file is inside a submodule and rewrites both the submodule's history and the parent's gitlinks.

## scrub match

Replace all occurrences of a regex pattern across every blob, commit message, and tag annotation in repository history, rewriting commit trees so that sensitive values like secrets and credentials are permanently removed from all historical snapshots.

### When to Use

Use `safegit scrub match` when a secret or sensitive value appears across multiple files in history and needs to be replaced everywhere, not just in one file. This command handles blobs, commit messages, and tag annotations in a single pass, with optional scope filtering and mangle mode for randomized replacements.

### Flags

| Flag | Presence | Description |
|------|----------|-------------|
| `--pattern` | required | Regular expression pattern to search for |
| `--reason` | required | Mandatory audit trail message |
| `--scope` | optional | Glob pattern limiting which file paths are searched |
| `--remap-shas-in` | optional | Glob for SHA remapping in affected files (repeatable) |

### `substitution` — required, exactly one

Each alternative is its own flag; supplying neither, or both, is refused by the parser.

| Flag | Description |
|------|-------------|
| `--replace <str>` | Literal string to substitute for each regex match |
| `--mangle` | Replace matches with random printable ASCII of the same length |

### `range` — required, exactly one

| Flag | Description |
|------|-------------|
| `--from <sha>` | First commit hash to include (rewrite from this point to HEAD) |
| `--entire-history` | Rewrite all commits from root to HEAD |

### Examples

```bash
# Replace a specific API key with a placeholder
safegit scrub match --pattern "sk-live-[a-zA-Z0-9]{24}" --replace "sk-live-REDACTED" \
  --reason "leaked Stripe key" --entire-history

# Mangle all matches (random replacement preserving length)
safegit scrub match --pattern "ghp_[a-zA-Z0-9]{36}" --mangle \
  --reason "leaked GitHub token" --entire-history

# Scope to only .env files
safegit scrub match --pattern "DATABASE_URL=.*" --replace "DATABASE_URL=REDACTED" \
  --scope "*.env" --reason "leaked DB URL" --entire-history

# Dry run to see what would match
safegit --dry-run scrub match --pattern "password" --replace "REDACTED" \
  --reason "test" --entire-history

# With SHA remapping for changelog JSONL files
safegit scrub match --pattern "secret_value" --replace "REDACTED" \
  --reason "leaked secret" --remap-shas-in "*.jsonl" --entire-history
```

### Safety Guarantees

- All guarantees from `scrub file` apply.
- **Blob + message + tag rewriting**: Replaces matches in blobs, commit messages, and tag annotations.
- **Mangle mode**: Uses crypto/rand for random character generation, preserving whitespace structure.
- **Scope filtering**: When `--scope` is set, only blobs at matching paths are rewritten; out-of-scope blobs are untouched.
- **Post-scrub verification**: Re-scans the entire object store to confirm no matches survive.
- **Policy recording**: Appends a scrub policy entry to `.git/safegit/scrub-policies.jsonl` for future verification with `scrub verify`.
- **Submodule support**: Scans and rewrites submodule histories, then updates parent gitlinks.

## scrub run

Execute a multi-operation scrub recipe from a TOML file, applying all pattern replacements and file removals across history in a single coordinated pass with topological ordering, overlap detection, and automatic post-scrub verification.

### When to Use

Use `safegit scrub run` when multiple patterns need to be scrubbed simultaneously. A recipe file defines all operations, their dependencies, and scopes. Operations are applied in topological order with overlap detection.

### Flags

| Flag | Presence | Description |
|------|----------|-------------|
| `--reason` | required | Mandatory audit trail message |
| `--diff` | optional; omitted means the rewrite is performed | Preview changes as unified diffs without modifying objects (mutually exclusive with `--dry-run`) |
| `--limit` | optional; omitted means 50 | Maximum number of blob diffs to show in `--diff` mode |
| `--remap-shas-in` | optional | Glob for SHA remapping (repeatable) |

### `range` — required, exactly one

| Flag | Description |
|------|-------------|
| `--from <sha>` | First commit hash to include |
| `--entire-history` | Rewrite all commits from root to HEAD |

### Arguments

| Name | Required | Description |
|------|----------|-------------|
| `recipe` | Yes | Path to the TOML recipe file |

### Recipe File Format

```toml
[[operations]]
pattern = "AKIA[0-9A-Z]{16}"
replace = "AKIA_REDACTED"
scope = "*.env"

[[operations]]
pattern = "ghp_[a-zA-Z0-9]{36}"
mangle = true

[[operations]]
pattern = "post-cleanup-pattern"
replace = "CLEAN"
depends_on = [0, 1]  # runs after operations 0 and 1
```

Each operation requires:
- `pattern`: regex string (required)
- Exactly one of `replace` (literal string) or `mangle = true`
- `scope`: optional glob to limit which file paths are affected
- `target`: optional, one of `"blobs"`, `"commits"`, `"tags"` (default: all)
- `depends_on`: optional array of zero-indexed operation indices (must form a DAG)

### Examples

```bash
# Run a recipe
safegit scrub run --reason "quarterly secret rotation" --entire-history -- recipe.toml

# Preview changes as diffs
safegit scrub run --diff --entire-history -- recipe.toml

# Dry run (match count summary without diffs)
safegit --dry-run scrub run --reason "test" --entire-history -- recipe.toml

# Limit diff preview to 20 blobs
safegit scrub run --diff --limit 20 --entire-history -- recipe.toml
```

### Safety Guarantees

- All guarantees from `scrub match` apply.
- **Topological ordering**: Operations are sorted by dependency graph (Kahn's algorithm). Independent operations are applied simultaneously against the original content; dependent operations match against post-dependency content.
- **Overlap detection**: Overlapping byte ranges across independent operations are a hard error.
- **Cycle detection**: Circular dependencies in the recipe's `depends_on` graph are detected and rejected at parse time.
- **Per-operation policies**: Each operation's pattern is recorded as a separate scrub policy entry.

## scrub verify

Check all scrub policies recorded in the repository configuration to confirm that previously scrubbed secrets and sensitive patterns remain completely absent from every blob, commit message, and tag annotation in the git object store.

### When to Use

Run `safegit scrub verify` periodically or in CI to confirm that scrubbed secrets have not been reintroduced into the repository. Policies are created automatically by `scrub match` and `scrub run`, and verification scans all blobs, commit messages, and tag annotations using batched multi-pattern scanning for efficiency.

### Flags

No command-specific flags beyond the global flags described above. Use `--json` for machine-readable output that can be parsed by CI pipelines, and `--quiet` to suppress informational messages while still reporting verification failures.

### Examples

```bash
# Verify all scrub policies
safegit scrub verify

# JSON output
safegit --json scrub verify
```

### Safety Guarantees

- **Read-only**: Does not modify any objects.
- **Full scan**: Scans all blobs, commit messages, and tag annotations for each policy pattern.
- **Scope-aware**: Scoped policies only flag matches at paths within their scope.
- **Batched scan**: Uses multi-pattern scanning to avoid iterating the object store once per policy.

## doctor

Run diagnostic health checks on the repository and optionally repair issues such as stale lock files from crashed processes, orphan temporary index directories, configuration schema problems, hook script permission errors, and bypass detection where raw git commits were made outside safegit's isolation guarantees. Supports diagnose-only, fix, and full uninstall modes.

### When to Use

Use `safegit doctor` to diagnose and optionally repair repository health problems including stale lock files left by crashed processes, orphan temporary index directories, configuration file corruption, hook permission errors, and raw git commit bypass detection via oplog comparison.

### Required Choice: `--action`

Exactly one value, and there is no default: a doctor invocation that does not say what it does with its findings is refused.

| Value | Description |
|-------|-------------|
| `diagnose` | Run all health checks and report results without fixing |
| `fix` | Run all health checks and automatically repair issues found |
| `uninstall` | Remove all safegit hooks and metadata from this repository |

### Health Checks

1. **initialized**: Is safegit initialized in this repo?
2. **tmp_dirs**: Are there orphan temporary index directories?
3. **stale_locks**: Are there stale lock files from crashed processes?
4. **config**: Is the config file readable with a valid schema version?
5. **bypass_detect**: Does the branch tip match the last oplog entry? (Detects raw `git commit` bypassing safegit.)
6. **filesystem**: Is the repo on a network filesystem (NFS/SMB) that may not support atomic operations?
7. **hook_perms**: Are all hook scripts executable?

### Examples

```bash
# Run diagnostics only
safegit doctor --action diagnose

# Run diagnostics and fix issues
safegit doctor --action fix

# Dry-run fix (see what would be cleaned without doing it)
safegit --dry-run doctor --action fix

# Uninstall safegit from this repo
safegit doctor --action uninstall
```

### What `--action fix` Repairs

- Removes orphan temporary index directories
- Removes the legacy queue directory (from safegit v0.1)
- Removes stale lock files (only those whose owning process is dead)
- Rotates the oplog if it exceeds the configured max size (default 100 MB)
- Cleans up submodule safegit directories

## unlock

Release a stale `.lock` file left behind by a crashed git or safegit process, after verifying that the lock's owning process is actually dead via PID liveness checks to prevent releasing locks held by live processes.

### When to Use

Use `safegit unlock` when a safegit or git operation crashed and left a lock file behind, preventing new operations on that ref. The command checks that the lock's owning process is actually dead before releasing it.

### Arguments

| Name | Required | Description |
|------|----------|-------------|
| `ref` | Yes | The ref name whose stale lock file to remove (e.g., `main` or `refs/heads/main`) |

### Examples

```bash
# Release a stale lock on the main branch
safegit unlock main

# Release using full ref name
safegit unlock refs/heads/feature-branch

# Dry run
safegit --dry-run unlock main
```

### Safety Guarantees

- **Liveness check**: Refuses to release locks held by live processes. If the owning process is still running, the unlock fails with an error suggesting you kill the process or wait.
- **PID verification**: Parses the lock file to determine the owning PID and checks if that process is alive.

## author list

List all distinct author and committer identities across the entire commit history, showing name, email, role, and commit count for each unique identity to help audit repositories for identity variations before performing a rewrite.

### When to Use

Use `safegit author list` to audit a repository for identity variations such as typos, old email addresses, bot accounts, and duplicate identities that should be consolidated before performing a rewrite. The output is sorted by frequency, making the most prolific identities easy to identify.

### Examples

```bash
# List all identities
safegit author list

# JSON output
safegit --json author list
```

### Output Format

A table showing Name, Email, Role (author/committer/both), and Count columns, sorted by frequency so the most common identities appear first. In JSON mode, each identity is emitted as a separate object with all fields.

## author check

Check that all commits in the repository history use the expected author and committer identity, scanning every commit and reporting deviations with exact commit hashes, mismatched fields, and suggested rewrite commands.

### When to Use

Use `safegit author check` to find commits that deviate from the expected identity by scanning the entire commit history and comparing each commit's author and committer fields against the specified name and email. The command suggests the appropriate `safegit author rewrite` command to fix each deviation found.

### Flags

| Flag | Presence | Description |
|------|----------|-------------|
| `--name` | optional | Expected author/committer display name |
| `--email` | optional | Expected author/committer email address |

At least one of `--name` or `--email` is required.

### Examples

```bash
# Check for name deviations
safegit author check --name "Alice Smith"

# Check for email deviations
safegit author check --email "alice@example.com"

# Check both
safegit author check --name "Alice Smith" --email "alice@example.com"

# JSON output
safegit --json author check --name "Alice Smith"
```

## author rewrite

Rewrite author and committer name or email across all commits in the repository history, replacing every occurrence of the old identity with the new one while preserving timestamps, commit messages, tree contents, and parent relationships.

### When to Use

Use `safegit author rewrite` to correct identity mistakes such as wrong names, old email addresses, or bot account identities across the entire repository history, including identity-bearing trailers like Signed-off-by and Co-authored-by, and tagger fields on annotated tags.

### Flags

| Flag | Presence | Description |
|------|----------|-------------|
| `--old-name` | optional | Current name to search for and replace |
| `--new-name` | optional | New name to substitute |
| `--old-email` | optional | Current email to search for and replace |
| `--new-email` | optional | New email to substitute |

### Constraints

The two pairs and the requirement that at least one of them be supplied are declared, and the parser enforces them before the command runs:

```
author-name      all or none of --old-name, --new-name
author-email     all or none of --old-email, --new-email
author-change    at least one of (--old-name with --new-name), (--old-email with --new-email)
```

Both pairs can be specified simultaneously. A command line naming half a pair is refused by `author-name` or `author-email`; one naming neither pair is refused by `author-change`. Both refusals exit **1** (the framework's parse-error code). Before 0.28.0 the missing-pair case was a hand-written check in the handler that exited 2.

### Examples

```bash
# Rewrite name
safegit author rewrite --old-name "alice" --new-name "Alice Smith"

# Rewrite email
safegit author rewrite --old-email "alice@old.com" --new-email "alice@new.com"

# Rewrite both name and email
safegit author rewrite --old-name "alice" --new-name "Alice Smith" \
  --old-email "alice@old.com" --new-email "alice@new.com"

# Dry run
safegit --dry-run author rewrite --old-name "alice" --new-name "Alice Smith"
```

### Safety Guarantees

- **Clean tree required**: Refuses to run with uncommitted changes.
- **Rewrite lock**: Acquires a repository-wide rewrite lock.
- **Deliberate confirmation**: The framework's consequential gate takes consent before dispatch and `--json` never answers it; pass `--approve-consequential` from a script. safegit adds no second prompt behind the gate -- the number of commits about to be rewritten is printed as a notice.
- **Snapshot verification**: Takes a full snapshot of repository state (commit count, tag count, branch names, tag names, messages, dates, tree hashes, parent topology) before and after the rewrite, then compares them. All invariants except the target identity fields must match exactly.
- **Trailer rewriting**: Also rewrites identity-bearing trailers (Signed-off-by, Co-authored-by, etc.) to match the new identity.
- **Tag rewriting**: Annotated tag objects are rewritten when their tagger name/email matches the old identity.
- **AND/OR matching**: When both `--old-name` and `--old-email` are specified, a commit must match both to be rewritten (AND). When only one is specified, any commit matching that field is rewritten (OR).

## checkout

Checkout a branch or ref with working-tree safety guards that prevent checking out while another safegit operation is in progress, syncing the main index after checkout and recording the operation in the oplog.

### When to Use

Use `safegit checkout` instead of `git checkout` to get coordination guards that prevent checking out while another safegit operation is in progress, protecting uncommitted work from other sessions that may be sharing the same worktree and ensuring the main index stays consistent after the checkout completes.

### Arguments

All arguments are passed through to `git checkout` after the coordination guard passes. Any flag or positional argument that `git checkout` accepts can be used, including branch names, commit hashes, `--create` (`-b`), and path specs.

### Examples

```bash
safegit checkout main
safegit checkout -b new-feature
safegit checkout v1.0.0
```

### Safety Guarantees

- **Coordination guard**: Checks for in-progress safegit operations before proceeding.
- **Index sync**: Runs `git read-tree HEAD` after checkout to keep the main index consistent.
- **Oplog recording**: Logs the checkout with old and new HEAD SHAs.

## merge

Merge a branch into the current HEAD with working-tree safety guards that check for in-progress safegit operations, sync the main index after completion, and record the merge in the oplog for audit purposes.

### When to Use

Use `safegit merge` instead of `git merge` for coordination-guarded merges that verify no other safegit operation is in progress before proceeding, preventing data loss when multiple sessions share a worktree and one session's uncommitted work could be clobbered by the merge.

### Arguments

All arguments are passed through to `git merge` after the coordination guard passes. Any flag or positional argument that `git merge` accepts can be used, including branch names, `--no-ff`, `--squash`, and merge strategy options.

### Examples

```bash
safegit merge feature-branch
safegit merge --no-ff feature-branch
```

### Safety Guarantees

- **Coordination guard**: Refuses to merge if another safegit operation is in progress.
- **Index sync**: Syncs the main index after the merge.
- **Oplog recording**: Logs the merge with branch name and result SHA.

## rebase

Rebase the current branch onto an upstream ref with coordination safety guards that check for in-progress operations, sync the main index after completion, and record the rebase in the oplog for audit trail purposes.

### When to Use

Use `safegit rebase` instead of `git rebase` for coordination-guarded rebasing that verifies no other safegit operation is in progress before proceeding, preventing data loss when multiple sessions share a worktree and one session has uncommitted edits in the working tree.

### Arguments

All arguments are passed through to `git rebase` after the coordination guard passes. Any flag or positional argument that `git rebase` accepts can be used, including upstream refs, `--interactive`, `--onto`, and `--autosquash`.

### Examples

```bash
safegit rebase main
safegit rebase --interactive HEAD~5
```

### Safety Guarantees

- **Coordination guard**: Refuses to rebase if another safegit operation is in progress.
- **Index sync**: Syncs the main index after rebase completes.
- **Oplog recording**: Logs the rebase with the upstream ref.

## reset

Reset HEAD with selective guards that activate only for `--hard` resets to prevent accidental data loss from tree-mutating operations, while allowing soft and mixed resets to pass through without coordination checks.

### When to Use

Use `safegit reset` instead of `git reset` to get selective coordination guards. The guard only activates for `--hard` resets because those are tree-mutating operations that can destroy uncommitted work from other sessions. Soft and mixed resets pass through without the coordination guard since they do not modify the working tree.

### Arguments

All arguments are passed through to `git reset` after the coordination guard passes (for `--hard` only). Any flag or positional argument that `git reset` accepts can be used, including `--soft`, `--mixed`, `--hard`, commit refs, and path specs.

### Examples

```bash
# Soft reset (no guard needed)
safegit reset --soft HEAD~1

# Hard reset (guarded)
safegit reset --hard HEAD~3
```

### Safety Guarantees

- **Selective guard**: Only `--hard` resets trigger the coordination check.
- **Index sync**: Syncs the main index after a hard reset.
- **Oplog recording**: Logs the reset with all arguments.

## bisect

Binary search through commits to find the commit that introduced a bug, with selective coordination guards that activate only for tree-moving subcommands like good, bad, reset, and start to protect the working tree from concurrent modification.

### When to Use

Use `safegit bisect` instead of `git bisect` for coordination-guarded bisecting that protects the working tree from concurrent modification by other sessions sharing the same worktree, with selective guards that only activate for tree-moving subcommands.

### Arguments

All arguments are passed through to `git bisect` after the coordination guard passes (for tree-moving subcommands only). Any subcommand that `git bisect` accepts can be used, including start, good, bad, old, new, reset, skip, log, and replay.

### Examples

```bash
safegit bisect start
safegit bisect bad HEAD
safegit bisect good v1.0.0
safegit bisect reset
```

### Safety Guarantees

- **Selective guard**: Only tree-moving subcommands (`good`, `bad`, `old`, `new`, `reset`, `start`) trigger the coordination check.
- **Index sync**: Syncs the main index after bisect operations.

## cherry-pick

Cherry-pick one or more commits onto the current HEAD with coordination safety guards that check for in-progress safegit operations, sync the main index after completion, and record the cherry-pick in the oplog.

### When to Use

Use `safegit cherry-pick` instead of `git cherry-pick` for coordination-guarded cherry-picks that verify no other safegit operation is in progress before applying commits, protecting uncommitted work from other sessions sharing the same worktree.

### Arguments

All arguments are passed through to `git cherry-pick` after the coordination guard passes. Any flag or positional argument that `git cherry-pick` accepts can be used, including multiple commit hashes, ranges, `--no-commit`, and `--mainline`.

### Examples

```bash
safegit cherry-pick abc1234
safegit cherry-pick abc1234 def5678
```

### Safety Guarantees

- **Coordination guard**: Checks for in-progress operations before proceeding.
- **Index sync**: Syncs the main index after cherry-pick.
- **Oplog recording**: Logs the operation.

## revert

Revert one or more commits by creating inverse patches, with coordination safety guards that check for in-progress safegit operations, sync the main index after completion, and record the revert in the oplog for audit purposes.

### When to Use

Use `safegit revert` instead of `git revert` for coordination-guarded reverts that verify no other safegit operation is in progress before applying inverse patches, protecting uncommitted work from other sessions sharing the same worktree.

### Arguments

All arguments are passed through to `git revert` after the coordination guard passes. Any flag or positional argument that `git revert` accepts can be used, including commit hashes, ranges, `--no-commit`, and `--mainline` for merge reverts.

### Examples

```bash
safegit revert abc1234
safegit revert HEAD~3..HEAD
```

### Safety Guarantees

Same safety guarantees as the cherry-pick command: the coordination guard checks for in-progress safegit operations before proceeding to prevent data loss in shared worktrees, the main index is synced with HEAD after completion to keep it consistent, and the operation is recorded in the oplog for audit trail and undo purposes.

## config show

Show all configuration values currently in effect for this repository, including built-in defaults and any user overrides from the `.git/safegit/config.json` file. Values are printed as key-value pairs to stdout for inspection and debugging, with each key showing its current effective value whether from the config file or a built-in default.

### Examples

```bash
safegit config show
```

Prints all config keys with their current values (including defaults).

## config get

Get the current value of a single configuration key from the `.git/safegit/config.json` file, printing the raw value to stdout so it can be captured by scripts or used in automation pipelines.

### Arguments

| Name | Required | Description |
|------|----------|-------------|
| `key` | Yes | Configuration key to retrieve |

### Examples

```bash
safegit config get commit.casMaxAttempts
safegit config get push.retryAttempts
```

## config set

Set a configuration key to a new value in the `.git/safegit/config.json` file, creating the file if it does not exist yet and persisting the change for all future safegit invocations in this repository.

### Arguments

| Name | Required | Description |
|------|----------|-------------|
| `key` | Yes | Configuration key to set |
| `value` | Yes | New value |

### Examples

```bash
safegit config set commit.casMaxAttempts 10
safegit config set push.retryAttempts 5
safegit config set lock.acquireTimeoutSeconds 60
```

Configuration is stored in `.git/safegit/config.json`.

## hook list

List all pre-pre-push hooks currently installed in the `.git/safegit/hooks/` directory, showing each hook's name, file path, and whether it is executable, so you can audit which checks run before every push.

### Examples

```bash
safegit hook list
```

Shows each hook's name and file path.

## hook run

Run all installed pre-pre-push hooks (or a single named hook) immediately without performing an actual push, so you can verify that all configured hook checks pass before committing to a real push operation.

### Arguments

| Name | Required | Description |
|------|----------|-------------|
| `name` | No | Name of a specific hook to run; omit to run all |

### Examples

```bash
# Run all hooks
safegit hook run

# Run a specific hook
safegit hook run my-check.sh
```

### `--dry-run` is refused

`hook run` declares `dry_run_supported=false`. A hook is a script the operator
supplied; safegit cannot know what it does, and the effects handle has no way to
mint a subprocess that is fed stdin, so any would-do log rendered here would be
invented. Passing `--dry-run` therefore fails with the reason instead of
pretending. Use `safegit hook list` to see which scripts a push would run.

## hook install

Install a pre-pre-push hook by copying a script file into the `.git/safegit/hooks/` directory and making it executable, so it will run automatically before every `safegit push` operation performs any network I/O.

### Arguments

| Name | Required | Description |
|------|----------|-------------|
| `path` | Yes | Path to the hook script file to install |

### Examples

```bash
safegit hook install ./scripts/lint-check.sh
```

The script is copied to `.git/safegit/hooks/` and made executable.

## version

Print the safegit binary version, Go runtime version with platform architecture, and the installed git version in a human-readable format. This command provides all the version information needed for bug reports, compatibility checks, and verifying that the correct safegit binary is installed on the system.

### Examples

```bash
safegit version
```

Output:

```
safegit 0.22.0
go      go1.23.0 linux/amd64
git     git version 2.47.0
```

## Configuration Reference

All safegit configuration is stored in `.git/safegit/config.json` and managed via the `config show`, `config get`, and `config set` subcommands. The configuration controls commit CAS retry behavior, lock acquisition timeouts, pre-pre-push hook execution timeouts, push retry attempts for transport errors, and oplog rotation size limits. The following keys are available with their default values:

| Key | Default | Description |
|-----|---------|-------------|
| `schemaVersion` | `1` | Config file schema version |
| `commit.casMaxAttempts` | `5` | Maximum CAS retry attempts for commits |
| `commit.autoBumpParent` | `nil` | Auto-bump parent repo's submodule pointer on commit |
| `lock.acquireTimeoutSeconds` | `30` | Timeout for acquiring ref locks |
| `hooks.preprepush.timeoutSeconds` | `1800` | Timeout for pre-pre-push hooks (30 minutes) |
| `push.retryAttempts` | `3` | Number of push retry attempts on transport errors |
| `log.maxSizeMB` | `100` | Maximum oplog size before rotation |

## Exit Code Reference

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |
| 2 | Usage / argument error |
| 3 | Not a git repository |
| 4 | safegit not initialized |
| 5 | Coordination guard failed (another operation in progress) |
| 7 | CAS retries exhausted |
| 9 | write-tree failed |
| 10 | commit-tree failed |
| 20 | Pre-pre-push hook failed |
| 21 | Pre-pre-push hook timed out |
| 40 | Git push failed |
