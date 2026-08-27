---
title: Divergences
description: "The catalog of every place where safegit's design philosophy and git's own idiom pulled in different directions, with what git does, what safegit does, and which way each ruling went."
---

# Where safegit follows git, and where it deliberately does not

safegit wraps git, so every command it offers stands next to something git
already does. Most of the time the two agree. This document is the catalog of
the places where they do not, or where they only appear to.

## The subset law

safegit does not promise full git support and never will. It deliberately
implements a small, opinionated subset of git's functionality, chosen for
agent-heavy workflows. When a git feature, command, flag or edge case is judged
actively harmful or irrelevant for that workflow, safegit deliberately omits it
and never looks back — no compatibility pressure, no "but git supports it"
argument. Every such omission is recorded here, unapologetically. safegit is for
our agents, not for all humans.

That law is why this document exists in the shape it does. An entry is not an
apology for a gap; it is the record of a decision, with what git does beside it
so a reader can see exactly what they are giving up and why.

## What is in here

An entry belongs in this catalog when safegit's design philosophy — hard errors
instead of warnings, explicit declarations instead of inferred modes, no silent
behavior, one consent seam per irreversible act, a structured stdout — pulled in
a different direction from git's own idiom: operator muscle memory, permissive
defaults, and semantics people already carry between repositories. An internal
architecture choice with no git-facing analogue is not an entry, however
deliberate it was.

Each entry records which way the decision went:

| Direction | Meaning |
|-----------|---------|
| **git-like** | git's idiom won. safegit does what a git user expects, even where the philosophy would have argued for something stricter or louder. |
| **ours** | The philosophy won. safegit does something git does not, and an operator's git reflex will be wrong here. |
| **mixed** | The two were separated: part of the behavior is git's, part is safegit's, and the entry says where the line falls. |

Status is **deliberate** for a settled decision, or **provisional — awaiting
review** for one that has not yet been confirmed and may be overturned. A
behavior newly cataloged here is provisional until it has been reviewed as an
entry, whatever its direction says.

> **The provisional entries**, listed rather than counted, are these:
>
> - [A dirty working tree refuses the guarded commands, untracked files
>   included](#a-dirty-working-tree-refuses-the-guarded-commands-untracked-files-included)
> - [`safegit commit` refuses while an operation is in
>   flight](#safegit-commit-refuses-while-an-operation-is-in-flight)
> - [`reset` is refused when the tree is dirty, in exactly the modes that write
>   to it](#reset-is-refused-when-the-tree-is-dirty-in-exactly-the-modes-that-write-to-it)
> - [An autostash is applied only when it belongs to the merge being
>   concluded](#an-autostash-is-applied-only-when-it-belongs-to-the-merge-being-concluded)
>   — its stated limit is part of what it awaits.
> - [A conclusion whose commit already stands finishes the cleanup, and commits
>   nothing](#a-conclusion-whose-commit-already-stands-finishes-the-cleanup-and-commits-nothing)
> - [A working-tree write that would destroy a hand edit is
>   refused](#a-working-tree-write-that-would-destroy-a-hand-edit-is-refused)
> - [A path the commit stops tracking never pairs into an observed
>   move](#a-path-the-commit-stops-tracking-never-pairs-into-an-observed-move)
> - every entry under [The subset boundary](#the-subset-boundary), which is
>   where the allowlist verdicts live — including the fast-forward rulings
>   ([the fast-forward-only
>   refusal](#the-fast-forward-only-refusal-is-safegits-not-gits), [a parked
>   merge stays
>   parked](#a-parked-merge-stays-parked-even-when-it-could-have-fast-forwarded),
>   [a fast-forward is safegit's own ref
>   move](#a-fast-forward-is-safegits-own-ref-move-and-undo-refuses-it)) and
>   [the `FETCH_HEAD`
>   refusal](#a-fetch-that-marked-several-branches-is-not-a-merge-safegit-will-make).
>
> Every entry under "The subset boundary" is a verdict about what safegit will
> not do, and every one of them is open at the review: overturning one means
> putting a capability back into an allowlist, not writing new machinery.

Every future change that introduces a decision of this kind adds its entry here.

---

## Committing

### The commit command names its files; there is no staging area

- **git's idiom:** the index is a workspace. You build a commit up with `git
  add`, inspect it with `git status`, and `git commit` turns whatever is staged
  into a commit. `git commit -a` skips the building and takes every tracked
  change.
- **safegit:** `safegit commit -m "msg" -- a.go b.go` names its content on the
  command line. The commit is built in a per-invocation temporary index seeded
  from the parent commit's tree, so whatever sits in the shared `.git/index` is
  neither read nor committed, and there is no `-a`. Naming `.` expands to the
  whole repository, which is the closest equivalent. This is the property the
  whole tool exists for: two sessions committing at once cannot leak files into
  each other's commits, because neither one has a shared workspace to leak
  through.
- **Ruling:** ours — deliberate

### An argument that changes nothing is a refusal

- **git's idiom:** `git commit -- a.go b.go c.go` where `b.go` is unchanged
  commits `a.go` and `c.go` and says nothing about `b.go`.
- **safegit:** naming a path is a statement that it belongs in the commit. If it
  contributes nothing to the tree — a typo, a file already committed with this
  exact content, a directory that is empty on disk and absent from the parent
  tree — the whole commit is refused, and the refusal names **every** argument
  that contributed nothing, not just the first. Exit code 11
  (`PathMatchedNothing`).
- **Ruling:** ours — deliberate

### An explicitly named gitignored path is refused, and there is no override

- **git's idiom:** `git add ignored.log` refuses and tells you to pass `-f`;
  `git add -f ignored.log` stages it.
- **safegit:** an explicitly named gitignored path is refused with no escape
  flag of any kind. The refusal applies only to paths the caller typed: a
  directory expansion **skips** ignored files instead, because those are names
  the caller never wrote, and the skipped set is reported in the command's JSON
  payload so a machine consumer can see what was passed over.
- **Ruling:** ours — deliberate

### Directory expansion stops at a nested repository

- **git's idiom:** `git add dir/` where `dir/sub` is its own repository records
  a gitlink for `sub`, with a warning about an embedded repository.
- **safegit:** expanding a directory argument stops at any directory carrying
  its own `.git` — a submodule or an unrelated repository sitting inside this
  one. A submodule's gitlink is staged only when the caller names the gitlink
  itself, never as a by-product of naming something above it, so a commit can
  never move another repository's pointer by accident.
- **Ruling:** ours — deliberate

### A symlink whose target leaves the repository is refused

- **git's idiom:** git records the link text and says nothing. Whether that text
  resolves anywhere is not git's problem.
- **safegit:** committing such a link is REFUSED (exit 29), naming the literal
  target, with nothing staged and nothing committed. A link whose target
  resolves outside the repository is a fact about one machine: in anybody
  else's checkout it resolves to nothing, or — worse — to a different file that
  happens to sit at that absolute path, which is a reference the repository
  cannot honor and cannot be seen to be dishonoring. The earlier answer was to
  commit it with a warning line, and that is exactly the shape this tool refuses
  everywhere else: a warning nobody reads in front of a mistake nobody wanted.
  `--allow-escaping-targets` elects committing it and restores the notice line;
  it is the only way to say so. The refusal covers ADDING or STAGING escaping
  link content, which is where such a link enters history; `safegit mv` moving
  an already-tracked one is untouched, because a move-only commit carries the
  blob across without re-reading the link. An ABSOLUTE target that resolves
  INSIDE the repository is accepted, which is machine-specific in the same way
  and is listed for the review rather than defended.
- **Ruling:** ours — deliberate

### `--untrack` removes the index entry and leaves the file on disk

- **git's idiom:** `git rm --cached <path>` drops the index entry and keeps the
  file. A path that is not tracked is an error.
- **safegit:** `safegit commit --untrack <path>` does the same job inside the
  commit. Any tracked path qualifies, not only an ignored one, and a target the
  commit's parent does not track is a hard error rather than a no-op. When the
  path is not covered by any ignore rule, safegit prints a notice saying so —
  nothing stops the path from being committed again, and the forgotten
  `.gitignore` edit is the usual reason someone is here.
- **Ruling:** ours (the shape is git's, the naming-it-a-flag-of-commit and the
  notice are safegit's) — deliberate

### Repeated `-m` values join with a blank line

- **git's idiom:** `git commit -m subject -m body` produces a subject, a blank
  line, and a body.
- **safegit:** identical. `-m` is repeatable and the values are joined with a
  blank line, so the first is the subject and the rest are paragraphs.
- **Ruling:** git-like — deliberate

### No editor, anywhere

- **git's idiom:** `git commit`, `git merge`, `git revert` and `git cherry-pick`
  open `$EDITOR` when no message is supplied, and `git add -p` / `git add -e`
  select content interactively.
- **safegit:** no command ever opens an editor or an interactive picker. A
  message comes from `-m`, from `-F`, or (for a conclusion) from the draft git
  already wrote. Hunk selection is declared as data: `--hunks 'path:1,3'`, and
  `git reset -p`'s interactive session has no counterpart either. `-e`/`--edit`
  is refused by name on `merge`, `cherry-pick` and `revert` rather than
  forwarded into a command that would then try to open one; the refusal names
  `-m`, or the matching `-continue` command's `-m`, as where the message comes
  from instead. The consequence is that safegit is fully usable from a script or
  an agent with no terminal, and that a `prepare-commit-msg` hook has nothing to
  prepare (see below).
- **Ruling:** ours — deliberate

### Grammar is never decided by what is on disk

- **git's idiom:** git routinely disambiguates an argument by consulting the
  world. `git checkout foo` is a branch if a branch named `foo` exists and a
  path otherwise; `git add` treats an argument as a directory or a file by
  looking at it.
- **safegit:** an argument's meaning comes from the command line alone. A
  positional path is always the literal name of a file, so a hunk selection has
  its own flag (`--hunks 'path:1,3'`, split on the last colon) and a file whose
  name ends in `:1` stays committable. `scrub file` takes `--delete` or
  `--replace-with <file>` as a required choice, where it used to call `os.Stat`
  on the target and infer which one was meant — the same command line meant
  opposite things from different directories, and a mistyped path was a silent
  deletion from history. A trailing slash, not the filesystem, is what says
  whether an argument names a symlink or the directory it points at.
- **Ruling:** ours — deliberate

### Committing in a submodule refuses until the parent has decided about the gitlink

- **git's idiom:** committing in a submodule leaves the parent repository's
  gitlink pointing at the old commit, silently, until somebody commits the
  parent.
- **safegit:** a commit-family command in a submodule refuses unless the parent
  repository's safegit config has explicitly answered whether the gitlink should
  be bumped automatically (`commit.autoBumpParent`, which has no default). An
  unanswered question is a hard error, not a guess in either direction. `commit`,
  `--amend`, `--reword`, `safegit mv` and the three conclusions raise that
  refusal **before** anything is written, so nothing moves in either repository.
  `safegit undo` now raises it in the same place: the decision is required
  between loading the config and taking the operation lock, so the refusal fires
  before any ref moves, exactly as it does on every other commit-family route.
  It used to fire only *after* the submodule's ref had been moved back, which
  produced the outcome the check exists to prevent.
- **Ruling:** ours — deliberate

### An empty commit is refused; an empty merge is not

- **git's idiom:** `git commit` with an unchanged tree refuses and names
  `--allow-empty`. A merge commit, however, is created even when the tree is
  identical to the tip, because a merge records its parents whether or not
  anything changed.
- **safegit:** the same split. `safegit commit` refuses a tree-unchanged commit
  and names `--allow-empty`; `safegit merge-continue` commits without any flag
  at all. `safegit cherry-pick-continue` or `revert-continue` — the door where
  the operation was already in flight when the command ran — gets its own
  refusal naming `git <verb> --skip` and `git <verb> --abort`, because those
  commands have no `--allow-empty` to point at, and because the state is the
  operator's to resolve differently.
- **safegit, where the empty result is safegit's own compute:** `safegit
  cherry-pick` and `safegit revert` compute with `git <verb> --no-commit` and
  park what git staged. When that turns out to change nothing there is no commit
  to make, so safegit removes the state it just parked and the refusal says so —
  the branch, the index and the working tree stand where they did, and the next
  safegit command just works. git leaves the equivalent state in place and
  offers its own ways out of it (`--skip`, `--abort`, `--continue`, or an
  `--allow-empty` commit); the operation safegit is cleaning up after is one it
  started itself moments earlier, and one nobody asked to be left mid-flight.
  Where a half of that cleanup fails the operation really is still in flight,
  and the message says that instead, with the abort advice that is then correct.
- **Ruling:** git-like — deliberate

### `safegit commit` refuses while an operation is in flight

- **git's idiom:** `git commit` is how you *finish* a stopped merge. git leaves
  `MERGE_HEAD` in place, you resolve the conflict, and the next `git commit`
  picks the second parent up out of that file and records the merge. The same
  holds for a stopped cherry-pick and revert.
- **safegit:** `commit`, `--amend`, `--reword`, `safegit mv` and `undo` all
  refuse at exit 5 (`CoordinationBusy`) while git reports a merge, cherry-pick,
  revert, rebase or `am` in progress, and so do the commands that COMPUTE an
  operation — `merge`, `cherry-pick`, `revert` and `pull`, in their own form and
  in the forwarded `-n`/`--no-commit` form, which hands git the same computation
  — before they compute anything, and for `pull` before its fetch. Those need
  the check made rather
  than inherited: raw git refuses to start one operation over another, but they
  compute with `git <verb> --no-commit`, and git's refusal does not reach that
  form uniformly — `git merge --no-ff --no-commit` over a parked revert reports
  "Automatic merge went well" and exits 0, though it does refuse over a parked
  cherry-pick — so without it a pick ran over a parked revert and committed.
  `safegit rebase` refuses there too, on a NARROWER predicate: an in-flight state
  whose kind is not a REBASE. It computes nothing of safegit's — git replays and
  authors — but it runs over whatever state it finds, and over a parked revert on
  a clean tree it exits 0 and strands that revert's state files behind it,
  blocking every later commit. Scoping the predicate to the kind is what lets a
  rebase's own `--continue`, `--abort` and `--skip` through with no second
  exemption list: mid-rebase state reports the rebase kind.
  The other verbs' state-control forms (`--abort`, `--quit`) and the three
  conclusion commands are exempt, because they are the way out of the very state
  being refused over. The refusal names the way out —
  `safegit merge-continue`, `cherry-pick-continue` or `revert-continue` where
  safegit owns the conclusion, `git rebase --continue` or `git am --continue`
  where it does not, each with the abandoning command beside it, all rendered
  from the one authority every in-flight refusal reads. The reason is structural:
  safegit's commit is pathspec-only and builds its tree from the parent commit,
  handing `commit-tree` a single parent, so run mid-merge it would silently drop
  the operation's second parent and every path the pathspec does not name. The
  check lives in the commit pipeline rather than in the command handlers, so
  every route into a commit is covered by it, including the submodule auto-bump,
  which arrives through a safegit spawned in the parent repository. A caller may
  declare itself the conclusion of a named operation, and the declaration is
  checked rather than trusted: naming an operation other than the one in flight
  is a refusal, and so is naming one when nothing is in flight. A state file
  safegit cannot parse is a refusal too — an unreadable `MERGE_HEAD` is not
  evidence that no merge is in flight, and the permissive reading is the
  dangerous one.
- **Ruling:** ours — deliberate

---

## Where the working directory reaches

### The working directory scopes arguments, never the operation

- **git's idiom:** a large part of git's plumbing is scoped to the process
  working directory. Run from a subdirectory, `git ls-files` lists that
  subdirectory, and `git status` reports relative to it.
- **safegit:** every git subprocess safegit constructs runs with its working
  directory pinned to the repository root, so what safegit sees, protects and
  rewrites does not depend on where it was invoked. Paths a **caller typed**
  are unaffected: they are resolved against the invoking shell's directory
  before any git call is built. The exemptions are enumerated in code, in one
  table (`internal/gitexec/exemptions.go`), each row naming its kind and its
  reason; an identifier the table does not declare cannot escape the pin at all. A site given a **git directory and work tree as its own
  arguments** — submodule enumeration, a cross-repository scan, the parent-pointer
  read — has no operator directory left for a pin to correct. A **passthrough**
  forwards the operator's own argv to git in the operator's own directory,
  because a pathspec they typed has to mean what it meant there. And an
  invocation the **effects handle** starts, so that `--dry-run` can record it
  instead of performing it — the push, the commit pipeline's ref update, a
  recorded history rewrite — names refs and object names only, with nothing in it
  that a directory resolves.
- **Ruling:** ours — deliberate

### `--resolve` paths are repository-relative while commit positionals are not

- **git's idiom:** everything a git command takes on the command line is
  resolved against the current directory.
- **safegit:** the asymmetry is deliberate and follows what the argument *is*. A
  commit positional is a filesystem argument — a path a person tab-completed at
  a shell prompt — so it resolves against the invoking directory. A conclusion's
  `--resolve 'path=theirs'` is a **declaration about an index entry**: it names a
  path git itself listed as unmerged, in git's own repository-relative
  spelling, so canonicalizing it against the shell would be answering a question
  nobody asked. The refusal for an unresolved path says so in as many words.
  `scrub file` splits the same way inside one command: its target argument is
  repository-relative, its `--replace-with` is a file on your disk.
- **Ruling:** ours — deliberate

---

## Concluding an operation git stopped

### safegit concludes a stopped merge, cherry-pick and revert; `git <verb> --continue` is refused

- **git's idiom:** `git merge --continue` (or plain `git commit`) finishes a
  merge git stopped on. The same for `cherry-pick` and `revert`.
- **safegit:** `safegit merge --continue`, `safegit cherry-pick --continue` and
  `safegit revert --continue` are refused at exit 5 and name safegit's own
  command — `merge-continue`, `cherry-pick-continue`, `revert-continue`. Letting
  git's `--continue` through would commit the whole index with none of safegit's
  trailers and none of its commit-time machinery, and whether a conclusion is
  safegit's or git's must not depend on whether the merge happened to change a
  file.

  The refusal is **total**, and the two halves of it read differently. With an
  operation in flight it is the one above, naming safegit's own conclusion
  command. With **nothing** in flight — where git would answer "no cherry-pick
  in progress" — the argv still never reaches git: safegit's git-execution
  boundary refuses any invocation whose shape would let git author a commit,
  and a forwarded `--continue` is exactly that shape. The command line was
  going to fail either way; what changes is that the refusal is safegit's and
  says why.
- **Ruling:** ours — deliberate

### `safegit rebase` is the one door where git authors the commits

- **git's idiom:** git replays the commits and authors every one of them, and
  `git rebase --continue` / `git am --continue` finish what stopped.
- **safegit:** every other commit made through safegit is safegit's own —
  trailered, `commit-msg`-hooked, oplog-recorded, undoable. A rebase is the
  single declared exception, and it is UNIFORM rather than conditional: git
  performs the replay and authors every replayed commit, always, in every shape
  of `safegit rebase` there is. There is no run-time split to reason about and
  no announcement to read; what safegit adds is the worktree operation lock
  (held for the whole rebase, an interactive one's editor session included), the
  uncommitted-work check, a refusal over an in-flight state that is NOT a rebase
  (whose predicate and whose reason are spelled in "`safegit commit` refuses
  while an operation is in flight", the entry that owns the in-flight refusal
  across every command that makes one), a refusal on an UNBORN branch, where
  there are no commits to replay (see "The friendly unborn pre-flight
  refusals"), the argv allowlist and the oplog entry.

  The exception is enforced structurally rather than maintained by convention.
  safegit's git-execution boundary refuses any invocation whose verb-and-flag
  shape would let git create a commit, unless the call site carries a DECLARED
  DOOR — and the rebase passthrough is the only door in the table. That is what
  makes "git never authors a commit through safegit, except `safegit rebase`" a
  property of the code rather than a promise about it.

  A rebase's and a mailbox application's `--continue` stay git's for the same
  reason: safegit has no verb that finishes either, so refusing one would leave
  the operator with nothing to run. The refusal above is scoped to the three
  operations safegit actually concludes, read from one shared authority rather
  than a hand-written list.

  Replacing this with a native, pipeline-authored non-interactive rebase is
  deliberately deferred work rather than an accepted permanent state; it is
  tracked in `todo/pipeline-authored-rebase.md`.
- **Ruling:** git-like, and the only one of its kind — deliberate

### Every commit made through safegit is safegit's; the ones it cannot write, it refuses

- **git's idiom:** git writes the concluding commit itself, whatever the shape
  of the operation.
- **safegit:** safegit builds the commit — git's message draft with its comment
  block stripped, the repository's `commit-msg` hook run, safegit's trailers
  injected, the operation's whole state-file set removed, the result reversible
  with `safegit undo`. That holds for a conclusion (`merge-continue`,
  `cherry-pick-continue`, `revert-continue`) and equally for `safegit merge`,
  `cherry-pick`, `revert` and `pull`, which use git only to COMPUTE a result
  with `--no-commit` and then commit it through the same pipeline.

  There used to be a second answer for the shapes safegit could not write
  itself. A QUEUED sequence — `git cherry-pick a b` — was DELEGATED: safegit
  staged the declared resolutions into a copy of the index and handed the rest
  of the queue to git's own `--continue`, and the resulting commits were git's,
  with no trailers, no `commit-msg` handling and no way to undo them. It said so
  on stderr every time. That whole authorship class is **deleted**. A commit
  made under a safegit command name is safegit's, or it is not made.

  So a queue is now REFUSED instead. `safegit cherry-pick` and `safegit revert`
  take one commit, so a `.git/sequencer` directory can only have come from raw
  git, and the conclusion commands refuse it naming git's own `--continue` and
  `--abort` — finish with git what git started. The same rule reaches two merge
  shapes safegit's own merge cannot produce: an octopus `MERGE_HEAD` (more sides
  than every check over a merge is written for) and a content conflict with no
  `AUTO_MERGE` file, which is the signature of a non-default strategy and leaves
  the overwrite refusal with nothing to compare a working-tree file against.

  What an operator loses is real and is stated plainly: safegit will not help
  you finish a multi-commit sequence or an octopus. What they gain is that no
  commit bearing a safegit command name is ever one safegit did not write, check
  and record.
- **Ruling:** ours — deliberate

### `safegit revert` and `safegit cherry-pick` are split at git's own seam

- **git's idiom:** `git revert <sha>` and `git cherry-pick <sha>` compute and
  commit in one step, authoring the commit themselves, and take any number of
  revisions.
- **safegit:** the operation is split where git already splits it. `git <verb>
  --no-commit` computes and stages the patch, and safegit's conclusion engine
  commits it — trailers, the `commit-msg` hook, the state files cleaned up, one
  oplog entry, and `safegit undo` reverses it. A conflicted compute parks
  exactly as it does under plain git and is finished with the matching
  `-continue` command, which reaches the same engine, so the clean and the
  conflicted path produce the same kind of commit.

  There is **no second arm**. An earlier design fell back to an ordinary
  passthrough for anything the split could not honor, which meant an option
  nobody had considered quietly changed who authored the commit. Now the command
  line is validated against an allowlist first, and an option outside it is
  refused (see [The subset boundary](#the-subset-boundary)).

  One detail is safegit's own rather than git's: `git cherry-pick --no-commit`
  writes no `CHERRY_PICK_HEAD` on either the clean or the conflicted path, while
  `git revert --no-commit` does write `REVERT_HEAD` on both. The conclusion
  machinery, the author preservation, git's own status output and
  `git cherry-pick --abort` all key on that file, so safegit writes it itself
  after the compute, in git's own one-line format, uniformly. The parked state
  then looks exactly like the one a conflicted pick leaves under plain git.
- **Ruling:** ours — deliberate

### Resolution keywords write the working tree

- **git's idiom:** `git checkout --ours <path>` replaces the file on disk with
  the stage-2 content, and `git rm <path>` deletes the file from disk as well as
  from the index.
- **safegit:** `--resolve path=ours` and `--resolve path=theirs` write the chosen
  content into the working tree, and `--resolve path=delete` removes the file
  from disk. The reasoning was that an operator who types `ours` means what git
  means by it, and that resolving only the index would leave the
  marker-carrying file sitting in the working tree, one `safegit commit -- x`
  away from committing the very conflict markers the conclusion just resolved
  away. The alternative reading — that a conclusion declares what goes into the
  **commit** and should not touch the operator's files — was weighed and not
  taken. Both the preview and the report say, path by path, which files were
  written and which were deleted.

  What settled it is that the write is no longer unguarded. A materialization
  that would destroy content matching no side of the conflict is now REFUSED
  rather than performed (see [A working-tree write that would destroy a hand
  edit is refused](#a-working-tree-write-that-would-destroy-a-hand-edit-is-refused)),
  which removes the case the alternative reading existed to protect: the file
  git's own `checkout --ours` would have overwritten without a word is exactly
  the file safegit stops for. Writing the tree keeps the keyword meaning what an
  operator means by it, and the protection is now stronger than either reading
  would have been on its own.
- **Ruling:** git-like — deliberate

### A working-tree write that would destroy a hand edit is refused

- **git's idiom:** `git checkout --ours <path>` and `git rm <path>` do what they
  say to whatever is on disk. If the file holds an hour of hand-resolving that
  was never staged, never committed and never stashed, it is replaced or removed
  without a word — the content is in no object and nothing can bring it back.
- **safegit:** a conclusion looks at the file before it writes over it. Per
  declared path, the ACCEPTED SET is the conflict's index stages plus the blob
  git itself wrote into the working tree, read verbatim out of `AUTO_MERGE`
  (never reconstructed — a rename-mediated conflict's marker labels carry the
  path and no reproduction recovers them). A file matching none of them is a
  hand edit, and the conclusion refuses at exit 27 with nothing committed and
  the operation still in flight, naming the file and the resolution that keeps
  the edit (`=worktree`). It covers `delete` and an absent stage too, both of
  which remove the file rather than write one.
  `--discard-unmatched-worktree` elects the destruction and names each file it
  takes; it is the only way to say so, which is what makes it a consent flag
  rather than an escape hatch.
- **The check is TOTAL, and that was chosen over a degraded one.** There is no
  skip arm and no declined-check fallback. Conflict kinds with no `AUTO_MERGE`
  emission — a delete-resolved path, an absent stage, a delete/modify, a binary
  file, a merge-driver path — are stages-only by NATURE, so what git left on
  disk for those IS one of the stage blobs and the check is complete there too.
  The one shape that would have needed a skip — a content conflict git recorded
  with no `AUTO_MERGE` at all — is refused at the front door instead: safegit's
  own merge cannot compute one (strategy selection is refused) and
  `merge-continue` refuses one raw git produced. A degraded check that silently
  did less on some paths was the alternative, and refusing the input was chosen
  over it.
- Every path that MATERIALIZES a declaration goes through it, including the
  re-run of a conclusion whose commit already stands: that path writes the
  working tree from the standing commit's own tree, which is a write like any
  other, so it is checked like any other.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### Every conflicted path must be declared, and nothing else may be

- **git's idiom:** you resolve however you like — edit the file, `git add` it,
  `git checkout --ours`, a merge tool — and `git commit` accepts the result. git
  refuses only if unmerged entries remain.
- **safegit:** a conclusion names each conflicted path with `--resolve` (or in a
  `--resolve-file`), and the declaration must match the conflict exactly: a
  conflicted path with no resolution is a refusal, and a resolution naming a
  path that is not conflicted is a refusal too. Both list the offending paths,
  and the missing-path listing prints, per path, what each keyword concretely
  resolves to for **this** operation — which is where the `theirs`-on-a-revert
  confusion is addressed, at the moment the choice is made rather than in help
  text. Exit code 17. A path the operator already resolved with `git add` is no
  longer unmerged and needs no declaration; it is still content-checked below.
- **Ruling:** ours — deliberate

### Conflict markers are verified before the commit exists

- **git's idiom:** git verifies nothing. `git commit` will happily record a file
  full of `<<<<<<<` markers.
- **safegit:** the content a conclusion is about to commit is checked for
  complete conflict blocks, and a surviving block is a refusal at exit 18 naming
  the path and line. The check is differential — a block that some side of the
  conflict, or some base commit of the operation, already carried is attributed
  to that side rather than reported — so a repository whose real content holds
  marker-shaped lines (documentation about conflicts, a stored fixture) stays
  committable. It covers paths the operator staged themselves with `git add`,
  which is exactly where a forgotten marker hides. There is **no escape flag**:
  the one way past it is a `safegit-conflict-markers` attribute declared in
  `.gitattributes`, read from the first parent's tree so it must have been
  committed before the conflict, and every rejection prints the exact line that
  would grant it.
- **Ruling:** ours — deliberate

### A revert authors as the operator; a cherry-pick preserves the source author

- **git's idiom:** `git cherry-pick` keeps the original author and records you
  as committer; `git revert` records you as both, because undoing something is
  your decision and not the original author's.
- **safegit:** the same division, in both the conclusion commands and the
  restructured single-commit revert. It is git's division rather than a safegit
  policy, and the human report says which case it is — a preserved author is
  labeled as preserved, and a revert's author line says outright that a revert
  is your own change.
- **Ruling:** git-like — deliberate

### A lone `AUTO_MERGE` file is residue, not evidence

- **git's idiom:** git's own `rebase --continue` leaves an `AUTO_MERGE` behind
  when an operation finishes. Only an operation's own state files say one is in
  flight.
- **safegit:** a lone `AUTO_MERGE` with no operation state is never a refusal
  and never changes a verdict. When a conclusion command reports that there is
  nothing to conclude, it mentions the file informationally — an operator who
  just looked inside `.git` and saw it deserves to know why it does not count.
- **Ruling:** git-like — deliberate

### A conclusion does not teach rerere the resolution it just made

- **git's idiom:** with `rerere.enabled`, git records the conflict in
  `.git/MERGE_RR` when the operation stops, and when **git** commits the
  resolution it writes that resolution into `.git/rr-cache`, so the next time
  the same conflict appears git replays it automatically.
- **safegit:** a conclusion removes `MERGE_RR` along with the rest of the
  operation's state files — it owns the state of the operation it concluded —
  but it never records the resolution into `rr-cache`. Nothing safegit does is
  replayed on a later conflict, and an operator whose rerere has been learning
  from every merge will find that the ones concluded through safegit taught it
  nothing. This is a scope decision rather than an argument against rerere.
  Writing `rr-cache` means producing git's own resolution records from outside
  git, and safegit's conclusion engine does not do it. Two of the keywords would
  be thin things to record anyway — `ours` and `theirs` name an index stage
  rather than merged text — but `--resolve path=worktree` commits exactly what
  the operator hand-edited into the file, which is precisely the case git's own
  flow would have taught rerere. So the gap is real and this entry states it as
  a fact an operator has to know, not as a claim that there was nothing worth
  recording.
- **Ruling:** ours — deliberate

### The commit stands and the aftercare did not: one exit code says so

- **git's idiom:** git's verdict is about the operation. `git merge --continue`
  applies the autostash git set aside in `.git/MERGE_AUTOSTASH` and prints
  `Applied autostash.`; when the apply conflicts, git stores the stash commit on
  `refs/stash`, removes the file, says the changes are safe in the stash — and
  **exits 0**. The merge is finished, so as far as git is concerned the command
  succeeded. The same holds generally: once git has made the commit it was asked
  for, what happens afterwards rarely changes the exit code.
- **safegit:** everything after the ref update is AFTERCARE — removing the state
  files, syncing the index, writing the working tree, putting an autostash back,
  bumping a parent repository's gitlink — and a failure there gets its own exit
  code, **26**, whose meaning is exactly "the operation's ref move is real, and
  a step after it did not finish". Every pipeline author shares it: `commit`,
  `--amend`, `--reword`, `mv`, `undo`, `merge`, `cherry-pick`, `revert`, `pull`
  and the three conclusions, so a caller reads one number for one meaning rather
  than a vocabulary per command.

  It is not a silent partial success and not a bare number either. The run emits
  its machine envelope — the code is returned rather than exited through, so the
  document is written — and the payload names the created commit and lists what
  was left, including the autostash's own state. An autostash that will not apply is
  the case that first forced this: the commit stands, the stash is stored as
  `stash@{0}`, the message names it and the commands that reach it, and a script
  that read `0` would never have looked at the message. The exit code is the one
  channel a caller cannot ignore, and the payload is where the detail lives.
- **Ruling:** ours — deliberate

### A conclusion whose commit already stands finishes the cleanup, and commits nothing

- **git's idiom:** the state files ARE the operation. `git merge --continue`
  reads `MERGE_HEAD` and the index and concludes the merge they describe;
  nothing ties either to a commit that may already exist on the branch. A
  conclusion moves the ref before it removes the state files, so a process
  killed between the two leaves both — and the next `--continue` conclusion
  builds a second commit out of the same state, whose incoming parent is
  already an ancestor of its first.
- **safegit:** the re-run recognizes its own work. The op log's last entry for
  the branch names one of the ops that conclude this kind of operation and
  records the commit HEAD stands at — and that is then corroborated against the
  operation itself, differently per kind, because the tip alone lies:
  - a **merge** by parentage. HEAD's parents are the branch tip plus the
    `MERGE_HEAD` line, which is exactly the commit this conclusion would build.
  - a **cherry-pick or revert** by SOURCE. The pipeline records the sha it
    applied on the conclusion's own op-log entry, and recognition requires that
    logged source to equal the parked state's `Source`. Corroborating by the
    message the conclusion would write was tried first and was wrong: after any
    concluded pick the tip still IS the logged commit, so a second pick of a
    commit whose subject matched read as "already concluded", had its state
    removed and its work silently dropped. The source sha cannot collide that
    way.

  When they agree, nothing is committed: the index and working tree are put in
  step with the commit that is already there — derived from THAT commit's own
  tree for the conflicted paths, so no unmerged stage survives the claim that
  the two are in step — the overwrite refusal still runs over every path it
  would materialize, the state files go, the report names the commit, and the
  run exits on the aftercare's own terms. A declaration whose side's blob
  differs from what the standing commit holds is refused naming that commit
  rather than silently ignored.

  The window is not closed everywhere, and the limit is stated rather than
  glossed: the op-log entry is written just after the ref update, so a crash in
  that sliver leaves no entry — a merge is still caught by parentage, a
  cherry-pick or revert in it is not. A crash after the state files were removed
  is `safegit doctor`'s to report.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### An autostash is applied only when it belongs to the merge being concluded

- **git's idiom:** `.git/MERGE_AUTOSTASH` is a plain file holding an object
  name, and git checks nothing about that object. `git merge --continue` applies
  whatever it names to the working tree and then deletes the file — a stale one
  left by a merge that was abandoned or a conclusion that was killed, or one
  written by hand, is applied just the same.
- **safegit:** the conclusion asks whose stash it is first, and two facts have
  to agree: the stash commit's **first parent** is the tip this conclusion
  committed onto, and its **message** carries git's own autostash shape (`On
  <branch>: autostash`, which is what tells it from the `WIP on <branch>: ...`
  an ordinary `git stash` writes). A stash that fails either is neither applied
  nor deleted: the file stays exactly where it is, the commit it names is
  printed with the commands that reach it, and the conclusion exits the
  commit-stands code with the leftover named in its payload. Applying it would
  put somebody else's uncommitted work into files the merge never touched and
  then remove the only name that work has left. `safegit doctor` reports the
  same file as an orphan when no merge is in flight at all.
- **The limit, stated:** the two facts cannot separate a genuine abandoned
  autostash from this merge's own when the branch has **not moved** since — it
  carries git's own message shape and the same first parent, so it passes both
  halves and is applied. That state takes hand-mutilated repository state to
  reach: git's `merge --abort` and `rebase --abort` re-apply the autostash and
  remove the file, so it survives at an unmoved tip only where somebody deleted
  the operation's state files by hand. Whether to strengthen the key is part of
  what this entry awaits.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### A detached HEAD is refused, with the exact way back

- **git's idiom:** git is happy to commit, merge, cherry-pick and revert on a
  detached HEAD.
- **safegit:** every commit safegit makes is a compare-and-swap on a ref, so a
  detached HEAD has nothing to swap and is refused. A conclusion's refusal spells
  out the recovery — `git branch <name>` then `git symbolic-ref HEAD
  refs/heads/<name>` — and says explicitly that `git switch -c`, which is what
  most people reach for first, does **not** work here because git refuses to
  switch branches mid-operation. The two commands given move HEAD without
  touching the index or the working tree, so every state file, conflict stage
  and resolution already made survives. `safegit undo` refuses on a detached
  HEAD for the same reason.
- **Ruling:** ours — deliberate

---

## The subset boundary

The commands that take git's own vocabulary — `switch`, `merge`, `cherry-pick`,
`revert`, `pull`, `rebase`, `reset`, `bisect` — are where the subset law has to
be stated, because an operator can type anything git accepts and expect it to
arrive. Every entry in this section is one git capability safegit deliberately
does not have.

They are all open at the review, and overturning one is cheap: it means putting
a capability back into an allowlist, not writing new machinery. [What each
guarded command allows](#what-each-guarded-command-allows), at the end of the
section, is the other half of the picture — the refusals below only make sense
next to what is admitted.

### The forwarded command line is an allowlist, not a refusal list

- **git's idiom:** a wrapper forwards what it does not recognize. git itself
  accepts every option it documents, and an option a tool has not thought about
  reaches git and does what git does.
- **safegit:** each of these commands validates its argv against an explicit
  ALLOWLIST before the operation lock is taken, before any git runs, and before
  the repository is read at all. An option the command honors passes; anything
  else is refused, parser-shaped, at exit 2.

  The direction is the whole point. A refusal LIST answers "is this one of the
  things we already thought about and decided against", and an option nobody has
  thought about passes it, reaches git, and changes what git does while safegit's
  checks, its record of the operation and its report stay written for something
  else. An allowlist answers the other question — "is this one of the things
  safegit can honor" — which is the only honest one for a tool that promises a
  subset rather than a wrapper.

  Two refusals come out of one table. A capability the table NAMES carries its
  own reason, because "safegit's merge does not select a merge strategy, and here
  is why" is an answer an operator can act on while "unsupported option" is not.
  Everything else carries the subset law itself, and points here.

  One class of option is refused by neither of those answers: the framework's
  own flags — `--dry-run`, `--json`, `--quiet`, `--verbose`,
  `--approve-consequential` — written AFTER one of these command names. They are
  read anywhere in the command line up to that name, and after it argv is git's
  vocabulary, which does not include them. The refusal stands (forwarding one to
  git would be the accept-and-quietly-ignore shape this whole boundary exists to
  kill), but its reason is neither of the two above: safegit HAS the flag, on
  every command it has. So it names the route instead — write the flag before the
  command name — rather than citing a subset law that is not what refused it.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### Navigation is `safegit switch`, and there is no `safegit checkout`

- **git's idiom:** `git checkout` is the command everyone reaches for, and it is
  two commands wearing one name: it moves HEAD, and it restores files from a
  commit over the working tree. git's own answer to that overload was to add
  `switch` and `restore` without removing anything.
- **safegit:** safegit implements the navigation half, under git's own modern
  name for it, and `checkout` is not a safegit command at all. `safegit switch
  <branch>` moves onto a branch that exists; `safegit switch -c <new>` makes one
  where you are standing (switch's own spelling, which replaces checkout's
  `-b`). That is the whole surface.

  An operator's muscle memory will be wrong here, and deliberately so: typing
  `safegit checkout` gets an unknown-command error rather than a helpful alias,
  because an alias would carry the overload back in.
- **Ruling:** ours — deliberate

### File restoration is absent, not refused

- **git's idiom:** `git checkout -- <path>` (and its successor `git restore`)
  writes a commit's content over whatever the working tree holds, destroying
  uncommitted work with no record anywhere. The content was in no object, so
  nothing brings it back.
- **safegit:** there is no file mode on `switch` and no restore command, so the
  destructive shape is **inexpressible** as a safegit command line rather than
  refused by one. A pathspec after `safegit switch` is rejected with that
  reason.

  The distinction matters more than it looks. A refusal is a check, and a check
  can be conditioned, flagged past, or forgotten in a code path nobody tested. An
  absent command has none of those failure modes. In a worktree several sessions
  share, the work this shape destroys may not even be the operator's own, which
  is why it is the one capability removed rather than guarded.
- **Ruling:** ours — deliberate

### `switch` takes a branch name, and only a branch name

- **git's idiom:** `git switch` takes any commit-ish with `--detach`, `-C`
  force-recreates a branch that already exists, `--orphan` starts a history with
  no parent, and `--force`/`--discard-changes` and `--merge` exist to get past a
  dirty working tree.
- **safegit:** an argument that resolves to a commit but is not a branch is
  refused, and the refusal names both ways forward — make a branch there and
  switch to it, or use `git switch --detach` when a detached HEAD is deliberately
  what you want. Switching onto anything else DETACHES HEAD, and a detached HEAD
  is the state safegit's commit, conclusion and undo paths all refuse, since
  every commit safegit makes is a compare-and-swap on a ref. Note that
  `--detach` guards nothing here: the ARGUMENT is what detaches, so refusing the
  flag while accepting the argument would be a check that never fires — and the
  flag is refused too, for the operator who typed it meaning to be explicit.

  The other refusals, each with its own reason: `-C` re-points a branch that
  already exists at wherever you are standing, which is a ref move dressed as
  navigation and outside the compare-and-swap; `--orphan` is a decision about
  the repository rather than a step between branches;
  `--force`/`--discard-changes` and `--merge` are dead flags (see below).

  What is NOT refused is git's DWIM reading: a name that exists only on a remote
  still creates and lands on a local branch tracking it, because git resolves
  that before safegit's branch-ness question is even asked. An argument that
  resolves to nothing at all is left to git, so `safegit switch no-such-thing`
  exits with git's own verdict rather than a message safegit invented about
  branches.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### `switch -c` starts the new branch where you are standing

- **git's idiom:** `git switch -c <new> <start-point>` creates the branch at any
  commit-ish and moves onto it in one step, and `git checkout -b` does the same.
  The start-point is ordinary daily usage: branch off `origin/main` without
  going there first.
- **safegit:** `safegit switch -c` takes the new branch's name and NOTHING else.
  A start-point alongside it is refused at exit 2, and the refusal names the two
  commands that do the same work — `git branch <name> <start>`, then `safegit
  switch <name>`. The reason is what `switch` is here: the navigation half of
  git's `checkout`, one step from where you are standing to a branch, with `-c`
  minting that branch at HEAD. A start-point makes it a branch CREATION at a
  commit nobody navigated to, which is a ref write wearing navigation's spelling
  — and safegit's every ref write is a compare-and-swap it performs itself,
  never a side effect of moving HEAD. The oplog entry is the same argument from
  the other end: a switch records the positions HEAD moved BETWEEN, and the
  start-point form would record a jump that never happened.

  This is a capability safegit deliberately lacks rather than one it guards.
  Nothing about the start-point is unsafe in itself; it is simply not part of
  the one form this command implements, and git creates branches perfectly well.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### A flag whose whole job is to carry a dirty tree is dead here

- **git's idiom:** `git merge --autostash` stashes uncommitted changes, merges,
  and puts them back. `git switch --merge` carries local modifications across a
  branch change by merging them, and `--discard-changes` throws them away to make
  the switch possible.
- **safegit:** all three are refused by name, and the reason is the same: the
  coordination check refuses ANY dirty working tree before git runs. A clean tree
  has nothing to stash or carry, and a dirty tree never reaches git, so the flag
  could not fire even if it were forwarded. Accepting a flag that cannot do
  anything is the accept-and-quietly-ignore shape safegit refuses everywhere
  else.

  There is a second reason for the discarding forms specifically. In a worktree
  several sessions share, the uncommitted changes being carried or discarded may
  be another session's work, and safegit cannot tell whose they are. The refusal
  says to commit first, which is the one answer that is safe whoever made them.
- **Ruling:** ours — deliberate

### A merge has exactly one other side

- **git's idiom:** `git merge a b c` makes one octopus commit with four parents.
- **safegit:** exactly one other side, and an octopus is refused. A conclusion
  has one staged result to check and one message to write however many sides
  went into it, and every check safegit makes over a merge — the completeness
  check, the marker verification, the overwrite refusal, the crash-window
  parentage corroboration — is written against two. Supporting a shape those
  checks cannot reason about would mean a merge safegit committed and did not
  verify.

  The refusal reaches the conclusion too: a `MERGE_HEAD` carrying more than one
  line can now only have come from raw git, and `safegit merge-continue` refuses
  it naming git's own `--continue`. The argument may be a branch, a tag or an
  object name — there is no branch-ness requirement, because merging from a tag
  detaches nothing.
- **Ruling:** ours — deliberate

### A fetch that marked several branches is not a merge safegit will make

- **git's idiom:** `FETCH_HEAD` is git's one token that expands into SEVERAL
  heads. `git merge FETCH_HEAD` after a fetch that marked three branches for
  merging makes an octopus out of one argument.
- **safegit:** when the merge argument is spelled exactly `FETCH_HEAD`, safegit
  reads `.git/FETCH_HEAD` and refuses when more than one line is marked
  for-merge — up front, before the fast-forward decision, so both arms of the
  merge are covered, and `safegit pull` inherits it.

  It is cataloged separately from the octopus refusal because counting the
  revisions on the command line does not catch it: this is ONE token. Without
  the check it produced a many-parent commit through the front door, and on the
  fast-forward arm it silently took the first line and dropped the rest. An
  unreadable or absent `FETCH_HEAD` is left to git, which is the same convention
  every unresolvable revision follows here.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### One commit per cherry-pick, one per revert; ranges are refused

- **git's idiom:** `git cherry-pick a b c` and `git revert A..B` queue the
  commits in git's sequencer and apply them one after another, authoring each.
- **safegit:** each command applies ONE commit and authors the result itself.
  Several revisions on the command line are refused, and so are the range and
  revision-set spellings — `A..B`, `A...B`, a leading `^`, `^!`, `^@` — with the
  refusal naming the sequential form: run the command once per commit, in the
  order you want them applied.

  The refusal is on the OPERATORS rather than on how many arguments were typed,
  and that is not pedantry. `git cherry-pick A..B` hands the operation to git's
  sequencer — queue directory and all — even where the range holds a single
  commit, so a check that counted argv tokens would let exactly that command
  line through as "one commit" and produce a queue safegit cannot conclude.

  What an operator loses is the convenience of one command line; what they get
  is that every commit is separately checked, separately recorded and separately
  undoable, and that none of them is git's.
- **Ruling:** ours — deliberate

### Merge strategies and strategy options are refused

- **git's idiom:** `-s ort`, `-s resolve`, `-X ours`, `-X theirs` and the rest
  change how git computes and stages a merge, a pick or a revert.
- **safegit:** refused on `merge`, `cherry-pick` and `revert` alike. Everything
  safegit checks about the result — the completeness check over the conflicted
  paths, the differential marker verification, the overwrite refusal that reads
  `AUTO_MERGE` — is written against what the DEFAULT strategy stages, and a
  strategy those checks cannot read would be protected by nothing while still
  reporting as protected.

  It is not hypothetical: a pick computed with `-s resolve` parks a content
  conflict with no `AUTO_MERGE` at all, which is precisely the shape the
  overwrite refusal has nothing to compare against — and the same shape
  `merge-continue` refuses when raw git produces it. Refusing at the front door
  is what keeps that check total rather than degraded.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### `--squash` is refused

- **git's idiom:** `git merge --squash` stages a merge's whole result and leaves
  you to commit it as an ordinary commit, with none of the merge's parents
  recorded.
- **safegit:** refused. A squash produces a commit with a merge's CONTENT and
  none of its HISTORY, so nothing afterwards can tell that the branches were
  brought together, and `safegit merge` either records a merge commit or records
  nothing. An operator who wants the content without the history can stage it
  themselves and commit it with `safegit commit`, where it is what it looks like.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### Options that would hand the commit or the ref back to git are refused

- **git's idiom:** `--commit` is `merge`'s and `cherry-pick`'s default and can be
  spelled explicitly; `git cherry-pick --ff` lets git fast-forward the branch
  onto the picked commit outright.
- **safegit:** both are refused by name. The compute step is pinned to
  `--no-commit`, and git takes the LAST of a conflicting pair, so a `--commit`
  trailing safegit's own flag would hand the commit back to git — authored by
  git, with none of safegit's trailers, moving the ref outside the
  compare-and-swap. `--ff` on a pick does the same to the ref without even
  making a commit.

  `merge --commit` used to be accepted and quietly stripped from the forwarded
  argv, which is the shape this tool refuses everywhere else: an operator who
  asks for something gets it, or gets told they cannot have it. It is now
  refused by name in all three tables.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### Options that govern git's own commit step are refused, not ignored

- **git's idiom:** `-S`/`--gpg-sign` signs the commit git makes; `--no-verify`
  skips the hooks git's commit path runs; `--cleanup` governs how git strips the
  message at its commit time; `--allow-empty`, `--allow-empty-message`,
  `--keep-redundant-commits` and `--empty` govern what git commits when a pick
  produces nothing.
- **safegit:** the commit here is safegit's pipeline's, so none of git's commit
  path runs and every one of these would silently do nothing. They are refused
  rather than forwarded into a no-op:
  - safegit's pipeline does not sign, so a signature asked for here would simply
    not be on the result.
  - the pipeline runs the repository's `commit-msg` hook and has no flag that
    turns it off, so `--no-verify` would skip nothing.
  - the message the pipeline takes is git's draft with its comment block
    stripped, or the text passed to the matching `-continue` command's `-m`, so
    `--cleanup` has no step to govern.
  - the pipeline refuses a commit that changes nothing outright, and there is no
    flag here that turns that refusal off.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### `--skip` is refused

- **git's idiom:** `git cherry-pick --skip` and `git revert --skip` move past the
  commit that stopped a SEQUENCE and keep the rest of the queue going.
- **safegit:** refused, because there is no sequence to keep going: each command
  applies one commit. The refusal names what actually ends the state — `git
  cherry-pick --abort`, or concluding the revert you are in with `safegit
  revert-continue` and then `git revert --abort` — and then the sequential form
  for the commits you did want.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### The fast-forward-only refusal is safegit's, not git's

- **git's idiom:** `git merge --ff-only` refuses a non-fast-forward with git's
  own message and exit code 128.
- **safegit:** safegit decides fast-forward-ness itself, with a merge-base
  ancestry check, and raises its own refusal at its own general failure code
  rather than letting git raise one.

  The reason is a race rather than a preference. Letting git make the call means
  letting git MOVE the ref, and between safegit's check and git's run the two
  branches can stop being diverged — at which point git would fast-forward
  outside safegit's compare-and-swap, which is the one thing the whole commit
  design exists to prevent. The cost is that an operator scripting against git's
  128 sees a different number here.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### A parked merge stays parked, even when it could have fast-forwarded

- **git's idiom:** `git merge --no-commit` fast-forwards anyway when the merge is
  a fast-forward, because git takes the fast-forward before the flag is
  consulted. The flag means "do not commit the merge you computed", and a
  fast-forward computes nothing.
- **safegit:** `--no-commit` parks wherever a merge can be parked at all. The
  flag says the operator wants to look at the result before it becomes anything,
  and a fast-forward that moved the branch silently would deny exactly that. So
  the fast-forward arm is taken only where nothing on the command line asks for
  a commit to inspect, and the parked state is concluded with `safegit
  merge-continue` like any other.

  There is exactly one place a merge cannot be parked, and it is REFUSED rather
  than quietly fast-forwarded: an unborn branch. Parking is computed with
  `--no-ff` underneath, and an unborn head cannot take a non-fast-forward — so
  `--no-commit` there is refused before git runs, with the reason. See "The
  friendly unborn pre-flight refusals" below.
- **Ruling:** ours — deliberate

### The friendly unborn pre-flight refusals

- **git's idiom:** git lets each command meet an unborn branch on its own terms
  and answers about the mechanism that failed. `git merge --no-ff` says
  "Non-fast-forward commit does not make sense into an empty head", `git rebase`
  says "Could not resolve HEAD to a commit", `git bisect start` says "bad HEAD -
  strange symbolic ref". `git merge --no-commit` does not fail at all: it
  fast-forwards and exits 0, because git takes the fast-forward before the flag
  is consulted.
- **safegit:** an unborn branch — a repository between `git init` and its first
  commit, or one `safegit undo` of a root commit has emptied — is a supported
  state, not an edge case: `commit` roots, `switch` moves, `merge` and `pull`
  fast-forward, `cherry-pick` produces a root commit, `reset` works, and the
  dirty-tree check compares against the empty tree. The four forms that cannot
  be served there are refused BEFORE git runs, each naming the situation and the
  way forward rather than the mechanism that would have failed: `merge --no-ff`,
  `merge --no-commit`, `pull --merge-strategy no-ff` (the same request through
  the other command), `rebase` and `bisect start`. The exit is safegit's general
  code.

  Three of them replace a git error with a better-aimed one and end the same
  way. The `--no-commit` refusal is a real behavioral divergence: git would have
  succeeded. It follows from the parked-merge model above — safegit parks by
  computing with `--no-ff`, and an unborn head cannot take one — and the
  alternative would be a `--no-commit` that silently moves the branch in exactly
  the case the flag exists to prevent.

  A `revert` onto an unborn branch is not in this set and needs no refusal of
  its own: reverting onto nothing puts nothing back, so the standing
  empty-result refusal ("An empty commit is refused; an empty merge is not")
  fires, cleans up the state safegit parked, and says so.
- **Ruling:** mixed — deliberate

### A fast-forward is safegit's own ref move, and undo refuses it

- **git's idiom:** a fast-forward merge moves the branch ref and updates the
  index and working tree, in one step, inside git.
- **safegit:** safegit performs it itself: a compare-and-swap onto the incoming
  tip, then a read-tree that puts the index and working tree in step with it.
  The sync is not a tidy-up — a bare ref move leaves the INVERSE of the incoming
  diff staged — so it is part of the operation, and a failure there is the
  commit-stands family (exit 26) with the sync named in the residue.

  `safegit undo` REFUSES a fast-forward. The tip is a commit safegit did not
  create, and undo never rolls a branch back over one; the refusal names the
  skipped fast-forward rather than reversing it. The mechanism is worth stating,
  because the obvious one is not enough: any oplog entry carrying an `outcome`
  key is a record of what git did to the branch rather than a commit safegit
  authored — the pipeline never writes that key — so a one-commit fast-forward,
  which the ancestry check alone would have happily reversed, is excluded by
  construction.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### `reset` takes a commit; the pathspec form is refused

- **git's idiom:** `git reset <commit> -- <path>` resets one path's index entry,
  and `git reset -p` opens an interactive hunk session to do it selectively.
- **safegit:** `safegit reset` takes a COMMIT in one of five modes, and the
  pathspec form is refused in every spelling that gives it away — an explicit
  `-- <path>`, a second revision that names a path, a bare argument that
  resolves to no commit but does name one, and the `--pathspec-from-file`
  family.

  The pathspec form writes the SHARED index entry by entry, and safegit's whole
  design keeps out of that file: every commit stages into a temporary index of
  its own precisely so that concurrent sessions cannot stage over each other. A
  reset of one path would be the single exception, and what it did to a path
  another session staged would be invisible to everything safegit records. The
  refusal names the route that stays inside the design: reset the whole path and
  commit the hunks you want with `safegit commit --hunks`.

  `-p`/`--patch` is refused separately, as one instance of a rule that holds
  everywhere in safegit: there is no interactive mode anywhere. An argument that
  resolves to NEITHER a commit nor a path is deliberately left to git, so
  `safegit reset --hard no-such-ref` exits with git's own verdict on it.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### `rebase` is one upstream and a replay, and nothing else

- **git's idiom:** `git rebase` has two backends, replays merges with
  `--rebase-merges`, runs a command after each commit with `--exec`, rewrites
  from the root with `--root`, and takes `<upstream> <branch>` to switch
  branches before it starts.
- **safegit:** the door safegit declares is a replay onto an upstream, and the
  allowlist keeps the forms of that door and nothing more.
  - the **apply backend** (`--apply`, and its `--whitespace`/`-C` patch options)
    is refused: safegit's in-flight state reader, its conflict machinery and its
    refusals are all written against the merge backend, and the apply backend
    keeps its state under a directory `git am` shares, where safegit's readers
    would answer about the wrong operation.
  - `-x`/`--exec` is refused: a rebase through safegit is a replay and nothing
    else, and running an operator's command after every replayed commit is a
    second, unbounded operation inside one lock. Run it yourself when the rebase
    finishes.
  - `-r`/`--rebase-merges` is refused: it re-merges the sides of every merge
    commit, so the operation creates merges safegit never computed and cannot
    check. Rebase a linear range, or redo the merges with `safegit merge`.
  - `--root` is refused: rewriting every commit including the first is a history
    rewrite rather than a replay, and safegit's history-rewriting surface is
    `safegit scrub`, with its own lock, verification and journal.
  - `rebase <upstream> <branch>` is refused because it switches branches first,
    which is a navigation safegit makes you state: switch to the branch, then
    rebase it. A pathspec is refused too — a rebase replays whole commits, and
    there is no part of one it can replay.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### `pull --rebase` is refused, naming the two commands

- **git's idiom:** `git pull --rebase` is one command, and `pull.rebase` can make
  it the default.
- **safegit:** refused, and the refusal names the two steps: `git fetch
  <remote>`, then `safegit rebase <remote>/<branch>`. A pull's merge step is
  safegit's own — it authors the commit — while a rebase is git's replay from
  end to end, which is a different operation with a different door. Folding them
  into one flag would put two authorship models behind one command line, decided
  by a flag or, worse, by configuration.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### `bisect`'s subcommand vocabulary is its allowlist, and it takes no options

- **git's idiom:** `git bisect` has its own subcommand language plus options that
  rename its terms (`--term-old`, `--term-new`), change what it checks out
  (`--no-checkout`) and limit the walk.
- **safegit:** the subcommands safegit forwards are exactly the ones its git
  classification table declares, and one outside that vocabulary is refused. The
  point of reading them from the table rather than restating them is that the
  same declaration is what tells safegit which subcommands write the working
  tree and therefore need the uncommitted-work check — so what safegit ADMITS
  and what it KNOWS about what it admitted cannot drift apart.

  The option allowlist is deliberately EMPTY. Every option git's bisect takes
  renames its terms, changes what it checks out, or limits the walk, and none of
  them has been considered against safegit's guards.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### What each guarded command allows

The refusals above are only half the picture, and this table is the other half:
what each command ADMITS, which is what a reviewer needs in order to judge
whether a refusal is drawn in the right place. It is the same set the code's
allowlist tables hold.

| Command | Arguments | Options it honors |
|---|---|---|
| `switch` | one existing branch name, or nothing with `-c` | `-c`/`--create` |
| `merge` | exactly one commit-ish (branch, tag or object name) | `-m`/`--message`, `-F`/`--file`, `--no-edit`, `--ff`/`--no-ff`/`--ff-only`/`--no-commit` (safegit's own selectors; they never reach git), `--signoff`/`--no-signoff`, `--log`/`--no-log`, `--into-name`, `--stat`/`--no-stat`, `--allow-unrelated-histories`, `--rerere-autoupdate`/`--no-rerere-autoupdate`, `--abort`, `--quit` |
| `cherry-pick` | exactly one commit | `-x`, `-s`/`--signoff`, `--no-edit`, `-m`/`--mainline`, `-n`/`--no-commit`, `--rerere-autoupdate`/`--no-rerere-autoupdate`, `--abort`, `--quit` |
| `revert` | exactly one commit | `-s`/`--signoff`, `--no-edit`, `-m`/`--mainline`, `-n`/`--no-commit`, `--reference`, `--rerere-autoupdate`/`--no-rerere-autoupdate`, `--abort`, `--quit` |
| `pull` | an optional remote and branch | `--merge-strategy ff\|ff-only\|no-ff` (required, no default) |
| `rebase` | exactly one upstream | `--onto`, `-i`/`--interactive`, `--continue`/`--abort`/`--skip` (git's own, taking no argument), `--autostash` |
| `reset` | one commit | `--soft`, `--mixed`, `--hard`, `--merge`, `--keep` |
| `bisect` | one subcommand from the classification table's vocabulary | none |

What is admitted follows one rule on the four pipeline-authoring commands: an
option is allowed when it reaches only the COMPUTE step or the message draft git
writes there — the pipeline commits that draft — and refused when it would change
how git COMMITS, since nothing of git's commit path runs. `--signoff` is allowed
because git writes the trailer into the draft at the compute step; `--gpg-sign`
is not, because the pipeline is what commits here, and it does not sign.

One spelling in the code's tables is not in this one: `--continue` passes each
command's option allowlist and is then refused by name further in, since
concluding one of these operations is safegit's own job. It is listed under
[safegit concludes a stopped merge, cherry-pick and
revert](#safegit-concludes-a-stopped-merge-cherry-pick-and-revert-git-verb---continue-is-refused)
rather than here, because what an operator meets is a refusal.

---

## Moves

### Moves are recorded, never detected by similarity

- **git's idiom:** renames are **detected** after the fact and never recorded.
  `git log --follow`, `git diff -M` and every rename-aware view guess from
  content similarity, with a threshold that can be tuned and that changes the
  answer. Ask the same repository twice with different settings and you get
  different history.
- **safegit:** a move is a RECORD in the commit message, written when the commit
  is made, and no similarity score is consulted at any point. There is still no
  `diff -M`, no threshold, and no path staged that the caller did not name. What
  changed is who may state the record: a person, or safegit reading the commit's
  own delta.

  A **declared** record is `--moved 'old -> new'` (or `safegit mv`, which
  performs the move and mints the record in one invocation), checked against the
  repository — the old path tracked in the commit's parent and gone from disk,
  the new one present — and a declaration the repository contradicts is a hard
  error (exit 19), never a record.

  An **observed** record is one safegit minted from the commit's own RAW DELTA,
  and it carries the token `observed` after its id so a reader can tell a claim
  safegit derived from one a person made. The conditions are exact rather than
  probabilistic, which is what separates this from detection: the same blob
  leaves one path and arrives at another, both sides are regular files, the
  pairing is one-to-one, and the blob sits at exactly one path in each tree once
  the declared and `--untrack`ed paths are taken out. A blob with two candidates
  on either side mints nothing — there are no tie-breaks. A uniform directory
  move collapses to ONE subtree record; past the scattered-move cap the commit
  records none of them and says so on stderr, pointing at `--moved`; every
  candidate a fence declined rides the commit payload with its reason. Exactness
  cuts both ways, and the accepted consequence is stated rather than hedged:
  identical unique content — one file deleted, one added in the same commit —
  mints an observed move even when the two files are genuinely unrelated,
  because the conditions are conditions and the delta meets them, and the record
  a coincidence produces is retractable.

  A declaration outranks the reading for the paths it names, so a pair is never
  stated twice — and declaring a pair that an OBSERVED record on the commit
  being amended already carries SUPERSEDES it, writing that record's retraction
  and the new declaration together. A record is never edited, whatever its
  origin: correcting one is a retraction plus a new declaration in one commit,
  because editing the commit that carries it would rewrite history.

  The consequence a consumer repository feels: the records go onto the message
  BEFORE the `commit-msg` hook runs, so a hook that rejects unknown trailer keys
  or rewrites trailer blocks will now meet `Moved:` lines on commits nobody
  declared a move for.
- **Ruling:** ours — deliberate

### `safegit mv` commits the move and nothing else

- **git's idiom:** `git mv old new && git commit` records the rename carrying
  the blob the parent commit held. Uncommitted content changes at the moved path
  are staged by `git mv`, which is the one place the two differ in detail.
- **safegit:** each moved path is carried across as the **exact blob the parent
  tree held**, through index edits rather than by staging from disk. Uncommitted
  content changes at a moved path stay uncommitted and are a separate commit — a
  move is a move. This also makes a preview and a real run compute the same tree.
- **Ruling:** git-like — deliberate

### `safegit mv` refuses to move a file with uncommitted edits

- **git's idiom:** `git mv` moves whatever is on disk and STAGES it, so a file
  with uncommitted content changes arrives at the new path with those changes
  staged, and the next commit records the move and the edit together as one
  change.
- **safegit:** the commit is the move and nothing else — each path is carried
  across as the exact blob its parent commit held — so a moved path whose disk
  content has been edited would have those edits silently left behind,
  uncommitted, at a path that no longer exists in the tree. `safegit mv`
  therefore REFUSES rather than moving it (exit 19, the collected refusal it
  shares with a destination the world contradicts), and nothing moves.

  There is no override flag, because both legitimate intents already have a
  route and the refusal names them: the edits belong in their own commit, so
  commit the content first and then move it; or the edits should ride along with
  the move, so move the files on disk yourself and
  `safegit commit --moved 'old -> new' -- <new>`, which stages from disk and
  commits the content and the move together. A flag would be a third answer to a
  question that already has two.

  The check is filter-aware: the disk bytes are hashed with `--path <newpath>`
  so the repository's own attributes decide, and a checkout that converted line
  endings never false-refuses. Every dirty path is named — the human output
  aggregates them for a subtree move, and the complete list is never truncated —
  and a dry run refuses identically.
- **Ruling:** ours — deliberate

### A path the commit stops tracking never pairs into an observed move

- **git's idiom:** git has no move records, so nothing here has a counterpart.
  Rename DETECTION would happily pair a removed index entry with a same-content
  addition, since it reads trees and knows nothing about why a path left one.
- **safegit:** `safegit commit --untrack <path>` removes a path from the index
  and LEAVES THE FILE ON DISK. Its removal is real in the tree, so the same blob
  arriving at another path in that commit satisfies every pairing condition, and
  inference would mint a move record for it — true about the tree, while the old
  file is still sitting right there for anyone to open.

  safegit does not mint it. The paths named by `--untrack` join the suppressed
  set exactly as declared paths do, and each candidate they suppress is reported
  as a refused pair naming `--untrack`. What forces it is safegit's own
  consistency rather than a judgement about tastefulness: the DECLARED spelling
  already refuses that same claim — `--moved 'old -> new'` with the old path
  still on disk exits 19 — so inference stating it would have had safegit assert
  what safegit refuses to be told.

  The fence is scoped to the paths this command line named, not to a general
  on-disk check: a `--branch` commit's working tree is unrelated to the tree
  being built, and consulting it would be inference reading the wrong world.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### `safegit undo` of a move reverses the commit, not the files

- **git's idiom:** there is no `git undo`; the nearest thing, `git reset`, moves
  a ref and (with `--hard`) the working tree.
- **safegit:** `undo` moves a ref and has never touched the working tree, so
  undoing an `mv` leaves the files at their new paths. That is stated on stderr
  unconditionally, with both ways forward: move them back by hand, or re-commit
  them where they are with `safegit commit --moved`.
- **Ruling:** ours — deliberate

---

## Hooks

Two different hook systems meet here: git's own hooks, which safegit runs, and
safegit's pre-pre-push hooks, which are its own subsystem.

### git's commit hooks run, in git's order, once per operation

- **git's idiom:** `pre-commit`, then `commit-msg`, then `post-commit`. A
  `post-commit` hook's exit status is ignored. A non-executable hook is skipped.
  git resolves the hook directory through `core.hooksPath`, and a linked
  worktree uses the common git directory's.
- **safegit:** all of that is reproduced, including the directory resolution.
  What is safegit's own is the **retry semantics**: safegit's commit loop can run
  the staging and object-building phase several times when another session moves
  the ref underneath it, and a hook that ran per attempt would lint the same
  message twice, send the same notification twice, or let a rewriting
  `commit-msg` hook rewrite its own rewrite. So each hook runs at most once per
  operation and every later attempt reuses the answer — which is also what git
  does, for a different reason: one commit, one run.
- **Ruling:** mixed — deliberate

### `prepare-commit-msg` never runs, and `doctor` says so

- **git's idiom:** git runs `prepare-commit-msg` before opening the editor, and
  repositories use it to seed messages with branch names, issue keys and
  templates.
- **safegit:** safegit never opens an editor, so there is no message-preparation
  step for the hook to prepare, and it is not run. Because a hook that has always
  shaped messages simply not running is exactly the kind of silent surprise this
  tool is supposed to prevent, `safegit doctor` reports every executable hook in
  git's hook directory that safegit does not run, derived from the same list the
  commit pipeline executes so the two cannot drift. It is stated as a fact, not
  a fault: git still runs them for anyone using git directly.
- **Ruling:** ours — deliberate

### The `commit-msg` hook sees the operator's message; safegit's trailer goes on after

- **git's idiom:** the hook receives the whole message file and may rewrite it in
  place — that is how `Signed-off-by` and issue-key hooks work.
- **safegit:** the hook sees the caller's `-m` text plus the caller's own
  trailers and move records, and whatever it leaves in the file is adopted.
  safegit's session trailer is injected **afterwards**, so a hook can neither be
  confused by it nor strip it. The hook also runs *after* the refusals — nothing
  to commit, an argument that matched nothing — so a commit safegit is about to
  refuse never sets an operator's message hook running, which is the order git
  uses too.
- **Ruling:** ours — deliberate

### Each commit writes its own message file, never `.git/COMMIT_EDITMSG`

- **git's idiom:** the message the `commit-msg` hook edits is
  `.git/COMMIT_EDITMSG`, one file per repository.
- **safegit:** the file is per-invocation, in a scratch directory under
  `.git/safegit/tmp`, for exactly the reason the index is per-invocation: two
  safegit runs in one repository must not be able to hand each other's message to
  a hook.
- **Ruling:** ours — deliberate

### Hook output goes to stderr, whatever the hook wrote it to

- **git's idiom:** a hook's stdout goes to the terminal alongside git's.
- **safegit:** both a hook's stdout and its stderr are routed to safegit's
  stderr. safegit's stdout is a structured channel — exactly one JSON envelope in
  machine mode, a parseable commit line otherwise — and an operator-supplied
  script must not be able to write into it.
- **Ruling:** ours — deliberate

### Hooks the checkout provides are executed on push

- **git's idiom:** hooks live in `.git/hooks`, are never cloned, and git will not
  execute a script that arrived with a repository. That is a security stance, not
  an oversight.
- **safegit:** `safegit push` runs the scripts in the checkout's
  `.safegit/hooks`, so cloning a repository and pushing from that checkout runs
  the repository's own checks. Membership is the **directory**, not git's
  tracking: an uncommitted, even gitignored, executable file there runs like a
  committed one, because probing tracked-ness would make a hook an operator just
  wrote invisible to `hook list` while it kept running. The boundary is drawn at
  execution: these scripts run on push and on `safegit hook run` — an operator
  action with push intent — and never on clone, fetch, checkout or any
  inspection command, and `hook list` names every location with its origin
  precisely so the set can be read before anything is pushed.
- **Ruling:** ours — deliberate

### A non-executable hook is a refusal, in either store

- **git's idiom:** git ignores a hook it cannot execute, silently.
- **safegit:** a discovered pre-pre-push hook without its execute bit is a
  REFUSAL (exit 25), wherever it lives — the tool-owned live store or the
  store the checkout provides — and on `safegit push` and `safegit hook run`
  alike. `hook run` in particular does not report "no hooks to run": a command
  whose whole purpose is to say whether the checks pass must not exit 0 because
  a check was passed over.

  A hook is disabled by REMOVING it, never by dropping its mode. A lost
  executable bit — a filesystem without modes, a patch applied by a tool that
  drops them, an archive extracted without them — would otherwise turn into
  checks that quietly stopped running, which is the one failure this whole
  subsystem exists to prevent. The earlier split (skip the local one, refuse the
  tracked one) made the answer depend on which directory the file happened to be
  in, and the mode bit is no more trustworthy in one than the other.

  `safegit hook list` still LISTS such a hook, marked `NOT EXECUTABLE` — the
  listing is the diagnostic, and the hook an operator is asking about is usually
  the one that is not running — and `safegit doctor`'s `hook_perms` check
  reports the condition at error severity.
- **Ruling:** ours — deliberate

### Hooks left in the pre-migration location are a refusal, not a fallback

- **git's idiom:** git runs whatever is in its hook directory.
- **safegit:** safegit's hook store moved out of git's own `.git/hooks` into the
  tool-owned `.git/safegit/hooks`. A `pre-pre-push` file or `pre-pre-push.d/`
  directory still sitting in the old place stops every push and every `hook run`
  with exit 24 naming `safegit hook migrate`. Running them from there would make
  the executed set depend on where a file happened to be left; skipping them
  would stop an operator's checks in silence. Neither is acceptable, so
  discovery refuses.
- **Ruling:** ours — deliberate

---

## Output channels, exit codes and previews

### Passthrough commands exit with git's own exit code

- **git's idiom:** 1 for a conflicted merge, 128 or 129 for a fatal error, and a
  vocabulary per command.
- **safegit:** `switch`, `rebase`, `reset` and `bisect` forward the operator's
  arguments to git and report git's verdict verbatim once git has run. Those
  codes are deliberately absent from safegit's own registry: a registry row
  would claim ownership of a number safegit does not choose. A code from one of
  those commands is safegit's own only when the failure happened **before** git
  ran — the coordination check, an uninitialized repository, a rejected
  argument.

  `merge`, `cherry-pick`, `revert` and `pull` are only half in that set. git's
  verdict on the COMPUTE step still surfaces — a conflicted merge exits 1 the
  way git does, and the operation parks — but the commit is safegit's, so the
  pipeline's own codes reach these commands after git has already run: 17 and 18
  for a conclusion's refusals, 27 for an overwrite, 26 when the commit stands
  and its aftercare did not finish.
- **Ruling:** git-like where git decided, ours where safegit did — deliberate

### Every other exit code comes from one registry

- **git's idiom:** exit codes are per-command and largely undocumented; 1 means
  "something".
- **safegit:** every numeric code safegit chooses is a named constant in one
  registry with a doc comment saying what it means and which commands produce
  it, and the documentation table is generated from that registry so it cannot
  drift. Two carve-outs are declared: the passthrough codes above, and the
  `128 + signal` convention, both because they are numbers safegit does not
  choose. A new refusal ships with its registry entry or it is incomplete.
- **Ruling:** ours — deliberate

### stdout is a structured channel

- **git's idiom:** git writes progress, prompts, notices and results to whichever
  stream is convenient, and interleaves them freely.
- **safegit:** stdout carries the command's own result and nothing else — under
  `--json`, exactly one document, the framework's envelope, with the command's
  data as its payload and a declared JSON Schema validated at emission.
  Everything that is not the result goes to stderr: notices, warnings, prompts,
  a guarded child's output in machine mode, and git's own stdout during a
  push under `--json`. That holds for the guarded commands too — `switch`,
  `pull`, `merge`, `rebase`, `reset`, `bisect`, `cherry-pick` and `revert` stream
  git's output live at a terminal and CAPTURE it under `--json`, re-emitting both
  of the child's streams on stderr afterwards. Nothing is discarded, and two
  costs are stated rather than hidden: a machine-mode run of a long operation
  says nothing until it finishes, because the framework offers no tee and a
  second copy written by safegit would duplicate every line at a terminal; and
  the capture decodes as text, so a child writing non-UTF-8 bytes fails that
  decode and the run degrades to the general failure code instead of reporting
  git's own verdict. At a terminal, where the output is streamed rather than
  captured, the same command is unaffected.

  There is no JSON error OBJECT — safegit never writes a second, error-shaped
  document, because the envelope is the only document machine mode has. What a
  failure produces on stdout is one of three shapes, and a consumer has to
  handle all three: an envelope WITH a payload (exit 26, where the payload names
  the commit that stands and the aftercare that did not finish), an envelope
  with `payload: null` (a refusal that reached dispatch — a payload schema
  describes a performed operation, and there is no error-payload channel), or
  nothing at all where the path exits before dispatch. The human-readable reason
  is on stderr in all three.
- **Ruling:** ours — deliberate

### `--dry-run` is uniform, and refused where it would lie

- **git's idiom:** previews are per-command and inconsistent. `git add`, `git
  clean`, `git push` and `git merge` each have their own `--dry-run` or
  `-n`, with different meanings; most commands have none at all.
- **safegit:** `--dry-run` is available on every command and records each
  mutation instead of performing it. A commit preview really stages, writes the
  tree and builds the commit object — inside a throwaway object store, so nothing
  reaches the repository — and then records the ref update rather than making it.
  A preview creates nothing, not even safegit's own state directory on a
  first-ever run. Where an honest preview is impossible the flag is **refused
  with its reason** rather than quietly ignored: `hook run` refuses at
  registration time because a hook is an operator-supplied script whose effects
  cannot be known. (The other refusal this used to name — a queued conclusion —
  is gone with the delegation it stood in front of: a queue is refused outright
  now, preview or not.) Hooks are never run under a preview, and the preview says so in three places — the help
  text, its own stderr, and the payload — because a preview that silently omitted
  them would read exactly like one whose hooks passed.
- **Ruling:** ours — deliberate

---

## Safety and consent

### A dirty working tree refuses the guarded commands, untracked files included

- **git's idiom:** git tolerates a dirty tree for most of this. `git checkout
  <branch>`, `git merge`, `git rebase` and `git pull` carry uncommitted changes
  across and refuse only where the operation would actually overwrite one of
  them, and untracked files are none of their business at all — `git reset
  --hard` does not even look at an untracked file, let alone stop for one.
- **safegit:** `switch`, `pull`, `merge`, `rebase`, `cherry-pick`, `revert`,
  the working-tree-writing `reset` modes and the stepping `bisect` subcommands
  all run one coordination check before git is started, and **any** dirt refuses
  the command at exit 5 (`CoordinationBusy`), with the offending paths listed.
  Which forms are in that set is not restated per handler: it is DERIVED from
  internal/gitexec's classification table, the single authority over what a git
  invocation does, and an argv the table does not declare is refused rather than
  assumed harmless. Dirt is a diff of the working tree against `HEAD` — so
  a staged change counts too, and the shared `.git/index`, which a safegit commit
  deliberately leaves stale, is never consulted — **plus every untracked file
  that is not ignored**. On an UNBORN branch, where there is no `HEAD` to
  resolve, the comparison is against the EMPTY TREE instead: a repository with
  no commits holds exactly that, so a staged addition is reported there exactly
  as it is on a born branch. That substitution is what makes an unborn
  repository usable at all — `git diff HEAD` is fatal in one, so the check
  itself used to fail and every guarded command refused with git's "ambiguous
  argument 'HEAD'" before its own handler decided anything. Untracked files are in scope because of the premise the
  whole tool rests on: safegit cannot tell an operator's half-finished scratch
  file from a file a concurrent session is about to name in its own commit, and
  the branch-switching commands are exactly where the second one gets lost. When
  git already has an operation in flight the refusal changes shape rather than
  relaxing — the dirt is then the conflict itself, so committing it is not advice
  anyone can follow, and the message names the operation and the commands that
  conclude or abandon it instead.
- **Ruling:** ours — deliberate

### `reset` is refused when the tree is dirty, in exactly the modes that write to it

- **git's idiom:** `git reset --hard` is the command that throws uncommitted work
  away. That is its whole purpose, and it does it without asking and without
  keeping a copy. `--merge` and `--keep` overwrite working-tree files too, and
  stop only where the specific file they would overwrite has local changes.
- **safegit:** `safegit reset` forwards the operator's arguments to git
  unchanged, with one check in front: when the argv names a mode that writes
  working-tree files — `--hard`, `--merge` or `--keep` — the dirty-tree guard
  above runs first, and any dirt refuses the command at exit 5. `--soft`,
  `--mixed` and a bare `reset` are unguarded, because none of them touches the
  working tree. The pathspec form is not unguarded but ABSENT: it is refused
  outright (see [The subset boundary](#the-subset-boundary)). Which modes are
  which is not read here at all:
  the classification table declares reset's effects and the guard reads that one
  view, so the vocabulary is stated in one place rather than approximated at the
  call site. The **operation lock**, by contrast, is taken unconditionally for
  every reset, because every reset moves HEAD. What is deliberately not covered:
  a clean-tree `reset --hard <older>` still moves the branch back over whatever
  was in the way, with no ancestry check of the kind `undo` performs. The guard
  blunts the destroy-uncommitted-work edge, not the move-the-ref one.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

### No bare `--force`, anywhere

- **git's idiom:** `--force` and `-f` are everywhere — `git push -f`, `git add
  -f`, `git checkout -f`, `git clean -f` — and mean something different in each.
- **safegit:** there is no flag named `force`. Where forcing is a real operation
  it is spelled for what it forces (`--force-with-lease`,
  `--overwrite-remote-backup`, `--allow-public-remote`), and where a check exists
  there is no flag that turns it off at all: no `-f` for an ignored path, no
  escape from the conflict-marker verification, no way to skip a coordination
  check.
- **Ruling:** ours — deliberate

### Consent is per-condition, and `--json` never grants it

- **git's idiom:** git prompts rarely and mostly assumes consent from the
  command line.
- **safegit:** four commands declare themselves consequential — `scrub file`,
  `scrub match`, `scrub run`, `author rewrite` — and the framework obtains
  consent before dispatch; `--approve-consequential` answers that in advance, and
  without a terminal they refuse rather than hang. Three conditions the framework
  cannot see keep their own seam: `doctor --action uninstall` and
  `push --force-with-lease`, both answered by `--approve-consequential` because
  the condition is the flag the caller typed,
  and a backup to a remote that is public or whose visibility cannot be
  determined, answered **only** by `--allow-public-remote` because the visibility
  is discovered at run time and the caller may not have known. The blanket flag
  does not answer that one — "yes, run this command" is not "yes, to that
  remote". `--json` answers none of them and refuses instead, naming the flag
  that would consent.
- **Ruling:** ours — deliberate

### Prompts go to stderr, and `--quiet` never hides one

- **git's idiom:** prompts go wherever the terminal is.
- **safegit:** a prompt is written to stderr, always. stdout is a structured
  channel, so a question written there would interleave with the answer to a
  different one — and a prompt a quiet run hid would be a prompt that hangs.
- **Ruling:** ours — deliberate

### A push declares which refs it pushes

- **git's idiom:** `git push` with no refspec consults `push.default`, whose
  value has changed across git versions and whose effect an operator often does
  not know.
- **safegit:** `--refs head|branches|tags|both` is required and closed over those
  four choices. There is no default, and no negatable boolean that could elect
  nothing — an earlier design had `--no-only-tags` silently pushing HEAD.
- **Ruling:** ours — deliberate

### A pull declares its merge strategy

- **git's idiom:** `git pull` decides between a merge, a rebase and a
  fast-forward from `pull.rebase`, `pull.ff` and branch configuration.
- **safegit:** `--merge-strategy ff|ff-only|no-ff` is required with no default,
  so a pull never depends on the repository's configuration to decide whether it
  may create a merge commit.
- **Ruling:** ours — deliberate

---

## Publishing

### Leases are pinned per ref to a SHA safegit observed

- **git's idiom:** bare `--force-with-lease` compares the remote ref against the
  **remote-tracking** ref for it. Tags have no remote-tracking refs at all, so
  git zeroes the expectation and refuses to move any tag the remote already
  carries.
- **safegit:** `--force-with-lease` builds one expectation per ref, pinned to the
  SHA safegit itself observed a moment earlier, and to git's empty expectation —
  "this ref must not exist" — for a ref the remote does not have yet. That makes
  it work for tags, which the bare form cannot do, and it makes an unreadable
  remote a hard error rather than an answer: an unreadable remote reported as
  "absent" would assert that a ref which plainly exists does not.
- **Ruling:** ours — deliberate

### Every multi-ref push is atomic

- **git's idiom:** `git push` is not atomic unless you pass `--atomic`; one
  refused ref leaves the others published.
- **safegit:** `--atomic` goes on every multi-ref push, forced or not, and the
  payload reports it from the same predicate that put it on the command line, so
  the argv and the machine document cannot disagree about what the push was.
- **Ruling:** ours — deliberate

### A push retries a transport failure, never a verdict

- **git's idiom:** `git push` fails once and stops.
- **safegit:** a transport failure is retried with backoff. A **verdict** never
  is: a lease rejection is terminal (exit 41), because retrying would re-observe
  the other session's ref, pin the lease to it, and quietly do the overwriting
  the lease just prevented. Between attempts safegit re-reads the remote, and a
  local ref that moved to a SHA the pre-pre-push hooks never saw stops the push
  rather than publishing un-validated content — the hooks are not re-run
  mid-retry.
- **Ruling:** ours — deliberate

### Backups live in a tool-owned namespace and are ancestry-checked

- **git's idiom:** you back up work by pushing a branch, which puts it in
  `refs/heads/*` where it competes with everyone else's branches.
- **safegit:** `safegit backup backup` keeps one slot per branch at
  `refs/backups/<branch>`, never touching `refs/heads/*`. The slot is fetched and
  checked first — a slot holding commits your history does not contain is a hard
  error (exit 22) rather than an overwrite — and the push is pinned with a lease
  to the SHA just observed. It passes `--no-verify` because the namespace is
  tool-owned, and `--dry-run` previews entirely from local state with no network
  contact at all.
- **Ruling:** ours — deliberate

---

## Rewriting history, undo, and searching

### A rewrite range must be an ancestor of HEAD

- **git's idiom:** `git filter-branch` and `git rebase --onto` take whatever
  revision you name and do something with it.
- **safegit:** `--from <sha>` must resolve **and** be an ancestor of HEAD, or the
  scrub is refused before anything is read. A submodule gets the same guarantee
  in whichever of the two ways the command's own shape allows, and neither one
  may quietly widen the range:
  - **`scrub file` on a path inside a submodule** runs the whole scrub in that
    submodule, so `--from` is read as a commit of the **submodule's** history and
    checked there. A parent-repository hash is refused outright, in as many
    words, rather than resolved by accident — the existence check is
    `rev-parse <sha>^{commit}`, because bare `rev-parse` echoes any 40-hex string
    back unchanged and a parent hash would otherwise "resolve" and fail
    confusingly one step later.
  - **`scrub match`** rewrites the parent and its submodules in one pass, so
    `--from` names a **parent** commit and is mapped into each submodule through
    the gitlink that boundary commit records: the boundary read in the
    submodule's own terms. The mapped commit gets the same `^{commit}` existence
    check and the same ancestry check against the submodule's HEAD, and every way
    the mapping can fail — a boundary commit that records no gitlink for that
    submodule, a gitlink the submodule's object store does not hold, a mapped
    commit that is not an ancestor of its HEAD — is a hard error naming the fix.

  Both used to fall back to rewriting the submodule's **entire** history when the
  boundary could not be honored, so a bounded request quietly became an unbounded
  rewrite of another repository and the operator was told only how many commits
  were rewritten. Widening a range is now a refusal; `--entire-history` is the
  only way to ask for one.
- **Ruling:** ours — deliberate

### A rewrite is verified before any ref moves

- **git's idiom:** `filter-branch` rewrites and moves the refs; verifying that it
  did what you wanted is your problem afterwards.
- **safegit:** the rewritten commits exist as unreachable objects first, and a
  verification pass runs before a single ref moves: a commit that changed a path
  no operation asked to change, a declared change that is missing, scrubbed
  content surviving in the rewritten trees, a named file that appears in no
  commit at all — each refuses at exit 30 with **nothing moved**, no ref, no tag,
  no journal record, and the command re-runnable. A separate code (31) says the
  opposite thing: the rewrite stands and something after it left residue. Every
  rewrite persists its old-to-new commit map to a crash-safe journal so the
  damage a rewrite does to hash-referencing files elsewhere can be repaired.
- **Ruling:** ours — deliberate

### `undo` refuses to move a branch past a commit safegit did not create

- **git's idiom:** `git reset --hard <older>` moves the branch back over anything
  in the way, without comment.
- **safegit:** `undo` computes its target from its own operation log, then checks
  the branch: every commit the branch would **lose** — the first-parent walk from
  the rollback target to where the ref actually stands — must be one the log says
  this undo is reversing. A plain `git commit`, a commit a rebase replayed, or a
  fast-forward safegit recorded but did not author is a hard error naming it,
  not a commit quietly dropped out of history. The compare-and-swap alone could not
  answer this: it pins the newest recorded tip and therefore sees only a foreign
  commit sitting on top. The refusal also reaches `--dry-run`, so a preview
  cannot announce a rollback the real run would refuse. Undo is session-scoped by
  default, and where a foreign commit turns out to be another safegit session's,
  the refusal says which flag widens the scope.
- **Ruling:** ours — deliberate

### `scan` reads what git's own search cannot reach

- **git's idiom:** `git grep` searches a tree, `git log -S` searches reachable
  history. Neither sees an unreachable object, and neither looks inside `.git`.
- **safegit:** `safegit scan` searches every object in the store — reachable or
  not, reporting which — plus commit messages, tag annotations, trailers, and
  files inside git's own state: the config (where credentials hide in remote
  URLs), `COMMIT_EDITMSG`, the hook directories, safegit's own state directory.
  Each match carries a marker saying which coordinate system its path is in, so a
  reader never has to guess whether a path is repository-relative or
  git-directory-relative.
- **Ruling:** ours — deliberate
