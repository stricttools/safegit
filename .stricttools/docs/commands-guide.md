+++
title = "Commands Guide"
description = "Complete reference for every safegit command: commit, undo, push, pull, backup, scan, scrub, doctor and author, with flags, machine-mode output, examples and safety guarantees."
+++

# Commands Guide

safegit is a concurrency-safe git wrapper providing atomic commits, oplog-based undo, history rewriting, and multi-agent coordination. This guide covers every command in detail.

## Global Flags

Every safegit command accepts these global flags, which control output verbosity, dry-run previewing, interactive prompt behavior, configuration file location, and machine-readable JSON output mode.

| Flag | Default | Description |
|------|---------|-------------|
| `--quiet` | `false` | Suppress informational output, only showing errors and results |
| `--verbose` | `false` | Enable verbose output with detailed progress and diagnostic info |
| `--dry-run` | `false` | Preview what would happen without changing the repository |
| `--approve-consequential` | `false` | Approve a consequential command up front instead of being asked |
| `--json` | `false` | Select machine mode: stdout carries the framework's envelope and nothing else |
| `--config-file` | optional | Path to a custom safegit config file; omitted means the default location |

The first five are owned by the CLI framework, not by safegit. Five consequences follow:

- **They have no short forms.** `-q`, `-n` and `-y` are gone; write `--quiet`, `--dry-run` and `--approve-consequential`. The approval flag is deliberately unwieldy so it cannot decay into muscle memory.
- **They are recognized anywhere in the command line -- with one boundary.** `safegit --dry-run push` and `safegit push --dry-run` are the same run. `--config-file` is safegit's own rather than the framework's, but it is registered as an app-level global, so it is accepted on either side of the subcommand too and appears under "Global flags" in every command's `--help`. The boundary is a command that takes git's own vocabulary: after `switch`, `merge`, `cherry-pick`, `revert`, `rebase`, `reset` or `bisect`, argv belongs to that command's own git-shaped parser, so one of these flags written there is read as an option for git rather than for the framework. It is not silently mistaken for one either -- the command's allowlist refuses it by name. **Write the flag before the command:** `safegit --dry-run merge feature`, not `safegit merge --dry-run feature`.
- **Only *consequential* commands ask before they run.** Classification (`read_only` / `mutating`) decides what a dry run records; it does not decide what prompts. A command prompts only when it declares itself **consequential**, and in safegit exactly four do: `scrub file`, `scrub match`, `scrub run` and `author rewrite` -- the operations that rewrite history irreversibly. Each prompts `about to run consequential command '<name>'. Proceed? [y/N]` on a terminal, and refuses outright with `error: stdin is not interactive; a consequential command must be confirmed at a terminal` when there is no terminal to ask at. **Everything else -- `commit`, `mv`, `push`, `pull`, `undo`, `config set` and the guarded commands -- runs bare, with nothing added to the command line.**
- **Three conditions ask on their own, and each owns its consent.** They are conditions the framework cannot see, so they are safegit's own seams rather than a fifth, sixth and seventh consequential command: `doctor --action uninstall` and `push --force-with-lease` are answered by `--approve-consequential` (the condition IS the flag the caller typed), while a `backup backup` to a remote that is public -- or whose visibility safegit cannot determine -- is answered ONLY by `--allow-public-remote`, because that fact is discovered at run time and the caller may not know it. `--json` answers none of them and refuses instead; a declined confirmation always exits nonzero.
- **Every prompt is written to stderr, and `--quiet` never suppresses one.** That holds for the framework's own confirmation and for safegit's three run-time seams (`doctor --action uninstall`, `push --force-with-lease`, and a `backup backup` to a remote safegit cannot prove is private). stdout is a structured channel -- a command's own result, and under `--json` exactly one document -- so a question written there would interleave with the answer to a different one, and a prompt a quiet run hid would be a prompt that hangs.

`--json` does **not** imply `--quiet`, and it never implies approval. The two are independent: `--quiet` governs the human stream, and the envelope is not written through the writers `--quiet` can reach, so `--json --quiet` still emits the complete document. `--json` says how to answer; it says nothing about consent. Adding it to a command line can therefore never destroy or publish anything on its own.

Under `--dry-run` the framework writes a **would-do log** to stdout after the command's own output, listing every mutation the run would have performed:

```
DRY RUN — no changes were made. Would do:
  1. run: git push origin refs/heads/main:refs/heads/main (granted: push — publishing local refs to a remote is what this command is for)
```

The log is never suppressed by `--quiet`. In machine mode it is not printed as text at all: the same records ride the envelope's `preview` member, so a machine-readable dry run's stdout is still exactly one JSON document. Parse it whole.

Two things a dry run does do, stated because "changes nothing" is a promise about the repository and not about the machine:

- **A preview of a commit still builds objects, into a throwaway quarantine.** `commit`, `mv` and the conclusion commands stage, write the tree and build the commit object exactly as the real run would, but every git subprocess writes its objects into a temporary directory outside the repository that is deleted when the command exits. `.git/safegit` is left alone -- a dry run does not even create it in a repository where safegit has never run -- and no lock file, oplog line or ref update happens at all.
- **A preview still READS, including over the network where the command reads over the network.** `push --dry-run` contacts the remote to observe the refs it would publish, because the leases it would send are pinned to what is actually there; the preview then records the push instead of performing it. The one command whose preview is deliberately local-only is `backup backup` (see its section).

## commit

Stage and commit specified files in a single atomic operation. This is safegit's core command -- it uses a per-invocation temporary index to isolate each commit from concurrent sessions, then updates the branch ref with compare-and-swap (CAS) retries.

### When to Use

Use `safegit commit` instead of `git add` + `git commit` whenever multiple sessions might share the same worktree. It prevents index races and file leaks between commits by staging files into an isolated temporary index and updating the branch ref with compare-and-swap retries, so concurrent commits never corrupt each other.

### Flags

| Flag | Short | Presence | Description |
|------|-------|----------|-------------|
| `-m` | `-m` | optional | Commit message paragraph; repeatable, and the values are joined with a blank line between them, so `-m subject -m body` is a subject and a body |
| `-F` | `-F` | optional | Read the full commit message body from a file (mutually exclusive with `-m`) |
| `--branch` | | optional | Commit onto a different branch without switching to it |
| `--amend` | | optional; omitted means a new commit | Amend the current HEAD commit by replacing it with updated content |
| `--allow-empty` | | optional; omitted means an empty commit is refused | Allow creating a commit even when no files have been changed |
| `--trailer` | | optional | Add a key-value trailer line to the commit message (repeatable) |
| `--hunks` | | optional; omitted means every named file is committed whole | Commit only the selected hunks of one file, as `path:1,3` or `path:2-4`; repeatable, once per path |
| `--untrack` | | optional; omitted means nothing is untracked | Stop tracking a path, leaving the file on disk: the commit records its removal from the index (repeatable) |
| `--moved` | | optional; omitted means the commit declares no moves | Declare that content moved, as `'old -> new'` (repeatable). End BOTH paths with a slash for a whole subtree. The old path must be tracked in the commit's parent and gone from disk, and the new one must exist -- and the commit itself must bear the move out, carrying the new path in its tree and no longer carrying the old one, so name both paths among the files to commit |
| `--allow-non-portable-targets` | | optional; omitted means such a link is refused | Record a symlink whose target text will not resolve in another checkout -- an absolute target, or a relative one resolving outside the repository -- as the link text. Omitted (and with `--no-allow-non-portable-targets`) the commit is refused with the target named |
| `--moved-retract` | | optional; omitted means the commit retracts nothing | Retract a move record declared earlier in this branch's history, by its id -- the token a `Moved:` trailer begins with (repeatable, one id each). The id must name a record that exists and is not already retracted in the history this commit is built on; one that does not is refused (exit 19) rather than written, and every bad id is named. `--trailer 'Moved-Retract: <id>'` writes an unchecked retraction instead |

### Arguments

| Name | Required | Description |
|------|----------|-------------|
| `files` | No (variadic) | Files to commit, taken literally -- a colon in an argument is part of the filename |

Hunk selection lives entirely in `--hunks`, and a positional path is always the
literal name of a file. The split inside a `--hunks` element is on its LAST
colon, so `--hunks 'sprint:1:2,3'` selects hunks 2 and 3 of the file named
`sprint:1`. Naming one path both as a positional and in `--hunks` is refused, as
is naming it twice in `--hunks`: each element states the whole selection for its
path. The refusal is decided on the paths themselves, not on how they were
typed, so `-- ./a.go --hunks a.go:1` is the same contradiction as
`-- a.go --hunks a.go:1` and is refused the same way.

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
safegit commit -m "partial stage" --hunks 'main.go:1,3'

# A filename that contains a colon is an ordinary positional path
safegit commit -m "add notes" -- 'sprint:1'

# Stop tracking a build directory and record the .gitignore pattern in one commit
safegit commit -m "stop tracking build output" --untrack dist/bundle.js -- .gitignore

# Declare a move: both halves are named as ordinary paths, and the record goes
# into the commit message
safegit commit -m "move the parser" --moved 'src/parse.go -> internal/parse/parse.go' -- src/parse.go internal/parse/parse.go

# Declare a whole subtree move with one record (both sides end in a slash)
safegit commit -m "move src to lib" --moved 'src/ -> lib/' -- src lib

# Add the record to a move that was already committed without one
safegit commit --amend --moved 'src/parse.go -> internal/parse/parse.go'

# Allow an empty commit (no file changes)
safegit commit --allow-empty -m "trigger CI rebuild"
```

### Symlinks, and the targets that will not resolve elsewhere

A symlink is committed as its LINK TEXT -- the string it points at -- exactly as git records one. safegit adds one rule about which link texts it will record, and the rule is PORTABILITY: since the text is all that gets stored, the only question is what a checkout somewhere else makes of it.

Two shapes fail that question and are REFUSED (exit **29**), naming the literal target, with nothing staged and nothing committed:

- an **absolute** target, whether or not it resolves inside this checkout -- it resolves against a machine's filesystem rather than against the repository, so a checkout at any other path finds nothing there, or finds a file the repository never carried;
- a **relative** target that resolves **outside the repository** -- portable in spelling, but pointing at something the repository does not carry.

A relative target that resolves inside the repository is portable and commits as usual, including one that climbs out of its own directory with `..` and comes back down, and including one whose target does not exist yet.

The remedies differ, so the refusal states the one that fits the shape. A relative target that leaves the repository has to be pointed back inside. An absolute target gets both halves of the answer, because the judgment never resolves it and the group therefore holds both cases: spell it relative to the link if it points inside the repository, and if it points outside there is nothing portable to spell -- point the link inside instead. A commit naming offenders of both shapes gets ONE refusal with the offenders grouped by shape, each group followed by its own remedy.

`--allow-non-portable-targets` elects committing such a link anyway, and restores the one-line notice on stderr saying the link will not resolve elsewhere. The election is a fact about ONE invocation and is recorded nowhere, so a repository that deliberately carries such a link needs the flag on EVERY later commit that names that link or sweeps it up by directory expansion.

The judgment is made in the commit family's intake and nowhere else: `safegit commit` and its `--amend` form, over the paths that invocation stages -- the ones named on the command line, a `--moved` commit's paths among them, and the ones a directory argument expands to. Two other ways link content reaches a tree do not pass through it: `safegit mv` moving an already-tracked link carries the blob across and never re-reads it, and a conclusion's `--resolve path=worktree|ours|theirs` stages a conflicted path's content directly.

### Safety Guarantees

- **Atomic staging**: Each commit uses a per-invocation temporary index. The shared `.git/index` is never written to during the staging phase, so concurrent commits cannot leak files into each other.
- **CAS ref updates**: Branch refs are updated using `git update-ref` with the expected old value. If another session committed between staging and ref update, the CAS fails and the operation retries (up to `commit.casMaxAttempts`, default 5).
- **Two locks, in one fixed order**: `commit` takes the worktree operation lock around the whole invocation (a second safegit process in the same worktree waits `lock.acquireTimeoutSeconds` and then exits **8** naming the holder), and the per-ref lock for the target ref inside it, immediately before the CAS update. Nothing takes them the other way round. A lock whose holder is genuinely gone is reclaimed automatically; a lock a live process still holds is never taken from it.
- **Oplog recording**: Every commit, amend, and reword is logged to an append-only operation log, enabling `safegit undo`.
- **Moves are declared or observed, and never detected from contents**: safegit runs no similarity scoring, no `diff -M`, and never stages a path the caller did not name. A move is stated with `--moved 'old -> new'`, checked against the repository (the old path tracked in the commit's parent and gone from disk, the new one present), and written into the commit message as a `Moved:` record with its own identifier. A declaration the repository does not bear out is refused with exit **19**, and nothing is committed. Both halves of the move are still ordinary arguments -- committing the deletion of the old path is naming it. Where the move has not happened yet, `safegit mv` does the move, the record and the commit in one step instead.
- **What the commit's own delta witnesses is recorded without being asked**: where the same blob leaves one path and arrives at another, both sides are regular files, the pairing is one-to-one and the blob sits at exactly one path in each tree, the commit carries a record for it marked `observed` -- the token after the id that separates a claim safegit derived from one a person made (a declared record carries no token). A whole directory that moved is one subtree record; a commit carrying more scattered inferred moves than safegit records on its own gets none of them and one stderr line pointing at `--moved`; every candidate a fence declined rides the commit payload with its reason. An amend records what ITS authoring event witnesses and preserves everything the message already carried; a reword changes no tree, so it records nothing.
- **A declaration outranks all of it**: the paths a `--moved` pair names leave the candidate sets and the fences' tree listings before any pairing, so nothing is ever stated twice -- and declaring a pair that an OBSERVED record on the commit being amended already carries supersedes that record, writing its retraction and the new declaration together.
- **`--untrack`ed paths are fenced off the same way**: a path removed from the index but still sitting on disk could pair with a same-blob addition and mint a record, which would be true about the TREE while the old file is still right there. The declared spelling refuses that same claim (`--moved` with the old path still on disk exits **19**), so inference does not make it either: the named `--untrack` paths join the suppressed set exactly as declared paths do, and each candidate they suppress is reported as a refused pair naming `--untrack`. The fence is scoped to the paths this command line named, not to a general on-disk check.
- **A retry that would change the answer aborts rather than guessing**: inference runs per compare-and-swap attempt, so a concurrent commit can move the ground under it. The record set of the first attempt is kept as DATA (never re-parsed out of the cached message, which a rewriting `commit-msg` hook may have edited), each retry recomputes, and a set that differs aborts the operation with its own exit code naming the pair whose witness changed, and advice to re-run. It is the one purely transient, auto-retryable abort safegit produces, which is why it does not share the general failure code.
- **A `commit-msg` hook in a consumer repository now sees records it did not before**: the records go on the message BEFORE the hook runs, exactly like every other piece of caller content, and a repository whose hook rejects unknown trailer keys or rewrites trailer blocks will meet `Moved:` lines on commits nobody declared a move for. A hook that rewrites the block is adopted as written (dropping a record is then that hook's doing); a hook that refuses exits **16** and nothing is committed. Declaring the moves does not avoid the lines -- it changes who claimed them.
- **Two declarations that speak about each other are refused**: a set of `--moved` pairs is one statement, so two pairs may not NEST (one path inside another pair's path, in either direction) and may not CHAIN (`a -> b` beside `b -> c`, whose result would depend on the order they were performed in). Both are argument-against-argument contradictions and exit **2**, before the repository is consulted. `safegit mv` refuses the same two shapes through the same check.
- **Retraction, not editing**: a record that turns out to be wrong is corrected by RETRACTING it -- `--moved-retract <id>`, which verifies the id against the history the commit is built on -- never by editing it, because editing the commit that carries it rewrites history. A replacement is a retraction and a new `--moved` in one commit.

## mv

Move tracked paths and commit the moves with their records in one operation. `--moved` is a DECLARATION about a move somebody already made; `mv` is the other half -- it performs the move, mints the record for what it moved, and commits the result, so the move and its record can never be out of step.

### When to Use

Use `safegit mv` when the move has not happened yet. Use `safegit commit --moved` when it already has (the files are at their new paths and the record is missing).

### Flags

| Flag | Presence | Description |
|------|----------|-------------|
| `-m` | required; repeatable, no default | Commit message paragraph. Repeating it joins the values with a blank line between them, so the first is the subject and the rest are the body. There is no default message: a message the framework chose would be a message the framework wrote into history |
| `--create-missing-directories` | optional; omitted means a destination whose directory does not exist is refused | Make the destination's parent directories when they are not there, removing again what this invocation made if the move is rolled back |

### Arguments

| Name | Required | Description |
|------|----------|-------------|
| `pairs` | Yes (variadic) | One move each, written `'old -> new'`. End BOTH paths with a slash to move a whole directory. Quote a path C-style when it holds a space, a quote, a backslash or the arrow itself |

### The three steps: validate, move, commit

1. **Validate, before the first file is touched.** Every pair is checked against the repository and against the other pairs: the source must be tracked in HEAD and present on disk and carry no uncommitted content edits, the destination must be free (on disk and in the tree) and its parent directory must exist, a directory must be written in subtree form and a file in file form, and no two pairs may nest or chain. A set of moves is one statement, so a set with one bad pair in it never leaves the working tree half-moved -- and every failing pair is reported at once rather than one command at a time.
2. **Move.** Each path is moved on disk through the effects handle, so `--dry-run` records the moves instead of performing them. A rollback removes the topmost directory this invocation created and nothing that was already there.
3. **Commit.** One commit carrying the moves and their `Moved:` records.

### The commit is the move and nothing else

Each moved path is carried across as the exact blob its parent commit held, through index edits rather than by staging from disk. That is what `git mv` followed by a commit produces, and it is what makes the preview and the execution compute the same tree. Case-only moves fall out of the same mechanism.

A directory pair produces ONE subtree record however many files it holds, while the commit itself changes every path under it.

### A path with uncommitted edits is refused

Because the commit carries the parent's blob across, a moved path whose disk content has been edited would have those edits silently left behind, uncommitted, at a path that no longer exists in the tree. `safegit mv` therefore REFUSES rather than moving it (exit **19**, the same collected refusal a missing destination raises -- the world contradicts the move's preconditions). There is no override flag, because both legitimate intents already have a route, and the refusal names them:

1. the edits belong in their own commit -- commit the content first, then `safegit mv`;
2. the edits should ride along with the move -- move the files on disk yourself, then `safegit commit --moved 'old -> new' -- <old> <new>`, which stages from disk and commits the content and the move together. Both paths are named because a declaration is checked against the tree the commit writes: naming only the destination leaves the old path in that tree, and the declaration is refused.

The check is filter-aware: the disk bytes are hashed with `--path <newpath>` so the repository's own attributes decide, and a checkout that converted line endings never false-refuses. Every dirty path is named -- the human output aggregates them for a subtree move, and the complete list goes to stderr, never truncated. A dry run refuses identically.

### A missing destination directory is refused

`safegit mv a.txt sub/a.txt` where `sub/` does not exist is refused, naming the missing directory, with nothing moved. That is what `git mv` does too. `--create-missing-directories` elects the creation instead; a rollback then removes exactly the directories this invocation added and nothing that was already there.

### Examples

```bash
# Move one file and commit the move with its record
safegit mv -m "move the parser" 'src/parse.go -> internal/parse/parse.go'

# Move a whole directory: both sides end in a slash, and it is one record
safegit mv -m "move src to lib" 'src/ -> lib/'

# Several moves as one statement -- all of them, or none
safegit mv -m "regroup the loaders" 'a.go -> load/a.go' 'b.go -> load/b.go'

# Preview: the moves are recorded, not performed
safegit --dry-run mv -m "move the parser" 'src/parse.go -> internal/parse/parse.go'
```

### Safety Guarantees

- **Nothing moves until everything checks out**: a pair the repository does not bear out -- an untracked source, an occupied destination, a missing destination directory, a source carrying uncommitted content edits -- exits **19** naming every failing pair, with nothing moved and nothing committed. A contradiction between the arguments themselves -- an unparseable pair, two pairs claiming one path, a nesting or a chain -- exits **2** before the repository is read at all.
- **Rollback on a filesystem failure**: when a move the checks could not foresee fails part-way through, every move this invocation had already made is put back, and nothing is committed.
- **A commit failure leaves the files moved**: the moves stand, the message says so, and `safegit commit --moved` commits them where they are once the cause is fixed.
- **Serialized like every other tree mutation**: `mv` takes the worktree operation lock around the whole operation -- the moves and the commit are one step -- and refuses (exit **5**) when git has a merge, cherry-pick, revert, rebase or mailbox application in flight. That check is made inside the lock and BEFORE the first move.
- **Undo reverses the commit, never the working tree**: `safegit undo` on an `mv` moves the ref back and says so -- the files are still at their new paths. Move them back by hand, or re-commit them where they are with `safegit commit --moved`.

## The three conclusion commands

`merge-continue`, `cherry-pick-continue` and `revert-continue` finish an operation git started and stopped before committing. They are not passthroughs: safegit writes the commit itself, so it carries safegit's trailers, the repository's `commit-msg` hook runs against it, and `safegit undo` can reverse it.

They are separate verbs rather than a mode of `commit` because git's model for finishing one of these operations is whole-index -- the state file names the other side, the index holds the operation's staged result, and a pathspec-less `git commit` turns both into a commit. `safegit commit` is the opposite by contract: pathspec-only, a temporary index seeded from the parent tree, one parent. A flag that silently switched between the two would be the ambiguity safegit exists to remove, so a conclusion asks for the SHARED index as its base and names the state file's commits as extra parents.

**git's own `--continue` is refused for these three operations.** `safegit merge --continue`, `safegit cherry-pick --continue` and `safegit revert --continue` exit **5** and name the safegit command that does conclude the state:

```
error: safegit merge --continue does not conclude a merge of 5bc7ac7c; safegit does
  git's own --continue would commit the whole index itself, with none of safegit's trailers
  and none of its commit-time machinery. Use the command that does:
    conclude it:  safegit merge-continue
    abandon it:   git merge --abort
```

A rebase and a mailbox application are not in that set: safegit has no verb that finishes either, so `safegit rebase --continue` reaches git rather than any conclusion of safegit's -- and it does so by construction rather than by exemption, because `safegit rebase`'s own in-flight refusal is scoped to state that is NOT a rebase, and mid-rebase state is a rebase. Reaching git is not the same as being unguarded on the way: rebase's ordinary guards still run, and the dirty-tree one refuses `--continue` at exit **5** over the staged resolutions a conflicted rebase leaves behind, so the form that actually gets through is the one over a CLEAN tree -- an interactive rebase parked at `edit` or `break`. See "The guarded commands and their two coordination layers". (Over any in-flight state that is NOT a rebase -- a parked merge, cherry-pick or revert, or a mailbox application -- that same refusal fires, on `--continue` as on any other rebase command line: what is in flight there is not a rebase to continue. The enumeration follows the predicate rather than the other way round, so a kind added later is covered without this sentence being edited.) A `git am` is concluded with git's own commands.

**Two shapes safegit cannot start, it will not conclude either.** Both are states only RAW git can produce now, and for both the refusal names git's own `--continue` as the way to finish what git began:

- a QUEUED cherry-pick or revert -- a `.git/sequencer` directory, which `git cherry-pick a b` creates and `safegit cherry-pick` no longer can. Concluding one step of a queue natively is not possible: a conclusion removes the operation's whole state-file set, and for a queue that set includes the queue, so the remaining commands would be thrown away by the act of concluding the current one. safegit does not delegate to git instead -- it refuses, so that no commit made under a safegit command name is ever git's.
- a raw merge shape safegit's own merge cannot produce: a `MERGE_HEAD` carrying more than one line (an octopus), or a content-conflicted path with no `AUTO_MERGE` file (the signature of a non-default strategy -- on the supported git floor a normal merge always writes one). The overwrite check reads `AUTO_MERGE` as one of the sides it accepts, so a conflict computed by a strategy that recorded nothing there would be protected by nothing.

### Declaring the resolutions

Every path git left unmerged must be named exactly once, and nothing else may be named. The declaration is `--resolve 'path=<keyword>'` (repeatable) or a `--resolve-file`; the two may be combined, and a path named by both is a hard error (exit **2**) rather than an override.

The split inside a `--resolve` element is on its LAST `=`, so a path that itself contains one stays intact. Paths are repository-relative.

The four keywords are defined by INDEX STAGE, not by operation folklore:

| Keyword | What it commits | What it does to the file on disk |
|---------|-----------------|----------------------------------|
| `ours` | the stage-2 blob -- the content the branch being committed onto already had | overwritten with that content (git's own `checkout --ours`) |
| `theirs` | the stage-3 blob -- the operation's incoming side | overwritten with that content |
| `worktree` | the file's current content on disk | nothing; the file on disk was the source |
| `delete` | nothing -- the path is left out of the commit | the file is removed (git's own `rm`) |

What the incoming side *is* differs per operation, and each command says so in its own words at the moment the choice has to be made -- see the three sections below. A stage the conflict does not have is the side that DELETED the path, so resolving to it removes the path from the commit; the per-path listing says that instead of describing content that is not there.

A `--resolve-file` is TOML, the same conventions the scrub recipe uses:

```toml
[[resolutions]]
path = "src/a.go"
choice = "theirs"

[[resolutions]]
path = "src/b.go"
choice = "worktree"
```

### Completeness: exit 17

A conclusion must name every conflicted path and name nothing else. Both halves are hard errors listing the offending paths, and both exit **17**.

The omission half prints, per path, exactly what each keyword resolves to for THIS operation -- which is where the `theirs`-on-a-revert confusion is answered, at the decision point rather than in help text:

```
error: 1 conflicted path(s) have no resolution:
  f.txt
      --resolve 'f.txt=ours'      the content this branch already had, before the merge
      --resolve 'f.txt=theirs'    the content merged in from 5bc7ac7
      --resolve 'f.txt=worktree'  the file as it stands in your working tree right now
      --resolve 'f.txt=delete'    leave the path out of the commit and delete the file from disk
      (ours and theirs write the chosen content into the working tree too, as git's own checkout --ours does)
```

Nothing is committed and the operation is still in flight, so the same command re-run with the missing (or without the surplus) entries concludes it.

### Marker verification: exit 18

The content a conclusion is about to commit is checked for surviving conflict markers, so a conclusion cannot record the conflict it was asked to resolve. **There is no escape flag.**

The check covers every path the commit will RECORD: the conflicted ones, plus every other path whose index entry differs from the first parent. That second half is not padding -- an operator who resolved a conflict the way git's own documentation says to (edit the file, `git add` it) leaves a path with no stages, no `--resolve` entry, and the markers still in it, and only that listing reaches it. A path resolved to `delete` is excluded: it has no content in the commit.

The verdict is DIFFERENTIAL. A block that some side of this conflict -- or some base commit of this operation -- already carried is attributed to that side rather than reported, so a repository whose real content holds marker-shaped lines (documentation about conflicts, a stored fixture) stays committable. What is refused is a complete block that came into being with this conflict. Content named by `ours` or `theirs` passes by construction, because it IS one of the stage blobs the differential measures against.

```
error: 1 conflict marker block(s) survive in what this merge would commit:
  f.txt:1  the conflict git wrote here is still in the content being committed
  nothing was committed and the merge is still in progress. Edit the file(s) and re-run.
  Or take one side whole, which cannot carry a marker:
    safegit merge-continue --resolve 'f.txt=ours'   (or =theirs)
  or, if this path's real content contains marker-shaped lines, declare it in .gitattributes:
    f.txt -safegit-conflict-markers
    (read from the first parent's tree, so the declaration must be COMMITTED before the conflict)
```

The one way past a rejection is that `.gitattributes` declaration, and it is read from the FIRST PARENT's tree -- an uncommitted edit made while the operation is in flight exempts nothing, and the working tree's own `.gitattributes` may itself be conflicted at the moment the question is asked. Only git's explicit-unset spelling (`<path> -safegit-conflict-markers`) exempts; any other value, and any typo, leaves the path checked.

An exemption that fired is not silent: the check it declined to make is carried through the conclusion and reported, in the human output and in the payload's `declined_checks` member. A skipped check nobody can see is the same thing as no check at all.

### Overwrite protection: exit 27

`ours`, `theirs` and `delete` write the working tree (see the keyword table above), and the file they write over may hold an hour of hand-resolving that was never staged, never committed and never stashed. git's own `checkout --ours` and `rm` replace or remove it without a word, and the content is then in no object at all.

So a conclusion LOOKS at the file first. Per declared path whose materialization would destroy disk content, the accepted set is the conflict's three index stages, plus the blob git itself wrote into the working tree, read verbatim out of `AUTO_MERGE`. Verbatim rather than reconstructed: a rename-mediated conflict's marker labels carry the path, and no reconstruction recovers them. A file matching none of the accepted set is a hand edit, and the conclusion refuses at exit **27** with nothing committed and the operation still in flight, naming the file and the resolution that keeps the edit (`=worktree`).

The check is TOTAL over everything safegit concludes. Kinds with no `AUTO_MERGE` emission -- a delete-resolved path, an absent stage, a delete/modify, a binary file, a merge-driver path -- are stages-only by nature, and what git left on disk for those IS a stage blob. The shapes that have content conflicts with no emission at all never reach this loop: safegit's own merge cannot start them, and `merge-continue` refuses them from raw git. There is no skip arm.

`--discard-unmatched-worktree` elects the destruction and names each file it takes. It is the only way to say so, which is what makes it a consent flag rather than an escape hatch.

### The message

Omitted, the message is git's own draft for this operation (`MERGE_MSG`, which all three operations write) with its comment block stripped -- reproducing what git would have done at commit time, not editing the operator's text. `-m` replaces it, and repeating `-m` joins the values with a blank line between them. `--trailer` adds trailer lines. safegit's session trailer goes on after the `commit-msg` hook, so a rewriting hook cannot strip it.

A draft that is absent, or empty once its comments are stripped, with no `-m` to stand in, is refused with exit **2**.

### Shared flags

| Flag | Presence | Description |
|------|----------|-------------|
| `--resolve` | optional; omitted means every conflicted path is unresolved, which is a refusal listing them | Resolve one conflicted path, as `path=ours\|theirs\|worktree\|delete`. Repeatable, once per path |
| `--resolve-file` | optional; omitted means the `--resolve` flags are the whole declaration | Read resolutions from a TOML file of `[[resolutions]]` tables. Combinable with `--resolve` |
| `-m` | optional; omitted means git's draft with its comment block stripped | Commit message paragraph; repeatable, joined with a blank line between values |
| `--trailer` | optional | Add a key-value trailer line to the commit message (repeatable) |

### Shared refusals

| Situation | Exit | What is left behind |
|-----------|------|---------------------|
| An unknown `--resolve` keyword | 1 | Nothing was touched -- the parser refused the command line |
| A path resolved twice, or a `--resolve-file` that does not parse | 2 | Nothing was touched; the check runs before any lock |
| No message draft and no `-m` | 2 | The operation is still in flight |
| Nothing in flight, or an operation this command does not conclude | 5 | Whatever was there; the refusal names the command that does conclude it |
| A conflicted path unresolved, or a resolved path that is not conflicted | 17 | The operation is still in flight |
| A complete conflict block survives in what would be committed | 18 | The operation is still in flight |
| Materializing a declaration would write over a working-tree file that matches none of the conflict's own sides | 27 | The operation is still in flight; nothing was committed and nothing on disk was touched |
| A raw-git queue, or a raw merge shape safegit cannot start | 5 | Whatever git left; the refusal names git's own `--continue` and `--abort` |
| A detached HEAD | 1 | The operation is still in flight; the refusal prints the exact two commands that put HEAD on a branch |
| The commit was created and its aftercare did not finish | 26 | The COMMIT STANDS; the payload names the created SHA and lists what was left -- see "When the commit stands and the aftercare does not" |

The detached-HEAD refusal is a refusal rather than a special case because the commit pipeline is branch-shaped throughout: every commit is a compare-and-swap on a ref, and a detached HEAD has no ref to swap. The remedy it prints is deliberately NOT `git switch -c`, which git refuses outright while an operation is in flight:

```
    git branch <name>
    git symbolic-ref HEAD refs/heads/<name>
    safegit merge-continue
```

Those two plumbing calls move HEAD without touching the index or the working tree, so every state file, every conflict stage and every resolution already made survives.

### What a conclusion leaves behind

On success, in this order: the commit is created, the operation's whole state-file set is removed (so a later `safegit commit` is not refused), the shared index is put in step with the new tip, and the working tree is written to match the declared resolutions. The working tree goes last because it is the only step whose failure leaves nothing inconsistent behind.

A conclusion inside a submodule moves the parent's gitlink exactly as an ordinary commit does, so `commit.autoBumpParent` must have been decided in the parent before anything is written.

**`safegit undo` reverses the ref, not the operation.** Undoing a conclusion gives back the pre-conclusion tip, but git's operation state is gone -- `MERGE_HEAD`, the message draft and the conflict stages are not restored -- so the repository is idle rather than mid-merge. It says so on stderr, and `--quiet` does not suppress that.

### When the commit stands and the aftercare does not

Everything after the ref update -- removing the state files, syncing the index, writing the working tree, putting back a merge's autostash, bumping a parent repository's gitlink -- is aftercare, and a failure there cannot be undone by pretending the commit did not happen. Exit **26** is the family code for exactly that: **the operation's ref move is real, and its aftercare did not finish.**

It is not a silent partial success. The run emits its envelope, `exit_code` is 26, and the payload names the created SHA and carries a `residue` list of what was left. Every pipeline author shares the code -- `commit`, `--amend`, `--reword`, `mv`, `undo`, the restructured `merge`/`cherry-pick`/`revert`/`pull`, and the three conclusions -- so a caller reads one number for one meaning rather than a per-command vocabulary.

**A merge's autostash** is the aftercare step with its own member. When `MERGE_AUTOSTASH` is present, the conclusion asks whose stash it is before applying it, and two facts have to agree: the stash commit's FIRST PARENT is the tip this conclusion just committed onto, and its MESSAGE carries git's own autostash shape (`On <branch>: autostash`, which is what tells it from the `WIP on <branch>: ...` an ordinary `git stash` writes). A stash that fails either is neither applied nor deleted -- the file stays where it is, the commit it names is printed with the commands that reach it, and the payload's `autostash` member says which state it was in (`applied`, `stored`, `foreign`, `pending`, `none`). Applying someone else's stash would put uncommitted work into files this merge never touched and then remove the only name that work had left. `safegit doctor` reports the same file as an orphan when no merge is in flight, and `doctor --action fix` stores its commit as a stash entry before removing the file.

### A conclusion whose commit already stands

A conclusion moves the ref before it removes the state files, so a process killed between the two leaves both -- and a naive re-run would build a SECOND commit out of the same state.

safegit's re-run recognizes its own work instead. The op log's newest entry for this branch names an op that concludes this kind of operation and records the commit HEAD stands at, and the recognition is corroborated per kind: a merge by parentage (HEAD's parents being the branch tip plus every `MERGE_HEAD` line, which is exactly the commit this conclusion would build), a cherry-pick or revert by the SOURCE commit the entry recorded matching the parked state's own source. When they agree, nothing is committed: the index and working tree are derived from the standing COMMIT's own tree for the conflicted paths, the overwrite check above still runs, the state files go, the report names the commit that is already there, and the run exits on the aftercare's own terms. A declaration whose side's blob differs from what the standing commit holds is refused naming that commit, rather than silently ignored.

The window is not closed everywhere, and the limit is stated rather than glossed: the op-log entry is written just after the ref update, so a crash in that sliver leaves no entry -- a merge is still caught by parentage, a cherry-pick or revert in it is not. A crash AFTER the state files were removed is `safegit doctor`'s to report.

### `--dry-run`

A preview stages, writes the tree and builds the commit object exactly as the real run would, into a throwaway quarantine outside the repository, so the reported tree and file count are computed rather than guessed. No state file is removed, nothing is written into the working tree, and no ref moves:

```
would conclude the merge on main (tree dbedabbb): concluding a merge of 5bc7ac7c
 1 file(s) would be committed, 2 parent(s), 1 declared resolution(s)
 the merge state files would then be removed
 1 working-tree file(s) would be overwritten with the resolved content: [f.txt]
```

A conclusion whose state safegit refuses outright -- a queued cherry-pick or revert, an octopus merge, a conflict computed by a non-default strategy -- meets that refusal under `--dry-run` too, before anything is recorded.

## merge-continue

Conclude a merge git stopped before committing.

### When to Use

Use it whenever `safegit merge` (or a plain `git merge`) stopped on a conflict. It is the command the coordination guard names when a mid-merge repository refuses something else.

### What the commit is

- **Parents:** HEAD plus the single `MERGE_HEAD` line. An octopus -- a `MERGE_HEAD` carrying more than one -- is REFUSED rather than concluded: safegit's own merge cannot start one, and every check safegit makes over a merge is written against two sides. The refusal names git's own `merge --continue` and `merge --abort`.
- **Tree:** the merge's whole staged result, so a path the merge staged cleanly is never dropped.
- **Author:** no identity is preserved or pinned -- a merge has no source commit to take one from, and the payload carries no `author` member for the same reason.
- **`theirs` means:** the content merged in from the other side, named with the merge head's short SHA when there is exactly one.

**An empty merge needs no flag.** A merge commit records its parents whether or not the tree changed, so a conclusion whose resolutions leave the tree exactly as it was still commits. There is no `--allow-empty` here and none is needed.

### Examples

```bash
# See the conflicted paths and what each keyword would resolve them to
safegit merge-continue

# Take the incoming side of one path
safegit merge-continue --resolve 'src/a.go=theirs'

# Several paths, mixed
safegit merge-continue --resolve 'src/a.go=theirs' --resolve 'src/b.go=worktree' \
  --resolve 'old/gone.go=delete'

# Resolutions from a file, with a message of your own
safegit merge-continue --resolve-file resolutions.toml -m "merge side into main" \
  -m "Kept our config and their parser."

# Preview
safegit --dry-run merge-continue --resolve 'src/a.go=theirs'
```

## cherry-pick-continue

Conclude a cherry-pick git stopped before committing.

### What the commit is (a SINGLE cherry-pick)

- **Parents:** one -- the branch tip.
- **Author:** PRESERVED from the commit being applied; the committer is you. That is git's own division: a cherry-pick applies somebody else's change, so their authorship travels with it.
- **`theirs` means:** the result of applying the cherry-picked commit.
- **Move records:** none. A cherry-pick re-applies somebody's change, so a record on the source commit describes a move this commit is repeating rather than undoing, and whether the same move happened again is a fact about trees the operator is the one to state.

An empty result is refused (exit **1**): every conflicted path was resolved to content the branch already had, so the pick produces nothing. git refuses the same case for the same reason, and the message names the two ways out that exist (`git cherry-pick --skip`, `git cherry-pick --abort`).

### A QUEUED sequence is refused

`git cherry-pick <a> <b>` puts a QUEUE in git's sequencer, and it stops on the first commit that conflicts. `safegit cherry-pick` cannot create that state any more -- it applies one commit -- so a `.git/sequencer` directory means raw git started this, and `cherry-pick-continue` refuses it:

```
error: safegit cherry-pick-continue does not conclude a queued cherry-pick
  ...
    conclude it:  git cherry-pick --continue
    abandon it:   git cherry-pick --abort
```

Concluding one step of a queue natively is not possible: a conclusion removes the operation's whole state-file set, and for a queue that set includes the queue, so the remaining commands would be thrown away by the act of concluding the current one. The alternative -- staging the resolutions into a copy of the index and handing the rest to git -- is what safegit used to do, and it produced commits under a safegit command name that were git's: no trailers, no `commit-msg` handling, not undoable. That second authorship class is gone. safegit either writes the commit or refuses; it never signs off on git's.

### Examples

```bash
# Conclude a single conflicted pick
safegit cherry-pick-continue --resolve 'src/a.go=theirs'

# The picked commit deleted a file this branch modified: take the deletion
safegit cherry-pick-continue --resolve 'src/gone.go=theirs'
```

## revert-continue

Conclude a revert git stopped before committing.

### What the commit is (a SINGLE revert)

- **Parents:** one -- the branch tip.
- **Author:** the OPERATOR, not the author of the commit being reverted. That too is git's own revert semantics: undoing something is the reverter's own new change. The report says so explicitly rather than leaving it to be inferred.
- **Move records:** the INVERSE of every `Moved:` record the reverted commit declared, each under a fresh id. Retractions are deliberately not inverted -- a retraction says "that record was wrong", and reverting the commit that said so does not make the record right again.

**Read the stage keywords carefully here.** A revert applies an INVERSE patch, so:

| Keyword | On a revert, this is |
|---------|----------------------|
| `theirs` | the result of UNDOING the reverted commit -- what its PARENT held, not the reverted commit's own content. Resolving to `theirs` **keeps the revert** |
| `ours` | your branch's current content. Resolving to `ours` **keeps the commit being reverted** |

The per-path listing prints this at the moment of choice, naming the commit:

```
      --resolve 'f.txt=theirs'    the result of UNDOING 1b8e83f (the change)
```

An empty result is refused (exit **1**): the commit's effect is already absent from the tree.

### A QUEUED sequence is refused

A revert of more than one commit is git's sequencer, and `safegit revert` cannot create one. A `.git/sequencer` directory therefore means raw git started this, and `revert-continue` refuses it naming `git revert --continue` and `git revert --abort`, exactly as the cherry-pick conclusion does and for the same reason. See the cherry-pick section above.

### Relationship to `safegit revert`

A single `safegit revert` reaches this same engine through a different door: `git revert --no-commit` computes the inverse patch, and the conclusion engine commits it. So a clean revert and one that hit a conflict and was concluded here declare the same move records, run the same marker verification, and clean up the same state files.

### Examples

```bash
# See what each keyword would resolve the conflict to, for THIS revert
safegit revert-continue

# Keep the revert for this path
safegit revert-continue --resolve 'src/a.go=theirs'

# Keep the reverted commit's content for one path while reverting the rest
safegit revert-continue --resolve 'src/a.go=ours' --resolve 'src/b.go=theirs'
```

## undo

Reverse the last operation safegit's own commit pipeline authored, by reading the append-only operation log (oplog) and restoring the previous branch ref value. Supports undoing multiple operations in one invocation and is session-scoped by default to prevent one session from accidentally rolling back another session's work.

The undoable set is every operation that ENDED IN A COMMIT SAFEGIT WROTE: a `commit`, an `mv`, an `amend`, a `reword`, a `merge`, `pull`, `cherry-pick` or `revert` the pipeline authored, and the three conclusion commands (`merge-continue`, `cherry-pick-continue`, `revert-continue`). What is excluded is excluded by construction rather than by a list: a **fast-forward** is refused, because the tip it moved onto is a commit git created and undo never rolls a branch back over one, and so is a parked, up-to-date or failed operation, which authored nothing to reverse. A queued (multi-commit) `cherry-pick` or `revert` is git's own sequencer, so those commits are not safegit's to take back either. See "The oplog baseline" for the mechanism that tells the two apart.

### When to Use

Use `safegit undo` when you need to reverse a recent commit, move, amend, reword, merge, pull, pick, revert or conclusion. It reads the oplog to find the correct rollback target, so it works even when multiple sessions have committed to the same branch.

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
- **Range validation: a commit safegit did not create is never rolled over**: before anything moves, undo walks the commits the branch would LOSE -- the first-parent walk from the rollback target to where the ref actually stands -- and every one of them must be an operation this undo is reversing, according to the oplog. A plain `git commit`, a commit a rebase replayed, or anything else safegit did not author sitting in that range is a hard error (exit 1) naming the offending commit, not a commit quietly dropped out of history. The CAS above cannot answer this on its own: it pins the newest recorded tip and therefore sees only a foreign commit sitting on top of it. The refusal reaches `--dry-run` too, so a preview never announces a rollback the real run would refuse, and where the foreign commit turns out to be another safegit session's the message names `--bypass-session`.
- **History rewrite barrier**: If a `scrub` or `rewrite-author` operation is found in the oplog while scanning for undoable operations, the undo is blocked with an error. History rewrites invalidate all prior SHAs, making earlier oplog entries unsafe to undo.
- **Root commit undo**: Undoing the root commit (the first commit in the repo) deletes the branch ref entirely, leaving the branch in an unborn state. That state is supported rather than a dead end -- see "Unborn branches" for what works there and what is refused.
- **Undo moves a ref; it never moves the working tree**: undoing a `safegit mv` reverses the COMMIT and leaves the files at their new paths, and says so on stderr. Undoing a merge, pull, cherry-pick or revert -- the pipeline-authored form or a conclusion (`merge-continue`, `cherry-pick-continue`, `revert-continue`) -- gives back the pre-operation tip but does NOT restore git's operation state -- `MERGE_HEAD`, the message draft and the conflict stages are gone -- so the repository is idle rather than mid-merge, and the notice says to re-run the operation to get back to a state the conclusion command can conclude. Both notices print whatever `--quiet` says: they are facts about what the undo did not do.
- **Oplog recording**: The undo itself is logged to the oplog, enabling redo-like workflows and audit trails.

## push

Push refs to a remote with pre-pre-push hooks that run before any network I/O, automatic retry with exponential backoff on transient transport errors, and oplog recording of every push operation for audit purposes.

### When to Use

Use `safegit push` instead of `git push` to benefit from pre-pre-push hooks (custom checks that run before git's built-in pre-push hook), automatic retries for transient network failures with exponential backoff, submodule hook cascading from parent repositories, and full oplog recording of every push attempt and result.

### Flags

| Flag | Presence | Description |
|------|----------|-------------|
| `--pre-push-hook` / `--no-pre-push-hook` | optional; omitted means the hooks run | Run pre-pre-push hook scripts before pushing. Under `--dry-run` they are never run whatever this says -- a hook is an arbitrary script, so running one is a mutation a preview may not perform -- and the preview says so on stderr and in the payload's `pre_pre_push_hooks_skipped` member. Only EXECUTION is skipped, though: they are still DISCOVERED, with the exit 24 and 25 refusals that discovery can produce, and `--no-pre-push-hook` skips discovery along with execution |
| `--force-with-lease` / `--no-force-with-lease` | optional; omitted means an ordinary push | Force push, pinning each ref to the SHA safegit just observed on the remote. Forcing is consequential: it is confirmed at the terminal, and `--approve-consequential` answers it in advance |

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

# Force push with lease (safe force push); confirmed at the terminal
safegit push --refs head --force-with-lease

# The same, from a script or an agent, consenting in advance
safegit push --refs tags --force-with-lease --approve-consequential

# Push without running pre-pre-push hooks
safegit push --refs head --no-pre-push-hook

# Push all branches and tags
safegit push --refs both
```

### Safety Guarantees

- **Pre-pre-push hooks**: Hooks in the live store under the repository's common `.git/safegit/hooks/`, and hooks the checkout provides in `.safegit/hooks/`, run before any network I/O. A failing hook aborts the push (exit code 20). A timed-out hook aborts with exit code 21. A hook without its execute bit is a refusal (exit code 25) in BOTH stores: a hook is disabled by removing it, never by dropping its mode, and a lost mode bit -- a filesystem without modes, a patch tool that dropped it -- would otherwise turn into checks that quietly stopped running. Hooks still sitting in the pre-migration location, `.git/hooks/pre-pre-push` and `.git/hooks/pre-pre-push.d/`, do **not** run at all: every push and every `hook run` refuses with exit code 24 until `safegit hook migrate` moves them.
- **Where hooks run from, and who can put one there**: the live store is keyed on the **common** git dir, so a hook installed from a linked worktree is the hook every worktree of the repository runs -- the same anchor the ref locks use. The checkout-provided store is per-worktree, because it is checkout content: a file is in it because it sits in `.safegit/hooks/`, whether or not git tracks it, so an uncommitted script there runs on the next push. That means **cloning a repository and pushing from that checkout runs the repository's committed scripts**. Execution happens only on `safegit push` and `safegit hook run` -- an operator action with push intent -- never on clone, fetch, checkout or any inspection command, and `safegit hook list` names every location with its origin so the set can be read before anything is pushed.
- **Submodule hook cascading**: When pushing from inside a submodule, hooks from the parent repo are discovered and run first, then the submodule's own hooks.
- **Automatic retry**: Transport errors (connection refused, DNS failure, TLS errors, broken pipe) are classified from git's own stderr and trigger automatic retries with exponential backoff (1s, 2s, 4s). Every retry re-reads the remote and re-pins the leases, so an expectation is never carried over from a failed attempt. Non-transport errors (non-fast-forward, permission denied, a stale lease) are not retried. Default: 3 attempts, configurable via `push.retryAttempts`.
- **Pinned per-ref leases**: `--force-with-lease` sends one expectation per ref, `--force-with-lease=<remoteRef>:<sha>`, pinned to the SHA safegit itself observed on the remote -- or the empty expectation ("this ref must not exist yet") for a ref the remote does not have. A bare `--force-with-lease` would compare against the remote-tracking ref instead, which tags do not have at all: git zeroes the expectation there and refuses to move any tag the remote already carries, which made pushing rewritten tags impossible.
- **Atomic multi-ref pushes**: a push of more than one ref is `--atomic`. One refused ref leaves the remote exactly as it was, never half-published.
- **Terminal lease rejection**: when the remote moved between safegit reading it and the push reaching it, git refuses and safegit exits 41 without retrying. Retrying would re-read the other session's ref, pin the lease to it, and perform exactly the overwrite the lease prevented. Fetch, look at what arrived, and decide again.
- **Consent for forcing**: an ordinary push prompts for nothing. `--force-with-lease` overwrites remote refs, so it is confirmed at the terminal before any network contact; the prompt goes to stderr and `--quiet` does not suppress it. `--approve-consequential` answers the confirmation in advance, `--json` answers nothing and refuses, and a declined confirmation exits nonzero.
- **A dry run still reads the remote**: `--dry-run` records the push instead of performing it, but it resolves the refs first, which means an `ls-remote`. The preview would otherwise be unable to say which refs it would publish or what each lease would pin to. Hook EXECUTION is the part a preview never runs.
- **A dry run still discovers the hooks**: discovery and execution are separate, and only execution is skipped under `--dry-run`. Finding which scripts a push would run reads the filesystem and mutates nothing, and its two refusals -- exit **24** for hooks still in the pre-migration `.git/hooks` location, exit **25** for a discovered hook without its execute bit, in either store -- are verdicts about the checkout that hold whether or not anything is pushed. A preview that skipped discovery reported success for a push that could only ever exit 24 or 25, which is the one thing a preview may never do. `--no-pre-push-hook` skips discovery along with execution, since there is then nothing to discover for.
- **Order of operations**: the force-push confirmation comes first, before any network contact, so a declined force reaches nothing. Then the refs are resolved -- which reads the remote with `ls-remote` -- and only then do the pre-pre-push hooks run, on the ref set that read produced. The hooks' input and the push set are decided once: a retry that finds a LOCAL ref has moved since refuses (exit **40**) rather than publishing on the strength of a hook run that never saw it.
- **Oplog recording**: one `push` entry is appended after the push succeeds, recording the remote, the pushed refs and how many hooks ran. Failed attempts and retries are not separate entries, and a hook timeout writes none.
- **git's push output arrives at the end**: safegit captures git's stdout and stderr rather than streaming them, because classifying a transport error from a verdict needs git's stderr as data. Progress therefore appears when the attempt finishes instead of live. Under `--json` git's stdout is re-routed to stderr, so the envelope stays the only document on stdout.

### Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | A declined force-push confirmation, or a local ref or remote URL that would not resolve |
| 20 | Pre-pre-push hook failed |
| 21 | Pre-pre-push hook timed out |
| 24 | Hooks are still in the pre-migration `.git/hooks` location; run `safegit hook migrate` |
| 25 | A discovered hook is not executable, in either store |
| 40 | The push did not get through: `git push` failed after the retry policy was exhausted, the remote could not be observed, or a local ref moved since the hooks saw it |
| 41 | A `--force-with-lease` expectation no longer matched: the remote ref moved after safegit observed it |

Codes 24 and 25 fire during hook discovery, before any network contact -- `hook run` produces both for the same reason, and a `--dry-run` push produces them too, because discovery runs in a preview.

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

### The merge step is `safegit merge`'s

A pull is a fetch followed by a merge, and the merge is the one described under `merge` above: git computes it with `--no-ff --no-commit` and safegit's own pipeline writes the commit, so a pull that merges produces a pipeline-authored commit -- trailers, the `commit-msg` hook, undoable. The strategy selector maps straight onto it:

| `--merge-strategy` | What it does |
|---|---|
| `ff` | fast-forward where the branches allow it, otherwise a pipeline-authored merge commit |
| `ff-only` | refuse anything but a fast-forward, with safegit's own refusal rather than git's |
| `no-ff` | always a merge commit, even where a fast-forward was available |

A fast-forward moves the ref by compare-and-swap and then puts the index and working tree in step with it; `safegit undo` refuses it, because the new tip is a commit safegit did not create.

The `FETCH_HEAD` octopus check is inherited whole: when the fetch marks more than one branch for merging, the pull is refused rather than producing a many-parent commit or silently taking the first line.

`--rebase` is refused, and the refusal says what to do instead. A pull's merge step is safegit's own -- it authors the commit -- while a rebase is git's replay from end to end, which is a different operation with its own door: `git fetch <remote>` then `safegit rebase <remote>/<branch>`.

### Safety Guarantees

- **Coordination guard, both layers**: the worktree operation lock (a second safegit process in this worktree waits, then exits **8** naming the holder) and then the dirty-tree check (exit **5**). See "The guarded commands and their two coordination layers".
- **Explicit merge strategy**: No implicit default merge behavior -- you must choose `ff`, `ff-only`, or `no-ff`. A pull never depends on the repository's configuration to decide whether it may create a merge commit.
- **Two-phase**: `git fetch`, then the merge, as separate steps.
- **The commit is safegit's**: except after a fast-forward, where there is no new commit at all.
- **git's own exit code where git decided**: when the fetch fails, safegit exits with the code git returned; the merge step's refusals are safegit's own.
- **Oplog recording**: exactly ONE entry under the op name `pull`, carrying the branch baseline (see "The oplog baseline" above). A pull that COMMITTED is the pipeline's own entry -- `ref`, `parent`, `sha`, `tree`, `attempts`, and no `outcome`, which is what makes it undoable. Every other ending is the recorder's, which names the remote and the branch alongside the baseline: a pull whose fetch OR whose merge failed records an empty new tip and a failed outcome, and a fast-forward records the fast-forward outcome.
- **The payload nests the merge's**: a fetch summary plus the merge payload, so the outcome member says which of the four shapes the merge step took.

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
- **A `backup backup` dry run never touches the network**: that command's `--dry-run` builds its preview from local state alone -- no `ls-remote`, no `fetch`, no prompt -- so previewing against an unreachable remote succeeds. The slot's current SHA, the ancestry check against it, and the lease pinned to it are all resolved when the backup actually runs.
- **A `backup restore` dry run DOES read the remote**, and the difference is stated rather than glossed: the preview has to know which SHA the slot holds in order to say what it would fast-forward to, and that lookup is an `ls-remote`. So `backup restore --dry-run` contacts the remote. It performs neither the fetch nor the merge -- both are recorded instead -- but a preview against an unreachable remote fails where `backup backup`'s succeeds. Whether a network READ belongs on a preview path at all awaits a ruling in the CLI framework's effects model; until then this is the honest description of what happens.
- **Hooks bypassed on purpose**: backup pushes run with `--no-verify`. `refs/backups` is a tool-owned namespace, and pre-push policies exist to police branches and tags.
- **Restore never discards work**: the restore is `merge --ff-only`, so a branch carrying commits the backup lacks is refused with the range to inspect.
- **Coordination guard on restore**: a restore checks the working tree first and refuses (exit **5**) when it is dirty, naming what is uncommitted -- or, when git has an operation in flight, naming that operation and the command that ends it. It does not take the worktree operation lock; the `--ff-only` merge is git's own.
- **Oplog recording**: every backup and restore is logged with the remote, slot ref, previous SHA, and new SHA.

### Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error, including a declined public-remote confirmation and a detached HEAD (slots are per branch) -- a refusal never reports success |
| 5 | `backup restore` refused: the working tree is dirty, or git has an operation in flight |
| 22 | The remote slot holds work missing from the local history |
| 23 | The branch has no backup slot on the remote |
| 40 | The push did not get through |
| 41 | Another machine wrote this branch's slot between safegit observing it and the push reaching it, so the `--force-with-lease` expectation no longer matched |

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
- **Non-object matches say which coordinate system their path is in.** A match found inside git's OWN state -- `.git/config`, `COMMIT_EDITMSG`, the hook directories, safegit's own `.git/safegit` (everything there except the rewrite journal) -- carries `in_git_dir: true` in the payload and a path relative to the git directory. Every other path is repository-relative. The one exception is a location git resolves OUTSIDE the git directory, which today means a redirected `core.hooksPath`: still git's state, so the marker is still true, but the path stays absolute, because rendered relative it would name a file that is not there.
- **Binary skip**: Binary blobs (containing NUL bytes in the first 8 KB) are automatically skipped.
- **Trailer separation**: When `--target` includes `trailers`, commit matches are split into body-only and trailer-only subsets.

## scrub file

Replace or remove a specific file across every commit in a selected range of history, rewriting each affected commit tree to either substitute the file contents with those of a sanitized file or delete the file entirely from every snapshot in that range. The range is a required choice -- `--from <commit>` or `--entire-history` -- so "all commits" is one of the two answers, never the default.

### When to Use

Use `safegit scrub file` when you need to remove a leaked secret file (like `.env` or a key file) from every historical commit, or replace it with a sanitized version.

### Flags

| Flag | Presence | Description |
|------|----------|-------------|
| `--delete` / `--replace-with <path>` | required, exactly one | What happens to the file at every commit in range: delete it, or replace its contents with those of `<path>` |
| `--from <commit>` / `--entire-history` | required, exactly one | How much of the history is rewritten |
| `--reason` | required | Mandatory audit trail message explaining why this scrub is needed |
| `--remap-shas-in` | optional | Glob selecting files whose 40-character commit hashes are remapped during rewrite (repeatable) |

The mode is a declaration, not an inference. It used to be inferred by checking whether the target existed on disk, which made the same command line mean opposite things depending on the directory it was typed in, and turned a typo in the path into a silent deletion from history. Naming `--delete` or `--replace-with` is now mandatory, and a command line with neither is refused before anything runs.

**The two paths resolve differently, deliberately.** The `file` argument is repository-relative -- it names a path inside the commits being rewritten. The `--replace-with` path is yours: it resolves against the directory you are standing in, like any other filename you type at a shell prompt.

### Arguments

| Name | Required | Description |
|------|----------|-------------|
| `file` | Yes | Repository-relative path to the file to scrub from history |

### Examples

```bash
# Delete a file from every commit since abc1234
safegit scrub file --delete --from abc1234 --reason "leaked API key" -- secrets.env

# Replace a file's contents everywhere with a sanitized copy
safegit scrub file --replace-with ./clean-database.yml --from abc1234 \
  --reason "sanitize credentials" -- config/database.yml

# Rewrite the whole history rather than a range
safegit scrub file --delete --entire-history --reason "key committed at the root" -- id_rsa

# Dry run to preview without making changes
safegit --dry-run scrub file --delete --from abc1234 --reason "test" -- secrets.env

# With SHA remapping for changelog files
safegit scrub file --delete --from abc1234 --reason "leaked key" --remap-shas-in "*.jsonl" -- .env
```

### Safety Guarantees

- **Clean tree required**: Refuses to run if the working tree has uncommitted changes.
- **Rewrite lock**: Acquires a repository-wide rewrite lock to prevent concurrent scrub operations.
- **Deliberate confirmation**: The framework's consequential gate takes consent before dispatch: on a terminal it prompts, and without one it refuses and names `--approve-consequential`. `--json` never answers it. safegit adds no second prompt behind the gate -- it prints the commit count and scope as a notice, so what the rewrite covers is stated rather than asked twice.
- **Verification before anything moves**: The rewritten commits are built as unreachable objects and checked BEFORE a single ref moves -- commit messages, author/committer identity and parent topology preserved, only the declared paths changed, the target holding what the mode promised in every rewritten commit, and the working tree still free of anybody else's work. A failure here exits `30` and changes nothing at all: no ref, no tag, no rewrite-journal record.
- **Verification after the rewrite stands**: Old (pre-scrub) blob objects gone from the object store, no ref left pointing at a pre-rewrite commit. These can only be asked after cleanup, so a finding cannot undo anything: the command exits `31` naming what survived, with the rewrite in place.
- **Never overwrites concurrent work**: If another session stages or writes something while the refs are moving, the working-tree sync is SKIPPED rather than performed, the command says so and prints the one command that completes it (`git read-tree --reset -u HEAD`), and it exits `31`.
- **Post-rewrite cleanup**: Expires tainted reflog entries, repacks objects, and prunes unreachable objects.
- **Rewrite maps**: Persists crash-safe rewrite maps to `.git/safegit/rewrite-maps.jsonl`.
- **Submodule support**: Automatically detects if the target file is inside a submodule and rewrites both the submodule's history and the parent's gitlinks.
- **`--delete` removes the move records naming the path; `--replace-with` edits no message.** A record is a claim about a path, and it lives in a commit message, which is the one place a tree rewrite does not reach. When the path is being ERASED, every `Moved:` record naming it -- as its old side, as its new side, or inside a subtree prefix covering it -- is removed in the same rewrite, whole (half a move is not a smaller move, it is a malformed one), on the top-level walk and inside a submodule alike. When the path is being REPLACED it still exists, so a record saying content moved there is exactly as true afterwards and nothing is edited. A retraction naming a removed record's id is left where it is; the projection already treats it as inert.
- **The scope is stated on completion**: a successful rewrite prints which ref's history it followed and says that other refs were not rewritten. "Scrub complete" reads as "the content is gone from this repository" and it is not: a branch, a tag or a stale remote-tracking ref reaching commits the walk never visited still holds every one of them.

## scrub match

Replace every occurrence of a regex pattern in the blobs, commit messages and tag annotations of a selected range of history, rewriting commit trees so that sensitive values like secrets and credentials are permanently removed from the snapshots in that range. Like `scrub file`, the range is a required choice -- `--from <sha>` or `--entire-history`.

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
- **Rotation notice**: The completion output states that rewriting history cannot un-leak a secret that was ever pushed, tells you to rotate the credential, and prints the `scrub verify --pattern` command that re-checks this repository later.
- **No pattern retention**: Neither the pattern nor the replacement text is written anywhere under `.git/safegit`. The oplog entry keeps metadata only (operation, reason, scope, mode, counts).
- **Submodule support**: Scans and rewrites submodule histories, then updates parent gitlinks. A rewrite that touched a submodule names that history in the scope line too.
- **Move records are rewritten as records, not as text**: a `Moved:` trailer holds two C-quoted paths, so a substitution inside one is applied to the DECODED paths and the pair is re-encoded through the one encoder. The output therefore always parses, and the quoting stays correct whatever the replacement contained. Two consequences follow. A pattern written to match the ESCAPED spelling matches nothing, so it changes nothing. And a substitution whose result is no longer a move -- both paths equal, one of them empty, one side a subtree marker and the other not -- is a **hard refusal, exit 30, before any ref moves**: the commit, the record and the invalid result are named, and the record is neither written broken nor silently dropped. The refusal suggests the three ways out: a replacement that leaves the pair a move, `scrub file --delete <path>` (which removes the records naming it), or retracting the record in a commit of its own first.

## scrub run

Execute a multi-operation scrub recipe from a TOML file, applying all of its pattern replacements across the selected range of history in a single coordinated pass with topological ordering, overlap detection, and automatic post-scrub verification.

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
safegit scrub run --diff --reason "quarterly secret rotation" --entire-history -- recipe.toml

# Dry run (match count summary without diffs)
safegit --dry-run scrub run --reason "test" --entire-history -- recipe.toml

# Limit diff preview to 20 blobs
safegit scrub run --diff --limit 20 --reason "quarterly secret rotation" --entire-history -- recipe.toml
```

`--reason` is required on every `scrub run`, including a `--diff` preview: the audit
trail is a property of the invocation, not of whether it wrote anything. `--diff` and
`--dry-run` are mutually exclusive (exit **2**) -- they are two different previews and
asking for both says nothing about which one you want.

### Safety Guarantees

- All guarantees from `scrub match` apply.
- **Topological ordering**: Operations are sorted by dependency graph (Kahn's algorithm). Independent operations are applied simultaneously against the original content; dependent operations match against post-dependency content.
- **Overlap detection**: Overlapping byte ranges across independent operations are a hard error.
- **Cycle detection**: Circular dependencies in the recipe's `depends_on` graph are detected and rejected at parse time.
- **Rotation notice and scope line**: The completion output prints the same rotation warning and the same scope line `scrub match` does, with `safegit scrub verify <recipe>` as the re-check command — the recipe file is the durable record of what was scrubbed.
- **Move records**: the same record-aware rewriting `scrub match` performs, including the exit-**30** refusal for a substitution that would turn a move record into a line nothing can read.

## scrub verify

Confirm that the patterns you name are absent from every blob, commit message, and tag annotation in the git object store.

Verification is **stateless**: safegit records nothing about past scrubs and reads no policy file, so an invocation checks exactly what its command line says. The two ways to say it are a repeatable `--pattern` and a scrub recipe file; at least one is required, and both may be given at once.

### When to Use

Run `safegit scrub verify` periodically or in CI to confirm that scrubbed secrets have not been reintroduced. Keep the patterns wherever you keep the rest of your CI configuration — a checked-in scrub recipe is the natural home, and the same file drives `scrub run`.

### Flags and Arguments

| Name | Description |
|------|-------------|
| `--pattern` | Regular expression that must be absent from every object. Repeatable. |
| `--scope` | Glob limiting which blob paths a `--pattern` match counts against (e.g. `*.env`, `config/**`). Requires `--pattern`; recipe operations carry their own scope in the recipe file. |
| `recipe` (positional) | Path to a scrub recipe TOML file. The format is the one `scrub run` takes, read unchanged; its `replace`, `mangle` and `depends_on` fields are ignored here because verification substitutes nothing. |

Use `--json` for machine-readable output that can be parsed by CI pipelines, and `--quiet` to suppress informational messages while still reporting verification failures.

### Examples

```bash
# Verify one pattern
safegit scrub verify --pattern 'AKIA[0-9A-Z]{16}'

# Verify several, one of them only inside a path scope
safegit scrub verify --pattern 'sk_live_[a-z0-9]+' --pattern 'AKIA[0-9A-Z]{16}'

# Verify every operation of a checked-in recipe
safegit scrub verify .safegit/scrub-recipe.toml

# JSON output
safegit --json scrub verify --pattern 'sk_live_[a-z0-9]+'
```

### Exit Codes

`0` when every pattern is absent, `1` when any pattern is still present. Naming no pattern at all is a usage refusal, not a pass.

### Safety Guarantees

- **Read-only**: Does not modify any objects.
- **Full scan**: Scans every object in the store — including unreachable ones — for each pattern.
- **Scope-aware**: A scoped pattern only flags blob matches at paths within its scope; commit messages and tag annotations carry no path and are always checked.
- **Batched scan**: Uses multi-pattern scanning to avoid iterating the object store once per pattern.
- **Nothing to go stale**: With no stored policies there is no local state that can silently disagree with what a fresh clone would check.

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
| `uninstall` | Remove safegit's state from this repository -- every worktree's, not only the one you are standing in |

### Health Checks

This table is generated from the check registry in `doctor.go` by `scripts/gen-doctor-table`; edit the registry, not the table.

**A registered severity is a FLOOR, not the verdict.** The severity below is the one a failing finding carries ORDINARILY, and a check may report an individual finding at a graver status when the reason for failing is graver than its ordinary one. Two warn-severity checks do: `config` reports an UNREADABLE config file at error severity (every command reads it, so it is not advisory), and `bypass_detect` reports at error severity the two cases where it cannot make its comparison at all -- an oplog it cannot read for the current ref, and a ref the oplog records a tip for that no longer resolves, which is itself the bypass signal. An escalated finding is an error-severity finding in every respect, the exit code included. A script that reads this table alone and concludes those two checks can never fail a run is therefore wrong; read the reported status, not the registered one.

<!-- BEGIN generated doctor health-check table (scripts/gen-doctor-table) -->

| Check | Severity | Question |
|-------|----------|----------|
| `initialized` | error | Is safegit initialized in this repository? |
| `tmp_dirs` | warn | Are there orphan temporary index directories? |
| `stale_locks` | warn | Are there lock files whose holder is gone -- and orphaned lock-publication temporaries? |
| `config` | warn | Is the config file readable with a valid schema version? |
| `oplog` | error | Does the oplog read completely, with no unparseable lines? |
| `bypass_detect` | warn | Does the branch tip match the last oplog entry? (Detects a raw `git commit` bypassing safegit.) |
| `filesystem` | warn | Is the repository on a network filesystem (NFS/SMB) that may not support atomic operations? |
| `submodules` | error | Can safegit enumerate this repository's submodules? (`--action fix` cleans each submodule's state with that enumeration and a scrub decides what it rewrites with it, so a failure is answered by a repair, never by a quietly smaller scope.) |
| `hook_perms` | error | Are all hook scripts executable, in both stores? (Every push refuses while one is not.) |
| `hooks_migrated` | error | Are safegit's pre-pre-push hooks out of the pre-migration `.git/hooks` location? (While they are not, every push refuses with exit 24.) |
| `native_hooks` | warn | Are there git hooks in `.git/hooks` that safegit's own commit path does not run? |
| `git_version` | warn | Is the installed git new enough for the features safegit uses? |
| `merge_autostash` | warn | Is `MERGE_AUTOSTASH` present with no merge in flight? (It names a stash-shaped commit holding uncommitted work no ref reaches; the check itself removes nothing, and `--action fix` stores that commit as a stash entry before removing the file, so the work gains a name instead of losing its only one.) |
| `unmerged_index` | error | Does the index carry unmerged entries with no merge, cherry-pick or revert in flight to resolve them? (git refuses every commit in that state and so does safegit, at exit 28; `--action fix` stages each path's own working-tree content. An unmerged index the operation in flight owns is reported as such and is not a fault.) |
| `legacy_scrub_policies` | error | Is the pre-0.2 scrub-policy file -- which stored scrubbed patterns in plaintext inside the repository -- gone? |

<!-- END generated doctor health-check table -->

**Exit code 50.** `diagnose` exits `50` when at least one finding is REPORTED at **error** status; findings reported as warnings alone exit `0`. Reported, not registered: a finding a warn-severity check escalated to error (see above) counts here exactly like one from a check registered at error. After `--action fix` the code reflects what the fix LEFT: an error-status finding that is still there keeps the exit nonzero.

### Uninstall is repository-wide

`--action uninstall` removes safegit's state for the whole repository: the invoking worktree's `.git/safegit`, the shared store (locks and the live hook store), and **every other worktree's state directory too**, including that of a worktree that was deleted without being pruned. Run from a linked worktree it reaches the main worktree's state.

Because that is the part an operator has no reason to expect, the command enumerates every path it is about to remove, one per line, marking the ones outside the worktree you are in -- and it does so BEFORE asking, since you cannot consent to what you have not been shown. The enumeration is a statement of what the command does, so `--quiet` does not hide it; machine mode carries the same set in the envelope. The confirmation is answered by `--approve-consequential`, declining exits nonzero, and `--dry-run` prints the same enumeration and removes nothing.

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

- Removes orphan temporary index directories (identified by the dead PID in the directory name)
- Removes the legacy queue directory (from safegit v0.1)
- Removes the legacy scrub-policy file, whose content is exactly what should not be sitting on disk
- Reclaims lock files whose holder is genuinely gone, and removes orphaned lock-publication temporaries a kill left behind
- Cleans up submodule safegit directories the same way
- Stores an orphaned `MERGE_AUTOSTASH` as a stash entry and then removes the file. The order is the safety: until the commit is on `refs/stash` the only name it has is the object name inside the file, so a removal-first repair would hand the operator's uncommitted work to the next `git gc`. A failure to store leaves the file exactly where it is
- Re-stages an orphaned unmerged index -- conflict stages with no merge, cherry-pick or revert in flight to resolve them, which git and safegit both refuse every commit over. Each path's own working-tree content is staged at stage 0, and a path the working tree does not hold at all is dropped from the index, which is what resolving that conflict by deleting the file looks like. An unmerged index the operation IN FLIGHT owns is left alone: it is that operation's to resolve

It does not rotate or truncate the oplog. The oplog is an append-only audit trail and is complete by design; there is no size limit and nothing prunes it silently.

## unlock

Release one of **safegit's own** lock files -- a per-ref lock, the worktree operation lock, or the repository-wide rewrite lock -- after verifying that the lock's owning process is genuinely gone.

It has nothing to do with git's `.git/index.lock` or any other lock git takes for itself: those are git's to clean up, and safegit never writes into that namespace.

### When to Use

Use `safegit unlock` when a safegit process was killed while holding a lock and the lock file is still there, blocking new operations. Ordinarily nothing needs it: a lock whose holder is gone is reclaimed automatically by the next contender, and `safegit doctor --action fix` sweeps them unattended. This command is the last-resort path for the case where that reclamation cannot work -- a filesystem where `flock(2)` does not, on which contenders time out instead of reclaiming.

The staleness check still stands in front of it: a lock a live process holds is refused, naming the holder's PID. What the weaker stance gives up is only the narrow window in which another process reclaims the same stale lock between the check and the removal, which is why `doctor` -- which sweeps by the hundred and unattended -- takes the strict, identity-checked path instead.

### Arguments

| Name | Required | Description |
|------|----------|-------------|
| `ref` | Yes | Which lock to release: a branch name (`main`), a full ref (`refs/tags/v1`), or a tool-owned lock name (`safegit/rewrite`, `safegit/operation`) |

The naming grammar has three arms. A name starting with `safegit/` is one of the tool-owned locks, which are not refs: `safegit/rewrite` is the repository-wide history-rewrite lock (shared by every worktree), `safegit/operation` is this worktree's operation lock. A name starting with `refs/` is a full ref. Anything else is a branch shorthand. An unknown `safegit/` name is refused with the known ones listed.

### Examples

```bash
# Release a stale lock on the main branch
safegit unlock main

# Release using full ref name
safegit unlock refs/heads/feature-branch

# Release the rewrite lock a crashed scrub left behind
safegit unlock safegit/rewrite

# Release this worktree's operation lock
safegit unlock safegit/operation

# Dry run
safegit --dry-run unlock main
```

### Safety Guarantees

- **Liveness check**: Refuses to release a lock a live process holds. If the owning process is still running, the unlock fails with an error naming its PID and suggesting you kill it or wait.
- **Identity, not just the PID**: the lock file records the holder's PID, hostname and process start identity. A PID the kernel has recycled fails the start-identity comparison and the lock is judged stale; a lock taken on a different machine (a differing `host=`) is never judged stale at all, because a PID from another machine's namespace means nothing here. Where either side of a comparison is unavailable the check fails closed and the lock is left alone.

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

## The guarded commands and their two coordination layers

`switch`, `pull`, `merge`, `rebase`, `reset`, `bisect`, `cherry-pick` and `revert` all run the same two checks before anything happens, in this order, and some of them add a third of their own on top. These two are what the per-command sections below mean where they say "coordination guard".

1. **The worktree operation lock** answers "is anyone else already working here?" for the WHOLE operation. It is taken first and held for the full duration of the git command, an interactive `rebase -i`'s editor session included. A second safegit process in the same worktree waits `lock.acquireTimeoutSeconds` (default 30) and then exits **8**, naming the holder and printing the `safegit unlock safegit/operation` recovery command; it never runs concurrently. The lock is worktree-local, so two worktrees of one repository proceed independently. A `--dry-run` takes no lock -- a command that promises to change nothing must not write a file into `.git/safegit`.
2. **The dirty-tree check** then answers "is it safe to start?" at that instant: any tracked modification (`git diff HEAD`, never the possibly-stale shared index) or any untracked file refuses the command with exit **5**, listing what is uncommitted. On an unborn branch the comparison is against the EMPTY TREE instead, because there is no `HEAD` to resolve and a repository with no commits holds exactly that -- see "Unborn branches". When git has an operation in flight, the same dirt is that operation's conflict markers and staged result, so the refusal names the operation and the command that ends it instead of advising a commit nobody can make.

The second check is only worth anything because of the first: without the lock, another process could put the repository mid-merge in the window between the check and the ref update.

Neither of those two is about the in-flight state as such: the second is over the working TREE, so a state-control form invoked against a clean one gets past both -- an interactive rebase parked at `edit` or `break`, where `safegit rebase --continue` is the way on, is the shape that reaches git. What refuses over an in-flight operation is the THIRD check, and only the commands that own one make it.

The commands that COMPUTE an operation make it. `merge`, `cherry-pick`, `revert` and `pull` -- in every form that asks git to work a result out, which includes the forwarded `-n`/`--no-commit` forms of `cherry-pick` and `revert`, and never `--abort` or `--quit` -- refuse at exit **5** when git reports ANY operation in flight, whatever the working tree looks like, naming what is in flight and the way out of it. `pull` asks it before its fetch, so a pull that cannot merge never reaches the network.

`rebase` makes a third check too, and its predicate is NARROWER: it refuses an in-flight state that is not itself a REBASE. A rebase computes nothing of safegit's -- git replays and authors, which is the one declared exception to single authorship -- but it runs over whatever state it finds, and over a parked revert on a clean working tree it exits 0 and STRANDS that revert's state files behind it, so every later `safegit commit` refuses over a revert nobody is running. Scoping the predicate to the KIND is what lets a rebase's own `--continue`, `--abort` and `--skip` through without a second exemption list to keep in step: mid-rebase state reports the rebase kind, so those forms pass by construction.

The compute commands need the check made rather than inherited. Raw git refuses to start one operation over another, but these compute with `git <verb> --no-commit`, and git's refusal does not reach that form uniformly: `git cherry-pick --no-commit` over a parked revert applies cleanly, `git revert --no-commit` over a parked cherry-pick stages its inverse patch, and `git merge --no-ff --no-commit` over a parked revert reports "Automatic merge went well" and exits 0 (it does refuse over a parked cherry-pick). Without the check a pick over a parked revert committed and left `REVERT_HEAD` orphaned for the next `safegit commit` to refuse over. A parked operation whose result equals the current tree -- a pick of an already-applied commit, a revert of an already-reverted one -- leaves the tree clean, which is exactly why the dirty-tree check cannot stand in for it, and why the same argument covers `rebase`.

What that does not do is exempt the usual mid-operation case. A conflicted or parked operation dirties the tree by construction -- the conflict markers, or the staged result, ARE the dirt -- so `safegit merge --abort`, and a `rebase --continue` over staged resolutions, are refused at exit **5** like any other dirty-tree command. The refusal changes SHAPE rather than relaxing: it names the operation in flight and the commands that conclude or abandon it (`safegit merge-continue`, `git merge --abort`), instead of advice to commit the conflict, which is advice nobody can follow. Abandoning an operation is git's own command in practice, and the refusal is where safegit says so.

### Which of them forward to git, and which author their own commit

Four of them hand the operator's arguments to git and let git do the work: `switch`, `rebase`, `reset` and `bisect`. Four do not: `merge`, `cherry-pick`, `revert` and `pull` use git only to COMPUTE a result (`--no-commit`, always), and safegit's own commit pipeline writes the commit. That is the single-authorship rule the whole tool now rests on -- every commit made through safegit is safegit's: trailered, `commit-msg`-hooked, oplog-recorded, and reversible with `safegit undo`.

`safegit rebase` is the one declared exception, and it is uniform rather than conditional: git performs the replay and authors every replayed commit, always. The refusal that makes the rule structural lives at safegit's git-execution boundary, which refuses any git command line whose shape would let git create a commit unless the call site is a declared door -- and the rebase passthrough is the only door there is.

### The allowlist

Each of these commands validates its argv against an explicit allowlist BEFORE the lock is taken, before any git runs, and before the repository is read at all. An option the command honors passes; anything else is refused at exit **2**.

The direction is the point. A refusal LIST answers "is this one of the things we already thought about and decided against", and an option nobody has thought about slips through it into git, where it can change what git does while safegit's checks, its record of the operation and its report are still written for something else. An allowlist answers the other question -- "is this one of the things safegit can honor" -- which is the only honest one for a tool that implements a deliberate subset.

Two refusals come out of one table: a capability the table NAMES carries its own reason ("safegit's merge does not select a merge strategy, and here is why"), and everything else carries the subset law itself. `.stricttools/docs/divergences.md` catalogs every refused capability, with what git would do and why safegit does not.

**One argument is intercepted everywhere**, for the three operations safegit concludes itself. `safegit merge --continue`, `safegit cherry-pick --continue` and `safegit revert --continue` are refused and name `safegit merge-continue`, `safegit cherry-pick-continue` or `safegit revert-continue` instead -- see "The three conclusion commands". With an operation in flight the refusal is exit **5** and names the conclusion command; with NOTHING in flight the argv still never reaches git, because a forwarded `--continue` is exactly the shape the boundary refuses, and the refusal is safegit's (exit 1, its own reason) rather than git's "no merge in progress". A rebase's `--continue` is git's own and passes through untouched -- by construction rather than by exemption, because `safegit rebase`'s own in-flight refusal is scoped to state that is NOT a rebase, and mid-rebase state is a rebase; over any other in-flight state that refusal fires on `--continue` as on any other rebase command line. A mailbox application is concluded with `git am --continue`, which safegit never sees: there is no `safegit am`.

### git's own output, and where it goes

At a terminal these commands STREAM git's output live: whatever git writes appears as it writes it. Under `--json` they CAPTURE it instead and re-emit it when git finishes, with both of the child's streams going to stderr -- stdout there carries exactly one document, the framework's envelope, and git's narration written in front of it would make the stream unparseable.

Nothing is discarded, and one thing is lost: a machine-mode run of a long rebase or merge says nothing until it ends.

### The oplog baseline

Every one of these operations appends one oplog entry carrying the same three facts a commit entry carries: the full ref name (`ref`), the tip it moved from (`parent`), the tip it ended on (`sha`). The operator's own arguments ride alongside them.

**The `outcome` key is the discriminator, and it is present exactly when safegit authored NOTHING.** An entry written by the guarded-operation recorder describes what GIT did to the branch, and carries `outcome` -- `ok` or `failed`, or one of the merge's own words (`fast-forward`, `parked`, `up-to-date`). An entry written by the commit pipeline records a commit safegit made, and carries `ref`, `parent`, `sha`, `tree` and `attempts` -- plus `source` where the operation it concluded is known -- and NEVER an `outcome`, because such an entry exists only when the commit does. That absence is what `safegit undo` reads: an entry carrying the key is a ref movement it merely performed and refuses to reverse, which is how one op name (`merge`) can cover both the merge undo reverses and the fast-forward it will not.

An operation git REFUSED records an empty new tip and a failed outcome. That is the mechanism rather than a formality: the readers of this log (`oplog.LastRefUpdate`, doctor's bypass check, `undo`'s per-ref filter) take the newest entry for a ref that carries a new tip, so an entry with none is passed over and the position safegit really last left the branch at is still the one they compare against.

`switch` and `bisect` are the exception, because neither moves a branch ref: a switch moves HEAD, and a bisect step parks HEAD on some other commit entirely. Their positions ride under `observed_parent` and `observed_tip`, names those readers do not consume. An entry claiming a branch position there would reset doctor's bypass-detection baseline and mask an out-of-band commit made before the switch. On an unborn branch one of those two is empty -- a `switch -c` that leaves the branch unborn records no tip, a switch away from an unborn HEAD records no parent -- and both are harmless for the same reason: nothing consumes the `observed_` spelling.

## Unborn branches

An **unborn branch** is a branch with no commits: the state a repository is in between `git init` and its first commit, the state a clone that fetched without checking out sits in, and the state `safegit undo` of a root commit leaves behind. `git rev-parse HEAD` fails there, and every git command that takes `HEAD` as a treeish is fatal.

safegit treats it as an ordinary state rather than an edge case. The dirty-tree check compares the working tree against the **empty tree** instead of `HEAD` (a repository with no commits holds exactly that), so a clean unborn repository is clean and a dirty one is refused at exit **5** with its paths listed -- a staged addition and an untracked file alike.

**What works:**

- **`commit`** makes the root commit, exactly as it always did. `--allow-empty` with no named path makes a root commit whose tree has no entries at all.
- **`switch <branch>`** moves onto a branch that has commits; **`switch -c <name>`** starts a new branch and leaves it unborn too.
- **`merge <branch>`** and **`pull`** fast-forward: the branch is created by compare-and-swap pinned to the all-zero object name (git's "this ref must not exist yet"), and the index and working tree are put in step with the new tip. The oplog records the fast-forward outcome, and `safegit undo` refuses it like any other fast-forward.
- **`cherry-pick <commit>`** produces a ROOT commit with the source's author preserved. A conflicted pick parks and `safegit cherry-pick-continue` concludes it -- note the conflict shape: onto an unborn branch an add-only pick applies cleanly, so a conflict is a MODIFY/DELETE one whose index holds stages 1 and 3 and no stage 2.
- **`reset --hard <commit>`** works.
- **`--dry-run`** works: a plain merge preview answers "a fast-forward" without computing anything (merge-tree needs two commits and there is one), and a cherry-pick or revert preview computes against the empty tree as our side.

**What is refused, before git runs:**

- **`merge --no-ff`**, **`merge --no-commit`** and **`pull --merge-strategy no-ff`**, each naming the flag the operator typed. Each asks for a merge commit onto a first parent that does not exist -- `--no-commit` because safegit parks a merge by computing it with `--no-ff` underneath. `pull` asks before its fetch, so the refusal costs no network round-trip, and merge's `--dry-run` refuses identically to its real run.
- **`rebase`**: there are no commits to replay.
- **`bisect start`**: there is no range of commits to search.

Each refusal names the unborn branch and the way forward, and exits with the general code. See `.stricttools/docs/divergences.md`, "The friendly unborn pre-flight refusals" -- one of them is a deliberate divergence, since raw `git merge --no-commit` fast-forwards an unborn head at exit 0.

**What is NOT refused, and reads badly:** a `merge` naming a ref that does not resolve. On an unborn branch it reaches the compute step, which safegit pins to `--no-ff --no-commit`, so the operator meets git's own "Non-fast-forward commit does not make sense into an empty head" -- an error about a flag they never typed rather than about the ref they got wrong.

**`revert`** has no check of its own, and which of its two outcomes you get depends on what the reverted commit did. Reverting a commit that only ADDED files puts nothing back -- the unborn branch never had them -- so the result would be a root commit with an empty tree, and the standing empty-result rule refuses it: safegit removes the state it parked and says the revert produces no change. Reverting a commit that MODIFIED a file is the other outcome: the inverse patch wants to restore content the unborn tree does not carry, so the revert PARKS on a MODIFY/DELETE conflict, and `safegit revert-continue` concludes it into a root commit holding the restored content -- the same conflict shape a cherry-pick meets here, stages 1 and 3 with no stage 2.

## switch

Move HEAD onto another BRANCH, guarded by the worktree operation lock and the uncommitted-work check, and recorded in the oplog.

### There is no `safegit checkout`

git's `checkout` is two commands wearing one name. It MOVES HEAD, and it RESTORES files from a commit over whatever the working tree holds. The second one destroys uncommitted work with no record anywhere -- in a shared worktree, work that may be another session's -- so safegit does not implement it. Not guarded, not refused with a flag: absent. The destructive `checkout -- <path>` shape is **inexpressible** as a safegit command line rather than refused by one, which is a stronger guarantee than any check could be.

What is left is the navigation half, under git's own modern name for it.

### Arguments and flags

`safegit switch <branch>` moves onto a branch that already exists. `safegit switch -c <new-branch>` creates one where you are standing and moves onto it (`-c` is switch's own spelling; it replaces checkout's `-b`). That is the whole surface.

**`-c` takes the branch name and nothing else.** git's `switch -c <new> <start-point>` starts the new branch at any commit-ish; here a start-point alongside `-c` is refused at exit **2**, and the refusal names the two commands that do the same work: `git branch <name> <start>`, then `safegit switch <name>`. Creating a branch at a commit nobody navigated to is a ref write wearing navigation's spelling, and it is a git capability safegit deliberately lacks rather than one it guards.

An argument that resolves to a commit but is NOT a branch -- a tag, an object name, `HEAD~3` -- is refused, because switching onto one detaches HEAD, and a detached HEAD is the state safegit's commit, conclusion and undo paths all refuse. The refusal prints the two ways forward: make a branch there and switch to it, or use `git switch --detach` when a detached HEAD is deliberately what you want. Note that `--detach` guards nothing here: the ARGUMENT is what detaches, so refusing the flag while accepting the argument would be a check that never fires.

An argument that resolves to NOTHING is deliberately left to git, so `safegit switch no-such-thing` exits with git's own verdict on that argument rather than a message safegit invented about branches. git's DWIM reading survives with it: a name that exists only on a remote still creates and lands on a local branch tracking it, exactly as `git switch` would.

Refused flags, each naming its reason: `--detach`, `-C`/`--force-create` (re-pointing an existing branch is a ref move dressed as navigation, and outside safegit's compare-and-swap), `-f`/`--force`/`--discard-changes` (throwing away uncommitted work that may be another session's), `--orphan` (a decision about the repository, not a step between branches), and `-m`/`--merge` (a dead flag through safegit: it exists to carry a dirty tree across, and the dirty-tree check refuses first). A pathspec is refused too, with the file-restoration reason above. Everything outside the allowlist is refused by the subset law.

### Examples

```bash
# Move onto an existing branch
safegit switch main

# Create a branch where you are standing and move onto it
safegit switch -c new-feature
```

### Safety Guarantees

- **Coordination guard, both layers**: the worktree operation lock first (a second safegit process in this worktree waits, then exits **8** naming the holder), then the dirty-tree check (exit **5**). See "The guarded commands and their two coordination layers".
- **The argument is checked against the repository, under the lock**: the branch-ness question is a state reading like any other, so it is asked after the lock is held rather than before it.
- **git's own exit code**: when `git switch` fails, safegit exits with the code git returned.
- **The index is git's**: safegit does not touch the index after the switch; whatever git left there is what remains.
- **Oplog recording**: the RESOLVED full ref name (never the operator's argument, which for a `-c` form is the literal flag string) and the positions HEAD moved between, under `observed_parent`/`observed_tip` -- see "The oplog baseline" above. A branch creation records the zero SHA as the position it came from, because the ref did not exist. A refused or failed switch records an empty new tip and a failed outcome.

## merge

Merge ONE other side into the current branch. git computes the merge; safegit writes the commit.

### The commit is safegit's

`safegit merge` is not a passthrough. git is asked to compute the merge with `--no-ff --no-commit` -- both flags always, whatever the operator passed -- and safegit's own conclusion engine turns the staged result into the commit. So a merge made through safegit carries safegit's trailers, runs the repository's `commit-msg` hook, is recorded in the oplog under the op name `merge`, and is reversible with `safegit undo`.

The `--no-ff` is not a preference: `--no-commit` alone cannot stop a fast-forward (git takes the fast-forward before the flag is consulted), so a bare `--no-commit` invocation racing a concurrent tip move would let git move the ref outside safegit's compare-and-swap. With `--no-ff` git always parks a merge state instead, and the conclusion's CAS ref update catches any movement.

**A clean merge concludes immediately**, in the same invocation: the message from `-m` or from git's own `MERGE_MSG` draft with its comment block stripped, the hook, the trailers, the CAS ref update, the state files removed. **A conflicted merge parks** exactly as it does under plain git, git's own CONFLICT narration is relayed, and the operator finishes with `safegit merge-continue`.

### Exactly one side

`safegit merge <committish>` takes ONE argument. An octopus merge -- several sides in one commit -- is refused: a conclusion has one staged result to check and one message to write however many sides went into it, and every check safegit makes over a merge is written against two.

The argument may be a branch, a tag or an object name; there is no branch-ness requirement here, because merging from a tag detaches nothing.

`FETCH_HEAD` gets its own check, because it is git's one token that expands into several heads. When the argument is spelled exactly `FETCH_HEAD` and the fetch marked more than one branch for merging, the command is refused UP FRONT -- before the fast-forward decision, so both arms are covered, and `safegit pull` inherits it. Without that check a single token would have produced a three-parent commit, or silently fast-forwarded onto the first line and dropped the rest.

### Fast-forward, and who decides

safegit decides fast-forward-ness itself, with a merge-base ancestry check, rather than letting git decide:

- **A fast-forward** (and no `--no-ff`, and not `--no-commit`, and HEAD on a branch) moves the ref by compare-and-swap and then puts the index and working tree in step with it. The ref move alone is not enough -- it leaves the INVERSE of the incoming diff staged -- so the sync is part of the operation, not an optional tidy-up. The oplog entry records the fast-forward outcome, and **`safegit undo` refuses it**: the new tip is a commit safegit did not create, and undo never rolls a branch back over one.
- **`--no-ff`** elects a merge commit even where a fast-forward was available -- except on an unborn branch, where there is no first parent for a merge commit to record and the flag is refused before git runs (see "Unborn branches").
- **`--ff-only`** refuses a non-fast-forward -- and the refusal is **safegit's own** (the general failure code), not git's 128. Letting git make that call would let git move the ref, outside the compare-and-swap, in the race where the two branches stop being diverged between the check and the run.
- **`--no-commit`** computes and PARKS, even when the result is clean and even when a fast-forward was available: it says the operator wants to look at the result before it becomes anything, and a fast-forward that moved the branch silently would deny exactly that. Conclude the parked state with `safegit merge-continue`. (git's own `merge --no-commit` fast-forwards anyway; safegit's does not, so the flag means one thing wherever a merge can be parked at all.) The one place it cannot be is an unborn branch: parking is computed with `--no-ff` underneath, which an unborn head cannot take, so the flag is refused there before git runs -- see "Unborn branches".

### What the command line may say

Allowed, because each of them reaches only the compute step or the message draft git writes there -- and the pipeline commits that draft. (Reaching the compute step is what makes an option eligible, not what settles it: two options below reach it and are refused anyway, for reasons of their own.) `-m`/`--message`, `-F`/`--file`, `--no-edit`, `--ff`/`--no-ff`/`--ff-only`/`--no-commit` (safegit's own selectors, which never reach git), `--signoff`/`--no-signoff`, `--log`/`--no-log`, `--into-name`, `--stat`/`--no-stat`, `-X`/`--strategy-option` (strategy options tune the ort compute's content decisions without changing authorship, parking, or anything a conclusion reads -- every occurrence of either spelling is forwarded, and the `--dry-run` preview forwards them too), `--no-rerere-autoupdate`, and the state-control forms `--abort` and `--quit`.

Refused by name, each with its reason: `-s`/`--strategy` (selecting a strategy changes what is staged, and the conclusion's completeness and marker checks cannot see the change -- strategy OPTIONS are a different question and are allowed, above), `--allow-unrelated-histories` (a merge of two histories that share no commit is nearly always an accident, and the pre-flight below refuses the shape whether or not this flag was typed), `--rerere-autoupdate` (it stages a remembered resolution nobody made in this operation; the NEGATIVE spelling stays allowed, because it is the only per-run switch against the `rerere.autoUpdate` config key safegit still honors), `--squash` (a commit with a merge's content and none of its history), `-e`/`--edit` (safegit's commit surface has no editor; pass `-m`), `--commit` (the opposite of the `--no-commit` the compute step is pinned to -- git takes the last of the pair, so it would hand the commit back to git), `--autostash` (a dead flag: the dirty-tree check refuses before git runs, so a merge never reaches git with anything to stash), `--no-verify` (nothing of git's commit path runs here, so it would skip nothing), and `-S`/`--gpg-sign` (safegit's pipeline does not sign). Everything else is refused by the subset law.

### Unrelated histories

A merge whose two sides share no commit at all is refused before anything computes -- with no flag typed, and with no way past it on a safegit command line. The pre-flight fires when this branch's HEAD resolves and `git merge-base` reports no base between it and the other side; `safegit pull` inherits it, and `merge --dry-run` refuses identically. The exit code is the general failure code and the refusal is recorded in the oplog, on the same line the `--ff-only` refusal draws: it is a fact about where the branches stand rather than about what was typed.

Nothing about an unrelated merge defeats a check safegit makes -- the reason is that the state is nearly always reached by accident (a wrong remote, a wrong branch, a repository re-initialized over another) and what it produces is a permanent second root. git's own `refusing to merge unrelated histories` names nothing actionable; this refusal names the route for the case that is deliberate:

```bash
git merge --no-commit --allow-unrelated-histories other-project
safegit merge-continue
```

git computes and parks the merge, safegit's conclusion commits it -- trailers, `commit-msg` hook, undoable. An UNBORN branch is deliberately outside the predicate: it has no commit to take a merge base from, and a merge into one is the plain fast-forward safegit supports there.

On a SHALLOW clone the refusal stands but the wording changes, because `merge-base` reports no base there for a second reason: the base may exist below the fetch depth, in the part of the history that was never fetched. That refusal says the clone is shallow and names `git fetch --unshallow` followed by the same command again; the never-connected reading and the import route above are spoken only in a repository that holds its whole history.

### Examples

```bash
# Clean merge: one pipeline-authored merge commit, concluded in this invocation
safegit merge feature-branch

# Force a merge commit even where a fast-forward was available
safegit merge --no-ff feature-branch

# Refuse anything but a fast-forward
safegit merge --ff-only origin/main

# Compute the merge and park it for inspection, then conclude it yourself
safegit merge --no-commit feature-branch
safegit merge-continue
```

### Safety Guarantees

- **Coordination guard, both layers**: the worktree operation lock, then the dirty-tree check. See "The guarded commands and their two coordination layers".
- **The commit is safegit's**: trailers, the repository's `commit-msg` hook, the oplog entry, and `safegit undo` -- except after a fast-forward, where the tip is a commit git made long ago and undo refuses.
- **git's narration is relayed, on the channels it belongs on**: at a terminal git's CONFLICT lines appear as git writes them; under `--json` they are captured and re-emitted on stderr, so stdout carries only the envelope.
- **The way out is named**: when the merge parks, safegit prints `conclude it: safegit merge-continue` / `abandon it: git merge --abort` on stderr, from the same authority every other in-flight refusal reads.
- **Oplog recording**: exactly ONE entry under the op name `merge`, carrying the branch baseline (see "The oplog baseline" above). A merge that COMMITTED is the pipeline's own entry -- `ref`, `parent`, `sha`, `tree`, `attempts` and no `outcome` at all, which is what makes it undoable. Every other ending is the recorder's, and states itself in `outcome`: `fast-forward`, `parked`, `up-to-date` or `failed`. A merge that was concluded later with `merge-continue` records that command's own entry instead.
- **The payload says which outcome it was**: `merge`'s JSON payload carries an `outcome` member, because a fast-forward, a parked state and an up-to-date merge produce no commit and the other members are unreadable without it.

## rebase

Rebase the current branch onto an upstream ref with coordination safety guards that check for in-progress operations and record the rebase in the oplog for audit trail purposes.

### When to Use

Use `safegit rebase` instead of `git rebase` for coordination-guarded rebasing that verifies no other safegit operation is in progress before proceeding, preventing data loss when multiple sessions share a worktree and one session has uncommitted edits in the working tree.

### Arguments

`safegit rebase <upstream>` names exactly one upstream, and `--onto <newbase> <upstream>` names a new base for it. The upstream is stated on the command line rather than read from configuration. `git rebase <upstream> <branch>` -- which switches branches first -- is refused: that is a navigation safegit makes you state, so switch to the branch and then rebase it.

Allowed: `--onto`, `-i`/`--interactive`, git's own state-control verbs `--continue`/`--abort`/`--skip` (which take no argument of their own), `--autostash`, and `-r`/`--rebase-merges`, which preserves the replayed range's own merge topology -- the whole replay runs inside the one door where git authors, so a merge it re-creates is no more git's than a linear commit replayed beside it. That flag's optional value is attached only (`--rebase-merges=rebase-cousins`); the short cluster spelling `-rno-rebase-cousins` is read letter by letter and refuses.

Refused by name: `--apply` and its patch-application options `--whitespace`/`-C` (the apply backend keeps its state under a directory `git am` shares, and safegit's in-flight state reader, conflict machinery and refusals are all written against the merge backend), `-x`/`--exec` (a rebase through safegit is a replay and nothing else), and `--root` (a history rewrite rather than a replay; safegit's history-rewriting surface is `safegit scrub`). A pathspec is refused: a rebase replays whole commits, and there is no part of one it can replay. Everything else is refused by the subset law.

### rebase is the one place git authors commits

Every other command that could create a commit through safegit creates it in safegit's own pipeline. A rebase does not: git performs the replay and AUTHORS every replayed commit, so those commits carry no safegit trailers, and `safegit undo` does not reverse them. This is uniform rather than conditional -- there is no shape of `safegit rebase` where the commits come out safegit's -- and it is declared at the git-execution boundary as the single door through which authoring argv may pass.

A rebase's own `--continue` and `--abort` stay git's for the same reason: safegit has no verb that finishes one, so refusing them would leave the operator with nothing to run. A native, pipeline-authored non-interactive rebase is a deliberately deferred piece of work, tracked in `todo/pipeline-authored-rebase.md`.

### Examples

```bash
safegit rebase main
safegit rebase --interactive HEAD~5
safegit rebase --onto main feature-base
```

### Safety Guarantees

- **Coordination guard, both layers, plus its own refusals**: the worktree operation lock -- held for the whole rebase, an interactive one's editor session included, so a second safegit process in this worktree waits that long -- then the dirty-tree check, then a refusal (exit **5**) over an in-flight operation that is not a REBASE, and then a refusal on an unborn branch, which has no commits to replay (see "Unborn branches"). See "The guarded commands and their two coordination layers".
- **git's own exit code**: When `git rebase` stops or fails, safegit exits with the code git returned.
- **The index is git's**: a rebase stopped at a conflict keeps its unmerged entries; safegit does not touch the index after the rebase.
- **Oplog recording**: the branch baseline (see "The oplog baseline" above). The upstream ref rides alongside it.

## reset

Reset HEAD with selective guards that activate for the reset modes that write working-tree files -- `--hard`, `--merge` and `--keep` -- to prevent accidental data loss, while allowing soft and mixed resets to pass through without coordination checks.

### When to Use

Use `safegit reset` instead of `git reset` to get selective coordination guards. The guard activates for the modes that overwrite working-tree files, because those can destroy uncommitted work from other sessions. Soft and mixed resets pass through without the coordination guard since they do not modify the working tree.

### Arguments

`safegit reset` takes a COMMIT, in one of five modes: `--soft`, `--mixed`, `--hard`, `--merge`, `--keep`. The arguments are forwarded to git after the coordination guard passes (for the working-tree-writing modes only).

The **pathspec form** is refused, in every spelling that gives it away: `reset <commit> -- <path>`, a second revision that names a path, a bare argument that resolves to no commit but does name one, and the `--pathspec-from-file` family. `git reset <path>` writes the SHARED index entry by entry, and safegit's whole design keeps out of that file -- every commit stages into a temporary index of its own so that concurrent sessions cannot stage over each other, and a reset of one path would be the single exception, invisible to everything safegit records. To unstage part of a file, reset the whole path and commit the hunks you want with `safegit commit --hunks`.

`-p`/`--patch` is refused too: it opens an interactive hunk session, and safegit's surface has no interactive mode anywhere. An argument that resolves to NEITHER a commit nor a path is deliberately left to git, so `safegit reset --hard no-such-ref` exits with git's own verdict on it.

### Examples

```bash
# Soft reset (no guard needed)
safegit reset --soft HEAD~1

# Hard reset (guarded)
safegit reset --hard HEAD~3

# --merge and --keep write working-tree files too, so they are guarded as well
safegit reset --merge HEAD~1
safegit reset --keep HEAD~1
```

### Safety Guarantees

- **Selective guard**: the worktree operation lock is taken for every reset, because every reset moves HEAD; the modes that write working-tree files -- `--hard`, `--merge`, `--keep` -- additionally go through the dirty-tree check. Which modes those are is DERIVED from internal/gitexec's classification table rather than re-read here, so the vocabulary is declared in one place.
- **git's own exit code**: When `git reset` fails, safegit exits with the code git returned.
- **The index is git's**: safegit does not touch the index after the reset, so a `--soft` or `--mixed` reset leaves exactly what git staged.
- **Oplog recording**: the branch baseline (see "The oplog baseline" above). The operator's arguments ride alongside it.

## bisect

Binary search through commits to find the commit that introduced a bug, with selective coordination guards that activate for the stepping subcommands -- the ones that check another commit out -- to protect the working tree from concurrent modification.

### When to Use

Use `safegit bisect` instead of `git bisect` for coordination-guarded bisecting that protects the working tree from concurrent modification by other sessions sharing the same worktree, with selective guards that activate for the stepping subcommands.

### Arguments

The subcommand vocabulary safegit forwards is the one its git classification table declares -- `start`, `good`, `bad`, `old`, `new`, `skip`, `run`, `replay`, `reset`, `terms`, `log`, `view` -- and a subcommand outside it is refused. The same declaration is what tells safegit which of them write the working tree and therefore need the uncommitted-work check, so what safegit ADMITS and what it KNOWS about what it admitted cannot drift apart. bisect's OPTION allowlist is deliberately empty: every option git's bisect takes relabels its terms, changes what it checks out or limits the walk, and none of them has been considered here.

### Examples

```bash
safegit bisect start
safegit bisect bad HEAD
safegit bisect good v1.0.0
safegit bisect reset
```

### Safety Guarantees

- **Selective guard**: the worktree operation lock is taken for every `bisect` invocation; the STEPPING subcommands (`start`, `good`, `bad`, `old`, `new`, `skip`, `run`, `replay`, `reset`) additionally go through the dirty-tree check, because each of them checks another commit out. The reporting forms (`terms`, `log`, `view`) do not. Which is which is DERIVED from internal/gitexec's classification table rather than kept as a list here.
- **`bisect start` on an unborn branch is refused** before git runs: there is no range of commits to search there. See "Unborn branches".
- **`bisect run` and build artifacts**: `git bisect run` steps by itself, so the dirty-tree check applies to the invocation and not to each step -- but a build the script performs between steps leaves whatever it wrote in the working tree. Anything the repository IGNORES never counts as dirt; a build artifact that is NOT gitignored does, and the next guarded `bisect` invocation in that worktree is refused at exit 5 until it is cleaned up or ignored.
- **git's own exit code**: When `git bisect` fails, safegit exits with the code git returned.
- **The index is git's**: safegit does not touch the index after the bisect step.
- **Oplog recording**: the branch being bisected and the positions HEAD moved between, under `observed_parent`/`observed_tip` -- a bisect step moves HEAD and no branch ref, so it uses the same observed spelling `switch` does. See "The oplog baseline" above.

## cherry-pick

Apply ONE commit onto the current branch. git computes the patch; safegit writes the commit.

### One commit, and the sequential form

`safegit cherry-pick <commit>` takes the name of exactly ONE commit. Several commits in one command line are refused, and so are the range and revision-set spellings -- `A..B`, `A...B`, a leading `^`, `^!`, `^@`. The refusal is on the OPERATORS rather than on how many arguments were typed, and that distinction is the whole point: `git cherry-pick A..B` hands the operation to git's sequencer, queue directory and all, even where the range holds a single commit. A check that counted argv tokens would let exactly that command line through as "one commit".

What to do instead is what the refusal says: run the command once per commit, in the order you want them applied. Each invocation authors its own commit, and each one is separately undoable.

### The commit is safegit's

git is asked to compute the pick with `--no-commit`, and safegit's conclusion engine turns the staged result into the commit -- trailers, the repository's `commit-msg` hook, one oplog entry under the op name `cherry-pick`, and `safegit undo` reverses it. The AUTHOR is preserved from the commit being applied and the committer is you, which is git's own division: a cherry-pick applies somebody else's change, so their authorship travels with it.

**One thing safegit writes itself.** `git cherry-pick --no-commit` records nothing about the picked commit -- there is no `CHERRY_PICK_HEAD` on either the clean or the conflicted path, because git only writes that file for a conflicted pick made without `-n`. The conclusion machinery, the author preservation, the marker labels, `git status` and `git cherry-pick --abort` all key on that file, so after the compute step safegit writes `.git/CHERRY_PICK_HEAD` itself, in git's own one-line format, on the clean and the conflicted path alike. The parked state then looks exactly like the one a conflicted pick leaves, and the conclusion's own state-file cleanup removes it.

**A clean pick concludes immediately**, in the same invocation. **A conflicted pick parks**, `git status` shows the pick as it always did, and the operator finishes with `safegit cherry-pick-continue`.

### What the command line may say

Allowed: `-x`, `-s`/`--signoff` and `--no-edit` (git writes these into the message draft at the compute step, and the pipeline commits that draft), `-m`/`--mainline` (which parent of a merge commit the pick is relative to -- a compute-step question), `-X`/`--strategy-option` (strategy options tune the ort compute and leave every conclusion protection reading what it reads without one; the preview forwards them), `-n`/`--no-commit` (the operator asking for the pick to be computed and left staged; it authors nothing, so it is forwarded to git -- but it COMPUTES, so it takes the same in-flight refusal the restructured form takes), `--no-rerere-autoupdate`, and the state-control forms `--abort` and `--quit`, which author nothing AND are the way out of a parked operation, so they are never refused over one.

Refused by name: `--skip` (it moves past one commit of a SEQUENCE and keeps the rest going, and there is no sequence here -- abandon with `git cherry-pick --abort` and pick the commits you do want, one invocation each), `-e`/`--edit`, `-S`/`--gpg-sign`, `--strategy` (a pick computed with another strategy parks a content conflict git recorded nowhere the checks can read; the SHORT `-s` here is signoff, which is git's own spelling, so only the long name is refused), `--rerere-autoupdate` (the same remembered-resolution refusal merge carries, with the negative spelling still allowed), `--ff` (it lets git move the branch onto the picked commit outright, outside the compare-and-swap), `--commit`, `--cleanup`, and the `--allow-empty`/`--allow-empty-message`/`--keep-redundant-commits`/`--empty` family (safegit's pipeline refuses a commit that changes nothing, and there is no flag here that turns that refusal off). Everything else is refused by the subset law.

### Examples

```bash
# Apply one commit; the result is a pipeline-authored commit
safegit cherry-pick abc1234

# Several commits: one invocation each, in the order you want them applied
safegit cherry-pick abc1234
safegit cherry-pick def5678
```

### Safety Guarantees

- **Coordination guard, both layers**: the worktree operation lock, then the dirty-tree check. See "The guarded commands and their two coordination layers".
- **The commit is safegit's**: trailers, the source author preserved, the `commit-msg` hook, the oplog entry, and `safegit undo`.
- **A conflicted pick parks like git's**: the unmerged entries, `CHERRY_PICK_HEAD` and the message draft are all there, and the conclusion is `safegit cherry-pick-continue`. `safegit cherry-pick --continue` is refused and points there -- see "The three conclusion commands".
- **git's narration is relayed, on the channels it belongs on**: streamed at a terminal, captured and re-emitted on stderr under `--json`.
- **Oplog recording**: exactly ONE entry under the op name `cherry-pick`, carrying the branch baseline (see "The oplog baseline" above). A pick that COMMITTED is the pipeline's own entry -- `ref`, `parent`, `sha`, `tree`, `attempts`, and no `outcome`, which is what makes it undoable; a pick that stopped or was refused is the recorder's, and says so in `outcome`. A pick concluded later with `cherry-pick-continue` records that command's own entry instead.

## revert

Undo ONE commit by applying its inverse patch. git computes the patch; safegit writes the commit.

### One commit, and the sequential form

`safegit revert <commit>` takes the name of exactly ONE commit. Several commits in one command line are refused, and so are the range and revision-set spellings -- `A..B`, `A...B`, a leading `^`, `^!`, `^@` -- for exactly the reason cherry-pick refuses them: a range hands the operation to git's sequencer even where it holds a single commit, and the refusal is on the operators rather than on the argument count. Run the command once per commit, in the order you want them reverted.

### The commit is safegit's

git is asked to compute the inverse patch with `--no-commit`, and safegit's conclusion engine turns the staged result into the commit -- trailers, the repository's `commit-msg` hook, git's state files (`REVERT_HEAD`, `MERGE_MSG`, `AUTO_MERGE`) cleaned up afterwards, one oplog entry under the op name `revert`, and `safegit undo` reverses it.

The identity is git's own semantics and the OPPOSITE of a cherry-pick's: a revert is a new change of the reverter's, so YOU are both author and committer, not the author of the commit being reverted.

**Move records are inverted.** The commit carries the INVERSE of every `Moved:` record the reverted commit declared, each under a fresh id and each marked `observed` -- undoing a move is a move. The inverses are minted through both doors, so a clean computed revert and a conflicted one concluded with `safegit revert-continue` declare the same thing. Retractions are deliberately not inverted: a retraction says "that record was wrong", and reverting the commit that said so does not make the record right again. An inverse is still minted for a record some later commit retracted, because the inverse describes THIS revert commit's own tree delta, and the trees arbitrate any wrong claim.

**A clean revert concludes immediately**, in the same invocation. **A conflicted revert parks**, and the operator finishes with `safegit revert-continue` -- which reaches the same engine, so the two paths declare the same records and clean up the same state files.

### What the command line may say

Allowed: `-s`/`--signoff` and `--no-edit` (git writes the signoff trailer into the message draft at the compute step, and the pipeline commits that draft), `-m`/`--mainline` (which parent a merge commit is reverted relative to), `-X`/`--strategy-option` (strategy options tune the ort compute; note that a revert applies an INVERSE patch, so `-X theirs` keeps the revert and `-X ours` keeps the commit being reverted), `-n`/`--no-commit` (forwarded to git, but it COMPUTES, so it takes the same in-flight refusal the restructured form takes), `--reference` (the line git puts in the draft naming the reverted commit), `--no-rerere-autoupdate`, and the state-control forms `--abort` and `--quit`, which author nothing AND are the way out of a parked operation, so they are never refused over one.

Refused by name: `--skip` (there is no sequence to skip a step of -- conclude the revert you are in with `safegit revert-continue`, or drop it with `git revert --abort`), `-e`/`--edit`, `-S`/`--gpg-sign`, `--strategy` (the SHORT `-s` here is signoff, exactly as it is on cherry-pick, so only the long name is refused), `--rerere-autoupdate` (the same remembered-resolution refusal merge carries, with the negative spelling still allowed), `--commit`, and `--cleanup`. Everything else is refused by the subset law.

### Examples

```bash
# Undo one commit; the result is a pipeline-authored commit
safegit revert abc1234

# Several commits: one invocation each
safegit revert abc1234
safegit revert def5678
```

### Safety Guarantees

- **Coordination guard, both layers**: the worktree operation lock, then the dirty-tree check. See "The guarded commands and their two coordination layers".
- **The commit is safegit's**: trailers, YOU as author and committer, the inverse move records, the `commit-msg` hook, the oplog entry, and `safegit undo`.
- **A conflicted revert parks like git's**: the unmerged entries, `REVERT_HEAD` and the message draft are all there, and the conclusion is `safegit revert-continue`. `safegit revert --continue` is refused and points there -- see "The three conclusion commands".
- **Oplog recording**: exactly ONE entry under the op name `revert`, carrying the branch baseline (see "The oplog baseline" above). A single revert that COMMITTED is the pipeline's own entry -- `ref`, `parent`, `sha`, `tree`, `attempts`, and no `outcome`, which is what makes it undoable; a revert that stopped, was refused, or ran as a queued sequence git authored is the recorder's, and says so in `outcome`. A revert concluded later with `revert-continue` records that command's own entry instead.

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

List every pre-pre-push hook location safegit knows about, showing each hook's store-relative name, origin, file path, and whether it is executable, so you can audit which checks run before every push.

### The three origins

| Origin | Where | Runs when |
|--------|-------|-----------|
| `local` | The tool-owned live store under the repository's **common** `.git/safegit/hooks/`, which `hook install` writes to and every worktree shares | Every `safegit push` and `safegit hook run`. A non-executable one is a refusal (exit code 25), never a silent skip |
| `tracked` | The hooks the **checkout** provides, in `.safegit/hooks/` in the work tree | Every `safegit push` and `safegit hook run`, before the local ones. A non-executable one is a refusal (exit code 25) too |
| `legacy` | The pre-migration location in git's own `.git/hooks/` (`pre-pre-push` and `pre-pre-push.d/`) | Never. Their presence makes every push and `hook run` refuse with exit code 24 until `safegit hook migrate` relocates them |

Membership of the checkout-provided store is the directory, not git: an uncommitted -- even gitignored -- executable file in `.safegit/hooks/` runs on the next push exactly like a committed one. So cloning a repository and pushing from that checkout runs the repository's scripts; that execution happens only on push and `hook run`, never on clone or inspection, and this listing is how the set is read beforehand.

Non-executable entries, dot-files and editor backups are listed too, because the hook an operator is asking about is usually the one that is *not* running. A dot-prefixed or `~`-suffixed entry is shown as `not a hook`, and anything without an execute bit as `NOT EXECUTABLE`.

### Examples

```bash
safegit hook list
```

Shows each hook's name, origin, state and file path, then exits 24 if any legacy-location hook was listed.

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

### Discovery's refusals reach here too

`hook run` runs the same discovery a push does, so it produces the same two
verdicts about the checkout: exit **24** while a hook is still in the
pre-migration `.git/hooks` location, and exit **25** when a discovered hook is
not executable, in either store. Neither is a quiet skip and neither reports "no
hooks to run": a command whose whole purpose is to say whether the checks pass
must not exit 0 because a check was passed over.

### `--dry-run` is refused

`hook run` declares `dry_run_supported=false`. A hook is a script the operator
supplied; safegit cannot know what it does, and the effects handle has no way to
mint a subprocess that is fed stdin, so any would-do log rendered here would be
invented. Passing `--dry-run` therefore fails with the reason instead of
pretending. Use `safegit hook list` to see which scripts a push would run.

## hook install

Install a pre-pre-push hook by copying a script file into the live store under the repository's common `.git/safegit/hooks/` directory and making it executable, so it will run automatically before every `safegit push` operation performs any network I/O. Because the store is keyed on the common git dir, a hook installed from a linked worktree is the same hook every worktree of the repository runs. An existing destination is refused rather than overwritten: upgrading a hook is `safegit hook remove <name>` followed by an install.

### Arguments

| Name | Required | Description |
|------|----------|-------------|
| `path` | Yes | Path to the hook script file to install |

### Examples

```bash
safegit hook install ./scripts/lint-check.sh
```

The script is copied to `.git/safegit/hooks/` and made executable.

## hook remove

Remove one hook from the tool-owned live store by name -- either its store-relative path (`pre-pre-push.d/20-lint`) or just its base name -- so a hook can be retired or replaced without deleting files by hand.

### Arguments

| Name | Required | Description |
|------|----------|-------------|
| `name` | Yes | Name of the installed hook to remove, as shown by `safegit hook list` |

### The two stores are not symmetric here

- A name that only the **checkout-provided** store carries is refused with an explanation: that hook is a file in the work tree, and removing it means deleting the file and committing that.
- A name **both** stores carry removes the live one and says plainly that the checkout's hook still runs. Refusing outright would make the live hook unremovable by name.

Both non-removals exit 1. So does an ambiguous base name that resolves to more than one live hook, which asks for the full name instead. A name that is only in the pre-migration `.git/hooks` location exits **24** and points at `safegit hook migrate`.

### Examples

```bash
safegit hook remove pre-pre-push.d/20-lint
safegit hook remove lint-check.sh
```

## hook migrate

Move safegit's hooks out of git's own `.git/hooks/` directory into the tool-owned live store.

`pre-pre-push` and `pre-pre-push.d/` are the only two names safegit ever wrote into git's directory, so the relocation is unconditional -- there is nothing to identify or choose. While either name sits there, every `safegit push` and every `safegit hook run` refuses with exit 24; this is the command that clears that. With nothing to move it reports success and explains why.

Both ends are under the repository's **common** git dir -- git's hook directory is the same one in every worktree -- so a migration run from a linked worktree relocates the repository's hooks.

### Examples

```bash
safegit hook migrate
```

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

All safegit configuration is stored in `.git/safegit/config.json` and managed via the `config show`, `config get`, and `config set` subcommands. The configuration controls commit CAS retry behavior, lock acquisition timeouts, pre-pre-push hook execution timeouts, push retry attempts for transport errors, and the submodule auto-bump decision. The following keys are available with their default values:

| Key | Default | Description |
|-----|---------|-------------|
| `commit.casMaxAttempts` | `5` | Maximum CAS retry attempts for commits |
| `commit.autoBumpParent` | (unset -- and an unset one is a refusal, see below) | Whether a commit in a submodule also commits the parent's moved gitlink |
| `lock.acquireTimeoutSeconds` | `30` | Timeout for acquiring a lock (per-ref, operation, or rewrite) |
| `hooks.preprepush.timeoutSeconds` | `1800` | Timeout for pre-pre-push hooks (30 minutes) |
| `push.retryAttempts` | `3` | Number of push retry attempts on transport errors |

Those five are the whole key set. `config.json` also carries a `schemaVersion` member (currently `1`), but it is not a key: `config show` does not list it, and `config get schemaVersion` and `config set schemaVersion` both answer `unknown config key`. Every integer key must be a positive integer parsed whole -- `5abc` is refused rather than silently read as 5.

**`commit.autoBumpParent` has no working default, and its PRESENCE is mandatory.** It is read from the PARENT repository's config, and it only applies inside a submodule. `true` means bump the parent's gitlink, `false` means deliberately do not -- and absent means nobody has decided, which safegit refuses rather than guessing at, because either guess is wrong in somebody's repository. The refusal happens BEFORE anything is committed (a dry run validates it too, reading the parent's config and creating nothing there), and it names the command that settles it:

```
safegit config set commit.autoBumpParent true    # run in the PARENT repository
```

Reaching that refusal after the commit was what the pre-commit validation replaced: it left the submodule with a commit whose parent pointer was never updated and a nonzero exit. `undo` is the one path that still meets it late, because it can only discover the question after the rollback it is undoing.

There is no oplog size or rotation setting. The oplog is append-only and complete by design; the exclusive lock held across each append is what makes concurrent appends atomic, and nothing truncates or rotates the file. `log.maxSizeMB` was removed -- writing it now reports an unknown config key.

## Exit Code Reference

Every code safegit produces is a named constant in `internal/exitcode`, and the
table below is generated from that registry by `scripts/gen-exit-table`. Do not
edit it by hand: `go test .` re-renders it from the registry and fails when the
file is stale.

Two codes cover argument errors, and the split is not cosmetic. The CLI
framework refuses a command line it cannot parse -- an unknown flag, an unknown
command, a missing required flag, a value outside a declared choice set, a
`--hunks` element the flag's own validator rejects -- and those refusals **exit
1**, which the framework owns. Code 2 is safegit's own validation, reached only
after the parse succeeded: mutually exclusive flags, a missing commit message,
one path named both as a whole file and in `--hunks`. Unifying the two awaits an
upstream ruling on a framework usage-error code.

The table below covers safegit's own codes. The commands that FORWARD their
arguments to git -- `switch`, `rebase`, `reset`, `bisect` -- exit with **git's**
exit code when git itself fails, and git's codes are not in this registry:
git's fatal errors exit 128 or 129. A code from one of those commands is
therefore only safegit's when the failure happened before git ran (the
coordination guard, an uninitialized repository, a rejected argument).

`merge`, `cherry-pick`, `revert` and `pull` use git only to COMPUTE, and
safegit's pipeline writes the commit, so the conclusion engine's own refusals
reach them after git has already run: exit **17** (the staged result carries an
unmerged entry no resolution names), **18** (the content it would commit still
holds a complete conflict region), **27** (materializing a resolution would
destroy a hand edit) and **26** (the commit stands and its aftercare did not
finish). A conflicted compute still surfaces git's own verdict -- a conflicted
merge exits 1 the way git does -- and the operation parks for the matching
`-continue` command.

<!-- BEGIN generated exit-code table (scripts/gen-exit-table) -->

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |
| 2 | Argument error safegit itself rejected (the framework's own parse refusals exit 1) |
| 3 | Not a git repository |
| 4 | safegit not initialized |
| 5 | Coordination guard refused (another operation owns the working tree) |
| 7 | CAS retries exhausted |
| 8 | Timed out acquiring a lock a live holder still owns |
| 9 | write-tree failed |
| 10 | commit-tree failed |
| 11 | A named path or directory contributes nothing to the commit |
| 14 | Hunk spec given for a binary file |
| 15 | Hunk spec given for a symlink, which has no hunks to select |
| 16 | A pre-commit or commit-msg hook refused the commit |
| 17 | A conclusion's declared resolutions do not match the conflicted paths in the index |
| 18 | A conclusion's content still holds a complete conflict region |
| 19 | A claim about a move (--moved, --moved-retract, a `mv` pair) is contradicted by the repository |
| 20 | Pre-pre-push hook failed |
| 21 | Pre-pre-push hook timed out |
| 22 | The remote backup slot holds work missing from the local history |
| 23 | The branch has no backup slot on the remote |
| 24 | Hooks are still in the pre-migration .git/hooks location (run `safegit hook migrate`) |
| 25 | A discovered hook is not executable, in either store |
| 26 | The operation's ref move is real, but a step after it did not finish (aftercare) |
| 27 | A conclusion's working-tree write would destroy a hand edit no side of the conflict accounts for |
| 28 | The shared index carries an unmerged entry, so no commit can be built beside it |
| 29 | A named symlink's target will not resolve in another checkout: absolute, or outside the repository (`--allow-non-portable-targets` records it anyway) |
| 30 | A history rewrite was refused before any ref moved (nothing changed) |
| 31 | A history rewrite stands, but post-rewrite verification found residue or skipped the working-tree sync |
| 32 | A concurrent change altered the moves this commit's delta witnesses; nothing was committed, so run the command again |
| 40 | The push did not get through: git push failed, or the refs could not be safely re-read around it |
| 41 | The remote ref moved after safegit observed it, so the --force-with-lease expectation no longer matched |
| 50 | doctor found at least one error-severity problem (warnings alone exit 0) |
| 70 | Internal invariant violated (a bug) |

<!-- END generated exit-code table -->
