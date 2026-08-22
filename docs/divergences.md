# Where safegit follows git, and where it deliberately does not

safegit wraps git, so every command it offers stands next to something git
already does. Most of the time the two agree. This document is the catalog of
the places where they do not, or where they only appear to.

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
review** for one that has not yet been confirmed and may be overturned.

> **One entry is provisional right now:** [Resolution keywords write the working
> tree](#resolution-keywords-write-the-working-tree). It went git-like, and it
> is the one ruling in this document that is explicitly still open.

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
  that contributed nothing, not just the first. Exit code 10
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

### A symlink whose target leaves the repository is committed, with a notice

- **git's idiom:** git records the link text and says nothing.
- **safegit:** the same object is committed — refusing would make safegit
  stricter than git for no safety it can actually provide — but one line goes to
  stderr saying that the link will not resolve in anyone else's checkout.
- **Ruling:** mixed (the commit is git's behavior, the notice is safegit's) —
  deliberate

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
- **safegit:** every commit-family command in a submodule refuses **before**
  anything is written unless the parent repository's safegit config has
  explicitly answered whether the gitlink should be bumped automatically
  (`commit.autoBumpParent`, which has no default). An unanswered question is a
  hard error, not a guess in either direction.
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
  before any git call is built. The exemption is narrow and declared — a
  passthrough forwards the operator's own argv to git in the operator's own
  directory, because a pathspec they typed has to mean what it meant there.
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
- **Ruling:** ours — deliberate

### A rebase's and a mailbox application's `--continue` stay git's

- **git's idiom:** `git rebase --continue`, `git am --continue`.
- **safegit:** untouched, and passed straight through. safegit has no verb that
  finishes a rebase, so refusing one would leave the operator with nothing to
  run. The refusal above is scoped to the three operations safegit actually
  concludes, read from one shared authority rather than a hand-written list.
- **Ruling:** git-like — deliberate

### Conclusion commits are pipeline-authored; a queued sequence stays git-authored

- **git's idiom:** git writes the concluding commit itself.
- **safegit:** for a **single** merge, cherry-pick or revert, safegit builds the
  commit: git's message draft with its comment block stripped, the repository's
  `commit-msg` hook run, safegit's trailers injected, the operation's whole
  state-file set removed, and the result reversible with `safegit undo`. For a
  **queued** sequence — `git cherry-pick a b` — safegit stages the declared
  resolutions into a copy of the index and hands the rest of the queue to git's
  own `--continue` with that copy as its index, because concluding one step
  natively would throw the remaining queue away. Those commits are git's: no
  trailers, no `commit-msg` handling, not undoable. That boundary is stated on
  stderr on every delegated run, unconditionally, and `-m`, `--trailer` and
  `--dry-run` are **refused** there rather than silently ignored.
- **Ruling:** ours, with the boundary drawn explicitly at the queue — deliberate

### A single `safegit revert` is split at git's own seam

- **git's idiom:** `git revert <sha>` computes the inverse patch and commits it
  in one step, authoring the commit itself.
- **safegit:** for exactly one commit, and only when every option on the command
  line is one safegit has considered, the operation is split where git already
  splits it: `git revert --no-commit` computes and stages the inverse patch, and
  safegit's conclusion engine commits it. Anything else — a range, an
  unresolvable revision, a pathspec, an option outside the allowlist — is an
  ordinary guarded passthrough. Nothing is ever silently dropped; an option
  safegit has not considered simply takes the passthrough route.
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
  **commit** and should not touch the operator's files — is the one under
  review. Both the preview and the report say, path by path, which files were
  written and which were deleted.
- **Ruling:** git-like — **provisional, awaiting review**

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

### An autostash that cannot be applied exits nonzero

- **git's idiom:** `git merge --continue` applies the autostash git set aside in
  `.git/MERGE_AUTOSTASH` and prints `Applied autostash.`. When the apply
  conflicts, git stores the stash commit on `refs/stash`, removes the file, says
  the changes are safe in the stash — and **exits 0**. The merge is finished, so
  as far as git is concerned the command succeeded.
- **safegit:** the conclusion mirrors every part of that except the exit code.
  The commit stands, the stash is stored as `stash@{0}`, the message names it
  and the two commands that reach it — and `safegit merge-continue` exits
  nonzero. A conclusion that could not put the operator's uncommitted work back
  is a partial outcome, and a script that reads `0` will not look at the
  message. The exit code is the only channel a caller cannot ignore.
- **Ruling:** ours — deliberate

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

## Moves and renames

### Moves are declared; blob equality never decides anything

- **git's idiom:** renames are **detected** after the fact. `git log --follow`,
  `git diff -M` and every rename-aware view guess from content similarity, with
  a threshold that can be tuned and that changes the answer.
- **safegit:** safegit detects nothing. A move is stated — `--moved 'old ->
  new'` on a commit, or performed outright by `safegit mv` — and the statement is
  checked against the repository: the old path must be tracked in the commit's
  parent and gone from disk, the new one must exist. A declaration the
  repository contradicts is a hard error (exit 19), never a record. The record
  written into the commit message is what every later reader resolves against
  the trees, and no similarity score is consulted at any point. A record is
  never edited — correcting one is a retraction plus a new declaration in one
  commit, because editing the commit that carries it would rewrite history.
- **Ruling:** ours — deliberate

### `safegit mv` commits the rename and nothing else

- **git's idiom:** `git mv old new && git commit` records the rename carrying
  the blob the parent commit held. Uncommitted content changes at the moved path
  are staged by `git mv`, which is the one place the two differ in detail.
- **safegit:** each moved path is carried across as the **exact blob the parent
  tree held**, through index edits rather than by staging from disk. Uncommitted
  content changes at a moved path stay uncommitted and are a separate commit — a
  move is a move. This also makes a preview and a real run compute the same tree.
- **Ruling:** git-like — deliberate

### `safegit mv` creates a missing destination directory

- **git's idiom:** `git mv a.txt sub/a.txt` fails with `destination directory
  does not exist` when `sub/` is not there.
- **safegit:** the destination's parent directory is created when it is missing,
  and a failure part-way through the move set removes exactly the directories
  this invocation added — nothing that was already there. The reasoning is that
  every pair is validated before the first filesystem mutation anyway, so
  refusing at the last step over a directory safegit is about to create is a
  refusal with no safety in it.
- **Ruling:** ours — deliberate

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

### A non-executable hook: skipped when it is local, refused when the checkout provides it

- **git's idiom:** git ignores a hook it cannot execute, silently.
- **safegit:** a hook in the tool-owned local store is skipped with a warning,
  which is git's stance. A hook the **checkout** provides is a refusal (exit 25):
  such a hook is disabled by deleting the file and committing that, never by
  dropping its mode, so a lost executable bit — a filesystem without modes, a
  patch applied by a tool that drops them — would otherwise turn into checks that
  quietly stopped running.
- **Ruling:** mixed — deliberate

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
- **safegit:** `checkout`, `pull`, `merge`, `rebase`, `reset`, `bisect`,
  `cherry-pick` and `revert` report git's verdict verbatim once git has run.
  Those codes are deliberately absent from safegit's own registry: a registry row
  would claim ownership of a number safegit does not choose. A code from one of
  those commands is safegit's own only when the failure happened **before** git
  ran — the coordination check, an uninitialized repository, a rejected argument.
- **Ruling:** git-like — deliberate

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
  a passthrough child's output in machine mode, and git's own stdout during a
  push under `--json`. There is no JSON error object; an error path writes to
  stderr and exits nonzero.
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
  without a terminal they refuse rather than hang. Two conditions the framework
  cannot see keep their own seam: `doctor --action uninstall`, answered by
  `--approve-consequential` because the condition is the flag the caller typed,
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
  scrub is refused before anything is read. Inside a submodule the same check is
  made against the submodule's own history, with a refusal that says outright
  that a parent-repository commit hash means nothing there.
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
