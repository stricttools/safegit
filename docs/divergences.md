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
> - [`--ff-only` is refused by safegit, with safegit's own exit
>   code](#ff-only-is-refused-by-safegit-with-safegits-own-exit-code)
> - [`--no-commit` parks a merge that git would have
>   fast-forwarded](#no-commit-parks-a-merge-that-git-would-have-fast-forwarded)
> - [A fast-forward is safegit's own ref move, and `undo` refuses
>   it](#a-fast-forward-is-safegits-own-ref-move-and-undo-refuses-it)
> - [`FETCH_HEAD` is refused when the fetch marked more than one
>   branch](#fetch_head-is-refused-when-the-fetch-marked-more-than-one-branch)
> - [An `--untrack`ed path never pairs into an observed
>   move](#an-untracked-path-never-pairs-into-an-observed-move)
> - and every entry under [The subset boundary](#the-subset-boundary), which is
>   where the allowlist verdicts live.
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
  already wrote. Hunk selection is declared as data: `--hunks 'path:1,3'`. The
  consequence is that safegit is fully usable from a script or an agent with no
  terminal, and that a `prepare-commit-msg` hook has nothing to prepare (see
  below).
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
  at all. A cherry-pick or revert conclusion that produces nothing gets its own
  refusal naming `git <verb> --skip` and `git <verb> --abort`, because those
  commands have no `--allow-empty` to point at.
- **Ruling:** git-like — deliberate

### `safegit commit` refuses while an operation is in flight

- **git's idiom:** `git commit` is how you *finish* a stopped merge. git leaves
  `MERGE_HEAD` in place, you resolve the conflict, and the next `git commit`
  picks the second parent up out of that file and records the merge. The same
  holds for a stopped cherry-pick and revert.
- **safegit:** `commit`, `--amend`, `--reword`, `safegit mv` and `undo` all
  refuse at exit 5 (`CoordinationBusy`) while git reports a merge, cherry-pick,
  revert, rebase or `am` in progress, and the refusal names the way out —
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
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

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
  uncommitted-work check, the argv allowlist and the oplog entry.

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
  was left, including the autostash's own state. The unappliable autostash is
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
  candidate a fence declined rides the commit payload with its reason.

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
  cannot be known, and a queued conclusion refuses per invocation because
  concluding a queue means running git's `--continue`, which commits. Hooks are
  never run under a preview, and the preview says so in three places — the help
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
  that is not ignored**. Untracked files are in scope because of the premise the
  whole tool rests on: safegit cannot tell an operator's half-finished scratch
  file from a file a concurrent session is about to name in its own commit, and
  the branch-switching commands are exactly where the second one gets lost. When
  git already has an operation in flight the refusal changes shape rather than
  relaxing — the dirt is then the conflict itself, so committing it is not advice
  anyone can follow, and the message names the operation and the commands that
  conclude or abandon it instead.
- **Ruling:** ours — **provisional, newly cataloged, awaiting review**

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
  outright (see [The subset boundary](#the-subset-boundary)). Which modes are which is not read here at all:
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
  this undo is reversing. A plain `git commit`, a passthrough cherry-pick or a
  git-authored queued conclusion in the way is a hard error naming it, not a
  commit quietly dropped out of history. The compare-and-swap alone could not
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
