# safegit

Concurrency-safe Git wrapper (Go CLI). When multiple AI agent sessions share one repo, standard git races on `.git/index` -- files leak between commits. safegit isolates each commit via per-invocation temporary indexes and CAS-retry ref updates. Output is standard git commits, transparent to CI and teammates.

## Commands

- `commit -m "msg" -- file1 file2` -- atomic commit (handles tracked and untracked files)
- `commit --amend --branch <name>` -- cross-branch amend/reword
- `undo` -- reverse last commit/amend/reword via oplog (session-scoped by default; `--bypass-session` for cross-session)
- `redo` -- restore what undo removed (one-shot; prevents oscillation)
- `push` -- push with pre-pre-push hooks and retry
- `doctor --fix` -- health checks, garbage collection, repair
- `config` -- show/set configuration
- `unlock` -- release stale ref locks
- `hook` -- manage pre-pre-push hooks
- `rewrite-author` -- deprecated, replaced by `author rewrite`
- `scan --pattern <regex>` -- search git objects and working tree for regex pattern matches
- `author list` -- enumerate all distinct author/committer identities with commit counts
- `author check` -- compare identities against expected name/email, exit 1 on deviations
- `author rewrite` -- rewrite author/committer identity across history
- `scrub file <path> --from <commit> --reason <text>` -- replace a file's blob across history with current on-disk content
- `scrub match --pattern <regex> --replace <text> --reason <text> --entire-history` -- pattern-based secret removal across all git objects (blobs, commit messages, tag annotations) with surgical cleanup and re-scan verification; `--dry-run` to scan without rewriting; `--scope <glob>` to limit file paths
- `scrub run <recipe.toml>` -- execute multi-operation scrub recipes from TOML files; `--diff` to preview
- `scrub verify` -- check that previously scrubbed patterns remain absent from history via persistent policies
- `pull` -- pull with `--ff-only`, `--ff`, `--no-ff` flag support
- `version` -- print safegit version, Go runtime, and git version
- Guarded passthroughs: `checkout`, `merge`, `rebase`, `reset`, `bisect`, `cherry-pick`, `revert`

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
- All git plumbing calls go through internal/git (never shell out to git directly from other packages)
- Per-invocation tmp indexes: never write to the shared .git/index
- All ref updates use CAS (compare-and-swap) via git update-ref with old-value argument
- Lock files use O_CREAT|O_EXCL for atomic creation
- Oplog entries must be < 4096 bytes (POSIX atomic append guarantee)
- Tests in internal/test/ are integration tests that build and run the safegit binary as a subprocess
- CGO_ENABLED=0 for all builds (static binary, no C dependencies)
- `safegit undo` reverses the last commit/amend/reword via oplog (session-scoped by default)
- `safegit redo` restores what undo removed (one-shot to prevent oscillation)
- `safegit commit --amend --branch <name>` for cross-branch amend/reword
- `safegit doctor --fix` to garbage-collect and repair (replaces gc)
- `safegit scrub file` and `safegit scrub match` for history rewriting (repo-wide coordination lock prevents concurrent rewrites)
- `safegit scrub run` for recipe-based multi-operation scrub from TOML files
- `safegit scrub verify` for policy-based verification that scrubbed patterns remain absent
- cherry-pick, revert are guarded passthroughs (coordination check before git)
