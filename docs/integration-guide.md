---
title: Integration Guide
description: "How to integrate safegit with other tools: Claude Code sessions, rlsbl release workflows, pre-push hooks, scrub orchestration, and environment variables."
nav_group: "Guides"
nav_order: 100
---

# Integration Guide

safegit is designed to be embedded in automated workflows and AI agent toolchains. This guide covers how safegit interacts with external tools and how to configure those integrations.

## Claude Code sessions

safegit is the required git wrapper for Claude Code sessions that share a worktree. The core problem: when multiple AI agent sessions run `git commit` concurrently, they race on `.git/index`, causing files to leak between commits. safegit solves this with per-invocation temporary indexes and CAS-retry ref updates.

### Commit workflow

Use `safegit commit` instead of `git commit` for all commits. The `--` separator separates flags from file paths:

```
safegit commit -m "message" -- file1 file2
```

safegit stages only the listed files into a temporary index, creates a tree, and updates the branch ref via compare-and-swap. No shared index is touched.

Key operations:

- **Commit:** `safegit commit -m "message" -- file1.go file2.go`
- **Amend with files:** `safegit commit --amend -m "new message" -- file3.go`
- **Reword (no files):** `safegit commit --amend -m "reworded message"`
- **Cross-branch amend:** `safegit commit --amend --branch feature-x -m "msg" -- file.go`
- **Undo last commit:** `safegit undo`
- **Undo last N operations:** `safegit undo --count 3`

### Session scoping

safegit reads `CLAUDE_CODE_SESSION_ID` from the environment to scope undo operations. Each oplog entry records the session ID, and `safegit undo` only reverses operations from the current session by default. Use `--bypass-session` to undo across all sessions.

The session ID is also injected as a `Claude-Code-Session-Id` git trailer on every commit, providing attribution in the commit history.

### Restricted operations

The following git operations are forbidden in multi-session worktrees and safegit does not provide wrappers for them:

- `git add .` / `git add -A` / `git add --all` / `git add -u` (stages files from other sessions)
- `git stash` (hides working tree state from other sessions)
- `git restore` / `git checkout -- <file>` (destroys uncommitted work from other sessions)

safegit provides guarded passthroughs for `checkout`, `merge`, `rebase`, `reset`, `bisect`, `cherry-pick`, and `revert`. These check for uncommitted changes (coordination guard) before proceeding.

## rlsbl release workflow

safegit integrates with [rlsbl](https://github.com/smm-h/rlsbl) for release orchestration. The integration surfaces in three areas: push handling, scrub orchestration, and rlsbl-awareness in post-rewrite hints.

### Push handling

In rlsbl-managed projects, pushes happen exclusively through `rlsbl release run` (which handles version bumps, changelog finalization, tagging, and pushing in one atomic flow). safegit's `push` command is available for non-release pushes (e.g., dev branches via `rlsbl push`), but direct pushes to release branches are discouraged.

`safegit push` provides:

- Pre-pre-push hooks (run before any network I/O)
- Automatic retry with exponential backoff on transport errors
- Oplog recording of every push

### rlsbl detection

safegit detects rlsbl-managed repositories by checking for `.rlsbl/` or `.rlsbl-monorepo/` directories at the repository root. This detection affects two behaviors:

1. **Scrub guard:** destructive history rewrites (`scrub file`, `scrub match`, `scrub run`, `author rewrite`) are blocked in rlsbl-managed repos unless the `RLSBL_SCRUB_ORCHESTRATED` environment variable is set to `"1"`. Dry-run and `--diff` previews remain available.

2. **Push hints:** after a history rewrite, safegit prints context-appropriate instructions. In rlsbl-managed repos, it says "Complete the rewrite via your release tooling" instead of showing a raw `safegit push` command.

## Pre-push hooks (pre-pre-push)

safegit has its own hook system called "pre-pre-push hooks" -- these run before `safegit push` performs any network I/O, separate from git's built-in pre-push hook.

### Hook lifecycle

1. `safegit push` resolves which refs will be pushed
2. Pre-pre-push hooks run with the resolved ref information on stdin
3. If any hook fails (non-zero exit) or times out, the push is aborted
4. Only after all hooks pass does the actual `git push` execute

### Installing hooks

```
safegit hook install /path/to/script.sh
```

This copies the script into `.git/safegit/hooks/` and makes it executable. Hooks are discovered by scanning that directory.

### Hook environment

Every pre-pre-push hook receives these environment variables:

| Variable | Description |
|----------|-------------|
| `SAFEGIT_REMOTE_NAME` | Name of the remote being pushed to (e.g., `origin`) |
| `SAFEGIT_REMOTE_URL` | URL of the remote |
| `SAFEGIT_PHASE` | Always `pre-pre-push` |
| `SAFEGIT_HOOK_TIMEOUT_S` | Timeout in seconds for this hook run |

Hook stdin follows the same format as git's pre-push hook: one line per ref being pushed, with fields `<local-ref> <local-sha> <remote-ref> <remote-sha>`.

### Hook management

- `safegit hook list` -- show all installed hooks
- `safegit hook run` -- run all hooks without pushing (dry run)
- `safegit hook run <name>` -- run a specific hook by name

### Timeout configuration

The default hook timeout is 1800 seconds (30 minutes). Configure it per-repo:

```
safegit config set hooks.preprepush.timeoutSeconds 300
```

### Submodule hook cascading

When `safegit push` runs inside a submodule, it discovers hooks from both the parent repo and the submodule, running parent hooks first. This enables organization-wide push policies.

### Interaction with git pre-push hooks

rlsbl installs a git pre-push hook (`.git/hooks/pre-push`) that runs `rlsbl pre-push-check` to enforce JSONL changelog coverage. This is a standard git hook, separate from safegit's pre-pre-push system. The execution order is:

1. safegit pre-pre-push hooks (safegit's own system)
2. `git push` executes
3. git's pre-push hook runs (rlsbl's coverage check)

If either layer rejects the push, the operation fails.

## Scrub orchestration protocol

When rlsbl needs to rewrite history (e.g., removing a leaked secret), it coordinates with safegit through an orchestration protocol.

### The handshake

1. The user runs `rlsbl release scrub`
2. rlsbl sets `RLSBL_SCRUB_ORCHESTRATED=1` in the environment
3. rlsbl invokes `safegit scrub` as a subprocess
4. safegit checks for the environment variable and allows the destructive rewrite
5. safegit writes rewrite maps to `.git/safegit/rewrite-maps.jsonl`
6. rlsbl reads the rewrite maps and performs post-scrub work:
   - Remaps commit hashes in JSONL changelog files (via `--remap-shas-in`)
   - Regenerates `CHANGELOG.md`
   - Updates tags
   - Recreates GitHub Releases

### Without orchestration

Without `RLSBL_SCRUB_ORCHESTRATED=1`, safegit refuses destructive scrubs in rlsbl-managed repos with a hard error directing the user to `rlsbl release scrub`. Non-destructive operations (`--diff` preview, `scrub verify`, `scan`) always work regardless.

### Rewrite maps

Every destructive scrub persists crash-safe rewrite maps to `.git/safegit/rewrite-maps.jsonl`. Each rewrite produces three JSONL records sharing a unique ID:

| Phase | Contents | Written when |
|-------|----------|-------------|
| `start` | Full old-to-new commit SHA map, pre-rewrite remote-tracking state | Before any refs move |
| `refs` | All tag rewrites (ref-level and annotation-pass) | After refs are updated |
| `complete` | New HEAD SHA, cleanup status (ok/errors) | After cleanup and verification |

The `start` record is written first so that even a mid-rewrite crash leaves the mapping recoverable. Orchestrators (like rlsbl) read these records to remap hashes in dependent files.

### SHA remapping in files

The `--remap-shas-in` flag (available on `scrub file`, `scrub match`, and `scrub run`) takes a glob pattern selecting files whose 40-character commit hashes should be remapped during the rewrite walk. This keeps hash-referencing files (like JSONL changelogs) self-consistent at every commit in the rewritten history, not just at HEAD.

```
safegit scrub match --pattern "SECRET_KEY" --replace "REDACTED" \
  --remap-shas-in ".rlsbl/changes/*.jsonl" --reason "leaked API key"
```

## Environment variables

### Variables safegit reads

| Variable | Used by | Purpose |
|----------|---------|---------|
| `CLAUDE_CODE_SESSION_ID` | `commit`, `undo`, oplog | Session scoping for undo operations and commit trailer injection. When set, commits get a `Claude-Code-Session-Id` trailer. `undo` filters oplog entries to the current session. |
| `RLSBL_SCRUB_ORCHESTRATED` | `scrub file`, `scrub match`, `scrub run`, `author rewrite` | Must be `"1"` to allow destructive history rewrites in rlsbl-managed repos. Set by rlsbl during orchestrated scrubs. |

### Variables safegit sets (for hooks)

| Variable | Set by | Value |
|----------|--------|-------|
| `SAFEGIT_REMOTE_NAME` | `push`, `hook run` | Name of the remote (e.g., `origin`) |
| `SAFEGIT_REMOTE_URL` | `push`, `hook run` | URL of the remote (or `manual-run` for `hook run`) |
| `SAFEGIT_PHASE` | `push`, `hook run` | Always `pre-pre-push` |
| `SAFEGIT_HOOK_TIMEOUT_S` | `push`, `hook run` | Hook timeout in seconds |

## Configuration reference

All configuration lives in `.git/safegit/config.json`. Manage it with `safegit config show`, `safegit config get <key>`, and `safegit config set <key> <value>`.

| Key | Default | Description |
|-----|---------|-------------|
| `commit.casMaxAttempts` | 5 | Maximum CAS retry attempts for concurrent ref updates |
| `commit.autoBumpParent` | (unset) | When `true`, auto-bumps the parent submodule pointer after commits |
| `lock.acquireTimeoutSeconds` | 30 | Timeout for acquiring per-ref locks |
| `hooks.preprepush.timeoutSeconds` | 1800 | Timeout for pre-pre-push hook execution |
| `push.retryAttempts` | 3 | Number of push retry attempts on transport errors |
| `log.maxSizeMB` | 100 | Maximum oplog file size |

## Submodule integration

When safegit detects it is running inside a git submodule, two additional behaviors activate:

- **Auto-bump parent:** if `commit.autoBumpParent` is `true` in the parent repo's safegit config, every `safegit commit` in the submodule automatically creates a bump commit in the parent repo updating the submodule pointer. Nested submodules are detected and rejected.

- **Hook cascading:** `safegit push` discovers and runs pre-pre-push hooks from both the parent repo and the submodule, with parent hooks executing first.

## JSON output mode

All commands support `--json` for machine-readable output. When `--json` is active, safegit automatically enables `--quiet` (suppresses informational stderr) and `--yes` (auto-confirms prompts). If a command fails before producing JSON output, an error envelope is written to stdout:

```json
{
  "error": "description of what went wrong"
}
```

This makes safegit suitable for embedding in tool pipelines that parse structured output.
