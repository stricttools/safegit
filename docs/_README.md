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

## Install

From source (requires Go 1.24+):

```
go install github.com/smm-h/safegit@latest
```

Pre-built binaries are available on
[GitHub Releases](https://github.com/smm-h/safegit/releases) via goreleaser.

## Quick start

```
cd your-repo
safegit commit -m "add feature X" -- src/foo.go src/bar.go
safegit push --refs head
```

safegit auto-initializes on first use (creates `.git/safegit/`).
Use `safegit doctor --action uninstall` to remove safegit from a repo.

## Commands

:-: table-commands

Tree-mutating commands (`checkout`, `pull`, `merge`, `rebase`, `reset`,
`bisect`, `cherry-pick`, `revert`) are passed through with coordination guards.

## How it works

The commit pipeline has two phases. Phase A (parallel-safe) creates a temporary
index, stages the requested files into it, and builds the tree object -- all
without touching the shared `.git/index`. Phase B acquires a per-ref lock,
reads the current tip, creates the commit with that parent, and updates the ref
using CAS. If the ref moved between read and write, Phase B retries from the
new tip (re-parenting the commit) with random jitter to avoid thundering-herd
stampedes under heavy concurrency.

See [docs/architecture.md](docs/architecture.md) for the full architecture specification.

## Configuration

Run `safegit config` to view all settings, or `safegit config <key> <value>`
to change one.

| Key | Default | Description |
|-----|---------|-------------|
| `commit.casMaxAttempts` | 5 | Max CAS retry attempts for ref updates |
| `lock.acquireTimeoutSeconds` | 30 | Timeout waiting for a per-ref lock |
| `hooks.preprepush.timeoutSeconds` | 1800 | Timeout for pre-pre-push hook execution |
| `push.retryAttempts` | 3 | Number of push retry attempts |
| `log.maxSizeMB` | 100 | Max operation log size before rotation |

Configuration is stored in `.git/safegit/config.json`. Remove the entire
`.git/safegit/` directory to return to vanilla git.

## Known limitations

- **Same-machine concurrency only.** Lock staleness detection uses PID liveness
  checks and hostname comparison. On network filesystems (NFS, CIFS), `safegit
  doctor` warns about reduced lock atomicity guarantees. Cross-machine lock
  reclaim is refused when the hostname doesn't match.
- **PID reuse.** On Linux, safegit compares process start time against lock
  creation time via `/proc` to detect PID reuse. On other platforms, a reused
  PID keeps an orphan lock looking alive, and `safegit unlock <ref>` refuses to
  clear a lock whose holder is alive, so such a lock has to be removed by hand
  from `.git/safegit/locks/`. Where the holder really is gone, `safegit unlock
  refs/heads/main` clears the lock.
- **Linux and macOS only.** Windows is not supported (Unix-only syscalls for
  locking, signals, process management). WSL (Windows Subsystem for Linux) works
  since it runs the Linux binary natively.

## License

MIT
