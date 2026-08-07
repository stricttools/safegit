---
title: CLAUDE.md
---
# safegit

Concurrency-safe Git wrapper (Go CLI). When multiple AI agent sessions share one repo, standard git races on `.git/index` -- files leak between commits. safegit isolates each commit via per-invocation temporary indexes and CAS-retry ref updates. Output is standard git commits, transparent to CI and teammates.

## Commands

:-: table-commands

## Architecture

| Package | Role |
|---------|------|
| `internal/commit` | Two-phase commit pipeline, file staging |
| `internal/stage` | Index staging, hunk spec parsing |
| `internal/git` | All git plumbing calls (sole interface to git) |
| `internal/lock` | Per-ref locking with PID liveness checks |
| `internal/oplog` | Append-only operation log (undo, audit) |
| `internal/coord` | Coordination guards for tree-mutating commands |
| `internal/index` | Temporary index file management |
| `internal/hooks` | Pre-pre-push hook management |
| `internal/repo` | Configuration and repository state |
| `internal/scan` | Object store scanning (pattern matching, attribution, non-object files) |
| `internal/submodule` | Submodule enumeration, parent detection, nesting checks |
| `internal/test` | Integration tests (build + run safegit as subprocess) |
| `internal/testutil` | Test helpers for repo setup and symlink resolution |
| `internal/trailer` | Git trailer injection, parsing, and identity replacement |

## Build and test

- `go build -o safegit .` to build
- `go test ./... -race` to run all tests with race detection
- `go test ./internal/test/ -race -count=5 -timeout=15m` for stress tests
- `testdata/stress` for a quick stress run

## Release workflow

This project uses [rlsbl](https://github.com/smm-h/rlsbl) for release orchestration.

- Update CHANGELOG.md with a `## X.Y.Z` entry describing changes
- Run `rlsbl release [patch|minor|major]` to bump version and create a GitHub Release
- CI handles publishing via goreleaser (cross-platform static binaries)
- Never publish manually -- always use `rlsbl release`
- Use `rlsbl release --dry-run` to preview a release without making changes

## Conventions

- Always use `safegit commit` (not raw `git commit`) when committing to this repo, if safegit is installed
- Ordinary commands need no approval flag. `safegit commit -m "msg" -- file` is the bare, correct invocation from a script, hook or agent. The CLI framework's confirm protocol prompts only for commands that declare themselves `consequential`, and in safegit that is exactly `scrub file`, `scrub match`, `scrub run` and `author rewrite`; those four refuse with `error: stdin is not interactive; pass --approve-consequential to confirm` when there is no terminal, so a script that means to run one passes `--approve-consequential`
- `--quiet`, `--verbose`, `--dry-run` and `--approve-consequential` are framework-owned: no short forms (`-q`/`-n`/`-y` are gone) and recognized anywhere in the command line
- All git plumbing calls go through internal/git (never shell out to git directly from other packages)
- Per-invocation tmp indexes: never write to the shared .git/index
- All ref updates use CAS (compare-and-swap) via git update-ref with old-value argument
- Lock files use O_CREAT|O_EXCL for atomic creation
- Oplog entries must be < 4096 bytes (POSIX atomic append guarantee)
- Tests in internal/test/ are integration tests that build and run the safegit binary as a subprocess
- CGO_ENABLED=0 for all builds (static binary, no C dependencies)
- `safegit undo` reverses the last commit/amend/reword via oplog (session-scoped by default; `--count N` to undo multiple)
- `safegit commit --amend --branch <name>` for cross-branch amend/reword
- `safegit doctor --fix` to garbage-collect and repair (replaces gc)
- `safegit backup backup` / `backup list` / `backup restore` keep one slot per branch at `refs/backups/<branch>` on a remote: ancestry-checked, pushed under a `--force-with-lease` pinned to the SHA just observed (empty expectation for a first backup), `--no-verify` because the namespace is tool-owned, confirmation required on public or unclassifiable remotes (only `--allow-public-remote` answers it -- the blanket `--approve-consequential` does not, `--json` refuses, and a declined confirmation exits nonzero), `--dry-run` previews from local state with no network contact at all, restore is `merge --ff-only`
- `safegit scrub file` and `safegit scrub match` for history rewriting (repo-wide coordination lock prevents concurrent rewrites)
- Every scrub persists crash-safe rewrite maps to `.git/safegit/rewrite-maps.jsonl` (flock-guarded JSONL, three phase records per rewrite: commit map + pre-rewrite remote-tracking state before refs move, all tag rewrites, new HEAD + cleanup status); scrub JSON output includes `pre_rewrite_remotes`, `cleanup_ok`, `cleanup_errors`
- Destructive rewrites in release-managed repos (`.rlsbl/` or `.rlsbl-monorepo/` present) are ordinary operations: safegit rewrites and journals, and the release tooling detects the dangling references afterwards (hard error) and heals them from the journal
- Every confirmation owns its consent flag, and `--json` answers none of them. The framework's confirm protocol gates the four consequential commands (`scrub file`/`match`/`run`, `author rewrite`) and `--approve-consequential` answers it; safegit adds no second prompt behind it, only a notice stating the scale of the rewrite. The two conditions the framework cannot see keep their own seam: `doctor --uninstall`, answered by `--approve-consequential` because the condition is the flag the caller typed, and a `backup backup` to a public or unclassifiable remote, answered ONLY by `--allow-public-remote` because the visibility is discovered at run time and the caller may not know it. Declining exits nonzero
- `--dry-run` is honest across the board: the framework records each mutation in a would-do log on stdout instead of performing it. `push`, `pull`, `checkout`, `merge`, `rebase`, `reset`, `bisect`, `cherry-pick`, `revert`, `config set` and `hook install` all ignored `--dry-run` before 0.25 and mutated anyway. The one exception is `hook run`, which declares `dry_run_supported=false` and refuses the flag with its reason: a hook is an operator-supplied script whose effects safegit cannot know, so any preview would be invented (use `hook list` to see which scripts would run)
- `safegit scrub run` for recipe-based multi-operation scrub from TOML files
- `safegit scrub verify` for policy-based verification that scrubbed patterns remain absent
- cherry-pick, revert are guarded passthroughs (coordination check before git)
