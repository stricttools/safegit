---
title: README.md
---
# safegit

Go CLI wrapper around git for safe concurrent multi-agent use.

## The problem

When multiple AI agent sessions share a single git repository, they race on
`.git/index`. Two agents staging files at the same time produce mixed commits
-- files from one agent leak into another's commit, or writes are silently
lost. Standard git has no built-in isolation for this scenario.

## The solution

safegit wraps git plumbing commands behind a two-phase commit pipeline that
keeps every invocation isolated. Per-invocation temporary index files prevent
staging races. Ref updates use per-ref locks with compare-and-swap (CAS) retry,
so concurrent commits to the same branch serialize correctly. An append-only
operation log records every mutation. The output is standard git commits --
teammates, CI, and code review tools see nothing unusual.

## A deliberate subset

safegit does not promise full git support and never will. It implements a
small, opinionated subset of git's functionality, chosen for agent-heavy
workflows. When a git feature, command, flag, or edge case is judged actively
harmful or irrelevant for that workflow, safegit deliberately omits it and
never looks back. Every such omission is recorded in
[docs/divergences.md](docs/divergences.md), unapologetically. safegit is for
agents, not for all humans.

## Install

From source (requires the Go version `go.mod` declares -- currently 1.25.7):

```
go install github.com/smm-h/safegit@v0
```

`@v0`, not `@latest`: safegit issues no 1.x tags, so `@latest` cannot resolve to
a real release. Pin an exact version (`@v0.28.0`) when you need one.

Pre-built binaries are available on
[GitHub Releases](https://github.com/smm-h/safegit/releases) via goreleaser.

## Quick start

```
cd your-repo
safegit commit -m "add feature X" -- src/foo.go src/bar.go
safegit push --refs head
```

safegit auto-initializes on first use (creates `.git/safegit/`); a `--dry-run`
deliberately does not, so previewing in a fresh repository writes nothing at
all. Use `safegit doctor --action uninstall` to remove safegit from a
repository -- a repository-wide operation that lists every path it will remove,
including the state of worktrees other than the one you are standing in, before
asking you to confirm.

## Commands

:-: table-commands

Tree-mutating commands (`checkout`, `pull`, `merge`, `rebase`, `reset`,
`bisect`, `cherry-pick`, `revert`) are passed through with coordination guards.
Reverting a single commit is the exception: git computes the inverse patch and
safegit commits it, so it carries safegit's trailers and `safegit undo` reverses
it.

When git parks a merge, cherry-pick or revert on a conflict, safegit finishes it
rather than git: `merge-continue`, `cherry-pick-continue` and `revert-continue`
take one `--resolve 'path=ours|theirs|worktree|delete'` per conflicted path and
write the commit themselves, refusing a declaration that does not match the
conflict or content that still holds a conflict block.

## How it works

The commit pipeline has two phases. Phase A (parallel-safe) resolves the tip of
the target branch, creates a temporary index seeded from it, stages the
requested files, and builds the tree and commit objects -- all without touching
the shared `.git/index`. Phase B acquires a per-ref lock, re-reads the tip to
confirm it has not moved, and updates the ref with a compare-and-swap. If it
did move, the pipeline retries from Phase A against the new tip (re-parenting
the commit) with random jitter to avoid thundering-herd stampedes under heavy
concurrency.

The parent is resolved BEFORE the index rather than after the tree: the other
order lets another session's commit land in between and produces a commit whose
tree is based on the old tip but whose parent is the new one, silently dropping
that session's files.

See [docs/architecture.md](docs/architecture.md) for the full architecture specification.

## Configuration

Run `safegit config show` to view every setting, `safegit config get <key>` to
read one, and `safegit config set <key> <value>` to change one.

| Key | Default | Description |
|-----|---------|-------------|
| `commit.casMaxAttempts` | 5 | Max CAS retry attempts for ref updates |
| `commit.autoBumpParent` | (unset, and an unset one is a refusal) | Whether a commit in a submodule also commits the parent's moved gitlink |
| `lock.acquireTimeoutSeconds` | 30 | Timeout waiting for a lock |
| `hooks.preprepush.timeoutSeconds` | 1800 | Timeout for pre-pre-push hook execution |
| `push.retryAttempts` | 3 | Number of push retry attempts |

Those five are the whole key set: anything else is an unknown-key error. There
is no oplog size or rotation setting -- the operation log is append-only and
complete by design, and nothing truncates it.

Configuration is stored in `.git/safegit/config.json`. Remove the entire
`.git/safegit/` directory to return to vanilla git -- or run `safegit doctor
--action uninstall`, which does it for the whole repository (every worktree's
state directory plus the shared store) and enumerates every path before it asks
for confirmation.

## Known limitations

- **Same-machine concurrency only.** Lock staleness detection uses PID liveness
  checks and hostname comparison. On network filesystems (NFS, CIFS), `safegit
  doctor` warns about reduced lock atomicity guarantees. Cross-machine lock
  reclaim is refused when the hostname doesn't match.
- **The filesystem must support hard links and `flock(2)`.** A lock is published
  by writing its record to a temporary sibling and `link(2)`-ing it into place,
  so the published file is complete the instant it exists; and a stale lock is
  reclaimed only under an exclusive `flock` on the lock file, with an inode
  identity re-check, so two contenders can never both "reclaim" the same lock.
  Where `flock` does not work, nothing is reclaimed at all: contenders time out
  and `safegit unlock <name>` is the recovery path.
- **PID reuse.** On Linux, safegit records the holder's process start identity
  from `/proc` in the lock file and compares it against the current start time
  of whatever holds that PID, so a recycled PID is detected and a live holder is
  never mistaken for one. On other platforms the comparison is unavailable and
  fails closed: a reused PID keeps an orphan lock looking alive, and `safegit
  unlock` refuses to clear a lock whose holder appears alive, so such a lock has
  to be removed by hand from `.git/safegit/locks/`. Where the holder really is
  gone, `safegit unlock refs/heads/main` clears it.
- **Linux and macOS only.** Windows is not supported and is not built: the
  release binaries cover linux and darwin on amd64 and arm64, and safegit uses
  Unix-only syscalls for locking, signals and process management. WSL (Windows
  Subsystem for Linux) works, since it runs the Linux binary natively.

## License

MIT
