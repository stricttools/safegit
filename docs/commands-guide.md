---
title: Commands Guide
description: Detailed reference for all safegit commands with flags, examples, and safety guarantees.
---

# Commands Guide

safegit is a concurrency-safe git wrapper providing atomic commits, oplog-based undo, history rewriting, and multi-agent coordination. This guide covers every command in detail.

## Global Flags

Every safegit command accepts these global flags:

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--quiet` | `-q` | `false` | Suppress informational output, only showing errors and results |
| `--verbose` | | `false` | Enable verbose output with detailed progress and diagnostic info |
| `--dry-run` | `-n` | `false` | Preview what would happen without writing any changes to disk |
| `--yes` | `-y` | `false` | Automatically confirm all interactive prompts |
| `--config-file` | | `""` | Path to a custom safegit config file instead of the default location |
| `--json` | | `false` | Emit machine-readable JSON output to stdout (implies `--quiet` and `--yes`) |

## commit

Stage and commit specified files in a single atomic operation. This is safegit's core command -- it uses a per-invocation temporary index to isolate each commit from concurrent sessions, then updates the branch ref with compare-and-swap (CAS) retries.

### When to Use

Use `safegit commit` instead of `git add` + `git commit` whenever multiple sessions might share the same worktree. It prevents index races and file leaks between commits.

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `-m` | `-m` | | Commit message line; repeatable for multi-line messages |
| `-F` | `-F` | `nil` | Read the full commit message body from a file (mutually exclusive with `-m`) |
| `--branch` | | `nil` | Commit onto a different branch without switching to it |
| `--amend` | | `false` | Amend the current HEAD commit by replacing it with updated content |
| `--allow-empty` | | `false` | Allow creating a commit even when no files have been changed |
| `--trailer` | | | Add a key-value trailer line to the commit message (repeatable) |

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

Reverse the last commit, amend, or reword operation using the operation log (oplog). Supports undoing multiple operations in one invocation.

### When to Use

Use `safegit undo` when you need to revert a recent commit, amend, or reword. It reads the oplog to find the correct rollback target, so it works even when multiple sessions have committed to the same branch.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--bypass-session` | `false` | Undo across all sessions by ignoring the session ID ownership check |
| `--count` | `1` | Number of operations to undo |

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

Push refs to a remote with pre-pre-push hooks and automatic retry on transport errors.

### When to Use

Use `safegit push` instead of `git push` to benefit from pre-pre-push hooks (custom checks that run before git's built-in pre-push hook) and automatic retries for transient network failures.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--pre-push-hook` / `--no-pre-push-hook` | `true` | Run pre-pre-push hook scripts before pushing |
| `--force-with-lease` / `--no-force-with-lease` | `false` | Force push using `--force-with-lease` to prevent overwriting others' work |

### Mutex Group (pick at most one)

| Flag | Default | Description |
|------|---------|-------------|
| `--only-head` | `false` | Push only the current HEAD branch |
| `--only-branches` | `false` | Push all local branches |
| `--only-tags` | `false` | Push all local tags without pushing branches |
| `--both-branches-and-tags` | `false` | Push all local branches and all tags |

### Arguments

| Name | Required | Default | Description |
|------|----------|---------|-------------|
| `remote` | No | `origin` | Name of the remote repository to push to |

### Examples

```bash
# Push current branch to origin (default)
safegit push

# Push to a specific remote
safegit push upstream

# Push all tags
safegit push --only-tags

# Force push with lease (safe force push)
safegit push --force-with-lease

# Push without running pre-pre-push hooks
safegit push --no-pre-push-hook

# Push all branches and tags
safegit push --both-branches-and-tags
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

Fetch from a remote and merge, with explicit merge strategy selection.

### When to Use

Use `safegit pull` as a safer alternative to `git pull`. It requires an explicit merge strategy and runs coordination guards to prevent pulling into a dirty worktree.

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

## scan

Search git history for regex pattern matches across all objects and working tree files.

### When to Use

Use `safegit scan` to find secrets, credentials, or any pattern across blobs, commit messages, tag annotations, trailers, and non-object files (working tree, `.git/config`, hooks). This is typically a prerequisite to a `scrub` operation.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--pattern` | (required) | Regular expression pattern to search for |
| `--scope` | `nil` | Glob pattern limiting which blob paths are included (e.g., `*.env`, `config/**`) |
| `--from` | `nil` | First commit hash to include (mutually exclusive with `--entire-history`) |
| `--entire-history` | `false` | Scan all commits from root to HEAD (mutually exclusive with `--from`) |
| `--target` | `nil` | Comma-separated list of match types: `blobs`, `commits`, `tags`, `trailers`, `files` (default: all) |

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

Replace or remove a specific file across all commits in repository history.

### When to Use

Use `safegit scrub file` when you need to remove a leaked secret file (like `.env` or a key file) from every historical commit, or replace it with a sanitized version.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--from` | (required) | First commit hash to include when rewriting history |
| `--reason` | (required) | Mandatory audit trail message explaining why this scrub is needed |
| `--remap-shas-in` | | Glob selecting files whose 40-character commit hashes are remapped during rewrite (repeatable) |

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
- **Orchestration guard**: In rlsbl-managed repositories, destructive scrubs must run under release-tool orchestration (set `RLSBL_SCRUB_ORCHESTRATED=1`). Dry-run stays available.
- **Interactive confirmation**: Prompts for confirmation before rewriting (skippable with `--yes`).
- **Structural verification**: After rewriting, verifies that commit messages, author/committer identity, parent topology, and non-target files are preserved. Only the target file should change.
- **Old blob verification**: Verifies that old (pre-scrub) blob objects are no longer reachable after cleanup.
- **Post-rewrite cleanup**: Expires tainted reflog entries, repacks objects, and prunes unreachable objects.
- **Rewrite maps**: Persists crash-safe rewrite maps to `.git/safegit/rewrite-maps.jsonl`.
- **Submodule support**: Automatically detects if the target file is inside a submodule and rewrites both the submodule's history and the parent's gitlinks.

## scrub match

Replace all occurrences of a regex pattern across every blob in repository history.

### When to Use

Use `safegit scrub match` when a secret or sensitive value appears across multiple files in history and needs to be replaced everywhere (not just in one file).

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--pattern` | (required) | Regular expression pattern to search for |
| `--reason` | (required) | Mandatory audit trail message |
| `--scope` | `nil` | Glob pattern limiting which file paths are searched |
| `--remap-shas-in` | | Glob for SHA remapping in affected files (repeatable) |

### Mutex Group: Replacement Mode (pick one)

| Flag | Default | Description |
|------|---------|-------------|
| `--replace` | `nil` | Literal string to substitute for each regex match |
| `--mangle` | `false` | Replace matches with random printable ASCII of the same length |

### Mutex Group: Range (pick one)

| Flag | Default | Description |
|------|---------|-------------|
| `--from` | `nil` | First commit hash to include (rewrite from this point to HEAD) |
| `--entire-history` | `false` | Rewrite all commits from root to HEAD |

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

Execute a multi-operation scrub recipe from a TOML file, applying all operations in a single coordinated pass.

### When to Use

Use `safegit scrub run` when multiple patterns need to be scrubbed simultaneously. A recipe file defines all operations, their dependencies, and scopes. Operations are applied in topological order with overlap detection.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--reason` | (required) | Mandatory audit trail message |
| `--diff` | `false` | Preview changes as unified diffs without modifying objects (mutually exclusive with `--dry-run`) |
| `--limit` | `50` | Maximum number of blob diffs to show in `--diff` mode |
| `--remap-shas-in` | | Glob for SHA remapping (repeatable) |

### Mutex Group: Range (pick one)

| Flag | Default | Description |
|------|---------|-------------|
| `--from` | `nil` | First commit hash to include |
| `--entire-history` | `false` | Rewrite all commits from root to HEAD |

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

Check all scrub policies to confirm previously scrubbed patterns remain absent from the object store.

### When to Use

Run `safegit scrub verify` periodically or in CI to confirm that scrubbed secrets have not been reintroduced. Policies are created automatically by `scrub match` and `scrub run`.

### Flags

No command-specific flags. Uses global flags only.

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

Run diagnostic health checks on the repository and optionally repair issues.

### When to Use

Use `safegit doctor` to diagnose problems (stale locks, orphan temp directories, config issues, hook permission errors) and optionally fix them.

### Flags (Mutex Group -- pick at most one)

| Flag | Default | Description |
|------|---------|-------------|
| `--diagnose` | `false` | Run all health checks and report results without fixing |
| `--fix` | `false` | Run all health checks and automatically repair issues found |
| `--uninstall` | `false` | Remove all safegit hooks and metadata from this repository |

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
safegit doctor --diagnose

# Run diagnostics and fix issues
safegit doctor --fix

# Dry-run fix (see what would be cleaned without doing it)
safegit --dry-run doctor --fix

# Uninstall safegit from this repo
safegit doctor --uninstall
```

### What `--fix` Repairs

- Removes orphan temporary index directories
- Removes the legacy queue directory (from safegit v0.1)
- Removes stale lock files (only those whose owning process is dead)
- Rotates the oplog if it exceeds the configured max size (default 100 MB)
- Cleans up submodule safegit directories

## unlock

Release a stale `.lock` file left behind by a crashed git or safegit process.

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

List all distinct author and committer identities across the entire commit history.

### When to Use

Use `safegit author list` to audit a repository for identity variations (typos, old email addresses, bot accounts) before performing a rewrite.

### Examples

```bash
# List all identities
safegit author list

# JSON output
safegit --json author list
```

### Output Format

A table showing Name, Email, Role (author/committer/both), and Count, sorted by frequency.

## author check

Check that all commits use the expected author and committer identity.

### When to Use

Use `safegit author check` to find commits that deviate from the expected identity. The command suggests the appropriate `safegit author rewrite` command to fix deviations.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--name` | `nil` | Expected author/committer display name |
| `--email` | `nil` | Expected author/committer email address |

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

Rewrite author and committer name or email across all commit history.

### When to Use

Use `safegit author rewrite` to correct identity mistakes (wrong name, old email) across the entire repository history.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--old-name` | `nil` | Current name to search for and replace |
| `--new-name` | `nil` | New name to substitute (required if `--old-name` is set) |
| `--old-email` | `nil` | Current email to search for and replace |
| `--new-email` | `nil` | New email to substitute (required if `--old-email` is set) |

At least one pair (`--old-name`/`--new-name` or `--old-email`/`--new-email`) is required. Both pairs can be specified simultaneously.

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
- **Orchestration guard**: In rlsbl-managed repos, must be coordinated with the release tooling.
- **Snapshot verification**: Takes a full snapshot of repository state (commit count, tag count, branch names, tag names, messages, dates, tree hashes, parent topology) before and after the rewrite, then compares them. All invariants except the target identity fields must match exactly.
- **Trailer rewriting**: Also rewrites identity-bearing trailers (Signed-off-by, Co-authored-by, etc.) to match the new identity.
- **Tag rewriting**: Annotated tag objects are rewritten when their tagger name/email matches the old identity.
- **AND/OR matching**: When both `--old-name` and `--old-email` are specified, a commit must match both to be rewritten (AND). When only one is specified, any commit matching that field is rewritten (OR).

## checkout

Checkout a branch or ref with working-tree safety guards.

### When to Use

Use `safegit checkout` instead of `git checkout` to get coordination guards that prevent checking out while another safegit operation is in progress.

### Arguments

All arguments are passed through to `git checkout`.

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

Merge a branch into HEAD with working-tree safety guards.

### When to Use

Use `safegit merge` instead of `git merge` for coordination-guarded merges.

### Arguments

All arguments are passed through to `git merge`.

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

Rebase the current branch onto upstream with safety guards.

### When to Use

Use `safegit rebase` instead of `git rebase` for coordination-guarded rebasing.

### Arguments

All arguments are passed through to `git rebase`.

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

Reset HEAD with guards that prevent accidental `--hard` data loss.

### When to Use

Use `safegit reset` instead of `git reset`. The coordination guard only activates for `--hard` resets (tree-mutating operations); soft and mixed resets pass through without the guard.

### Arguments

All arguments are passed through to `git reset`.

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

Binary search through commits to find a bug, with safety guards.

### When to Use

Use `safegit bisect` instead of `git bisect` for coordination-guarded bisecting.

### Arguments

All arguments are passed through to `git bisect`.

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

Cherry-pick one or more commits onto HEAD with safety guards.

### When to Use

Use `safegit cherry-pick` instead of `git cherry-pick` for coordination-guarded cherry-picks.

### Arguments

All arguments are passed through to `git cherry-pick`.

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

Revert one or more commits, creating inverse patches, with safety guards.

### When to Use

Use `safegit revert` instead of `git revert` for coordination-guarded reverts.

### Arguments

All arguments are passed through to `git revert`.

### Examples

```bash
safegit revert abc1234
safegit revert HEAD~3..HEAD
```

### Safety Guarantees

Same as `cherry-pick`.

## config show

Show all configuration values currently in effect.

### Examples

```bash
safegit config show
```

Prints all config keys with their current values (including defaults).

## config get

Get the current value of a single configuration key.

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

Set a configuration key to a new value.

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

List all pre-pre-push hooks installed in the repository.

### Examples

```bash
safegit hook list
```

Shows each hook's name and file path.

## hook run

Run all installed pre-pre-push hooks (or a single named hook) without performing an actual push.

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

## hook install

Install a pre-pre-push hook by copying a script file into the hooks directory.

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

Print safegit version, Go runtime version, and git version.

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

Configuration is stored in `.git/safegit/config.json` with the following defaults:

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
