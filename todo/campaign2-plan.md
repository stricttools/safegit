# Campaign 2: implementation plan (revision 4)

Self-contained: every decision this campaign executes is stated in full in
THIS file. A session with zero conversation context implements any subphase
from this file plus the cited code, and releases at the end. There is no
companion record: the execution-log convention is abandoned and the log
deleted; where older artifacts (git history, superseded revisions) disagree
with this file, this file wins. Code anchors were verified against the tree
at `c3c05c3` and re-verified in sample by an adversarial critique at
`ed355dd` (whose probe results are inlined where they bind); treat each
claim's TEXT as the anchor and re-locate by searching for it — expect drift
in line numbers, never in the claim.

**EXECUTION IS ON HOLD.** The user's standing order: implementation starts
only on a further explicit go. This file existing is not that go.

## The subset law (standing design law)

safegit does not promise full git support and never will. It deliberately
implements a small, opinionated subset of git's functionality, chosen for
agent-heavy workflows. When a git feature, command, flag, or edge case is
judged actively harmful or irrelevant for that workflow, safegit
deliberately omits it and never looks back — no compatibility pressure, no
"but git supports it" argument. Every such omission is recorded in
`docs/divergences.md`, unapologetically. safegit is for our agents, not for
all humans. (Already adopted into the CLAUDE.md and README templates and
the generated root files; this campaign applies it throughout.)

## Single authorship (the campaign's structural centerpiece) `[user]`

Every commit safegit creates is authored by its own pipeline — trailered,
undoable, commit-msg-hook covered, oplog-recorded. The former second
authorship class (git authoring commits behind safegit command names) is
DELETED, not managed: no announcements, no mode table, no delegation. The
one deliberate exception is `safegit rebase`, which remains a guarded
passthrough where git performs the replay and authors the replayed commits
— a UNIFORM behavior (every rebase commit is git's, always), not a run-time
split, recorded as one divergences entry. The native non-interactive rebase
reimplementation is deferred to `todo/pipeline-authored-rebase.md`.

Consequences, all ruled:
- Clean merges, clean cherry-picks, and pulls become pipeline-authored
  (Phase 2).
- Multi-commit cherry-pick argv is refused naming sequential single
  invocations — the same ruling revert already carries. Range/rev-set
  spellings (`A..B`, `A...B`, `^rev` and kin) are refused too: probed,
  `git cherry-pick A..B` creates queue state even when the range holds
  one commit, and it is one argv token — argument validation must refuse
  rev-set OPERATORS, not count tokens.
- Sequencer queues created by RAW git are refused by the continue commands
  ("safegit did not start this; finish or abort with git"), as are raw-git
  MERGE SHAPES safegit can no longer start (octopus; no-AUTO_MERGE content
  conflicts) `[user]`; the delegation machinery is deleted wholesale.
- Branch navigation is `safegit switch` (branch names only); `checkout` is
  not a safegit command and file restoration is deliberately absent
  (2.6) `[user]`.
- Enforcement is structural: a boundary guard refuses commit-creating git
  argv outside the pipeline's compute doors and the declared rebase door
  (Phase 2.1). No mode table, no announcement generator, no generated mode
  docs are built. The one surviving run-time split — `--amend` resolving to
  amend vs reword, identical in authorship and safety — is carried as a
  payload member only, with NO stderr announcement `[user]`.

## Standing orders and operating facts

- **Tracked-hooks stance is FINAL** (the committed `.safegit/hooks` store
  executes directly; push-intent is the trust boundary; the blunt
  clone-then-push sentence stays in the docs). The user has ruled this
  repeatedly; NO critique, audit, or review may re-open it without NEW
  FACTS (an actual exploit, not a re-argument). Never ask again.
- **Nothing pushes before the single release.** The spec suite is
  deliberately red until the campaign turns it green; CI on main is not
  exercised meanwhile. No session may "fix" the spec tests.
- **Implementor discipline:** commits via the INSTALLED safegit (single
  `-m`, plain paths, repo root — the installed binary predates the
  campaigns and its known-buggy paths are avoided that way); scratch repos
  only via `t.TempDir` (the git-execution boundary guard scans gitignored
  files, so an in-repo scratch repo trips it); batch edits per the global
  batch-operation rule (dry-run capable, dry run first, output examined,
  then execute; occurrence-count check plus full diff review); red-first
  for behavior changes; one fresh audit per phase, never per subphase; `jq`
  is required by `scripts/test-baseline`; `--dump-schema` WRITES the
  tracked `.strictcli/schema.json` as a side effect; `internal/test`'s
  TestMain compiles the LIVE tree, so mid-wave test results are advisory
  and authoritative verification runs on a quiescent tree; the repo's
  gitignored `go.work` overlays a local strictcli checkout, so the
  verification phase includes a `GOWORK=off` run.
- **Vocabulary:** safegit moves files; say "move", not "rename". "Rename"
  survives only where quoting git's own terms (rename detection, `diff
  -M`). Phase 7 sweeps existing docs and help text accordingly.
- **The dry-run doctrine, stated generally** (the narrow "mint last" form
  caused a real omission and is retired): on a dry-run path, perform all
  state READS first, then record the would-do mutations IN THE ORDER THE
  EXECUTE PATH PERFORMS THEM (the recorder convention scrub_preview.go's
  comment states); no state read may follow the first recorded mutation
  (recorded-not-performed mutations make later reads stale). Values only a
  real run can know are placeholders — the established
  `previewCommitPlaceholder` / placeholder-lease convention. Nobody runs a
  dry run to obtain real hashes.
- **No drift-prone details:** this plan and every document it touches
  follow the global rule — no prose counts or hand-typed totals; lists and
  tables ARE the enumeration; needed numbers are generated and
  freshness-tested.

**Decision-origin marks.** `[user]` = decided by the user
(recommended-option picks are weakly held and freely reversible by the
user; that qualifier applies to every `[user]` mark unless noted
deliberate). `[plan]` = orchestrator resolution of a discovered gap,
reversible on request. `[probe]` = contingent on a recorded-fact probe.
The subset law, the single-authorship restructure, the tracked-hooks
stance, and the release postponement are deliberate.

**Exit-code assignments `[plan]`** (free slots verified; uniqueness and
ascending order enforced by tests): 26 = the commit-stands family code;
27 = the conclusion overwrite refusal; 28 = the unmerged-index refusal;
29 = the escaping-symlink refusal. Exit 25's constant is RENAMED to match
its widened meaning (`HookNotExecutable` family spelling; the old
`TrackedHookNotExecutable` name would lie at every call site).

**Non-goals:** reset/rebase recovery (todo stays active; 1.2 ships its
data half) `[user]`; per-command bypass surfacing (deferred) `[user]`;
live streaming under machine mode for guarded commands (framework tee
absent; no workaround per the push-streaming todo); the move-record
reader command (NOT planned anywhere `[user]` — the framework-repo filing
was deleted on order, archived); the scrub backward path-projection
feature and, with it, the ruled projection-visible-before-consent
requirement `[user]` — that ruling binds the feature whenever it is
built, and it is not in this campaign; native pipeline-authored rebase
(`todo/pipeline-authored-rebase.md`, after this campaign) `[user]`;
minting backup's execute-path fetch, and the network read inside
`backup restore`'s preview (its slot lookup is an `ls-remote`) — both
await the framework's network-effects ruling; the await todo tracks
them `[user]`; the ruling-await todos and the contingent todos
(enumerated in Phase 10's triage list).

---

## Phase 0 — Groundwork

Independent subphases; sequential or one implementor.

### 0.1 Campaign-2 baseline artifact
`scripts/test-baseline testdata/campaign2-baseline.txt`, commit it. The
frozen campaign-1 artifact stays untouched. (The script passes `-short`
and `-count=1` and refuses baselines containing build failures; the
artifact is Phase 9's reconciliation anchor.) The script gains an
explicit `jq` presence check with a named error (today a jq-less machine
fails with a confusing pipeline error) `[plan]`.
**Verify:** committed; `--check` clean; header records HEAD and argv;
the jq check refuses cleanly when jq is absent from PATH.

### 0.2 Grammar reservation
Turn green `internal/trailer/grammar_reserved_keywords_test.go` (both
tests red today): `needsQuoting` (internal/trailer/cquote.go:53) gains
exact-match membership for `observed`/`declared`/`derived`; `ParseRecord`
(internal/trailer/moved.go:208) refuses a bare reserved keyword in the
post-id slot (the refusal lands in Malformed via ReadMoves' existing
verbatim capture — no new plumbing). Sanctioned-later note stated in the
test: 6.4 makes `observed` valid in that slot and rewrites the
keyword-before-a-pair subtests then.
**Verify (red to green):** both tests.

### 0.3 Implicit-fallback family deletion
Delete the dead `<=0` config fallbacks — all verified existing, no
others in the config family: push.go:293-296, push.go:328-330,
hook.go:132-135, internal/commit/commit.go:327-330 and :573-576,
internal/commit/amend.go:143-146, :297-300, :474-477, :559-562. They are
dead because every `*repo.Config` in production passes `Validate`
(repo.go:409-422, covering all four integer keys), which hard-errors on a
non-positive value, and `SetConfigValue` refuses writing one — so the
fallbacks can never fire and only obscure the real authority. Call sites
read the value directly; no substitution survives `[plan]`. Add one
loader-refuses-non-positive pin per key (none exists). An
attribute-derived sibling of the same dead shape exists at
internal/conflict/conflict.go:332-334 (its only writer already
guarantees a positive value) — outside this family's config scope; MAY
be cleaned in the same pass, not required.
**Verify:** pins green; the pattern greps to zero outside Validate.

### 0.4 Recorded-fact probe: autostash message shape
The existing probes (internal/git/autostash_probe_test.go) never inspect
the stash commit's MESSAGE. Add the probe pinning: a genuine
`merge --autostash` stash's message is `On <branch>: autostash`; a bare
`git stash create` yields `WIP on <branch>: ...`. (Reproduced live on
git 2.54 and re-reproduced on 2.55 — the expectation is confirmed; the
probe records it permanently.) 3.2 keys on this `[probe]`.
**Verify:** probe committed and green.

---

## Phase 1 — Foundations and deletions

Depends on Phase 0. FILE-SHARING: 1.1, 1.2 AND 1.3 all edit coord_cmd.go
(the reset scan and bisect list share the file with the oplog seams and
passthroughStdout; 1.2's seams sit inside handler bodies 1.1 edits) — run
1.1-1.3 under ONE implementor. 1.4 and 1.5 share doctor.go — one
implementor or sequential. Phase 2 later REPLACES the merge and pull
handlers this phase touches (and renames checkout's); the double-touch is
accepted (1.2/1.3's changes there are one small shared branch each) and
the restructure inherits their conventions as stay-green constraints.

### 1.1 Derived worktree guards `[user]`
- New derived view in internal/gitexec/classify.go over `EffectsOf`'s
  `MutatesWorktree` bit; default-deny on unclassified argv (the
  `WritesObjects` precedent, classify.go:429).
- reset: delete the literal scan (coord_cmd.go:349-356; its comment's
  only-`--hard`-mutates claim is false). The reset row EXTENDS its
  conditionals to `--merge` and `--keep` (worktree-writing) while
  `--soft`/`--mixed` stay refs|index — `--mixed` needs no conditional
  entry (it is reset's base classification); the green pin
  `TestFlagConditionalEffects` (internal/gitexec/classify_test.go:62)
  pins `--soft` (refs|index) and `--hard` and MUST STAY GREEN; do not
  make the row unconditional. `TestTableIsWellFormed`
  (classify_test.go:190) refuses empty conditional stubs — no leftovers.
- bisect: delete the hand-kept name list (coord_cmd.go:398-404). The
  bisect row (classify.go:114-118, currently unconditional-worktree with
  no conditionals — it already disagrees with the deleted list) gains
  conditionals so the stepping vocabulary
  (good/bad/old/new/skip/run/replay/reset/start) derives the guard and
  non-stepping forms (terms, log, view) do not `[user, options
  reviewed]`. The `bisect run` consequence (non-gitignored build
  artifacts dirty the tree between invocations) is documented in the
  guide; ignored files never count.
- The marker-exemption silent skip (sequencer_markers.go:136) becomes a
  carried declined-check. STATED MECHANISM: `verifyMarkers` returns a
  declined list alongside its int (signature change; TWO production
  callers — sequencer_continue.go:450 AND revert_cmd.go:235, the
  restructured single revert). The list rides `conclusionResult`, the
  human report renders it, and the conclusion payload schemas gain a
  `declined_checks` member (schema builder edit + required list) —
  coordinate with 3.1's payload work (same builder, one pass). The
  revert caller renders the declined list in TEXT ONLY at this subphase
  (revert has no payload schema until 2.4 adds one; the payload member
  joins there).
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
(internal/commit/commit.go:649-658). Seam facts:
- Pre-op tip exists only in checkout today (coord_cmd.go:158); merge
  records the post-op tip under `result`; rebase/reset/bisect/pull
  record neither; resolve HEAD before each operation.
- The navigation entry (checkout today; `switch` after 2.6's rename):
  its `ref` key currently holds the OPERATOR'S ARGUMENT
  (coord_cmd.go:168-175 — for `-b` forms that is the literal flag
  string) — fix to the resolved full ref name. For branch creation
  (`-c` after the rename), the pre-op tip is the zero SHA (the ref did
  not exist) `[plan]`.
- NAVIGATION-ENTRY VISIBILITY `[plan]`: a successful branch switch
  moves HEAD, not the branch ref — but an entry carrying ref + tips in
  commit-entry spelling becomes visible to the fail-closed TipSHA
  readers (`oplog.TipSHA`, oplog.go:184-191, feeding `LastRefUpdate`,
  doctor's bypass_detect, undo's per-ref filter), where it would RESET
  the bypass-detection baseline and mask earlier out-of-band commits.
  Navigation entries therefore record their tips under a spelling the
  TipSHA readers do not consume (e.g. `observed_tip`), and this
  subphase AUDITS the three consumers against the new entry shapes.
- Bisect records the NAVIGATION spelling too `[plan — ratified during
  execution]`: no bisect subcommand ever moves a branch ref (`start`
  detaches HEAD; `reset` restores HEAD), so the commit-entry spelling
  would hand the TipSHA readers a detached-HEAD tip and recreate the
  doctor false-positive class this subphase removes. The enumeration
  above listing bisect among the baseline recorders is corrected by
  this rule, not the other way around.
- Failure entries: the return-before-append sites (checkout :160-162,
  pull :211-213 and :227-229, merge :277-280, rebase :316-318, reset
  :364-366, bisect :412-414) now append entries with ref + old tip +
  EMPTY new tip + a failed-outcome marker; the runGuardedPassthrough
  unconditional append (:469-474) gains the outcome field and moves
  behind the exit code. An empty new tip keeps the entry invisible to
  `LastRefUpdate` and doctor's bypass check — the verified-safe shape
  for the fail-closed readers.
- The `--dry-run`-after-a-passthrough-name defect (the flag forwards to
  git, exits loudly, but was oplog-logged as an attempt) is cured on the
  logging half by outcome recording; the forwarding itself is git-argv
  semantics, documented in Phase 7.
- STAY-GREEN constraints: `TestBackupWritesOplogEntries`
  (internal/test/backup_test.go:419) hard-pins backup's existing extra
  keys — leave backup.go's entries alone or update that pin as a listed
  sanctioned rewrite; the bypass_detect consumer chain (doctor.go:413,
  undo.go:176/181/445, stress_test.go:184-188) must keep passing. The
  TipSHA multi-spelling search (oplog.go:184-191) may consolidate ONLY
  together with the backup pin rewrite; both `LastRefUpdate` tests are
  sha-only and unaffected.
- Phase 2's restructured merge/pull implement the SAME entry spelling
  natively, under the SAME op names (see 2.2/2.5 — the wave2 pins
  additionally require exactly-one entry with the COMMAND's op name);
  the wave2 oplog pins must stay green across the restructure.
**Verify (red to green):** the two wave2 oplog tests. NEW red-first: a
failed merge writes a failed-outcome entry; doctor's bypass check does
not misfire after a clean safegit merge; the root-undo false doctor
error is gone; bypass detection still flags an out-of-band commit made
BEFORE a branch switch (the visibility rule above).

### 1.3 One JSON document on the guarded commands `[user; mechanism plan]`
Under `--json`, runGitMutation captures (drop `Stream(true)` when
flags.json) and re-emits child stdout through `passthroughStdout`
(coord_cmd.go:496; stderr to stderr); human mode keeps `Stream(true)`
and live output. `Check(false)`+`ExitCode()` verified identical in
capture mode. Known limit, documented in Phase 7: non-UTF-8 child
output under --json fails the capture decode and degrades the exit to
General. (Phase 2 later replaces the merge and pull handlers with
structured commands and renames checkout's; the capture branch is one
shared spot in runGitMutation, so the fix costs nothing extra on the
rows the restructure retires or renames.)
**Verify (red to green):** both machine_contract_json_document tests
(the leaking rows; the checkout row is a green forward-guard, renamed
with 2.6's switch rename per Appendix B).

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
  RENAMED (header assignment); hookDiscoveryExit maps it. EXPLICITLY:
  `hook run` currently exits 0 "no hooks to run" when discovery skips
  the file — it must refuse identically. `hook list` keeps listing (the
  diagnostic). doctor's hook_perms local branch (doctor.go:475)
  escalates to error severity;
  `TestDoctorExitCodeFollowsErrorFindings`'s warn fixture is replaced
  (sanctioned).
- Doc rows THIS subphase falsifies (Phase 7 executes; listed here for
  the audit): commands-guide.md:1546 ("skipped with a warning" — note
  it spells `.git/safegit/hooks`, NOT the tracked store),
  integration-guide.md:111, architecture.md:333 and :404 (the per-hook
  timeout paragraph deletes with its mechanism),
  commands-guide.md:584; the divergences timeout entry deletes; the
  non-executable-hooks entry rewrites.
**Verify (red to green):** the three wave2 hook tests. Sanctioned
rewrites per Appendix B.

### 1.5 Consent uniformity `[user — ratified]`
A dry run never prompts for consent, uniformly. One early dry-run
return in `confirmDeliberate` (main.go:990-1008 — verified: no dryRun
consultation exists today; `--json --dry-run doctor --action uninstall`
exits nonzero today and NO test pins that). push's `&& !flags.dryRun`
(push.go:179) becomes redundant and goes; backup's dry return is
untouched and the rule is NOT license to run backup's exposure
classification in a preview. Write the pins the ruling never had
(dry-run uninstall without consent succeeds and enumerates; json+dry-run
emits the envelope).
**Verify:** new pins green; the consent suite stays green.

---

## Phase 2 — Single authorship and the subset boundary `[user]`

Depends on 1.1 (guard column), 1.2 (entry spelling), 1.3.

### 2.1 The boundary guard
EXECUTION ORDER `[plan — sequencing correction]`: this subphase is
implemented AFTER 2.2-2.5 and 2.7. Until the restructure lands, the
production tree still constructs authoring argv (the passthrough merge
and cherry-pick, the delegation machinery), so enforcing the runtime
guard first would refuse commands the plan has not yet restructured.
The guard turns on once the tree is clean of undeclared authoring argv;
its numbering stays 2.1 because it is the phase's structural statement,
not its first task.

Enforcement of single authorship lives at the git-execution boundary,
which every git subprocess already passes through. The mechanism is a
PAIR `[plan — the campaign-1 AST guard alone cannot see runtime argv]`:
- STATIC HALF: the campaign-1 exec-boundary AST guard pattern
  (internal/gitexec/boundary_guard_test.go's shape) extended with a rule
  refusing LITERAL authoring verbs (`"commit"`, and
  `"merge"`/`"cherry-pick"`/`"revert"` literals not accompanied by a
  literal `--no-commit` in the same argv construction) outside the
  declared doors. Scope rule as in campaign 1: `_test.go` files and
  `internal/testutil` are exempt wholesale; production packages are not.
- RUNTIME HALF: an authors-commit check at the existing `Validate`
  choke point (every constructed argv passes it): argv whose verb+flags
  shape would let git create a commit (`commit`;
  `merge`/`cherry-pick`/`revert` lacking `--no-commit`) is refused
  unless the call site carries a DECLARED DOOR (an ExemptionID-style
  parameter; the only door is the rebase passthrough — `am` needs no
  row, the table already default-denies it). Table mechanics: the
  classification table's `ConditionalEffect` vocabulary is additive and
  presence-only, and its `Base` is deliberately the wider
  classification — the wrong shape for an authoring view — so the
  authors-commit fact is a SEPARATE field (an `Authors` marker with
  `SuppressedBy` tokens, e.g. `--no-commit`), leaving
  `ObservePrefixes`' reasoning about conditionals intact `[plan]`.
- The invariant this makes structural: "git never authors a commit
  through safegit, except `safegit rebase`" — enforced at the choke
  point, not maintained in a table. `[user — boundary guard chosen over
  a mode table]`
- The amend/reword payload member: the commit payload gains an
  `execution_mode` member. The schema is closed
  (`additionalProperties:false`, all members required, shared by
  commit/amend/reword — internal/commit/commit.go:83-97), so the member
  follows the existing nullable convention: present on every form, the
  string `amend`/`reword` on the amend path, null on plain commit
  `[plan]`. Payload only, no stderr line `[user]`.
**Verify:** static-guard test red-first against a planted literal
violation; runtime refusal red-first against a planted no-door argv;
the declared door enumerated in the test; the execution_mode member
asserted in the payload tests (null and non-null forms).

### 2.2 Merge restructure
`safegit merge <branch>` becomes pipeline-authored:
- Exactly ONE branch argument (an octopus merge — merging several
  branches in one commit — is refused under the subset law; divergences
  entry). Strategy selection (`-s`) and strategy options (`-X`) are
  refused (the conclusion and overwrite machinery cannot see what
  non-default strategies write; refusing keeps the protection total).
  `--squash` and `--edit` are refused (allowlist, 2.6). `--autostash`
  is REFUSED `[user]`: it is a dead flag through safegit — the
  coordination check refuses any dirty tree BEFORE git runs (a clean
  tree has nothing to stash; a dirty tree never reaches git), and in a
  multi-agent repo the uncommitted changes may be another session's
  work; the refusal says "commit your changes first". Divergences
  entry. (The campaign-1 machinery that handles autostash state RAW git
  created stays — 3.2 covers it.) Operator-level `--no-commit` is
  ALLOWED `[plan]`: it computes and PARKS (even when clean — the
  operator inspects, then concludes with merge-continue); it never
  auto-concludes.
- Fast-forward handling `[plan]`: safegit decides ff-ness itself
  (merge-base ancestry check). When the merge is a fast-forward and
  `--no-ff` was not given: CAS ref move to the incoming tip, THEN
  `git.SyncMainIndexWithWorktree` (internal/git/git.go:442-453 —
  `read-tree --reset -u` with the gitignore protection; the same
  primitive scrub's post-rewrite sync uses, including its
  foreign-staged-state handling). A bare ref move alone leaves the
  INVERSE of the incoming diff staged (probed) — the sync is not
  optional. The oplog entry carries Op `merge` with a fast-forward
  outcome; `safegit undo` REFUSES it (the tip is a commit safegit did
  not create). MECHANISM CORRECTED during execution `[plan]`: the
  existing not-safegit-authored range check did NOT cover a
  single-commit fast-forward (the entry's recorded tip populated
  `reversing`, so a one-commit ff would have been undone); as built,
  undo treats any oplog entry carrying an `outcome` key as a record of
  what git did to the branch, never a commit safegit authored
  (`authoredByPipeline` — exact: the pipeline never writes `outcome`),
  and the no-undoable-operations refusal names the skipped
  fast-forward. Pinned. `--no-ff` elects a merge commit; `--ff-only`
  refuses non-fast-forward (safegit's OWN refusal at exit General, not
  git's 128 — letting git decide would move the ref outside CAS in the
  race; DIVERGENCE-flagged in code). The merge payload additionally
  carries an `outcome` member (fast-forward/parked/up-to-date carry no
  commit; the other members are unreadable without it) `[plan]`.
  EXECUTION-DISCOVERED, PRE-EXISTING, AWAITING THE USER (as-built
  stands): `internal/coord`'s dirty-tree check runs `git diff HEAD`,
  which is fatal on an UNBORN branch — every guarded command already
  refused in a no-commits repository before this campaign, and still
  does; merge's unborn fast-forward path is implemented but unreachable
  until coord learns unborn HEADs (empty-tree diff). Fixing it is a
  small capability addition outside the ruled scope — the user decides
  whether it rides this campaign's remediation or waits.
- Non-fast-forward path: run `git merge --no-ff --no-commit` — BOTH
  flags always, regardless of what the operator passed: `--no-commit`
  alone cannot stop a fast-forward (probed: it fast-forwards the ref
  immediately), so if the ff-ness check races a concurrent tip move, a
  bare `--no-commit` invocation would move the ref outside CAS;
  `--no-ff` makes that impossible (git always parks a merge state, and
  the conclusion's CAS ref update then catches any tip movement).
  Clean result (probed: exit 0, MERGE_HEAD + MERGE_MSG + AUTO_MERGE
  present, result fully staged, ref unmoved): the parked state is
  concluded IMMEDIATELY through the merge-continue engine with an
  empty resolution set — message from `-m` (probed: MERGE_MSG carries
  exactly the custom message) or the stripped MERGE_MSG default,
  commit-msg hook, trailers, CAS ref update, state cleanup — a
  pipeline-authored merge commit. Conflicted result: state parks
  exactly as today and the operator concludes with `merge-continue`.
- OUTPUT CONTRACT `[plan — two committed pins bind it]`: the compute
  step's git narration (CONFLICT lines etc.) is captured and relayed
  through the 1.3 channel convention — human mode re-emits on the
  original channels; `--json` re-routes child stdout to stderr
  (machine_contract_json_document_test.go:185-193 requires CONFLICT on
  stderr and absent from stdout under --json; merge_fixture_test.go:86
  requires CONFLICT visible on a conflicted `safegit merge`, and that
  fixture underpins many conclusion suites).
- OPLOG `[plan — a committed pin binds it]`: the pipeline entry for a
  merge concluded by `safegit merge` itself carries Op `merge`
  (wave2_passthrough_oplog_positions_test.go:114-116 requires exactly
  ONE entry with Op=="merge" — the pipeline's `req.OplogOp` is set per
  entry command, not hardcoded to the conclusion command's name); a
  conflicted merge concluded later by `merge-continue` records
  `merge-continue` as today. Entries carry the 1.2 baseline spelling.
  `merge` gains a payload schema (members follow the conclusion
  payload minus the resolution members) `[plan]`.
**Verify:** red-first — a clean non-ff `safegit merge` produces a
pipeline-authored commit (trailers present, undoable) with both parents
and exactly one Op=="merge" oplog entry; ff moves the ref AND syncs
index+worktree (git status clean after), with the ff outcome recorded
and undo refusing it; octopus argv, `-s`/`-X`, and `--autostash` refuse
naming the divergence; `--no-commit` parks without concluding;
conflicted merges still park and conclude with CONFLICT narration on
the pinned channels; existing merge-continue suite stays green.

### 2.3 Cherry-pick restructure
`safegit cherry-pick` becomes single-form and pipeline-authored:
- ONE commit argument; multi-commit argv AND range/rev-set spellings
  (`..`, `...`, `^` operators — probed: `git cherry-pick A..B` creates
  queue state even for a one-commit range) are refused naming
  sequential single invocations `[user — the revert precedent
  applied]`. The argument must resolve to exactly one commit
  (`rev-parse --verify <arg>^{commit}` after the operator refusal).
- THE PARK FILE `[user]`: `git cherry-pick --no-commit` records
  NOTHING about the picked commit — no CHERRY_PICK_HEAD on either the
  clean or the conflicted path (probed twice, independently; git only
  writes CHERRY_PICK_HEAD on a conflicted pick WITHOUT `-n`). The
  conclusion machinery, author preservation (`sequencer.SourceAuthor`
  reads state.Source), marker labels, `git status`, and
  `git cherry-pick --abort` all key on that file. So: after the
  `--no-commit` compute, SAFEGIT WRITES `.git/CHERRY_PICK_HEAD` itself
  (git's own trivial one-line-SHA format), uniformly on clean and
  conflicted paths — the parked state then matches what a conflicted
  pick looks like today, and the existing machinery works unchanged
  (the conclusion's state-file cleanup already owns the file's
  removal). Clean pick: conclude IMMEDIATELY through the
  cherry-pick-continue machinery — author preserved from the source
  commit, committer the operator (the campaign-1 authorship rule),
  state files cleaned, undoable. Conflicted pick: parks; the operator
  concludes with `cherry-pick-continue`. (Contrast recorded for the
  implementor: `git revert --no-commit` DOES write REVERT_HEAD on both
  paths — probed — which is why revert_cmd.go never needed this; the
  park-file write is the pick-specific addition.)
- `--abort`/`--quit` remain guarded passthrough state-control forms
  (they author nothing); `--skip` is refused naming `--abort` and the
  sequential form `[user]`. Operator-level `--no-commit` stays a
  guarded passthrough (it authors nothing); `--edit` is refused.
- OUTPUT CONTRACT: the compute step's narration relays through the 1.3
  channel convention (same pins-driven rule as 2.2).
- OPLOG: ONE entry per pick, Op `cherry-pick` (the wave2 pick pin
  counts entries under that op name); any compute-step append is not
  written `[plan — the revert double-entry rule applied]`. Payload: a
  dedicated single-pick schema (continuePayload members minus the
  queue members) `[plan]`.
**Verify:** red-first — a clean pick's commit carries trailers and the
source author, exactly one Op=="cherry-pick" oplog entry, and `safegit
undo` reverses it; multi-commit and range argv refuse; `--skip`
refuses; conflicted pick parks with CHERRY_PICK_HEAD present,
`git status` shows the pick, and `cherry-pick-continue` concludes;
Appendix B covers the retired passthrough pins.

### 2.4 Revert single-form
As ruled: the git-authored passthrough arm is DELETED. `safegit revert
<commit>` = the restructured single-commit form. Refusals: multi-commit
argv AND range/rev-set spellings (same rule and mechanism as 2.3),
`-S`, `--edit`, and any other option the pipeline cannot honor.
`--abort` and `--quit` remain guarded passthrough state-control;
`--skip` is refused naming revert-continue and `--abort`; operator
`--no-commit` stays a guarded passthrough `[plan]`. OPLOG `[plan]`: one
entry per revert — the pipeline's entry, Op `revert`; the
compute-step's own `Op:"revert"` append (revert_cmd.go:182-187) is
DELETED (today a single revert writes two entries; the not-undoable one
goes). PAYLOAD `[plan]`: a dedicated single-revert schema (the
continuePayload members minus the queue members — reusing
revertContinuePayloadSchema would require members that lie); the
in-code no-schema statements (sequencer_continue_cmd.go:182-184,
revert_cmd.go:296-298) are rewritten; the declined-checks member from
1.1 joins here. The preview fix is scoped to the REVERT caller of
previewSequencerOperation only (revert_cmd.go:167) — merge's and
cherry-pick's recorded argv are correct and their previews are
untouched.
**Verify:** the revert restructure suite; the retired passthrough-arm
pins per Appendix B; NEW red-first: single revert writes exactly one
oplog entry; range argv refuses.

### 2.5 Pull
`safegit pull` is ALREADY a declared command composed as
fetch-then-merge (main.go:429-460 registers it with the required
`--merge-strategy` selector, Choices ff/ff-only/no-ff;
coord_cmd.go:211-229 builds its own merge argv) — the work is
REPLACING ITS MERGE STEP with the 2.2 path and ADDING a structured
payload (fetch summary + the merge payload) `[plan]`. The strategy
values map onto 2.2 directly: `ff` = fast-forward when possible, else
a pipeline-authored merge commit; `ff-only` = 2.2's `--ff-only`;
`no-ff` = always a merge commit. `--rebase` is refused naming the
two-step (rebase is its own command and its own door). Oplog: the 1.2
baseline spelling under Op `pull`, exactly one entry per run (today's
entries carry remote/branch and no tips — reshaped; the wave2 pull
expectations get the same op-name care as 2.2's).
**Verify:** red-first — a pull that merges produces a pipeline-authored
commit; `--rebase` refuses; oplog baseline entry present under Op
"pull".

PHASE-2 AUDIT FINDINGS (remediated post-audit) `[plan]`:
- THE OCTOPUS ESCAPE (the audit's one defect): `merge FETCH_HEAD` with
  multiple for-merge lines produced a pipeline-authored THREE-PARENT
  commit (token counting, the exact error class the rev-set correction
  fixed for pick/revert — FETCH_HEAD is one token resolving to N
  heads), and on the fast-forward arm silently ff'd onto the FIRST
  line dropping the rest. Fix: FETCH_HEAD is the only token git
  special-cases into multiple heads — when the merge argument is
  spelled FETCH_HEAD, read .git/FETCH_HEAD and refuse >1 for-merge
  lines UP FRONT (before the ff decision, so both arms are covered;
  pull inherits); plus defense in depth, `concludeParkedOperation`
  refuses a multi-line MERGE_HEAD park (the raw-shape octopus check
  now guards the immediate-conclusion path too, keeping 3.5's
  totality structural), cleaning up the state safegit itself just
  parked. Red-first from all three audit probes (loud octopus,
  silent ff drop, conflicting-octopus control).
- `merge --commit` was accepted and silently stripped —
  accept-and-quietly-ignore is the shape this campaign kills, and
  pick/revert refuse it by name; merge now refuses it by name too
  (uniformity; divergences entry rides it).
- REVIEW NOTE (catalog review): 2.2's "exactly one BRANCH argument"
  is enforced as exactly-one-COMMITTISH — a tag or SHA merges (no
  detach harm; switch's branch-ness check has no analogue here). The
  merge entry states this reading for the user's verdict.
- FOR 3.3's IMPLEMENTOR: the crash-window red currently fails for
  2.7's no-AUTO_MERGE refusal, not its own reason — its fixture
  snapshots only index/MERGE_HEAD/MERGE_MSG while a REAL crash in the
  window leaves AUTO_MERGE present; add AUTO_MERGE to the fixture
  snapshot (faithful state), no production ordering change for this.
EXECUTION CORRECTIONS after 2.3-2.5 (executed by 2.6/2.7) `[plan]`:
- `-s`/`-X` are REFUSED on cherry-pick and revert too, matching merge.
  Forced for `-s` by 3.5's ruled totality (a pick computed with
  `-s resolve` parks a content conflict with NO AUTO_MERGE — exactly
  the shape 3.5's "safegit cannot start them" premise excludes); `-X`
  joins for uniformity with merge's stated reason (weakly held; every
  refusal is reviewable at the divergences review). The as-built 2.3/
  2.4 honor-list carrying them is corrected in 2.6's allowlist pass.
- The dry-run would-do logs of merge AND cherry-pick still record the
  bare operator argv while the execute paths run the compute argv
  (`git merge --no-ff --no-commit ...`, `git cherry-pick --no-commit
  ...`); revert already records its compute argv. All three record the
  COMPUTE argv (the dry-run doctrine: records are what the execute
  path performs). The plan's earlier "their recorded argv are correct"
  premise was written before the restructure and is obsolete.
- The multi-commit queue-replay path in sequencer_preview.go
  (replayOrder's range reversal, previewReplay's multi-step loop) is
  unreachable after 2.3/2.4's refusals — DELETED in 2.7 with the
  delegation machinery (superseded dead code).
- 2.3's probe found `--commit` trailing safegit's prepended
  `--no-commit` wins (git's last-wins) and would have moved the ref
  outside CAS — refused in both tables; 2.6's allowlists must keep
  that refusal.
- `--no-verify` does not exist on git cherry-pick/revert (moot there);
  it DOES exist on git merge and 2.2's table does not carry it — 2.6's
  default-deny allowlist covers it and `-S`/`--gpg-sign` on merge with
  explicit reasons (the pipeline runs the commit-msg hook and does not
  sign; silent ignoring is the killed shape).

### 2.6 The allowlist and the switch rename `[user — full allowlist now]`
Every guarded git-forwarding command validates its forwarded argv
against an explicit allowlist BEFORE anything runs; unlisted tokens are
refused (default-deny) with a message naming the subset law and the
divergences doc. Refusals are parser-shaped (exit 2). Each refused
capability gets a divergences entry in Phase 7; the user's pre-release
divergences review is the checkpoint where any verdict here is
overturned. Verdicts:
- NAVIGATION IS `safegit switch` `[user — deliberate rename]`:
  `checkout` stops existing as a safegit command. `switch` accepts an
  EXISTING BRANCH NAME or `-c <new>` (branch creation; switch's own
  spelling replaces checkout's `-b`) — and nothing else. Tags, SHAs,
  and other commit-ish arguments are refused naming the reason (any
  non-branch argument detaches HEAD — the flag `--detach` alone guards
  nothing — and safegit treats detached HEAD as a refusal state whose
  remedy its own guide teaches) with the raw-git escape named for the
  rare deliberate case `[user — branch names only]`. Refused flags:
  `--detach`, `-C` (force-recreate), `--force`/`--discard-changes`,
  `--orphan`, `--merge` (dead through safegit — it exists to carry a
  dirty tree across, and the dirty-tree check refuses first; same
  shape as merge's `--autostash`). File restoration is deliberately
  ABSENT (switch has no file mode — the destructive
  `checkout -- <path>` shape, banned fleet-wide, becomes structurally
  inexpressible rather than refused). Divergences: two entries
  (safegit implements switch, not checkout; file restoration
  deliberately absent) replacing checkout's refusal list. Breaking
  changelog entry; the rename sweeps the handler, registries,
  machine-contract row, oplog op (`switch`), docs (Appendix B).
- `reset`: ALLOW `--hard`, `--soft`, `--mixed`, `--merge`, `--keep`
  with a commit argument (guards derived per 1.1). REFUSE the pathspec
  form (`reset <commit> -- <path>` manipulates the shared index safegit
  owns) and `--patch` (interactive).
- `bisect`: the classified subcommand vocabulary IS the allowlist
  (1.1's default-deny already refuses the rest).
- `rebase`: ALLOW `<upstream>`, `--onto`, `-i` (interactive rebase is
  git's own door end-to-end; campaign 1 deliberately kept its editor
  working under the operation lock), `--continue`/`--abort`/`--skip`
  (git's own), `--autostash`. REFUSE the apply backend and its options
  (`--whitespace`, `-C`), `--exec`, `--rebase-merges`, `--root`.
- `merge` / `cherry-pick` / `revert` / `pull`: the option surfaces
  stated in 2.2-2.5 are the allowlists (everything else refuses).
Mechanism `[plan]`: one table per command in the registration layer,
validated in the handler before the operation lock; the table is code
(no generated doc table — the divergences entries are the documentation
of what is refused and why).
**Verify:** red-first per command — one allowed form passes, one
refused form names the law; `switch` to a branch works and to a SHA
refuses; the destructive restore shape does not parse as any safegit
command; every refusal has a divergences entry by the end of Phase 7.

### 2.7 Raw-git shapes refused; delegation deleted
- `cherry-pick-continue` and `revert-continue` with the sequencer queue
  directory present (a multi-commit sequence, which only RAW git can
  now create — the reader's discriminator is `.git/sequencer`
  existing) refuse, naming git's own `--continue`/`--abort` as the way
  to finish what git started.
- `merge-continue` refuses raw-git merge SHAPES safegit can no longer
  start `[user]`: MERGE_HEAD carrying more than one line (octopus), or
  a content-conflicted path with NO AUTO_MERGE file (the non-default-
  strategy signature; on the supported git floor a normal ort merge
  always writes AUTO_MERGE). The refusal names git's own conclusion as
  the way out. The campaign-1 octopus-conclusion capability is DELETED
  — its green pin becomes a refusal pin and its fixtures switch to raw
  git (Appendix B); the sequencer reader KEEPS multi-line MERGE_HEAD
  support (the refusal needs it to detect the shape).
- The delegation machinery is deleted: sequencer_delegate.go,
  `git.AdoptIndexFrom`, the delegated payload shape and its members
  (`queue_delegated`, `stopped_again`, delegatedPayload,
  `continuePayloadSchema(true)`), `reportDelegated`, the delegation
  branch at sequencer_continue.go:466-468, the delegation notice, and
  the delegated oplog op. RELOCATE FIRST: the `unmergedCount` helper
  (sequencer_delegation_test.go:109) is imported by the 3.6 spec red
  (commit_unmerged_index_test.go:48) — it moves to a surviving file
  before the suite deletion, or the whole test package stops
  compiling.
**Verify:** red-first — a raw-git queued pick meets the refusal naming
git; a raw-git octopus merge meets merge-continue's refusal; the
delegation symbols gone (build asserts no callers); refusal pins
replace the delegation suite per Appendix B; the 3.6 red still
compiles.

### 2.8 Fallback deletions
- commit `--hunks`: the silent `--3way` retry
  (internal/stage/stage.go:180-198) is deleted; failure is a hard error
  naming the file — `ApplyPatch(ctx, indexPath, patch)` has no path
  parameter, so the SIGNATURE gains the repo-relative path (or the
  caller wraps the error with it) `[plan]`; red-first (no wave pin; the
  real test neighbors are the commit_hunks_conflict tests — no "stage
  retry tests" exist).
- scrub's submodule-enumeration failures become HARD errors at all
  three seams: scrub.go:154-158 (fully silent today) and
  scrub_match.go:242-244 and :535-538 (which warn today but still
  silently drop submodules from the rewrite's SCOPE — the warn does
  not cure the scope narrowing). doctor.go:686's enumeration-failure
  warn ESCALATES to an error-severity finding `[plan — the failure
  silently empties doctor fix's submodule cleanup scope, the same
  class]`. The submodule redirect's infof line stays (the payload
  already carries the submodule member); `--remap-shas-in` is honored
  on the parent during a submodule-target scrub exactly as its help
  declares (verified; `scrub_remap_test.go:403` stays green — no
  change there).
**Verify:** each deletion red-first; the hunks failure names the file;
the enumeration errors are hard on all three scrub seams; doctor's
finding escalation pinned.

---

## Phase 3 — Conclusion repairs

Depends on 0.4, 1.2, and 2.4 (the revert payload schema exists — a
passthrough with no schema panics in `Context.Payload`; the spine
records 2 -> 3).

### 3.1 The family code (exit 26) and envelope-always `[user]`
Scope: every pipeline author. The site map (two files share a base
name; paths are spelled in full):
- Genuinely post-ref die() sites converted to report-then-return:
  sequencer_continue.go:524 (finishConclusion) and :529 (auto-bump);
  revert_cmd.go:288 and :293. (The delegation site is deleted by 2.7,
  not converted.)
- NOT converted: sequencer_continue.go:508-517 and revert_cmd.go:283
  route pipelineExitCode over the GENERIC pipeline error — pre-commit
  failures with real exit codes (5/7/8/9/10/16) that must survive.
  These sites gain the family code ONLY via the new typed
  post-ref-update pipeline error.
- The typed error: internal/commit returns a typed
  commit-stands-partial result (carrying the created SHA and the failed
  step) from the post-updateRef failures —
  internal/commit/commit.go:665-667 (reconcile),
  internal/commit/amend.go:356-358 and :607 — through BOTH result
  types (CommitResult and AmendResult).
- Additional aftercare family members: the post-ref auto-bump failure
  sites in every author — root commit.go:234-236, :433-435 (amend),
  :503-505 (reword); mv.go:576-585 (whose auto-bump arm RETURNS
  exitcode.General rather than dying); undo.go:310-312. The
  runCommit-family handlers return ints; the widening threads the
  family code through those returns.
- consumeAutostash's outcomes map into the payload: an `autostash`
  member (state enum + stash SHA where one exists) plus a `residue`
  list on continuePayload. Context.Payload is one-shot —
  finishConclusion and aftercare RETURN structured outcome data; one
  payload is built at the end. The reason die() converts to return is
  the ENVELOPE seam (os.Exit bypasses finishDispatch); lock release is
  NOT the reason (die already releases pending locks).
- Exits stay outcome-only; the stored-autostash exit moves from General
  to 26.
- AS-BUILT notes (ratified): the typed result travels through all
  THREE result types (RewordResult too — the plan's "both" predated
  it); at the NOT-converted pipelineExitCode sites a PartialError with
  a non-nil result reports-then-returns while everything else still
  dies with its real code (a 26 through die() would lack the envelope
  the ruling mandates); the autostash enum gained `pending` (the
  not-reached state — "none" would lie); undo's post-ref reconcile arm
  joined the family AND now writes its oplog entry where die() used to
  skip it (an unrecorded ref move read as foreign by later passes).
  RIDER for 3.2-3.5's implementor: the four Phase-2 command payload
  schemas (merge/cherry-pick/revert/pull) gain the `residue` member
  too — they are pipeline authors exiting 26 whose envelopes currently
  say only state_cleared:false; the ruled "payload carries aftercare"
  principle covers them (autostash stays OFF those four: their
  clean-tree precondition makes autostash state impossible by
  construction there — stated in the schema comment).
**Verify (red to green):** both conclusion_envelope tests. NEW
red-first: each aftercare shape exits 26 with the envelope; ordinary
commit's post-ref reconcile failure emits the envelope naming the
created sha; registry + generated table regenerated (exit_table_test
enforces).

### 3.2 Stale-autostash guard (0.4-keyed) + doctor orphan check `[user]`
Key: consume MERGE_AUTOSTASH only when the stash commit's first parent
== HEAD AND its message carries git's autostash shape (`On <branch>:
autostash`) — the 0.4 probe records the fact. (This machinery serves
autostash state RAW git created — `safegit merge --autostash` itself is
refused per 2.2.) A failing stash is not consumed, not deleted: named
in the residue list; doctor's new check (registration-table row, WARN
severity `[plan]`) reports MERGE_AUTOSTASH-without-MERGE_HEAD. The
`--action fix` store-as-stash action ships in 4.2 with the doctor-mint
work `[plan — 3.2 delivers the CHECK, 4.2 the FIX action]`.
**Verify (red to green):** both stale_autostash tests (the diagnose
half here; the fix half's mint pin in 4.2). Green stays: the
genuine-autostash tests.

### 3.3 Crash-window idempotence `[user]`
Placement: after refuseWrongState AND the detached-HEAD refusal
(sequencer_continue.go:436), after the stages read (:440) and
indexEditsFor (:454) — the idempotent branch needs the edits to run
finishConclusion — and BEFORE checkCompleteness (:445 moves below it),
since a crash-restored index reads as resolved-but-not-conflicted
there. Key: the pipeline's own oplog entry for the ref (op matches, sha
== HEAD; the append precedes the reconcile so it exists in the window),
corroborated for merges by HEAD's parent set equaling 1+MERGE_HEADS.
ACKNOWLEDGED WINDOW: a crash between the pipeline's ref update
(internal/commit/commit.go:615) and its oplog append
(internal/commit/commit.go:649, error discarded) leaves no entry —
merges are still caught by parentage; a pick/revert in that sliver is
not detected (stated scope limit, alongside the crash-after-Cleanup
half that is 3.6-doctor territory).
**Verify (red to green):** the crash_window test. Green stays: the
conclusion suite.
AS-BUILT CORRECTION `[plan — defect in this subphase's stated key,
found and fixed red-first]`: the oplog+sha==HEAD key FALSE-POSITIVES
on cherry-pick/revert — after any concluded pick the tip still is the
logged commit, so a SECOND conflicted pick read as "already concluded"
had its state removed and its commit silently dropped. As built,
`concludesThisState` corroborates per kind: merges by parentage (the
plan's rule), picks/reverts by HEAD's message opening with the message
THIS conclusion would write; regression-pinned
(sequencer_conclusion_second_pick_test.go). The recognition op set
also accepts the restructured commands' own op names (their immediate
conclusions leave the same crash state). Also as built (ratified): the
autostash enum gained `foreign` (a stash failing the ownership key —
"none" denies the file, "pending" means not-reached); pull's residue
reports through merge.residue (one fact, one member).

### 3.4 Deletion-honest reporting `[user]`
worktreeEffects (the computation, sequencer_continue_cmd.go:380-390)
learns the absent-stage fact via sides carried on conclusionResult; the
affected path moves between the written/removed groups in the renderers
(sequencer_continue_cmd.go:250-282 and :342-348) — the fix rides the
existing pinned wording, not an extra sentence.
**Verify (red to green):** the deletion_report tests.

### 3.5 The overwrite refusal (exit 27) `[user]`
A SEPARATE per-path verdict loop over the full declared set — NOT
inside the verifyMarkers pass, whose verifiablePaths EXCLUDES
delete-resolved paths and whose exemption skip must not apply here. For
each declared path whose materialization would destroy disk content,
the ACCEPTED SET is: the three stage blobs, UNION the `AUTO_MERGE`
blob for the path where one exists (`AUTO_MERGE:<path>` — git's
verbatim saved emission; used DIRECTLY, no reconstruction, which also
covers rename-mediated conflicts whose path-suffixed marker labels
reconstruction cannot reproduce — probed). Kinds with no emission
(delete-resolved, absent-stage deletes, delete/modify, binary,
merge-driver paths) are stages-only BY NATURE and the check is still
complete there — what git left on disk for those kinds IS a stage
blob. The shapes with content conflicts but no AUTO_MERGE never reach
this loop: safegit cannot start them (2.2) and merge-continue refuses
them from raw git (2.7) — so the check is TOTAL over everything
safegit concludes, with no skip arm and no declined-check fallback
`[user — front-door refusal chosen over a degraded check]`. Covers
ours/theirs overwrites, declared delete, and absent-stage deletes;
byte-compare via CatFileBlob (no clean-filter hashing); the flag
`--discard-unmatched-worktree` (name weakly held) elects destruction —
justified as a consent flag because no other route expresses "yes,
destroy my hand edits"; registry row for 27.
**Verify (red to green):** the overwrite test. NEW: flag elects;
delete and absent-stage variants refuse; a rename-mediated conflict's
untouched emission passes (the AUTO_MERGE-blob arm); the
worktree-writing greens stay green.

### 3.6 The unmerged-index guard (exit 28) + doctor repair `[user]`
Git parity: ANY unmerged entry in the shared index refuses every
pipeline commit; conclusions exempt (req.Sequencer /
IndexBaseSharedIndex). Mechanics `[plan]`: the guard reads
git.UnmergedStages beside guardSequencer's call sites
(commit/amend/reword entries; mv inherits via the pipeline); the
refusal names git's fact and `safegit doctor --action fix`. The doctor
repair: ONLY when sequencer.Read reports nothing in flight; takes the
worktree operation lock (a second shared-index writer — the
single-writer comment at internal/git/index_resolve.go:33-35 updates);
re-stages disk content to stage 0 via HashObjectWriteBytes +
SetIndexStage0; minted per the 4.2 doctor-mint convention (the fix
action SHIPS in 4.2 — same resolution as 3.2's).
**Verify (red to green):** the unmerged_index test (the guard half:
refusal fires, names doctor). The REPAIR half's verification lives in
4.2 with the fix action (the repair resolves a planted orphan state
and a subsequent commit succeeds); conclusions still commit during
unmerged state (pinned here).

### 3.7 Undo's auto-bump ordering `[user]`
`requireAutoBumpDecision` is ADDED to undo (it is not called anywhere
in undo.go today; existing call sites live in commit.go, mv.go,
revert_cmd.go and sequencer_continue.go) between loadConfig and the
operation lock (the undo.go:75-80 gap), so the refusal fires BEFORE
any ref moves — the same refusal shape every other commit-family route
uses; the two stale comments (autobump.go:210-212, :131-136) update.
**Verify (red to green):** the undo_autobump test.

---

PHASE-3 AUDIT FINDINGS (remediated post-audit) `[plan]`:
- D1: the already-concluded (crash re-run) path bypassed
  refuseWorktreeOverwrite — routed through it (3.5's totality holds on
  every materializing path).
- D2: pick/revert crash recognition by message prefix false-positived
  on duplicate subjects (a second `wip` pick swallowed, work dropped)
  — fixed source-anchored: the pipeline's pick/revert conclusion oplog
  entries record the SOURCE sha, and recognition requires the logged
  source to equal the parked state's Source; message-prefix
  corroboration retired.
- D4: the crash re-run skipped completeness and declared "index and
  working tree in step" while leaving unmerged stages. As redesigned:
  the commit embodies the original resolutions, so the re-run derives
  its index/worktree edits FROM THE COMMIT's own tree for the
  conflicted paths (stage-0 the committed blobs, drop
  committed-deletions), runs the overwrite check (D1), then aftercare
  — the success claim becomes true and no stages survive. A re-run
  declaration whose side's blob differs from the committed content is
  refused naming the standing commit (silently ignoring it is the
  killed shape); matching declarations are accepted and moot.
- The fast-forward sync-failure arm (2.2's, shared by pull): the ref
  moved but the run exited General with a null payload — exit 26's
  meaning WIDENS to "the operation's ref move is real; aftercare did
  not finish" (registry text updated) and the arm carries the merge
  payload (outcome fast-forward, residue naming the sync step).
- The autostashPending arm no longer claims "your uncommitted work"
  without ownership (checks the key or drops the claim).
- The residue member extends to commitPayload and mvPayload too (the
  same ruled payload-carries-aftercare principle; the earlier rider's
  list was not exhaustive).
- D3 = REVIEW ITEM FOR THE USER (as-built stands): the ruled autostash
  ownership key (first parent == tip AND message shape) cannot
  distinguish a GENUINE abandoned autostash from the current merge's
  own when the tip has not moved (requires hand-mutilated state:
  normal aborts re-apply the stash; only manual state-file deletion
  leaves the file behind at the same tip). The false code comment
  claiming that coverage is corrected and the limit documented in code
  and in the provisional catalog entry; whether to strengthen the key
  is the user's call at the review.

## Phase 4 — Effects honesty `[user]`

Depends on 1.5 and 3.1 (the payload conventions the effects records
ride). 3.2's and 3.6's doctor FIX actions ship inside 4.2.

### 4.1 Undo
Mint the ref update / root-undo deletion through the effects handle
with real SHAs (from the oplog; CAVEAT stated: currentSHA can fall
back to RevParse on a thin log — undo.go:221-227 — still a real SHA),
NEW exemption row (kind effects-handle; reusing the commit row would
falsify its ID), the per-ref lock block wrapped in !dryRun (the
no-lock table row stays green). THE PREVIEW CARRIES THE PARENT BUMP
`[user — the earlier exclusion is overturned]`: per the dry-run
doctrine, the dry path performs ALL reads first (the oplog rollback
target, then the parent-bump decision reads: parent config,
CheckNested, the gitlink), then records the would-do mutations IN
EXECUTION ORDER — the ref move first, then the parent-bump record
(the real path moves the ref before maybeAutoBumpParent; the recorder
convention lists effects in the order the execute path performs them)
`[plan — order corrected to execution order]` — with the real rollback
target from the oplog and previewCommitPlaceholder for the parent's
own commit SHA; then returns. Undo gains its payload schema and
`WithTags("json")` alongside it `[plan]`. The exemption enumeration
test (internal/gitexec/exemptions_test.go:13, exact-set) updates here
and in 4.3.
**Verify (red to green):** the undo_effects tests, adjusted so the
preview asserts BOTH records in execution order in a submodule fixture
and exactly the ref record outside one; the no-lock row pin stays
green.

### 4.2 Unlock and doctor fix
- unlock: Effects-based removal of the computed lock path; refusals
  stay in front. The remover must ERROR on a missing path `[plan]` —
  Effects.Remove is RemoveAll semantics (missing = success), which
  would silently convert reclaimNone into reclaimDone; use a remover
  that os.Remove-errors.
- doctor fix mechanics: orphan tmp dirs — internal/index gains a plan
  API (the GC scanner returns PATHS, not just names/counts;
  GarbageCollect takes the remover or doctor iterates the planned
  paths itself) `[plan]`; legacy queue dir, policy file, publication
  temps — Effects.Remove over scanner-computed paths in both modes;
  stale locks — `reclaimLocked` (whose SINGLE unlink at reclaim.go:130
  both its callers share) gains a REMOVER PARAMETER: doctor's path
  (ReclaimIfStale) passes the minted remover, the Acquire-path caller
  (lock.go:226) passes os.Remove `[plan — corrected: there is no
  second direct remove today; the parameterization creates the
  split]`; lockScan gains paths. Dry mode never uses the reclaim path
  (scan-based). doctorFix's two-branch shape collapses onto
  plan-then-mint; diagnose stays effect-free. The 3.2 store-as-stash
  and 3.6 orphaned-unmerged fix actions ship here, minted; the 3.6
  repair verification (planted orphan resolved; subsequent commit
  succeeds) lands with them.
- STAY-GREEN constraints listed (Appendix B): the reclaim/naming/index
  unit tests; the stdout pins ("stale lock", "Would back up", "Would
  fast-forward", submodule fix lines) survive beside the new records.
**Verify (red to green):** unlock and doctor-fix mint tests; the 3.2
fix-half pin; the 3.6 repair pin.
AS-BUILT notes (ratified): the 3.6 repair uses one stdin-free
`git update-index --add -- <path>` per path (probed: collapses stages
1/2/3 to stage 0) instead of the plan's HashObjectWriteBytes +
SetIndexStage0 — Effects.Run cannot feed stdin (the known framework
gap), and the replacement additionally gets modes, clean filters and
symlinks right; missing-from-disk paths get --force-remove. A THIRD
exemption row (main.runRepairGit) joined the anticipated two, with
KindEffectsHandle's doc widened to cover declared-Cwd sites; the
restore's EXECUTE-path merge is minted too (a real mutation invisible
in machine mode is the phase's own defect); the parent-bump preview
propagates the real path's refusals instead of swallowing them.
Appendix-B addition: internal/lock/lock_test.go's ForceRelease call
sites (signature gained the remover parameter). Phase 7 rows: the
health-check table's merge_autostash row gains the fix-action
sentence; classify.go's stale `stash` row note; doctorFix's
IsInitialized precondition (an uninitialized repo's fix repairs
nothing — unreachable from the exit-28 path, noted for the audit).

### 4.3 Backup
- backup backup: the dry path mints the push argv with a placeholder
  lease via execGitPush under the existing exemption and grant; the
  placeholder KEEPS the `--force-with-lease=` prefix spelling (grant
  selection depends on it) `[plan]`; exposure classification stays off
  the dry path. The execute-path fetch inside backup is NOT minted
  `[user — awaits the framework's network-effects ruling; the await
  todo tracks it]`.
- backup restore: `fetchSlotObjects` (backup.go:52-61, shared with
  backup backup) is SPLIT — the fetch invocation mints via Effects.Run
  (new effects-handle exemption row); the FETCH_HEAD RevParse runs
  only on the execute path; the dry path records the fetch and the
  ff-only merge from the slot SHA it already read and performs neither
  — NOTE, stated honestly: that slot read (`remoteSlotSHA`,
  backup.go:373) is itself an `ls-remote` NETWORK READ, so `backup
  restore --dry-run` contacts the remote today and continues to under
  this subphase; that behavior rides the same framework
  network-effects await as the fetch mint (non-goals) and Phase 7's
  docs state it. The shared caller keeps its old direct behavior
  `[plan]`.
**Verify (red to green):** both backup mint tests; the would-do stdout
pins stay green.

### 4.4 The parent-bump preview record
The record is minted in maybeAutoBumpParent so its callers inherit —
the call sites: commit, amend (root commit.go:433), reword (:503), mv,
the restructured revert, cherry-pick, merge and pull (their pipeline
conclusions), the conclusions themselves, and undo (per 4.1's
reads-first, execution-ordered records). PREVIEW DECISION PROCEDURE
`[plan]`: the dry path performs exactly the reads the dry branch of
requireAutoBumpDecision already performs (parent config) plus
CheckNested and the gitlink read (both reads; the comments at
autobump.go:138-141 and :189-191 update to permit them for the
preview); if the gitlink already matches, the preview records nothing
(the already-current case); the recorded argv's Triggered-by trailer
carries previewCommitPlaceholder.
**Verify (red to green):** the parent-bump preview test.
PHASE-4 AUDIT FINDINGS (remediated post-audit) `[plan]`:
- D1: the conclusion commands' dry path skipped concludeAftercare
  wholesale, so a conclusion preview in a submodule recorded no parent
  bump (the -continue door and the crash re-stand path; the
  immediate-conclusion site is latent — unreachable in a preview).
  Fixed red-first: the dry path reaches the parent-bump record per the
  doctrine.
- D2: undo's parent-bump preview hardcoded previewCommitPlaceholder
  where the real value (the rollback target) was in hand — violating
  the doctrine's own placeholder rule. recordParentBumpPreview takes
  the Triggered-by value; authoring callers pass the placeholder, undo
  passes the target.
- COHERENCE COMPLETION: doctor gains an ERROR-severity check row for
  the orphaned unmerged index (the repair existed with no diagnose
  visibility — a repair whose state diagnose cannot see is the
  silently-disabled-check shape; the state refuses every commit, hence
  error severity like the sibling escalations).
- RATIFIED AS FORCED: merge/cherry-pick/revert/pull previews cannot
  reach their pipeline conclusions (the compute is recorded, not
  performed — no park, no conclusion, no bump), so 4.4's caller list
  covers them only structurally; their previews record the compute or
  fetch argv and stop. Audit-noted, no action: the doctorFix dry
  path's post-record reads are benign (different repositories/paths;
  the git-state repairs reuse the plan's answers); conclusion
  execute-path worktree writes stay unminted (outside the ruled
  six-command list; reported in prose by 3.x); backup restore's
  no-slot die() emits no envelope (pre-existing seam); the four
  command previews carry payload null (Phase 2's shape — Phase 7's
  machine-contract paragraph states it).

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
mkdir + rollback run unchanged. Appendix B:
TestMvMovesRecordsAndCommitsInOneInvocation (mv_test.go:57, moves into
absent `sub/`) joins the sanctioned list with
TestMvCreatesTheDestinationDirectory and the scrub_moves fixtures.
Divergence fate `[plan]`: the entry is DELETED in Phase 7 — after this
change safegit MATCHES git (git mv refuses too), and converged
behavior carries no entry.
**Verify (red to green):** the wave2 mv test. NEW: the flag elects
with the mkdir recorded.

### 5.2 Escaping symlink targets refuse (exit 29) `[user]`
The refusal replaces the notice at its real site — the END of
resolveFiles (intake.go:506; the accumulation happens inside the loop
at :465/:483; the property is pre-CAS-loop, nothing staged, commit and
amend both inherit). Names the literal target;
`--allow-escaping-targets` (weakly held) elects committing and
restores the notice line. SCOPE `[plan]`: the refusal covers
ADDING/STAGING escaping link content; `safegit mv` moving an existing
tracked escaping link is untouched (move-only commits never restage
link content). Registry row 29; the freshness and completeness tests
regenerate. Phase 7 ADDS the guide's symlink policy text (none exists
today) and rewrites the divergences entry.
**Verify (red to green):** the wave2 symlink test; its non-escaping
control stays green. NEW red-first: the flag elects with the notice
line restored.

### 5.3 mv refuses dirty moves `[user — replaces the notice design]`
`safegit mv` REFUSES when any moved file carries uncommitted content
edits. No override flag exists — both legitimate intents already have
routes, and the refusal text names them: (1) edits belong in their own
commit -> commit the content first, then mv; (2) edits should ride
along with the move -> move on disk yourself, then `safegit commit
--moved 'old -> new' -- <new>`, which stages from disk and commits
content + move together. The refusal joins mv's collected refusal at
exit 19 (the world contradicts the move's preconditions — the same
family as a missing destination) `[plan]`. Mechanics: dirtiness is
filter-aware — compare `git hash-object --path <newpath>` of the disk
bytes against the parent blob (attributes decided by the NEW path), so
CRLF-conversion checkouts never false-refuse (control test); the
refusal names EVERY dirty path (aggregated human output for subtree
moves; the machine payload lists all of them, never truncated
`[user]`); the dry run refuses identically; the check runs with the
other pair validations before the first filesystem mutation.
OPEN RULING (execution-discovered, awaiting the user; as-built stands
meanwhile): the never-truncated listing cannot ride the PAYLOAD — a
refusal emits the envelope with `payload: null` (framework fact: no
error-payload channel exists, and mvPayload's schema describes a
performed mv). As built, the complete untruncated list goes to STDERR
(which machine mode never suppresses) and the envelope carries
exit_code 19 — the documented refusal shape. Whether a refusal payload
should exist is the user's call at the pre-release review. Related,
same review (audit-flagged): commit's exit-29 refusal exits through
die() and emits NO envelope at all, while mv's exit-19 refusal emits
the envelope with null payload — two different answers to a machine
consumer for the same refusal class (a pre-existing die-path shape;
3.1 shrinks the no-envelope set but does not touch refusals). And one
5.2 scope question for the same review: an ABSOLUTE symlink target
resolving INSIDE the repo is accepted (escape = "outside the
repository" per the subphase wording), yet it is machine-specific
exactly like an escaping one — it resolves to nothing in a checkout at
any other path. Spec-conformant as built; listed for review.
Divergences entry ("ours": git mv moves dirty files; safegit refuses,
naming the two routes).
**Verify:** red-first — dirty move refuses naming the path and both
routes; the autocrlf control does not refuse; subtree move refusal
aggregates in text and is complete in the payload; clean moves
unaffected.

---

## Phase 6 — Auto-record-moves `[user]`

Depends on 0.2, 2 (payload conventions and the boundary guard), 3, 5.
The design source is the ruled amendment set over
`todo/move-records-for-undeclared-moves.md` (consumed at Phase 10's
triage). Spec tests written red-first inside this phase.

### 6.1 The raw delta
DiffTree moves to raw+z+no-abbrev; ChangedPath gains
SrcMode/DstMode/SrcSHA/DstSHA; the root-commit synthesis branch
(git.go:911-921) fills them from its TreeEntry data. Consumers read
Status/Path only (verified: intake.go:134/148/858) — unaffected.
**Verify:** unit pins per field; existing DiffTree consumers green.

### 6.2 The mint engine
STRUCTURAL FACT: inference RUNS INSIDE tryCommit per attempt (declared
records stay resolved once pre-loop at internal/commit/commit.go:302);
the fences need TWO per-attempt tree listings — a paths-by-blob view
over the attempt's parent tree and one over the new tree — built as
fresh treeIndex-style instantiations inside tryCommit (the intake-time
instances are pre-loop against the old tip and are not reachable
there).
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
intersect empty -> no listings, no cost.
Shared-index requests skip inference entirely (IndexBase predicate);
amend has no IndexBase and gets 6.4's arm.
Ambiguity/cap refusals: one aggregate stderr line (intake-notice
channel — unconditional, once per operation) suggesting declaration;
the refused pairs ride the payload.
THE CAS RULE `[user]`, implementable form `[plan]`: attempt 1's
inferred record set is RETAINED AS DATA in the once-per-operation
state (the nativeHooks pattern) — never re-parsed from the cached
message text (a rewriting hook may have stripped records; text
comparison would false-abort). Each retry recomputes; a differing set
aborts the operation with the transient-race error advising re-run.
Identical set -> proceed with the cached message.
**Verify:** the fence/pairing spec tests (each fence red-first); the
CAS mismatch abort (two sessions, differing attempt-2 delta); the
shared-index skips.
AS-BUILT notes (6.1-6.3): ratified — the CAS-mismatch abort exits
General with the retry advice (no Phase-6 code was allocated;
CASExhausted's meaning would be falsified); a minted-vs-declared/
minted-vs-minted `trailer.Overlap` fence refuses self-contradictory
record sets; single pairs never collapse to subtree records; collapse
is greedy shallowest-first per group. CORRECTION ordered (executed by
6.4-6.6) `[plan — forced by the ruled un-blocking rationale]`: the
uniqueness fences' tree listings must EXCLUDE declared-away paths
(suppression extends to the fence views, not just the candidate sets)
— as first built, a blob at two parent paths never minted even when
one path was declared, making the ruling's own example unreachable.
REVIEW NOTE (audit + user): an `--untrack`ed path (removed from the
index, still on disk) can pair with a same-blob addition and mint a
move record — true about the TREE (which is what records describe)
while the old path still exists on disk; whether that claim should be
fenced off is open. Appendix-B addition:
TestAmendCanAddARecordToACommitThatLacksOne's fixture now changes
content in flight (its old clean-move fixture is exactly what
inference records; subject unchanged).

### 6.3 Witnessed subtree collapse and the cap `[user]`
One observed subtree record when the commit's delta fully witnesses a
uniform prefix mapping; the predicate's inputs come from the delta
plus the 6.2 per-attempt parent listing (no extra calls); the
scattered-move cap is a package const in internal/commit, value 20
(weakly held), refusing with the declare-it notice.
**Verify:** uniform-move single-record; scattered cap refusal; partial
moves fall through to per-file.

### 6.4 Origin token, supersede, amend, revert
- Token `observed` (declared/derived stay reserved); absence =
  declared; Record gains Origin; encoder emits the token for observed
  only; ParseRecord accepts it in the reserved slot (0.2's
  keyword-before-a-pair subtests rewritten here, sanctioned);
  RewriteMessage round-trips it (pinned — else every scrub strips it).
  The token's documented meaning: derived by safegit, not claimed by a
  person.
- SUPERSEDE, scope `[plan]`: same-commit amend ONLY (validateMoved
  makes later-commit re-declaration of an earlier commit's pair
  structurally impossible — the old path is no longer tracked; stated
  in code). Mechanism: refuseRedeclaredPairs (already a ReadMoves
  parser over the replaced message) becomes origin-aware —
  declared-vs-declared still refuses naming the id;
  declared-vs-OBSERVED emits a synthesized retraction of the observed
  id PLUS the new declared record in one commit. The retraction is
  INTERNAL SYNTHESIS — it does NOT route through resolveMovedRetract
  (whose reachable-history base cannot see the replaced tip's own
  records and whose same-breath doctrine forbids it); that doctrine
  comment (internal/commit/moved.go:132-135) is updated to name the
  supersede exception.
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
required list, CommitResult AND AmendResult, emission sites), closing
the declared-records payload gap.
**Verify:** payload spec tests; schema regeneration.

### 6.6 Contract reversals
The declared-only doctrine is reversed at every site that states it:
main.go:307-308 and the --moved help :311;
internal/commit/commit.go:113-117; internal/commit/moved.go:14-31 and
:325-337; internal/trailer/moved.go:8-15, :46, :225;
internal/trailer/project.go:29-32 ("nothing is stored about
confidence" — falsified by Origin); internal/git/git.go:901-904
(qualified, not deleted); docs/architecture.md:234;
docs/commands-guide.md:141; docs/_CLAUDE.md:84 (template);
moves_test.go:12-21 header; moves_cross_session_test.go:14-29 header;
commit_staged_deletion_dir_test.go:109/:130;
moves_declared_test.go:14-15. `assertNoRenameNotice` (moves_test.go:25
— verified a no-op; its strings match nothing in production) is
DELETED and its consuming tests — call sites spread across
moves_test.go and moves_cross_session_test.go — assert the real
properties directly (no content adoption; no auto-staging; the mint
notice's actual wording asserted where relevant) `[plan]`. The
divergences moves-entry rewrite happens in Phase 7 with the rest of
the catalog (single-writer rule); this subphase marks the entry stale
in a code comment only. The consumer-repo commit-msg-hook consequence
documented in the guide.
**Verify:** grep shows no declared-only doctrine claim survives
outside the catalog; the re-pointed tests pass.

---

## Phase 7 — Documentation

After Phases 1-6. SINGLE-WRITER RULE `[user — campaign-only override
of the same-commit convention; the convention stays in force for
steady-state development after this campaign]`: ALL divergences-catalog
edits (entries and the preamble roster) happen HERE, in one pass —
earlier phases only flag entries in code comments. The USER REVIEWS
the finished catalog PRE-RELEASE and may overturn any entry, including
every allowlist verdict from 2.6.

PROCESS DEVIATION, recorded: the 3.2-3.5 implementor pre-wrote three
catalog entries (autostash ownership, crash-window recognition, the
overwrite refusal — all marked provisional) and did the count-free
preamble rewording, against this phase's single-writer rule. They
stand (no concurrent catalog writer existed; reverting is churn);
Phase 7's single pass REVIEWS AND OWNS them — never duplicates them.

Catalog work:
- Preamble: state the subset law; replace every hard-coded count with
  counts-free wording (the provisional entries are listed, not
  counted) `[user — also now a global rule]`.
- Rewrites: escaping-symlink (5.2's refusal + flag);
  non-executable-hooks (1.4 refuse-both); the revert entry (single-form
  ruling, 2.4); the cherry-pick and merge entries (pipeline authorship,
  2.2/2.3); the moves entry (6.x); resolution-keywords flips
  provisional->deliberate and its text notes the extended overwrite
  protection (3.5); the autostash-exit entry (which IS the aftercare
  entry — one entry, divergences.md:424) generalizes to the family
  code; the rebase entry states the uniform git-authorship door and
  points at the deferred native reimplementation todo.
- New entries: safegit implements `switch`, not `checkout`, and file
  restoration is deliberately absent (2.6 — two entries replacing
  checkout's refusal list); mv refuses dirty moves (5.3, ours);
  `merge --autostash` refused as a dead flag (2.2); raw-git queues
  and exotic raw merge shapes refused at the continue commands (2.7);
  every remaining 2.6 allowlist refusal (octopus, strategies, switch's
  refused flags, reset pathspec/patch, rebase exec/apply-backend, pull
  --rebase, --skip, --edit, --squash, the rev-set spellings — one
  entry per refused capability class, each naming what git would do
  and why safegit refuses).
- Deletions: the timeout-override entry (mechanism deleted, 1.4); the
  mv-missing-directory entry (behavior now MATCHES git, 5.1 — the
  matches-git-means-no-entry rule `[user]`).
- Execution-flagged: the old "Conclusion commits are pipeline-authored;
  a queued sequence stays git-authored" entry still describes the
  deleted delegation — rewrite it to the 2.7 refusals (raw-git queues
  are concluded by git, named in the refusal). The Phase-2 audit's
  actively-false rows join the sweep: the revert entry's "ordinary
  guarded passthrough" fallback text, the three checkout-as-a-command
  mentions, and the merge-continue octopus text in the templates
  ("EVERY MERGE_HEAD line as parents, an octopus merge included" — now
  the opposite of shipped behavior).
- The entries 1.1's derivation falsifies (the dirty-tree scope entry's
  reset/bisect sentences; the reset-hard-only entry) update.
- Fate rule `[plan]`: rewritten entries carry deliberate status (they
  are rulings). Verify no entry was accidentally added for the
  unmerged-index refusal (it matches git and carries none).
Other docs:
- The machine-contract boundary paragraph into integration-guide.md's
  machine-mode section (after the parse-the-whole-stream sentence):
  the three failure shapes truthfully (payload-carrying nonzero;
  envelope with null payload; no envelope on paths that die before
  dispatch — a set 3.1 shrinks), payload-carries-aftercare, the 1.3
  UTF-8 limit. The sentence integration-guide.md:276 ("does not write
  a JSON error object ...") is REWRITTEN — 3.1's payload-carrying
  error envelopes falsify it as absolute.
- Every hand-written doc updated for phases 0-6 (the per-phase lists
  above), including the switch rename everywhere checkout appears; the
  guide GAINS symlink-policy text (none exists); the hook-row updates
  per 1.4's list; the app_version example becomes a placeholder; the
  restore preview's ls-remote stated (4.3); the VOCABULARY SWEEP:
  "rename" -> "move" everywhere except quotations of git's own terms.
- Execution-discovered rows: the commands guide's health-check table
  gains a `submodules` row (error severity, added by 2.8) and its
  `hook_perms` row corrects warn -> error (1.4's escalation;
  pre-existing drift). Also assess whether 2.8's fallback deletions
  warrant a divergences entry — the implementor judged NO under the
  no-git-facing-analogue clause (git has no scrub; the --3way retry
  was internal mechanics, not surface); confirm that judgment or add
  one entry covering the deletions.
- `--dump-schema` (commit via rlsbl commit) and bare `selfdoc gen`.
**Verify:** every named row resolved; regeneration clean; fresh
spot-checks against code; the catalog handed to the user for the
pre-release review.

## Phase 8 — Changelog

The edits BY ID: entry id
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
precedented), plus one edit dropping the tacked-on clause from
breaking entry id `18ce413b4e9236f3bcd5994bf6bb4dd2872e676e90d1ee92`.
Coverage: every campaign-2 commit (the uncovered set grows with this
plan's own commits) — feature-area user-facing entries for the new
behaviors and no-user-facing clusters; `--allow-batch` with reasons
where a legitimate area exceeds the limit; breaking-type entries for
the authorship restructure (merge/cherry-pick/pull now
pipeline-authored; multi-commit forms refused), the checkout-to-switch
rename, the allowlist refusals, the mv dirty-move refusal, the
non-executable-hook refusal, and the timeout-override removal. Phase
9's own commits are covered by a FINAL top-up pass at the end of
Phase 9 `[plan]`.
**Verify:** `rlsbl check --tag changelog` fully green (re-verified
after Phase 9's top-up).

## Phase 9 — Verification

- Full `-race` green; the stress run; GOWORK=off; gofmt; vet.
- Exit-sites census regenerated and committed (pre-empting the release
  hook), with its changelog line in the Phase-9 top-up.
- RECONCILIATION PROCEDURE `[plan]` (the script's --check is a strict
  diff and cannot serve): generate a fresh snapshot, diff test-lines
  against testdata/campaign2-baseline.txt, and CLASSIFY every
  differing line — healed red (cite the subphase), sanctioned rewrite
  (cite Appendix B), or new test — with ZERO unexplained lines;
  confirm the campaign-1 frozen artifact's reconciliation still holds
  the same way.
- One fresh audit per phase (0-8), each briefed with THIS PLAN and the
  phase's red list; remediation; then the Phase-8 top-up re-check.

## Phase 10 — Release

- The user's PRE-RELEASE DIVERGENCES REVIEW (Phase 7's deliverable) —
  any overturned entry is fixed before release.
- Triage: move to `todo/.done/` — this plan, the campaign-1 plan
  (`todo/redesign-campaign-plan.md`), and
  `todo/move-records-for-undeclared-moves.md` (consumed). Stay active:
  the ruling-await files (conditional-consequential, dry-run-network —
  whose stale doc claim the triage notes per todo immutability,
  exit-codes-registry, effects-handle-closed-method-set), the
  contingent files (reader-writer-operation-lock,
  scrub-strict-mode-selector, push-streaming-restoration),
  reversibility-gaps, and `todo/pipeline-authored-rebase.md`
  (deferred until after this campaign). Verify no move-record-reader
  todo survives anywhere (the filings were deleted/removed on order).
- `rlsbl release init`; the release file (minor bump; two-campaign
  description; context block); commit it; the single
  `rlsbl release run --no-allow-dirty --watch --approve-consequential`.
- Post-release notes (outside the release): fleet repos holding legacy
  no-op placeholder pre-pre-push hooks will hit the exit-24 refusal
  once the new version installs — sweep the fleet for
  `.git/hooks/pre-pre-push` files and run `hook migrate` (or delete
  the placeholder) in each; dependent projects' parked todos unblock
  on mv shipping (their own triage finds them); reset/rebase
  recovery's data precondition is met by 1.2.

---

## Dependency spine

| Phase | Depends on | Notes |
|---|---|---|
| 0 | — | 0.2 -> 6; 0.4 -> 3.2 |
| 1 | 0 | 1.1+1.2+1.3 ONE implementor (coord_cmd.go); 1.4+1.5 share doctor.go |
| 2 | 1.1, 1.2, 1.3 | replaces the merge/pull handlers and renames checkout's; 2.4 -> 3 (revert schema) |
| 3 | 0.4, 1.2, 2 | 3.2/3.6 fix ACTIONS ship in 4.2 |
| 4 | 1.5, 3.1 (payload), 3.2/3.6 (fix actions) | |
| 5 | 0 | before 6 |
| 6 | 0.2, 2, 3, 5 | |
| 7 | 1-6 | sole divergences writer; ends at the user's review |
| 8 | 7 | Phase 9 runs the final top-up |
| 9 | 8 | |
| 10 | 9 | |

Sequential execution is the default throughout.

## Appendix A — the red map

The spec suite is committed and deliberately red; the map below routes
each red file to its subphase. The single-authorship restructure
retires or rewrites the delegation-dependent entries as noted
(sanctioned; Appendix B).

| Red tests (file) | Subphase |
|---|---|
| grammar_reserved_keywords | 0.2 (subtests rewritten 6.4) |
| wave2_passthrough_oplog_positions | 1.2 (stay green through 2.2/2.5, incl. the op-name rule) |
| machine_contract_json_document | 1.3 (merge/pull rows also healed structurally by 2.2/2.5; the checkout row is a green forward-guard, renamed to switch with 2.6) |
| wave2_hook_timeout_override_dead | 1.4 |
| wave2_hook_nonexec_local_refusal | 1.4 |
| sequencer_conclusion_envelope | 3.1 |
| sequencer_stale_autostash | 3.2 (fix-half pin 4.2) |
| sequencer_conclusion_crash_window | 3.3 |
| sequencer_delegation_delete | RETIRED by 2.7 (delegation deleted); replaced by the raw-git-queue refusal pin; its `unmergedCount` helper relocates first |
| sequencer_conclusion_deletion_report | 3.4 |
| sequencer_conclusion_overwrite | 3.5 |
| commit_unmerged_index | 3.6 (guard half; repair pin 4.2) |
| undo_autobump_precheck | 3.7 |
| machine_contract_undo_effects | 4.1 (adjusted: preview carries the parent-bump record, execution order) |
| machine_contract_invisible_mutations | 4.2-4.4 |
| wave2_mv_missing_destdir | 5.1 |
| wave2_symlink_escape_refusal | 5.2 (plus its green control) |

Reds written inside their subphases: 0.3, 0.4, 1.1, 1.2 (the new
failure-entry pins and the switch-visibility pin), 1.5, 2.1 (both guard
halves), 2.2, 2.3, 2.4 (one-entry and range-refusal), 2.5, 2.6 (per
command, incl. switch), 2.7 (both refusals), 2.8 (each deletion), 3.1
(per aftercare shape), 3.5 (variants incl. the AUTO_MERGE-blob arm),
5.1 (flag), 5.2 (flag), 5.3, all of Phase 6.

## Appendix B — sanctioned rewrites (a break not listed here is a
plan defect)

- 1.1: none beyond stub hygiene (TestFlagConditionalEffects must STAY
  green — the reset row keeps `--soft` narrow; `--mixed` is the base).
- 1.2: TestBackupWritesOplogEntries (only if the tip-spelling
  consolidation proceeds); the TipSHA search tests.
- 1.4: TestSkipNonExecutable; TestSetOutputCapturesDiscoverWarning;
  TestHookListRendersStateAndOrigin's hook-run tail;
  TestDoctorExitCodeFollowsErrorFindings's warn fixture;
  TestParseTimeoutOverride (deleted);
  TestEnumerateSeesWhatDiscoverFilters (added during execution — its
  non-executable fixture is a refusal under the ruling; the Discover
  half adapts, the enumerator contract stays fully pinned); mechanical
  references to the renamed exit-25 constant in test files
  (compile-forced by the rename, no behavior change).
- 2.2 (merge restructure): pins asserting git authors the clean merge
  commit or asserting merge's passthrough shape; merge rows in the
  guarded-passthrough registries and classification pins; the octopus
  conclusion fixtures (`sequencer_conclusion_test.go:171` and `:245`
  invoke `safegit merge` with two branches / `--no-commit` — they
  switch to raw git so the surviving conclusion tests keep their
  subjects) — and the octopus-conclusion green itself
  (`sequencer_conclusion_test.go:146-195`) becomes 2.7's refusal pin.
  Added during execution: `TestPreviewRefusesWhatItCannotCompute`'s
  three MERGE rows (`-s`, `-X`, `--squash`) — 2.2 refuses those
  options outright, so the preview-specific refusal message is no
  longer producible for merge; the rows moved to
  `TestMergeSubsetRefusalsApplyToAPreviewToo` (refused, nothing
  recorded), the cherry-pick row stays exercising the criterion.
- 2.3 (cherry-pick restructure): pins asserting the clean pick is
  git-authored or not undoable; the multi-commit pick suites (become
  refusal pins); classification/registry rows.
- 2.4 (revert single-form): newQueuedPickRepo("revert") fixture
  (sequencer_delegation_test.go:88 — switches to raw git) and its
  tests; TestRevertFormsThatStayPassthroughs
  (revert_restructure_test.go:164-197);
  TestMultiCommitRevertStaysASequencerPassthrough (:134);
  sequencer_conflict_test.go:288-295;
  sequencer_preview_test.go:528-553 (multi-commit range preview);
  TestPickAndRevertContinuePassthroughsNameSafegitsCommand/revert;
  the multi-commit-no-records test (becomes a refusal pin).
  scrub_remap_test.go:403 STAYS GREEN (no remap change exists).
- 2.5 (pull restructure): pins asserting pull's passthrough-shaped
  output or git-authored merge results; pull's rows in the registries
  and classification pins; pull's oplog-key expectations (remote/
  branch keys reshape onto the baseline spelling).
- 2.6 (allowlist + switch rename): any existing test exercising a
  now-refused forwarded form through safegit is rewritten to an
  allowed form or becomes a refusal pin (raw-git fixture setup is
  unaffected — tests drive fixtures with git directly); every test,
  registry row, and doc invoking `safegit checkout` renames to
  `switch` (incl. the machine_contract checkout forward-guard row and
  the coord/classification registries).
- 2.7 (delegation deletion): the sequencer_delegation suites and the
  delegation-notice literal-sentence pins (the sentence's producer is
  deleted); sequencer_delegation_delete rewritten as the raw-git-queue
  refusal pin; delegated-payload schema tests deleted with the shape;
  `unmergedCount` (sequencer_delegation_test.go:109) RELOCATED to a
  surviving file before the deletion (commit_unmerged_index_test.go:48
  imports it). Added during execution:
  TestVerificationHoldsWithoutAnAutoMergeToReadFrom — its subject (a
  no-AUTO_MERGE merge verified anyway) is the exact shape 2.7 refuses;
  converted to the refusal pin (the cherry-pick reconstruction sibling
  is untouched — the refusal is merge-scoped);
  TestPickAndRevertContinuePassthroughsNameSafegitsCommand re-fixtured
  onto a single-form fixture (its old fixture was a queue, now a
  different claim); undo_foreign's delegated-conclusion test reshaped
  to drive the queue with raw git (its undo subject survives).
  Ratified execution decisions: coord.WayOutOf is queue- and
  octopus-aware (a refusal must name a way out that works); a
  forwarded `--continue` on merge/cherry-pick/revert is refused
  whenever ANY state is in flight — the clean-tree mid-queue
  `--continue` was the last route by which git could author commits
  behind a safegit command name (reproduced red-first; forced by the
  deliberate single-authorship ruling). The conclusion payloads'
  `head` and `commits_created` members are QUEUE members under the
  minus-the-queue-members rule and are DELETED now that one shape
  remains (2.1's rider executes the schema change). FOR THE PHASE 7
  REVIEW: the presentation of the 2.6 verdicts must include each
  command's ALLOWED set (the implementor-judged compute/message-draft
  entries — merge's -F/--log/--into-name/--stat/
  --allow-unrelated-histories/--rerere-autoupdate, cherry-pick's -x,
  -s/--signoff on pick/revert) alongside the refusal entries, and
  switch's DWIM reading (a remote-only branch name still creates and
  lands on a local branch; unresolvable arguments fall through to
  git's own error) is stated in its entry for the user's verdict.
- 2.1 (boundary guard, added during execution): test fixtures that
  built commits/merges through internal/git's own runner or drove
  `--continue` through RunPassthroughWithEnv moved to testutil.Git /
  testutil.GitTryEnv (internal/git/git_test.go — 11 calls;
  delegation_probe_test.go's two recorded-fact probes);
  gitexec_test.go's root-pin fixture argv became a suppressed form;
  TestValidateAcceptsDeclaredVerbs excludes Authors verbs (other side
  pinned); coord_cmd_test.go's runGitMutation argv list was already
  stale after 2.2-2.4 and now lists the real compute shapes plus the
  rebase door. RATIFIED: with NOTHING in flight, a forwarded
  `--continue` on merge/cherry-pick/revert is refused by the boundary
  (exit 1, its reason) instead of forwarding to git's own exit-128
  error — forced by the strict boundary (--continue authors; a
  carve-out to preserve git's error voice would admit exactly the
  authoring argv); divergences entry updated, reviewable at the
  catalog review.
- 2.8 (--hunks): pins of the 3way retry if any exist at the
  commit_hunks seams (verify; the stage package has none).
- 3.2: no rewrites (the fallback was never built; the probe confirmed
  the key).
- 3.x/4.x: exit_table_test regenerations;
  TestDirPinExemptionTableIsEnumerated (rows added in 4.1 and 4.3).
- 4.1: the undo_effects preview assertions extend to the parent-bump
  record in execution order (submodule fixture).
- 4.2: internal/lock/reclaim_test.go:58/85/109,
  internal/lock/naming_test.go:95/107,
  internal/index/index_test.go:77/112 (API reshapes); the stdout pins
  must stay green (concurrent_test.go:804, submodule_test.go:969-974,
  backup_test.go:385-390/:410).
- 5.1: TestMvCreatesTheDestinationDirectory;
  TestMvMovesRecordsAndCommitsInOneInvocation (mv_test.go:57); the
  scrub_moves fixtures moving into nonexistent directories.
- 5.2: TestCommitSymlinkEscapingTargetIsCommittedWithANotice.
- 5.3: any pin asserting mv proceeds over a dirty file (the dirty-move
  fixtures adjust to clean content or assert the refusal).
- 6.4: grammar_reserved_keywords' keyword-before-a-pair subtests
  (observed becomes valid).
- 6.6: assertNoRenameNotice deleted; moves_test/moves_cross_session
  headers re-pointed.
