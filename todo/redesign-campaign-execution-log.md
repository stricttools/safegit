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

## Phase 8 remediation ratifications

- All eight audit items done with red-first proof; the false-lease path
  from a failed observation reproduced exactly as predicted before the
  fix. remoteReadError routes ALL remote-observation failures (single
  and bulk) to exit 40 — one unreadable remote, one answer.
- The transport probe found two MORE dead patterns beyond the audit's
  list ("failed to connect" and "broken pipe" never matched curl's
  "Failed to connect to" / strerror's "Broken pipe" — case-sensitive
  Contains). Every pattern is now a multi-word phrase (ref names cannot
  contain spaces), attributed to its source layer, with the captured
  real samples living in the test fixture as the re-derivation
  authority. Two patterns (early EOF, remote end hung up) are marked as
  not-locally-probed.
- The local-refs-moved retry refusal exits 40 (same family as the two
  existing retry-window refusals) and is UNCONDITIONAL (a moved local
  ref is a problem whether or not hooks ran; the invariant is "the push
  set and the hooks' input are decided once").
- gitShim generalized to record full argv per interception (needed to
  assert re-pinned leases); PushFailed's Meaning rewritten (it fires
  before any push in the observation-failure arms) with the table
  regenerated.

## SESSION HANDOFF (2026-08-22, end of the first execution session)

The campaign paused here by the user's instruction; a later session
continues. Position and remaining work, for a reader with zero
conversation context:

**Done and audited:** Phases 0, 1, 2, 3, 4, 5, 6, 8 — each with a
closing audit and a remediation pass, all findings resolved. Phase 7
subphases 7.1 and 7.2 are DONE (ratified below) but NOT yet audited.
The small-items queue is drained except where noted below.

**Suite state at handoff:** fully green — roughly 1495 PASS / 0 FAIL /
8 SKIP (regenerate with scripts/test-baseline for the exact figure);
working tree clean; HEAD 7af45f6. The frozen testdata/campaign-baseline.txt
remains the ORIGINAL deliberately-red artifact from campaign start —
Phase 10.1 regenerates and reconciles against it; per-phase .local-only
snapshots are the working references.

**Remaining work, in order:**
1. Subphases 7.3 (safegit mv), 7.4 (inverse records on revert), 7.5
   (record-aware scrub) — one implementor; the 7.1 projection and
   encoder are their foundation; 7.5 may want a real --moved-retract
   flag (ratified note below).
2. Phase 7 closing audit (dynamic; covers 7.1/7.2 too) + remediation.
3. Phase 9 documentation healing: Appendix A PLUS every "Phase 9"
   entry scattered through this log (search "Phase 9" here — there are
   many additions beyond the appendix, including rows the appendix
   never had and rows recorded as pre-healed that need verify-only).
   Finish with --dump-schema (NOTE: it WRITES .strictcli/schema.json
   as a side effect) + bare selfdoc gen.
4. Phase 10: 10.1 full green incl. the stress run AND a GOWORK=off
   full run; baseline reconciliation; 10.2 fresh AUDIT protocol
   auditors — per phase, one auditor per phase, each briefed with its
   phase text, its Appendix A rows, AND THIS LOG (ratified deviations
   are not audit failures); 10.3 changelog coverage for the ~200+
   unreleased commits (rlsbl changelog add per commit group).
5. Phase 11: todo triage (the nine original todos move to .done after
   verification; the strictcli-await todos stay; the deferred Windows
   todo stays), release file, single release per the RLSBL protocol.

**OPEN USER RULINGS (both must be resolved before Phase 11; as-built
stands meanwhile):** the macOS-in-per-push-CI reversal, and the
per-path vs set-level no-match rule — see "Open rulings" below.

**Queued items for the next fixer window:** scrub match's nil-result
path (parent has nothing to rewrite but submodules did) still emits no
payload — same family as the fixed scrub file case; the confirmDeliberate
prompt goes to stdout unsuppressed by --quiet (pre-existing, shared
with doctor/backup, noted by the Phase 8 audit).

**Operational facts a future session needs:** subagents commit via the
INSTALLED safegit (single -m, plain paths, repo root); the
git-execution boundary guard scans GITIGNORED files (scratch repos
inside the repo trip it — use t.TempDir outside); --dump-schema writes
the tracked file; the schema's version field goes stale by one commit
immediately (release regenerates it); internal/test's TestMain compiles
the LIVE tree (mid-wave test results are advisory); known load flake
TestRootCommitConcurrentSafegitBothLand, and one unreproduced transient
in TestStagesReportsADeleteModifyConflictAsOneSided.

## Ratified 7.1/7.2 decisions (not yet audited)

- Exit 19 MoveNotBorneOut (new; NOT a reuse of 11 — a declaration
  stages nothing, its failure is a claim the world contradicts;
  declaration-vs-declaration nesting exits Usage). Records are
  caller content: they ride with user trailers BEFORE the commit-msg
  hook; the session trailer follows — with one honest exception, a
  kept-message amend appends after the existing block's session
  trailer (blocks are unordered; readers parse by key).
- The C-quoting encoder is local to internal/trailer (git's index-info
  quoting is unconditional with no decoder; only the escape SPELLING is
  shared). Decoder lenient for bare tokens (people type non-ASCII
  names); two unquoted separators refuse rather than guess.
- ULID: 48-bit UnixMilli + 80-bit crypto/rand, Crockford base32,
  degraded entropy is an ERROR (a colliding id would let a retraction
  hit the wrong record); id is a leading token in the value.
- Projection: applies only if the old path is in SOME parent tree AND
  the answer is in the commit's own tree (merges need no special
  case); longest match wins, ties by sorted answer; file-form records
  never speak for descendants; malformed lines surfaced, not dropped.
  A fixture vacuity (tree filter masking the ordering rule) was found
  by mutation probing and fixed.
- Third amend arm ratified: --amend --moved with no files and no -m
  rebuilds the tip's tree adding only the record. --moved judged
  against the FIRST PARENT of the replaced tip. "new present or
  staged" reads as on-disk-or-in-base-tree (the pipeline stages from
  disk, not the index). No --moved-retract flag yet (expressible via
  --trailer; 7.5 may want the real flag). Forward projection only (a
  query index is a campaign non-goal).
- Small-items closure: submodule scrub no-gitlink payload emitted
  (new_head stays ABSENT — echoing old_head would claim a rewrite);
  stopped-again delegation bumps the parent whenever the branch moved;
  the uninstall probe is the DISJUNCTION of both stores (never refuses
  where the old code proceeded); signal exits are 128+signum with the
  registry carve-out documented (the guard cannot see computed exits —
  stated there). Observation left open: uninstall from a linked
  worktree removes locks+hooks but leaves config/oplog while printing
  "uninstalled" — the removal set is a separate decision.

## Phase 6 remediation closure and small-item rulings

- All seven audit items done red-first; suite 1438 PASS / 0 FAIL / 8
  SKIP. Undo range validation is first-parent by definition (an
  all-parents walk would refuse every undo of a merge conclusion —
  pinned); the pin-mismatch second refusal fires only where the CAS
  would fail anyway; --bypass-session hint appears only when the
  foreign commit belongs to another session's oplog. git var declared
  ObserveOnly (the operator-identity source under dry-run).
  stopped_again required on both queueable payload shapes. The
  delegation-authorship note prints unconditionally on stderr with a
  note: prefix (survives --quiet and machine mode).
- NEW QUEUED ITEM: the stopped-again delegated path creates commits but
  never calls maybeAutoBumpParent — a submodule conclusion that stops
  mid-queue does not bump the parent gitlink. Next fixer window.
- SMALL-ITEM RULINGS: the lock-cleanup signal handler adopts 128+signum
  (the Unix convention), documented as a carve-out beside the
  git-passthrough carve-out, not a registry row. The 61 bare
  `return 0` sites are DECLARED FINE (success needs no constant; the
  guard's scope note already states the boundary honestly).

## Phase 6 closing-audit outcome and rulings

- Full suite 1424 PASS / 0 FAIL / 8 SKIP under -race; stress green;
  darwin vet clean; every frozen-baseline delta line reconciled (53
  removed-PASS all deliberate renames/deletions; zero unexplained).
- UNDO DETERMINATION (the open item, established live): --count 1
  refuses safely via the CAS pin (raw plumbing message, no guidance);
  --count N>1 SILENTLY DESTROYS interleaved git-authored commits at
  exit 0 (reproduced for plain git commits and delegated conclusions);
  --dry-run announces rollbacks the real run refuses (never consults
  the ref). ORCHESTRATOR RULING (per the audit's recommendation): undo
  validates the RANGE target..ref — every commit in it must be one the
  oplog says this undo reverses; a foreign commit in the range is a
  refusal naming it. One mechanism fixes the destruction, the message
  quality, and the preview lie. Delegated conclusions are invisible to
  undo (no ref key) — coherent, unchanged.
- ORCHESTRATOR RULING (B1, HIGH): the pick/revert marker differential
  adds the operation's INCOMING side to the base set (the source
  commit's blob; for a revert also the source's parent's blob) —
  completing the every-parent correction on the pick/revert half. A
  clean revert restoring marker-shaped documentation was refused with
  a false git-wrote-this attribution; reproduced twice, no test
  covered it.
- ORCHESTRATOR RULING (revert authorship, a plan correction): the
  plan's "author preserved from the source commit (pick/revert)"
  over-generalized. Cherry-pick KEEPS source-author preservation (the
  picked change is the original author's); REVERT conclusions author
  as the OPERATOR (a revert is the reverter's new change — git's own
  revert semantics, control-tested). Tests pinning the old behavior
  are sanctioned conversions.
- Also dispatched: --ff-only preview verdict precedes the conflict
  branch (git refuses outright; the preview said "resolve conflicts");
  a mid-queue delegation stop must report the commits it DID create
  (text notice and payload — currently payload null at exit 1); the
  delegation notice prints unconditionally like undo's partial-restore
  note (--quiet inconsistency); exit 17/18 registry text gains
  safegit revert as producer.
- The stale .strictcli/schema.json was regenerated by the audit's
  --dump-schema probe (side-effect write, not stdout) and committed by
  the orchestrator via rlsbl commit. Note for future sessions:
  --dump-schema WRITES the tracked file.

## Ratified 6.4/6.5/6.6 decisions

- Delegation reconciles via git.AdoptIndexFrom, NOT ReconcileMainIndex
  (probe-forced: git writes a mid-queue second conflict's stages into
  the COPY only, and the shared index still holds the already-resolved
  old conflict — a HEAD-based reconcile either resurrects the resolved
  conflict or hides the live one). Adoption is a DIFFERENT operation
  from reconciliation (the copy is authoritative because git advanced
  it), stated in its doc comment; ReconcileMainIndex remains the
  authority for its own job.
- Worktree materialization happens BEFORE the delegation (git's
  --continue compares the worktree against the index it is given;
  markers beside a resolved entry are "local changes would be
  overwritten"). Delegated payload: pipeline members ABSENT not null;
  queue_delegated/head/commits_created added; state_cleared READ from
  disk. Delegated oplog op is outside undoableOps; -m/--trailer/
  --dry-run refused for queued conclusions rather than dropped.
  Git's hooks on the delegated path probed and pinned (concluded step
  fires all four; sequencer-committed follow-ups fire only
  prepare-commit-msg and post-commit; no safegit trailers — output
  says so).
- refuseOwnedConclusion is a ratified plan extension: --continue on
  merge/pick/revert refuses whenever safegit owns the conclusion (read
  from coord.WayOutOf; rebase/am pass through) — the dirty-tree guard
  alone let a CLEAN-tree merge --continue produce a git-authored,
  trailer-less commit (reproduced live).
- Revert restructure gated by an explicit option allowlist; --edit and
  --no-commit and multi-commit stay passthrough. merge-tree argv
  verified: pick C = --merge-base=C^ HEAD C; revert C =
  --merge-base=C HEAD C^ (non-vacuity pinned). Preview replay order
  fixed (--no-walk=unsorted + range reversal for cherry-pick).
  Unsupported-preview-flag refusal exits 1 (same number the framework's
  own dry-run refusal uses — one situation, one number); the -X
  refusal reason is version-free (reversible if merge-tree -X at the
  2.38 floor is ever confirmed).
- OPEN DETERMINATION (assigned to the Phase 6 closing audit): what
  does safegit undo do when git-authored commits (passthrough pick,
  delegated conclusion) sit on top of the last oplog entry — refuse
  via CAS/tip arithmetic, or roll back and destroy the newer commits?
  Rule after facts are established.

## Ratified 6.3 decisions (two plan corrections, evidence-forced)

- LAYER INVERSION: the plan's region-primary/structural-secondary
  ordering was wrong — AUTO_MERGE holds the WHOLE file git wrote,
  including marker-shaped content copied through from a side, so
  treating all its regions as emitted refused a legitimate conclusion
  (a real defect found by the parental-content test). As built: the
  structural complete-block check plus the counting differential is THE
  VERDICT; region survival is ATTRIBUTION (message wording naming which
  blocks git itself created). The emitted set itself passes through the
  differential.
- NO hard refusal for absent AUTO_MERGE (the drafted exit 19 was
  removed before commit): with the differential the structural layer is
  a complete decision procedure — an absent AUTO_MERGE degrades only
  the MESSAGE, never the verdict, so the plan's "hard-refuse rather
  than verify less" premise does not hold (nothing is verified less);
  and the trigger is undetectable anyway (a legitimate -s resolve merge
  records no AUTO_MERGE and random temp-name labels — probe-verified).
- Differential base for non-conflicted paths is EVERY parent, not the
  first (a file arriving wholesale from the incoming side is absent
  from the first parent; first-parent-only refused importing a fixture
  carrying marker-shaped lines — the exact thing the differential
  permits).
- add/add is IN region coverage (git writes it to AUTO_MERGE normally —
  probe-corrected from the plan's exclusion list); only delete/modify,
  binary and custom-driver paths have nothing emitted.
- Exemption is the UNSET attribute form (-safegit-conflict-markers),
  resolved from the first parent via --attr-source: a typo can never
  silently exempt; every rejection prints the exact committed line
  needed. Exit 18 ConclusionMarkerSurvived (17 stays one situation).
- Worktree materialization is the LAST step of finishConclusion —
  reached only after a real commit, so never-on-refusal/never-in-dry-run
  is structural; mode-aware (exec bit, symlink, gitlink skipped);
  failure names the one path. Property matrix: 36 rows, labels via
  production calls for pick/revert and test-supplied for merge (the
  side that typed it), non-vacuity proven by label substitution.
- Known limit documented: CRLF checkouts' regions are found (trailing
  CR tolerated) but attribution stays byte-exact; filter+marker-content
  repos need the exemption.
- Facts for future sessions: the git-execution boundary guard scans the
  WHOLE tree including gitignored files (a scratch probe repo under
  build/ tripped it); one non-reproducible transient in
  TestStagesReportsADeleteModifyConflictAsOneSided (5x re-run green) —
  watch for recurrence. Small item queued: ApplyIndexEditsTo anchors
  with git.RepoRoot where AnchorRoot is the declared authority
  (identical under the pin; unify at the next fixer window).

## Ratified 6.2 decisions and the suite-green milestone

- THE SPECIFICATION SUITE IS FULLY GREEN as of 6.2 — zero campaign reds
  remain. Everything after this point is additive (6.3-6.6, Phase 7)
  plus docs/verification/release.
- Engine/pipeline seam: IndexEdits (Kind Blob|Worktree|Remove, no
  conflict vocabulary) applied inside tryCommit per CAS attempt — gains
  quarantine coverage, retry re-application, and never-touch-shared
  properties an engine-side pre-staged copy could not have. Keyword
  meaning decided ONCE in the engine against the shared index's stages.
- Reconcile ordering: cleanup -> apply edits to the shared index ->
  ReconcileMainIndex(firstParent, HEAD) — reconciling first would
  replay the just-resolved conflict (ReconcileMainIndex preserves
  unmerged stages by design). The pipeline's internal reconcile fires
  redundantly first; harmless under the lock, no suppression flag.
- Exit 17 ConclusionUnresolved (omission and stray are one situation);
  wrong-command and nothing-in-progress reuse 5; detached HEAD, queued
  refusal, empty pick/revert reuse 1; duplicate resolution 2.
- PLAN CORRECTIONS (probe-verified): git switch -c is REFUSED
  mid-operation — the working detached-HEAD remedy is git branch +
  git symbolic-ref (test executes it end-to-end and pins that switch -c
  still fails); a CONFLICTED octopus cannot exist (git's octopus aborts
  without parking MERGE_HEAD) — 6.3's octopus scope is empty for
  conflicts; only clean octopus conclusions occur.
- RE-RULING (was 6.2's flagged open decision): resolution keywords
  align with git's own idiom — ours/theirs write the resolved content
  to the WORKING TREE too (like checkout --ours/--theirs) and delete
  removes the file from disk (like git rm); worktree takes disk content
  by definition. 6.2 as built was index-only with a loud warning; the
  footgun (a later commit of the marker-carrying file) outweighs the
  conservatism. Dispatched with the 6.3 implementor.
- AUTO_MERGE-alongside-different-state is SUBSUMED by the wrong-command
  refusal (a standalone check would fire on every conflicted rebase,
  whose AUTO_MERGE is legitimate). Lone AUTO_MERGE mentioned
  informationally per the earlier ruling.
- Auto-bump wired into conclusions (a conclusion in a submodule moves
  the parent gitlink). announceWayOut prints for merge, cherry-pick and
  revert passthrough failures, unconditionally (the tail of a failure).
- The command-count number is deleted from the app description; the pin
  now asserts NO count is stated.
- Upstream todos FILED in the framework repo by the orchestrator: the
  dict-flag ValidateFn silent skip, and the five effects-Run gaps
  (stdin, settled-ness probe + Completed constructability, tee,
  observe-under-dry-run non-uniformity).

## Ratified 6.1 decisions and probe facts (binding on 6.2/6.3/6.4)

- BOTH delegation probes hold: git revert --continue AND git rebase
  --continue honor a substituted GIT_INDEX_FILE (commit the copy's
  resolution, advance/finish, remove their state, leave the shared
  index STALE — the 1.3 reconcile after delegation is mandatory). The
  future rebase extension is feasible; recorded in permanent probe
  tests.
- ORCHESTRATOR RULING (stale AUTO_MERGE): a normally-concluded REBASE
  leaves .git/AUTO_MERGE behind (git's own behavior, control-tested),
  so the plan's "stale AUTO_MERGE with no matching operation state at
  entry is a hard error" is wrong as written — it would fire on every
  post-rebase repo. As ruled: a LONE AUTO_MERGE (no operation state) is
  completed-operation residue — ignored by entry checks, mentioned
  informationally in the nothing-in-progress refusal; the hard error
  applies to AUTO_MERGE alongside a DIFFERENT operation's state.
- ORCHESTRATOR RULING (octopus): no AUTO_MERGE exists, index stages
  describe only the LAST pairwise step (stage 2 is an intermediate blob
  in no commit), marker labels are random temp file names — byte-exact
  reconstruction is impossible BY CONSTRUCTION. Octopus therefore joins
  6.3's own structurally-inapplicable list (delete/modify, add/add,
  binary): content-level region verification where the stages permit,
  structural layer + write-tree completeness always. The
  hard-refusal stays reserved for anomalous absence (a two-parent
  content conflict whose AUTO_MERGE should exist but does not).
- Label facts (git 2.54, recorded because nothing documents them):
  ours is always HEAD; merge base label is the abbreviated OID for one
  base, "merged common ancestors" for several, "empty tree" for none;
  cherry-pick theirs is "<abbrev> (<subject>)" with base "parent of
  <abbrev> (<subject>)"; revert swaps those. A MERGE's theirs label is
  the name the operator typed and is recorded NOWHERE machine-readable
  — 6.3's byte-identity property must supply it, read it from the
  marker line, or compare region content.
- merge-file's exit status IS the conflict count (success); only >=128
  is failure. merge-file classified MutatesWorktree (its bare form
  overwrites a file; safegit uses -p); check-attr and stripspace
  observe-only; --attr-source added to the value-taking globals.
- New package internal/conflict (resolver + reconstruction) — Phase 9
  package-table row, alongside the queue.
- Version floors wired at the structural chokepoints (CheckAttr with
  attr-source -> 2.40; AutoMergeTree -> 2.38, because on older git
  AUTO_MERGE is absent for EVERY merge and reporting "none" would
  silently downgrade verification).

## Phase 5 audit outcome, corrections, and rulings

- Audit PASS on all subphases; exactly the two named dead-code tests
  removed; the exit-sites artifact spot-verified fresh.
- CORRECTION to the earlier tracked-store ratification: the "a worktree
  writer could equally write the local store" rationale is over-broad —
  it conflates a local writer with REMOTE CONTENT materialized by
  clone/pull/checkout (git deliberately does not version .git/hooks so
  cloned content cannot execute; the tracked store re-opens that path).
  The defensible argument, verified as-built, is that pre-pre-push
  hooks execute only on push and hook run — an operator action with
  push intent; the residual exposure (clone, then push from that
  checkout, runs the repository's committed script) is real, narrow,
  and must be DOCUMENTED, not argued away. The ruling itself stands.
- ORCHESTRATOR RULING (D5): the LIVE hook store adopts
  repo.SharedSafegitDir — hooks are repo-level policy exactly like the
  locks that already share state across worktrees. hook
  install/migrate/remove/list, discovery, and doctor's hooks_migrated
  all key on the shared dir; the per-worktree store produced a new
  doctor false-negative (hooks_migrated OK from a linked worktree while
  pushes exit 24). The tracked store stays per-worktree by nature (it
  is checkout content).
- Dispatched to the fixer besides the ruling: the security-boundary
  sentence in code-level docs (Origin's doc comment carries the
  directory-not-tracked-ness rule; the "committed hook(s)" error text
  and hook list help stop implying git-tracked-ness; the
  commands-guide sentence saying legacy .d hooks "run before network
  I/O" — now actively false, they exit 24); exit 24's producer list
  gains hook remove; the internal/commit hooks package comment still
  says "run from .git/hooks"; strengthen the two flipped tests that
  retain the red-era tolerance branches; suppress hook remove's
  advisory line under --quiet; document in_git_dir as "found inside
  git's own state" (an escaping hooksPath path stays absolute); add
  the missing config/COMMIT_EDITMSG sweep test.

## Ratified Phase 5 decisions

- Exit codes 24 HooksNotMigrated, 25 TrackedHookNotExecutable, 50
  DoctorFindings (error-severity findings only; warnings never affect;
  post-fix the code reflects what the fix LEFT). TestDoctorReportsCorruptedOplog's
  exit assertion 0 -> 50 was forced by the ruling.
- submodule.DetectParent returns a Parent{GitDir, WorkTree,
  SubmodulePath} struct (the cascade needs the parent's work tree for
  the tracked store).
- hooks_migrated is an ERROR-severity doctor check with no RequiresInit
  (every push refuses while hooks sit in the legacy location, so doctor
  must not call the repo healthy) — an unmigrated repo exits 50 from
  diagnose.
- hook list prints the listing FIRST, then exits 24 on legacy hooks
  (the operator must see the files the refusal names).
- hook remove on a name in BOTH stores removes the live one and states
  the committed one still runs (refusing outright would make the live
  hook unremovable by name); a committed-only name is the explanatory
  hard error; both non-removals exit General.
- Tracked-store membership is the .safegit/hooks DIRECTORY on disk, not
  git-tracked-ness: an uncommitted hook there runs on the next push. A
  tracked-ness probe would make an uncommitted hook silently invisible,
  and a writer of the worktree could equally write the local store —
  no additional trust boundary exists to enforce.
- Scan sweeps all of .git/safegit except rewrite-maps.jsonl; git-dir
  matches carry in_git_dir with gitdir-relative coordinates; a
  core.hooksPath-escaping path stays absolute.
- Native-hook resolution is git.HooksDir (rev-parse --git-path hooks),
  shared by the commit family and doctor — one resolution, cannot
  disagree; core.hooksPath and linked worktrees now honored
  (red-proved both).
- Phase 9 additions from Phase 5: Appendix row 7 needs the two-store
  text; command tables lack hook migrate/hook remove; docs/cli-index.md
  says "31 commands" while the pinned app description says 33; scan's
  in_git_dir and doctor's new checks and exit 50 need sentences.

## Phase 4 remediation ratifications

- Tier A scope is WalkedTips: refs whose object the SHA map covers,
  plus annotated tags whose dereferenced target is in the map OR whose
  tag object this plan rewrote (a freshly rewritten annotation body
  must be Tier A verified — the ruling's necessary second clause).
  Tier B's residue check now names the refs holding each surviving
  match (one rev-list per ref, failure path only); an attribution
  failure is stated, never read as "no ref holds it". scrub verify
  shares the renderer without ref attribution (its question is
  whole-store).
- Rewrite locks: one acquireRewriteLock seam carries the ordering
  declaration (parent first, submodule second); both held through
  prepare and publish; the parent lock before submodule delegation is
  behind !dryRun (a preview takes no lock — the main path's own rule).
- foreignWorktreeState now refuses an empty baseline outright (the
  structural half) AND the call site dies on the RevParse error.
- sync_skipped is the disjunction over all published repos (no
  per-submodule payload records exist; the exit code already behaves
  this way). git.MkTree writes NUL-terminated records (mktree -z);
  pathological paths round-trip.
- Queued small item: scrub file into a submodule where no gitlink
  moved returns before flags.payload — an envelope with an absent
  payload under --json; same family as the companion-alone branch just
  pinned on the match side. Next fixer window.

## Phase 4 closing-audit outcome

- All four subphases PASS with independent hand-reproduction of the
  headline scenarios; the published silent-destruction class is
  structurally blocked (undeclared changes are unrepresentable past
  Tier A's intent validation).
- ORCHESTRATOR RULING (F1, HIGH): Tier A's pattern-absence check scans
  ONLY the tips the walk actually produced (rewritten tips plus refs in
  the SHA map) — the plan's "new commit set". A secret surviving on an
  off-walk ref (another branch, a stale remote-tracking ref) is Tier
  B's whole-store finding: the rewrite stands, exit 31, and the message
  names the off-walk refs. The as-audited behavior (hard exit-30
  refusal naming unwalked objects "of the rewritten history") was
  implementation overreach past the plan text; no test covered the
  scenario, which is how it survived. Test added with the fix.
- Dispatched to the fixer alongside: the submodule-path scrub file
  acquiring NO rewrite lock (parent lock before delegation plus the
  submodule's own inside, ordering documented); the discarded
  RevParse error that silently disables the submodule cleanliness check
  and journals an empty old_head (die like the siblings); the
  submodule-side skipped sync invisible to the payload; verify's
  AddAttribution routing around the scan selection authority; mktree
  fed non -z input (a path starting with a double quote would
  mis-parse — Tier A refuses rather than corrupts, but fix the
  encoding); the untested publishCompanionsAlone branch; two stale
  comments (rewrite_maps' old Finalize-order narrative; a cited test
  name that does not exist).

## Ratified Phase 4 (P4b: 4.3/4.4) decisions

- Stateless verify: input requirement is a PARSER-declared constraint
  (AtLeastOne of --pattern/recipe; --scope Requires --pattern — scope is
  a modifier on flag patterns only, recipe operations carry their own
  scope field; no invented glob-intersection rule). Payload records
  carry a source enum (flag/recipe) since both inputs may coexist.
  Verify deliberately keeps the whole-store scan (its question is "is
  this ANYWHERE"), not the new explicit-commit-set mode.
- Rotation notice rides infof (stdout, quiet/json-suppressed), matching
  the adjacent push hint; scrub file's re-check prints a
  fill-in-yourself placeholder regex (it names a path, not a pattern).
- 4.4: Finalize split into prepare (plan + Tier A + cleanliness) and
  publish; prepareAll/publishAll order submodule-first; the
  between-finalizes crash window leaves parent gitlinks PRUNED but
  journal-explainable (the submodule's completed journal maps every
  stale gitlink) — the plan's property, pinned as such after a stronger
  still-resolvable assertion failed. Crash injection keys the gitShim
  on GIT_DIR presence to address each repo's calls exactly.
- The 4.4 tests are structurally red against the old ordering (a
  pre-restructure binary would need a worktree, banned here); the
  submodule-journal-empty-at-parent-commit-tree assertion is the
  discriminator.
- ORCHESTRATOR RULING for Phase 5.5's doctor work: doctor diagnose
  exits a NEW registered code when at least one error-severity finding
  FAILS (warn findings never affect exit); after --action fix, error
  findings that remain unfixed keep the nonzero. Pre-existing weakness
  surfaced by 4.3: an error-severity finding (a secret-bearing legacy
  policy file) currently exits 0.

## Ratified Phase 4 (P4a: 4.1/4.2) decisions

- Exit 30 RewriteRefused (Tier A, nothing moved) and 31
  RewriteIncomplete (rewrite stands; residue or skipped sync). Tier B
  never aborts: findings accumulate, print as CRITICAL, and map to 31.
  TestScrubMatchStashWarning's exit changed 1 -> 31 (semantics
  unchanged); TestFinalizeWritesStartRecordBeforeVerifyFailure's body
  rewritten for the reorder (all three journal records now exist when
  Tier B fails; the nothing-before-refs half moved to a Tier A test).
- The Tier A cleanliness check compares against the PRE-REWRITE head
  using git status's re-hashing (diff-index alone trusts a stat cache
  the pipeline never writes and reported everything modified — recorded
  so nobody re-derives it), plus cached diff against the baseline and
  untracked files; submodule state excluded (--ignore-submodules=all; a
  submodule scrub legitimately moves the gitlink mid-flight).
- Tier A's exact-content check skips a target also covered by a
  --remap-shas-in glob (the remap edits the file after replacement);
  the no-old-blob half still applies.
- The path-taking git.HashObject/HashObjectWrite are DELETED (they
  resolve against the pinned root — the exact divergence hazard);
  bytes-taking forms replace them. sync_skipped added to the four
  rewrite payloads (was written, never readable). Submodule --from
  resolution uses <rev>^{commit} (bare rev-parse echoes any 40-hex
  string back).
- The skipped-sync race window is tested deterministically via git's
  reference-transaction hook (fires exactly between Tier A and the
  sync).
- 4.4 seam prepared: planRefUpdates / verifyIntendedChanges / Tier A
  hook / applyRefUpdates are separate ctx-parameterized functions.

## Phase 8 audit outcome and dispatched fixes

- Audit PASS on all three subphases; the adversarial sweep found no
  surviving bare-lease path and no --force producer at all.
- Dispatched to a fixer (after the scrub implementor frees the exit
  table): getRemoteSHA must propagate ls-remote failures instead of
  answering "absent" (silent degradation that since 8.1 becomes a false
  must-not-exist lease and a wrong terminal diagnosis); a test for the
  revived retry loop (transport failure then success via the git shim,
  asserting two attempts and a re-pinned lease); anchor the lease
  classifier to "(stale info)" and force mode; tighten transport
  patterns that could match ref names (bare EOF/SSL/TLS/transport);
  one authority for --atomic (payload and argv derive from one place);
  ORCHESTRATOR RULING: backup's concurrent-slot lease rejection
  classifies to exit 41 like push (one meaning per code; BackupDiverged
  stays the ancestry refusal) and the PushLeaseRejected/PushFailed doc
  comments update to the truth (40 also covers failed re-observation);
  RULING for the hook/retry window: if a retry's re-resolution finds
  LOCAL ref SHAs differing from what the pre-pre-push hooks saw, the
  retry is REFUSED with a clear message (fail closed; no hook re-run
  machinery); stale comments (confirm_deliberate's "only two sites",
  ExemptGitPush's reason, hookSkipDryRun's three-readers claim,
  classification_test's push row); a one-line comment making the
  disabled-beats-dry-run payload precedence deliberate.
- Phase 9 rows recorded by the audit: architecture's push flow (consent
  first, ls-remote before hooks, pinned per-ref lease + atomic);
  "oplog records each attempt" is false (one append after success);
  push output now BUFFERED (and git stdout to stderr under --json) —
  needs a user-facing sentence; row 37 largely pre-healed (verify, not
  rewrite); the consent-seam and template bullets already queued.

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

- doctor --action uninstall from an UNINITIALIZED linked worktree
  refuses ("nothing to remove") even when the repository has shared
  state — the initialized probe keys on the per-worktree dir while
  uninstall now removes the shared store. Decide: uninstall is a
  repository operation; key its probe on the shared dir. (Surfaced by
  the Phase 5 remediation; pre-existing.)

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

## USER RULINGS (settled 2026-08-22, review session)

- macOS CI: release-only — macOS runs where it must be green (the
  release-candidate push on main) via a branch/dispatch condition, not
  on every push. Supersedes both the plan's matrix and the as-built
  workflow; the workflow edit is next-session work before Phase 11.
- No-match commit errors: PER-PATH STANDS, with a refinement the user
  specified: the refusal must AGGREGATE and name every argument that
  contributed nothing (today unmatchedSource reports only the first).
  Small fix queued for the next fixer window.
- Tracked hook store: accepted as documented (push-intent boundary;
  the blunt clone-then-push sentence stays).

## USER RULINGS (settled 2026-08-22, review session, round 2)

- Scrub secret-scope: the recommended subset — behavior stays
  complete-and-name-refs, PLUS the unconditional scope line ("rewrote
  the history of <ref>; other refs were not rewritten", beside the
  rotation notice) queued for the next fixer window. The two-mode
  required selector is deferred to todo/scrub-strict-mode-selector.md
  ("way later", user's words).
- Doctor exit 50: KEPT. Ships as a breaking-type changelog entry
  (previously always exit 0).
- NEW STANDING DELIVERABLE (user decision): a single living document in
  docs/ cataloging EVERY instance where safegit's design philosophy and
  git's idioms clash, and which way each ruling went — because some
  campaign rulings deliberately went git-like (muscle-memory
  preservation) where the user might have ruled stricter, and the user
  wants to review the whole class systematically. safegit's CLAUDE.md
  (via the docs/_CLAUDE.md template) must mention the file so every
  future agent keeps it updated. Slotted into Phase 9 (next session):
  an agent mines the codebase for divergences and drafts the document;
  known entries to seed the mining: resolution keywords writing the
  worktree incl. delete-removes-disk (git-like; PROVISIONALLY as-built,
  awaiting the user's review via this document — the direct question
  was deliberately left unanswered); revert authorship = operator
  (git-like); lone-AUTO_MERGE tolerance (git-like); empty merges need
  no flag (git-like); repeated -m joining (git-like); commit-msg hook
  ordering and message-file isolation (mixed); hooks output on stderr
  (ours); untrack keeps the file on disk (ours); no --force anywhere,
  qualified flag names, required selectors, exit-code registry,
  consent seams (ours); passthrough exit codes = git's own (git-like);
  --resolve paths repo-relative while commit positionals are
  cwd-relative (ours/asymmetric); conclusion trailers and pipeline
  authorship vs git's own committing (ours).

## USER RULINGS (settled 2026-08-22, review session, round 3)

- Commit serialization: accepted as built. The reader-writer
  alternative is filed as todo/reader-writer-operation-lock.md,
  explicitly CONTINGENT on measured proof of real contention (the
  user's words: "proof is needed") — it is not general backlog.
- Oplog unbounded growth: accepted. Append-only audit trails are
  complete by design; any future management must be an explicit
  operator-invoked archival, never silent rotation.
- Uninstall: BOTH halves — it becomes a repository-wide operation
  (removes every worktree's state dir plus the shared store) AND the
  output, dry runs included, clearly enumerates what is about to be
  removed and what may be surprising (e.g. that state for worktrees
  other than the invoking one is included). Queued for the next
  session's fixer window alongside the aggregated no-match listing and
  the scrub scope line.

## USER RULINGS (settled 2026-08-22, review session, round 4 — ASKME complete)

- Push buffering: accepted for the release; everything else parked in
  todo/push-streaming-restoration.md (tee-shipped check, streaming
  restoration, --porcelain lease detection, the heartbeat question,
  and the full alternatives record with pros and cons).
- Consent prompts move to STDERR (all three confirmDeliberate
  consumers; --quiet still never suppresses a prompt) — queued for the
  next-session fixer window. Additionally, by user direction, a
  framework todo was filed proposing authoritative channel management
  (prompts/notices/results primitives, a documented stdout/stderr
  contract): strictcli's todo/channel-management-authority.md.
- Pre-commit exit 16 KEPT; ships with a breaking-type changelog entry
  naming the 1-to-16 change.

THE ASKME REVIEW IS COMPLETE. Every open ruling in this log is now
settled except items that are explicitly parked as contingent or
way-later todos. The next session's fixer-window queue, consolidated:
aggregated no-match listing; scrub scope line; repository-wide
uninstall with enumerating output (dry runs included); consent prompts
to stderr; scrub match's nil-result payload gap.

## USER RULINGS (settled 2026-08-22, review session, round 5 — loose-end review)

- The divergence-document review happens BEFORE the release: Phase 9
  drafts it, the user reviews it as part of the release gate, and any
  overturned git-like ruling is fixed pre-release.
- Changelog granularity: feature-area entries with --allow-batch and
  reasons (likely 15-25 user-facing entries); internal clusters as
  no-user-facing entries.
- The move-records todo was SPLIT three ways by the orchestrator:
  delivered parts to .done, the 7.3-7.5/docs rump stays active as
  todo/record-file-moves-remaining.md, and the superseded blob-pairing
  mechanism to .obsolete (blob equality never decides — the declared
  --moved ruling replaced it).
- macOS trigger mechanism: still open — the user asked for the option-2
  cost estimate (answered: ~an hour plus re-auditing both CI gates'
  job-name matching; recommendation remains the zero-mechanism plain
  push trigger, release-only de facto under never-push-between-releases).

## USER RULINGS (settled 2026-08-22, review session, round 6)

- Phase 7.5 additionally builds a real `--moved-retract <id>` flag with
  existence validation (refuses a typo'd/nonexistent id loudly); the
  trailer spelling stays as the open-convention escape hatch.
- Upstream todos (framework and release tooling) wait for those
  projects' natural triage; safegit's tee-dependent follow-ups stay
  parked accordingly.

## USER RULINGS (settled 2026-08-22, review session, round 7)

- macOS CI mechanism: PLAIN PUSH TRIGGER — macOS stays in the
  push-triggered matrix with zero conditions; release-only de facto
  under never-push-between-releases. No workflow edit needed at all:
  the as-built matrix already satisfies this ruling, so the earlier
  release-only ruling is implemented by DOING NOTHING to ci-go.yml.
- The escalation rule (user-shaped subagent decisions go to open
  rulings, ratification only for internals) was adopted into the
  GLOBAL rules file by the user; wording added to
  /home/m/Projects/CLAUDE.md.

## Open rulings (RESOLVED above — section retained for history)

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
