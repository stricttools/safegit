# The 2026-08 redesign campaign: implementation plan

This plan is self-contained: every decision it executes is stated in full, and
a session with zero conversation context can implement any subphase from this
file plus the cited code. All file:line references were verified against the
working tree at HEAD `ecfec08` on 2026-08-21; expect small drift.

**Status quo this plan starts from.** Seventeen investigation test files are
committed in `internal/test/` with ~45 deliberately failing (red) tests that
specify the target behavior; the suite (and CI on main) is red on purpose
until the campaign completes. Nothing is pushed between now and the single
release at the end (the release's own CI gate runs on the candidate commit,
by which point the suite must be green). Release happens exactly once, in the
final phase.

**Decision-origin note.** Decisions marked `[%%]` were trust-adopted (the
user accepted a recommendation without deliberating) and are freely
reversible if evidence turns against them. Everything else was deliberate.

**Explicit non-goals (deferred, most with todos already filed):**
- Plan-file sequencer ownership (safegit computing merges via merge-tree into
  a tool-owned plan file so git never sees a merge in progress) -- declared
  destination, separate future project.
- Rebase conclusion. The sequencer-state reader classifies mid-rebase state
  and safegit's own verbs refuse during it; concluding a rebase remains
  git's own `rebase --continue/--abort`, stated as a documented limitation.
- A derived local query index for move records (regenerable cache) -- built
  when a consumer needs it, per the move-records design todo.
- Everything blocked on strictcli rulings (conditional-consequential
  official/banned; exit-code registry; dry-run network-observe contract;
  effects-handle missing shapes) -- await todos exist in this repo; the
  campaign builds the hand-rolled forms and migrates later.
- `backup restore --dry-run`'s network read: only the doc overclaim is
  struck in this campaign (Phase 9); the behavior awaits the strictcli
  dry-run ruling per `todo/dry-run-network-await-strictcli-ruling.md`.

---

## Phase 0 -- Groundwork

Foundational work every later phase calls into. Subphases 0.1-0.7 are
mutually independent and may run in parallel.

### 0.1 Test-helper consolidation

The seventeen investigation test files duplicated helpers with unique
prefixes to avoid collisions in the shared `internal/test` package. Eight
collision groups exist (run-git-in-dir ~17 spellings; write-repo-file 7;
tree-path listing 8; slice-contains 6; merge-state probes 5; conflicted-merge
fixtures 3; rev/blob readers 9; run-safegit variants). Consolidate each group
into one shared helper (home: `internal/test` shared file or
`internal/testutil`), rewrite call sites, delete the duplicates. No behavior
changes anywhere.

**Verify:** the suite compiles; the pass/fail set is byte-identical to
before (same tests red, same tests green).

### 0.2 One git-execution boundary

The convention "all git plumbing goes through internal/git" is currently
false at four sites: `internal/submodule/submodule.go:261-263` (deliberate,
to avoid an import cycle), `autobump.go:25`, `main.go:704`, and
`coord_cmd.go:21-29` (`runGitMutation` builds argv for the effects handle
directly, so the seven guarded passthroughs run without
`--no-optional-locks`). Fix structurally: extract the low-level process
construction (argv prefixing, env assembly, the context-carried overrides of
0.3) into a leaf package both `internal/git` and `internal/submodule` can
import, route the three stray callers through it, and make
`runGitMutation`'s argv construction use the same prefixing. Add a guard
test that greps the source tree for `exec.Command.*"git"` outside the
boundary package(s) and fails on any hit.

**Verify:** the guard test passes; all passthrough invocations carry
`--no-optional-locks`; `docs/concurrency-guide.md:113`'s claim becomes true.

### 0.3 Root-pinned execution context and full-tree listings

Build the execution context once at dispatch (a helper on `globalFlags`
replacing the ~37 bare `context.Background()` calls in top-level handlers):
it carries the repo-root pin (the existing `git.WithDir` machinery at
`internal/git/git.go:28-47`, today used only by submodule scrub paths),
applied inside every exec site of the boundary package (all eight already
call `applyDirOverride`: `git.go:58, 82, 408, 735, 766, 862, 884, 911`).
Passthrough commands that legitimately observe the operator's cwd (argv
forwarded to `git merge`/`rebase`/etc., and hooks git itself spawns) get a
declared, visible exemption -- the exemption list is the honest residue, not
an escape hatch. Independently and additionally, `git.LsTree`
(`git.go:655`) and `git.LsTreeAll` (`git.go:644`) gain `--full-tree`
(pinning cwd alone does not fix tree listing). User-typed relative path
arguments keep resolving against the invoking cwd at intake (that part is
already correct).

**Verify (red tests going green):** `coord_subdir_test.go` --
`TestCoordSubdirCheckoutRefusesUntrackedFromSubdir`,
`TestCoordSubdirResetHardRefusesUntrackedFromSubdir`,
`TestCoordSubdirSkipWorktreeSurvivesCommitFromSubdir`,
`TestCoordSubdirScrubProtectsTrackedIgnoredFromSubdir`,
`TestCoordSubdirScrubFromSubdirPreservesHistoryPaths`; all four red tests in
`scrub_subdir_test.go`; `commit_relative_cwd_test.go` --
`TestCommitFromSubdirRelativePathNoPendingChange` and its amend twin in
`amend_parity_test.go:335`. All root-invoked green controls keep passing.

### 0.4 The ZeroSHA contract

`git.UpdateRef` (`internal/git/git.go:177-184`) omits the old-value argument
when it is empty -- an unconditional write, which is the root-commit CAS hole
(`internal/commit/commit.go:324` passes empty for root commits; the doc
comment promises create-only semantics the code does not implement). Change
the contract: `UpdateRef` and `DeleteRef` hard-error on an empty old value;
export `git.ZeroSHA` (the all-zeros object name, git's "this ref must not
exist" convention -- git then refuses with "reference already exists", which
`isTransientRefError` already classifies for retry); the root-commit call
site passes `git.ZeroSHA`. Consolidate the four existing null-SHA literals
(`commit.go:142`, `push.go:36`, `push.go:352`, `hook.go:179`) onto the
constant.

**Verify:** `root_commit_cas_test.go` --
`TestRootCommitDoesNotClobberRefCreatedInWindow` goes green;
`TestRootCommitZeroOldValueRefusesExistingRef` and
`TestRootCommitConcurrentSafegitBothLand` keep passing.

### 0.5 The exit-code registry

Create `internal/exitcode`: every exit code as a named constant with a doc
comment -- the single authority. Route all ~240 exit sites through it (161
`die()` calls, 14 `os.Exit`, ~64 nonzero handler returns; existing constants
live in three homes: `internal/commit/commit.go:26-30`, `push.go:19-23`,
`backup.go:17-23`, plus bare literals like `coord_cmd.go:42`). Add a test
that pins the exit-code table in `docs/commands-guide.md:1124-1140` to the
registry (fails on divergence in either direction). In the same pass:

- **Exit 8 (lock acquisition timeout):** type the timeout error in
  `internal/lock` and surface it as exit 8. This also fixes a real bug: the
  four rewrite commands (`scrub.go:216-219`, `scrub_match.go:187-190`,
  `scrub_run.go:275-278`, `rewrite_author.go:144-147`) and `undo.go:184-187`
  discard the lock error and report "another rewrite operation is in
  progress" for any failure including a mkdir error -- they must surface the
  real error.
- **Exit 14 (binary file + hunk spec):** export the sentinel at
  `internal/stage/stage.go:45-47` and map it. Precondition: convert the
  three bare `CommitError` type assertions (`commit.go:87, 187, 251`) to
  `errors.As` -- the binary error reaches the caller wrapped
  (`internal/commit/commit.go:222-224`), which a type assertion cannot see.
- **Exit 40:** declared (`push.go:22`) and documented but never produced --
  every push failure returns 1. Wire it: a failed `git push` returns 40.
- **Exit 2 stance:** unchanged in this campaign (deferred to the strictcli
  usage-code ruling, `todo/exit-codes-await-strictcli-registry.md`); the
  docs table states the split honestly (framework parse errors exit 1;
  safegit's post-parse guards exit 2).
- The staleness re-check documented as exit 13 is NOT built (the guarded
  window is harmless -- `git apply --cached` never reads the worktree -- and
  the dangerous window is invisible to a self-comparison); exits 12/13
  disappear from docs in Phase 9.

**Verify:** the table-pin test passes; new red-green tests for exit 8 (live
lock + short timeout) and exit 14 (binary + hunk spec) pass; no test in the
suite asserts a code the registry does not define.

### 0.6 Oplog integrity

- Remove the 4096-byte line cap (`internal/oplog/oplog.go:17, 52-55`) -- the
  flock (`LockedAppend`) is the integrity mechanism, matching the scrub
  journal's stated reasoning at `rewrite_maps.go:16-19` (rewrite that
  comment; it contrasts against the cap being removed). Raise the
  `bufio.Scanner` buffer in `Read` (`oplog.go:80`) accordingly or switch to
  a reader without a line cap.
- `oplog.Read` returns a skipped-unparseable-line count alongside entries
  (signature change; five consumers). `safegit undo` hard-errors when the
  count is nonzero (its step arithmetic is unreliable over a log with holes);
  `doctor` diagnose reports the count.
- Delete rotation entirely: `oplog.Rotate` + `LogSize`
  (`oplog.go:105-153`), the doctor wiring (`doctor.go:260-283, 301-309,
  323-325`), and the `log.maxSizeMB` config key everywhere
  (`internal/repo/repo.go:22, 51-54, 64, 232, 293-294, 335-336, 351`).
  Existing config.json files carrying the key still parse (plain
  `json.Unmarshal` ignores unknown members -- verified); `config set
  log.maxSizeMB` starts erroring "unknown config key", which is correct
  pre-stable behavior. Update `internal/repo/repo_test.go:237-238, 257` and
  the fixture at `internal/test/stress_test.go:662`; invert
  `TestAppendRejectsOversizedLine` (`internal/oplog/oplog_test.go:84`) into
  an oversized-append-succeeds test.
- Rewrite the comment in `internal/filelock/locked_append_windows.go:6-7`
  (its justification cites the removed cap); the five `//go:build windows`
  source files stay compilable.

**Verify:** new tests -- an append well over 4096 bytes survives a
read-back; `undo` refuses on a corrupted log line; doctor reports the count.

### 0.7 Platform and repo hygiene

- Remove `windows` from `.goreleaser.yml:12` and the zip format override at
  `:20-22`; make the same edit in the scaffold base copy
  `.rlsbl/bases/.goreleaser.yml` in the same commit (otherwise the next
  `rlsbl scaffold` three-way merge resurrects it).
- Consolidate the duplicate push-triggered CI: `.github/workflows/ci.yml`
  (OS matrix) and `.github/workflows/ci-go.yml` (ubuntu-only) run
  near-identical suites and both are named `CI`, doubling the release CI
  wait. Keep the OS-matrix workflow; remove the duplicate through the rlsbl
  scaffold configuration so it is not regenerated.
- Fix the non-atomic `config.json` write: `repo.Init`
  (`internal/repo/repo.go:146`) uses a plain `os.WriteFile` while
  `repo.IsInitialized` (`repo.go:104-107`) stats the same file -- a
  concurrent first invocation reads a partial file (observed flake:
  "parsing config.json: unexpected end of JSON input"). Write to a temp file
  in the same directory and rename.
- Add a git-version floor helper: safegit currently never parses the git
  version (`gitVersion()` at `main.go:703-709` only prints it). Phase 6
  introduces hard dependencies on `git merge-tree --write-tree` and
  `AUTO_MERGE` (git 2.38 era) and `--attr-source` (git 2.40 era). Build a
  parse-and-compare helper with per-feature floors; features refuse with the
  named floor when git is older; doctor diagnose reports the git version
  against the highest floor.

**Verify:** goreleaser config lists linux+darwin only in both copies; one
push-triggered test workflow remains; a concurrency test hammers first-time
init without the flake; a unit test covers version parsing and refusal.

---

## Phase 1 -- Shared-index ownership and sequencer state

Depends on 0.2/0.3 (the preserve helper's `ls-files` snapshot must run under
the pinned context or it inherits the cwd truncation).

### 1.1 Delete the post-passthrough index sync

During a passthrough, git owns the shared index and leaves it exactly as its
own contract requires; the sync repairs nothing and destroys sequencer
state. Delete the `syncMainIndex` helper at `coord_cmd.go:47-55` and all its
call sites: checkout `:89`, pull `:152`, merge `:193`, rebase `:234`, reset
`:280`, bisect `:326`, and `runGuardedPassthrough` `:373` (cherry-pick,
revert). Also delete the sync after `backup restore`'s `--ff-only` merge
(`backup.go:395`) -- that merge maintains the index itself, same reasoning.
The early `return 1` branches in merge/rebase (`coord_cmd.go:186-188`,
`:227-229`) become ordinary error returns (their sync-avoidance purpose is
gone); while there, propagate git's real exit code instead of the hardcoded 1
(matching `runGuardedPassthrough:381`).

**Verify (red going green):** all seven red tests in
`sequencer_conflict_test.go` (`TestSeqConflictCherryPickPreservesConflictState`,
`...ContinueProducesSameCommit`, `...RevertPreservesConflictState`,
`...CherryPickNoCommitPreservesStagedResult`,
`...RevertNoCommitPreservesStagedResult`,
`...MultiPickPartialProgressPreserved`,
`...MergeNoCommitPreservesStagedResult`) plus
`TestGuardedPassthroughKeepsCherryPickConflictStages`
(`undo_index_sync_test.go:354`). The two green controls
(`TestSeqConflictMergePreservesConflictState`,
`TestSeqConflictRebasePreservesConflictState`) keep passing.

### 1.2 The sequencer-state reader

New package `internal/sequencer`: the single authority for in-flight git
operation state. It resolves the worktree git dir and returns a typed
result: merge (parents from every `MERGE_HEAD` line, message file), single
cherry-pick or revert (source commit, author info, message file), queued
cherry-pick (discriminator: the `.git/sequencer` directory exists -- probed
and confirmed; single conflicted picks have `CHERRY_PICK_HEAD` +
`AUTO_MERGE` but no sequencer dir), rebase (`rebase-merge`/`rebase-apply`
dirs), or none. It also knows each operation's full state-file set:
`MERGE_HEAD`, `MERGE_MODE`, `MERGE_MSG`, `CHERRY_PICK_HEAD`, `REVERT_HEAD`,
`AUTO_MERGE`, and the sequencer dir.

**Verify:** unit tests per state, including the queued-vs-single
discriminator and octopus (multi-line `MERGE_HEAD`).

### 1.3 The preserve helper

New helper (in `internal/git` or `internal/commit`): snapshot the shared
index's delta against the pre-operation tip (including unmerged stage 1/2/3
entries, via `ls-files -s` under the pinned context), perform the read-tree
sync, replay the snapshot with one `git update-index --index-info` batch
(unmerged-path replay requires first clearing the stage-0 entry the sync
wrote -- a zero-mode removal line; mechanically probed and confirmed). A
replay failure is a HARD error, never a warning -- the current
warn-and-continue at every sync call site is removed. The helper preserves
skip-worktree flags exactly as `syncMainIndexInner`
(`internal/git/git.go:266-271, 292-298`) does today. Adopt it at: commit
(`internal/commit/commit.go:334-338`), amend (`amend.go:230-235`), reword
(`amend.go:389-393`), undo (`undo.go:201-209`; root undo's read-tree-empty
case included). Rewrite the two change-detector assertions in
`commit_untrack_gitignored_test.go:146-152` (they pin the old
discard-everything behavior and were written as deliberate change alarms).

**Verify:** `TestUndoPreservesForeignStagedState`
(`undo_index_sync_test.go:132`) goes green;
`TestUndoLeavesWorkingTreeIntact` (green pin -- no worktree-touching sync
variant allowed) keeps passing; the skip-worktree green pins
(`skipworktree_test.go`) keep passing; NEW tests assert foreign staged state
(modification + addition + rm-cached deletion from "another session")
survives `safegit commit`, `amend`, and reword.

### 1.4 Mid-sequencer hard refusals

While the reader reports any in-flight state: `commit` (both the pathspec
and `--allow-empty` forms), `amend`/reword, and `undo` hard-refuse, naming
the state found and the working way out (the Phase 6 conclusion verb for
merge/pick/revert; git's own rebase commands for rebase). Refusals live in
the pipeline (`internal/commit`), not the handlers, so the submodule
auto-bump self-spawn (`autobump.go:71-79`) is covered: a parent repo mid-
merge now correctly refuses the auto-bump commit with an explanatory error.
`coord.DirtyState.Refuse`'s suggestion text (`internal/coord/coord.go:55-70`)
becomes sequencer-aware so it never suggests a command that cannot work.

**Verify (red going green):** `TestCommitWithPathspecRefusedDuringMerge`,
`TestCommitAllowEmptyRefusedDuringMerge` (`commit_merge_state_test.go`),
`TestUndoRefusedMidMerge` (`undo_index_sync_test.go:209` -- HEAD, MERGE_HEAD
and the unmerged stages must all be untouched by the refusal),
`TestAmendRefusedWhileMerging`, `TestAmendRefusedWhileCherryPicking`
(`amend_parity_test.go:693, 758`). Message assertions are by substance (the
state name and suggested verb appear), not exact strings.

### 1.5 The worktree operation lock

A worktree-scoped lock (the existing `lock.Acquire` primitive with the
worktree-local safegit dir as the locks base -- `repo.SafegitDir`,
`internal/repo/repo.go:69-71`) serializes every tree-mutating passthrough
(checkout, pull, merge, rebase, reset --hard, bisect, cherry-pick, revert)
and, later, the conclusion verb. Lock ordering is declared once: the
worktree operation lock is always OUTERMOST; the per-ref commit lock
(acquired deep in `tryCommit`, `internal/commit/commit.go:300`) nests
inside. Extend `safegit unlock` and `doctor`'s lock scanning to reach
worktree-local locks and pseudo-ref lock names -- the existing
`safegit/rewrite` lock is currently reachable by neither (`unlock.go:20-22`
prefixes `refs/heads/`; `doctor.go:257, 312` scan only the shared dir); fix
that hole in the same pass. This also makes the documented "another
operation in progress" refusal true (docs updated in Phase 9 to describe
both layers: the dirty-tree check and the operation lock).

**Verify:** NEW tests -- two concurrent passthroughs in one worktree
serialize; a stale operation lock is recoverable via `safegit unlock`;
`doctor` lists it.

### 1.6 Scrub's pre-sync cleanliness re-check

Immediately before the worktree-touching sync at `rewrite_result.go:189`,
inside the rewrite lock the caller already holds, re-run the clean-tree
check (the entry check at `main.go:815-824` runs long before the rewrite
finishes). Staged state that appeared mid-rewrite is a hard error naming the
paths -- never silently overwritten.

**Verify:** NEW test -- stage a foreign change after a scrub starts (hook or
test-orchestrated timing), assert hard error and intact staged state.

---

## Phase 2 -- Commit pipeline core rewrite

One coordinated pass over `internal/commit` (the collapse-multi-pass rule:
these subphases share files and are implemented together per file group,
in the order below). Depends on Phase 0; 1.3/1.4 land first so this phase
does not re-touch the sync and refusal seams.

### 2.1 Delete automatic move detection

Delete `internal/commit/moves.go` and its call sites
(`internal/commit/commit.go:235`, `amend.go:171`), the
`AutoStagedDeletions` members on both result structs (`commit.go:79`,
`amend.go:41`), the stderr notices (`commit.go:119-121, 224-226` in the root
package), and their contribution to the count line. Convert the eleven
green `TestMoveDetection_*` tests (`moves_test.go`) into removal-regression
tests asserting deletions are NOT auto-staged; invert the six red
`TestCrossSessionMoveDetection_*` tests into "cannot happen" guards.

**Verify:** the inverted cross-session tests pass (no adoption, no rename
notice, victim session can commit its own deletion); the removal-regression
tests pass; `TestIntakeEdgeDanglingSymlinkNoColon` goes green as a side
effect (the hash-through-symlink crash was in detection).

### 2.2 Canonical paths, directory expansion, no-match errors, parent-ref validation

- One normalization point in `resolveFiles` (`internal/commit/commit.go:365-420`)
  producing canonical slash-separated REPO-RELATIVE paths; absolute derived
  only at filesystem-syscall boundaries via one helper. Delete the scattered
  re-derivations.
- Directory expansion at intake `[%%]`: a directory argument expands to the
  union of what is on disk under the prefix and what the commit's parent
  tree tracks there (deletions included), never descending into
  gitlink/submodule boundaries. Downstream code never sees a directory.
- A named path or directory contributing nothing to the commit is a HARD
  error naming the path (unifying today's inconsistent trio: early error for
  vanished dirs, late misleading "nothing to commit" for empty dirs, silent
  no-op for unchanged files).
- Tracked-path validation uses the commit's ACTUAL parent ref, not HEAD:
  `git.IsTracked` (`internal/git/git.go:217-224`) takes a rev parameter;
  other callers pass HEAD explicitly. Cross-branch deletions of paths
  tracked only on the target branch work; the HEAD-only case becomes an
  accurate early refusal naming the target branch.

**Verify (red going green):**
`TestCommitStagedDeletions_DirectoryPathWithMovedFile`,
`..._DirectoryPathWithUnrelatedEmptyFile` and their amend twins;
`TestIntakeEdgeCrossBranchDeleteTrackedOnlyOnTarget`,
`...TrackedOnlyOnHead`, `...AmendDeleteTrackedOnlyOnTarget`,
`TestAmendCrossBranchDeletionOfPathTrackedOnlyOnTarget`. Green controls
(`TestCommitStagedDeletions_DirectoryPath`, `..._FilePathsWithMovedFile`,
cross-branch shared/add controls) keep passing. NEW tests: no-match hard
errors for a file and for an empty directory; expansion stopping at a
submodule boundary.

### 2.3 Symlink policy

The final path component is never symlink-resolved (`resolveSymlinks` at
`internal/commit/commit.go:426-436` restricted to parent components, whose
macOS rationale stays valid); symlinks stage as 120000 link objects.
Escaping targets (outside the repo) are allowed with a one-line stderr
notice. A directory-symlink argument uses trailing-slash disambiguation:
bare name means the link object, trailing slash means through the link (the
grammar is documented). Hunk specs on symlinks are refused. Dangling
symlinks are committable (detection's hash-through-link is already gone).

**Verify (red going green):** `TestCommitSymlink_LinkToCommittedFile`,
`TestCommitSymlink_MixedWithRegularFile`,
`TestAmendSymlink_LinkToCommittedFile`,
`TestIntakeEdgeColonNameBrokenSymlink`. NEW tests: escaping-target notice;
trailing-slash both meanings; hunk-on-symlink refusal.

### 2.4 The hunks flag

Hunk selection moves from the colon-suffix grammar with its `os.Stat` probe
(`main.go:861-901`) to a repeatable flag: `--hunks 'path:1,3'`
(StringFlag, Repeatable, Unique(true), Optional, with a per-element
ValidateFn splitting on the LAST colon -- no filesystem probe, ever).
Positional arguments are always literal paths; colons in filenames need no
escaping. `FileSpec` internals (`commit.go:51-55` root pkg) are unchanged.
Deliberately rewrite the green pins of the old grammar:
`TestIntakeEdgeHunkSpecFromSubdir`, `...FromRoot`,
`TestIntakeEdgeSameArgvDifferentMeaningByCwd` (its own comment names itself
as the test a syntax change must update), `TestIsHunkSpec`,
`TestParseFileSpecs` (`main_test.go:51, 80`), and the hunk invocation in
`dryrun_object_purity_test.go:170`.

**Verify (red going green):** `TestIntakeEdgeColonNameDeletion` (deleting a
colon-named file works). Rewritten pins pass under the new grammar.

### 2.5 The untrack flag

`--untrack <path>` (repeatable) on commit and amend: stages removal-from-
index while the file stays on disk. General scope -- any tracked path, with
a tracked-in-the-parent guard making typos hard errors; when the target is
NOT gitignored, an informative line is printed (not a refusal). The
gitignored-path refusal at `internal/commit/commit.go:410-415` stops
applying to `--untrack` targets (it still blocks ADDING ignored content).
One-commit atomicity with a `.gitignore` edit works by construction.
Rewrite the two red tests that currently assert a flagless untrack form
(`TestCommitUntrackGitignoredPath`, `TestAmendUntrackGitignoredPath`) to use
the flag -- the flagless form remains refused (declared intent).

**Verify:** rewritten untrack tests green (single commit containing the
`.gitignore` edit and the index removal; file still on disk); NEW tests:
untrack of a non-ignored tracked path (works + informative line); untrack of
an untracked path (hard error).

### 2.6 Multi-parent and index-base pipeline inputs

The commit request carries a parents slice and an explicit index-base
selector (parent-tree, the current default; the multi-parent + shared-index-
copy mode is consumed by Phase 6). `git.CommitTree`
(`internal/git/git.go:163-173`) becomes multi-parent (the plumbing exists:
`CommitTreeWithAuthor` at `git.go:573-594` already loops parents; unify so
default-identity callers get multi-parent too). Amend and reword read the
full parent list via `git.ParseCommit` (`git.go:484-491, 517`) instead of
`ref^` (`amend.go:125, 343`) and preserve every parent.

**Verify (red going green):** `TestRewordMergeCommitPreservesBothParents`,
`TestAmendMergeCommitWithFilesPreservesBothParents`,
`TestRewordMergeCommitCrossBranchPreservesBothParents`
(`amend_parity_test.go:536, 575, 609`).

### 2.7 Message joining

Repeated `-m` values join with a blank line (subject + body, matching git).
The join is written once (a shared helper) and used by the three sites in
the root `commit.go` (commit ~:55, amend ~:162, reword ~:234). Update the
`-m` flag help (`main.go:182`) and the hand-written mention in
`docs/commands-guide.md`.

**Verify (red going green):** all three tests in
`commit_multi_message_test.go` (commit, amend, reword).

### 2.8 Truthful reporting: changed-path list, count, JSON payload

Add a `diff-tree` wrapper to `internal/git` (none exists; recursive
name-status between two trees). After each commit/amend/reword, derive the
changed-path list ONCE from parent tree vs new tree; the human line prints
its length ("N file(s) committed" can no longer lie); amend gains the same
line (it prints none today). Declare a JSON payload schema for `commit`
(pattern: `versionPayloadSchema`, `main.go:677-687`): ref, parent(s), tree,
sha (null under dry run), files, attempts, dry_run. Nothing is ever counted
from arguments.

**Verify:** the count assertion inside `TestCommitSymlink_MixedWithRegularFile`
goes green; `TestMachineModeReachesEveryCommand` covers the new payload;
NEW test: payload shape for a normal commit and an amend.

### 2.9 Commit-family git hooks and auto-bump ordering

- Run `commit-msg` explicitly: on the user's composed message BEFORE
  safegit trailer injection, once per commit (outside the CAS retry loop --
  and move `pre-commit` out of the loop too; it currently re-runs per
  attempt at `internal/commit/commit.go:242-250`); nonzero aborts; the
  possibly-rewritten message is re-read. Run `post-commit` (fire-and-forget)
  after the ref moves. Amend and reword gain the same hook execution (they
  run none today). All three hooks are skipped under `--dry-run` (as
  `pre-commit` is today), and the preview notes it. `prepare-commit-msg` is
  never run (documented in Phase 9; doctor reports it in Phase 5).
- Submodule `autoBumpParent`: the key's PRESENCE becomes mandatory and is
  validated BEFORE the submodule commit executes (today the hard error at
  `autobump.go:144-146` fires after the commit moved the ref -- the worst
  ordering). Explicit `false` remains legal ("do not bump"). Rewrite
  `TestAutoBumpConfigAbsentErrors` (`internal/test/submodule_test.go:1826`,
  which currently asserts the commit DID happen); `TestAutoBumpConfigFalseSkips`
  and the other seven auto-bump green tests keep passing.

**Verify:** NEW tests -- commit-msg rejection aborts with no commit;
commit-msg rewriting is honored and trailers survive; post-commit runs
exactly once per real commit and never in dry runs; amend runs the hooks;
auto-bump absent-key refusal happens with NO submodule commit made.

---

## Phase 3 -- Dry-run honesty and effects wiring

Depends on 0.2/0.3 (the context machinery) and Phase 2 (payload/count).

### 3.1 Object quarantine with enforcement

- A context-carried object-directory override in the execution boundary
  (sibling of `WithDir`, applied in the same eight exec sites): sets
  `GIT_OBJECT_DIRECTORY` to a throwaway dir and APPENDS the repo's object
  dir to any inherited `GIT_ALTERNATE_OBJECT_DIRECTORIES` (Go's exec env
  dedup keeps the last occurrence -- appending blindly clobbers).
- Installed at handler entry for every previewing command, BEFORE the first
  git call (`git.RepoRoot` at `internal/commit/commit.go:85` currently
  precedes preview-dir creation by four calls; a missing quarantine dir
  makes git fail repo discovery entirely, so the dir is created first). The
  quarantine lives inside the existing auto-cleaned preview area
  (`indexBaseDir`, `commit.go:153-169`) but with handler-scope lifetime, not
  per-CAS-attempt; the green pins in `dryrun_test.go` (exactly one
  `safegit-preview-*` dir, nothing under `.git/safegit`) keep holding.
- ENFORCEMENT: `internal/git` holds the closed list of object-writing argv
  shapes (add, apply --cached, write-tree, commit-tree, hash-object -w,
  mktree, merge-tree --write-tree) and, in preview mode, hard-errors any of
  them running without an active quarantine. A future leak becomes a loud
  error, not a silent regression.
- Reword's preview stops writing anything: it is a pure computation (it
  writes only a commit object today, purely to print a hash that is not the
  real one anyway).

**Verify (red going green):** all three tests in
`dryrun_object_purity_test.go`. NEW test: the enforcement refusal fires for
an unquarantined object write in preview mode (unit-level).

### 3.2 Honest preview reporting

- Retire the `[main <sha>]`-shaped line in dry runs (the committed SHA is
  unknowable in advance -- committer timestamps are inside the object).
  Print a would-commit line with the file count and the TREE sha (a real
  prediction).
- The minted `update-ref` in the would-do record carries a preview
  placeholder instead of an invented commit SHA, and its argv gains
  `--no-optional-locks` so the log prints the command the execute path
  actually runs. Fix the identical argv defect in all four
  `scrub_preview.go:47-62` records (sibling rule). Rewrite the assertion at
  `effects_regime_test.go:255` (it pins the old argv).
- The commit payload's `sha` is null under dry run (schema from 2.8).

**Verify:** rewritten effects-regime assertions pass; NEW tests: dry-run
human output contains the would-commit form and no fake commit line; JSON
payload has sha null and dry_run true.

### 3.3 Pipeline onto the effects handle

Mint `update-ref` through the effects handle in BOTH modes (deleting the
dry-only branch in `recordCommitRefUpdate`, root `commit.go:126-148`).
Declare the observe allowlist (`WithProcObserveAllowlist`) for safegit's
read plumbing -- every prefix spelled with the literal
`--no-optional-locks` element (matching is exact element-wise from index 0)
and at least two tokens. The ref lock's exclusive-create and the oplog
append stay OFF-handle with the reason stated in code comments (the
framework lacks exclusive-create and append-only shapes; the strictcli todo
is filed; dry mode never reaches either site). Hunk staging's stdin call
also stays off-handle (Run has no stdin option) -- it executes for real
under the quarantine in dry mode, which is the decided model (only the ref
update is a recorded mutation). Mind the framework's observe-staleness rule:
after the first recorded mutation, dry-mode observes return unusable stale
values -- the mint must remain the last effects action on the dry path.

**Verify:** the effects-regime suite passes with the new wiring;
`safegit --dump-schema` shows the allowlist; a dry-run commit's envelope
carries exactly one would-do record (the ref update) plus the preview data.

---

## Phase 4 -- Scrub integrity

Depends on 0.3 (cwd), 0.5 (exit codes), 2.8's diff-tree wrapper.

### 4.1 Two-tier verification, hard-erroring, ordered before refs move

Restructure `RewriteResult.Finalize` (`rewrite_result.go:92-272`) to take
TWO verification hooks:

- **Tier A -- pre-refs, aborting.** Runs BEFORE `captureRemoteTrackingState`
  (`rewrite_result.go:96`) and before the journal `start` record
  (`:109-124`) -- an abort here must not look like a crashed rewrite to the
  journal's start-without-complete detection. At this point the rewritten
  commits exist as unreachable objects and nothing has moved. Checks, all
  HARD errors leaving original history untouched:
  - **Preservation invariant** on every rewritten old/new commit pair: the
    changed-path set (one `diff-tree` per pair -- not the current
    four-subprocess tree materialization in `scrub_verify.go:122-181`) must
    equal the operation's intended change set. This generalizes the
    existing check 5 (which exists only for `scrub file` and is currently
    unrunnable because cleanup prunes the old commits before verification
    reads them).
  - **Content verification for `scrub file`, both modes:** delete mode --
    the target path absent from every rewritten tree; replace mode -- new
    content present at the path, old blobs referenced nowhere in the new
    trees.
  - **The tripwire:** the operation computes an EXPECTATION SET (the
    commits whose tree or message it determined must change, plus their
    descendants -- not the raw range size, which legitimately exceeds the
    rewrite count) and hard-errors when the actually-rewritten set differs.
    A genuinely empty operation (zero candidates from the start) is: for
    `scrub file`, a hard error (target absent from all history = mistyped
    target, the measured silent-no-op hole); for match/run, success with an
    explicit "0 commits contained the pattern" statement.
  - **Pattern absence over the NEW commit set** (match/run): a new scan
    mode over an explicit commit list (`rev-list --objects` on the new
    tips + batch cat-file; `ScanOpts` at `internal/scan/scan.go:38-44`
    gains the mode -- owned by the scan package). The whole-store scan
    cannot run here (old objects still exist pre-cleanup, by design).
- **Tier B -- post-cleanup, non-aborting but nonzero.** The old-object
  residue checks (`scrub_verify.go:292`, `cleanup.go:82`) and stale-pointer
  checks that structurally require refs to have moved. Failures exit
  nonzero with explicit "rewrite completed; residue remains" reporting --
  never silent, never rolled back. The green pins
  `TestScrubMatchStashWarning` and `TestScrubMatchUnreachablePruned`
  (`scrub_match_test.go:341, 229`) keep passing under exactly this split.

The annotation pass (`scrub_exec.go:389-450`) is split so tag-object writes
happen pre-refs (verifiable in Tier A) and its `UpdateRef` calls happen with
the other ref moves. `author rewrite`'s existing post-refs verification
(`rewrite_author.go:216-243`) stays in the Tier B slot.

**Verify:** NEW tests -- a verification failure leaves every ref and the
journal untouched (the central new guarantee); the subdirectory-destruction
scenario now aborts in Tier A even with the cwd fix reverted locally (defense
in depth); the mistyped-target scrub file hard-errors; the zero-candidate
match reports explicitly; Tier B residue still exits nonzero with the
rewrite standing.

### 4.2 Explicit scrub-file modes

`scrub file` gains a required member-spelled selector: `--delete` vs
`--replace-with <path>` (its own ChoiceDecl vars -- the framework refuses
aliasing a ChoiceDecl across selectors of different names). The `os.Stat`
inference at `scrub.go:146-154` (and the submodule twin at `:414-422`) is
deleted. The replacement SOURCE is read at the operator's cwd via
`os.ReadFile` + `git.HashObjectWriteBytes` (a path-based hash-object would
resolve against the pinned repo root -- wrong for an operator-typed source
path); the source is an arbitrary file, the positional argument remains the
repo-relative TARGET. The JSON payload's mode enum keeps its `replace`/
`remove` spellings (documented mapping). While registering, `scrub file`
also adopts the shared `range` selector (`--from` / `--entire-history`) that
match/run already use, removing its odd-one-out plain `--from`. Update the
53 `scrub file` invocations across the nine test files and the doc examples.

**Verify:** registration refuses neither-flag; both modes red-green
(delete: path gone everywhere; replace: new content present, old blobs
gone); an `--entire-history` file scrub works.

---

## Phase 5 -- Hooks subsystem

Independent of Phases 1-4 except 0.2/0.3.

### 5.1 The location enumerator

Export from `internal/hooks` a location walker: enumerates the LIVE hooks
area recursively with NO executability or naming filters (a secret in a
chmod-644 leftover or `~`-suffix file must still be enumerable);
`hooks.Discover` layers execution-eligibility (executable bit, naming
rules) on top of it. Its three consumers replace their private copies of the
location knowledge: discovery (`hooks.go:47, 51, 61`), doctor's hook-perms
check (`doctor.go:176`), and scan's sweep (5.6).

### 5.2 The directory move and the tracked store

- Live hooks move from `.git/hooks` to tool-owned `.git/safegit/hooks`.
  `safegit hook migrate` performs the one-time move: it relocates the
  `pre-pre-push` file and the `pre-pre-push.d/` directory UNCONDITIONALLY
  (they are the only safegit-owned names in `.git/hooks` -- no content
  sniffing, which cannot distinguish an edited placeholder from an operator
  hook and must not try); a repo with nothing to move succeeds and says so.
  After migration, discovery finding hooks at the legacy location is a HARD
  error naming `hook migrate` -- no silent ignoring, no shim reading.
- The tracked store: `.safegit/hooks/` in the worktree, committed, executed
  directly (no approval machinery -- deliberate ruling: hooks are not
  exceptional among the committed code a repo already runs, and pre-pre-push
  hooks fire on push, not on clone-to-inspect). Discovery reads BOTH
  locations; on a name collision both run, tracked first, name-sorted within
  each location. `hook list` shows each hook's origin (tracked/local) and
  executability (making the help text's promise at `main.go:368` true). A
  tracked hook that is not executable is a hard error at push time listing
  the chmod-and-commit fix.
- `hook install` writes into `.git/safegit/hooks`, calls the initialization
  guard first (it currently skips `ensureInitialized`, which would create a
  half-initialized safegit dir), and REFUSES any existing destination
  (upgrade = `hook remove` then install). The lost no-clobber logic from the
  dead placeholder path is thereby restored; native-git-hook clobbering
  becomes structurally impossible.
- `doctor --action uninstall`'s promise becomes true for free
  (`repo.Uninstall`'s RemoveAll now covers the hooks); it additionally
  removes legacy-location safegit-owned names, and never touches the
  tracked store (committed content is not tool state).
- Update the location preconditions inside `hook_safety_test.go:199-202,
  243` (they hardcode the legacy path as fixture setup).

**Verify (red going green):** `TestHookInstallArbitraryBasenameIsDiscoverable`
(any installed name is discoverable in the tool-owned dir -- the
basename/discovery mismatch no longer exists),
`TestHookInstallDoesNotClobberNativeGitHook`,
`TestDoctorUninstallRemovesInstalledHooks`. Controls keep passing. NEW
tests: migrate (populated and empty repos, post-migration legacy detection);
tracked-store execution and ordering; non-executable tracked hook push
refusal; install collision refusal.

### 5.3 `hook remove`

Remove-by-name over the tool-owned live directory (including entries under
the `.d` directory), minted through the effects handle (honest dry run).
When the name resolves to a TRACKED hook, hard-error explaining that tracked
hooks are removed by committing their deletion.

**Verify:** NEW tests -- remove installed hook; remove nonexistent (error);
remove tracked-name (explanatory error); dry-run records the removal.

### 5.4 Dead code disposition

Delete `hooks.Install` (`hooks.go:296-313`, superseded) and
`hooks.InstallPlaceholder` (`hooks.go:316-337`) after absorbing the
no-clobber check into the live path (done in 5.2); delete their tests.

### 5.5 Doctor: never-executed git hooks

`doctor` diagnose reports any git-native hook files present that safegit
never executes (`prepare-commit-msg` always; anything else outside
pre-commit/commit-msg/post-commit) -- a stated fact, not a warning on an
operation.

**Verify:** NEW test with a `prepare-commit-msg` file present.

### 5.6 Scan coverage `[%%]`

Scan's non-object sweep uses the location enumerator UNION git's native
`.git/hooks` (safegit executes pre-commit/commit-msg/post-commit from
there, so their content stays scanned -- preserving the green
`TestScanSeesTopLevelPrePrePushHook` and the attribution test that matches
inside `.git/hooks/pre-commit`), recursively. It also sweeps
`.git/safegit/` EXCLUDING `scrub-policies.jsonl` and `rewrite-maps.jsonl`
(tool journals that legitimately reference scrubbed content; exclusion
reason stated in code). Path coordinates are unified: worktree and blob
matches repo-relative; git-dir-internal files reported gitdir-relative with
an explicit marker field (the current payload mixes three coordinate
systems); the worktree file listing runs under the pinned context (it is
cwd-scoped today, a scan-specific hole the scrub fix does not cover).

**Verify (red going green):** `TestScanSeesHooksInPrePrePushDir`. Controls
keep passing. NEW tests: scan from a subdirectory finds a root-level
worktree secret; coordinate fields.

---

## Phase 6 -- Sequencer conclusion

Depends on: 1.1/1.2/1.4/1.5 (sync deletion, reader, refusals, lock), 2.6
(multi-parent + index-base), 3.1 (quarantine, for previews), 0.7 (version
floors). The verb name used below is `conclude`; naming is confirmable at
review without design impact.

### 6.1 Plumbing prerequisites

- An index-copy constructor in `internal/index` (seed a temp index from a
  byte-copy of the shared index -- the merge state carrier).
- A `RunPassthrough` variant accepting env (`GIT_INDEX_FILE` for the
  delegated continue; `internal/git/git.go:406-417` takes none today).
- `MERGE_MSG` comment stripping via `git stripspace --strip-comments`
  (honors `core.commentChar`/`commentString`; no hand-rolled `#` rule).
- Readers for `AUTO_MERGE` blobs and `git merge-file` reconstruction
  (matching the repo's `merge.conflictStyle` and per-path
  `conflict-marker-size`), used by 6.3.
- Version-floor checks (0.7) wired: `merge-tree --write-tree`/`AUTO_MERGE`
  and `--attr-source` features refuse with the named floor on older git.

### 6.2 The conclusion verb

`safegit conclude --operation merge|cherry-pick|revert` (required
member-spelled choice -- intent is declared; a mismatch between the declared
operation and the actual state from the reader is a hard error). For merge
and SINGLE cherry-pick/revert it authors the commit natively through the
pipeline: temp index copied from the shared index; per-path declared
resolutions `--resolve 'path=ours|theirs|worktree|delete'` (repeatable
string flag with a registration-declared per-element validator -- the
framework's dict flag cannot validate values, a framework bug to report
upstream; a file-driven form `--resolve-file <toml>` covers large conflicts,
shaped like scrub run's recipe); a conclusion must name every conflicted
path (omissions and strays are hard errors listing them); `ours`/`theirs`
resolve from the index stage blobs, `worktree` hashes the on-disk file,
`delete` removes the entry. Completeness is enforced by `git write-tree`
refusing unmerged entries (free), with a readable pre-pass listing unmerged
paths. Parents: HEAD plus every `MERGE_HEAD` line (octopus included) for
merge; single parent with author preserved from the source commit for
pick/revert. Message: stripped `MERGE_MSG` by default, `-m` overrides;
`--trailer` accepted; the commit-msg hook runs (2.9); trailers injected;
CAS ref update under the worktree operation lock (outermost) plus the
per-ref lock; oplog op `conclude` registered in `undoableOps` (undo of a
conclusion rolls the ref back but cannot restore the sequencer state --
undo says so explicitly). Empty merges (tree equal to first parent) are
allowed without any flag -- the pipeline's tree-unchanged refusal is
bypassed for conclusions, deliberately. Tree-empty is normal for merges of
already-merged branches. After committing, the verb deletes the operation's
FULL state-file set (`MERGE_HEAD`, `MERGE_MODE`, `MERGE_MSG`,
`CHERRY_PICK_HEAD`, `REVERT_HEAD`, `AUTO_MERGE`) and reconciles the shared
index through the preserve helper. A stale `AUTO_MERGE` with no matching
operation state at verb start is a hard error (it would poison marker
verification). Detached HEAD is refused. The verb declares a JSON payload
schema.

**Verify (red going green):** `TestMergeCanBeConcludedThroughSafegit` (add
the verb's argv to the route table at `commit_merge_state_test.go:37-42`),
`TestMergeConflictTellsOperatorHowToConclude` (the conflict-path guidance
from `safegit merge` names the verb). NEW tests: octopus parents;
pick/revert author preservation; resolution completeness errors;
`--resolve-file`; empty-merge conclusion; state-file cleanup including
`AUTO_MERGE`; stale-`AUTO_MERGE` refusal; undo of a conclusion; two
sessions racing a conclusion (lock).

### 6.3 Marker verification

Layered, over EVERY staged path, no escape flag:

- **Primary -- emitted-region survival:** parse the conflict regions from
  what git itself wrote (`AUTO_MERGE:<path>` when present; otherwise
  reconstruct byte-identically with `git merge-file` under the repo's
  conflictStyle and the path's marker size). A region git emitted surviving
  verbatim in the resolution is a hard error (definitionally unresolved).
  When `AUTO_MERGE` is absent AND reconstruction is impossible (e.g. a
  strategy that leaves no stages), the verb hard-refuses rather than
  verifying less.
- **Secondary -- structural + counting differential:** complete marker
  regions (attribute-aware sizes) whose marker-line counts exceed the
  maximum across the stage 1/2/3 blobs (conflicted paths) or the first
  parent's blob (other paths) are hard errors. Pre-existing marker-shaped
  content (Markdown underlines, conflict-documentation fixtures) passes by
  construction because it exists in a parent.
- **Declared exemption:** a path with the `safegit-conflict-markers`
  attribute set to unset/false -- resolved from the FIRST PARENT'S tree via
  `--attr-source` (an exemption must predate the conflict; the operator at
  the wall cannot write it into an unstaged `.gitattributes`) -- is exempt.
  Every rejection prints the one-line attribute declaration that would
  exempt that path.

**Verify:** NEW tests -- forgotten markers hard-error naming path and line;
legitimate marker content from a parent passes; the byte-identical-block
case is refused (emitted-region layer); the committed exemption works and
an uncommitted one does not; `conflict-marker-size` respected; diff3 style
reconstruction.

### 6.4 Queued-pick delegation `[%%]`

When the reader reports a QUEUED cherry-pick (the `.git/sequencer` dir),
native authorship is refused and the verb delegates: the same resolution
staging into the temp index copy, the same completeness and marker checks,
then git's own `cherry-pick --continue` driven with `GIT_INDEX_FILE`
pointing at the copy (probed end-to-end: git advances its queue correctly
and leaves the shared index stale -- so the verb reconciles it through the
preserve helper afterward). The delegation is stated in output -- a declared
split by operation state, never try-and-fall-back.

**Verify:** NEW tests -- multi-pick with a mid-queue conflict concludes and
the queue completes; the shared index ends clean; output names the
delegation.

### 6.5 Passthrough text and `merge --continue`

`safegit merge --continue` stays refused by the coordination guard; the
refusal text (and the merge-conflict guidance from 1.4) names `conclude`.
The revert command is restructured for Phase 7's needs: a SINGLE-commit
`safegit revert` runs `git revert --no-commit` and concludes through the
pipeline (gaining trailers, CAS, oplog -- and the seam Phase 7 uses for
inverse move records); multi-commit revert remains a sequencer passthrough
(concluded via the verb like picks), stated in docs.

**Verify:** NEW tests -- `merge --continue` refusal names the verb;
single-commit revert produces a pipeline-authored commit with trailers.

### 6.6 Honest previews for merge, cherry-pick, revert

`--dry-run` on the three passthroughs computes the real outcome via
`git merge-tree --write-tree` (with `--merge-base` for pick/revert) under
the object quarantine: reports clean-vs-conflict and the conflicted path
list in the preview. Invocations `merge-tree` cannot faithfully compute
(strategy options, `--squash`, etc.) are refused at runtime with the reason
-- a hand-rolled per-invocation refusal (the framework only supports
per-command refusal; noted for migration when the framework ruling ships).

**Verify:** NEW tests -- clean-merge preview, conflict preview with path
list, unsupported-option refusal, object store untouched.

---

## Phase 7 -- Move records

Depends on Phase 2 (expansion, flags infrastructure) and 6.5 (revert
restructure); 7.5 depends on Phase 4.

### 7.1 The record format

In `internal/trailer` (plus a small helper for IDs):

- The `Moved:` trailer: one self-contained line per record, arrow form
  `old -> new` `[%%]`, with git-style C-quoting whose trigger is spec'd
  exhaustively (quote iff the value contains whitespace, a double quote, a
  backslash, any control byte, any non-ASCII byte, or the literal arrow
  token) -- byte-complete for any legal filename; JSON was rejected because
  it cannot carry non-UTF-8 paths. Encoder + decoder + a property-based
  round-trip test over random byte strings. No C-quoting helper exists in
  the repo today; both directions are new code.
- Subtree form: a trailing slash means everything under the prefix -- one
  record per directory move; per-file answers derived by prefix application
  at read time, validated against trees.
- Per-record ULIDs (hand-rolled Crockford base32 over `crypto/rand`; no new
  module dependency), as a trailing token or companion key per the encoder
  design.
- Retract-only corrections: a `Moved-Retract: <id>` trailer in a later
  commit; replacement = retraction + a new record in the same commit.
  Readers fold retractions when projecting.
- Projection semantics: records are CLAIMS; TREES ARE THE ARBITER -- a
  record whose old path is not in the commit's parent tree (or whose
  subtree expansion names files that never existed) is ignored at read
  time; at merges the surviving path in the merge tree decides.
  Longest-prefix wins between overlapping records; a file-form record never
  applies to descendants. No confidence field is stored (exact-vs-declared
  is recomputable from the trees). A key-value trailer parser (none exists
  -- `SplitBodyTrailers` returns an unparsed block) is part of this work.

### 7.2 Declared moves on commit and amend

`--moved 'old -> new'` (repeatable string flag; the value uses the SAME
grammar as the record, C-quoting included `[%%]`), accepting the
trailing-slash subtree form. Validation against the Phase 2 expansion: old
path (or prefix) tracked in the parent and absent from disk, new path (or
prefix) present/staged. Blob equality is NEVER used to decide whether a
record exists (the inference heuristic is dead); it is recomputable
evidence only. Amend accepts `--moved`, and amend/reword with `-m` PRESERVE
existing `Moved:`/`Moved-Retract:` trailers by re-appending them (dropping a
record requires explicit retraction) -- closing the silent record-deletion
path.

### 7.3 `safegit mv`

New top-level command: variadic arguments, each ONE quoted pair token
`'old -> new'` (same grammar again; the framework cannot express grouped
arity, so the pair-per-token rule is the declared shape). Multi-pair: ALL
pairs validated before the FIRST filesystem mutation (sources tracked and
present, destinations absent, no source doubling as destination, no pair
nested in another's prefix); filesystem moves minted through the effects
handle (`Rename` -- honest dry run for free); a mid-sequence failure rolls
back completed renames before erroring. Directories are accepted and emit
one subtree record. Case-only renames on case-insensitive filesystems
(detected via `core.ignorecase`) take an explicit same-file path. The
command commits (one commit per invocation, records included), registers
its own oplog op (undo rolls back the commit but does NOT restore the
filesystem moves -- consistent with undo's worktree-untouched contract, and
undo's output says so), declares a payload schema, and joins the pinned
command registries (`classification_test.go:70-117`; the command-count
string in `main.go:120` heals through the schema dump).

### 7.4 Inverse records on revert

Single-commit `safegit revert` of a commit carrying `Moved:` records emits
the inverse records (new -> old) in the revert commit it authors via the
6.5 pipeline path (projection stays correct through reverts).

### 7.5 Record-aware scrub

`scrub file` retracts/redacts records referencing the scrubbed path as part
of the same rewrite (message edits feeding the Phase 4 expectation set);
`scrub match`'s message transform becomes trailer-aware (it is a blind
regex today and would corrupt quoted values) using the body/trailer split
`filterTrailerMatches` already demonstrates (`scan_cmd.go:135-181`).

**Verify (whole phase):** NEW tests -- record round-trip property test;
`--moved` file and subtree forms with validation errors; multi-hop
projection (chain reconstruction); retraction folding; trees-as-arbiter
(hand-written garbage record projects as nothing; merge divergence resolves
by tree); mv multi-pair atomicity and rollback; mv directory subtree
record; case-only rename; amend preserves records; revert inverse; scrub
retraction and quoting-aware match. The inverted cross-session tests from
2.1 stay green (no record without declaration).

---

## Phase 8 -- Push and consent

Independent; needs 0.5 (registry) only.

### 8.1 The pinned lease `[%%]`

FIRST the red test: force-pushing tags with today's bare
`--force-with-lease` (tags have no remote-tracking refs, so git zeroes the
expectation and rejects existing remote tags) -- the currently-underived
failure that makes safegit's own post-scrub instruction unsatisfiable.
Then: build the lease per-ref as `--force-with-lease=<remoteRef>:<observed
SHA>` from the remote SHAs push ALREADY resolves (`push.go:273, 304, 338`),
translating the internal null-SHA "absent" marker to the EMPTY lease
expectation ("must not exist" -- a literal zero-SHA lease would reject every
new ref). `--atomic` is always on for multi-ref pushes (a half-pushed set is
never desirable; single-ref pushes are unaffected). The retry loop
RE-OBSERVES the remote and re-pins the lease on every attempt (a lease
pinned before attempt 1 goes stale if attempt 1 partially succeeded); a
lease rejection is terminal with an explanatory message, never retried as a
transport error.

**Verify:** the new tag-lease test red-green; branch lease against a moved
remote ref refuses; new-ref lease works; multi-ref failure pushes nothing.

### 8.2 Conditional consent

`push --force-with-lease` becomes conditionally consequential in the
established hand-rolled shape (the four-line `consent` +
`confirmDeliberate` pattern at `doctor.go:41-49` / `main.go:764-796`):
prompt at a terminal, `--approve-consequential` answers it, `--json`
refuses to answer, declining exits 1. Migration to a framework mechanism
awaits the strictcli ruling
(`todo/conditional-consequential-await-strictcli-ruling.md` already names
this instance).

**Verify:** NEW tests mirroring `confirm_deliberate_test.go`'s shapes for
the force path; unforced pushes prompt nothing.

### 8.3 Honest dry-run hook notice

The pre-pre-push hook skip under `--dry-run` (today one suppressible stderr
line at `push.go:100-102`) becomes user-visibly documented: stated in the
push help text and the preview output, and carried in the payload.

---

## Phase 9 -- Documentation healing

After behavior stabilizes (Phases 0-8), one healing pass over the EDITABLE
surfaces only: hand-written docs (`docs/architecture.md`,
`docs/commands-guide.md`, `docs/concurrency-guide.md`,
`docs/integration-guide.md`, `docs/index.md`, `docs/req.md`), the templates
(`docs/_CLAUDE.md`, `docs/_README.md`), and help strings in `main.go` --
never the chmod-444 generated files; finish with `--dump-schema` +
`selfdoc gen`.

- **architecture.md:** pipeline description updated to the rewritten
  reality (Phase A/B ordering text at :93-179 currently documents an
  ordering the code deliberately rejects); phantom exit codes 8-is-real-now,
  12/13 removed, the apply `--index` claim and invented hint text removed;
  hooks paths updated to `.git/safegit/hooks` + the tracked store
  (overturning the recorded note at :46-47 deliberately); the dead
  `--no-pre-pre-push` flag name fixed; commit-family hook execution
  described truthfully; the "rm -rf .git/safegit returns to vanilla git"
  claim now true.
- **commands-guide.md:** exit-code table regenerated against the registry
  (adds 8, 14, 22, 23, 70, corrected 40; removes 12, 13; states the 2-vs-1
  split honestly); dry-run section updated -- the "without writing any
  changes to disk" claim becomes true under the quarantine; the "dry runs
  never touch the network" OVERCLAIM is struck (safegit-authored, false
  today, and the stance belongs to the framework ruling --
  `backup backup`'s local-only preview is described as that command's
  behavior); scrub file mode-flag examples; hunk-flag grammar; untrack,
  moved, mv, conclude sections.
- **concurrency-guide.md:** the every-command `--no-optional-locks` claim
  (true after 0.2); the "operation in progress" description updated to the
  real two layers (dirty-tree check + worktree operation lock); the CAS
  belt-and-suspenders claim (true after 0.4); oplog paragraph updated
  (flock-based, no cap, no rotation).
- **integration-guide.md:** retired rlsbl references (`rlsbl push`,
  `pre-push-check`) replaced with current commands; hook install/discovery
  description corrected.
- **_CLAUDE.md template:** release-workflow section rewritten to the
  current rlsbl flow (JSONL changelog, `release run`); the
  git-plumbing-through-internal/git convention (true after 0.2); the
  architecture table gains `internal/filelock`, `internal/procutil`,
  `internal/exitcode`, `internal/sequencer`; command table and counts heal
  via the schema dump.
- **main.go help strings:** hook list/install (true after Phase 5), pull's
  false "defaulting to fast-forward-only" phrasing, unlock's "crashed git
  process" overclaim (it releases safegit's own locks), scrub match/file
  blast-radius wording, push force help stating the pinned-lease semantics.
- The exit-code decline row contradiction in the backup table; push's local
  exit table.

**Verify:** the docs-table pin test (0.5) passes; `selfdoc gen` runs clean;
a spot-check subagent replays the original docs-vs-code audit list and finds
every listed claim either healed or now true.

---

## Phase 10 -- Verification and audit

- **10.1 Full green:** `go test ./... -race` green;
  `go test ./internal/test/ -race -count=5 -timeout=15m` (the stress run)
  green; every investigation red test either green or deliberately
  rewritten per this plan (grep for any remaining FAIL and reconcile).
- **10.2 Fresh audit:** a fresh-context auditor (AUDIT protocol: spec-only,
  no git history, reads files on disk) audits the implementation against
  THIS FILE as the specification, item by item, all phases -- correctness,
  completeness, consistency; every failure fixed before release.
- **10.3 Changelog:** every commit since the last tag covered in
  `.rlsbl/changes/unreleased.jsonl` (`rlsbl changelog add`, batched with
  `--allow-batch` where a phase is one cohesive entry); `rlsbl check --tag
  changelog` green.

## Phase 11 -- Release

Todo triage first: the original bug/design todos this campaign resolves move
to `todo/.done/` (the commit-path bugs, symlinks, multi-message, untrack,
merge-commit, hook defects, reversibility's hook-remove half, the
effects-handle item 1, the move-records design todo per its final state);
items awaiting strictcli rulings stay active. Then `rlsbl release init`,
edit the release file (minor bump; description covering the campaign;
context block), commit it, and the single
`rlsbl release run --no-allow-dirty --watch --approve-consequential`.

---

## Dependency spine (summary)

Phase 0 unlocks everything. 1 needs 0.2/0.3. 2 needs 0 and lands after
1.3/1.4 (shared seams). 3 needs 0.2/0.3 + 2.8. 4 needs 0.3/0.5 + 2.8's
diff-tree. 5 needs 0.2/0.3. 6 needs 1.1/1.2/1.4/1.5 + 2.6 + 3.1 + 0.7.
7 needs 2 + 6.5 (and 4 for 7.5). 8 needs 0.5. 9 needs everything. 10-11
close. Phases 4, 5, 8 can run in parallel with their numeric neighbors;
the numbered order is safe sequentially.
