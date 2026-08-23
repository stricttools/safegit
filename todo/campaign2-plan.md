# Campaign 2: implementation plan

This plan is self-contained: every decision it executes is stated in full,
and a session with zero conversation context can implement any subphase
from this file plus the cited code. File:line anchors were verified
against the working tree at HEAD `c304985` (2026-08-23) by five grounding
investigations; expect small drift — the claim text is the anchor.

**Status quo this plan starts from.** The campaign-2 specification suite
is committed and deliberately red: the first red wave (22 tests across
`internal/test/sequencer_conclusion_*`, `sequencer_stale_autostash`,
`sequencer_delegation_delete`, `commit_unmerged_index`,
`undo_autobump_precheck`, `machine_contract_*` files and
`internal/trailer/grammar_reserved_keywords_test.go`) plus the second
wave (8 tests across `internal/test/wave2_*.go`). Every subphase below
names the red tests it turns green. Nothing is pushed before the single
release at the end. The execution log
(`todo/redesign-campaign-execution-log.md`) records every ruling this
plan executes; where this plan states a ruling, the log is its origin.
Implementor discipline: commits via the INSTALLED safegit (single -m,
plain paths, repo root); scratch git repos only via t.TempDir (the
boundary guard scans gitignored files); batch edits follow the standing
rule (dry-run capable, dry run first, output examined, then execute;
occurrence-count check plus full diff review for any bulk change);
red-first for every behavior change; one fresh audit per phase, never
per subphase.

**Decision-origin note.** Rulings marked `[user]` were decided by the
user; `[plan]` marks orchestrator resolutions of grounding-discovered
contradictions, made in the spirit of an adjacent user ruling and freely
reversible; `[probe]` marks a decision contingent on a recorded-fact
probe named in its subphase.

**Non-goals (deferred, tracked elsewhere):**
- Reset/rebase recovery commands (todo stays active; Phase 1's oplog
  baseline ships the data half of its precondition). `[user]`
- Per-command bypass-detection surfacing (deferred until after the
  baseline exists; no commitment made). `[user]`
- Live streaming under machine mode for the guarded commands (blocked on
  the framework's tee; `todo/push-streaming-restoration.md` forbids a
  safegit-side workaround).
- The reader command for move records is NOT PLANNED anywhere `[user]`
  (2026-08-23): the framework-repo filing was deleted on the user's
  order (archived via saferm, recoverable); no safegit todo replaces
  it. The read library's remaining consumer story is scrub's future
  backward projection only.
- Everything in the three contingent todos and the three
  strictcli-await todos.
- The global-rules-file adoption of the batch-operation rule (a one-off
  edit outside this repo, at the user's go).

---

## Phase 0 — Groundwork

Subphases are independent; run sequentially or under one implementor.

### 0.1 Campaign-2 baseline artifact

Capture the current red/green population as a tracked artifact:
`scripts/test-baseline testdata/campaign2-baseline.txt` and commit it
(the script always passes -short and -count=1 and refuses baselines
containing build failures). This is the reconciliation anchor for Phase
9, exactly as `testdata/campaign-baseline.txt` (frozen, campaign 1) was.

**Verify:** the artifact is committed; a `--check` run against it is
clean; the header records HEAD and argv.

### 0.2 Grammar reservation

Turn green the two reds in
`internal/trailer/grammar_reserved_keywords_test.go`:
- `needsQuoting` (internal/trailer/cquote.go:53) gains an exact-match
  test against the reserved origin keywords `observed`, `declared`,
  `derived`, so no encoder ever writes them bare as a path token.
- `ParseRecord` (internal/trailer/moved.go:208) treats a bare reserved
  keyword immediately after the id as reserved: the line is refused
  (and therefore lands in `Moves.Malformed` via ReadMoves' existing
  verbatim capture) — never guessed as a path, never guessed as a
  marker.
Known and sanctioned: the subtest pinning `<id> observed x -> y` as
Malformed is transitional — Phase 6 makes exactly that spelling valid
for the one chosen machine token and rewrites the subtest then (stated
in the test's comment during this subphase).

**Verify (red to green):** `TestReservedOriginKeywordsAreQuotedAsPaths`,
`TestBareReservedKeywordAfterTheIdIsMalformed`. The round-trip property
tests stay green.

### 0.3 Implicit-fallback family deletion

The 1800-second hook-timeout fallback is spelled at
internal/repo/repo.go:64 (the authority, `DefaultConfig`) and duplicated
as dead `<=0` call-site fallbacks at push.go:293-296 and hook.go:132-135
— dead because every `*repo.Config` a handler holds passes
`Validate()` (repo.go:408-425), which hard-errors on a non-positive
value, and `SetConfigValue` refuses writing one. `[plan]` Delete the two
call-site fallbacks (read the config value directly) and, in the same
pass, the six sibling instances of the identical dead pattern:
push.go:328-330 (retryAttempts), internal/commit/commit.go:328 and :574
(casMaxAttempts, lockTimeout), internal/commit/amend.go:144, :298-300,
:475, :560. `Validate` is the sole authority; no call-site substitution
survives. Add one unit pin per config field asserting the loader
refuses a non-positive value (the (E) rulings had no test).

**Verify:** new pins green; grep shows no `<= 0` fallback of the
pattern remains outside `Validate`; suite population otherwise
unchanged.

### 0.4 Recorded-fact probe: autostash message shape

Add a recorded probe beside internal/git/autostash_probe_test.go
pinning what git writes as the autostash stash-commit message: the
expectation is `On <branch>: autostash` for a genuine
`merge --autostash` stash, versus `WIP on <branch>: ...` for a bare
`git stash create`. Phase 3.2's keying is contingent on this fact
`[probe]`; if the probe disproves it, Phase 3.2's fallback applies (its
text states it).

**Verify:** the probe test is committed and green, and records both
shapes.

---

## Phase 1 — Foundations and deletions

Depends on Phase 0 only. Subphases 1.1-1.5 touch disjoint files except
1.2 and 1.3 (both edit coord_cmd.go): run those under one implementor
or sequentially.

### 1.1 Derived worktree guards

- Add a derived view to the gitexec classification
  (internal/gitexec/classify.go): worktree-guard-required, computed from
  `EffectsOf` over the `MutatesWorktree` bit, default-deny on
  unclassified argv (the `WritesObjects` precedent at classify.go:429).
- reset: delete the literal `--hard` scan (coord_cmd.go:350-356); the
  derivation reproduces it bit-for-bit today AND extends the guard to
  `--merge`, `--keep` and plain `reset <commit>` once the reset row's
  conditionals are stated per git's real semantics — grounding
  confirmed the current row already declares `--hard`; extend the row
  so the derivation covers every worktree-writing form, with the
  in-code justification corrected (the old comment's claim that only
  --hard mutates the tree is false).
- bisect: delete the hardcoded six-subcommand list
  (coord_cmd.go:398-404); narrow the bisect row's Base and add
  Conditional entries so stepping subcommands (`good/bad/old/new/skip/
  run/replay/next/reset/start`) derive the guard. `TestTableIsWellFormed`
  constraints apply (tokens, effects, why on every conditional).
  Narrowing is safe against the other views (grounding verified).
- The marker-verification per-path exemption skip
  (sequencer_markers.go:136) becomes a carried declined-check: the
  conclusion result gains a declined list (path, reason) that the
  report renders — a declined check announces itself like every other.
- Red-first: reset `--merge`/`--keep` on a dirty tree refuse with the
  coordination code (currently exit 0, unguarded — write the reds in
  this subphase, no wave pin exists); a bisect stepping subcommand on a
  dirty tree refuses; a conclusion over an exempt path reports the
  declined check.

**Verify (red to green):** the new reds; existing guard tests stay
green; the derived view's unit tests enumerate the guarded set.

### 1.2 The oplog baseline and outcome recording

Ruling `[user]`: every operation records the branch and its
before/after positions, and the audit trail never records operations
that did not happen — in both directions.

- Every passthrough seam records `ref` (full ref name via git.HeadRef),
  the pre-operation tip, and the post-operation tip, in the same
  spelling commit entries use (internal/commit/commit.go:649-658).
  Pre-operation tips must be resolved where missing (only checkout has
  one today, coord_cmd.go:158). Fix the checkout entry's `ref` key
  collision (it currently stores the operator's argument, not a ref
  name).
- Outcome: the six coord_cmd seams that currently return before their
  append on failure (grounding enumerates them) now append an entry
  carrying ref + old tip + EMPTY new tip + a failed outcome marker;
  the runGuardedPassthrough seam's unconditional append
  (coord_cmd.go:469-474) gains the same outcome field and moves behind
  the exit code so success and failure are distinguishable. Constraint
  from the fail-closed readers (grounding §5): a failed entry must not
  record a new tip — an empty tip keeps it invisible to `LastRefUpdate`
  and doctor's bypass check, which is the safe shape.
- The multi-spelling TipSHA search (oplog.go:184-191 accepting
  sha/to/result) may be consolidated; the wave test sanctions its
  rewrite.
- The delegated-conclusion entry (sequencer_delegate.go:143-155)
  already records from/to; align its keys with the baseline spelling.

**Verify (red to green):** `TestMergeOplogEntryCarriesBranchPositions`,
`TestFailedPassthroughOplogEntryIsDistinguishable`
(internal/test/wave2_passthrough_oplog_positions_test.go). NEW: a
failed merge writes an entry with the failed outcome and empty new tip;
doctor's bypass check does not misfire after a clean safegit merge
(write this pin — it fails today because merge entries carry no ref);
the root-undo false doctor error is gone (undo's entry handling per
Phase 4.1 keys this — coordinate; the doctor-side pin lives here).

### 1.3 One JSON document on the guarded commands

Ruling `[user]` with grounding resolution `[plan]`: under `--json`,
runGitMutation captures instead of streams and re-emits the child's
stdout through `passthroughStdout` (stderr to stderr), so the envelope
is the sole stdout document; in HUMAN mode `Stream(true)` stays and
output remains live. `Check(false)` + `Completed.ExitCode()` is
identical in capture mode (grounding verified), so exit propagation is
unchanged. Known limit, documented in Phase 7: in machine mode a child
emitting non-UTF-8 output fails the capture decode and degrades the
exit to General — confined to machine mode, stated, not silent.

**Verify (red to green):** the five leaking rows of
`TestGuardedCommandsEmitExactlyOneJSONDocument` and
`TestGuardedCommandChildOutputGoesToStderrInMachineMode`
(internal/test/machine_contract_json_document_test.go); the checkout
row stays green (its forward-guard role stated in the test).

### 1.4 Hook rulings

- Timeout-override deletion `[user]`: remove the first-line
  interception goroutine, the 2-second select, `parseTimeoutOverride`,
  and its table test (internal/hooks/hooks.go:203-238, :297-311;
  hooks_test.go:219-238); `bufio` and `strconv` imports go with them;
  the three ioDone syncs survive. `SAFEGIT_HOOK_TIMEOUT_S` and the
  config key are a separate mechanism and stay.
- Refuse-both non-executable hooks `[user]`: Discover's local-skip
  branch (hooks.go:113) becomes a typed error like the tracked branch;
  exit 25's registered meaning WIDENS to "a discovered hook is not
  executable" (one situation, the store named in the message) `[plan]`;
  hookDiscoveryExit maps it; `hook run` refuses identically; `hook
  list` keeps listing (it is the diagnostic that shows why a push
  refuses). doctor's hook_perms local branch escalates to error
  severity with refusal-flavored text; the warn-severity doctor test
  fixture that used a non-executable local hook gets a different warn
  fixture. Sanctioned rewrites (named in the wave test's header):
  `TestSkipNonExecutable`, `TestSetOutputCapturesDiscoverWarning`, the
  `hook run` tail of `TestHookListRendersStateAndOrigin`,
  `TestDoctorExitCodeFollowsErrorFindings`'s fixture.

**Verify (red to green):**
`TestHookTimeoutOverrideNoLongerShortensTheBudget`,
`TestHookTimeoutOverrideLineIsForwardedNotSwallowed`,
`TestPushRefusesNonExecutableLocalHook` (wave2 files). Registry: 25's
doc comment and the generated table regenerate; the two hand-written
guide rows that spell ".safegit/hooks" update in Phase 7.

### 1.5 Consent uniformity

Ruling `[plan]`, generalizing push's documented stance: a dry run never
prompts. One early dry-run return in `confirmDeliberate`
(main.go:990-1009); push's own `&& !flags.dryRun` becomes redundant and
is removed; doctor's dry-run uninstall stops demanding consent and
`--json --dry-run doctor --action uninstall` stops exiting nonzero.
Backup's dry path is unaffected (it returns before its confirmation by
design) — and the rule is NOT license to run backup's exposure
classification in a preview (the network probe stays off the dry path).
Write the pin the ruling never had: dry-run uninstall without
`--approve-consequential` succeeds and enumerates; the existing
enumerate-and-remove-nothing test keeps passing.

**Verify:** new pins green; `TestScrubFileDryRunPreviewsWithoutConsent`
and the consent suite stay green.

---

## Phase 2 — The declared mode model

Depends on 1.1 (derived guards are the model's guard column) and 1.3
(machine output coherent before payload members land). This is the
structural centerpiece `[user]`.

### 2.1 The table and its registration test

One table, one row per (command, mode), beside the registrations in
main.go, covering the 37 pinned commands. Each row: authorship class
(safegit-pipeline / git / none), oplog op with explicit undoability,
preview strategy, and the mode's SELECTION KIND:
- DECLARED: the command carries a required member-spelled or
  token-spelled selector covering exactly its modes (the seven
  existing selector-bearing commands' rows document what already
  exists; commit's commit/amend/reword modes are declared via the
  `--amend` flag shape and documented as such).
- DISCOVERED: the mode is chosen at run time (revert's arity/allowlist
  split; the conclusion commands' queued delegation; scrub file's
  submodule redirect). A discovered row REQUIRES a generated
  unconditional stderr announcement and a payload mode member.
Grounding constraint honored `[plan]`: the seven passthrough
registrations stay passthroughs (the framework refuses flags on them,
and conversion would change argv semantics); their dual modes are
therefore DISCOVERED rows — payload schemas are legal on passthrough
registrations and reach their handlers (grounding verified), so revert
and cherry-pick gain schemas here.
The enforcement test extends classification_test.go's shape: a
registered command absent from the mode table fails; a command with two
rows and no selector/discovered declaration fails; an oplog op absent
from undoableOps must set undoability false explicitly; every row's
preview strategy is named.

### 2.2 Generated surfaces

- The announcement: one helper (the reportDelegated mechanism —
  unconditional stderr surviving --quiet and --json, payload member
  set) whose TEXT is generated from the row's authorship column; the
  hand-written delegation notice becomes the generated one (single
  authority — a hand notice beside the generated one is a second
  authority for the same fact).
- Payload `execution_mode` members generated into the schemas of every
  multi-mode command (the conclusion schemas' builder gains the member;
  revert/cherry-pick passthrough schemas are new).
- A generated doc include for the mode table in the commands guide,
  cloning the exit-table generator shape exactly
  (internal/exitcode/docgen.go + scripts/gen-exit-table +
  exit_table_test.go's freshness pin).

### 2.3 Discovered-mode retrofits and fallback deletions

Applying the model to the tallied instances `[user]`:
- revert becomes SINGLE-FORM ONLY `[user]` (2026-08-23, superseding the
  announce-the-split shape): the git-authored passthrough arm is
  DELETED. `safegit revert <commit>` supports exactly the restructured
  single-commit pipeline form; multiple commits are sequential
  invocations (each authored, each undoable); options the pipeline
  cannot honor (-S, --edit, --no-commit, pathspecs) and multi-commit
  argv are refusals naming the fact and, for multi-commit, the
  sequential form. The registration stays a passthrough (the handler
  refuses non-single forms) and gains the payload schema of the
  single form; its mode-table entry is ONE row, so no announcement
  exists because no split exists. revert-continue's queued path stays
  (raw-git reverts in mixed repos still create queued state needing
  conclusion). The dry-run preview previews only the single form
  (fixing the wrong-argv record at sequencer_preview.go:66 by
  deletion); the divergences entry "A single safegit revert is split
  at git's own seam" is REWRITTEN to the single-form ruling in the
  same commit.
- scrub file's submodule redirect: the infof announcement becomes the
  generated unconditional one; the payload gains the submodule member
  (schema change); the swallowed submodule-enumeration error
  (scrub.go:154-158) becomes a HARD error `[plan]` — a swallowed error
  that silently changes which repository is rewritten is the exact
  silent-switch the model forbids; the three sibling sites keep their
  warn behavior (their failure does not change the target).
- `--remap-shas-in` on a submodule-target scrub: refused at intake
  naming the limitation `[plan]` (accepted-then-inert violates
  if-configured-it-must-work).
- commit `--hunks`: the silent `--3way` retry
  (internal/stage/stage.go:180-198) is deleted; a patch that does not
  apply exactly is a hard error naming the file `[plan]` (a
  mid-execution strategy switch is the model's forbidden shape; the
  literal selection is the declaration).
- cherry-pick's cross-command authorship inversion: the clean pick's
  git authorship is announced via the generated notice (its row is
  discovered-kind), closing the tally's near-miss.
Red-first for each retrofit (no wave pins exist for these — the tally
reproduced them live; the reds are written here).

**Verify:** the model test enforces the table; every retrofit's red
goes green; the existing delegation-notice and payload tests stay green
under the generated mechanism; `--dump-schema` shows the new members.

---

## Phase 3 — Conclusion repairs

Depends on 0.4 (the probe), 1.2 (oplog keys for idempotence), and
Phase 2 only for the payload-member conventions (3.1 coordinates with
2.2 on the conclusion schemas; if run before Phase 2, 3.1's members are
added by hand and 2.2 adopts them).

### 3.1 The family code and envelope-always

Ruling `[user]`, widened by grounding `[plan]`: one registered code —
"the commit stands, but a step after it did not finish" — produced by
EVERY commit-stands aftercare failure of every pipeline author, with
the JSON envelope always emitted and the payload carrying what
happened. Slot: 26 or the 12/13 gap (grounding confirmed both free;
the implementor picks and documents adjacency reasoning).
- The seven native shapes (grounding §1a table, including shape 0 — the
  pipeline's own post-ref-update reconcile failure): the die() sites at
  sequencer_continue.go:516/524/529, revert_cmd.go:283/288/293, and
  sequencer_delegate.go:184 become report-then-return; a plain return
  also releases the pending locks (safer than die — grounding
  verified). Shape 0 requires a typed post-ref-update error (or
  partial result carrying the created SHA) out of internal/commit —
  which gives ordinary commit/amend/mv the same envelope-and-code
  behavior on their own post-ref failures (the widening).
- consumeAutostash's eight outcomes map into the payload: an
  `autostash` member with the full state enum (absent / applied /
  applied-pointer-left / applied-cleanup-failed / stored /
  stored-pointer-left / store-failed) carrying the stash SHA where one
  exists, plus a `residue` list shared by continuePayload and
  delegatedPayload. The one-shot payload constraint (Context.Payload
  panics on a second call) forces finishConclusion and the aftercare
  steps to RETURN structured outcome data; one payload is built at the
  end.
- The four payload-less delegated returns
  (sequencer_delegate.go:137-139, :158-161, :166-170, :218-223) gain
  payload supply.
- The stored-autostash exit moves from General to the family code;
  exits stay outcome-only (mode never rides the exit code).

**Verify (red to green):**
`TestConclusionEmitsAnEnvelopeWhenThePostCommitStepFails`,
`TestStoredAutostashPayloadIsDistinguishableFromCleanSuccess`. NEW:
each aftercare shape exits the family code with the envelope present;
ordinary commit's post-ref reconcile failure emits the envelope naming
the created sha; registry/table regenerated.

### 3.2 The stale-autostash guard and doctor's orphan check

Contingent on 0.4 `[probe]`. Key: a MERGE_AUTOSTASH is consumed only
when its stash commit's first parent equals HEAD AND its message
carries git's autostash shape; a stash failing the key is NOT consumed,
NOT deleted — it is named in the payload residue list and left for
doctor. Doctor gains a check (registration-table row) reporting a
MERGE_AUTOSTASH present without MERGE_HEAD, severity warn (an
operator's mid-recovery repo must not exit 50 for this alone — the
wave test ignores the exit); `--action fix` offers store-as-stash,
minted through the effects handle (Phase 4's doctor-mint convention).
Fallback if the probe disproves the message shape: the parent check
plus refuse-to-consume-when-not-established, with the genuine-autostash
green test converted per whatever the probe DID record (a sanctioned
rewrite documented then).

**Verify (red to green):** `TestConclusionDoesNotConsumeAStaleAutostash`,
`TestDoctorReportsAnOrphanedAutostash`. Green stays:
sequencer_autostash_test.go's four (the genuine case still applies).

### 3.3 Crash-window idempotence

At conclusion entry, after refuseWrongState and before the stages read
(sequencer_continue.go after :433): if the operation's commit already
stands — keyed on the pipeline's own oplog entry for this ref (op
matches, sha == HEAD; the entry exists in the crash window because the
append precedes the reconcile), corroborated for merges by HEAD's
parent set equaling 1+MERGE_HEADS — the conclusion finishes the
cleanup steps instead of committing again, reporting what it did.
Scope limit stated in code and docs: a crash AFTER sequencer.Cleanup
reads as nothing-in-flight (exit 5) and its index/worktree residue is
Phase 3.7's doctor repair's territory, not this check's.

**Verify (red to green):**
`TestReRunAfterTheCrashWindowDoesNotMintASecondMergeCommit`. Green
stays: the ordinary conclusion suite.

### 3.4 Delegation delete-after-continue

The delete arm of materialization — declared `delete` AND a declared
ours/theirs naming an ABSENT stage (both destroy; grounding §4) —
moves after git's `--continue` succeeds on the queued path
(sequencer_delegate.go: materialize writes-only before :125; deletes
after :133). A post-continue delete failure is a commit-stands
aftercare failure (the family code; delegatedPayload's residue names
it). On stoppedAgain the deletes do not run and the report states the
files remain while the adopted index says removed. Residual risk
documented: a later queue step touching a deferred-delete path aborts
as untracked-would-be-overwritten — narrower than today's loss, stated
in the guide.

**Verify (red to green):**
`TestQueuedDeleteDoesNotDestroyTheFileBeforeGitCommits`. Green stays:
the delegation suite.

### 3.5 Deletion-honest reporting

worktreeEffects (sequencer_continue_cmd.go:380-390) learns the
absent-stage fact: conclusionResult and delegatedOutcome carry the
sides (or the precomputed written/removed lists), and a declared
ours/theirs resolving to an absent stage renders under deleted /
would-be-deleted, moved between the groups (not an extra sentence — the
pinned wording requires it).

**Verify (red to green):**
`TestConclusionReportsDeletionWhenResolvingToTheDeletingSide` and its
preview twin.

### 3.6 The overwrite refusal

Ruling `[user]`, with the grounding-forced accepted set `[plan]`: a
declared resolution whose materialization would destroy disk content
matching NONE of {the three stage blobs, git's own emitted content
(the AUTO_MERGE blob or the reconstruction — markerCheck.emittedContent
already computes it)} REFUSES, naming the file and suggesting
`worktree`; the refusal covers ours/theirs AND declared delete AND
absent-stage deletes (all destroy the same hand-edit; asymmetry would
be arbitrary). A dedicated qualified flag `--discard-unmatched-worktree`
(name is this plan's, weakly held) elects destruction, following the
--allow-public-remote per-condition pattern. Implemented as a second
per-path verdict inside the verifyMarkers pass — which must NOT honor
the marker exemption attribute for this check (the exemption is about
markers, not about destroying edits). Comparison is byte-compare
against stage blobs via CatFileBlob (no clean-filter hashing trap). New
registry code with its table row. The untouched-conflicted-file case
(disk = git's marker-laden write) matches emitted content and does not
refuse — the green worktree-writing tests stay green.

**Verify (red to green):** `TestStageResolutionRefusesToOverwriteAHandEdit`.
NEW: the flag elects destruction; delete/absent-stage variants refuse;
the six sequencer_conclusion_worktree tests stay green.

### 3.7 The unmerged-index guard and doctor repair

Ruling `[user]`, scoped by grounding `[plan]` to git parity: the
pipeline refuses when the SHARED index holds ANY unmerged entries —
mirroring guardSequencer's placement (Execute/Amend/Reword entries;
mv reaches the pipeline and inherits) — except for requests that are
conclusions (req.Sequencer declared / IndexBaseSharedIndex). The
refusal names git's own fact (unmerged files) and the way out:
`safegit doctor --action fix`, which gains the orphaned-unmerged repair
— re-stage disk content to stage 0 via HashObjectWriteBytes +
SetIndexStage0 (the zero-mode removal replay clears stages 1-3), ONLY
when no operation is in flight, under the worktree operation lock (the
repair is a second shared-index writer; the single-writer comment at
internal/git/index_resolve.go:33-35 updates). New registry code for the
refusal. Doctor's repair is minted through the effects handle.

**Verify (red to green):** `TestCommitRefusesAnUnmergedIndex`. NEW:
the doctor repair resolves a planted orphaned unmerged state and the
subsequent commit succeeds; conclusions still commit during unmerged
state.

### 3.8 Undo's auto-bump ordering

requireAutoBumpDecision joins undo's entry between loadConfig and the
operation lock (undo.go:75-80), with the same refusal shape every other
commit-family route uses; the two comments that describe the old
after-the-rollback discovery (autobump.go:210-212, :131-136) update.

**Verify (red to green):**
`TestUndoRefusesBeforeMovingTheRefWhenTheParentHasNotAnsweredAutoBump`.

---

## Phase 4 — Effects honesty

Depends on 1.5 (the dry-run-never-prompts rule) and Phase 3.1's family
conventions only where payloads overlap. All seven mints `[user]`.

### 4.1 Undo

- The ref update and root-undo ref deletion mint through the effects
  handle with REAL SHAs (both known from the oplog before anything
  moves), following the effectsRefUpdate shape with a NEW exemption
  row (kind effects-handle; reusing the commit row would falsify its
  ID). The mint is the LAST effects action on the dry path with the
  return immediately after; the per-ref lock block (undo.go:244-254)
  wraps in the not-dry-run condition so the green no-lock table row
  stays green. Everything after the update (reconcile, notices,
  auto-bump, oplog append) stays execute-only. Exactly ONE mutation
  record on the execute path (the pinned count).
- Undo gains a payload schema (ref, undone op, count, from, to —
  nullable for root undo — root_undo, dry_run, optional parent-bump
  sha and partial-restore subjects), registered on the command; the
  exemption-table enumeration test gains its row; the divergences
  sentence hard-coding "ten rows" updates in Phase 7.

**Verify (red to green):** `TestUndoDryRunRecordsTheRefUpdate`,
`TestUndoRecordsTheRefUpdateAndCarriesAPayload`,
`TestRootUndoRecordsTheRefDeletion`. Green stays:
TestDryRunCommitFamilyTakesNoLocks (undo row),
TestCommitPerformsExactlyOneRefUpdate.

### 4.2 Unlock and doctor fix

- unlock: the force-release removal mints via Effects.Remove on the
  already-computed lock path; the existence and liveness refusals stay
  in front; the deliberately-unconditional rationale stays.
- doctor fix: the removals (orphan tmp dirs, legacy queue dir, legacy
  policy file, publication temps, and the submodule twins) mint via
  Effects.Remove over paths the read-only scanners compute in BOTH
  modes; the STALE-LOCK removal keeps its flock+inode judgment by
  injecting the remover into reclaimLocked (the single unlink goes
  through the handle; the judgment stays in internal/lock — a bare
  handle Remove would regress the documented safety property); under
  dry-run the reclaim path is not used at all (it would report
  reclaimed for unremoved files) — the scan-based dry path stays, and
  lockScan gains PATHS so the records name what the test matches.
  doctor's direct os.Remove style is replaced accordingly; diagnose
  stays effect-free.

**Verify (red to green):** `TestUnlockRecordsTheLockRelease`,
`TestDoctorFixRecordsItsRemovals`.

### 4.3 Backup

- backup backup: the dry path mints the push argv with a PLACEHOLDER
  lease expectation (the network read stays off the preview by
  design), via execGitPush under the existing push exemption and
  grant; the exposure confirmation stays off the dry path.
- backup restore: the fetch and the ff-only merge both mint (execute
  order), via Effects.Run under a NEW effects-handle exemption row
  (the operator-cwd mutation row's reason does not cover a
  safegit-built argv).

**Verify (red to green):** `TestBackupDryRunRecordsThePush`,
`TestBackupRestoreDryRunRecordsTheFastForward`.

### 4.4 The parent-bump preview record

maybeAutoBumpParent's dry path records the spawn argv (grant, cwd,
resource as the real call) with recorded:true; the Triggered-by
trailer inside the recorded message carries the preview placeholder,
never a phantom SHA (the child's commit exists only in the deleted
quarantine). The fix sits in maybeAutoBumpParent so every caller
(commit, mv, undo, revert, conclusions, delegation) inherits.

**Verify (red to green):** `TestSubmoduleCommitPreviewRecordsTheParentBump`.

---

## Phase 5 — Intake and mv refusals

Depends on nothing after Phase 0; sequenced here so the commit pipeline
is stable before Phase 6 builds on it. Both rulings `[user]`.

### 5.1 mv refuses a missing destination directory

checkMvPair (mv.go:320) gains the missing-parent-directory reason
(Stat of the destination's parent — root and absNew already in scope),
inheriting the all-pairs-collected refusal (exit 19, nothing moved,
nothing committed). A qualified flag `--create-missing-directories`
(name weakly held) elects creation, declared Optional with its
omitted-behavior named in help per the codebase convention; when
elected, ensureParent's existing minted-mkdir-and-rollback machinery
runs unchanged. Sanctioned rewrites: TestMvCreatesTheDestinationDirectory
(becomes the flag-elected form), the two scrub_moves fixtures that move
into nonexistent directories (gain the flag or pre-create), the
commands-guide line, and the divergences entry (rewritten to the
refusal with the preamble roster updated).

**Verify (red to green):** `TestMvRefusesAMissingDestinationDirectory`.
NEW: the flag elects creation with the mkdir recorded in previews.

### 5.2 Escaping symlink targets refuse

noticeEscapingLinks' site in resolveFiles becomes a refusal (pre-loop,
nothing staged, both commit and amend inherit), naming the literal
target text; a qualified flag `--allow-escaping-targets` (name weakly
held) elects committing, restoring today's behavior including the
notice line. New registry code (the situation has none; borrowing
PathMatchedNothing would stretch its meaning), with table regeneration
and the two guard tests. Sanctioned rewrites:
TestCommitSymlinkEscapingTargetIsCommittedWithANotice (becomes the
flag-elected form), the divergences entry (mixed-notice to deliberate
refusal, roster updated), the commands-guide symlink policy text.
TestCommitSymlinkInsideTargetIsSilent stays green untouched.

**Verify (red to green):** `TestWave2CommitEscapingSymlinkIsRefused`;
its non-escaping control stays green.

---

## Phase 6 — Auto-record-moves

Depends on Phases 0.2, 2 (payload conventions and the mode model's
notice mechanism), 3 (conclusion stability), and 5 (intake stability).
The feature `[user]`; its spec tests are written INSIDE this phase per
ruling. The design is the ruled amendment set over
`todo/move-records-for-undeclared-moves.md`.

### 6.1 The raw delta

git.DiffTree's format changes to raw+z (with no-abbrev), extending
ChangedPath with source/destination modes and blob SHAs; the
root-commit synthesis branch fills them from the tree entries it
already holds. Existing consumers read Status/Path only and are
unaffected (grounding verified). One unit pin per new field.

### 6.2 The mint engine

In tryCommit between the delta (commit.go:479) and message assembly
(:515): pair THIS commit's deletions against ITS additions by exact
blob SHA, with the fences, in order:
- regular-file modes only (100644/100755; symlinks and gitlinks never
  pair; exec-bit changes across a pair are allowed);
- the empty blob never pairs;
- ambiguity refused, never tie-broken: a blob with two candidates on
  either side mints nothing for that blob;
- the parent-tree uniqueness fence: a deletion pairs only if its blob
  occurs at exactly one parent path, and the addition's blob at no
  other path in the commit's own tree (the treeIndex machinery gains a
  paths-by-blob view; zero-candidate commits — the delta's deleted and
  added blob-sets do not intersect — pay no listing at all);
- declarations suppress inference for any path they name on either
  side;
- shared-index requests (conclusions, restructured revert) skip
  inference entirely (req.IndexBase is the exact predicate; amend has
  no IndexBase and gets its own arm, 6.4).
Ambiguity and cap refusals produce a one-line stderr notice (the
intake-notice channel: unconditional stderr, emitted once — see the
attempt rule) suggesting the explicit declaration, and the refused
pairs ride the commit payload.
CAS-attempt rule `[plan]` (resolving the grounded impossibility: the
commit-msg hook caches the composed message on attempt 1): inference
runs per attempt; if a retry's inferred set differs from the set baked
into the cached message, the operation ABORTS with a clear
transient-race error advising re-run — the common retry (unchanged
blobs at the moved paths) proceeds; no commit ever carries records
describing a parent it does not have. The refusal set is thereby
pinned automatically (a pair ambiguous on attempt 1 stays refused: a
retry that would UN-refuse it changes the set and aborts).

### 6.3 The witnessed subtree collapse and the cap

When the delta fully witnesses a uniform prefix mapping — every paired
deletion under P maps one-for-one, same suffix, identical blob, to Q;
no other delta entry touches under either prefix; P is empty in the
commit's own tree (derivable from the parent listing minus the paired
deletions, no extra call) — ONE observed subtree record is minted
instead of per-file records `[user]`. Scattered (non-uniform) pairs
mint per-file up to the cap; above it, minting is refused with the
declare-it notice. The cap is a package const in internal/commit
(value 20, this plan's number, weakly held) `[plan]`.

### 6.4 The origin token, supersede, amend and revert

- The machine token is `observed` `[plan]`; its documented meaning is
  "derived by safegit, not claimed by a person" (covering both mint
  pairing and revert-inverse derivation); `declared` and `derived`
  stay reserved-unused. Token absence means declared — every existing
  record stays valid (the 0.2 reservation guarantees no ambiguity).
  Record gains an Origin field; the encoder emits the token only for
  observed records; ParseRecord accepts the reserved slot for exactly
  this token (the 0.2 transitional subtest is rewritten here,
  sanctioned). RewriteMessage round-trips the token (or every scrub
  strips it — pin this). RemoveMovedRecordsNaming and the projection
  are origin-agnostic except the projection's declared-beats-inferred
  tie-break and its doctrine comment reversal.
- Supersede `[user ruling, plan mechanism]`: a declaration matching an
  existing un-retracted OBSERVED pair emits a retraction of the
  observed id plus the new declared record in one commit — the
  catalog's replacement idiom; no line is ever dropped, so MovedLines'
  verbatim-carrier contract is untouched. Declared-vs-declared still
  refuses (pinned). refuseRedeclaredPairs becomes origin-aware via
  ReadMoves (already a parser).
- Amend: preservation stays verbatim for ALL records; amend inference
  is ADDITIVE (mints only pairs absent from the preserved set),
  computed against the authoring event's own delta — the new tree vs
  the replaced tip's first parent (a second diff-tree on the amend
  path; the existing payload delta keeps its meaning). Reword mints
  nothing (no tree change) and preserves everything `[plan]`.
- Revert inverses: EVERY auto-minted inverse carries the machine token
  `[user]` (ruling-literal: the token means "derived by safegit, not
  claimed by a person", and no person claimed the inverse regardless
  of the source record's origin). conclusionMovedRecords sets
  Record.Origin to observed unconditionally.

### 6.5 Payload and notices

The commit payload gains the move members — the minted records
(declared AND observed; the declared-records payload gap closes in the
same schema commit), the refused-ambiguity facts, and the cap-refusal
fact — as one six-edit lockstep commit (struct, schema, required list,
result structs, emission sites) for commit and amend. mv's payload
already carries its records.

### 6.6 Contract reversals and the catalog

Execute the reversal table the grounding enumerated: the --moved help
string and registration comment, the four in-code doctrine blocks, the
DiffTree doc qualification, the architecture and commands-guide
declared-only sentences, the _CLAUDE template bullet, the moves_test
file-header doctrine and assertNoRenameNotice re-pointing (the mint
notice must not match its two strings — re-point the helper at
no-content-adoption/no-auto-staging, which the cross-session guards
keep pinning), and the divergences moves entry REWRITTEN (same commit
as the behavior, per the catalog's own rule) with the provisional
roster updated. The consumer-repo commit-msg-hook consequence (a
strict message-format hook may start failing on coincidental pairs) is
documented in the guide.

**Verify (spec tests, written red-first inside this phase):** minting
(file pair; uniform-directory single subtree record; scattered cap
refusal); every fence (two-candidate refusal both directions; the
identical-copies parent-uniqueness case; symlink pair; gitlink pair;
empty blob; declaration suppression); the shared-index skip
(conclusion and restructured revert mint nothing by inference); the
CAS mismatch abort (two sessions, differing attempt-2 delta); origin
round-trip through scrub match; supersede (retract+redeclare emitted;
declared-vs-declared still refuses); amend additive; reword
preservation; observed-inverse on revert; retractability of an
observed record; the payload members; the notice lifecycle. The 0.2
transitional subtest rewrite. The projection tests stay green.

---

## Phase 7 — Documentation

After behavior stabilizes (Phases 1-6). One pass over the hand-written
surfaces and templates only; generated files heal via regeneration at
the end.

- The machine-contract boundary paragraph: into the integration
  guide's machine-mode section at the seam the grounding identified
  (after the parse-the-whole-stream sentence), describing the THREE
  failure shapes truthfully (envelope with payload; envelope with
  null payload on guarded refusals; no envelope on pre-dispatch died
  paths — now shrunk by Phase 3.1), the payload-carries-aftercare
  convention, and the capture-mode UTF-8 known limit from 1.3.
- Every hand-written doc updated for Phases 0-6's changes (the
  per-phase Phase-7 notes above name the rows: exit-table
  regeneration; the 25-widening rows; the reset/bisect guard scope;
  the mode-model include; the conclusion aftercare and family code;
  the delegation delete ordering's residual risk; the overwrite
  refusal and its flag; the unmerged guard and doctor repair; the mv
  and symlink refusals and flags; the whole moves-feature surface).
- Divergence catalog: the entries not already rewritten in their
  phases — resolution-keywords flips provisional-to-deliberate
  (ratified), the autostash-exit entry rewords under the family code,
  the timeout entry deletes with its mechanism (1.4), the
  non-executable-hooks entry rewrites to refuse-both (1.4), the
  revert-split entry rewrites under the mode model (2.3), plus the
  preamble roster and the two hard-coded counts (the ten-rows
  exemption sentence; the 1800 prose).
- The integration guide's hardcoded app_version example becomes a
  placeholder; the dry-run network claim is re-scoped to name backup
  restore's ls-remote read (the live falsehood the await-todo
  records).
- Divergence catalog additions from the late rulings: the revert entry
  rewritten to the single-form ruling (done in 2.3's commit — verify);
  the aftercare-nonzero entry generalized to every pipeline author
  (3.1); the resolution-keywords entry's rewrite notes the extended
  overwrite protection (3.6). The unmerged-entries refusal MATCHES git
  and therefore needs no divergence entry — verify none was
  accidentally added.
- Finish: `--dump-schema` (writes the tracked schema; commit via rlsbl
  commit) and bare `selfdoc gen`.
- STRUCK, with the log corrected: the earlier "CLAUDE.md regression"
  item — grounding proved template and generated file consistent and
  current; the stale text existed only in harness-injected context
  snapshots.

**Verify:** every named row resolved; regeneration clean; a fresh
spot-check reads each changed claim against code.

## Phase 8 — Changelog

- The three edits by id (grounding recorded the ids): entry 11's
  description fixes the nonexistent colon grammar to the arrow form;
  entries 17 and 32 retype to breaking. Entry 11's commit list is NOT
  touched (it rides a batch exclusion).
- The split: one `changelog add` giving the dry-run-uninstall
  destruction fix its own fix-type entry on its commit (no exclusion
  needed), plus one edit dropping the clause from the breaking
  uninstall entry.
- Coverage: every campaign-2 commit (45 uncovered at grounding time,
  growing) — feature-area user-facing entries for the new behaviors
  (the family code, the mode model's announcements and payload
  members, the mints, the new refusals and their flags, the
  auto-record-moves feature, the timeout-override removal and
  refuse-both as breaking) and no-user-facing clusters for tests,
  log entries, and internal work. Never fabricate; the enforced
  format_version rides automatically.

**Verify:** `rlsbl check --tag changelog` fully green.

## Phase 9 — Verification

- Full `go test ./... -race` green; the stress run; the GOWORK=off
  full run; gofmt -l clean; go vet clean.
- Regenerate and COMMIT testdata/exit-sites.txt (pre-empting the
  release hook, which the grounding proved will otherwise block the
  first release attempt), with its commit joining the existing
  non-user-facing inventory entry's area.
- Reconcile against testdata/campaign2-baseline.txt (every red from
  0.1 green or sanctioned-rewritten per this plan; zero unexplained
  lines), and confirm the campaign-1 frozen artifact's reconciliation
  still holds.
- One fresh audit per phase (0-8), each briefed with this plan, the
  execution log (ratified deviations are deliberate), and its phase's
  red-test list; a remediation pass closes findings.

## Phase 10 — Release

- Todo triage: this plan, the campaign-1 plan, and the execution log
  move to todo/.done; `move-records-for-undeclared-moves.md` (consumed
  by Phase 6) moves to .done; the three await todos, the three
  contingent todos, and the reversibility todo stay.
- `rlsbl release init`; the release file (minor bump; a description
  covering both campaigns; a context block naming the redesign and the
  postponement); commit it; then the single
  `rlsbl release run --no-allow-dirty --watch --approve-consequential`.
- Post-release notes (outside the release): the six fleet repos
  holding legacy no-op placeholder hooks need `hook migrate` or
  placeholder deletion once the new version installs; the
  claudewheel/saferm todos unblock on mv shipping; reset/rebase
  recovery's data precondition is now met.

---

## Dependency spine

| Phase | Depends on | Notes |
|---|---|---|
| 0 | — | 0.2 unblocks 6; 0.4 unblocks 3.2 |
| 1 | 0 | 1.1 unblocks 2; 1.2/1.3 share coord_cmd.go |
| 2 | 1.1, 1.3 | the centerpiece; 2.2's schema conventions feed 3.1/6.5 |
| 3 | 0.4, 1.2, (2 for payload conventions) | 3.1 before 3.2-3.5 (they report through it) |
| 4 | 1.5; 3.1 only for payload overlap | |
| 5 | 0 | before 6 (pipeline stability) |
| 6 | 0.2, 2, 3, 5 | spec tests written inside |
| 7 | 1-6 | |
| 8 | 7 | |
| 9 | 8 | |
| 10 | 9 | |

Phases 4 and 5 are mutually independent and independent of 3 except
the noted payload overlap; the numbered order is safe sequentially and
sequential execution is the default (shared worktree, one build).

## Appendix A — red-test map (wave pins to subphases)

| Red test (file) | Subphase |
|---|---|
| grammar_reserved_keywords_test.go (2) | 0.2 (one subtest rewritten in 6.4) |
| wave2_passthrough_oplog_positions_test.go (2) | 1.2 |
| machine_contract_json_document_test.go (2) | 1.3 |
| wave2_hook_timeout_override_dead_test.go (2) | 1.4 |
| wave2_hook_nonexec_local_refusal_test.go (1) | 1.4 |
| sequencer_conclusion_envelope_test.go (2) | 3.1 |
| sequencer_stale_autostash_test.go (2) | 3.2 |
| sequencer_conclusion_crash_window_test.go (1) | 3.3 |
| sequencer_delegation_delete_test.go (1) | 3.4 |
| sequencer_conclusion_deletion_report_test.go (2) | 3.5 |
| sequencer_conclusion_overwrite_test.go (1) | 3.6 |
| commit_unmerged_index_test.go (1) | 3.7 |
| undo_autobump_precheck_test.go (1) | 3.8 |
| machine_contract_undo_effects_test.go (3) | 4.1 |
| machine_contract_invisible_mutations_test.go (5) | 4.2-4.4 |
| wave2_mv_missing_destdir_test.go (1) | 5.1 |
| wave2_symlink_escape_refusal_test.go (1 red + 1 green control) | 5.2 |

Reds with no wave pin, written red-first inside their subphases: 1.1
(reset --merge/--keep, bisect stepping, declined-check report), 1.2
(failed-merge entry, doctor no-misfire, root-undo doctor fix), 1.5
(dry-run-never-prompts pins), 2.3 (each retrofit), 3.1 (per-shape
family exits incl. ordinary commit's), 3.6 (flag + delete/absent
variants), 3.7 (doctor repair), 0.3 (config-floor pins), and all of
Phase 6's spec tests.

## Appendix B — sanctioned rewrites (existing green tests this plan
deliberately changes; anything else that breaks is a defect)

- 1.4: TestSkipNonExecutable; TestSetOutputCapturesDiscoverWarning;
  TestHookListRendersStateAndOrigin's hook-run tail;
  TestDoctorExitCodeFollowsErrorFindings's warn fixture;
  TestParseTimeoutOverride (deleted with its parser).
- 2.3: any test pinning the silent revert fallback's silence OR the
  passthrough revert arm's existence (multi-commit reverts, forwarded
  exotic flags — including the 7.4-era multi-commit-emits-no-records
  test, which becomes a refusal pin), the scrub submodule infof-only
  announcement, and the --hunks 3way retry (the stage package's retry
  tests).
- 3.2: contingent — only if the 0.4 probe disproves the message shape.
- 5.1: TestMvCreatesTheDestinationDirectory; the two scrub_moves
  fixtures moving into nonexistent directories.
- 5.2: TestCommitSymlinkEscapingTargetIsCommittedWithANotice.
- 6.4: grammar_reserved_keywords_test.go's keyword-before-a-pair
  subtest (the observed token becomes valid).
- 6.6: moves_test.go's assertNoRenameNotice re-pointing;
  moves_cross_session_test.go's header prose.
- 1.2: the TipSHA multi-spelling search's tests, if consolidated.
