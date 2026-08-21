# The 2026-08 redesign campaign: implementation plan (revision 2)

This plan is self-contained: every decision it executes is stated in full,
and a session with zero conversation context can implement any subphase from
this file plus the cited code. File:line references were verified against
the working tree between HEAD `ecfec08` and `3e287a2` on 2026-08-21; expect
small drift. Revision 2 incorporates an adversarial plan review, an
empirical re-verification of two disputed claims, and a scrub-policy
assessment; the material corrections are marked inline.

**Status quo this plan starts from.** Seventeen investigation test files are
committed in `internal/test/` with ~45 deliberately failing (red) tests that
specify target behavior; the suite (and CI on main) is red on purpose until
the campaign completes. Nothing is pushed before the single release at the
end (the release's CI check runs on the candidate commit, by which point the
suite must be green).

**Decision-origin note.** Decisions marked `[%%]` were trust-adopted (a
recommendation accepted without deliberation) and are freely reversible.
Decisions marked `[adopted-from-instinct]` follow the user's stated
direction plus a supporting assessment but were not answered as an explicit
question; they are equally reversible on request. Everything else was
deliberate.

**Explicit non-goals (deferred, most with todos already filed):**
- Plan-file sequencer ownership (safegit computing merges via merge-tree
  into a tool-owned plan so git never sees a merge in progress) -- declared
  destination, separate future project.
- Rebase conclusion. The sequencer reader classifies mid-rebase state and
  safegit's own verbs refuse during it; concluding a rebase remains git's
  own `rebase --continue/--abort`. Honesty note: staging rebase resolutions
  requires `git add`, which fleet tooling blocks, so for agent sessions the
  rebase path retains a known friction this campaign does not fix; phase
  6.1 records the feasibility fact for the future extension.
- The hunk staleness re-check once documented as exit 13: NOT built. The
  window it guards (between diff extraction and `git apply --cached`) is
  harmless -- apply never reads the worktree -- and the dangerous window
  (the caller diffed minutes earlier) is invisible to a self-comparison. A
  caller-supplied `--expect-blob` pin remains a possible future design
  question. Exits 12/13 leave the docs in Phase 9.
- Windows support. Release targets AND the five `//go:build windows` source
  files are removed (Phase 0.7): with the oplog cap gone and the Windows
  flock a no-op, a from-source Windows build would have no oplog integrity
  at all -- unsupported is made structural (compile failure). The
  LockFileEx path is recorded in `todo/.defer/windows-lockfileex-support.md`.
- A derived local query index for move records -- built when a consumer
  needs it.
- Everything blocked on strictcli rulings (conditional consequential;
  exit-code registry; dry-run network-observe contract; effects-handle
  missing shapes) -- await todos exist; the campaign builds the hand-rolled
  forms and migrates later. Also to file upstream during Phase 6: the
  framework bug where a dict flag's ValidateFn is silently skipped.
- `backup restore --dry-run`'s network read: only the doc overclaim is
  struck (Phase 9); behavior awaits the strictcli dry-run ruling.

---

## Phase 0 -- Groundwork

Subphases 0.1-0.8 are logically independent, but several share files
(0.5 and 0.8 both edit `internal/lock`; 0.6 and 0.7 both edit `doctor.go`;
0.2, 0.3 AND 0.4 all rework `internal/git`; 0.5 and 0.6 both edit
`undo.go`): run overlapping groups sequentially or under one implementor;
the rest may run in parallel. The same rule applies across phases: 4.3,
5.2 and 5.5 all edit `doctor.go`, so the "phases 4, 5, 8 may parallelize"
note at the spine holds only if those subphases coordinate on that file.

### 0.1 Test-suite baseline and helper consolidation

FIRST, capture the baseline: run `go test ./internal/test/ -json` (and the
unit packages) and store the pass/fail set as an artifact under
`testdata/` so "unchanged pass/fail set" is checkable, not asserted. Then
consolidate the duplicated helpers (eight collision groups across the
seventeen investigation files: run-git-in-dir ~17 spellings;
write-repo-file 7; tree-path listing 8; slice-contains 6; merge-state
probes 5; conflicted-merge fixtures 3; rev/blob readers 9; run-safegit
variants). Placement rule: helpers that build or run the safegit binary can
only live in `internal/test` (the integration package); pure git/fs helpers
usable by per-package unit tests go to `internal/testutil`. No behavior
changes.

**Verify:** the suite compiles; a fresh `-json` run diffed against the
baseline artifact shows an identical pass/fail set.

### 0.2 One git-execution boundary and the argv classification table

The convention "all git plumbing goes through internal/git" is false at
four sites: `internal/submodule/submodule.go:261-263` (deliberate
import-cycle workaround), `autobump.go:25`, `main.go:704`, and
`coord_cmd.go:21-29` (`runGitMutation` builds git argv for the effects
handle directly). Correction from review: `git.RunPassthrough`
(`git.go:406-408`) already prefixes `--no-optional-locks`, so the prefix
gap is the six `runGitMutation`-built commands plus the two dry-run records
for cherry-pick/revert -- not all seven passthroughs.

Fix structurally:
- Extract low-level process construction (argv prefixing, env assembly, the
  context-carried overrides of 0.3) into a leaf package importable by both
  `internal/git` and `internal/submodule`; route the three stray callers
  and `runGitMutation`'s argv construction through it.
- Build ONE argv classification table in the boundary package -- the single
  authority over git argv vocabulary -- with three consumers: this
  subphase's guard, Phase 3.1's object-writing list, and Phase 3.3's
  observe-allowlist prefixes. One table, three views.
- Guard test: no `exec.Command` of git AND no `[]interface{}{"git", ...}`
  argv literal outside the boundary package(s). Scope rule: `_test.go`
  files and the test-only `internal/testutil` package are exempt wholesale
  (tests exercise git directly by design); production packages are not.

**Verify:** the guard test passes; would-do argv recorded by the effects
handle for passthrough dry runs carries `--no-optional-locks` (assert in
the effects-regime tests); `docs/concurrency-guide.md:113`'s claim becomes
true.

### 0.3 Root-pinned execution context, full-tree listings, Go-side anchoring

- Build the execution context once at dispatch (a helper on `globalFlags`
  replacing the ~37 bare `context.Background()` calls): it carries the
  repo-root pin (the `git.WithDir` machinery, `internal/git/git.go:28-47`).
  Correction from review: of the eight exec sites (`git.go:58, 82, 408,
  735, 766, 862, 884, 911`), only FIVE call `applyDirOverride`; the three
  `*WithDir` variants (`:862, :884, :911`) set `GIT_DIR`/`cmd.Dir`
  explicitly from their own arguments. The pin applies at the five; the
  three `*WithDir` variants are exempt-by-construction for the DIRECTORY
  pin (they take explicit dirs) and are listed as such in the declared
  exemption table -- but they must NOT be exempt from the other
  context-carried overrides (Phase 3.1's object quarantine applies at ALL
  eight).
- Passthrough commands that legitimately observe the operator's cwd get a
  DECLARED EXEMPTION TABLE in code, with a test enumerating it -- not
  prose.
- `git.LsTree` (`git.go:655`) and `git.LsTreeAll` (`git.go:644`) gain
  `--full-tree` (pinning cwd alone does not fix tree listing).
- CRITICAL addition from review: subprocess pinning does not change
  GO-SIDE relative-path resolution. Add one root-anchoring helper
  (join repo root with a repo-relative path) and apply it at every
  filesystem syscall that consumes git-listed paths:
  `internal/git/git.go:365` (`os.Lstat`), `:372` (`os.ReadFile`), `:388`
  (`os.WriteFile`) in the worktree-sync save/restore loop, and
  `internal/scan/nonobject.go:70`. Without this, the tracked-ignored
  protection still silently fails from a subdirectory and one of this
  subphase's own red tests cannot pass.
- User-typed relative path arguments keep resolving against the invoking
  cwd at intake (already correct).

**Verify (red going green):** `coord_subdir_test.go` --
`TestCoordSubdirCheckoutRefusesUntrackedFromSubdir`,
`TestCoordSubdirResetHardRefusesUntrackedFromSubdir`,
`TestCoordSubdirSkipWorktreeSurvivesCommitFromSubdir`,
`TestCoordSubdirScrubProtectsTrackedIgnoredFromSubdir`,
`TestCoordSubdirScrubFromSubdirPreservesHistoryPaths`; all four red tests in
`scrub_subdir_test.go`; `TestCommitFromSubdirRelativePathNoPendingChange`
and its amend twin (`amend_parity_test.go:335`). All root-invoked green
controls keep passing.

### 0.4 The ZeroSHA contract

`git.UpdateRef` (`git.go:177-184`) omits the old-value argument when empty
-- an unconditional write (the root-commit CAS hole;
`internal/commit/commit.go:324` passes empty for root commits, and the doc
comment promises create-only semantics the code lacks). Change the
contract: `UpdateRef` and `DeleteRef` hard-error on an empty old value;
export `git.ZeroSHA` (git's "must not exist" convention -- git refuses with
"reference already exists", which `isTransientRefError` already classifies
for retry); the root-commit site passes `git.ZeroSHA`. Consolidate the four
null-SHA literals (root `commit.go:142`, `push.go:36`, `push.go:352`,
`hook.go:179`).

**Verify:** `TestRootCommitDoesNotClobberRefCreatedInWindow` goes green;
`TestRootCommitZeroOldValueRefusesExistingRef` and
`TestRootCommitConcurrentSafegitBothLand` keep passing; NEW unit tests pin
the contract itself -- `UpdateRef` AND `DeleteRef` error on an empty old
value, and `ZeroSHA` means create-only.

### 0.5 The exit-code registry

Correction from empirical re-verification: exit 40 IS produced today
(`push.go:190` returns it on the post-retry-loop path; `main.go:224`
propagates; confirmed against a dead remote and a non-fast-forward
rejection) -- earlier claims that it was dead were misled by unreachable
`return 1` lines sitting after `die()` calls. And `undo.go:184-187` already
surfaces the real lock error verbatim; it lacks only a typed code.
Therefore:

- FIRST, generate the exit-site inventory MECHANICALLY (every `die(`,
  `os.Exit(`, nonzero handler return, with its literal), review it, THEN
  write `internal/exitcode`: every code a named constant with a doc
  comment; all ~240 sites routed through it (existing constants live in
  `internal/commit/commit.go:26-30`, `push.go:19-23`, `backup.go:17-23`,
  plus bare literals like `coord_cmd.go:42`).
- GENERATE the exit-code table in `docs/commands-guide.md` from the
  registry (or pin it with a bidirectional test) so the table cannot drift.
- Exit 8 (lock acquisition timeout): type the timeout error in
  `internal/lock`; the four rewrite commands (`scrub.go:218`,
  `scrub_match.go:189`, `scrub_run.go:277`, `rewrite_author.go:146`)
  currently DISCARD the error behind a fixed "another rewrite operation is
  in progress" string -- they must surface the real error AND the typed
  code; `undo` gains the typed code only.
- Exit 14 (binary file + hunk spec): export the sentinel at
  `internal/stage/stage.go:45-47`. Precondition: convert the three bare
  `CommitError` type assertions (`commit.go:87, 187, 251`) to `errors.As`
  (the binary error arrives wrapped at `internal/commit/commit.go:222-224`).
- Exit-2 stance unchanged (deferred to the strictcli usage-code ruling);
  the generated table states the split honestly.
- STANDING RULE for all later phases: every new hard error introduced by
  this campaign (operation-lock timeout, no-match pathspec, untrack typo,
  scrub Tier A/B, legacy-location hooks, non-executable tracked hook,
  resolution completeness, marker verification, stale AUTO_MERGE, lease
  rejection) registers its code here as part of its own subphase.

**Verify:** the generated/pinned table matches the registry; red-green
tests for exit 8 (live lock + short timeout) and exit 14; no test asserts a
code the registry does not define.

### 0.6 Oplog integrity

- Remove the 4096-byte line cap (`internal/oplog/oplog.go:17, 52-55`) --
  the flock is the integrity mechanism, matching the scrub journal's
  reasoning at `rewrite_maps.go:16-19` (rewrite that comment). Replace the
  line-capped `bufio.Scanner` in `Read` with a `bufio.Reader`-based loop
  (no bound exists once the cap is gone).
- `oplog.Read` returns a skipped-unparseable-line count (signature change;
  four non-test consumers: `undo.go:59`, `undo.go:251`, and the internal
  `LastRefUpdate`/`LastRefUpdateForSession` at `oplog.go:161/:188`, which
  FAIL CLOSED -- they return an error on a nonzero skip count, since
  bypass detection and undo arithmetic rely on completeness). `safegit
  undo` hard-errors on a nonzero count; `doctor` diagnose reports it.
  Correction from review: doctor is ALREADY an oplog consumer beyond
  `LogSize`/`Rotate` -- the bypass-detection check reads
  `oplog.LastRefUpdate`/`TipSHA` at `doctor.go:142-144`, and its current
  guard would silently SKIP the check when the read errors. Specify:
  under a skip-count error, bypass detection reports the oplog corruption
  as its own FAILING finding (the corrupted-oplog case is exactly when
  bypass detection is most wanted; a check silently disabled by a runtime
  condition is the banned silent-degradation shape).
- While first touching doctor here, convert its inline sequential check
  appends into a small check-REGISTRATION table (name, severity, fn) --
  five later subphases (0.7, 1.5, 4.3, 5.2, 5.5) add doctor checks, and
  the table turns each into an isolated entry instead of a hand-merged
  edit of one long function.
- Delete rotation entirely: `Rotate` + `LogSize` (`oplog.go:105-153`),
  doctor wiring (`doctor.go:260-283, 301-309, 323-325`), and the
  `log.maxSizeMB` config key everywhere (`internal/repo/repo.go:22, 51-54,
  64, 232, 293-294, 335-336, 351`). Existing config files carrying the key
  still parse (plain `json.Unmarshal`); `config set log.maxSizeMB` starts
  erroring "unknown config key" (correct pre-stable). Update
  `internal/repo/repo_test.go:237-238, 257` and the fixture at
  `internal/test/stress_test.go:662`; invert `TestAppendRejectsOversizedLine`
  (`internal/oplog/oplog_test.go:84`).
- The scrub-side oplog change (patterns no longer recorded verbatim) is
  Phase 4.3, not here.

**Verify:** an append well over 4096 bytes survives a read-back; `undo`
refuses on a corrupted log line; doctor reports the count.

### 0.7 Platform and repo hygiene

- Windows: remove `windows` from `.goreleaser.yml:12` and the zip override
  at `:20-22`, mirrored in `.rlsbl/bases/.goreleaser.yml` in the same
  commit; DELETE the five `//go:build windows` source files
  (`doctor_windows.go`, `internal/filelock/locked_append_windows.go`,
  `internal/hooks/hooks_windows.go`, `internal/lock/cleanup_windows.go`,
  `internal/procutil/alive_windows.go`) so `GOOS=windows` fails at compile
  time -- unsupported made structural. Deferred restoration path:
  `todo/.defer/windows-lockfileex-support.md`.
- CI: THREE push-triggered workflows are all named `CI` (`ci.yml`,
  `ci-go.yml`, `ci-docker.yml`), making the release CI-check resolution
  ambiguous and doubling the test wait. Resolution: the scaffold-managed
  test workflow (`ci-go.yml` is in `.rlsbl/managed-files.json`; the
  hand-made `ci.yml` is not) absorbs the linux+macos MATRIX content via
  the scaffold base; the hand-made `ci.yml` is deleted; the docker
  workflow is renamed (its `name:` field) so exactly one workflow is
  named `CI`. Note `ci-docker.yml` is ALSO scaffold-managed
  (`.rlsbl/managed-files.json`), so its rename goes into the scaffold
  base, not the generated file, or a future three-way merge reverts it.
  Stress-test skips are re-keyed from `-short` to the explicit opt-in
  environment variable `SAFEGIT_STRESS=1` (set by the stress script) so
  CI can drop `-short` and run the full integration suite within its
  timeout; `scripts/stress` and `docs/_CLAUDE.md:34-37` updated.
- Fix the non-atomic `config.json` write: `repo.Init`
  (`internal/repo/repo.go:146`) plain-writes while `IsInitialized`
  (`repo.go:104-107`) stats the same file (observed flake: "unexpected end
  of JSON input" on concurrent first init). Write via `os.CreateTemp` in
  the same directory (per-process unique -- a fixed temp name would
  recreate the race) and rename.
- Git-version floor helper: parse-and-compare with per-feature floors
  (Phase 6 needs `merge-tree --write-tree`/`AUTO_MERGE` ~2.38 and
  `--attr-source` ~2.40); features refuse with the named floor on older
  git; doctor reports the git version against the highest floor.

**Verify:** goreleaser lists linux+darwin in both copies and
`GOOS=windows go build ./...` fails; exactly one workflow named CI and one
push-triggered test workflow with the matrix; a parallel first-init test
with a stated iteration count and parallelism (e.g. 50 iterations x 8
concurrent) shows no flake -- with the 10.1 stress run named as the real
statistical check; version-floor unit tests.

### 0.8 Lock staleness: stop stealing live locks

Bug found during re-verification: `processStartedAfterLock`
(`internal/lock/lock.go:239-250`) compares the lock file's mtime against
`os.Stat("/proc/<pid>").ModTime()` as a PID-reuse test -- but that
directory's mtime is NOT the process start time and advances during the
process's life, so a LIVE holder is classified as a reused PID and its
lock is SILENTLY DELETED; the second operation proceeds concurrently. In
naive contention tests the lock was stolen every time. Fix: record the
holder's true start time in the lock file at acquire (read from
`/proc/<pid>/stat` field 22, converted via boot time and clock ticks) and
compare against the CURRENT process-start reading at staleness time;
absent or unparseable start info fails CLOSED (not stale). Keep the
hostname refusal as is. Parsing trap, stated so nobody re-derives it: the
stat file's comm field can contain spaces and parentheses, so fields are
indexed only after splitting at the LAST `)` -- naive whitespace
splitting mis-indexes everything.

**Verify:** NEW red-green test -- a lock held by a live process is never
removed as stale regardless of file mtimes; a genuinely dead holder's lock
still is; PID-reuse simulation (dead holder, new unrelated live process
with the same recorded pid) is correctly treated as stale; a holder whose
process name contains spaces and parentheses parses correctly.

---

## Phase 1 -- Shared-index ownership and sequencer state

Depends on 0.2/0.3 (the preserve helper's listing and Go-side anchoring)
and 0.8 (locks it relies on).

### 1.1 Delete the post-passthrough index sync

During a passthrough, git owns the shared index; the sync repairs nothing
and destroys sequencer state. Delete `syncMainIndex` (`coord_cmd.go:47-55`)
and all call sites: checkout `:89`, pull `:152`, merge `:193`, rebase
`:234`, reset `:280`, bisect `:326`, `runGuardedPassthrough` `:373`; also
the sync after `backup restore`'s `--ff-only` merge (`backup.go:395`).
While there: ALL `runGitMutation` callers stop returning a hardcoded 1 and
propagate git's real exit code -- that is EIGHT sites (checkout `:82-84`,
pull `:129-131` and `:145-147`, merge `:186-188`, rebase `:227-229`, reset
`:272-274`, bisect `:319-321`, and `runGuardedPassthrough`'s dry-run
branch `:365-366`, which keeps returning 0 since no git ran -- stated so
the count is exhaustive); verify FIRST whether the error returned by the
effects handle's Run carries the child's exit code, and if not, capture it
from the completed result -- a recorded verification task, not an
assumption.

**Verify (red going green):** all seven red tests in
`sequencer_conflict_test.go` plus
`TestGuardedPassthroughKeepsCherryPickConflictStages`. The two green
controls keep passing. NEW: a passthrough failure's exit code equals git's.

### 1.2 The sequencer-state reader

New package `internal/sequencer`: the single authority for in-flight
operation state. Typed results: merge (all `MERGE_HEAD` lines, message
file), single cherry-pick or revert (source commit, author, message file),
queued cherry-pick (discriminator: `.git/sequencer` exists -- probed;
single conflicted picks have `CHERRY_PICK_HEAD` + `AUTO_MERGE` and no
sequencer dir), rebase (`rebase-merge` AND `rebase-apply` variants), none.
It owns each operation's full state-file set (`MERGE_HEAD`, `MERGE_MODE`,
`MERGE_MSG`, `CHERRY_PICK_HEAD`, `REVERT_HEAD`, `AUTO_MERGE`, sequencer
dir) and exposes ONE cleanup function that removes an operation's set --
the single implementation later used by the continue commands and the
restructured revert.

**Verify:** unit tests per state including the queued-vs-single
discriminator, octopus (multi-line MERGE_HEAD), BOTH rebase variants, and
the cleanup function removing exactly the right set.

### 1.3 The preserve helper

One helper in `internal/git` (importable by both the pipeline and the
continue commands): snapshot the shared index's delta against the
pre-operation tip (including unmerged stage 1/2/3 entries, `ls-files -s`
under the pinned context), sync, replay via one `git update-index
--index-info` batch (unmerged replay first clears the stage-0 entry the
sync wrote -- a zero-mode removal line; probed). Replay failure is a HARD
error -- and the helper is the SINGLE index-reconciliation authority: the
skip-worktree preservation logic moves INTO it (out of
`syncMainIndexInner`, `git.go:266-271, 292-298`) with the same hard-error
stance (the current skip-worktree restore is warn-and-continue at
`git.go:294-297`; that softness is removed with the move). Adopt at:
commit (`internal/commit/commit.go:334-338`), amend (`amend.go:230-235`),
reword (`amend.go:389-393`), undo (`undo.go:201-209`, including root
undo's empty-tree case). Rewrite the two change-detector assertions in
`commit_untrack_gitignored_test.go:146-152` (deliberate change alarms for
exactly this).

**Verify:** `TestUndoPreservesForeignStagedState` green;
`TestUndoLeavesWorkingTreeIntact` and the `skipworktree_test.go` pins keep
passing; NEW: foreign staged state (modification + addition + rm-cached
deletion) survives commit, amend, AND reword; NEW direct unit test --
construct stage 1/2/3 entries via `update-index --index-info`, run the
helper, assert byte-identical `ls-files -s` after (the unmerged-replay
machinery must be exercised here, not first in Phase 6); plus a
randomized-index property test (random mixes of stage-0/1/2/3 entries,
modes, and pathological names round-trip byte-identically) -- this helper
is the single reconciliation authority for every command, and a bug here
corrupts users' staged work.

### 1.4 Mid-sequencer hard refusals -- as a declared pipeline input

While the reader reports in-flight state, `commit` (pathspec and
`--allow-empty` forms), amend/reword, and `undo` hard-refuse, naming the
state and the working way out (the operation-specific continue command;
git's own rebase commands for rebase). The refusal is implemented as a
DECLARED FIELD on the commit request (a sequencer context): absent means
"refuse if any state exists" (every ordinary caller), present means the
caller IS the conclusion path for that state (supplied only by the Phase 6
continue commands and the restructured revert). This resolves the
otherwise-fatal contradiction between these refusals and Phase 6's need to
commit during exactly that state. Refusals live in the pipeline, covering
the submodule auto-bump self-spawn. `coord.DirtyState.Refuse`
(`internal/coord/coord.go:55-70`) becomes sequencer-aware.

**Verify (red going green):** `TestCommitWithPathspecRefusedDuringMerge`,
`TestCommitAllowEmptyRefusedDuringMerge`, `TestUndoRefusedMidMerge` (HEAD,
MERGE_HEAD, stages untouched), `TestAmendRefusedWhileMerging`,
`TestAmendRefusedWhileCherryPicking`. Assertions by substance.

### 1.5 The worktree operation lock

Worktree-scoped lock (the `lock.Acquire` primitive with `repo.SafegitDir`
as base) serializing every tree-mutating passthrough AND -- closing the
review-found TOCTOU -- acquired by `commit`, `amend`/reword, and `undo`
around their sequencer-state read and operation (otherwise a concurrent
passthrough can create sequencer state between a commit's check and its
ref update). Ordering declared once: operation lock OUTERMOST, per-ref
lock inside. Timeout: the existing `lock.acquireTimeoutSeconds` key.
Extend `safegit unlock` with an explicit naming grammar for non-ref locks
(worktree-local and pseudo-ref names; today `unlock.go:20-22` prefixes
`refs/heads/` onto any argument lacking a `refs/` prefix, which is why the
`safegit/rewrite` lock is unreachable -- fixed in the same pass) and
extend `doctor`'s lock handling to both lock trees in BOTH paths: the
diagnose scan (`doctor.go:98-113`) and the fix-path counting
(`doctor.go:257, 312`). Note: a passthrough holds the
lock for its full duration, including `rebase -i`'s editor -- documented.

**Verify:** NEW -- two concurrent passthroughs in one worktree serialize; a
commit and a conflicted passthrough in one worktree serialize; stale
operation locks recoverable via `unlock` and listed by `doctor`; the
pseudo-ref rewrite lock is now unlockable.

(The former 1.6 -- scrub's pre-sync cleanliness re-check -- moved into
Phase 4.1 so `Finalize` is restructured once.)

---

## Phase 2 -- Commit pipeline core rewrite

One coordinated pass over `internal/commit` per file group; lands after
1.3/1.4. The reporting block (`commit.go:108-122` root, and the amend twin
`:209-227`) is edited ONCE in this phase (2.1's deletions and 2.8's rewrite
are the same edit), structured so Phase 3.2 later touches only the
dry-run branch.

### 2.1 Delete automatic move detection

Delete `internal/commit/moves.go` and call sites (`commit.go:235`,
`amend.go:171`), the `AutoStagedDeletions` members (`commit.go:79`,
`amend.go:41`), stderr notices and count contributions (root
`commit.go:119-121, 224-226`, folded into 2.8's block rewrite). Convert
the eleven green `TestMoveDetection_*` tests into removal-regression tests
(deletions NOT auto-staged); invert the six red
`TestCrossSessionMoveDetection_*` tests into cannot-happen guards.

**Verify:** inverted cross-session tests pass (no adoption, no rename
notice, victim can commit its own deletion); removal-regression tests
pass; `TestIntakeEdgeDanglingSymlinkNoColon` goes green as a side effect.

### 2.2 Canonical paths, directory expansion, no-match errors, parent-ref validation

- One normalization point in `resolveFiles`
  (`internal/commit/commit.go:365-420`) producing canonical
  slash-separated REPO-RELATIVE paths; absolute derived only at syscall
  boundaries via the 0.3 anchoring helper.
- Directory expansion at intake `[%%]`: union of on-disk contents and the
  commit's parent tree under the prefix (deletions included), never
  descending into gitlink/submodule boundaries. Gitignored files under an
  expanded directory are SKIPPED silently (matching git's own directory
  semantics -- expansion produces names the caller never typed, so the
  per-path ignore refusal applies only to EXPLICITLY NAMED paths; tracked
  ignored files still expand, since they are already in the parent tree).
- A named path or directory contributing nothing is a HARD error naming
  the path -- covering all three current inconsistencies: vanished dirs
  (early error stays), existing-but-empty dirs (late misleading error
  becomes early), and named-but-unchanged files (silent no-op becomes an
  error).
- Tracked-path validation against the commit's ACTUAL parent ref:
  `git.IsTracked` (`git.go:217-224`) takes a rev; other callers pass HEAD
  explicitly.

**Verify (red going green):**
`TestCommitStagedDeletions_DirectoryPathWithMovedFile`,
`..._DirectoryPathWithUnrelatedEmptyFile` + amend twins;
`TestIntakeEdgeCrossBranchDeleteTrackedOnlyOnTarget`, `...OnlyOnHead`,
`...AmendDeleteTrackedOnlyOnTarget`,
`TestAmendCrossBranchDeletionOfPathTrackedOnlyOnTarget`. Green controls
keep passing. NEW: no-match hard error for a file, an empty directory, AND
a named-but-unchanged file; expansion stops at a submodule boundary; a
directory containing untracked gitignored files commits its other contents
with the ignored files skipped, while explicitly naming an ignored file
still refuses.

### 2.3 Symlink policy

Final path component never resolved (`resolveSymlinks`,
`commit.go:426-436`, restricted to parent components); symlinks stage as
120000 objects; escaping targets allowed with a one-line stderr notice;
directory-symlink arguments use trailing-slash disambiguation (bare = the
link object, slash = through it); hunk specs on symlinks refused; dangling
symlinks committable.

**Verify (red going green):** `TestCommitSymlink_LinkToCommittedFile`,
`TestCommitSymlink_MixedWithRegularFile`,
`TestAmendSymlink_LinkToCommittedFile`,
`TestIntakeEdgeColonNameBrokenSymlink`. NEW: escaping-target notice;
trailing-slash both meanings; hunk-on-symlink refusal.

### 2.4 The hunks flag

`--hunks 'path:1,3'` (StringFlag, Repeatable, Unique(true), Optional,
per-element ValidateFn splitting on the LAST colon; no filesystem probe
ever). Positional arguments always literal. `FileSpec` internals
(`internal/commit/commit.go:51-55`) unchanged. Deliberately rewrite the
green pins of the old grammar: `TestIntakeEdgeHunkSpecFromSubdir`,
`...FromRoot`, `TestIntakeEdgeSameArgvDifferentMeaningByCwd`,
`TestIsHunkSpec`, `TestParseFileSpecs` (`main_test.go:51, 80`), and the
hunk invocation in `dryrun_object_purity_test.go:170`.

**Verify (red going green):** `TestIntakeEdgeColonNameDeletion`; rewritten
pins pass under the new grammar.

### 2.5 The untrack flag

`--untrack <path>` (repeatable) on commit and amend: removal-from-index
while the file stays on disk; general scope with a tracked-in-parent guard
(typos are hard errors); an informative line when the target is not
gitignored; the gitignored-path refusal (`commit.go:410-415`) stops
applying to `--untrack` targets, still blocks adding ignored content.
Rewrite the two red tests asserting a flagless form
(`TestCommitUntrackGitignoredPath`, `TestAmendUntrackGitignoredPath`) to
use the flag; the flagless form stays refused.

**Verify:** rewritten untrack tests green (one commit: .gitignore edit +
index removal; file on disk); NEW: non-ignored untrack works with the
line; untrack of an untracked path hard-errors.

### 2.6 Multi-parent and index-base pipeline inputs

The commit request carries a parents slice and an explicit index-base
selector (parent-tree default; shared-index-copy mode consumed by Phase
6). `git.CommitTree` (`git.go:163-173`) becomes multi-parent (unify with
`CommitTreeWithAuthor`, `git.go:573-594`, which already loops parents).
Amend/reword read the full parent list via `git.ParseCommit`
(`git.go:484-491, 517`) instead of `ref^` (`amend.go:125, 343`).

**Verify (red going green):** the three merge-parent tests in
`amend_parity_test.go:536, 575, 609`.

### 2.7 Message joining

Repeated `-m` values join with a blank line, via one shared helper used by
the three sites (root `commit.go:55, 162, 234`). Update the `-m` help
(`main.go:182`) and the commands-guide mention.

**Verify (red going green):** all three tests in
`commit_multi_message_test.go`.

### 2.8 Truthful reporting: changed-path list, count, JSON payload

New `diff-tree` wrapper in `internal/git` (recursive name-status between
two trees; also consumed by Phase 4's preservation check). Derive the
changed-path list ONCE per commit/amend/reword; the human line prints its
length; amend gains the COUNT it lacks (it already prints ref/sha/subject).
Declare a JSON payload schema for `commit` (pattern:
`versionPayloadSchema`, `main.go:677-687`): ref, parents, tree, sha (null
under dry run), files, attempts, dry_run. Nothing counted from arguments.

**Verify:** the count assertion in `TestCommitSymlink_MixedWithRegularFile`
green; `TestMachineModeReachesEveryCommand` covers the payload; NEW:
payload shape for commit and amend.

### 2.9 Commit-family git hooks and auto-bump ordering

- `commit-msg` runs on the user's composed message BEFORE trailer
  injection, once per commit (outside the CAS loop -- and `pre-commit`
  moves out of the loop too; it currently re-runs per attempt at
  `internal/commit/commit.go:242-250`); nonzero aborts; the
  possibly-rewritten message is re-read. `post-commit` (fire-and-forget)
  after the ref moves. Amend and reword gain the same execution. All three
  skipped under `--dry-run` (as pre-commit is today), noted in the
  preview. `prepare-commit-msg` never runs (docs in Phase 9; doctor in
  Phase 5.5).
- Submodule `autoBumpParent`: the key's PRESENCE mandatory, validated in
  the ROOT HANDLER before `p.Execute` is called (the parent detection and
  config read are main-package machinery; today the error at
  `autobump.go:144-146` fires after the ref moved). Explicit `false` stays
  legal. Dry runs ALSO validate (an early refusal is an honest preview;
  today `--dry-run` returns before the config read and never sees the
  missing key). Rewrite `TestAutoBumpConfigAbsentErrors`
  (`submodule_test.go:1826` currently asserts the commit DID happen); the
  other eight auto-bump tests keep passing.

**Verify:** NEW -- commit-msg rejection aborts with no commit; commit-msg
rewriting honored, trailers survive; post-commit once per real commit,
never in dry runs; amend runs the hooks; absent-key refusal with NO
submodule commit made, in both real and dry runs.

---

## Phase 3 -- Dry-run honesty and effects wiring

Depends on 0.2/0.3 and 2.8.

### 3.1 Object quarantine with enforcement

- Context-carried object-directory override in the boundary (sibling of
  `WithDir`), applied at ALL EIGHT exec sites -- including the three
  `*WithDir` variants (`git.go:862, 884, 911`), which per the 0.3
  correction do NOT run `applyDirOverride` today and need the quarantine
  env added explicitly (they are the submodule scan/cat-file paths, so a
  submodule scrub preview would otherwise escape the quarantine):
  `GIT_OBJECT_DIRECTORY` to a throwaway dir; APPEND the repo's object dir
  to any inherited `GIT_ALTERNATE_OBJECT_DIRECTORIES` (Go's env dedup
  keeps the last occurrence -- blind append clobbers).
- Installed at handler entry for every previewing command, BEFORE the
  first git call (`git.RepoRoot` at `internal/commit/commit.go:85`
  currently precedes preview-dir creation; a missing quarantine dir makes
  git fail repo discovery). Quarantine lives inside the auto-cleaned
  preview area (`indexBaseDir`, `commit.go:153-169`) with HANDLER-scope
  lifetime (not per-CAS-attempt); the `dryrun_test.go` pins (one
  `safegit-preview-*` dir, nothing under `.git/safegit`) keep holding.
- ENFORCEMENT via the 0.2 argv table's object-writing view (add, apply
  --cached, write-tree, commit-tree, hash-object -w, mktree, merge-tree
  --write-tree): in preview mode, any of these running without an active
  quarantine is a hard error.
- Reword's preview becomes a pure computation (no object writes at all).

**Verify (red going green):** all three `dryrun_object_purity_test.go`
tests. NEW: the enforcement refusal fires for an unquarantined object
write in preview mode; a SUBMODULE scrub preview leaves both object
stores untouched (covers the `*WithDir` paths).

### 3.2 Honest preview reporting

- Retire the `[main <sha>]`-shaped line in dry runs (committer timestamps
  make the SHA unknowable); print a would-commit line with file count and
  TREE sha.
- The minted `update-ref` record carries a preview placeholder instead of
  an invented commit SHA and gains `--no-optional-locks` so the log prints
  the argv the execute path runs; fix the identical defect in the four
  `scrub_preview.go:47-62` records (sibling rule). Rewrite the assertion
  at `effects_regime_test.go:255`.
- The commit payload's `sha` is null under dry run.

**Verify:** rewritten effects-regime assertions; NEW: dry-run human output
has the would-commit form and no fake commit line; payload sha null,
dry_run true.

### 3.3 Pipeline onto the effects handle

Mint `update-ref` through the handle in BOTH modes, with the MECHANISM
stated (the code's own comment at root `commit.go:129-136` names the trap:
a handler-side mint alongside the pipeline's own update would double-fire
and break CAS): the effects handle is threaded into the pipeline (direct
threading, decided earlier) and the pipeline's ref update INSIDE the CAS
retry loop goes through the handle's Run -- in real mode the handle
executes the very update-ref the loop needs (CAS argv unchanged, retries
work because a real failure returns an error); in dry mode the first
recorded mint returns nil and the loop exits after one iteration.
`recordCommitRefUpdate` (root `commit.go:126-148`) is DELETED, not
modified -- there is exactly one mint site, inside the loop. Declare the
observe allowlist (`WithProcObserveAllowlist`) generated from the 0.2 argv
table's read view -- every prefix spelled with the literal
`--no-optional-locks` element, at least two tokens. Ref-lock
exclusive-create and oplog append stay OFF-handle with reasons stated in
code (framework shapes absent; todo filed; dry mode never reaches them).
Hunk staging's stdin call stays off-handle too (Run has no stdin) -- it
executes for real under the quarantine in dry mode; only the ref update is
a recorded mutation. Observe-staleness constraint: the mint remains the
LAST effects action on the dry path (post-mint observes return stale
values in dry mode).

**Verify:** effects-regime suite green with the new wiring;
`--dump-schema` shows the allowlist; a dry-run commit's envelope carries
exactly one would-do record plus preview data; NEW real-mode assertions --
a real commit performs exactly ONE ref update, records exactly one
mutation, and the CAS retry path still succeeds under contention (extend
the existing concurrency tests to assert the recorded-mutation count).

---

## Phase 4 -- Scrub integrity

Depends on 0.3, 0.5, 1.3 (the preserve/reconciliation authority), 2.8's
diff-tree.

### 4.1 Two-tier verification, hard-erroring, ordered before refs move

Restructure `RewriteResult.Finalize` (`rewrite_result.go:92-272`) ONCE,
with two verification hooks:

- **Tier A -- pre-refs, aborting.** Runs BEFORE `captureRemoteTrackingState`
  (`:96`) and before the journal `start` record (`:109-124`) -- an abort
  must not read as a crashed rewrite. Rewritten commits exist as
  unreachable objects; nothing has moved. All hard errors, original
  history untouched:
  - **Preservation check (the rewritten-set expectation check):** each
    scrub executor produces an INTENDED-CHANGE MAP -- per old commit, the
    paths whose blobs the operation determined must change, plus whether
    the message changes (`scrub file`: the one target path;
    match/run/recipe: the per-commit blob and message decisions the
    executor already computes but currently only counts,
    `rewrite_result.go:70`). `Finalize` diffs each old/new pair (one
    diff-tree per pair -- the current check at `scrub_verify.go:122-181`
    materializes trees with two ls-tree calls per pair plus parses;
    replaced) and hard-errors when the changed-path set differs from the
    intended map.
  - **`scrub file` content verification, both modes:** delete -- target
    absent from every rewritten tree; replace -- new content present, old
    blobs unreachable in the new trees.
  - **Rewrote-count tripwire:** the intended map's commit set vs the
    actually-rewritten set. Genuinely empty operations: `scrub file` with
    a target absent from all history = hard error (mistyped target);
    match/run with zero candidates = success with an explicit "0 commits
    contained the pattern" statement.
  - **Pattern absence over the NEW commit set** (match/run): a new
    explicit-commit-set mode in `ScanOpts` (`internal/scan/scan.go:38-44`;
    `rev-list --objects` on the new tips + batch cat-file), owned by the
    scan package. The whole-store scan cannot run pre-cleanup by design.
  - **Cleanliness re-check** (moved here from the old Phase 1.6): the
    working tree/index re-checked under the rewrite lock; foreign state
    that appeared mid-rewrite aborts here, before anything moves.
- **Tier B -- post-cleanup, nonzero, rewrite stands.** Old-object residue
  (`scrub_verify.go:292`, `cleanup.go:82`), stale-pointer checks, and a
  RESIDUAL pre-sync cleanliness check immediately before the
  worktree-touching sync (`rewrite_result.go:189`): if foreign staged
  state appeared between Tier A and the sync, the SYNC IS SKIPPED with
  explicit instructions (refs stand; nothing overwritten) -- Tier-B
  semantics, not a mid-flight hard abort. Green pins
  `TestScrubMatchStashWarning` and `TestScrubMatchUnreachablePruned` keep
  passing under exactly this split.

The annotation pass (`scrub_exec.go:389-450`) is split: tag-object writes
pre-refs (Tier-A-verifiable), ref updates with the other ref moves.
`author rewrite`'s existing post-refs verification stays in the Tier B
slot. The worktree-sync save/restore consumers refactor onto the 1.3
reconciliation authority where they overlap.

**Verify:** NEW -- a Tier A failure leaves every ref and the journal
untouched; a false-positive test (a legitimate multi-operation recipe
PASSES Tier A); the mistyped-target file scrub hard-errors; zero-candidate
match reports explicitly; Tier B residue exits nonzero with the rewrite
standing; the skipped-sync path leaves foreign staged state intact with
instructions printed.

### 4.2 Explicit scrub-file modes and the range selector

`scrub file` gains a required member-spelled selector `--delete` vs
`--replace-with <path>` (its own ChoiceDecl vars; the framework refuses
cross-selector aliasing) replacing the `os.Stat` inference
(`scrub.go:146-154` and the submodule twin `:414-422` -- BOTH). The
replacement SOURCE is read at the operator's cwd via `os.ReadFile` +
`git.HashObjectWriteBytes` (a path-based hash-object would resolve against
the pinned root); the positional argument remains the repo-relative
TARGET; the payload's mode enum keeps `replace`/`remove` spellings
(mapping documented). `scrub file` also adopts the shared `range` selector
(`--from` / `--entire-history`). Additionally (review finding): the
submodule range fallback at `scrub.go:400-404` -- where a failed ancestry
check silently escalates a bounded `--from` to an entire-submodule-history
rewrite -- becomes a HARD error naming the fix. Update the 53 `scrub
file` invocations across nine test files and the doc examples.

**Verify:** registration refuses neither-mode; both modes red-green; an
`--entire-history` file scrub works; the submodule ancestry failure
hard-errors instead of escalating.

### 4.3 Stateless scrub verify and pattern-retention removal `[adopted-from-instinct]`

Assessment findings: the policy store has exactly one reader
(`cmd_scrub_verify.go:71`), zero external consumers (rlsbl provably
independent -- its integration depends only on the rewrite journal and
envelope fields, and its audit archive excludes patterns by whitelist),
zero CI adopters (and a fresh clone has no `.git/safegit`, so the
documented CI usage never worked), no field a caller cannot supply, and
ZERO policy files exist anywhere in the fleet. The REAL verbatim retention
is the oplog: `scrub match` records the pattern in `extra.pattern`
(`scrub_match.go:800`, `:875-887`).

- Delete the policy store: `scrub_policy.go` (relocate the shared
  `appendJSONLLine` helper, also used by `rewrite_maps.go:100`, to a
  shared home first), `RewriteResult.PolicyData`
  (`rewrite_result.go:53-55, 260-265`), and the four write sites
  (`scrub_exec.go:316-331`, `scrub_run.go:287-304`,
  `scrub_match.go:812-835`, plus the result-construction sites).
- `scrub verify` becomes stateless: a REQUIRED input selector between a
  repeatable `--pattern` and a recipe positional (the existing scrub-run
  recipe format, accepted UNCHANGED -- `replace`/`mangle`/`depends_on`
  ignored during verification; no second dialect), plus `--scope`. A
  verify with no input is an error (today's vacuous "No scrub policies
  found" pass is deleted). Payload schema redefined (per-pattern records;
  the required `reason` field drops). Internally verify keeps sharing the
  scan machinery it already uses.
- Scrub completion output gains the rotation line: state that scrubbing
  never un-leaks a pushed secret, tell the operator to rotate the
  credential, and print the on-demand re-check command (this line is new;
  nothing in scrub mentions rotation today).
- The scrub oplog entries STOP recording the pattern (drop
  `extra.pattern`; `extra.reason` and scope suffice for audit)
  `[adopted-from-instinct]` -- otherwise the store deletion removes one
  verbatim copy and leaves the other. The `extra.replace` string
  (`scrub_match.go:809, :886`) is deliberately RETAINED: the replacement
  is the redaction, not the secret -- stated so this is not re-litigated.
- `doctor` diagnose reports a leftover `.git/safegit/scrub-policies.jsonl`
  (written by older published safegit versions in consumer repos) as an
  error naming its content class; `--action fix` deletes it.
- Tests: rewrite `internal/test/scrub_verify_test.go` wholesale for the
  stateless surface; rewrite `scrub_subdir_test.go:298-343` to pass
  `--pattern`. Docs: the eight locations (via templates/selfdoc for
  generated ones) in Phase 9's appendix.

**Verify:** verify-with-no-input errors; `--pattern` and recipe forms both
detect a planted resurrection and exit nonzero; a clean store passes;
scrub match's oplog entry contains no pattern text; doctor
reports-and-fixes a planted legacy policy file; rotation line present in
scrub output.

### 4.4 Submodule scrubs: objects-before-refs across both repos

Restructure the submodule flow (today the submodule is FINALIZED --
`scrub.go:536` -- before the parent walk runs): rewrite submodule objects
WITHOUT finalizing, rewrite parent objects against the new gitlink SHAs
(unreferenced objects suffice), run Tier A over BOTH histories, then
finalize both (submodule first). A verification failure on either side
leaves both repos' refs untouched. The per-repo journals keep their
existing shapes.

**Verify:** NEW -- a parent-side Tier A failure leaves submodule AND
parent refs untouched; the happy path's end state is PINNED explicitly
(parent and submodule refs and tags at their rewritten SHAs, both
journals carrying start/refs/complete in order, cleanup done in both) --
not compared against pre-change behavior, which will no longer exist;
crash-injection tests at each boundary (after submodule objects, after
parent objects, between the two finalizes) assert journal and ref state
so a crash window always reads as either untouched or
journal-explainable.

---

## Phase 5 -- Hooks subsystem

Independent of Phases 1-4 except 0.2/0.3.

### 5.1 The location enumerator

Exported from `internal/hooks`: the single authority for hook LOCATIONS --
a recursive walker with NO executability/naming filters. ALL location
knowledge moves onto it, including the two consumers the first draft
missed: `hooks.PlanInstall` (`hooks.go:287-293` -- the LIVE install path;
`hooks.Install`/`InstallPlaceholder` are dead code handled in 5.4) and
`hook list` (which must show non-executable entries, so it reads the
enumerator, not the filtering `Discover`). Other consumers: `Discover`
(layering execution-eligibility on top), doctor's hook-perms check
(`doctor.go:176`), scan's sweep (5.6). `hooks.DiscoverMulti`
(`hooks.go:104-114`, the submodule parent cascade used by
`push.go:118-126`) changes signature to carry worktree+gitdir PAIRS so the
tracked store participates in the cascade.

**Verify:** unit tests -- enumerator sees non-executable, tilde-suffixed,
and nested entries that Discover filters; DiscoverMulti resolves tracked
stores across the cascade.

### 5.2 The directory move and the tracked store

- Live hooks move to tool-owned `.git/safegit/hooks`. `safegit hook
  migrate` relocates the `pre-pre-push` file and `pre-pre-push.d/`
  UNCONDITIONALLY (the only safegit-owned names in `.git/hooks`; no
  content sniffing); nothing-to-move succeeds and says so. Post-migration,
  discovery finding legacy-location hooks is a HARD error naming `hook
  migrate`.
- Tracked store: `.safegit/hooks/` in the worktree, committed, executed
  directly (deliberate ruling: hooks are not exceptional among committed
  code a repo already runs, and pre-pre-push hooks fire on push, not
  clone-to-inspect). Discovery reads BOTH; name collisions run both,
  tracked first, name-sorted within each. `hook list` shows origin
  (tracked/local) and executability (making `main.go:368`'s help true). A
  non-executable TRACKED hook is a hard error at push time listing the
  chmod-and-commit fix (disabling a tracked hook = committing its
  deletion; mode-based disabling would make accidental mode loss silent).
- `hook install` writes into `.git/safegit/hooks`, calls the
  initialization guard first (it currently skips `ensureInitialized`),
  and REFUSES any existing destination (upgrade = remove then install) --
  restoring the no-clobber check the dead placeholder path carried.
- `doctor --action uninstall`'s promise becomes true (`repo.Uninstall`'s
  RemoveAll now covers live hooks); it additionally removes
  legacy-location safegit-owned names and NEVER touches the tracked
  store.
- Update location preconditions inside `hook_safety_test.go:199-202, 243`.
- `hook migrate` is a new subcommand: it joins the pinned command
  registries (`classification_test.go:70-117` and the group tree) like
  every other command this campaign adds.

**Verify (red going green):**
`TestHookInstallArbitraryBasenameIsDiscoverable`,
`TestHookInstallDoesNotClobberNativeGitHook`,
`TestDoctorUninstallRemovesInstalledHooks`; controls keep passing. NEW:
migrate populated/empty/post-migration-legacy; tracked-store execution and
ordering; non-executable tracked hook push refusal; install collision
refusal; a submodule push still finds the parent cascade's hooks after
migration; uninstall leaves `.safegit/hooks` untouched.

### 5.3 `hook remove`

Remove-by-name over the tool-owned live directory (including `.d`
entries), minted through the effects handle. A name resolving to a TRACKED
hook hard-errors explaining commit-the-deletion. Registers in the pinned
command registries (`classification_test.go:70-117` and the group tree).

**Verify:** NEW -- remove installed; remove nonexistent (error); remove
tracked-name (explanatory error); dry-run records the removal.

### 5.4 (folded into 5.2)

The dead-code deletion -- `hooks.Install` (`hooks.go:296-313`) and
`hooks.InstallPlaceholder` (`hooks.go:316-337`) plus their tests, with the
no-clobber logic absorbed by the install refusal -- is part of 5.2's
single pass over `internal/hooks`, not a separate visit; 5.2's
install-collision test asserts the absorption and the build asserts no
callers remain.

### 5.5 Doctor: never-executed git hooks

`doctor` diagnose reports git-native hook files safegit never executes
(`prepare-commit-msg` always; anything outside
pre-commit/commit-msg/post-commit) -- a stated fact.

**Verify:** NEW test with a `prepare-commit-msg` file present.

### 5.6 Scan coverage `[%%]`

Scan's non-object sweep uses the location enumerator UNION git's native
`.git/hooks` (safegit executes pre-commit/commit-msg/post-commit from
there; preserves the green `TestScanSeesTopLevelPrePrePushHook` and the
attribution test matching inside `.git/hooks/pre-commit`), recursively. It
also sweeps `.git/safegit/` excluding `rewrite-maps.jsonl` (a tool journal
of commit maps; exclusion reason stated in code -- note the policy file
exclusion is moot once 4.3 deletes the store, but the doctor check covers
legacy leftovers). Path coordinates unified: worktree and blob matches
repo-relative; git-dir-internal files gitdir-relative with an explicit
marker field; the worktree file listing runs under the pinned context.

**Verify (red going green):** `TestScanSeesHooksInPrePrePushDir`; controls
keep passing. NEW: scan from a subdirectory finds a root-level worktree
secret; coordinate fields asserted.

---

## Phase 6 -- Sequencer conclusion

Depends on 1.1/1.2/1.3/1.4/1.5, 2.6, 2.9 (commit-msg hook), 3.1
(quarantine), 0.7 (version floors). The conclusion surface is THREE FLAT
COMMANDS in git word order -- `safegit merge-continue`,
`safegit cherry-pick-continue`, `safegit revert-continue` -- sharing one
internal engine and a common flag vocabulary where semantics coincide
(`--resolve`, `--resolve-file`, `-m`, `--trailer`), diverging freely where
they differ (multi-parent and empty-merge rules on merge-continue only;
author preservation and queue delegation on the pick/revert pair). Each
registers its own classification, payload schema, and help. The
command-count number in the app description (`main.go:121`) is DELETED
outright (it cannot self-heal and is a drift class).

### 6.1 Plumbing prerequisites

- Index-copy constructor in `internal/index` (temp index from a byte-copy
  of the shared index).
- A `RunPassthrough` variant accepting env (`git.go:406-417` takes none).
- `MERGE_MSG` comment stripping via `git stripspace --strip-comments`
  (honors commentChar).
- ONE conflict-attribute resolver (conflictStyle + per-path
  `conflict-marker-size`, resolvable from a named tree via
  `--attr-source`), consumed by both the reconstruction (6.3 primary) and
  the structural layer (6.3 secondary).
- `AUTO_MERGE` readers and `git merge-file` reconstruction using that
  resolver.
- Version-floor checks wired.
- Recorded-fact probe (investigation task, result written into this file
  or the code): does `git rebase --continue` honor a substituted
  `GIT_INDEX_FILE`? This decides the feasibility of the future rebase
  extension; nothing in this campaign depends on the answer.

**Verify:** unit tests for the index-copy constructor, stripspace
wrapper, attribute resolver (including a conflicted-`.gitattributes`
case pinned to the first-parent tree), and reconstruction fidelity
(default and diff3 styles, custom marker size).

### 6.2 The engine and the three commands

Native conclusion (merge-continue always; cherry-pick-continue and
revert-continue when the reader reports a SINGLE operation): temp index
copied from the shared index; per-path declared resolutions `--resolve
'path=ours|theirs|worktree|delete'` (repeatable string flag,
registration-declared per-element validator; the framework's dict flag
cannot validate values -- file that upstream bug during this subphase).
Keyword semantics are DEFINED BY STAGE, not by operation folklore: `ours`
= the stage-2 blob (the current branch's side), `theirs` = the stage-3
blob (the operation's incoming side -- which for a REVERT is the
inverse-patch side, the classic confusion; each command's help states its
concrete meaning). `--resolve-file <toml>` carries an array of tables,
each with `path` and `choice` keys (same TOML conventions as the
scrub-run recipe); flag and file forms may be combined, duplicate paths
across them are hard errors. A
conclusion must name every conflicted path (omissions and strays are hard
errors listing them); completeness enforced by `write-tree` refusing
unmerged entries, with a readable `ls-files -u` pre-pass. Parents: HEAD
plus every MERGE_HEAD line (merge-continue); single parent with author
preserved from the source commit (pick/revert). Message: stripped
MERGE_MSG default, `-m` override; `--trailer` accepted; commit-msg hook
runs; trailers injected; CAS ref update under the operation lock
(outermost) plus the per-ref lock; oplog op registered in `undoableOps`
(undo rolls the ref back but cannot restore sequencer state -- undo's
output says so). Empty merges allowed without any flag (the pipeline's
tree-unchanged refusal is bypassed for merge conclusions -- a merge commit
records parents even with an unchanged tree). The commands supply the 1.4
sequencer-context field (the DECLARED bypass of the mid-sequencer
refusal). After committing, the 1.2 cleanup function deletes the
operation's FULL state-file set; the shared index is reconciled through
the 1.3 helper. A stale `AUTO_MERGE` with no matching operation state at
entry is a hard error. Detached HEAD is refused WITH GUIDANCE (`git
switch -c <name>` works mid-merge and preserves the state; then
conclude). Running the wrong command for the actual state (merge-continue
mid-cherry-pick) is a hard error naming the state.

**Verify (red going green):** `TestMergeCanBeConcludedThroughSafegit` (add
`merge-continue`'s argv to the route table,
`commit_merge_state_test.go:37-42`),
`TestMergeConflictTellsOperatorHowToConclude`. NEW: octopus parents;
pick/revert author preservation; completeness errors; `--resolve-file`;
empty-merge conclusion; ZERO-RESIDUE test (after any conclusion, none of
MERGE_HEAD/MERGE_MODE/MERGE_MSG/CHERRY_PICK_HEAD/REVERT_HEAD/AUTO_MERGE/
sequencer-dir remain AND a subsequent `safegit commit` succeeds); stale
AUTO_MERGE refusal; wrong-command-for-state refusal; detached-HEAD
guidance; undo of a conclusion; two sessions racing a conclusion.

### 6.3 Marker verification

Layered, over every staged path, no escape flag, with PER-CONFLICT-KIND
scope (review finding: delete/modify and add/add conflicts have stages but
no meaningful marker regions):

- **Content conflicts (both sides present):** primary = emitted-region
  survival -- parse regions from `AUTO_MERGE:<path>` (or reconstruct
  byte-identically via merge-file under the 6.1 resolver); a surviving
  verbatim emitted region is a hard error. When AUTO_MERGE is absent AND
  reconstruction is impossible for a content conflict, hard-refuse rather
  than verify less.
- **All staged paths (any kind):** secondary = structural complete-region
  detection plus the counting differential against stage 1/2/3 blobs
  (conflicted paths) or the first parent's blob (others); marker-shaped
  content already present in a parent passes by construction.
- **Delete/modify, add/add, binary, custom-merge-driver paths:** the
  region layer does not apply (nothing meaningful to reconstruct); the
  structural layer and write-tree completeness still do.
- **Declared exemption:** a path exempted via the
  `safegit-conflict-markers` attribute resolved from the FIRST PARENT'S
  tree (`--attr-source`; an exemption must predate the conflict). Every
  rejection prints the one-line declaration that would exempt the path.

**Verify:** NEW -- forgotten markers hard-error with path+line; parental
marker content passes; the byte-identical-block case is refused;
committed exemption works, uncommitted does not; marker-size and diff3
fidelity; delete/modify and add/add conclusions pass without region
verification but fail structural checks when garbage markers are added;
plus a PROPERTY test generating randomized conflicts across the config
matrix (conflictStyle x marker size) and asserting the merge-file
reconstruction is byte-identical to the actual `AUTO_MERGE` blob -- the
hard-refusal path is not wired until this property test passes.

### 6.4 Queued-sequence delegation `[%%]`

`cherry-pick-continue` AND `revert-continue` with the sequencer dir
present refuse native authorship and delegate: same staging into the temp
copy, same completeness and marker checks, then git's own
`cherry-pick --continue` / `revert --continue` with `GIT_INDEX_FILE` at
the copy. For cherry-pick this was probed (git advances its queue and
leaves the shared index stale -- reconciled afterward via the 1.3 helper);
for revert the same behavior is EXPECTED (shared sequencer machinery) but
must be probe-verified as this subphase's first task before relying on it.
The delegation is stated in output.

**Verify:** NEW -- multi-pick AND multi-revert with a mid-queue conflict
conclude, the queue completes, the shared index ends clean, output names
the delegation; the revert `GIT_INDEX_FILE` probe result is recorded.

### 6.5 Passthrough texts and the revert restructure

`safegit merge --continue` stays guard-refused; the refusal (and the 1.4
texts) name the operation-specific command. `safegit revert` for a SINGLE
commit is restructured: compute via `git revert --no-commit`
(passthrough), then conclude through the revert-continue machinery --
which includes the 1.2 state-file cleanup, so the probed leftover set
(`REVERT_HEAD`, `AUTO_MERGE`, `MERGE_MSG` survive a plumbing conclusion)
is removed and later commits are not bricked. Multi-commit revert remains
a sequencer passthrough concluded via revert-continue like picks.

**Verify:** NEW -- `merge --continue` refusal names merge-continue;
single-commit `safegit revert` on a clean repo yields a pipeline-authored
commit with trailers, ZERO residual state files, and a subsequent
`safegit commit` succeeds.

### 6.6 Honest previews for merge, cherry-pick, revert

`--dry-run` on the three passthroughs computes the real outcome via
`git merge-tree --write-tree` (with `--merge-base` for pick/revert) under
the quarantine: clean-vs-conflict plus the conflicted path list in the
preview. The refusal set is defined by CRITERION, not example list: any
forwarded flag that alters TREE COMPUTATION and is not representable in
merge-tree's argv (`-s` strategies other than ort, `-X` strategy options,
`--squash`'s staging behavior) is refused at runtime with the reason;
flags affecting only commit creation or reporting are accepted. The
refusal is hand-rolled per-invocation (the framework only has per-command
refusal -- noted for migration when the framework ruling ships).

**Verify:** NEW -- clean preview, conflict preview with paths,
unsupported-option refusal, object store untouched.

---

## Phase 7 -- Move records

Depends on Phase 2 and 6.5; 7.5 depends on Phase 4.

### 7.1 The record format -- one encoder

In `internal/trailer` (plus a small ID helper): THE single pair-grammar
encoder/decoder, called by the record writer, the `--moved` validator, and
`safegit mv`'s argument parser -- one implementation, three consumers.

- `Moved:` trailer, arrow form `old -> new` `[%%]`, git-style C-quoting
  with an exhaustive trigger (whitespace, double quote, backslash, control
  bytes, non-ASCII bytes, or the literal arrow token) -- byte-complete for
  any legal filename. Encoder + decoder + property-based round-trip over
  random byte strings. (No C-quoting helper exists; both directions are
  new.)
- Subtree form: trailing slash = everything under the prefix; per-file
  answers derived at read time, validated against trees.
- Per-record ULIDs: hand-rolled Crockford base32 over `crypto/rand`; the
  ID is a LEADING token in the trailer value (a separate ID key was
  rejected for the same adjacency-fragility as split from/to keys).
- Retract-only corrections: `Moved-Retract: <id>` in a later commit;
  replacement = retraction + new record in one commit; readers fold.
- Projection: records are CLAIMS, TREES ARE THE ARBITER (a record whose
  old path is absent from the parent tree, or whose subtree expansion
  names never-existed files, is ignored; at merges the surviving path in
  the merge tree decides). Longest-prefix wins; file-form records never
  apply to descendants. No stored confidence (exact-vs-declared is
  recomputable). A key-value trailer parser is part of this work
  (`SplitBodyTrailers` returns an unparsed block).

**Verify:** round-trip property tests over random byte strings including
newline/control/non-UTF-8 names; ULID uniqueness and lexical ordering;
parser + folding unit tests; projection unit tests (multi-hop chain,
retraction, merge arbitration, garbage record ignored, subtree precedence,
file-vs-descendant rule).

### 7.2 Declared moves on commit and amend

`--moved 'old -> new'` (repeatable; the value grammar IS the 7.1 encoder
`[%%]`), accepting the subtree form. Validation against the Phase 2
expansion: old path/prefix tracked in the parent and absent from disk, new
path/prefix present or staged. Blob equality NEVER decides record
existence. Amend accepts `--moved`; amend/reword with `-m` PRESERVE
existing `Moved:`/`Moved-Retract:` trailers by re-appending (dropping a
record requires explicit retraction).

**Verify:** NEW -- file and subtree declarations with validation errors
(untracked old, present old, absent new, nested prefixes); amend
preservation under `-m`; records appear with ULIDs and correct quoting.

### 7.3 `safegit mv`

New top-level command: variadic arguments, each ONE quoted pair token
parsed by the 7.1 encoder. ALL pairs validated before the FIRST filesystem
mutation (sources tracked+present, destinations absent, no source doubling
as destination, no nested pairs); filesystem moves minted through the
effects handle (`Rename` -- honest dry run); mid-sequence failure rolls
back completed renames. Directories accepted (one subtree record).
Case-only renames on case-insensitive filesystems (via `core.ignorecase`)
take an explicit same-file path whose mechanism is: index-only rename
(remove the old entry, add the new, same blob) plus a two-step filesystem
rename through a temporary name in the same directory (a direct rename is
a no-op or error on such filesystems). The command commits (one commit per
invocation), registers its own oplog op (undo rolls back the commit, NOT
the filesystem moves -- consistent with undo's worktree-untouched
contract; undo's output says so), declares a payload schema, joins the
pinned registries.

**Verify:** NEW -- multi-pair atomicity and rollback; directory subtree
record; case-only rename on a case-insensitive fixture (skipped where
unavailable); dry-run records renames and commit; undo behavior; pinned
registries updated.

### 7.4 Inverse records on revert

Single-commit `safegit revert` of a commit carrying `Moved:` records emits
the inverse records (new -> old) in the revert commit authored via 6.5.
Multi-commit reverts (the 6.4 delegation path, where git authors the
commits) emit NO records -- stated in the revert docs, consistent with
records existing only on pipeline-authored commits.

**Verify:** NEW -- revert of a move commit carries the inverse records;
projection across the revert answers correctly.

### 7.5 Record-aware scrub

`scrub file` retracts/redacts records referencing the scrubbed path in the
same rewrite (message edits feeding 4.1's intended-change map);
`scrub match`'s message transform becomes trailer-aware using the
body/trailer split (`scan_cmd.go:135-181` demonstrates the shape) so it
cannot corrupt quoted values.

**Verify:** NEW -- scrubbing a path referenced by a record retracts it in
the same pass and Tier A accounts for the message change; a match pattern
overlapping a quoted trailer value rewrites without corrupting the
grammar.

---

## Phase 8 -- Push and consent

Needs 0.5 only.

### 8.1 The pinned lease `[%%]`

FIRST the red test: force-pushing tags with the bare lease (tags have no
remote-tracking refs; git zeroes the expectation and rejects existing
remote tags), which makes safegit's own post-scrub instruction
unsatisfiable. Then: per-ref leases
`--force-with-lease=<remoteRef>:<observed SHA>` from the SHAs push already
resolves (`push.go:273, 304, 338`), translating the internal null-SHA
"absent" marker to the EMPTY expectation ("must not exist"). `--atomic`
always on for multi-ref pushes. The retry loop RE-OBSERVES and re-pins per
attempt; a lease rejection is terminal with an explanatory message (its
own registry code), never retried as transport.

**Verify:** tag-lease red-green; branch lease against a moved remote ref
refuses; new-ref lease works; multi-ref failure pushes nothing; lease
rejection is not retried.

### 8.2 Conditional consent

`push --force-with-lease` becomes conditionally consequential in the
established hand-rolled shape (the `consent` + `confirmDeliberate` pattern,
`doctor.go:41-49` / `main.go:764-796`): prompt at a terminal,
`--approve-consequential` answers, `--json` refuses, declining exits 1.
Migration awaits the strictcli ruling (todo filed).

**Verify:** NEW tests mirroring `confirm_deliberate_test.go` for the force
path; unforced pushes prompt nothing.

### 8.3 Honest dry-run hook notice

The pre-pre-push hook skip under `--dry-run` becomes user-visibly
documented: stated in push's help text, present in preview output, carried
in the payload.

**Verify:** NEW -- dry-run push output and payload state the skip; help
text mentions it (asserted via `--help`).

---

## Phase 9 -- Documentation healing

After behavior stabilizes: one pass over the EDITABLE surfaces only
(hand-written docs, the `docs/_CLAUDE.md`/`docs/_README.md` templates, and
`main.go` help strings -- never chmod-444 generated files), finishing with
`--dump-schema` + `selfdoc gen`. The claims inventory is Appendix A --
the phase's checklist is IN this file, so its verification is
self-contained.

**Verify:** every Appendix A row resolved (healed, or now true by
behavior); the 0.5 registry-generated exit table in place; `selfdoc gen`
clean; a fresh spot-check reads each Appendix A location and confirms.

## Phase 10 -- Verification and audit

- **10.1 Full green:** `go test ./... -race` and the stress run
  (`go test ./internal/test/ -race -count=5 -timeout=15m`) green; ALSO a
  `GOWORK=off` run (the repo's gitignored `go.work` overlays a local
  strictcli checkout; CI resolves the released module -- both must pass);
  the 0.1 baseline artifact re-generated and every difference accounted
  for (each red test green or deliberately rewritten per this plan).
- **10.2 Fresh audit:** a fresh-context auditor (AUDIT protocol: this file
  as the spec, no git history, files on disk) audits every phase item;
  failures fixed before release.
- **10.3 Changelog:** every commit since the last tag covered in
  `.rlsbl/changes/unreleased.jsonl`; `rlsbl check --tag changelog` green.

## Phase 11 -- Release

Todo triage (resolved originals to `todo/.done/`; strictcli-await todos
stay; `todo/.defer/windows-lockfileex-support.md` stays deferred), then
`rlsbl release init`, the release file (minor bump, campaign description,
context block), commit it, and the single
`rlsbl release run --no-allow-dirty --watch --approve-consequential`.

---

## Dependency spine

| Phase | Depends on | Blocks |
|---|---|---|
| 0.1-0.8 | -- (mutually independent) | everything |
| 1.1 | 0.2, 0.3 | 6 |
| 1.2 | -- | 1.4, 6 |
| 1.3 | 0.2, 0.3 | 2 (untrack test rewrite), 4.1, 6.2, 6.4 |
| 1.4 | 1.2 | 2.9 (auto-bump refusal), 6.2 |
| 1.5 | 0.8 | 6.2 |
| 2 (all) | 0.*, 1.3, 1.4 | 3, 4 (diff-tree), 6 (2.6, 2.9), 7 |
| 3.1 | 0.2, 0.3, 2.8 | 3.2, 3.3, 6.6 |
| 4.1 | 0.3, 0.5, 1.3, 2.8 | 4.3, 4.4, 7.5 |
| 4.2-4.4 | 4.1 | 7.5 |
| 5 | 0.2, 0.3 | 9 (hook docs) |
| 6 | 1.1-1.5, 2.6, 2.9, 3.1, 0.7 | 7.4 (6.5) |
| 7 | 2, 6.5; 7.5 also 4 | 9 |
| 8 | 0.5 | 9 |
| 9 | all behavior phases | 10 |
| 10, 11 | 9 | -- |

Phases 4, 5, 8 can run in parallel with numeric neighbors; the numbered
order is safe sequentially.

---

## Appendix A -- documentation claims to heal (Phase 9 checklist)

Editable-source locations; "true-after" rows need no text change beyond
verification, "fix" rows need edits. Generated files heal via
`--dump-schema` + `selfdoc gen` after their origins are fixed. Line
numbers here will have drifted heavily by Phase 9 -- treat each row's
CLAIM text as the anchor and re-locate by searching for it; the wide
ranges (e.g. row 16) especially.

| # | Location | Claim | Resolution |
|---|---|---|---|
| 1 | docs/commands-guide.md:18; CHANGELOG.md:68 | dry-run writes nothing to disk | true after 3.1; verify wording |
| 2 | docs/commands-guide.md:328; docs/_CLAUDE.md dry-run block | dry runs never touch the network | STRIKE the tool-wide promise (safegit-authored, false, stance belongs to the framework ruling); describe backup backup's local-only preview as command behavior |
| 3 | docs/commands-guide.md:1124-1141 | exit-code table (omits 8/14/22/23/70; usage-code half-truth) | replaced by the 0.5 registry-generated table |
| 4 | docs/architecture.md:234-242 | exits 12/13 + staleness re-check + apply --index claim + invented hint text | remove (see non-goals) |
| 5 | docs/architecture.md:167, 378 | exit 8 after lock timeout | true after 0.5 |
| 6 | docs/architecture.md:93-179; docs/concurrency-guide.md:17-57 | pipeline narrative (incl. an ordering the code deliberately rejects) | rewrite to the Phase 2/3 pipeline |
| 7 | docs/architecture.md:46-47, 265, 271-276 | hooks live in .git/hooks only; no tracked .safegit | rewrite to the Phase 5 layout (deliberate overturn) |
| 8 | docs/architecture.md:275 | flag --no-pre-pre-push | fix to --pre-push-hook/--no-pre-push-hook |
| 9 | docs/architecture.md:276 | commit-family git hooks "run normally" | rewrite: pre-commit/commit-msg/post-commit run explicitly (2.9); prepare-commit-msg never |
| 10 | docs/architecture.md:25; docs/_README.md:78 | rm -rf .git/safegit returns to vanilla git | true after Phase 5 |
| 11 | docs/architecture.md:305-312 | push opens the network only after hooks | fix: ref resolution runs ls-remote before hooks (structural after 8.1) |
| 12 | docs/architecture.md:365, 385, 390-397 | push oplog per attempt; hook_timeout op; bypass warning on mutating commands | fix to actual behavior |
| 13 | docs/architecture.md:144, 162 | oplog append after lock release | fix: append while held |
| 14 | docs/architecture.md:135, 167, 378 | config key lock.acquireTimeout | fix: lock.acquireTimeoutSeconds |
| 15 | docs/architecture.md:243, 247-255, 347 | update-index --cacheinfo staging; unstage mechanism; no orphan blobs | fix/remove |
| 16 | docs/commands-guide.md:253, 331, 828-1133 (guard rows); main.go:193-197, 523-524 helps | exit-5 guard detects "another operation in progress" | true after 1.5 (two layers: dirty-tree check + operation lock); reword to describe both |
| 17 | docs/concurrency-guide.md:113 | every git command carries --no-optional-locks | true after 0.2 |
| 18 | docs/concurrency-guide.md:49 | root-commit CAS belt-and-suspenders | true after 0.4 |
| 19 | docs/concurrency-guide.md:119 | oplog 4096-byte atomic-append claim | rewrite: flock-based, no cap, no rotation (0.6) |
| 20 | docs/concurrency-guide.md:55 | CAS retries configurable up to 200 | fix: positive integer, no cap |
| 21 | docs/concurrency-guide.md:213-217 | tmp-index GC "manual" trigger | clarify |
| 22 | docs/integration-guide.md:57, 123 | rlsbl push; rlsbl pre-push-check | fix to current rlsbl commands |
| 23 | docs/integration-guide.md:88 | install copies into .git/safegit/hooks, discovered by scanning | true after Phase 5; reword to the real discovery |
| 24 | docs/integration-guide.md:199, docs/commands-guide.md:1122, docs/_README.md:76 | log.maxSizeMB rows | remove (0.6) |
| 25 | docs/integration-guide.md:205; docs/commands-guide.md:1118 | autoBumpParent presented as inert opt-in | rewrite: mandatory presence, validated pre-commit (2.9) |
| 26 | docs/_CLAUDE.md:39, 43, 46 | CHANGELOG hand-edited; rlsbl release [patch]; rlsbl release --dry-run | fix to JSONL/release-run flow |
| 27 | docs/_CLAUDE.md:52 | all git plumbing through internal/git | true after 0.2 |
| 28 | docs/_CLAUDE.md:23-29 | package table (14 of 16+; missing filelock/procutil + campaign's new packages) | regenerate/extend |
| 29 | docs/_README.md:26 | requires Go 1.24+ | fix to go.mod's version |
| 30 | docs/_README.md:55-61, 67-68 | phase narrative (commit created under lock); bare `safegit config` forms | fix |
| 31 | docs/_README.md:129-131 | Windows unsupported (Unix-only syscalls) | update: unsupported AND not built (0.7) |
| 32 | main.go:368, 386 | hook list/install help (.git/safegit/hooks; executable column) | true after Phase 5; verify wording |
| 33 | main.go:257 | pull "defaulting to fast-forward-only" | fix: --merge-strategy is required, no default |
| 34 | main.go:545; docs/commands-guide.md:663-665 | unlock releases "crashed git process" locks | fix: safegit's own lock namespace (incl. 1.5's new grammar) |
| 35 | main.go:455, 474 | scrub file "across all commits"; scrub match "every blob" | fix: range-selected (4.2); blobs+messages+tags |
| 36 | main.go:121 | "31 commands" | delete the number |
| 37 | main.go:241; docs/commands-guide.md:159, 205-215 | force-with-lease help + semantics + push exit table | rewrite to pinned-lease semantics + consent (8.1/8.2) |
| 38 | docs/commands-guide.md:334-341 | backup exit table decline row contradiction | fix to decline=1 |
| 39 | docs/commands-guide.md:387-425, 500, 580-609; docs/_CLAUDE.md:73 | scrub file os.Stat mode inference examples; scrub verify policy-store text (incl. the template's verify description) | rewrite to mode flags (4.2) and stateless verify (4.3) |
| 40 | docs/commands-guide.md:27; docs/_CLAUDE.md consequential list | "exactly four" consequential commands | update if 8.2's conditional form changes the count phrasing |
| 41 | docs/req.md:17 | lock must notify without polling | annotate as historical requirement; implementation polls with backoff |
| 42 | docs/_CLAUDE.md:34-37 | stress command docs | update for the 0.7 env re-keying |
