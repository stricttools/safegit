# Redesign campaign execution log (append-only)

Purpose: this file records every ratified deviation from
`todo/redesign-campaign-plan.md`, every plan extension, everything
deliberately not done, and cross-phase notes produced during execution. The
plan file itself is never edited; where this log and the plan disagree, this
log records the as-built decision and the reason. Phase 10.2 auditors must
read this log alongside the plan: items listed here as ratified deviations
are deliberate, not audit failures. Entries are appended as phases complete;
existing entries are never rewritten.

## Ratified deviations from the plan text

- **0.1 baseline flags.** The plan says `go test ./internal/test/ -json`;
  the baseline artifact and `scripts/test-baseline` always pass `-short`
  (stress scenarios recorded as SKIP, matching what CI ran at capture time)
  and `-count=1` (so a cached result can never shape a baseline). Both
  recorded in the artifact header. Baseline at capture: 688 PASS / 67 FAIL /
  8 SKIP at HEAD b1d1385.
- **0.1 consolidation scope.** Helper consolidation was package-wide over
  `internal/test`, not limited to the seventeen investigation files — the
  plan's own collision counts only reconcile package-wide. The run-git
  group was 19 spellings, not the plan's ~17.
- **0.2 argv literals.** The plan named one `[]interface{}{"git", ...}`
  argv-literal site (`runGitMutation`); five existed (`push.go`'s
  `execGitPush`, `commit.go`'s `recordCommitRefUpdate`, four records in
  `scrub_preview.go`'s `recordHistoryRewrite`). All five routed through the
  boundary.
- **0.2 vocabulary table contains `git commit`.** Three `internal/git` unit
  tests build fixtures via `git.Run(ctx, "commit", ...)`. The verb is
  declared in the classification table with a note that safegit's own
  pipeline never invokes it. Declaring a superset is harmless; rewriting
  the fixtures would have made the table a policy statement the plan did
  not ask for.
- **0.3 rewritten red test.** `TestCoordSubdirScrubFromSubdirPreservesHistoryPaths`
  was unsatisfiable as originally committed (it demanded all three fixture
  paths in every commit, but the fixture builds them across three
  successive commits, so ancestor commits could never comply). Rewritten to
  capture each commit's path set before the scrub and assert per-commit set
  equality after, plus a history-length check and a scrub-effect content
  check. The independent 0.2–0.4 audit judged the rewrite faithful and
  stronger than the plan's required property (it also catches paths
  appearing). This is the only red test whose assertions were changed in
  Phase 0.
- **0.7 scaffold bases stay pristine.** The plan says the CI matrix
  absorption and the docker-workflow rename go into the `.rlsbl/bases/`
  scaffold bases (and the goreleaser change is mirrored there). That
  instruction is inverted with respect to how rlsbl's scaffold actually
  merges: rlsbl's three-way merge writes the new template whenever the
  working file equals the stored base (verified by reading rlsbl's
  scaffold implementation: when ours equals base, theirs is written, and
  the stored base is replaced by theirs after every apply). A customization
  copied into the base therefore gets overwritten by the next template
  update; customizations survive only in the generated file, with the base
  left as pristine template output — which is exactly how this repo's
  pre-existing customizations (the `GIT_CONFIG_*` block in ci-go.yml, the
  `main.version` ldflag in .goreleaser.yml) already survive. As built:
  `.github/workflows/ci-go.yml`, `.github/workflows/ci-docker.yml` and
  `.goreleaser.yml` changed; all files under `.rlsbl/bases/` untouched.
- **0.8 comparison key is raw start ticks, not a derived absolute time.**
  The plan says to convert `/proc/<pid>/stat` field 22 via boot time and
  clock ticks and compare the converted time. As built, the raw ticks value
  is recorded and compared; the converted wall-clock time is written only
  as an informational `started=` field, never compared. Reason, endorsed by
  the independent 0.8 audit as strictly better in both error directions:
  `/proc/stat`'s btime is wall-clock-derived, whole-second, and can shift
  between reads (NTP, suspend), so comparing converted times can falsely
  classify a live holder as a reused PID — the exact bug the subphase
  removes — and whole-second truncation can also collapse genuinely
  different start instants into a false match. Raw ticks are constant for a
  process's life. Residual risk (post-reboot same-PID-same-tick collision)
  errs toward never stealing, which is the fail-closed direction.

## Plan extensions (work done beyond the plan, reversible on request)

- **0.8 reclamation race fix.** The independent 0.8 audit found a remaining
  live-lock-steal path the plan does not cover: between one contender's
  staleness judgment and its `os.Remove`, another contender can reclaim the
  same stale lock and create its own fresh lock, which the first contender
  then removes unconditionally — mutual exclusion broken in post-crash
  contention. Fixed (as a marked extension) with reclamation performed only
  under an exclusive flock on the lock file plus an inode identity re-check
  before removal, with a deterministic regression test for the
  interleaving. Deliberately NOT added in the same pass: recording a boot
  id (residual collision already errs safe), and any change to
  `internal/index` tmp-dir garbage collection (the audit proved its
  PID-reuse exposure can only retain an orphan directory, never delete a
  live process's directory).
- **0.8 atomic lock publication (second steal path).** The reclamation
  contention test exposed a second pre-existing way a LIVE holder's lock
  could be destroyed: `tryCreate` created the lock file with O_CREAT|O_EXCL
  and wrote its content afterwards, so the file was observably zero-length
  for an instant, and the staleness rule "corrupt or zero-length means
  stale" condemned a freshly-created live lock. The flock+inode reclamation
  fix cannot catch it (same inode, just not yet written). Fixed by atomic
  publication: content is written to a temporary sibling and published via
  os.Link (atomic, EEXIST exactly like O_EXCL; pinned by tests, including
  that doctor's `.lock`-suffix walk cannot mistake the temp name for a
  lock). Same bug class as the reclamation TOCTOU; both are marked
  extensions, reversible on request.
- **0.7 `git.Version` helper** (single place that asks the git binary for
  its version, feeding the `internal/gitversion` floors and the doctor
  check) — not named by the plan; added to avoid duplicating the query at
  each future call site.
- **0.1 audit remediations** beyond the plan's text: `scripts/test-baseline`
  build-failure guard rewritten (the original `printf | grep -q` pipeline
  was defeated by SIGPIPE under pipefail); `testutil` runner family
  documents per-function stream contracts, SHA-returning readers are
  stdout-only (`testutil.GitOut` added), and SHA-feeding call sites in
  `root_commit_cas_test.go` re-pointed at stdout-only readers.

## Phase 1 closing-audit outcome

- Fully dynamic audit (quiescent tree): full -race suite green outside
  the 39 campaign reds (strict subset of the frozen baseline; 28 baseline
  reds healed by Phases 0-1), stress suite green post-backoff-fix,
  darwin vet clean, exit table fresh.
- Two bugs found and dispatched to a fixer: backup restore passed the
  safegit dir where coordGuard needs the git dir (silent wrong advice
  mid-sequencer); undo's auto-bump failure path os.Exit'd with both
  locks held (the one surviving leak site). Plus pinning gaps (exit-5 on
  amend/reword/undo refusals; dry-run-creates-no-lock-file; unlock's
  refs/* arm) and three stale test comments.
- Marked extensions dispatched with the same fixer: ref-lock timeouts
  from the commit pipeline unify onto exit 8 (previously exit 1 with the
  inconsistency honestly documented — the doc rewrites to the new
  truth); Release/ReleasePending gain an identity check before removal
  (path-based removal could delete a newcomer's lock after an
  operator-forced unlock).
- Known and accepted: the 1.4 refusal texts advise the Phase-6 continue
  commands before they exist — plan-mandated; nothing releases before
  Phase 6; TestMergeConflictTellsOperatorHowToConclude stays red until
  6.2 for exactly this reason. coord.InFlightError's typed identity is
  API for Phase 6.

## Ratified 1.4/1.5 decisions

- The in-flight refusal exit code is exitcode.CoordinationBusy (5), whose
  registry meaning already covered sequencer state. coord.Check's
  dirty-tree VERDICT deliberately ignores in-flight state (the guarded
  passthroughs are how an operator reaches `rebase --continue` /
  `merge --abort`; refusing on state alone would refuse the way out) —
  only the advice text switches. The sequencer declaration is verified,
  not trusted: wrong kind, or a declaration with nothing in flight, is
  itself a refusal.
- The operation-lock timeout reuses exit 8 (LockTimeout).
- Backoff-reset fix in the shared lock primitive (beyond the brief,
  ratified): waiters escalated to the 1s poll cap and never reset, so a
  rapidly-handed-over lock drained the queue at ~1 waiter/second — the
  stress suite went red at exactly the 30s timeouts. Reset on observing
  a different lock-file inode than the previous poll. Alternative if
  this ever needs revisiting: a shared/exclusive design so commits
  parallelize with each other and only exclude passthroughs (rejected
  now: needs a new primitive and flock leaves no on-disk record for
  unlock/doctor recovery).
- Same-worktree commits now serialize with each other (the plan's own
  consequence of commit taking the operation lock; the TOCTOU strictly
  needs only commit-vs-passthrough exclusion). Throughput measured fine
  post-backoff-fix (~30 commits/second sequential).
- die() now releases pending locks (a refusal no longer leaks the
  operation/ref lock file); three os.Exit sites in the commit path
  routed through die() for the same reason.
- unlock's ForceRelease stays an unconditional operator tool (staleness
  refusal still in front; last-resort recovery where flock cannot work);
  doctor's unattended sweep takes the strict reclamation authority.
- stdin probe result (replaces the plan's assumption): under the
  effects-handle passthroughs the child's fd 0 is /dev/null — stdin
  reads fail, but /dev/tty opens, so real editors work under
  `safegit rebase -i`. Documented in the concurrency guide.
- NOT covered by the operation lock, deliberately: backup restore's
  ff-only merge and the scrub/author rewrite commands. A scrub can run
  concurrently with a commit in the same worktree; Phase 4.1's
  under-rewrite-lock cleanliness re-check is the designed mitigation —
  its implementor should treat this as a live scenario.

## Ratified 1.3 decisions

- `git.SyncMainIndex`/`syncMainIndexInner` are DELETED, not just
  bypassed: after all four sites adopted ReconcileMainIndex they had zero
  callers, and keeping an exported softer reconciliation path would have
  contradicted the single-authority ruling. Scrub's
  SyncMainIndexWithWorktree is untouched (Phase 4 refactors it).
- `--index-info` probed facts (recorded so nobody re-derives them): git
  rejects `-z` with --index-info, so replay paths are unconditionally
  C-quoted with exactly-three-octal-digit escapes; the zero-mode removal
  line precedes each path's unmerged stage lines; removal of an absent
  path is a no-op, which makes staged-deletion lines safe; skip-worktree
  can only be set on stage-0-present paths (git fatals otherwise), so the
  restore is scoped to those — the scoping is forced, not softness.
- Oplog append order: at commit/amend/reword the append now precedes the
  index reconcile (a fatal reconcile leaves an undoable oplog entry); in
  undo it deliberately does NOT (hoisting it past the auto-bump would
  change when an undo entry exists on auto-bump failure). Asymmetry is
  deliberate.
- Semantics change: an index that is stale-EMPTY relative to the
  pre-operation tip now reads as staged deletions of every tip path
  (the faithful delta reading) where the old sync silently rebuilt it
  from the tip. No safegit-managed path produces that state.
- The pre-staged-removal change detector was rewritten AND renamed
  (TestCommitGitignoreOnlyKeepsPreStagedRemoval) — the sanctioned
  rewrite; protective intent inverted to guard the new behavior.

## Ratified 1.1 decisions

- The effects-handle exit-code verification (a recorded task in the plan):
  strictcli's Run with default check-on returns a plain formatted error and
  an UNSETTLED Completed (ExitCode() panics); the child's code is reachable
  only with Check(false) via Completed.ExitCode(). runGitMutation now uses
  Stream(true)+Check(false), returns the child's code, returns 0 on the
  dry-run branch before touching ExitCode(), and prints framework-level
  failures (previously discarded silently) mapping them to exitcode.General.
  Signature became a bare int (framework errors handled inside).
- 1.1 healed the index-sync doc claims in docs/commands-guide.md ahead of
  Phase 9 (seven per-command bullets plus intro sentences) because the 1.1
  change made them false and Appendix A does not list them. A prose note
  above the generated exit table states that guarded passthroughs exit
  with git's own code, which is not a registry constant (deliberately
  prose, not table rows, so the registration sweep stays meaningful).
- Cosmetic leftovers, deliberately untouched: stale explanatory text in
  the failure branches of two now-green investigation tests
  (undo_index_sync_test.go's historic-cause message;
  sequencer_conflict_test.go's RED/GREEN header). Phase 10's fresh audit
  should not read those as live claims.

## Deliberately not done (with reasons)

- **0.3/4.2 scrub mode-inference divergence left live.** After the 0.3 root
  pin, `scrub file`'s mode inference (`os.Stat` at the operator's cwd vs
  tree lookup at the repo root) is incoherent from a subdirectory: a
  root-level target scrubbed from a subdirectory silently DELETES the file
  from history (stat misses, mode becomes remove); a nested same-named file
  produces a loud hash error. Phase 4.2 deletes the inference entirely
  (required `--delete` / `--replace-with`), which is the designed fix; an
  interim anchoring of the stat would have re-reddened a red test and
  contradicted 4.2. The 4.2 implementor must treat this as live behavior,
  not hypothetical.
- **`IsStale`'s error return is now dead** (every path returns nil) and
  should collapse to a bool per the fleet dead-API rule; deferred while
  `doctor.go` and `internal/lock` were being edited concurrently, assigned
  to the Phase 0 remediation wave.
- **`internal/hooks` `TestRunTimeout` stays keyed to `-short`** (it is a
  ~1s timeout test, not a stress scenario). Consequence: CI, which no
  longer passes `-short`, newly executes it; bare `-short` runs still skip
  it, keeping the baseline SKIP set byte-identical.

## Notes for later phases

- **Phase 3.1:** `gitexec.ArgvAny` takes no context, so context-carried
  overrides (the object-directory quarantine) cannot reach the four
  effects-handle argv sites. Assess whether those argv (push, update-ref
  records) can ever write objects in preview mode; wire the quarantine
  through if so.
- **Phase 3.3 / 6:** the exemption table's `operator-cwd` reason text for
  the guarded-mutation entry under-describes its coverage (cherry-pick and
  revert DRY RUNS also route through it); queued for the Phase 0
  remediation wave, but keep the per-exec-site (not per-command) exemption
  model in mind.
- **Phase 4.2:** see the scrub mode-inference note above; also the
  submodule branch of `scrub file` already resolves its stat absolutely and
  the two branches are inconsistent until 4.2 unifies them.
- **Phase 4.3:** must KEEP the oplog `extra.reason` field (the plan already
  says so: metadata kept is op, reason, scope, counts). The end-to-end
  oplog no-cap test (`TestOplogAcceptsOversizedEntry`) uses a 20 KB scrub
  `--reason` as its carrier and needs a new carrier if reason ever moves.
- **Phase 6.1:** `gitversion.Require` has no production caller yet by
  design; it is the API the version-floor refusals call.
- **Phase 6, from the 1.2 sequencer probes (all assertion-backed in
  internal/sequencer's tests):** (a) an OCTOPUS merge writes no AUTO_MERGE
  at all — 6.3's "AUTO_MERGE absent and reconstruction impossible for a
  content conflict means hard-refuse" would fire on every conflicted
  octopus; the 6.3 implementor must either cover octopus via
  reconstruction or explicitly scope the refusal. (b) git's own
  post-commit cleanup can remove the ENTIRE sequencer state when a
  mid-sequence commit completes the last todo item — a queue can vanish
  out from under a conclusion flow. (c) MERGE_MODE is an EMPTY file for an
  ordinary conflicted merge ("no-ff" only with --no-ff) — presence checks
  work, content reads yield "". (d) sequencer.Cleanup on a cherry-pick or
  revert removes the sequencer queue directory, so it must only be called
  for SINGLE operations (6.2 already restricts native conclusion to
  single ops; documented on the function). (e) The reader reports
  KindAM for `git am` (shares .git/rebase-apply with the apply-backend
  rebase, separated by git's rebasing/applying marker files) — refusal
  texts must not advise `git rebase --continue` for an am. (f) The
  reader resolves coexisting stale markers by git's own probe order
  instead of erroring; the stale-AUTO_MERGE hard error remains Phase 6's
  own check at its entry. (g) SourceAuthor is a separate ctx-taking call,
  not a State field (the only fact needing a subprocess; refusal paths
  stay filesystem-only).
- **Phase 1.4:** the sequencer reader models merge/pick/revert/rebase/am
  only — NO bisect state (BISECT_START/BISECT_LOG) and no REBASE_HEAD;
  1.4's refusals must not assume bisect is covered by the reader (bisect
  already has its own coordination guard).
- **Phase 1.5 / 6:** the strictcli effects handle never assigns stdin to
  its children, so every runGitMutation passthrough (checkout, pull,
  merge, rebase, reset, bisect) runs with NO stdin, while
  runPassthrough (cherry-pick, revert) inherits it. Editors that open
  /dev/tty directly may still work; anything reading stdin does not.
  Pre-existing, surfaced by the wave-A audit. 1.5's "the lock is held
  through rebase -i's editor" note must be verified against this, and
  the effects-shapes todo for the framework should mention stdin.
- **Phase 3.3:** when declaring the proc-observe allowlist, ensure no
  allowlisted prefix can match a runGitMutation argv: strictcli's
  observe branch EXECUTES even in dry mode, and coord_cmd.go's dry-run
  guard keys off flags.dryRun (it would return 0 after a real
  execution). Add a regression assertion binding the allowlist to
  read-only prefixes; a carrier-based guard in runGitMutation is the
  structural alternative if strictcli exposes settled-ness.
- **Phase 6, additional from the wave-A audit:** the merge cleanup set
  omits MERGE_AUTOSTASH and MERGE_RR — a native conclusion of a
  `merge --autostash` would strand the autostash; 6.2 must either add
  them to the owned set (autostash needs APPLYING, not just deleting)
  or refuse autostash merges explicitly.
## Ratified Phase 8 decisions

- PushLeaseRejected = 41; a lease rejection (git's "(stale info)"
  marker, empirically recorded) is terminal, never retried as transport;
  the retry loop re-observes and re-pins per attempt, and a failed
  re-observation is a hard error, never a stale-pin reuse.
- execGitPush switched from streaming to capture (Check(false)) because
  classification needs git's stderr, which the framework error never
  carries. Consequences, accepted: push progress arrives at the end
  rather than live (framework has no tee — added to the upstream filing
  list), and git's stdout re-routes to stderr under --json so the
  envelope stays the sole stdout document (previously corruptible).
  backup backup shares the helper and inherits both.
- FINDING: isTransportError had been matching against the framework's
  error string, which cannot contain git's stderr — the push retry loop
  was DEAD until this phase. Now reads the captured stderr; documented
  in code.
- Consent: the force-push confirmation sits after remote-URL resolution
  and BEFORE ref observation (a declined force contacts nothing);
  guarded by forceFlag && !dryRun; answered by --approve-consequential
  (the condition is the flag the caller typed — the doctor-uninstall
  precedent); push does NOT declare WithConsequential.
- push gained a minimal payload schema (refs with per-ref lease values,
  atomic, hook run/skip facts, dry_run) — the honest carrier for 8.3
  since the envelope's preview member is framework-owned.
- Phase 9 rows: the "two conditions the framework cannot see" sentence
  in the _CLAUDE template (and generated CLAUDE.md) is now three
  (push --force-with-lease joins doctor uninstall and backup's
  public-remote check). Also: a --dry-run push still contacts the
  remote to observe (the deferred strictcli network-observe question;
  honest, pinned from real observations, stated in the doc row already
  queued).
- Upstream filing addition: strictcli Run streams or captures with no
  tee (the reason push output is now buffered).
- testdata/exit-sites.txt needs one regeneration after Phase 8's
  commits (queued for the Phase 4 implementor's closeout).

## Ratified Phase 3 decisions

- Quarantine INSTALLATION lives with the commands that write objects in
  preview (today: the commit pipeline), not dispatch-wide; the boundary
  ENFORCEMENT (preview + object-writing argv + no quarantine = hard
  error) is what makes any future escape loud instead of silent, and
  main's ctx marks preview for every command. Application is structural
  (one exec constructor), so all eight sites including the *WithDir
  variants are covered without per-site edits.
- The pipeline's effects threading is a required one-method RefUpdate
  port (production implementation IS the handle) rather than the
  *strictcli.Effects type: strictcli.Completed cannot be constructed
  outside the framework (settled-ness unexported, accessors panic), so
  a direct dependency would leave the pipeline's unit tests unable to
  supply a double. Nil port is a hard error, never a fallback.
- The observe allowlist admits only verbs that are observe-only WITH NO
  conditional effects (reflog/tag/notes/stash/hash-object excluded — a
  prefix ending at the verb would admit their mutating forms into
  dry-run execution). The runGitMutation dry-run guard keeps keying off
  flags.dryRun; its premise pin became TestObserveAllowlistCannotAdmitAMutation
  (every declared prefix observe-only per the table AND none matches a
  runGitMutation argv).
- ArgvAny needs no context: allowlisted observes write nothing by
  construction; non-allowlisted effects argv is recorded, never started,
  in preview. Documented and pinned.
- ExemptCommitRefUpdateRecord renamed ExemptCommitRefUpdate (the old ID
  named a deleted function and a false never-executed claim).
- scrub_preview's records were already honest — the sibling-rule
  deliverable became a pin, not a change.
- todo/effects-handle-commit-pipeline-and-method-set.md split by the
  orchestrator: pipeline item to .done (delivered by 3.3), method-set
  item continues as todo/effects-handle-closed-method-set.md.

- **Phase 6 upstream filing:** when filing the framework's dict-flag
  ValidateFn bug (planned in 6.2), the same todo or a sibling should
  also report: (a) the effects handle assigns no stdin to children
  (breaks interactive passthroughs); (b) Completed exposes no public
  settled-ness/executed-vs-recorded probe (all accessors panic when
  unsettled — which also makes Completed unconstructable in a consumer's
  own tests, forcing port-based indirection around the handle), and (c)
  the observe branch under dry-run is non-uniform
  (executes and returns settled normally, but returns an unsettled
  stale-brand carrier after a recorded mutation). Evidence pinned by
  TestSafegitDeclaresNoProcObserveAllowlist and the wave-A audit.
- **Phase 9:** internal/sequencer (and internal/exitcode, internal/gitexec,
  internal/gitversion, internal/procutil, internal/filelock) need rows in
  the architecture/package tables in the doc templates; the tables
  predate the campaign's new packages.
- **Phase 9:** the doc rows Appendix A assigns to 0.6/0.7 were left for
  Phase 9 as planned, EXCEPT rows already healed alongside code: 0.8's lock
  docs (architecture, concurrency-guide, _README template paragraph on lock
  staleness) and 0.6/0.7's stress/oplog lines in the _CLAUDE template.
  `scripts/test-baseline`'s header comment still gives the pre-0.7 reason
  for `-short` (stress scenarios), now stale; queued for the remediation
  wave.
- **Phase 10.2:** audit against the plan PLUS this log; per-phase auditors,
  one per phase, each briefed with its phase text, the relevant Appendix A
  rows, and this log.

## Corrections to earlier entries

- The 0.8 atomic-lock-publication entry above says the temp-name safety
  against doctor's `.lock`-suffix walk is "pinned by tests". Overstated:
  the behavior is correct by construction (temp names end in random
  digits, never `.lock`) and a test pins that no temp survives tryCreate,
  but no test asserts the doctor-walk property itself. Queued below.

## Brief additions for Phase 1.5 (from the remediation-wave audit)

- Route doctor's `removeStaleLocks` (and assess `unlock`'s ForceRelease)
  through the reclamation authority (`openForReclaim` + `reclaimLocked`)
  instead of bare judge-then-remove — the same TOCTOU class the Acquire
  fix closed; doctor can currently delete a live lock that was published
  in the window after its staleness judgment. `unlock` is an explicit
  operator-forced removal so a weaker stance may be acceptable there —
  decide and document.
- doctor's lock scan should also report (and `--action fix` clean)
  orphaned lock-publication temp files (`.<name>.lock.tmp-*`), which a
  SIGKILL between temp creation and publication can leave behind; they
  are invisible to the `.lock`-suffix walk today.

## Queued small items (dispatch at the next natural gap)

- `repo.SaveConfig` / `SaveConfigTo` still plain-write `config.json`
  (the race writeFileAtomic closed on Init). First check whether they
  have any production caller (config set mints through the effects
  handle); if dead, delete per the fleet dead-API rule; if alive, route
  through writeFileAtomic.
- `SharedSafegitDir`'s relative-answer join (against the git dir asked
  about, not the cwd) has zero test coverage; add a unit test, ideally
  covering a linked worktree.
- A test pinning that doctor's lock walk never treats a publication temp
  name as a lock (see the correction above).
- Boundary guard self-test lacks a `[]any{"git", ...}` spelling case
  (code handles it; unexercised).
- `internal/repo` parseInt uses fmt.Sscanf, which accepts trailing
  garbage ("5abc" sets 5) — tighten to strconv.Atoi semantics.

## Queued 0.5 polish (from the 0.5 audit; dispatch after Phase 1 wave A)

- Route `hook run`'s and `push`'s ensureInitialized failures to
  exitcode.NotInitialized (they exit General today, falsifying the
  constant's "every command that needs .git/safegit" doc); add `scrub
  verify` and `scan` to the producer lists they are missing from.
- Fix the false hand-written backup table row in docs/commands-guide.md
  ("0 | Success (including a declined confirmation)") — a declined
  confirmation exits nonzero (backup.go returns General), and the prose
  eleven lines above says so; the registration sweep cannot catch a
  wrong MEANING, only an unregistered number.
- Package-doc carve-out (coordinates with 1.1): passthrough commands
  propagate git's own exit code; the registry's "every numeric exit code
  safegit produces is a named constant here" claim needs that stated in
  internal/exitcode's package doc and the generated guide prose.
- TestAmendHunkSpecOnBinaryFileIsTyped: add the same stderr "binary
  file" text assertion its commit sibling has.
- exitcode's completeness test parses only the hardcoded "exitcode.go";
  parse the whole package so a constant added in another file cannot
  escape.
- Widen TestNoTestAssertsAnUnregisteredExitCode modestly (reversed
  operands, a few more variable spellings) and make its comment state
  the honest scope; document (or cheaply police) the two guard scope
  holes: strictcli.Exit(N) and Code: N composite literals.
- coordGuard's doc comment still says numeric "5/0/1" where constants
  exist.
- Small-items queue addition: internal/lock/cleanup.go's signal handler
  exits General; the Unix convention is 128+signum — decide and register
  when convenient. Also: 61 bare `return 0` in package main are
  unrouted through exitcode.OK (cosmetic; decide once, sweep or
  declare fine).

## Brief additions for Phase 1.4/1.5

- undo's `--count must be positive` validation exits exitcode.General;
  the registry defines post-parse argument validation as Usage. Route it
  while undo's refusals are in scope.

## Brief additions for Phase 5 (hooks subsystem)

- internal/hooks/hooks.go hardcodes `ExitCode: 21` in a HookResult,
  duplicating exitcode.PushHookTimeout by value with no registry
  reference — invisible to the exit-site guard (it is a result field,
  not an exit). Route it through the registry constant during the
  hooks rework.

## Ratified Phase 2 (P2c) decisions

- The commit request carries ExtraParents (parents beyond the branch
  tip), not the plan's literal whole-parents slice: the tip doubles as
  the CAS expected value and is re-resolved per attempt, so a caller
  cannot supply it. Phase 6.2 passes IndexBase: IndexBaseSharedIndex
  plus ExtraParents: the MERGE_HEAD list, and no positional files.
- Hooks run once per operation via once-guards INSIDE the CAS loop
  (literal hoisting would invert git's pre-commit-before-commit-msg
  order, since staging must re-run per attempt); the adopted commit-msg
  answer is reused across retries. commit-msg runs after the
  empty/no-match refusals (git's own order); it sees the user message
  plus user trailers, and safegit's session trailer is injected after,
  so a rewriting hook cannot strip it. Per-invocation message file under
  .git/safegit/tmp, never git's shared COMMIT_EDITMSG.
- All hook output routes to stderr (safegit's stdout is a structured
  channel: the JSON envelope, and the parent auto-bump parses a child's
  stdout). Exit 16 CommitHookRejected covers BOTH pre-commit and
  commit-msg refusals; pre-commit's failure code changed 1 -> 16 (no
  test pinned the old value).
- git.CommitMessage deleted (dead after amend reuses ParseCommit's
  message). CommitTreeWithAuthor unified into multi-parent CommitTree.
- Known nuance, accepted: a pre-commit hook that stages content
  DIFFERING from the working tree loses that staging on a CAS retry
  (the hook does not re-run; disk is re-staged). Formatter hooks
  rewrite disk, so real exposure is negligible.
- For Phase 5.5 (doctor): native-hook discovery reads <gitdir>/hooks
  directly — it honors neither core.hooksPath nor linked worktrees
  (where the gitdir is .git/worktrees/<name> with no hooks/).
  `git rev-parse --git-path hooks/<name>` fixes both in one call;
  pre-existing, now recorded.
- architecture.md's "standard git hooks run normally" sentence was
  pre-healed to the as-built truth (Phase 9 note).

## Ratified Phase 2 (P2a/P2b) decisions

- Exit 11 PathMatchedNothing (no-match arms; also reused for an untrack
  target absent from the parent). Exit 15 SymlinkHunkSpec (a symlink has
  nothing to split; 14 stays strictly "binary").
- Expansion never yields a gitlink; naming a gitlink is a single path.
  Amend's changed-path count measures against the replaced tip, not the
  parent. Nothing in reporting counts from arguments.
- --hunks names its own complete file selection; a path named both
  positionally and in --hunks, or twice in --hunks, is a hard error (no
  precedence rule). Positionals are always literal (colon-named files
  just work).
- --untrack uses git update-index --force-remove, NOT git rm --cached
  (rm --cached consults the REAL HEAD and refuses drifted content —
  wrong for cross-branch operations and for exactly the untrack shape).
  Directory --untrack expands tree paths under the prefix. Explicit
  positional+untrack conflict is a hard error; expansion-swept overlap
  resolves silently to the removal (mirrors the ignore rule).
- Escaping-symlink notices fire for expansion-discovered links too (a
  notice, not a refusal, so the explicitly-named carve-out does not
  apply).
- Note for Phase 3.2/8: the two new stderr notices (escaping link,
  non-ignored untrack) are emitted from internal/commit, which cannot
  see --quiet; if quiet-awareness is wanted they must ride the result
  structs. Stdout/machine mode unaffected.

## Phase 2 remediation ratifications

- The positional/--hunks contradiction is decided on CANONICAL paths in
  intake (single authority; main.go's parse is spelling-free); exit 2.
  Plain duplicate positionals still dedup silently (one statement made
  twice is not a contradiction).
- The untrack notice asks the IGNORE RULES (check-ignore --no-index),
  never the index; git.IsIgnored keeps index-aware semantics for the
  positional refusal and expansion filtering — two helpers, two
  documented questions.
- ORCHESTRATOR RULING (consistent with the ignore and untrack
  precedents — explicit beats expanded): a directory expansion that
  sweeps up a path carrying an explicit --hunks selection EXCLUDES that
  path from the sweep; the hunk selection stands. The hard-error rule
  covers explicit-vs-explicit only. Dispatched as a follow-up fix (the
  silent whole-file commit was verified live).

## Phase 2 closing-audit outcome

- Eight subphases PASS, 2.5 PARTIAL: the --untrack "not gitignored"
  notice fired backwards (check-ignore without --no-index reports
  index-present paths as not ignored; every untrack target is still
  indexed), and the positional/--hunks conflict check was
  spelling-based (./a.go vs a.go evaded it and the hunk entry was
  silently dropped). Both dispatched to a fixer along with two cheap
  pinning strengthenings.
- Plan-text inaccuracy noted: 2.8's Verify names
  TestMachineModeReachesEveryCommand for the commit payload; actual
  coverage is the dedicated commit_payload_test.go (correct and
  green) — Phase 10.2 auditors should not chase the plan's test name.
- Reword's changed-path list is empty by construction (commented), a
  literal-reading deviation from 2.8's "per commit/amend/reword" with
  no behavioral consequence. Ratified.
- More Phase 9 rows found outside Appendix A: docs/commands-guide.md
  ~:120 "Rename detection: safegit auto-stages the deletion" (false
  since 2.1); docs/architecture.md ~:185, :217, :221-222 still document
  the removed positional file:1,3 hunk grammar (row 4 covers only
  :234-242).

## Phase 9 additions (locking rework falsified these claims)

The atomic publication and flock-based reclamation invalidated every
`O_CREAT|O_EXCL` claim: `docs/architecture.md` (~:82, :133, :138, :355,
:360 — creation story AND the "kill(pid,0) then remove and retry" stale
story), `docs/concurrency-guide.md` (~:45), `docs/_CLAUDE.md` lock
convention line (and its generated CLAUDE.md copy), and the
selfdoc-generated `docs/internal-lock.md` (heals on the next selfdoc
gen; the in-code package doc is already correct). Also stale after the
Phase-1 fixes: docs/architecture.md (~:145, :163) describes lock release
as a plain unlink(2); release is now identity-checked (SameFile against
the publication-time identity; a force-released lock is skipped
silently). Phase 9 must also DOCUMENT two new environment constraints: lock acquisition now requires
hard-link support on the filesystem holding .git, and stale-lock
reclamation requires working flock(2) (without it, contenders time out
instead of reclaiming, and doctor is the recovery path).

## Open rulings (need the user's decision; as-built stands meanwhile)

- **Per-path vs set-level no-match errors (2.2).** The plan contradicts
  itself: 2.2 says a named path contributing nothing is a hard error
  naming the path (per-path), while 0.3's verify list requires tests
  asserting that an unchanged file named ALONGSIDE a changed one commits
  fine. As built: per-path (2.2's text), exit 11 PathMatchedNothing;
  four formerly-green tests rewritten to assert the refusal while
  preserving 0.3's cwd-resolution property. Consequence: `safegit
  commit -- a.go b.go` fails when one listed file is unchanged — a
  common agent workflow. Alternative: set-level ("at least one named
  path must contribute"), which satisfies every 2.2 Verify item, keeps
  0.3's tests green, and only refuses when the WHOLE named set
  contributes nothing. Orchestrator lean: keep per-path (hard-errors
  philosophy; catches an agent believing an edit happened when it did
  not). Reversal is one call site (the third refusal arm) plus four
  test assertions.

- **macOS in per-push CI.** `todo/.done/ci-macos-cost-reduction.md` records a
  completed, deliberate decision to remove `macos-latest` from per-push CI
  (it was ~92% of this repo's CI cost). The plan's 0.7 text reinstates the
  full linux+macos matrix on every push — and with `-short` dropped, macOS
  now runs the full integration suite per push. The implementor followed
  the plan; the plan silently reverses the earlier decision. As built the
  plan's form stands. Nothing is pushed until the release, so no cost is
  incurred yet — but this MUST be resolved before Phase 11. Options: keep
  the matrix (plan), restore ubuntu-only pushes (prior decision), or a
  middle form (macOS on release branches / manual dispatch only).

## Queued Phase 0 closeout items (from the 0.6/0.7 audit)

- `internal/repo` SetConfigValue parses the value as an int BEFORE the
  unknown-key check, so `config set log.maxSizeMB abc` reports an integer
  error instead of "unknown config key". Reorder: key validation first.
- `doctor.go` checkBypassDetect still returns a silent findingNone when
  `git.RevParse` fails on the ref the oplog names — the same
  silently-disabled-check shape 0.6 removed one branch earlier. Make it a
  reported failing finding. (The HeadRef early-out is a genuine
  precondition-absent case and stays findingNone.)
- `oplog.LastRefUpdateForSession` appears to have no production caller
  (tests only). Investigate: if session-scoped undo reads entries some
  other way, either wire the helper in or delete it per the fleet dead-API
  rule — decide from what undo actually needs, not from the helper's
  existence.
- `writeFileAtomic` does not fsync the file before rename (crash can leave
  a zero-length config.json on some filesystems). One-line durability fix,
  low priority.
- Informational, no action: the CI consolidation incidentally CLOSED a
  publish-workflow hole — previously two push-triggered workflows had
  same-named jobs and the CI-check aggregation's group-by-name kept only
  the newest of a pair, so one could be silently ignored; the three job
  names are now distinct and all must be green.

## Phase 9 checklist additions (false claims found OUTSIDE Appendix A rows)

Appendix A under-covers the 0.6 removals; Phase 9 must also heal:

- `docs/architecture.md:78` — "Lines longer than 4096 bytes are rejected"
  (false since 0.6; row 19 names only concurrency-guide.md:119).
- `docs/integration-guide.md:190` — prose "and oplog rotation" (rotation
  is deleted; row 24 names only the table rows).
- `docs/commands-guide.md:1112` — prose "oplog rotation size limits".
- `docs/commands-guide.md:660` — "Rotates the oplog if it exceeds the
  configured max size" (doctor no longer rotates anything).
- Also stale but harmless: `.rlsbl/config.json` `hooks.pre_release` still
  passes `-short` with the pre-0.7 rationale; `-short` now only skips the
  internal/hooks timeout test. Reassess the flag when Phase 9/10 touches
  the release path.

## Known flakes and environment facts

- `TestRootCommitConcurrentSafegitBothLand` races two safegit processes on
  an unborn ref and can spuriously FAIL under load; re-run before drawing
  conclusions from a one-line baseline diff.
- 12 files carry pre-existing gofmt drift under the installed Go 1.26.3
  (const-block alignment); implementors format their own edits only and
  never whole-file reformat.
- `internal/test`'s TestMain builds the safegit binary from the LIVE
  working tree, so during parallel implementor waves a concurrent agent's
  half-finished edits get compiled into the binary under test and produce
  transient failures in unrelated tests. Consequence: mid-wave test
  results are advisory only; every authoritative verification runs on a
  quiescent tree.
- The repo's gitignored `go.work` overlays a local strictcli checkout;
  Phase 10.1 mandates a `GOWORK=off` full run before release.
- The installed safegit binary (used by implementors to commit) predates
  the campaign: commits are made from the repo root with a single `-m` and
  plain file paths to stay off its known-buggy paths.
