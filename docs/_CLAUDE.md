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
| `internal/commit` | Two-phase commit pipeline, file staging, intake/canonicalization, declared moves |
| `internal/stage` | Index staging, hunk spec parsing |
| `internal/git` | Git plumbing vocabulary (the only package that speaks git) |
| `internal/gitexec` | The single git-execution boundary: argv construction, the `--no-optional-locks` prefix, the subcommand classification table, the repository-root pin and its declared exemptions, the preview object quarantine |
| `internal/gitversion` | Git feature floors, and the refusals when the installed git is below one |
| `internal/lock` | Lock primitives: atomic publication by `link(2)`, flock-guarded reclamation, identity-checked release, backoff |
| `internal/filelock` | flock-guarded appends (what makes an oplog write atomic) |
| `internal/procutil` | Process liveness and start identity (PID-reuse detection) |
| `internal/oplog` | Append-only operation log (undo, audit); no size cap, no rotation |
| `internal/coord` | Coordination guards: the dirty-tree check, and the policy on what may run against an in-flight git operation |
| `internal/sequencer` | Reads git's own in-flight operation state (merge, pick, revert, rebase, am) from its state files |
| `internal/conflict` | Conflict-marker detection and the reconstruction that attributes a marker to git rather than to the operator |
| `internal/index` | Temporary index file management |
| `internal/hooks` | Pre-pre-push hook stores, discovery and execution |
| `internal/exitcode` | The exit-code registry: every code safegit produces, with its meaning; the source of the generated table in the commands guide |
| `internal/repo` | Configuration and repository state |
| `internal/scan` | Object store scanning (pattern matching, attribution, non-object files) |
| `internal/submodule` | Submodule enumeration, parent detection, nesting checks |
| `internal/test` | Integration tests (build + run safegit as subprocess) |
| `internal/testutil` | Test helpers for repo setup and symlink resolution |
| `internal/trailer` | Trailer injection and parsing, identity replacement, and the MOVE RECORD layer: the `old -> new` pair grammar with its C-quoting encoder/decoder, ULID minting, the overlap rules (nesting, chaining), forward projection over records and retractions, and record-aware message rewriting for scrub |

## Build and test

- `go build -o safegit .` to build
- `go test ./... -race` runs every ordinary test with race detection. It does NOT run the long-running stress scenarios: those are opt-in behind `--stress`, a flag registered on the integration test binary (`internal/test`), so a bare run stays fast and CI needs no `-short` to dodge them
- `go test ./internal/test/ -race -count=5 -timeout=15m --stress` for the stress scenarios
- `testdata/stress [count]` runs the same thing (it passes `--stress` for you)

## Release workflow

This project uses [rlsbl](https://github.com/smm-h/rlsbl) for release orchestration.

- **Never hand-edit CHANGELOG.md.** It is generated from the JSONL changelog in `.rlsbl/changes/` plus the archived release files; a manual edit is lost at the next regeneration. Cover each commit with `rlsbl changelog add --commits <sha> --description "..." --type feature|fix|breaking` (or `--no-user-facing`), and verify with `rlsbl check --tag changelog`
- The bump type is NOT a command-line argument: `rlsbl release init` scaffolds `.rlsbl/releases/unreleased.toml`, you set the bump type, the mandatory description and the optional context there, and you commit that file before releasing
- Release with `rlsbl release run --no-allow-dirty --watch --approve-consequential`. It pushes the version-bump commit untagged, waits for CI on that exact commit, and only then finalizes, tags, pushes and publishes -- so a red CI verdict leaves nothing behind and is fixed forward at the same version with `rlsbl release resume`
- CI handles publishing via goreleaser (cross-platform static binaries)
- Never publish manually, and never `git push` manually -- the release is the only thing that writes to `refs/heads/*`
- `--dry-run` previews a release without making changes (it is framework-owned and available on every rlsbl command)

## Conventions

- Always use `safegit commit` (not raw `git commit`) when committing to this repo, if safegit is installed
- Ordinary commands need no approval flag. `safegit commit -m "msg" -- file` is the bare, correct invocation from a script, hook or agent. The CLI framework's confirm protocol prompts only for commands that declare themselves `consequential`, and in safegit that is exactly `scrub file`, `scrub match`, `scrub run` and `author rewrite`; those four refuse with `error: stdin is not interactive; a consequential command must be confirmed at a terminal` when there is no terminal, so a script that means to run one passes `--approve-consequential`
- `--quiet`, `--verbose`, `--dry-run` and `--approve-consequential` are framework-owned: no short forms (`-q`/`-n`/`-y` are gone) and recognized anywhere in the command line
- All git plumbing calls go through internal/git, and every git subprocess is CONSTRUCTED by internal/gitexec -- the single execution boundary that prepends `--no-optional-locks`, checks the subcommand against one classification table, pins the working directory to the repository root (with a declared exemption table for the sites that cannot take the pin), and refuses an object-writing invocation running in preview mode without an object quarantine. Never shell out to git directly from another package, and never build a `context.Background()` for a git call in a handler: `globalFlags.ctx()` is the one place a context is made
- Per-invocation tmp indexes: never write to the shared .git/index
- All ref updates use CAS (compare-and-swap) via git update-ref with old-value argument
- Lock files are published ATOMICALLY IN CONTENT: the holder's record is written to a temporary sibling and `link(2)`-ed into place (EEXIST is the one-winner signal, exactly as O_CREAT|O_EXCL was, but the published file is already complete, so no contender can read a half-made lock and condemn a live holder). Two environment requirements follow: acquisition needs hard-link support on the filesystem holding `.git`, and stale-lock RECLAMATION needs a working `flock(2)` -- reclamation happens under an exclusive flock on the lock file with an inode identity re-check, never a judge-then-remove, and where flock does not work nothing is reclaimed at all (contenders time out at exit 8 and `safegit unlock <name>` is the recovery path). Release is identity-checked too: a holder removes the path only while it still names the file it published
- Oplog entries have no size limit: the exclusive flock held across each append is what makes it atomic, not the 4096-byte POSIX O_APPEND guarantee. `oplog.Read` returns a skipped-unparseable-line count; consumers that need a complete log (undo, bypass detection) fail closed on a nonzero count, and doctor reports it
- Tests in internal/test/ are integration tests that build and run the safegit binary as a subprocess
- CGO_ENABLED=0 for all builds (static binary, no C dependencies)
- `safegit undo` reverses the last oplog operation (session-scoped by default; `--count N` to undo multiple). The undoable set is `commit`, `amend`, `reword`, `mv` and the three conclusion commands -- it moves a REF and never the working tree, and it refuses outright rather than rolling the branch back over a commit safegit did not create
- `safegit commit --amend --branch <name>` for cross-branch amend/reword
- `safegit doctor --action fix` to garbage-collect and repair (replaces gc)
- **Every flag and positional argument declares its presence** -- required, optional, or a default -- and a mutating command may declare no value default at all (strictcli's mutating-default ban: on a mutating command a value the framework picked is a value the framework writes). safegit's opt-in and opt-out switches (`--amend`, `--allow-empty`, `--pre-push-hook`, `--force-with-lease`, `--bypass-session`, `--count`, `--diff`, `--limit`, `--overwrite-remote-backup`, `--allow-public-remote`) are therefore **optional** and name their fallback in their own help text. `optBool` / `optStr` / `optInt` in `main.go` are the one place absence becomes that fallback
- **Exactly-one selections are declarations, never handler guards.** `push --refs head|branches|tags|both` and `doctor --action diagnose|fix|uninstall` are required choice flags; `scrub match`'s `substitution` (`--replace` / `--mangle`) and `scrub match`/`scrub run`'s `range` (`--from` / `--entire-history`) are required member-spelled selectors, which keeps the flags an operator types unchanged. `author rewrite` declares `AllOrNone("author-name")`, `AllOrNone("author-email")` and `AtLeastOne("author-change")`. The `unreachable: the mutex guarantees exactly one` branches and the "at least one of --old-name or --old-email is required" guard are all deleted -- the parser refuses those command lines, and a refusal now exits 1 rather than safegit's 2 or 70
- `safegit backup backup` / `backup list` / `backup restore` keep one slot per branch at `refs/backups/<branch>` on a remote: ancestry-checked, pushed under a `--force-with-lease` pinned to the SHA just observed (empty expectation for a first backup), `--no-verify` because the namespace is tool-owned, confirmation required on public or unclassifiable remotes (only `--allow-public-remote` answers it -- the blanket `--approve-consequential` does not, `--json` refuses, and a declined confirmation exits nonzero), `--dry-run` previews from local state with no network contact at all, restore is `merge --ff-only`
- `safegit scrub file` and `safegit scrub match` for history rewriting (repo-wide coordination lock prevents concurrent rewrites)
- Every scrub persists crash-safe rewrite maps to `.git/safegit/rewrite-maps.jsonl` (flock-guarded JSONL, three phase records per rewrite: commit map + pre-rewrite remote-tracking state before refs move, all tag rewrites, new HEAD + cleanup status); scrub JSON output includes `pre_rewrite_remotes`, `cleanup_ok`, `cleanup_errors`
- Destructive rewrites in release-managed repos (`.rlsbl/` or `.rlsbl-monorepo/` present) are ordinary operations: safegit rewrites and journals, and the release tooling detects the dangling references afterwards (hard error) and heals them from the journal
- Every confirmation owns its consent flag, and `--json` answers none of them. The framework's confirm protocol covers the four consequential commands (`scrub file`/`match`/`run`, `author rewrite`) and `--approve-consequential` answers it; safegit adds no second prompt behind it, only a notice stating the scale of the rewrite. THREE conditions the framework cannot see keep their own seam: `doctor --action uninstall` and `push --force-with-lease`, both answered by `--approve-consequential` because the condition IS the flag the caller typed, and a `backup backup` to a public or unclassifiable remote, answered ONLY by `--allow-public-remote` because the visibility is discovered at run time and the caller may not know it. **Every prompt is written to STDERR and `--quiet` never suppresses one** -- stdout is a structured channel, and a prompt a quiet run hid would be a prompt that hangs. Declining exits nonzero
- `--dry-run` is honest across the board: the framework records each mutation in a would-do log on stdout instead of performing it. `push`, `pull`, `checkout`, `merge`, `rebase`, `reset`, `bisect`, `cherry-pick`, `revert`, `config set` and `hook install` all ignored `--dry-run` before 0.25 and mutated anyway. The one exception is `hook run`, which declares `dry_run_supported=false` and refuses the flag with its reason: a hook is an operator-supplied script whose effects safegit cannot know, so any preview would be invented (use `hook list` to see which scripts would run)
- `--json` is the framework's machine mode, not a safegit flag: stdout carries exactly one document, the envelope (`interface_version`, `app`, `app_version`, `command`, `exit_code`, `payload`, `dry_run`, `writes`, `preview`, `preview_error`, `diagnostics`), and a command's own data is its `payload`. Every payload-producing command declares a JSON Schema the framework validates at emission, published verbatim by `--dump-schema`. `writes` belongs to the framework's update contract, which safegit declares nowhere, so it is always `null` -- present, never absent. `preview` is the effect log and is populated in BOTH modes: a dry run's records carry `"recorded": true` and ride there instead of the text would-do log, while a real run's carry `"recorded": false`, so a consumer reads `dry_run` to tell them apart rather than the presence of `preview`. Machine mode does NOT imply `--quiet` (the envelope is structurally exempt from it) and never implies approval. An error path writes to stderr and exits nonzero -- there is no JSON error object
- `safegit scrub run` for recipe-based multi-operation scrub from TOML files
- `safegit scrub verify` is STATELESS: no policy file, nothing remembered between runs, so an invocation checks exactly what its command line names -- repeatable `--pattern` regexes, a scrub recipe file, or both; the parser requires at least one, and `--scope` is a modifier on `--pattern` (declared with `Requires`), never the only input
- `safegit mv 'old -> new'` performs a rename, mints its move record and commits, in one operation: every pair validated before the FIRST filesystem mutation, renames minted through the effects handle (so a dry run records them), rollback of everything already moved on a failure, and the commit carries each path across as the exact blob its parent held -- the rename and nothing else, so uncommitted content changes stay uncommitted. `-m` is required with no default. A directory pair is ONE subtree record. `safegit undo` on an `mv` reverses the COMMIT and never the working tree, and says so
- Moves are DECLARED, never detected: `--moved 'old -> new'` states a move already made (exit 19 when the repository does not bear it out), `--moved-retract <id>` retracts one after verifying the id against the history the commit is built on, and a record is never edited -- a replacement is a retraction plus a new declaration in one commit. `trailer.Overlap` is the single authority for the two refusals both spellings share: pairs may not NEST and may not CHAIN (exit 2)
- Rewrites treat move records as records: `scrub match`/`scrub run` substitute inside the DECODED paths and re-encode through the one encoder (so output always parses; a pattern matching the escaped spelling matches nothing), and a substitution whose result is no longer a move is a hard refusal at exit 30 before any ref moves. `scrub file --delete` removes the records naming the erased path, whole; `--replace-with` edits no message
- A single `safegit revert` is pipeline-authored (trailers, commit-msg hook, state cleanup, undoable) and mints the INVERSE of every move record the reverted commit declared; a QUEUED revert is git's own sequencer, so those commits carry no trailers and no records. The asymmetry is deliberate and the help text states it
- `doctor --action uninstall` is REPOSITORY-WIDE: every worktree's state directory plus the shared store, enumerated path by path (marking the ones outside the invoking worktree) BEFORE the confirmation and in a dry run too
- `checkout`, `pull`, `merge`, `rebase`, `reset`, `bisect` and `cherry-pick` are guarded passthroughs: the worktree operation lock, then the coordination check, then git (the check is conditional for `reset --hard` only and for `bisect`'s tree-moving subcommands). `revert` is one too EXCEPT for a single commit, which is restructured -- see above
- **safegit concludes a stopped merge, cherry-pick and revert itself**, with `merge-continue` / `cherry-pick-continue` / `revert-continue`. Every conflicted path is declared with `--resolve 'path=ours|theirs|worktree|delete'` (or a `--resolve-file` TOML), where the keywords name INDEX STAGES: a conclusion refuses when the declared paths are not exactly the conflicted ones (exit 17) and when the content it would commit still holds a conflict block no side already carried (exit 18, exempted only by a `safegit-conflict-markers` attribute read from the FIRST PARENT's tree). `safegit merge --continue` / `cherry-pick --continue` / `revert --continue` are refused at exit 5 and name safegit's own command; a rebase's and a `git am`'s `--continue` stay git's. A QUEUED cherry-pick or revert is DELEGATED to git's `--continue` with safegit's index copy as its index after the same checks, so those commits are git's -- no safegit trailers, no `commit-msg` handling, not undoable -- and `-m`, `--trailer` and `--dry-run` are refused there rather than ignored
