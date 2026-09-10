# Move-record consumers, scrub path translation, an undo probe, and mv hardening

Filed from a read-only assessment of safegit 0.29.0 against a planned
repository-data layout migration in the fleet. Every finding below was
verified against the working tree at filing time; line numbers are omitted
on purpose and symbol names are the references. Counts and measurements are
marked as measured at filing time and drift.

This file is a handoff: it records what the assessment established, the
work it implies, and the decisions the owner must make before any of it is
built. Nothing here is decided unless the "Open decisions" section says so.

## Context

A fleet-wide program will move every repository's tool state directories
into one function-named directory per repository (`.codehome/<function>/`,
with per-directory manifests describing each directory's nature). The
program's migration design relies on safegit in these ways:

- Each repository's move is committed with `safegit mv`, so the commit is a
  pure move, exact rename detection is preserved, and the commit carries
  declared subtree move records that other tools can later read as the
  in-history record of the layout change.
- The migration is meant to be reversible per repository through the oplog
  and `safegit undo`.
- After the move, a history rewrite (`safegit scrub`) whose range crosses
  the move commit should still remap commit hashes inside hash-carrying
  files in PRE-move historical trees, even though the caller's
  `--remap-shas-in` globs are written for the post-move layout.
- A consumer tool wants to ask, without acting, whether safegit's last
  operation can be undone and why not.
- The program asked whether safegit could enforce the layout's axioms at
  commit time by reading the per-directory manifests.

The assessment confirmed some of these and refuted or narrowed others.
The facts come first; the work items follow.

## Facts established at filing time

### Undo

- `safegit undo` moves the ref under compare-and-swap and reconciles the
  index through `git.ReconcileMainIndex`, which runs `git read-tree` with
  no `-u`. The working tree is never written. After undoing a move commit
  the files remain at their new paths; safegit says so unconditionally on
  stderr, and `docs/divergences.md` records it as deliberate.
- Undoable operations are the `undoableOps` map in `undo.go`; `mv` is in
  it. `--count N` walks back N live entries; each `undo` entry cancels one
  earlier undoable entry; the oplog is append-only and unbounded.
- Undo refuses, in `runUndo`'s order: an unanswered submodule auto-bump
  decision; a held worktree operation lock (exit `LockTimeout`); a git
  operation in flight (exit `CoordinationBusy`); detached HEAD; unparseable
  oplog lines; no `CLAUDE_CODE_SESSION_ID` without `--bypass-session`; a
  scrub or author-rewrite entry encountered while walking back; fewer live
  steps than `--count`; a commit in the range safegit did not create
  (`refuseUnaccountedRange`, first-parent); a branch not where the log's
  last operation left it. There is no dirty-tree check and no time-based
  invalidation. Every one of these refusals goes through `die()`, which
  exits below the seam that emits the `--json` document, so a refusing
  `undo --dry-run --json` prints nothing on stdout and exits with the
  refusal's code; all of the non-lock, non-in-flight refusals share exit
  `General`.
- `undo --dry-run` runs the whole refusal set (`refuseUnaccountedRange` is
  placed above the dry-run branch on purpose), takes no locks, moves no
  ref, and appends no oplog entry. The no-oplog property is structurally
  true but not pinned for `undo`: `TestDryRunGuardedOperationsAppendNoOplogEntry`
  omits it.

### mv and move records

- `mv` mints one DECLARED record per pair (`trailer.OriginDeclared`),
  regardless of how many files the pair covers, and hands them to the
  pipeline pre-minted, which suppresses inference for every path under the
  declared prefixes. The inference cap (`moveInferenceCap`, counted after
  subtree collapse and after declared pairs are split off) therefore never
  applies to an `mv`. A bare `git mv` followed by `safegit commit` without
  `--moved` runs inference instead, where one family that fails the
  collapse predicate (a file left behind, a stray file at the destination,
  a symlink left behind, an empty blob that never pairs) degrades to
  per-file pairs, exceeds the cap, and the commit records NOTHING at exit
  0. Measured at filing time in one consumer repository: its scaffold base
  directory stores byte-identical copies of the files it is a base for, so
  every base file fails the unique-blob fence and inference is structurally
  hopeless for that kind of directory. Declaration is the only reliable
  route.
- `mv`'s "dirty" predicate (`dirtyMoveReason`, `mvEntryIsDirty`) is
  disk-content-versus-HEAD for the tracked paths under the moved prefixes,
  no more. Not dirty: a staged-only change (the index is never read), an
  untracked file under the directory, a tracked file deleted from disk, a
  mode change, a gitlink, and anything elsewhere in the worktree.
- `mv` commits each entry's MODE from HEAD's tree entry, never from disk.
  Git records only the executable bit; a 444-versus-644 difference is not
  a tree fact at all, so nothing any commit does can make a fresh clone
  materialize a file read-only.
- `mv`'s filesystem half is `os.Rename` of the directory, so untracked and
  gitignored content under a moved directory travels silently to the new
  location, unreported and not undoable; at the new path the old ignore
  rules no longer match it.
- `trailer.Overlap` (through `Nests`) refuses two pairs in one invocation
  whose sources nest, whose destinations nest, or which chain — exit
  `Usage`. A subtree pair and a file pair that land under the same
  destination directory (`a/ -> d/x/` beside `f -> d/x/f`) are
  `SameDestination` and refused. `commit --moved` uses the same check.
- A destination whose parent directory does not exist is refused unless
  `--create-missing-directories` elects creating it.
- `mv` pins each entry's blob SHA before the compare-and-swap loop and
  re-applies the same index edits on every retry; a commit made by raw git
  to a moved path inside that window is silently reverted. The
  `MoveWitnessChanged` refusal guards inferred records only and has no
  declared-record counterpart.
- A pipeline failure after `mv`'s validation (a `pre-commit` rejection,
  an unmerged index, CAS exhaustion, a write-tree failure) leaves the
  renames on disk with no commit and no oplog entry; the recovery text
  names `safegit commit --moved`. Only a mid-sequence rename failure rolls
  the filesystem back.
- Declared subtree records are checked for PRESENCE (old prefix absent
  from the written tree, new prefix present), never for correspondence;
  observed records carry strictly stronger evidence, and the absence of an
  origin token is the only signal that a record was declared. A revert's
  inverse records are written `observed` (`sequencer_continue.go`), not
  "declared" as the 0.29.0 changelog says.
- A repository `commit-msg` hook may strip records; `commit`, `mv`,
  `reword` and `amend` report only the records the committed message
  holds.
- The record grammar, ids, origin slot, C-quoting and the rules in
  `internal/trailer/project.go` (records are claims, trees arbitrate;
  longest match wins; a file-form record never applies to descendants;
  retractions fold over the whole chain) are documented only as Go package
  doc rendered into `docs/internal-trailer.md`. There is no stated
  stability contract for an external reader of the on-disk format.
- `trailer.Forward`, `trailer.Projection`, `trailer.RetractedIDs` and the
  `Tree`/`PathSet` types have no production caller; the only callers are
  tests. No command lists a commit's records or follows a path across
  history. `internal/` forbids import, so a Go consumer cannot reuse the
  resolver either.

### Scrub

- The three scrub modes and the author rewrite build their commit list
  with `git rev-list --topo-order --reverse` (oldest first, parents before
  children) and hand it to `walkAndRewrite`, which calls `git commit-tree`
  directly. No scrub path enters the commit pipeline.
- `--remap-shas-in` globs are matched at one site, in `remapTree`, using
  Go `path.Match` first against the full repository-relative path and then
  against the basename. The glob set is fixed for the whole run in
  `remapState.globs`. `remapTree` reads every subtree of every commit in
  range with no tree cache, by design.
- `remapContent` replaces 40-hex runs found in the growing SHA map and
  HARD-ERRORS on a run that names an in-range commit not yet walked
  (`classify`). `blobCache` is keyed by old blob SHA and its soundness does
  not depend on which glob admitted the blob.
- `remapTree`'s changed paths feed the rewrite's own Tier A intent, so
  remapped files are accounted for by `verifyIntendedChanges`.
  `verifyScrubbedFileContent` consults the glob set to decide whether a
  scrub target is also a remap target.
- Remap never runs inside submodule histories.
- Records under scrub: `RewriteMessage` transforms decoded path tokens and
  keeps id and origin; a transform that would leave a non-move is a
  refusal before anything is written; `scrub file --delete` drops whole
  records naming the path; ids are stable across rewrites.
- A record cannot be declared retroactively: `--moved` requires the
  commit's own delta to bear the move out, and a record forced in through
  a raw `--trailer` is refuted by the trees and ignored by the projection.
  Moves made before 0.29.0 are permanently unrecorded.

### Pipeline seams (relevant to any new commit-time refusal)

- Every authored commit reaches one of three entry points
  (`Pipeline.Execute`, `Pipeline.Amend`, `Pipeline.Reword`) from five
  construction sites (`commit.go` twice, `mv.go`, `sequencer_continue.go`
  twice) plus the out-of-process submodule auto-bump.
- The intake stage (`resolveFiles`, `refuseNonPortableLinks`) never sees
  `mv`'s or a conclusion's paths, which arrive as index edits. The delta a
  check can trust is `git.DiffTree(parentTree, treeSHA)` in `tryCommit`,
  between the empty-tree refusal and `verifyDeclaredMoves`; an amend must
  judge `authored` (against the first parent), and a reword reuses the
  tip's tree.
- A pipeline refusal is never logged (the only oplog append is after the
  ref update); the failed-entry mechanism is command-level.
  `concludeImmediately` runs after git has parked its state, so a new
  pipeline refusal there strands the operator mid-merge unless the park is
  undone (`cleanEmptyPark` is the machinery).
- safegit already reads a declaration from a tree: `safegit-conflict-markers`
  through `git check-attr --source <tree-ish>` on the first parent's
  `.gitattributes`. TOML parsing is already a dependency (`go-toml-edit`,
  used by recipes). An explicitly named gitignored path is refused with no
  override; a directory expansion skips ignored members and reports them.

## Work items

### 1. A read-only command exposing the move-record projection

The format is complete and durable; the gap is read access. Add a
read-only command (`EffectReadOnly`, `WithTags("json")`, a declared
payload schema — `scan` is the model) that, for a path and a commit
range, builds the chain and reports `trailer.Forward`'s projection: the
resolved path, each hop (commit, record id, origin, from, to), and whether
the path is present at the range's end. Under the dead-code policy this
wires up `trailer.Forward` rather than leaving it test-only.

The command must expose the arbitration rules so consumers stop
reimplementing them: parent-tree veto, answer-must-exist, longest match,
file-form-never-descends, whole-chain retraction folding. The
chain-building primitives exist (`git.ReachableMessages`,
`git.LsTreeRecursive`, `git.FirstParentRange`, `git.ParseCommit`); the
test harness exists (`projectionChain`, `treePathSet` in
`internal/test/moves_revert_test.go`).

Also: document the on-disk record grammar as a stable format for external
readers (currently package doc only), stating safegit's own trailer-block
detection rule, which is not git's.

### 2. Scrub follows recorded moves

When a scrub's range crosses a commit carrying subtree move records, the
caller's `--remap-shas-in` globs (written for the newest layout) are
translated backward for older commits, so pre-move trees are remapped.

Design established by the assessment:

- The walk is oldest-first, so translation is a newest-first
  PRECOMPUTATION from the records in range, done before the walk.
- Two sound rules. (A) Whole-run backward closure: collect every record in
  range, compute the transitive closure of the glob set under backward
  translation, and use the closed set for the whole run — no change to
  `remapTree`, the walker or the verifiers; chained, nested, multi-record
  and merge cases need no special handling; over-inclusion means an
  old-layout glob is also live at commits newer than the move. (B)
  Per-commit glob sets propagated newest-first over a children map, with
  union at joins; narrower; threads a parameter through `remapTree` and
  its three call sites. A single mutated set swept over the reversed
  linearization is UNSOUND for a DAG with sibling branches and must not be
  built.
- Translation rule for a subtree record `O/ -> N/` and a glob `g`: split
  both on `/`; if `N` has at least as many components as `g`, no
  translation; each component of `N` must `path.Match` the corresponding
  component of `g` (this is what lets a wildcard directory component in a
  consumer's glob translate through a record naming one concrete
  directory; a plain string-prefix rule fails that real case); the result
  is the metacharacter-escaped literal `O` joined with the remaining glob
  components; the result is re-validated and a malformed one is a hard
  error naming the commit and the record id. A file-form record
  translates only a glob that is the literal new path.
- `verifyScrubbedFileContent` must receive the closed set. `blobCache`
  stays sound. Submodule histories are out of scope and the doc comment
  must say so.
- Report every translation (commit, record id, origin, from-glob,
  to-glob) in the payload of all three scrub modes and in the preview,
  since the preview is what a consequential command's consent rests on.
- Translated globs make scrub read files it never read before, so
  `classify`'s hard error can now refuse a scrub that used to succeed
  (a pre-move changelog naming an in-range non-ancestor). That refusal is
  correct; it is a behavior change needing its own test and changelog
  entry.
- This helps only moves made after 0.29.0. History moved before records
  existed is out of reach; the consumer's fallback is to accept the
  reported dangling hashes or to pass range-scoped globs itself, and that
  concession belongs in the docs.

Effort, measured against the code at filing time: roughly a new
`remap_moves.go` plus small edits at the three `newRemapState` sites, the
`scrub.go` verification call, three payload structs and schemas, flag help
and docs, and a new test file — on the order of several hundred lines
including tests; add a `trailer.Tree` adapter over real git trees if the
arbitrate option below is chosen.

### 3. A read-only undo recoverability probe

`undo --dry-run` already answers the question; the answer is not
consumable (no `--json` document on refusal; every reason collapses into
one exit code; `undo` is `EffectMutating`, so a read-only caller may not
invoke it under the effects regime).

Refactor `runUndo`'s pre-mutation body into a function returning a verdict
(`undoable`, a closed reason token, detail, ref, the SHAs, count, the
blocking commits); `runUndo` dies on it as today; a new read-only command
emits it with a declared schema on both arms. The reason enum is the
deliverable — it replaces English-matching on stderr — and must be
test-bound so it cannot gain a member with no fixture. Per the registry's
own criterion a new exit code is warranted only when the caller's recovery
differs; the probe's "no" is one recovery, so it is one code plus the
payload reason, not one code per reason.

The probe is advisory: the branch can move between the probe and the act;
compare-and-swap and `refuseUnaccountedRange` remain the guard. No
check-then-act pattern may be built on it, and the docs must say so.

Also: add `undo` to `TestDryRunGuardedOperationsAppendNoOplogEntry` and to
the no-locks dry-run test.

### 4. mv hardening

Four findings, each its own red-green test:

- Staged-only change under a moved path passes the dirty check; the
  resulting index resurrects the old path as a staged add after
  `ReconcileMainIndex`. Either refuse the pair or state in
  `docs/divergences.md` that staged state is out of scope (the existing
  entry describes the disk case only).
- Untracked and gitignored content under a moved directory relocates
  silently. Enumerate what travelled (`git ls-files --others` under the
  prefix) and report it on stderr and in the payload; the information is
  one command away and the tool's doctrine is that a caller learns what
  safegit did.
- The declared-record stale-blob race: a raw-git commit to a moved path
  between `mv`'s HEAD read and its ref update is silently reverted on the
  CAS retry. Give declared records a witness re-check on retry, or refuse
  with a transient error the way `MoveWitnessChanged` does for inferred
  ones. The `PhaseADone` injection hook is the test seam.
- Post-validation half-state: decide whether `mv` rolls the filesystem
  back for failures that are knowably pre-ref-update (hook rejection,
  unmerged index) the way it already does for a mid-sequence rename
  failure, or keeps the current "files moved, commit them with
  `commit --moved`" contract.

### 5. Integration-guide guidance for a migrating consumer

Document, in one place, what a tool that performs a large layout move
through safegit must do: use `mv` (declared records; inference is
structurally unreliable for directories holding byte-identical copies);
keep every pair disjoint on both sides, which means a destination
directory receiving from more than one source needs more than one
commit; pass `--create-missing-directories`; start from a clean tree;
expect exit 19's path list on stderr only; set a stable
`CLAUDE_CODE_SESSION_ID` and make no commit between its own commits if
undo is to remain possible; never rely on `--bypass-session`; own the
working-tree half of any rollback, since undo never writes the tree; and
know that gitignored content travels and is un-ignored at its new path
until the ignore rules are rewritten.

### 6. Conditional: an immutability refusal at commit time

Of the four checks the layout program proposed, three are not safegit's:

- "Refuse committing payload under a directory declared uncommitted" is
  already refused, harder, by the no-override gitignored-path refusal once
  the directory's derived `.gitignore` exists; the residual (already
  tracked payload) is a migration concern.
- "Refuse a change under a generated directory unless the commit carries a
  consumer tool's `Autogenerated: true` trailer" is authorship dressed as
  nature: `--trailer` is unchecked free text, so the check would establish
  that the committer ASSERTED machine origin and refuse a party that did
  not — a writer record, which the program rejected. The trailer
  vocabulary is a consumer's, and safegit conditioning a refusal on it
  would be learning a fleet convention (writing a session id from an
  environment variable it is told about is a weaker precedent). At filing
  time a consumer already makes some of its own release-state commits
  without that trailer, so the check would break it on the next such
  commit.
- "Verify the derived in-directory `.gitignore`" requires safegit to know
  the derivation rule, which makes it the convention's definer.

The fourth — refusing a content change to a path declared immutable — is
the one with a real case for a safegit built-in, because it must remain
effective in a fresh clone (a chmod does not) and sit in front of every
authoring path (a per-clone git hook does neither). If the owner wants it:

- The declaration should be a git attribute (`safegit-immutable` on a
  path pattern, with per-path overrides) rather than a per-directory TOML
  manifest: it is per path (the directory this exists to protect holds
  immutable records, freely regenerated renderings, a hand-appended
  unreleased file and a tracked cache side by side, so no per-directory
  declaration is true of it), git parses it, safegit already reads that
  shape from a tree, it needs no filename decision, and `safegit-` is
  safegit's own vocabulary. The manifest keeps its documentary role and
  safegit never reads it.
- Read the declaration from the PARENT tree, as the conflict-marker
  exemption does; state the consequence that a commit introducing a
  declaration is not governed by it.
- Insert the check at the delta seam in `tryCommit` and `tryAmend`
  (`authored` delta), covering `mv`, the conclusions and the
  compute-and-commit forms without intake involvement.
- An election flag (positive name, per-invocation, recorded nowhere,
  named by the refusal) is the only legitimate-writer route that keeps a
  writer list out of the declaration; it must be reachable on every
  command whose pipeline path can hit the refusal — `commit`, amend, `mv`,
  the three conclusions, `merge`, `pull`, `cherry-pick`, `revert` — and
  that breadth is itself evidence about placement.
- Undo the park when the refusal fires from `concludeImmediately`, or the
  operator is stranded mid-merge with no oplog entry and a
  `merge-continue` that refuses identically.
- An unreadable declaration is a hard error; the payload names the
  declarations that governed the commit (`declined_checks` is the
  pattern).
- Registry obligations: constant, doc comment, `All()` row,
  `scripts/gen-exit-table`; one `docs/divergences.md` entry per refusal.

### 7. Rejected: reading per-directory manifests to discover the remap set

Discovering `--remap-shas-in` globs from manifests found in each
historical tree was assessed and rejected: it makes a documentary manifest
decide what a destructive rewrite edits; pre-manifest trees would need
item 2 or the flag anyway; a manifest can itself be scrubbed mid-walk, so
any reader would have to read the pre-rewrite tree while `remapTree` runs
on the already-replaced one; and no shared schema authority safegit could
validate against exists for Go. Cost is not the objection (the walk
already reads every subtree; TOML is already a dependency).

The capability's real prize is that an UNORCHESTRATED scrub in a
repository with hash-carrying files leaves every historical tree dangling
forever, because consumer-side repair fixes the working tree only. If that
is ever wanted, the form that keeps one authority is a safegit-owned
repository-level declaration under the tracked `.safegit/` directory,
listing hash-carrying globs, additive with `--remap-shas-in` (never a
fallback), read at HEAD and composed with item 2's translation. It is a
new input surface, so it needs the owner's ruling against the
prefer-existing-artifacts rule.

## Open decisions

Each is self-contained; each blocks the item it names.

1. (Item 1) The read-only projection command's name.
2. (Item 2) Translation rule: whole-run backward closure (A) or per-commit
   DAG propagation (B). A is simpler and touches no walker code; B is
   narrower and avoids remapping a file re-created at an old path after
   the move.
3. (Item 2) Records no tree bears out (a hand-written or stale record):
   accept them for glob translation (a glob for a path that never existed
   matches nothing; a bogus record that maps onto an existing path is a
   real over-match) or arbitrate against the trees as the projection does
   (a `trailer.Tree` adapter over real git trees; one probe per record).
4. (Item 2) Accept that `classify`'s existing hard error will now refuse
   some scrubs that used to succeed, because translated globs reach files
   scrub never read before.
5. (Item 2) Whether the translation list is rendered in `--dry-run`
   previews (recommended: yes).
6. (Item 3) The probe command's name, and whether it registers a dedicated
   "not undoable" exit code or answers under an existing one with the
   payload reason.
7. (Item 4) Staged-only changes under a moved path: refuse, or document
   as out of scope.
8. (Item 4) Post-validation half-state: roll the filesystem back, or keep
   the current contract.
9. (Item 1, optional) Whether `undo` should gain a working-tree-restoring
   form for move commits (derivable from the commit's own records; must
   preserve foreign staged state and refuse to delete untracked files at
   the destination), or whether tree restoration stays the caller's job.
10. (Item 6) Whether a commit-time immutability refusal is built at all;
    if so, git attribute versus manifest; whether a MOVE of an immutable
    path (a delete plus an add) is allowed without the election; whether
    a REVERT of a commit that created immutable content is legitimate
    without the election.
11. (Item 7) Whether a safegit-owned repository-level hash-carrying-globs
    declaration under `.safegit/` is wanted despite being a new input
    surface.

## Dependencies and ordering

- Items 1, 3, 4, 5 are independent of each other and of any migration;
  useful today.
- Item 2 is migration-enabling infrastructure with no measurable benefit
  until a recorded move exists in some history; it should ship before the
  first repository migrates so the first post-migration scrub is not its
  first live run. Item 2 depends on item 1 only if decision 3 chooses to
  arbitrate through the projection.
- Item 6 waits on decision 10; item 7 waits on decision 11.
- Consumers adopting items 1 and 3 need a release; that is the sanctioned
  dependency-ordering exception.

## Test shape

- Item 1: end-to-end beside `internal/test/moves_revert_test.go` using its
  chain helpers; payload beside `commit_payload_moves_test.go`; a
  migration-shape regression in `moves_inferred_subtree_test.go` (several
  families, one with a stray destination file: inference records nothing
  with `moves_over_cap` set; declaration records all).
- Item 2: new `internal/test/scrub_remap_moves_test.go` reusing
  `appendChangelogLine`, `assertChangelogSelfConsistent`, `fullShasIn`:
  hash-bearing file at an old path across several commits; `safegit mv`;
  more commits at the new path; scrub from the first commit with a
  new-layout glob only; assert self-consistency at both paths (red today).
  Variants: wildcard component in the glob prefix; chained; nested;
  file-form does not translate a prefix; merge with the record on one
  side; metacharacter in the old prefix; unfounded record under the chosen
  disposition; the new `classify` refusal; payload and preview.
- Item 3: beside each fixture in `undo_foreign_test.go`, assert the reason
  token; `machine_contract_undo_effects_test.go`: a document on both arms
  and zero recorded effects; a table test binding every reason token to a
  fixture; remedy truthfulness for every remedy the payload names.
- Item 4: red-green per finding as described; `PhaseADone` for the race.
- Item 6: `internal/test/commit_symlink_test.go` is the template
  (exit code by constant, message names the path, HEAD unmoved, elected
  form succeeds, silent control, dry-run refuses too, collected refusal,
  amend inherits), plus each of the ten doors, cross-branch `--branch`
  (declaration read from the target branch's tip), an unborn branch, the
  submodule auto-bump, an unreadable declaration, a repository with no
  declaration, a declaration added in the same commit as the payload, and
  a scrub over an immutable path passing untouched.
- Every new exit code: `exitcode_test.go`, `exit_table_test.go`,
  `exit_site_guard_test.go` enforce registration automatically.

## Corrections to the 0.29.0 changelog text found in passing

- The revert entry says a single-commit revert "declares" the inverse of
  every move; the code writes the inverse records with origin `observed`.
- For any consumer that hard-codes these: the operation-in-flight refusal
  is `CoordinationBusy` (5), the operation-lock refusal is `LockTimeout`
  (8), and a ref moved under a commit is an internal retry that becomes
  `CASExhausted` (7) only on exhaustion — the three are easy to conflate.
