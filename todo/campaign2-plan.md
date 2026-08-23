# Campaign 2: implementation plan (revision 2)

Self-contained: every decision stated in full; a zero-context session can
implement any subphase from this file plus the cited code. Anchors
verified at HEAD `c3c05c3` (2026-08-23) by five grounding investigations
plus one adversarial plan critique whose findings this revision absorbs;
claim text is the anchor, expect small drift.

**EXECUTION IS ON HOLD.** The user's standing order: implementation
starts only on a further explicit go. This file existing is not that go.

**Status quo.** The campaign-2 spec suite is committed and deliberately
red: 30 red tests (23 in the first-wave files, 7 in `wave2_*`, plus one
green control in the symlink wave file) — the red map in Appendix A was
independently verified exact. Nothing pushes before the single release.
The execution log (`todo/redesign-campaign-execution-log.md`) is the
origin of every ruling here; it is append-only and NOT chronological —
later entries supersede earlier ones wherever they conflict.
Implementor discipline: commits via the INSTALLED safegit (single -m,
plain paths, repo root); scratch repos only via t.TempDir; batch edits
per the standing rule (dry-run first, output examined, then execute;
occurrence-count check + full diff review); red-first for behavior
changes; one fresh audit per phase, never per subphase; `jq` is required
by `scripts/test-baseline`.

**Decision-origin note.** `[user]` = decided by the user (per the log's
standing convention, recommended-option picks are weakly held and freely
reversible by the user; that qualifier applies to every `[user]` mark
below unless the log says deliberate). `[plan]` = orchestrator
resolution of a grounding- or critique-discovered gap, reversible on
request. `[probe]` = contingent on a recorded-fact probe. Under-marked
items from revision 1 are corrected here: the 3.1 widening, 3.7
git-parity scoping, and 6.2 race-abort are `[user]`; 1.1's derivation is
`[user]`; 1.5 resolves a question the log had parked for the user and is
`[plan]` — flag it at the phase audit.

**Exit-code assignments `[plan]`** (all free slots verified; unique and
ascending enforced by tests): 26 = the commit-stands family code; 27 =
the conclusion overwrite refusal; 28 = the unmerged-index refusal; 29 =
the escaping-symlink refusal. Exit 25's constant is RENAMED to match its
widened meaning (`HookNotExecutable` family spelling; the old name would
lie at every call site).

**Non-goals:** reset/rebase recovery (todo active; 1.2 ships its data
half) `[user]`; per-command bypass surfacing (deferred) `[user]`; live
streaming under machine mode for guarded commands (framework tee
absent; no workaround per the push-streaming todo); the move-record
reader command (NOT planned anywhere `[user]` — the framework-repo
filing was deleted on order, archived); the scrub backward
path-projection feature and, with it, the ruled
projection-visible-before-consent requirement `[user]` — that ruling
binds the feature whenever it is built, and it is not in this campaign;
the three ruling-await todos and the effects-handle method-set await
todo (FOUR await files total); the three contingent todos; the
global-rules batch-edit adoption (outside this repo, at the user's go).

---

## Phase 0 — Groundwork

Independent subphases; sequential or one implementor.

### 0.1 Campaign-2 baseline artifact
`scripts/test-baseline testdata/campaign2-baseline.txt`, commit it. The
frozen campaign-1 artifact stays untouched.
**Verify:** committed; `--check` clean; header records HEAD and argv.

### 0.2 Grammar reservation
Turn green `internal/trailer/grammar_reserved_keywords_test.go` (both
tests; 3/3 and 6/6 subtests red today): `needsQuoting`
(internal/trailer/cquote.go:53) gains exact-match membership for
`observed`/`declared`/`derived`; `ParseRecord`
(internal/trailer/moved.go:208) refuses a bare reserved keyword in the
post-id slot (the refusal lands in Malformed via ReadMoves' existing
verbatim capture — no new plumbing). Sanctioned-later note stated in the
test: 6.4 makes `observed` valid in that slot and rewrites the
keyword-before-a-pair subtests then.
**Verify (red to green):** both tests.

### 0.3 Implicit-fallback family deletion
Delete the nine dead `<=0` config fallbacks (all verified existing, no
others): push.go:293-296, push.go:328-330, hook.go:132-135,
internal/commit/commit.go:327-330 and :573-576,
internal/commit/amend.go:143-146, :297-300, :474-477, :559-562.
`repo.Config.Validate` (repo.go:409-422, covering all four integer
keys) is the sole authority; call sites read the value directly; no
substitution survives `[plan]`. Add one loader-refuses-non-positive pin
per key (none exists).
**Verify:** pins green; the pattern greps to zero outside Validate.

### 0.4 Recorded-fact probe: autostash message shape
The existing probes (internal/git/autostash_probe_test.go) never
inspect the stash commit's MESSAGE. Add the probe pinning: a genuine
`merge --autostash` stash's message is `On <branch>: autostash`; a bare
`git stash create` yields `WIP on <branch>: ...`. (The critique
reproduced both live on git 2.54 — the expectation is confirmed; the
probe records it permanently.) 3.2 keys on this `[probe]`.
**Verify:** probe committed and green.

---

## Phase 1 — Foundations and deletions

Depends on Phase 0. FILE-SHARING (corrected): 1.1, 1.2 AND 1.3 all edit
coord_cmd.go (the reset scan and bisect list share the file with the
oplog seams and passthroughStdout; 1.2's seams sit inside handler
bodies 1.1 edits) — run 1.1-1.3 under ONE implementor. 1.4 and 1.5
share doctor.go — one implementor or sequential.

### 1.1 Derived worktree guards `[user]`
- New derived view in internal/gitexec/classify.go over `EffectsOf`'s
  `MutatesWorktree` bit; default-deny on unclassified argv (the
  `WritesObjects` precedent, classify.go:429).
- reset: delete the literal scan (coord_cmd.go:349-356; its comment's
  only-`--hard`-mutates claim is false). The reset row EXTENDS its
  conditionals to `--merge` and `--keep` (worktree-writing) while
  `--soft`/`--mixed` stay refs|index — the green pin
  `TestFlagConditionalEffects` (internal/gitexec/classify_test.go:62)
  pins exactly that and MUST STAY GREEN; do not make the row
  unconditional. `TestTableIsWellFormed` (classify_test.go:190) refuses
  empty conditional stubs — no leftovers.
- bisect: delete the six-name list (coord_cmd.go:398-404). The bisect
  row (classify.go:114-118, currently unconditional-worktree with no
  conditionals — it already disagrees with the deleted list) gains
  conditionals so the stepping vocabulary
  (good/bad/old/new/skip/run/replay/reset/start) derives the guard and
  non-stepping forms (terms, log, view) do not `[user, options
  reviewed]`. The `bisect run` consequence (non-gitignored build
  artifacts dirty the tree between invocations) is documented in the
  guide; ignored files never count.
- The marker-exemption silent skip (sequencer_markers.go:136) becomes a
  carried declined-check. STATED MECHANISM (critique F-A4):
  `verifyMarkers` returns a declined list alongside its int (signature
  change; sole caller sequencer_continue.go:450-452), the list rides
  `conclusionResult`, the human report renders it, and the conclusion
  payload schemas gain a `declined_checks` member (schema builder
  edit + required list) — coordinate with 3.1's payload work (same
  builder, one pass).
- Red-first in this subphase (no wave pins): reset `--merge` and
  `--keep` on a dirty tree refuse (exit 5; today unguarded, exit 0);
  a bisect stepping subcommand outside the old list (skip) refuses on
  a dirty tree; a conclusion over an exempt path reports the declined
  check in text and payload.
**Verify:** new reds green; TestFlagConditionalEffects and the guard
suites stay green.

### 1.2 Oplog baseline and outcome recording `[user]`
Every operation records the full ref name (git.HeadRef), the
pre-operation tip, and the post-operation tip in commit-entry spelling
(internal/commit/commit.go:649-658). Seam facts (inlined; formerly a
dangling citation):
- Pre-op tip exists only in checkout today (coord_cmd.go:158); merge
  records the post-op tip under `result`; rebase/reset/bisect/pull
  record neither; resolve HEAD before each operation.
- checkout's `ref` key currently holds the OPERATOR'S ARGUMENT
  (coord_cmd.go:168-175) — fix to the resolved ref name.
- Failure entries: the seven return-before-append sites (checkout
  :160-162, pull :211-213 and :227-229, merge :277-280, rebase
  :316-318, reset :364-366, bisect :412-414) now append entries with
  ref + old tip + EMPTY new tip + a failed-outcome marker; the
  runGuardedPassthrough unconditional append (:469-474) gains the
  outcome field and moves behind the exit code. An empty new tip keeps
  the entry invisible to `LastRefUpdate` and doctor's bypass check —
  the verified-safe shape for the fail-closed readers.
- The `--dry-run`-after-a-passthrough-name defect (the flag forwards
  to git, exits loudly, but was oplog-logged as an attempt) is cured
  on the logging half by outcome recording; the forwarding itself is
  git-argv semantics, documented in Phase 7.
- STAY-GREEN constraints (critique): `TestBackupWritesOplogEntries`
  (internal/test/backup_test.go:419) hard-pins backup's existing extra
  keys — leave backup.go's entries alone or update that pin as a listed
  sanctioned rewrite; the bypass_detect consumer chain (doctor.go:413,
  undo.go:176/181/445, stress_test.go:184-188) must keep passing.
  The TipSHA multi-spelling search (oplog.go:184-191) may consolidate
  ONLY together with the backup pin rewrite; both `LastRefUpdate`
  tests are sha-only and unaffected.
**Verify (red to green):** the two wave2 oplog tests. NEW red-first: a
failed merge writes a failed-outcome entry; doctor's bypass check does
not misfire after a clean safegit merge; the root-undo false doctor
error is gone.

### 1.3 One JSON document on the guarded commands `[user; mechanism plan]`
Under `--json`, runGitMutation captures (drop `Stream(true)` when
flags.json) and re-emits child stdout through `passthroughStdout`
(coord_cmd.go:496; stderr to stderr); human mode keeps `Stream(true)`
and live output. `Check(false)`+`ExitCode()` verified identical in
capture mode. Known limit, documented in Phase 7: non-UTF-8 child
output under --json fails the capture decode and degrades the exit to
General.
**Verify (red to green):** both machine_contract_json_document tests
(five leaking rows; the checkout row is a green forward-guard).

### 1.4 Hook rulings `[user]`
- Timeout-override deletion: the interception goroutine
  (internal/hooks/hooks.go:203-227), the 2-second select (:229-238),
  `parseTimeoutOverride` (:297-311), its table test
  (hooks_test.go:219-238), and the `bufio`/`strconv` imports; the
  three ioDone syncs survive; `SAFEGIT_HOOK_TIMEOUT_S` and the config
  key stay (separate mechanism).
- Refuse-both non-executable hooks: Discover's local-skip branch
  (hooks.go:113) becomes a typed error like the tracked branch; exit
  25 widens to "a discovered hook is not executable" with the constant
  RENAMED (header assignment); hookDiscoveryExit maps it. EXPLICITLY
  (critique F-A3): `hook run` currently exits 0 "no hooks to run" when
  discovery skips the file — it must refuse identically. `hook list`
  keeps listing (the diagnostic). doctor's hook_perms local branch
  (doctor.go:475) escalates to error severity;
  `TestDoctorExitCodeFollowsErrorFindings`'s warn fixture is replaced
  (sanctioned).
- Doc rows THIS subphase falsifies (Phase 7 executes; listed here for
  the audit): commands-guide.md:1546 ("skipped with a warning" — note
  it spells `.git/safegit/hooks`, NOT the tracked store),
  integration-guide.md:111, architecture.md:333 and :404 (the
  per-hook timeout paragraph deletes with its mechanism),
  commands-guide.md:584; the divergences timeout entry deletes; the
  non-executable-hooks entry rewrites.
**Verify (red to green):** the three wave2 hook tests. Sanctioned
rewrites per Appendix B.

### 1.5 Consent uniformity `[plan — resolves a user-parked question]`
One early dry-run return in `confirmDeliberate` (main.go:990-1008 —
verified: no dryRun consultation exists today; `--json --dry-run
doctor --action uninstall` exits nonzero today and NO test pins that).
push's `&& !flags.dryRun` (push.go:179) becomes redundant and goes;
backup's dry return is untouched and the rule is NOT license to run
backup's exposure classification in a preview. Write the pins the
ruling never had (dry-run uninstall without consent succeeds and
enumerates; json+dry-run emits the envelope).
**Verify:** new pins green; the consent suite stays green.

---

## Phase 2 — The declared mode model `[user]`

Depends on 1.1 (guard column) and 1.3.

### 2.1 The table and its registration test
One table in a NEW internal package (e.g. internal/modetable) `[plan —
the doc generator cannot import package main]`, imported by main.go and
by the generator. "MODE" IS DEFINED `[plan]`: a run-time branch that
changes the authorship class, the oplog op/undoability, or the preview
strategy. Orthogonal value selectors that change none of those (scrub
file's --mode x --range, scrub match's substitution x range) are ONE
row each — their selectors are documentation columns, not mode rows.
Facts: 37 pinned commands; SIX selector-bearing commands exist (push,
pull, doctor, scrub file, scrub match, scrub run — not seven);
commit's `--amend` selects the amend/reword PAIR while amend-vs-reword
resolves at run time from the file list (commit.go:142, :384) — that
is a DISCOVERED split by this plan's own definition; both rows share
the safegit authorship class, so per the announcement rule below it
carries the payload mode member and no stderr announcement `[plan]`.
Selection kinds:
- DECLARED: a required selector covers the modes.
- DISCOVERED: run-time-chosen. Requires a payload mode member always;
  requires the generated unconditional stderr announcement ONLY when
  the rows differ in authorship class or target repository `[plan]`
  (announcements exist to surface authorship/target changes;
  same-authorship splits like amend/reword announce nothing).
The seven passthrough registrations STAY passthroughs (the framework
refuses flags on them; conversion breaks argv semantics); their modes
are discovered rows; payload schemas are legal on passthroughs and
reach their handlers (verified). The registration test extends
classification_test.go's shape: command absent from the table fails;
2+ rows without a declared selector or discovered marking fails; oplog
op absent from undoableOps requires explicit Undoable:false; every row
names a preview strategy.

### 2.2 Generated surfaces
- The announcement helper: generated from the row's authorship column.
  CONSTRAINT (critique): `TestPickAndRevertContinue...`-family and
  sequencer_delegation_test.go:208 pin the LITERAL sentence "these
  commits are git's..." — the generated template for git-authorship
  rows IS that existing sentence, so the pins stay green `[plan]`; and
  reportDelegated's note is conditional on commits-created today
  (sequencer_continue_cmd.go:314) — the generated form keeps that
  condition (announce when the run actually produced git-authored
  commits or delegated execution; a no-op stop announces nothing).
- Payload `execution_mode` members: optional-string, enum per command,
  REQUIRED in the schemas the model generates for multi-mode commands
  `[plan]`; the conclusion builder gains it; `queue_delegated` is
  RETAINED (a shape discriminator, not replaced) `[plan]`.
- The doc table: marker-delimited in-place generation into the
  commands guide (the exit-table mechanism exactly:
  internal/exitcode/docgen.go shape, a scripts/gen-mode-table wrapper,
  a freshness test).

### 2.3 Discovered-mode retrofits and fallback deletions
- REVERT, SINGLE-FORM `[user]`: the git-authored arm is deleted.
  `safegit revert <commit>` = the restructured single-commit form.
  Refusals: multi-commit argv (naming the sequential form), `-S`,
  `--edit`, `--no-commit`, and any other option the pipeline cannot
  honor. STATE-CONTROL CARVE-OUT `[plan]`: `--abort` and `--quit`
  remain guarded passthrough forms — they author nothing (pure state
  cleanup, no mode split); `--skip` is refused naming revert-continue
  and `--abort`. OPLOG `[plan]`: one entry per revert — the pipeline's
  revert-continue entry; the compute-step's own `Op:"revert"` append
  (revert_cmd.go:182-187) is DELETED (today a single revert writes
  two entries; the not-undoable one goes). PAYLOAD `[plan]`: a
  dedicated single-revert schema (the continuePayload members minus
  the queue members — reusing revertContinuePayloadSchema would
  require queue_delegated/stopped_again members that lie); the two
  in-code no-schema statements (sequencer_continue_cmd.go:182-184,
  revert_cmd.go:296-298) are rewritten. The preview fix is scoped to
  the REVERT caller of previewSequencerOperation only
  (revert_cmd.go:167) — merge's and cherry-pick's recorded argv are
  correct and their previews are untouched (critique #9).
- scrub file's submodule redirect: the infof announcement becomes the
  generated unconditional one; the payload gains the submodule member;
  the swallowed enumeration error (scrub.go:154-158) becomes a HARD
  error `[plan]`. EXTENDED `[plan]`: scrub_match.go:242-244 and
  :535-538 have the same defect class (subs=nil silently drops
  submodules from the rewrite's SCOPE) — both become hard errors too;
  doctor.go:686's warn genuinely does not change a target and stays.
- The `--remap-shas-in` bullet from revision 1 is DELETED (critique
  #8): the flag IS honored on the parent during a submodule-target
  scrub, exactly as its help declares, and
  TestScrubFileInSubmoduleRemapShas pins the split. No change.
- commit `--hunks`: the silent `--3way` retry
  (internal/stage/stage.go:180-198) is deleted; failure is a hard
  error naming the file — `ApplyPatch(ctx, indexPath, patch)` has no
  path parameter, so the SIGNATURE gains the repo-relative path (or
  the caller wraps the error with it) `[plan]`; red-first (no wave
  pin; note Appendix B's revision-1 "stage retry tests" citation was
  wrong — no such tests exist; the real neighbors are the
  commit_hunks_conflict tests).
- cherry-pick's clean-arm git authorship: announced via the generated
  notice (discovered row, authorship differs).
**Verify:** model test enforces the table; each retrofit red-first
green; the delegation-notice pins stay green; `--dump-schema` shows
the members; the six unlisted-in-revision-1 revert greens are handled
per Appendix B.

---

## Phase 3 — Conclusion repairs

Depends on 0.4, 1.2, and — HARD, corrected — on 2.3 for 3.1's revert
payload (a passthrough with no schema panics in Context.Payload;
either 2.3 lands first or 3.1 itself adds revert's schema; the spine
records 2.3 -> 3.1).

### 3.1 The family code (exit 26) and envelope-always `[user]`
Scope: every pipeline author. THE CORRECTED SITE MAP (critique #1-#3;
formerly a dangling citation, now inlined):
- Genuinely post-ref die() sites converted to report-then-return:
  sequencer_continue.go:524 (finishConclusion) and :529 (auto-bump);
  revert_cmd.go:288 and :293; sequencer_delegate.go:184 (auto-bump on
  the delegated path).
- NOT converted (critique #1): sequencer_continue.go:508-517 and
  revert_cmd.go:283 route pipelineExitCode over the GENERIC pipeline
  error — pre-commit failures with real exit codes (5/7/8/9/10/16)
  that must survive. These sites gain the family code ONLY via the new
  typed post-ref-update pipeline error.
- The typed error: internal/commit returns a typed
  commit-stands-partial result (carrying the created SHA and the
  failed step) from the post-updateRef failures — THREE sites:
  commit.go:665-667 (reconcile), amend.go:356-358 and :607 — through
  BOTH result types (CommitResult and AmendResult).
- Additional aftercare family members: the post-ref auto-bump die
  sites in every author — commit.go:234-236, :433-435 (amend),
  :503-505 (reword), mv.go:576-585, undo.go:310-312 (whose runCommit-
  family handlers return ints; the widening threads the family code
  through those returns).
- consumeAutostash's SEVEN outcomes (count corrected) map into the
  payload: an `autostash` member (state enum + stash SHA where one
  exists) plus a `residue` list on continuePayload AND
  delegatedPayload; the four payload-less delegated returns
  (sequencer_delegate.go:137-139, :158-161, :166-170, :218-223) gain
  payload supply. Context.Payload is one-shot — finishConclusion and
  aftercare RETURN structured outcome data; one payload is built at
  the end. The reason die() converts to return is the ENVELOPE seam
  (os.Exit bypasses finishDispatch); lock release is NOT the reason
  (die already releases pending locks — revision-1 claim corrected).
- Exits stay outcome-only; the stored-autostash exit moves from
  General to 26.
**Verify (red to green):** both conclusion_envelope tests. NEW
red-first: each aftercare shape exits 26 with the envelope; ordinary
commit's post-ref reconcile failure emits the envelope naming the
created sha; registry + generated table regenerated (exit_table_test
enforces).

### 3.2 Stale-autostash guard (0.4-keyed) + doctor orphan check `[user]`
Key: consume MERGE_AUTOSTASH only when the stash commit's first parent
== HEAD AND its message carries git's autostash shape (`On <branch>:
autostash`) — the 0.4 probe records the fact (already confirmed live;
the revision-1 "fallback" is deleted — it could not pass the red test
and the probe's outcome is known). A failing stash is not consumed,
not deleted: named in the residue list; doctor's new check
(registration-table row, WARN severity `[plan]`) reports
MERGE_AUTOSTASH-without-MERGE_HEAD. The `--action fix` store-as-stash
action ships in 4.2 with the doctor-mint work `[plan — resolves the
revision-1 spine cycle: 3.2 delivers the CHECK, 4.2 the FIX action]`.
**Verify (red to green):** both stale_autostash tests (the diagnose
half here; the fix half's mint pin in 4.2). Green stays: the four
genuine-autostash tests.

### 3.3 Crash-window idempotence `[user]`
CORRECTED placement (critique #15): after refuseWrongState AND the
detached-HEAD refusal (sequencer_continue.go:436), after the stages
read (:440) and indexEditsFor (:454) — the idempotent branch needs the
edits to run finishConclusion — and BEFORE checkCompleteness (:445
moves below it), since a crash-restored index reads as
resolved-but-not-conflicted there. Key: the pipeline's own oplog entry
for the ref (op matches, sha == HEAD; the append precedes the
reconcile so it exists in the window), corroborated for merges by
HEAD's parent set equaling 1+MERGE_HEADS. ACKNOWLEDGED WINDOW
(critique): a crash between updateRef (:615) and the append (:649,
error discarded) leaves no entry — merges are still caught by
parentage; a pick/revert in that sliver is not detected (stated scope
limit, alongside the crash-after-Cleanup half that is 3.7-doctor
territory).
**Verify (red to green):** the crash_window test. Green stays: the
conclusion suite.

### 3.4 Delegation delete-after-continue `[user]`
CORRECTED anchor (critique #13): the deferred deletes run on the
SUCCESS path after the runErr branch returns (sequencer_delegate.go's
:225/:227 region), never at :133 (AdoptIndexFrom) — placing them
there would run them before the stopped-again branch (:188-207), which
must NOT delete and must report the files as still present. Deferred
set: declared `delete` AND declared ours/theirs naming an ABSENT stage
(both destroy). A failed post-continue delete is a family-26 aftercare
shape in delegatedPayload's residue. Residual risk documented: a later
queue step touching a deferred path aborts as
untracked-would-be-overwritten.
NOTE (critique Tier 3): 3.6's refusal makes the existing
delegation-delete red test VACUOUS (its unique content refuses before
delegation) — this subphase adds a fresh ordering pin whose fixture
passes the overwrite check (disk content equal to a stage blob), and
the old red test is adjusted alongside 3.6 (sanctioned, Appendix B).
**Verify:** the (adjusted) delegation-delete pin green; the
stopped-again no-delete report pinned.

### 3.5 Deletion-honest reporting `[user]`
As revision 1 (verified sound): worktreeEffects learns the
absent-stage fact via sides carried on conclusionResult and
delegatedOutcome; the path moves between the written/removed groups.
**Verify (red to green):** both deletion_report tests.

### 3.6 The overwrite refusal (exit 27) `[user; mechanism corrected]`
CORRECTED mechanism (critique #5): a SEPARATE per-path verdict loop
over the full declared set — NOT inside the verifyMarkers pass, whose
verifiablePaths EXCLUDES delete-resolved paths and whose exemption
skip (contested with 1.1's rewrite) must not apply here. For each
declared path whose materialization would destroy disk content:
accepted set = the three stage blobs UNION git's emitted content,
computed per path on demand via markerCheck.emittedContent (currently
invoked only when unattributed blocks were found — this loop calls it
directly). WHERE EMITTED CONTENT IS UNCOMPUTABLE (octopus, `-s
resolve`: no AUTO_MERGE, labels underivable — sequencer_markers.go:57-62)
the check is SKIPPED for that path and the skip is reported as a
declined check `[plan — the 6.3-era structurally-inapplicable
precedent; refusing untouched octopus conflicts would break legitimate
conclusions]`. Covers ours/theirs overwrites, declared delete, and
absent-stage deletes; byte-compare via CatFileBlob (no clean-filter
hashing); the flag `--discard-unmatched-worktree` (name weakly held)
elects destruction; registry row for 27.
**Verify (red to green):** the overwrite test. NEW: flag elects;
delete and absent-stage variants refuse; the declined-skip renders for
an octopus fixture; the six worktree-writing greens stay green.

### 3.7 The unmerged-index guard (exit 28) + doctor repair `[user]`
Git parity: ANY unmerged entry in the shared index refuses every
pipeline commit; conclusions exempt (req.Sequencer /
IndexBaseSharedIndex). Mechanics stated `[plan]`: the guard reads
git.UnmergedStages beside guardSequencer's call sites
(commit/amend/reword entries; mv inherits via the pipeline); the
refusal names git's fact and `safegit doctor --action fix`. The doctor
repair: ONLY when sequencer.Read reports nothing in flight; takes the
worktree operation lock (a second shared-index writer — the
single-writer comment at internal/git/index_resolve.go:33-35 updates);
re-stages disk content to stage 0 via HashObjectWriteBytes +
SetIndexStage0; minted per the 4.2 doctor-mint convention (fix action
ships with 4.2's wave if sequenced there — same resolution as 3.2's).
**Verify (red to green):** the unmerged_index test. NEW: the repair
resolves a planted orphan state and a subsequent commit succeeds;
conclusions still commit during unmerged state.

### 3.8 Undo's auto-bump ordering `[user]`
As revision 1 (verified sound): requireAutoBumpDecision between
loadConfig and the operation lock (undo.go:75-80); the two stale
comments (autobump.go:210-212, :131-136) update.
**Verify (red to green):** the undo_autobump test.

---

## Phase 4 — Effects honesty `[user]`

Depends on 1.5. 3.2's and 3.7's doctor FIX actions ship inside 4.2.

### 4.1 Undo
Mint the ref update / root-undo deletion through the effects handle
with real SHAs (from the oplog; CAVEAT stated: currentSHA can fall
back to RevParse on a thin log — undo.go:221-227 — still a real SHA),
NEW exemption row (kind effects-handle; reusing the commit row would
falsify its ID), mint last on the dry path with the return immediately
after, the per-ref lock block wrapped in !dryRun (the no-lock table
row stays green). CONTRADICTION RESOLVED (critique #23): undo's dry
path returns AT the mint — it never reaches maybeAutoBumpParent, so
undo's preview does NOT carry a parent-bump record; 4.4's caller
coverage explicitly excludes undo's preview `[plan]`. The
exactly-one-record pin holds for the non-submodule case; the submodule
case records the bump too (execute path) — the wave test's count
assertion is scoped accordingly. Undo gains its payload schema
(members per revision 1) and `WithTags("json")` alongside it `[plan]`.
Appendix-B: the exemption enumeration test
(internal/gitexec/exemptions_test.go:13, exact-set + count) updates
here and in 4.3 (final count twelve).
**Verify (red to green):** the three undo_effects tests; the no-lock
row and one-ref-update pins stay green.

### 4.2 Unlock and doctor fix
- unlock: Effects-based removal of the computed lock path; refusals
  stay in front. The remover must ERROR on a missing path `[plan]` —
  Effects.Remove is RemoveAll semantics (missing = success), which
  would silently convert reclaimNone into reclaimDone; use a
  remover that os.Remove-errors.
- doctor fix, CORRECTED mechanics (critique #10): orphan tmp dirs —
  internal/index gains a plan API (the GC scanner returns PATHS, not
  just names/counts; GarbageCollect takes the remover or doctor
  iterates the planned paths itself) `[plan]`; legacy queue dir,
  policy file, publication temps — Effects.Remove over
  scanner-computed paths in both modes; stale locks — the remover is
  INJECTED into the reclaim path doctor uses (ReclaimIfStale →
  reclaimLocked's single unlink, reclaim.go:130), while the
  Acquire-path's second reclaimLocked caller (lock.go:226) keeps its
  direct remove `[plan]`; lockScan gains paths. Dry mode never uses
  the reclaim path (scan-based). doctorFix's two-branch shape
  collapses onto plan-then-mint; diagnose stays effect-free. The 3.2
  store-as-stash and 3.7 orphaned-unmerged fix actions land here,
  minted.
- STAY-GREEN constraints listed (Appendix B): the reclaim/naming/index
  unit tests; the stdout pins ("stale lock", "Would back up", "Would
  fast-forward", submodule fix lines) survive beside the new records.
**Verify (red to green):** unlock and doctor-fix mint tests; the 3.2
fix-half pin; the 3.7 repair pin.

### 4.3 Backup
- backup backup: the dry path mints the push argv with a placeholder
  lease via execGitPush under the existing exemption and grant; the
  placeholder KEEPS the `--force-with-lease=` prefix spelling (grant
  selection depends on it) `[plan]`; exposure classification stays off
  the dry path. The execute-path fetch inside backup is NOT minted
  (out of scope, stated).
- backup restore, CORRECTED (critique #11): `fetchSlotObjects`
  (backup.go:52-61, shared with backup backup) is SPLIT — the fetch
  invocation mints via Effects.Run (new effects-handle exemption row);
  the FETCH_HEAD RevParse runs only on the execute path; the dry path
  (which already knows slotSHA from the :373 read) records the fetch
  and the ff-only merge from known data and performs neither; the
  shared caller keeps its old direct behavior `[plan]`.
**Verify (red to green):** both backup mint tests; the would-do stdout
pins stay green.

### 4.4 The parent-bump preview record
The record is minted in maybeAutoBumpParent so its callers inherit —
NINE call sites (commit, amend commit.go:433, reword :503, mv, the
restructured revert, the conclusions, delegation; undo EXCLUDED per
4.1). PREVIEW DECISION PROCEDURE stated `[plan]`: the dry path
performs exactly the reads the dry branch of requireAutoBumpDecision
already performs (parent config) plus CheckNested and the gitlink read
(both reads; the comments at autobump.go:138-141 and :189-191 update
to permit them for the preview); if the gitlink already matches, the
preview records nothing (the already-current case); the recorded
argv's Triggered-by trailer carries previewCommitPlaceholder.
**Verify (red to green):** the parent-bump preview test.

---

## Phase 5 — Intake and mv rulings

After Phase 0; before Phase 6.

### 5.1 mv refuses a missing destination directory `[user]`
checkMvPair (mv.go:320) gains the missing-parent reason (Stat of the
destination's parent; root and absNew in scope), inheriting the
collected refusal (exit 19). `--create-missing-directories` (weakly
held) elects creation; threading `[plan]`: runMv reads the flag via
optBool and passes it into checkMvWorld/checkMvPair and mvFilesystem —
mv-local, no CommitRequest change. When elected, ensureParent's minted
mkdir + rollback run unchanged. Appendix B (critique):
TestMvMovesRecordsAndCommitsInOneInvocation (mv_test.go:57, moves into
absent `sub/`) joins the sanctioned list with
TestMvCreatesTheDestinationDirectory and the two scrub_moves fixtures.
Divergence fate `[plan]`: the entry is DELETED in Phase 7 — after this
change safegit MATCHES git (git mv refuses too), and converged
behavior carries no entry.
**Verify (red to green):** the wave2 mv test. NEW: the flag elects
with the mkdir recorded.

### 5.2 Escaping symlink targets refuse (exit 29) `[user]`
The refusal replaces the notice at its real site — the END of
resolveFiles (intake.go:506; the accumulation happens inside the loop
at :465/:483; "pre-loop" in revision 1 was wrong — the correct
property is pre-CAS-loop, nothing staged, commit and amend both
inherit). Names the literal target; `--allow-escaping-targets` (weakly
held) elects committing and restores the notice line. SCOPE `[plan]`:
the refusal covers ADDING/STAGING escaping link content; `safegit mv`
moving an existing tracked escaping link is untouched (rename-only
commits never restage link content). Registry row 29; the freshness
and completeness tests regenerate. Phase 7 ADDS the guide's symlink
policy text (none exists today — revision 1 cited a nonexistent
passage) and rewrites the divergences entry.
**Verify (red to green):** the wave2 symlink test; its control stays
green.

### 5.3 The mv dirty-content notice `[user Q4 ruling + critique
refinements; omitted from revision 1 — restored]`
mv stays rename-only; a notice names each moved path carrying
uncommitted changes ("remains uncommitted at <new>"; "would remain"
under --dry-run). Dirtiness is filter-aware: compare `git hash-object
--path <newpath>` of disk bytes against the parent blob (attributes
decided by the NEW path; the notice never overclaims "your changes").
Always-present payload array member (empty when clean; joins the
required list). Aggregate form for subtree moves (a count plus a
capped listing). The commit-failure recovery text (mv.go:577-579)
gains the plain-commit alternative once auto-record-moves ships
(cross-reference 6.6).
**Verify:** red-first notice tests (dirty file, autocrlf
false-positive control, dry-run wording, subtree aggregate).

---

## Phase 6 — Auto-record-moves `[user]`

Depends on 0.2, 2 (payload conventions), 3, 5. Spec tests written
red-first inside this phase.

### 6.1 The raw delta
DiffTree moves to raw+z+no-abbrev; ChangedPath gains
SrcMode/DstMode/SrcSHA/DstSHA; the root-commit synthesis branch
(git.go:911-921) fills them from its TreeEntry data. Consumers read
Status/Path only (verified: intake.go:134/148/858) — unaffected.
**Verify:** unit pins per field; existing DiffTree consumers green.

### 6.2 The mint engine
STRUCTURAL FACT stated (critique #25/#27): inference RUNS INSIDE
tryCommit per attempt (declared records stay resolved once pre-loop at
commit.go:302); the fences need TWO per-attempt tree listings — a
paths-by-blob view over the attempt's parent tree and one over the new
tree — built as fresh treeIndex-style instantiations inside tryCommit
(the intake-time instances are pre-loop against the old tip and are
not reachable there).
Pairing and fences, in evaluation order `[plan-stated order]`:
1. SUPPRESSION FIRST: declared records (including subtree prefixes,
   which suppress every path under the prefix `[plan]`) remove their
   named paths from BOTH candidate sets before pairing — a declaration
   is the human resolving the question, so it can un-block an
   otherwise-ambiguous blob (x1 declared to y lets nothing else claim
   x1; x2's fate is then judged on its own).
2. Regular-file modes only (100644/100755; exec-bit change across a
   pair allowed); the empty blob never pairs.
3. One-to-one exactness: a blob with two candidates on either side
   mints nothing for that blob (no tie-breaks).
4. Parent-tree uniqueness fence (deletion's blob at exactly one parent
   path) and new-tree uniqueness (addition's blob at no other path in
   the commit's own tree).
Zero-candidate fast path: the raw delta's deleted/added SHA sets
intersect empty → no listings, no cost.
Shared-index requests skip inference entirely (IndexBase predicate);
amend has no IndexBase and gets 6.4's arm.
Ambiguity/cap refusals: one aggregate stderr line (intake-notice
channel — unconditional, once per operation) suggesting declaration;
the refused pairs ride the payload.
THE CAS RULE `[user]`, implementable form `[plan, corrected per
critique #25]`: attempt 1's inferred record set is RETAINED AS DATA in
the once-per-operation state (the nativeHooks pattern) — never
re-parsed from the cached message text (a rewriting hook may have
stripped records; text comparison would false-abort). Each retry
recomputes; a differing set aborts the operation with the
transient-race error advising re-run. Identical set → proceed with the
cached message.
**Verify:** the fence/pairing spec tests (each fence red-first); the
CAS mismatch abort (two sessions, differing attempt-2 delta); the
shared-index skips.

### 6.3 Witnessed subtree collapse and the cap `[user]`
As revision 1 — the predicate's inputs come from the delta plus the
6.2 per-attempt parent listing (no extra calls); the cap is a package
const in internal/commit, value 20 (weakly held).
**Verify:** uniform-move single-record; scattered cap refusal; partial
moves fall through to per-file.

### 6.4 Origin token, supersede, amend, revert
- Token `observed` (declared/derived stay reserved); absence =
  declared; Record gains Origin; encoder emits the token for observed
  only; ParseRecord accepts it in the reserved slot (0.2's
  keyword-before-a-pair subtests rewritten here, sanctioned);
  RewriteMessage round-trips it (pinned — else every scrub strips it).
- SUPERSEDE, corrected scope `[plan per critique #26]`: same-commit
  amend ONLY (validateMoved makes later-commit re-declaration of an
  earlier commit's pair structurally impossible — the old path is no
  longer tracked; stated in code). Mechanism: refuseRedeclaredPairs
  (already a ReadMoves parser over the replaced message) becomes
  origin-aware — declared-vs-declared still refuses naming the id;
  declared-vs-OBSERVED emits a synthesized retraction of the observed
  id PLUS the new declared record in one commit. The retraction is
  INTERNAL SYNTHESIS — it does NOT route through resolveMovedRetract
  (whose reachable-history base cannot see the replaced tip's own
  records and whose same-breath doctrine forbids it); that doctrine
  comment (moved.go:131-135) is updated to name the supersede
  exception.
- Amend: preservation verbatim for all records; inference ADDITIVE
  against the authoring event's own delta (new tree vs the replaced
  tip's first parent — a second per-attempt diff on the amend path);
  reword mints nothing and preserves everything.
- Revert inverses: EVERY auto-minted inverse carries `observed`
  `[user]` — conclusionMovedRecords sets it unconditionally.
**Verify:** origin round-trip through scrub match; supersede
(retract+redeclare emitted; declared-vs-declared refuses; the
resolver-bypass pinned); amend additive; reword preservation;
observed-inverse; retractability of an observed record.

### 6.5 Payload and notices
The commit payload's move members (minted records both origins,
refused pairs, cap fact) — one lockstep commit (struct, schema,
required list, CommitResult AND AmendResult, emission sites),
closing the declared-records payload gap.
**Verify:** payload spec tests; schema regeneration.

### 6.6 Contract reversals
ENUMERATED (critique #30 — replacing revision 1's "four blocks"):
main.go:307-308 and the --moved help :311;
internal/commit/commit.go:113-117; internal/commit/moved.go:14-31 and
:325-337; internal/trailer/moved.go:8-15, :46, :225;
internal/trailer/project.go:29-32 ("nothing is stored about
confidence" — falsified by Origin); internal/git/git.go:901-904
(qualified, not deleted); docs/architecture.md:234;
docs/commands-guide.md:141; docs/_CLAUDE.md:84 (template);
moves_test.go:12-21 header; moves_cross_session_test.go:20-29 header;
commit_staged_deletion_dir_test.go:109/:130;
moves_declared_test.go:14-15. `assertNoRenameNotice` (moves_test.go:25
— verified a no-op; its strings match nothing) is DELETED and the two
consuming tests assert the real properties directly (no content
adoption; no auto-staging; the mint notice's actual wording asserted
where relevant) `[plan]`. The divergences moves-entry rewrite happens
in Phase 7 with the rest of the catalog (single-writer rule below);
this subphase marks the entry stale in a code comment only. The
consumer-repo commit-msg-hook consequence documented in the guide.
**Verify:** grep shows no declared-only doctrine claim survives
outside the catalog; the re-pointed tests pass.

---

## Phase 7 — Documentation

After Phases 1-6. SINGLE-WRITER RULE `[plan]`: ALL divergences-catalog
edits (entries and the preamble roster) happen HERE, in one pass —
earlier phases only flag entries in code comments. Catalog work:
- Rewrites: escaping-symlink (5.2's refusal + flag);
  non-executable-hooks (1.4 refuse-both); the revert entry ("split at
  git's own seam" -> the single-form ruling, 2.3); the moves entry
  (6.x); resolution-keywords flips provisional->deliberate and its
  text notes the extended overwrite protection AND the
  emitted-content-unavailable skip (3.6); the autostash-exit entry
  (which IS the aftercare entry — one entry, divergences.md:424)
  generalizes to the family code.
- Deletions: the timeout-override entry (mechanism deleted, 1.4); the
  mv-missing-directory entry (behavior now MATCHES git, 5.1 — the
  matches-git-means-no-entry rule).
- The two entries 1.1's derivation falsifies (the dirty-tree scope
  entry's reset/bisect sentences; the reset-hard-only entry) update.
- Fate rule `[plan]`: rewritten entries carry deliberate status (they
  are rulings); the provisional roster shrinks accordingly and its
  hard-coded counts ("Two"/"Four", plus the ten-rows exemption
  sentence and the 1800 prose) are replaced with counts-free wording.
Other docs:
- The machine-contract boundary paragraph into
  integration-guide.md's machine-mode section (after the
  parse-the-whole-stream sentence): the three failure shapes
  truthfully (payload-carrying nonzero; envelope with null payload;
  no envelope on paths that die before dispatch — a set 3.1 shrinks),
  payload-carries-aftercare, the 1.3 UTF-8 limit. The sentence
  integration-guide.md:276 ("does not write a JSON error object …")
  is REWRITTEN — 3.1's payload-carrying error envelopes falsify it as
  absolute (critique #36).
- Every hand-written doc updated for phases 0-6 (the per-phase lists
  above); the guide GAINS symlink-policy text (none exists); the
  hook-row updates per 1.4's corrected list; the app_version example
  becomes a placeholder. The dry-run network item from revision 1 is
  DROPPED — commands-guide.md:45 is already correct (critique #37);
  the stale await-todo's claim is noted for Phase 10's triage.
- `--dump-schema` (commit via rlsbl commit) and bare `selfdoc gen`.
- STRUCK (with the log corrected): the CLAUDE.md regression item —
  template and generated file verified byte-consistent.
**Verify:** every named row resolved; regeneration clean; fresh
spot-checks against code.

## Phase 8 — Changelog

The three edits BY ID (inlined; the entry-N numbering was
line-position shorthand): entry id
`18ce4135dd10331c7652c324065642ac969749e1fd24c32d` (the --moved
entry): description's `old:new` becomes the arrow form — AND the same
false text duplicated in its batch-exclusion `reason`
(.rlsbl/config.json:55) is edited too (text edit + rlsbl commit)
`[plan]`; ids `18ce4138de0d43002a1807d9ce8a4ac097d7c9ecfd49fc31`
(hook-store relocation) and
`18ce4143453d895ecd806e9fb0234d759230118bb4e7a7aa` (force-push
consent) retype to breaking. The dry-run-uninstall fix: one
`changelog add --type fix` CO-LISTING commit 819cae33 (the fix lives
inside the repository-wide-uninstall commit; dual-entry is legal and
already precedented), plus one edit dropping the tacked-on clause from
breaking entry id `18ce413b4e9236f3bcd5994bf6bb4dd2872e676e90d1ee92`.
Coverage: every campaign-2 commit (63 uncovered at critique time,
growing) — feature-area user-facing entries for the new behaviors and
no-user-facing clusters; `--allow-batch` with reasons where a
legitimate area exceeds the limit; the enforced format_version rides
automatically. Phase 9's own commits are covered by a FINAL top-up
pass at the end of Phase 9 `[plan — resolves the 8-vs-9 ordering
defect]`.
**Verify:** `rlsbl check --tag changelog` fully green (re-verified
after Phase 9's top-up).

## Phase 9 — Verification

- Full `-race` green; the stress run; GOWORK=off; gofmt; vet.
- Exit-sites census regenerated and committed (pre-empting the
  release hook), with its changelog line in the Phase-9 top-up.
- RECONCILIATION PROCEDURE stated `[plan]` (the script's --check is a
  strict diff and cannot serve): generate a fresh snapshot, diff
  test-lines against testdata/campaign2-baseline.txt, and CLASSIFY
  every differing line — healed red (cite the subphase), sanctioned
  rewrite (cite Appendix B), or new test — with ZERO unexplained
  lines; confirm the campaign-1 frozen artifact's reconciliation still
  holds the same way.
- One fresh audit per phase (0-8), briefed with this plan, the log,
  and the phase's red list; remediation; then the Phase-8 top-up
  re-check.

## Phase 10 — Release

- Triage (COUNTS CORRECTED): twelve top-level todo files today. FOUR
  move to .done: this plan, the campaign-1 plan, the execution log,
  and move-records-for-undeclared-moves.md (consumed). EIGHT stay:
  the FOUR await files (conditional-consequential, dry-run-network —
  whose now-stale doc claim the triage notes — exit-codes-registry,
  effects-handle-closed-method-set), the THREE contingent files
  (reader-writer-operation-lock, scrub-strict-mode-selector,
  push-streaming-restoration), and reversibility-gaps (active).
- `rlsbl release init`; the release file (minor bump; two-campaign
  description; context block); commit it; the single
  `rlsbl release run --no-allow-dirty --watch --approve-consequential`.
- Post-release notes (outside the release): the six fleet repos with
  legacy placeholder hooks need `hook migrate` or deletion; two
  dependent projects' parked todos unblock on mv shipping (named in
  the log, deliberately not here); reset/rebase recovery's data
  precondition is met.

---

## Dependency spine (corrected)

| Phase | Depends on | Notes |
|---|---|---|
| 0 | — | 0.2 -> 6; 0.4 -> 3.2 |
| 1 | 0 | 1.1+1.2+1.3 ONE implementor (coord_cmd.go); 1.4+1.5 share doctor.go |
| 2 | 1.1, 1.3 | 2.3 -> 3.1 (revert schema) |
| 3 | 0.4, 1.2, 2.3 | 3.2/3.7 fix ACTIONS ship in 4.2 |
| 4 | 1.5, 3.1 (payload), 3.2/3.7 (fix actions) | |
| 5 | 0 | before 6 |
| 6 | 0.2, 2, 3, 5 | |
| 7 | 1-6 | sole divergences writer |
| 8 | 7 | Phase 9 runs the final top-up |
| 9 | 8 | |
| 10 | 9 | |

Sequential execution is the default throughout.

## Appendix A — red map (verified exact; 30 red + 1 green control)

| Red tests (file) | Subphase |
|---|---|
| grammar_reserved_keywords (2) | 0.2 (subtests rewritten 6.4) |
| wave2_passthrough_oplog_positions (2) | 1.2 |
| machine_contract_json_document (2) | 1.3 |
| wave2_hook_timeout_override_dead (2) | 1.4 |
| wave2_hook_nonexec_local_refusal (1) | 1.4 |
| sequencer_conclusion_envelope (2) | 3.1 |
| sequencer_stale_autostash (2) | 3.2 (fix-half pin 4.2) |
| sequencer_conclusion_crash_window (1) | 3.3 |
| sequencer_delegation_delete (1) | 3.4 (adjusted with 3.6) |
| sequencer_conclusion_deletion_report (2) | 3.5 |
| sequencer_conclusion_overwrite (1) | 3.6 |
| commit_unmerged_index (1) | 3.7 |
| undo_autobump_precheck (1) | 3.8 |
| machine_contract_undo_effects (3) | 4.1 |
| machine_contract_invisible_mutations (5) | 4.2-4.4 |
| wave2_mv_missing_destdir (1) | 5.1 |
| wave2_symlink_escape_refusal (1 + green control) | 5.2 |

Reds written inside their subphases: 0.3, 0.4, 1.1, 1.2 (three new),
1.5, 2.3 (each retrofit), 3.1 (per-shape), 3.4 (fresh ordering pin),
3.6 (variants), 3.7 (repair), 5.1 (flag), 5.3, all of Phase 6.

## Appendix B — sanctioned rewrites (a break not listed here is a
plan defect)

- 1.1: none beyond stub hygiene (TestFlagConditionalEffects must STAY
  green — the reset row keeps --soft/--mixed narrow).
- 1.2: TestBackupWritesOplogEntries (only if the tip-spelling
  consolidation proceeds); the TipSHA search tests.
- 1.4: TestSkipNonExecutable; TestSetOutputCapturesDiscoverWarning;
  TestHookListRendersStateAndOrigin's hook-run tail;
  TestDoctorExitCodeFollowsErrorFindings's warn fixture;
  TestParseTimeoutOverride (deleted).
- 2.3 (revert single-form): newQueuedPickRepo("revert") fixture
  (sequencer_delegation_test.go:88 — switches to raw git) and its two
  tests; TestRevertFormsThatStayPassthroughs
  (revert_restructure_test.go:164-197);
  TestMultiCommitRevertStaysASequencerPassthrough (:134);
  sequencer_conflict_test.go:288-295; sequencer_preview_test.go:528-553
  (multi-commit range preview);
  TestPickAndRevertContinuePassthroughsNameSafegitsCommand/revert;
  the 7.4-era multi-commit-no-records test (becomes a refusal pin).
  scrub_remap_test.go:403 STAYS GREEN (the remap bullet was deleted).
- 2.3 (--hunks): pins of the 3way retry if any exist at the
  commit_hunks seams (verify; the stage package has none).
- 3.2: no rewrites (the fallback was deleted; the probe confirmed the
  key).
- 3.4/3.6: the delegation-delete red test's fixture adjusted so its
  content passes the overwrite check (else vacuous).
- 3.x/4.x: exit_table_test regenerations;
  TestDirPinExemptionTableIsEnumerated (rows added in 4.1 and 4.3;
  final count twelve).
- 4.2: internal/lock/reclaim_test.go:58/85/109,
  internal/lock/naming_test.go:95/107,
  internal/index/index_test.go:77/112 (API reshapes); the stdout pins
  must stay green (concurrent_test.go:804, submodule_test.go:969-974,
  backup_test.go:385-390/:410).
- 5.1: TestMvCreatesTheDestinationDirectory;
  TestMvMovesRecordsAndCommitsInOneInvocation (mv_test.go:57); the
  two scrub_moves fixtures moving into nonexistent directories.
- 5.2: TestCommitSymlinkEscapingTargetIsCommittedWithANotice.
- 6.4: grammar_reserved_keywords' keyword-before-a-pair subtests
  (observed becomes valid).
- 6.6: assertNoRenameNotice deleted; moves_test/moves_cross_session
  headers re-pointed.
