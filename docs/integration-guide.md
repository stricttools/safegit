---
title: Integration Guide
description: "How to integrate safegit with other tools: Claude Code sessions, rlsbl release workflows, pre-push hooks, scrub orchestration, machine mode's JSON envelope, and environment variables."
nav_group: "Guides"
nav_order: 100
---

# Integration Guide

safegit is designed to be embedded in automated workflows and AI agent toolchains. This guide covers how safegit interacts with external tools and how to configure those integrations.

## Claude Code sessions

safegit is the required git wrapper for Claude Code sessions that share a worktree. The core problem: when multiple AI agent sessions run `git commit` concurrently, they race on `.git/index`, causing files to leak between commits. safegit solves this with per-invocation temporary indexes and CAS-retry ref updates.

### Commit workflow

Use `safegit commit` instead of `git commit` for all commits in shared worktrees. The command stages only the listed files into an isolated temporary index, creates a tree object, and updates the branch ref via compare-and-swap. The `--` separator separates flags from file paths:

```
safegit commit -m "message" -- file1 file2
```

safegit stages only the listed files into a temporary index, creates a tree, and updates the branch ref via compare-and-swap. No shared index is touched.

Key operations:

- **Commit:** `safegit commit -m "message" -- file1.go file2.go`
- **Amend with files:** `safegit commit --amend -m "new message" -- file3.go`
- **Reword (no files):** `safegit commit --amend -m "reworded message"`
- **Cross-branch amend:** `safegit commit --amend --branch feature-x -m "msg" -- file.go`
- **Move and record it in one step:** `safegit mv -m "move the parser" 'src/parse.go -> internal/parse/parse.go'`
- **Undo last commit:** `safegit undo`
- **Undo last N operations:** `safegit undo --count 3`

### Session scoping

safegit reads `CLAUDE_CODE_SESSION_ID` from the environment to scope undo operations. Each oplog entry records the session ID, and `safegit undo` only reverses operations from the current session by default. Use `--bypass-session` to undo across all sessions.

The session ID is also injected as a `Claude-Code-Session-Id` git trailer on every commit, providing attribution in the commit history.

### Restricted operations

The following git operations are forbidden in multi-session worktrees because they mutate shared state in ways that cannot be isolated per-session, and safegit intentionally does not provide wrappers for them:

- `git add .` / `git add -A` / `git add --all` / `git add -u` (stages files from other sessions)
- `git stash` (hides working tree state from other sessions)
- `git restore` / `git checkout -- <file>` (destroys uncommitted work from other sessions)

`git checkout -- <path>` has no safegit equivalent at all -- not a guarded one, not a refused one. Branch navigation is `safegit switch`, which takes a branch name (or `-c <new>`) and has no file mode, so the destructive restore shape is inexpressible rather than refused.

safegit provides guarded commands for `switch`, `pull`, `merge`, `rebase`, `reset`, `bisect`, `cherry-pick`, and `revert`. Each takes the worktree operation lock for the whole operation before anything runs. The uncommitted-work check then runs for all of them except `reset` (only the modes that write working-tree files: `--hard`, `--merge`, `--keep`) and `bisect` (only the stepping subcommands, the ones that check another commit out). Which forms those are is derived from safegit's git classification table rather than restated per command.

A third check covers the commands that COMPUTE an operation -- `merge`, `cherry-pick`, `revert` and `pull`, their `--no-commit` forms included -- which refuse (exit 5) when git already holds an operation in flight: they compute with `git <verb> --no-commit`, and that form does not inherit git's own refusal to start one operation over another. `rebase` makes the same refusal, scoped to an in-flight state that is NOT a rebase, so its own `--continue`, `--abort` and `--skip` pass by construction. The state-control forms (`--abort`, `--quit`) and the conclusion commands are outside it: they are the way out of the state, and a way out that refused over it would strand the repository.

`switch`, `rebase`, `reset` and `bisect` forward the operator's arguments to git after those checks. `merge`, `cherry-pick`, `revert` and `pull` do NOT: git computes the result with `--no-commit` and safegit's own pipeline writes the commit, so it carries safegit's trailers, the repository's `commit-msg` hook runs against it, and `safegit undo` reverses it. Each of them applies ONE thing -- one merge side, one picked commit, one reverted commit -- and a multi-commit or revision-range command line is refused naming the sequential form. Only `safegit rebase` still lets git author commits, uniformly and by declaration.

Every one of these commands validates its forwarded argv against an explicit ALLOWLIST before anything runs: an option safegit has not considered is refused (exit 2) rather than passed to git, where it could change what git does while safegit's checks and its record of the operation stayed written for something else. The refusals name what they refuse and why; `docs/divergences.md` catalogs them.

### Concluding an operation git stopped

When git parks a merge, cherry-pick or revert on a conflict, safegit -- not git -- finishes it:

| State | Conclude with | Abandon with |
|-------|---------------|--------------|
| merge in progress | `safegit merge-continue` | `git merge --abort` |
| cherry-pick in progress | `safegit cherry-pick-continue` | `git cherry-pick --abort` |
| revert in progress | `safegit revert-continue` | `git revert --abort` |

`safegit merge --continue`, `safegit cherry-pick --continue` and `safegit revert --continue` are refused and name the command above; a rebase's and a mailbox application's `--continue` stay git's own. Every conflicted path is declared with `--resolve 'path=ours|theirs|worktree|delete'` (or in a `--resolve-file` TOML), and a conclusion refuses rather than guessing: exit 17 when the declared paths are not exactly the conflicted ones, exit 18 when the content it would commit still holds a conflict block, exit 27 when materializing a declaration would write over a working-tree file that matches none of the conflict's own sides.

Two shapes safegit cannot START are also ones it will not CONCLUDE, and the refusal names git's own `--continue` as the way to finish what git began: a QUEUED cherry-pick or revert (a `.git/sequencer` directory, which only raw git can create now), and an octopus merge or a content conflict computed by a non-default strategy. See the Commands Guide for the full surface.

For automation, two properties are the ones to design around. First, the conclusion is non-interactive by construction -- there is no editor anywhere in it, and the message defaults to git's own draft with its comment block stripped. Second, every commit a conclusion produces is safegit's -- there is no shape where a safegit command name gets you a commit git authored -- so trailers, the `commit-msg` hook and `safegit undo` hold uniformly, and the states safegit cannot write for you meet a refusal naming git's own commands rather than a quiet handover.

## rlsbl release workflow

safegit integrates with [rlsbl](https://github.com/smm-h/rlsbl) for release orchestration, with the integration surfacing in three areas: push handling with pre-pre-push hooks, the rewrite journal that lets rlsbl repair release metadata after a history rewrite, and context-aware post-rewrite hints.

### Push handling

In rlsbl-managed projects, pushes happen exclusively through `rlsbl release run`, which handles version bumps, changelog finalization, tagging and pushing in one flow. There is no dev-branch push path: rlsbl has no `push` command, and its release entry points refuse to run on anything but a release branch. safegit's `push` is available for repositories that are not managed that way.

`safegit push` provides:

- Pre-pre-push hooks, run before `git push` opens a transport
- Automatic retry with exponential backoff on transport errors, with every lease re-pinned per attempt
- One oplog entry per successful push

### rlsbl detection

safegit detects rlsbl-managed repositories by checking for `.rlsbl/` or `.rlsbl-monorepo/` directories at the repository root. The detection is advisory, not a gate: destructive rewrites run in these repositories exactly as they do anywhere else. What it changes is the guidance printed afterwards.

**Push hints:** after a history rewrite, safegit prints context-appropriate instructions. In rlsbl-managed repos, it says "Complete the rewrite via your release tooling" instead of showing a raw `safegit push` command.

## Pre-push hooks (pre-pre-push)

safegit has its own hook system called "pre-pre-push hooks" that run before `safegit push` performs any network I/O, entirely separate from git's built-in pre-push hook. These hooks enable custom validation checks like changelog coverage enforcement, test suite runs, or lint passes before any data leaves the local machine.

### Hook lifecycle

1. `safegit push` resolves which refs will be pushed
2. Pre-pre-push hooks run with the resolved ref information on stdin
3. If any hook fails (non-zero exit) or times out, the push is aborted
4. Only after all hooks pass does the actual `git push` execute

### Installing hooks

```
safegit hook install /path/to/script.sh
```

This copies the script into the live store under the repository's **common** `.git/safegit/hooks/` and makes it executable, so a hook installed from a linked worktree is the hook every worktree of the repository runs. An existing destination is refused rather than overwritten: upgrading a hook is `safegit hook remove <name>` followed by an install.

Discovery walks TWO stores in full, at any depth, in a deterministic order -- the store the CHECKOUT provides (`.safegit/hooks/` in the work tree, which is repository content everyone who clones gets) first, then the live store -- each sorted by its store-relative name. Within a store the traditional two shapes are just names: `pre-pre-push` is the single-file hook, `pre-pre-push.d/*` the directory of them, and anything else in the store is a hook too. Files starting with `.` or ending in `~` are not hooks by name and never run.

Membership of the checkout-provided store is the DIRECTORY, not git's tracking: an uncommitted -- even gitignored -- executable file there runs on the next push exactly like a committed one. The consequence, stated plainly: cloning a repository and pushing from that checkout runs the repository's committed scripts. Execution happens only on `safegit push` and `safegit hook run`, never on clone, fetch, checkout or any inspection command, and `safegit hook list` names every location with its origin so the set can be read beforehand.

A non-executable hook is a refusal (exit 25) in BOTH stores, on `safegit push` and on `safegit hook run` alike -- never a skip and never a "no hooks to run". A hook is disabled by removing it, not by dropping its mode, so a lost mode bit must not silently stop the repository's checks. `safegit hook list` still lists it, marked `NOT EXECUTABLE`.

**Hooks still in the pre-migration location do not run.** `.git/hooks/pre-pre-push` and `.git/hooks/pre-pre-push.d/` are where safegit kept its hooks before the live store existed; their presence makes every push and every `hook run` refuse with exit 24 until `safegit hook migrate` relocates them.

### Hook environment

Every pre-pre-push hook receives these environment variables providing the remote name and URL being pushed to, the hook phase identifier, and the configured timeout in seconds. Hook stdin follows the same format as git's built-in pre-push hook, with one line per ref being pushed containing local ref, local SHA, remote ref, and remote SHA fields:

| Variable | Description |
|----------|-------------|
| `SAFEGIT_REMOTE_NAME` | Name of the remote being pushed to (e.g., `origin`) |
| `SAFEGIT_REMOTE_URL` | URL of the remote |
| `SAFEGIT_PHASE` | Always `pre-pre-push` |
| `SAFEGIT_HOOK_TIMEOUT_S` | Timeout in seconds for this hook run |

Hook stdin follows the same format as git's pre-push hook: one line per ref being pushed, with fields `<local-ref> <local-sha> <remote-ref> <remote-sha>`.

### Hook management

- `safegit hook list` -- show every hook location with its origin (`local`, `tracked`, `legacy`), path and executable state, including non-executable and non-hook entries, because the hook an operator is asking about is usually the one that is NOT running
- `safegit hook run` -- run all hooks without pushing
- `safegit hook run <name>` -- run a specific hook by name
- `safegit hook remove <name>` -- remove one hook from the live store by name; a name only the checkout provides is refused with an explanation (removing that one means deleting the file and committing it), and a name both stores carry removes the live one and says the other still runs
- `safegit hook migrate` -- move safegit's hooks out of git's own `.git/hooks` into the live store

`hook run` declares that it cannot be previewed: `--dry-run` is refused with its reason rather than rendering an invented would-do log, because a hook is an operator-supplied script whose effects safegit cannot know. Use `hook list` to see which scripts a push would run.

### Timeout configuration

The default hook timeout is 1800 seconds (30 minutes), which is generous enough for most CI-like checks but configurable per-repo for hooks that need more or less time to complete:

```
safegit config set hooks.preprepush.timeoutSeconds 300
```

The configured value is the only one. A hook cannot raise or lower its own limit -- there is no `# safegit: timeout=NNN` line and no other in-band override -- because a limit the limited script rewrites for itself is not a limit. `SAFEGIT_HOOK_TIMEOUT_S` hands the hook the value it is running under so a script can monitor itself against it.

### Submodule hook cascading

When `safegit push` runs inside a submodule, it automatically discovers and runs pre-pre-push hooks from both the parent repository and the submodule itself, executing parent hooks first to enable organization-wide push policies that apply uniformly across all submodules in the project.

### Interaction with git pre-push hooks

rlsbl installs a git pre-push hook (`.git/hooks/pre-push`) that runs `rlsbl check --tag prepush` to enforce JSONL changelog coverage and its other pre-push checks. (The older `rlsbl pre-push-check` command was removed; a repository whose hook still calls it needs `rlsbl scaffold` to regenerate the hook.) This is a standard git hook, separate from safegit's pre-pre-push system. The execution order is:

1. safegit pre-pre-push hooks (safegit's own system)
2. `git push` executes
3. git's pre-push hook runs (rlsbl's coverage check)

If either layer rejects the push, the operation fails.

## Scrub orchestration protocol

A history rewrite moves every commit it touches, which invalidates three things that live outside the commit graph: changelog entries that name commit hashes, the remote's tags, and the forge releases attached to those tags. safegit does not require permission to rewrite. It rewrites, journals what moved, and leaves the repair to the release tooling, which detects the damage loudly and heals it from that journal.

### The orchestrated path

1. The user runs `rlsbl release scrub`
2. rlsbl invokes `safegit scrub` as a subprocess, passing `--remap-shas-in` for the changelog globs and `--approve-consequential` for the destructive confirmation
3. safegit rewrites history and writes rewrite maps to `.git/safegit/rewrite-maps.jsonl`
4. rlsbl reads the rewrite maps and performs post-scrub work:
   - Remaps commit hashes in JSONL changelog files (via `--remap-shas-in`)
   - Regenerates `CHANGELOG.md`
   - Updates tags
   - Recreates GitHub Releases

### The unorchestrated path

A raw `safegit scrub` in a release-managed repository is allowed and does the same rewrite, minus the post-scrub work. The result is detected rather than prevented: rlsbl's changelog hash-resolution check fails on the now-dangling hashes, `rlsbl changelog remap --from-journal` rewrites them from the journal safegit left behind, and `rlsbl release reconcile` re-pushes the moved tags and recreates their GitHub Releases. Nothing about the recovery depends on the rewrite having been announced in advance.

### Rewrite maps

Every destructive scrub persists crash-safe rewrite maps to `.git/safegit/rewrite-maps.jsonl`, a flock-guarded JSONL file that records the full old-to-new commit SHA mapping in three phases so orchestrators can remap hashes in dependent files. Each rewrite produces three JSONL records sharing a unique ID:

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
  --remap-shas-in ".rlsbl/changes/*.jsonl" --reason "leaked API key" --entire-history
```

The range is a required choice on all three: `--entire-history` or `--from <sha>`, never a default.

## Environment variables

### Variables safegit reads

| Variable | Used by | Purpose |
|----------|---------|---------|
| `CLAUDE_CODE_SESSION_ID` | `commit`, `undo`, oplog | Session scoping for undo operations and commit trailer injection. When set, commits get a `Claude-Code-Session-Id` trailer. `undo` filters oplog entries to the current session. |

### Variables safegit sets (for hooks)

| Variable | Set by | Value |
|----------|--------|-------|
| `SAFEGIT_REMOTE_NAME` | `push`, `hook run` | Name of the remote (e.g., `origin`) |
| `SAFEGIT_REMOTE_URL` | `push`, `hook run` | URL of the remote (or `manual-run` for `hook run`) |
| `SAFEGIT_PHASE` | `push`, `hook run` | Always `pre-pre-push` |
| `SAFEGIT_HOOK_TIMEOUT_S` | `push`, `hook run` | Hook timeout in seconds |

## Configuration reference

All safegit configuration lives in `.git/safegit/config.json` and is managed with the `safegit config show`, `safegit config get <key>`, and `safegit config set <key> <value>` subcommands. The following keys control commit retry behavior, lock timeouts, hook execution, push retries, and the submodule auto-bump decision. These five are the whole key set: writing anything else reports an unknown config key, and every integer key must be a positive integer, parsed whole (`5abc` is refused, not silently read as 5).

| Key | Default | Description |
|-----|---------|-------------|
| `commit.casMaxAttempts` | 5 | Maximum CAS retry attempts for concurrent ref updates (no upper bound) |
| `commit.autoBumpParent` | (unset, and an unset one is a refusal) | Whether a commit in a submodule also commits the parent's moved gitlink |
| `lock.acquireTimeoutSeconds` | 30 | Timeout for acquiring a lock (per-ref, worktree operation, or rewrite) |
| `hooks.preprepush.timeoutSeconds` | 1800 | Timeout for pre-pre-push hook execution |
| `push.retryAttempts` | 3 | Number of push retry attempts on transport errors |

There is no oplog size or rotation key. The oplog is append-only and complete by design; the exclusive lock held across each append is what makes concurrent appends atomic, so entries have no size limit and nothing truncates the file. `log.maxSizeMB` was removed: an existing `config.json` carrying it still loads (unknown members are ignored), but reading or writing the key is an unknown-key error.

## Submodule integration

When safegit detects it is running inside a git submodule, two additional behaviors activate to coordinate commits between the submodule and its parent repository, and to cascade push hooks from the parent down to nested submodules:

- **Auto-bump parent, and the decision is mandatory.** `commit.autoBumpParent` is read from the PARENT repository's config. `true` means every `safegit commit` in the submodule also creates a bump commit in the parent updating the submodule pointer; `false` means deliberately do not. **Absent means nobody has decided, and safegit refuses rather than guessing** -- either guess is wrong in somebody's repository. The refusal happens BEFORE anything is committed, and it names the command that settles it (`safegit config set commit.autoBumpParent true`, run in the parent). A dry run validates it the same way, reading the parent's config and creating nothing there. `undo` is the one path that can only meet the question after the rollback it is undoing. Nested submodules are detected and rejected.

- **Hook cascading:** `safegit push` discovers and runs pre-pre-push hooks from both the parent repo and the submodule, with parent hooks executing first.

## Machine mode (`--json`)

`--json` selects the CLI framework's machine mode, on every command. **stdout then carries exactly one document: the envelope.** safegit's own data is its `payload` member, the recorded effects of a `--dry-run` are its `preview` member, and everything safegit would have said in human text is either absent or a diagnostic:

```json
{
  "interface_version": 2,
  "app": "safegit",
  "app_version": "<the running safegit's version>",
  "command": "scrub.file",
  "exit_code": 0,
  "payload": {"version": 1, "dry_run": true, "file": "secret.txt", "commit_count": 3},
  "dry_run": true,
  "writes": null,
  "preview": [
    {"seq": 1, "verb": "run", "kind": "proc_mutate", "recorded": true,
     "detail": "git update-ref refs/heads/main <rewritten> 9e46d1bb"}
  ],
  "preview_error": null,
  "diagnostics": []
}
```

Parse the whole stream: there is no trailing would-do log to cut off, and no second document to skip.

### Where the machine contract ends

The one-document promise is about stdout on a run that reaches the framework's
emission point. A consumer has to handle three failure shapes, and they are not
interchangeable:

1. **A nonzero exit that still carries a payload.** The commit-stands family
   (exit **26**) is the shape this exists for: the operation's ref move is real
   and its aftercare did not finish. The envelope is emitted, `exit_code` is 26,
   and the payload names what stands and what was left -- the created SHA, the
   `residue` list of the aftercare steps that failed, and, on a conclusion, the
   `autostash` member. **Payload carries aftercare**: a partial outcome is
   reported in the document, never only in prose. Read the payload on a nonzero
   exit rather than assuming it is null.
2. **An envelope whose payload is null.** A refusal that reaches dispatch emits
   the envelope with `exit_code` set and `payload: null`, because a payload
   schema describes a performed operation and there is no error-payload channel
   to describe a refused one. `safegit mv`'s dirty-move refusal (exit 19) is the
   worked example: the complete, never-truncated list of offending paths goes to
   **stderr**, which machine mode never suppresses, while the envelope carries
   only the exit code. The same holds for the previews of `merge`,
   `cherry-pick`, `revert` and `pull`: a preview computes the outcome and stops
   before the pipeline conclusion that would build the payload, so those four
   emit `payload: null` under `--dry-run --json`. `preview` usually carries the
   compute step's recorded argv, with two exceptions worth knowing before you
   parse it. A merge preview that answers **"a fast-forward"** records NOTHING,
   because the real run performs that one itself with a compare-and-swap and
   never invokes git's merge machinery; a merge preview that answers **an
   `--ff-only` refusal** records nothing either, because safegit decides that
   refusal before git runs. In both cases recording the compute step would name a
   subprocess nothing runs. The consequence is stated rather than hidden: such a
   run answers with `exit_code: 0`, `payload: null` and `preview: []`, which in
   machine mode is indistinguishable from a preview that found nothing to say.
   For the refusal that is the whole truth -- nothing would be performed. For the
   fast-forward it is a gap: its ref move is not minted as an effect on the
   preview path, so there is nothing else for the envelope to carry, and a
   machine consumer that needs the verdict must run the preview in human mode,
   where the sentence is printed.
3. **No envelope at all.** Some refusals exit before the framework's dispatch
   returns, and those write to stderr and exit with nothing on stdout.
   `safegit commit`'s refusals are the class to know: every one probed answers
   this way -- the general failure (1), a rejected command line (2), a path that
   matched nothing (11), a move the repository does not bear out (19) and the
   non-portable symlink target (29). `safegit mv` is the contrast, and it is
   worth stating because the two look alike from outside: its refusal of the
   same move class (19) DOES emit a null-payload envelope. This set is
   deliberately shrinking: every post-ref-update failure was moved onto shape 1
   precisely so that the paths where an operation half-happened always answer
   with a document. Treat empty stdout plus a nonzero exit as a valid outcome,
   not as a parse failure.

One capture limit, stated because it changes an exit code: under `--json` the
guarded commands capture the child git's output instead of streaming it, and the
capture decodes as text. A child that writes **non-UTF-8 bytes** -- a path in a
foreign encoding in a conflict listing, say -- fails that decode, and the run
degrades to the general failure code instead of reporting git's own verdict. At
a terminal, where the output is streamed rather than captured, the same command
is unaffected.

Two members are the framework's and are the same on every safegit command. `writes` is the write set of a command declaring an update contract; safegit declares none, so it is always `null` -- present, never absent. `preview` is the effect log, and it is populated in BOTH modes: on a real run each record carries `"recorded": false`, meaning the effect was performed rather than recorded. **Read `dry_run` to tell the two apart, never the presence of `preview`.**

Each command that produces a payload **declares its JSON Schema**, and the framework validates the value against that declaration before writing it -- a wrong shape fails the run instead of shipping. `safegit --dump-schema` publishes every declaration verbatim.

The properties worth knowing:

- **Write `--json` before a guarded command's name.** `safegit --json merge feature` selects machine mode; `safegit merge --json feature` does not. The framework reads its own flags anywhere in the command line up to the name of a command that takes git's vocabulary -- `switch`, `merge`, `cherry-pick`, `revert`, `rebase`, `reset`, `bisect` -- and after that name argv belongs to that command's git-shaped parser, whose allowlist refuses the flag (exit 2) naming the pre-command form. The same holds for `--dry-run`, `--quiet`, `--verbose` and `--approve-consequential`. Every other command accepts them on either side.
- **The envelope is exempt from `--quiet`.** `--json --quiet` emits the complete document; quiet governs the human stream only.
- **`--json` does not imply approval.** A non-interactive `--json` run of a *consequential* command (`scrub file`/`match`/`run`, `author rewrite`) must pass `--approve-consequential` explicitly. Ordinary mutating commands such as `commit` need nothing. A `--json backup backup` to a remote safegit cannot prove is private is the one place `--approve-consequential` is not the answer either: that question belongs to the target, so it takes `--allow-public-remote`.
- **A successful run of a payload-producing command always carries its payload.** `scrub match` and `scrub run` used to emit a null payload on their nothing-matched early returns, so a machine consumer could not tell "the run said nothing matched" from "the run produced nothing"; both now answer with the payload in every completing shape, which is the one-envelope invariant doing its job.
- **git's own push output moves.** `push` captures git's streams rather than passing them through, and under `--json` git's stdout is re-routed to stderr, so the envelope stays the only document on stdout.
- **There is no JSON error OBJECT, but a failure is not always silent on stdout.** A command that fails writes its message to stderr and exits nonzero, and safegit never writes a second, error-shaped document: the envelope is the only document machine mode has. What a failure produces on stdout is one of the three shapes above -- an envelope with a payload (the commit-stands family), an envelope with `payload: null`, or nothing at all where the path exits before dispatch. The human-readable reason is on stderr in all three.

This makes safegit suitable for embedding in tool pipelines that parse structured output.
