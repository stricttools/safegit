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

## SESSION HANDOFF ADDENDUM (2026-08-22, after the review session — SUPERSEDES STALE PARTS OF THE HANDOFF ABOVE)

Read this WITH the handoff above; where they disagree, this addendum
wins. A full review session with the user followed the handoff and
settled everything; the corrections:

- **NO open user rulings remain.** The handoff's "OPEN USER RULINGS"
  paragraph is obsolete: macOS CI is settled as the PLAIN PUSH TRIGGER
  (the as-built matrix stands; NO workflow edit — round 7 supersedes
  round 1's branch/dispatch wording), and no-match is settled as
  PER-PATH plus an aggregated all-unchanged-files listing (round 2).
  Every ruling in the ten "USER RULINGS" round sections below is final.
- **Every historical queue in this file is DRAINED** — "Queued small
  items", "Queued 0.5 polish", "Brief additions for Phase 1.4/1.5",
  "Brief additions for Phase 1.5", "Brief additions for Phase 5", and
  "Queued Phase 0 closeout items" were all executed during the
  session. Do not re-dispatch them. The ONLY live queue is below.
- **The next session's queue, consolidated and final:**
  1. Housekeeping: saferm the build/audit residue tree and the
     pre-campaign stray repo under the system temp dir.
  2. Fixer window: aggregated no-match listing (all unchanged files in
     one refusal); scrub scope line; repository-wide uninstall with
     enumerating output, dry runs included; consent prompts to stderr;
     scrub match's nil-result payload gap; the exit-sites regeneration
     line added to the release checklist (candidate:
     .rlsbl/hooks/pre-checks.sh).
  3. Subphases 7.3/7.4/7.5 — 7.5 ALSO builds the real
     --moved-retract flag with existence validation (round 6; the
     handoff's "may want" is superseded).
  4. Phase 7 closing audit (covers 7.1/7.2 too) + remediation.
  5. Phase 9: the checklist (Appendix A + every Phase-9 row in this
     log) FIRST, then a FULL fresh-read of every hand-written doc
     line-by-line against the as-built tool (round 8); the
     PHILOSOPHY-VS-GIT-IDIOMS DIVERGENCE DOCUMENT in docs/ (round 2 —
     seeded entry list in round 2's section; mentioned from the
     docs/_CLAUDE.md template) — the user reviews it as a PRE-RELEASE
     GATE and may overturn git-like rulings (round 5); finish with
     --dump-schema + bare selfdoc gen.
  6. Phase 10: 10.1 as in the handoff PLUS one mechanical gofmt
     normalization commit on the quiescent tree (round 9; the
     four-files figure is current — the facts section's "12 files" is
     the stale original count) PLUS the flake investigation
     (stress-loop both watchlist tests to fix-or-certify, round 9);
     10.2 per-phase fresh auditors briefed on plan + this log; 10.3
     changelog as FEATURE-AREA entries with --allow-batch reasons
     (round 5), including breaking-type entries for pre-commit 1->16,
     doctor 0->50, and the buffered-push behavior.
  7. Phase 11: triage — originals to .done after verification; the
     move-records todo is ALREADY split (round 5: .done + active rump
     todo/record-file-moves-remaining.md + .obsolete); the plan file
     and this log BOTH move to .done at triage (round 9; no pointer
     banner is added to the plan meanwhile); then the release per the
     RLSBL protocol.
- **Deferred/contingent todos created by the review** (do not work
  without their stated preconditions): todo/scrub-strict-mode-selector.md
  (way later), todo/reader-writer-operation-lock.md (contingent on
  measured contention proof), todo/push-streaming-restoration.md
  (parked on the framework's tee).
- **File-reading note:** this log is append-only and NOT chronological
  (sections were inserted at markers as phases completed); the ten
  "USER RULINGS" rounds plus this addendum are the current layer. The
  round-10 residue ruling is recorded inside the round-9 section
  (labeled; placement quirk only).

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

## USER RULINGS (settled 2026-08-22, review session, round 8)

- Both operational facts adopted into the GLOBAL rules file
  (/home/m/Projects/CLAUDE.md): live-workspace harness results are
  advisory mid-wave; guard-triggering scratch goes outside the repo as
  an interim rule. The structural fix — a scaffold-owned experiments
  directory convention — is filed as an rlsbl todo; when it lands,
  safegit's boundary guard adds the one skip-list entry and
  experiments return in-project.
- Phase 9 does the FULL fresh-read: checklist (Appendix A + this log's
  rows) first, then a fresh-eyes agent reads every hand-written doc
  line-by-line against the as-built tool.
- testdata/exit-sites.txt: kept as-is, regenerated at releases only —
  next session adds the one-line regeneration to the release checklist
  (candidate: .rlsbl/hooks/pre-checks.sh) in the fixer window; between
  releases its line numbers are known-stale and untrusted.
- The aggregated no-match refusal's exact rendering (which unchanged
  files, one per line) is implementor-level detail; no further ruling
  needed beyond the round-2 aggregation ruling.

## USER RULINGS (settled 2026-08-22, review session, round 9)

- gofmt: one mechanical normalization commit in Phase 10 on the
  quiescent tree; the never-whole-file caveat retires with it.
- Flakes: a dedicated Phase 10 investigation stress-loops both
  watchlist tests to either fix a real race or certify load-sensitivity
  with the mechanism named in a comment; zero unexplained
  nondeterminism ships.
- Plan-file pointer: NONE — the user's call: the plan (and this log)
  move to todo/.done/ at Phase 11's triage anyway; auditor briefs
  remain the mechanism that pairs plan with log until then.
- The experiments-directory usage rule was added to the global file at
  the user's order: raw artifacts stay ignored and disposable;
  reusable value is promoted as generator scripts to committed homes;
  the test suite is where findings become permanent.
- build/audit residue: SETTLED (round 10) — plain saferm cleanup as
  the next session's first housekeeping act (the build/audit tree and
  the pre-campaign stray under the system temp dir); no salvage
  scripts (the pinned tests are the permanent form). THE ASKME REVIEW
  OF THIS SESSION'S LOOSE ENDS IS NOW FULLY COMPLETE — every ruling in
  this log is settled; nothing awaits the user before the next session
  begins its queue.

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

## Fixer-window closure (2026-08-22, resumed session — the six addendum items)

- All six items done red-first; suite 1511 PASS / 0 FAIL / 7 SKIP on the
  quiescent tree. The 8-vs-7 SKIP delta against the handoff figure is
  flag-explained: the handoff counted a `-short` run (scripts/test-baseline
  passes `-short`), which additionally skips internal/hooks' ~1s timeout
  test; a bare `-race` run executes it.
- Ratified: uninstall's worktree enumeration lists `<common>/worktrees/*`
  on disk (labels read from each git dir's gitdir marker) instead of
  `git worktree list` — the removal targets are git dirs, the disk set is
  exactly git's own layout (the dependency repo.SharedGitDir already
  documents), and it also removes the state of a deleted-but-unpruned
  worktree that a list-driven removal would strand.
- Ratified: the payload-gap fix covers scrub match's AND scrub run's
  nothing-matched early returns too (an absent payload there contradicted
  the one-envelope invariant the item exists to restore); scrub file's
  no-target case is already a hard error, no gap.
- Ratified: dry-run uninstall no longer prints "safegit uninstalled"
  (hook.go's established silent/dryRun output pattern).
- FIXED AS A SIDE EFFECT, changelog fix entry required at 10.3:
  `--dry-run doctor --action uninstall` was DESTRUCTIVE (removed state
  while claiming preview) until removal was routed through the effects
  handle. Uninstall's enumeration prints via outf before the confirmation,
  so --quiet does not hide what is about to be removed.
- Dispatched with the Phase 7 implementor (an application of the round-2
  scope/rotation ruling, not a new ruling): the submodule-only scrub match
  completion prints no human summary, scope line, or rotation notice — a
  successful rewrite completion must print them.
- Queued for the Phase 7 closing-audit remediation: the vacuous
  precondition in TestUninstallFromUninitializedLinkedWorktreeRemovesTheSharedStore
  (asserts `.git/worktrees/side/safegit` while the fixture names the
  worktree `linked-side`; vacuously true since it was written).
## Phase 10.2 consolidated outcome and remediation rulings

- All ten per-phase fresh audits complete. Phases 0, 1, 3, 4, 5, 8, 9
  PASS; Phase 2 PASS with one medium; Phase 7 PARTIAL (one medium-high
  defect); Phase 6 PARTIAL (one high defect, one medium-high
  unexecuted item). Findings consolidated into three remediation waves
  (A critical behavioral; B behavioral; C docs/comments/coverage).
- HIGH (Phase 6): the sequencer guard and its declared bypass resolve
  .git against the PROCESS CWD (internal/commit/sequencer.go:32 uses
  the relative GitDir answer; GuardInFlight joins it against safegit's
  own cwd). Reproduced: conclusions refuse from subdirectories; a
  single revert from a subdir bricks state after --no-commit already
  staged; a mid-merge commit from a subdir SUCCEEDS as a single-parent
  commit — 1.4's refusal silently disabled off-root. LATENT TWIN, fix
  together: commit.go:365-369 joins the same relative git dir for
  IndexBaseSharedIndex and a missing index file reads as EMPTY — the
  guard fix alone would make subdir conclusions silently truncate.
  RULING: anchor both resolutions absolutely; subdir tests for a
  conclusion, the mid-merge refusal, and the revert path.
- MEDIUM-HIGH (Phase 7): mv skips the destination-absent check for ANY
  case-only pair regardless of core.ignorecase — reproduced destroying
  an untracked destination's content on this case-sensitive
  filesystem. RULING: gate the exemption on core.ignorecase, read
  before validation; red-first.
- MEDIUM-HIGH (Phase 6, a binding log note left unexecuted):
  MERGE_AUTOSTASH / MERGE_RR unhandled — a safegit conclusion of an
  autostash merge strands the stash and the uncommitted work silently
  reverts (git's own --continue applies it and says so). RULING:
  native merge-continue APPLIES the autostash after commit and state
  cleanup, mirroring git; an apply conflict keeps the stash entry,
  prints recovery instructions, and exits nonzero (General, message
  names the stash). MERGE_RR joins the owned cleanup set; the residue
  test gains both files.
- MEDIUM rulings: scrub match --from silently rewrites each
  submodule's ENTIRE history (the 4.2 silent-escalation class,
  surviving on the match path) — adopt scrub file's submodule range
  mechanism and hard error, dedupe the byte-identical range arms; the
  cleanup-side old-object residue check warns at exit 0 — adopt the
  sibling's reachability filtering and route through Tier B (31); the
  DIRECTORY spelling of the positional+--untrack contradiction gets
  the same hard error as the file spelling; revert-continue's
  registration help falsely claims source-author preservation (the
  overturned ruling) — fix and regenerate; --dry-run push skips hook
  DISCOVERY along with execution, previewing success where the real
  push exits 24/25 — discovery is a pure read, run it in previews;
  divergences.md says exit 10 where PathMatchedNothing is 11.
- LOG CORRECTION (Phase 6 audit, reproduced both ways): the earlier
  ratified claim "a conflicted octopus cannot exist" is FALSE — only a
  FIRST-head conflict aborts unparked; a later-head conflict parks
  normally, and safegit merge-continue concludes it correctly today.
  The false rationale in sequencer_markers.go and 6.3's octopus scope
  sentence get corrected and a conflicted-octopus conclusion test is
  added; behavior unchanged.
- Generated-doc front-matter defect (Phase 9 audit): selfdoc gen
  PRESERVES an existing file's front-matter description, so
  internal-lock.md still carries the falsified O_CREAT|O_EXCL claim
  (the earlier "heals on next gen" assumption was wrong) and
  internal-trailer.md's description predates the record layer. Heal by
  regenerating from scratch if selfdoc rebuilds descriptions for
  absent files, else a deliberate front-matter edit; the mechanism is
  a candidate upstream selfdoc report.
- Wave B/C headline items: gnutls_handshake single-token transport
  pattern (falsifies the stated multi-word invariant) + the doubled
  stderr in the exit-40 message + two loose exit assertions + a
  test-name typo; readTreeEntries missing --full-tree; the RefUpdate
  port's silent empty->ZeroSHA substitution becomes a hard error (the
  one bypass of 0.4's contract); the reopened runGitIn helper
  consolidated; internal/filelock gains direct unit tests; mv
  intra-pair nesting refused at validation; a re-declared identical
  un-retracted pair on amend REFUSES as Usage naming the existing id
  (the whole-line dedup branch was unreachable — replaced by this);
  conclusion trailer/commit-msg-hook coverage tests; conflicted
  revert-continue inverse-record test; --moved-retract coverage
  (multi-id refusal, same-amend rule); hunks-vs-directory conflict
  detection with repo-relative error paths; exit-registry producer
  lists gain mv and the conclusions; the cross-repo auto-bump lock
  ordering documented; repo.Uninstall deleted (dead API, superseded by
  UninstallPlan + effects); hook_safety's stale comment and the two
  log-only branches strengthened; the scan state sweep unions the
  SHARED safegit dir from linked worktrees; the observe-staleness
  comment and the scrub-record prefix assertion; the remap-skip clause
  pinned; pre_rewrite_remotes nested per submodule path (schema
  updated); the Tier A/B scope-filter asymmetry documented in code;
  undo's registration string names all seven undoable ops (+ regen);
  the undo guide section gains the range-validation refusal;
  divergences.md gains front-matter and entries for rerere and (after
  the fix) autostash; stale red-era comment sweep over
  coord_subdir_test, commit_intake_edge_test,
  commit_staged_deletion_dir_test, amend_parity_test,
  commit_merge_state_test, and internal/conflict/property_test.
- RATIFIED AS-IS (no change): --allow-empty plus a no-match path
  refuses (the per-path rule governs; gains a pin);
  unmatchedSources's actual order (untrack arguments first) — the
  comment corrected to match; P8's quiet-channel decisions (git's
  captured output not quiet-suppressed — streaming parity, revisited
  by the push-streaming todo; the dry-run hook-skip notice suppressed
  under --quiet while the payload carries it under --json; no payload
  on a declined push); P5's six implementor-shaped hook decisions
  (ambiguity refusal, migrate no-clobber, legacy-name 24, hook_perms
  escalating a non-executable tracked hook to error/50, dot-files
  under legacy .d triggering 24, dangling symlink as non-executable);
  P1's untested pull exit path (identical code); P0's Spec.Env
  hardening, the third exemption kind, and version memoization;
  rerere non-recording on native conclusions (a documented
  divergence, not a defect).

## Phase 10.1 closure

- All seven battery items pass: build/vet clean; gofmt-clean; full
  -race 1571 PASS / 0 FAIL / 8 SKIP; stress run 4065 PASS / 0 FAIL /
  5 SKIP in 12m7s (all seven opt-in scenarios x 5 iterations; both
  former watchlist flakes green throughout); GOWORK=off run IDENTICAL
  to the default run (resolves released strictcli v0.33.0 from the
  module cache — no version skew); pre-checks.sh exit 0 (census
  unchanged at 520 sites); fresh baseline snapshot 1570/0/9 at
  a0887ec (the one-line delta vs the -race run is -short's
  TestRunTimeout skip, as designed).
- Baseline reconciliation vs the frozen campaign-start artifact
  (688/67/8 at b1d1385): 120 removed lines ALL classified — 61 healed
  reds; 59 sanctioned rewrites/renames/deletions, each cited to its
  plan or log reference. 936 added lines (61 healed + 47 successors +
  827 new, 583 of them new top-level test functions). Skip set 8 -> 9:
  the original 8 all still skip (7 re-keyed to --stress as planned;
  TestRunTimeout stays -short-keyed as recorded), plus the
  case-insensitive mv fixture. UNEXPLAINED: ZERO.
- The fresh snapshot lives at testdata/phase10-1-final.local-only,
  gitignored by the *.local-only convention like its 25 siblings.

10.1 COMPLETE. 10.2 per-phase fresh auditors dispatched next; 10.3
changelog after their findings (if any) are remediated.

## Phase 10 window closure and the flake root cause

- All six window items done; gofmt-clean; suite green; tree clean.
- FLAKE ROOT CAUSE FOUND AND FIXED (a real defect, not a
  certification): internal/test's TestMain built the test binary into
  os.MkdirTemp and deferred the RemoveAll directly above
  os.Exit(m.Run()) — os.Exit runs no defers, so EVERY run of the
  package permanently leaked ~11 MB into $TMPDIR. 1234 leaked
  directories holding 7.5 GB were found; the /tmp quota sat at 36 MB
  headroom, and EDQUOT at arbitrary points is exactly both watchlist
  flakes' shape (reproduced twice as quota failures). This was ALSO the
  "disk pressure" environment anomaly recorded earlier in this log.
  Fix: TestMain became os.Exit(runSuite(m)) with the build and cleanup
  inside runSuite; a new AST guard test refuses any function in
  internal/test that both calls os.Exit and defers (deliberately scoped
  to that package — production code pairs defer release() with die()
  by design, which is why die calls lock.ReleasePending). Red-first;
  leak count verified flat across runs.
- Both watchlist tests stress-certified INNOCENT after the fix:
  TestRootCommitConcurrentSafegitBothLand 1924 iterations (CPU pinning,
  up to 24 spinners, 8-way concurrency); TestStagesReportsADelete-
  ModifyConflictAsOneSided 3360 iterations. Comments on both record the
  cause, the no-interleaving-can-break-this argument, the counts, and
  forbid weakened assertions or retries. Zero unexplained
  nondeterminism remains.
- Cleanup: 574 of the leaked dirs (older than one day, 750 MB) deleted
  via saferm, age-restricted so no live run could own one; recoverable.
  660 recent ones left untouched (possibly owned by live sessions);
  deletable once no session is running the suite.
- Items 1-4, 6 as ruled: the six dead --help branches, guardedHelp and
  commandHelp deleted with one comment recording why they were
  unreachable; infof is the single output helper (printf deleted;
  push's block kept its !dryRun outer condition); the inner scan-result
  shadow renamed — the :540/:606 sites are NOT shadowing (the outer
  name is not yet in scope there; verified); gofmt normalization was
  four files, 12 insertions / 12 deletions, struct-field alignment (not
  const blocks as earlier recorded); census regenerated 498 -> 520,
  pre-checks.sh exits 0.
- Ratified: keeping infof (matches sibling outf); the TestMain/runSuite
  split (structural rather than remembered); the guard written against
  the class, scoped to internal/test. Process violation self-reported
  by the fixer: 11 mechanical replacements in sequencer_preview.go went
  through a Python heredoc instead of Edit — verified by read-back;
  not repeated.
- Changelog note for 10.3: the TestMain fix is --no-user-facing (test
  infrastructure).

## Phase 9 closure — PHASE 9 COMPLETE

- docs/divergences.md created: 46 verified entries across 8 sections;
  every entry deliberate except the working-tree-writing resolution
  keywords, marked PROVISIONAL in a top blockquote and its own entry —
  THE USER'S PRE-RELEASE REVIEW ITEM. The _CLAUDE template's
  conventions now require any change introducing a philosophy-vs-idiom
  decision to add its entry in the same commit (an overturn updates the
  existing entry rather than adding a second).
- --dump-schema ran clean; .strictcli/schema.json regenerated and
  committed via rlsbl commit (emission validates against the declared
  payload schemas by construction — settles the fresh reader's
  unsettled conformance item). Bare selfdoc gen regenerated 2 root
  files + 48 docs (cli-mv.md and the three cli-*-continue.md now
  exist) and auto-committed. Tree clean.

PHASE 9 IS COMPLETE. The divergence document awaits the user's review
as the pre-release item; Phase 10 proceeds meanwhile.

## Phase 9 fresh-read closure

- Ten commits; suite green three times (before/after the main.go edit
  and at the end); tree clean. The three conclusion-command sections now
  exist in commands-guide (one shared + three per-command, after mv);
  integration-guide gains the conclude/abandon table and the two
  automation properties; architecture gains the delegated-conclusion
  subsection and the six-pipeline-paths correction; _CLAUDE's
  self-contradicting passthrough bullet fixed; undo's documented
  undoable set completed everywhere (mv + the three conclusions).
  Substantive false claims fixed included: scrub run examples missing
  the required --reason, a scrub match example missing the required
  range flag, schemaVersion listed as a config key (it is a file
  member), push/backup exit tables missing 24/25 and 5/41, and the
  retired positional hunk syntax surviving in architecture's hunk note.
- CODE FINDING + ORCHESTRATOR RULING: the passthrough handlers' --help
  branches (coord_cmd.go:134-461 family plus guardedHelp) are
  UNREACHABLE — strictcli intercepts --help/-h anywhere in a
  passthrough argv before dispatch (reproduced three ways; after a bare
  -- the args begin with --, never --help). The checklist pass's
  passthrough-help commit had edited invisible text; the fresh reader
  moved the substance into the reachable app.Passthrough registration
  strings. RULING (dead-code policy, superseded and unreachable):
  DELETE the dead branches and guardedHelp — Phase 10 window.
- Ratified: @latest -> @v0 in the README template (the standing fleet
  convention mandates it; orchestrator additionally verified the proxy
  holds no phantom 1.x — @latest currently resolves v0.28.0, so the
  change is convention + insurance, not a live wrong resolution);
  conclusion-section placement; the six registration-string rewrites;
  the architecture delegated-conclusion subsection.
- Accepted as-is: the version section's sample output block stays a
  static era sample, not a live claim.
- Nuance recorded: a delegated cherry-pick's commits CAN carry a
  Claude-Code-Session-Id trailer copied verbatim from the source
  commit's message by git; "no safegit trailers" is about injection.
  Docs worded to prevent the misreading.
- The --dump-schema-vs-declared-schemas conformance check rides the
  phase-end schema/selfdoc run.

## Phase 9 checklist pass closure

- All 42 Appendix A rows and every log-recorded Phase 9 row resolved
  (fixed / verified-true / pre-healed-verified per the healer's ledger);
  11 commits, one per doc file or coherent cluster; build/vet clean;
  full suite green twice (after the main.go and coord_cmd.go help
  edits); zero test pins needed; the generated exit-table block
  untouched (no registry Meaning changed).
- LOG CORRECTION (code wins over an earlier entry here): the
  environment-constraint sentence saying doctor is the recovery path
  when flock(2) is unavailable was WRONG — doctor's --action fix goes
  through ReclaimIfStale, which itself needs flock; `safegit unlock` is
  the unconditional force-release (its own code comment names it the
  last resort). Docs state unlock as the recovery path; doctor takes the
  strict reclamation path.
- Appendix row 12's third claim ("bypass warning on the next mutating
  command") described behavior that exists NOWHERE in the tree; docs now
  describe doctor's bypass_detect check only.
- Ratified unbriefed decisions: hook remove/hook migrate sections added
  (the Phase-5 row about missing command coverage implied them); the
  seven passthrough --help strings edited in coord_cmd.go, where row
  16's text actually lives, not main.go.
- Handed to the fresh-eyes reader: the commands guide has NO sections
  for merge-continue / cherry-pick-continue / revert-continue — three
  commands with rich contracts entirely undocumented there (a
  completeness gap, not a false claim, hence outside the checklist).
- Row-1/2 refinements as built: the dry-run row now states what a
  preview DOES do (quarantine writes outside the repo; reads including
  network reads still happen); the backup network promise is scoped to
  backup's own dry run; push's section states --dry-run still
  ls-remotes.

## Phase 7 remediation closure — PHASE 7 CLOSED

- All seven items done red-first; eight commits; full -race green; tree
  clean. Item 1 as ruled: RewriteMessage returns an error, a
  RecordTransformError names original line / would-be line / verdict,
  refuseCorruptRecord rides the standard refuse() tail, and the two
  walk-error exits route through dieFinalize (now 30, was 1) — the
  refusal lands during the walk, before prepareAll/publishAll, nothing
  moves. Item 2: trailer.Overlap is the single overlap authority
  (NoOverlap/SameSource/SameDestination/Chained); internal/commit's
  private overlap deleted; --moved chaining exits Usage on commit and
  amend. Item 3: submoduleScopeRefs is the one spelling of the
  submodule scope line, shared by both completions.
- Item 4's premise was STALE against the tree: the declined force-push
  already routed "Aborted." through infof; the audit's bare-stdout
  reading was wrong. Deliverable became a regression pin.
- Ratified internals: dieFinalize broadened with its doc updated (name
  kept); refusal guidance rendered as trailing findings (refuse() has no
  guidance slot); the vacuous-precondition fix derives the path from
  filepath.Base(wt) so helper and assertion cannot drift; Nests stays
  exported behind Overlap; the new exported trailer surface (Pair,
  OverlapKind + constants, Overlap, RecordTransformError) was
  implementor-shaped. RepoRoot->AnchorRoot unified at Pipeline.Execute,
  Amend, Reword, and pushHintForRepo; main.go's repoRootOrEmpty stays
  RepoRoot (it IS discovery, the one thing the pin cannot consult).
- Queued for the Phase 10 window: globalFlags.printf duplicates infof
  (two spellings of one job — unify); scrub_match.go's inner subResults
  shadowing trap; the hand-rolled silent-check printf blocks in
  push.go/backup.go convert only if the printf unification happens.
- saferm archives from agent scratch: 17194 (34 MB), 17195 (178 MB) —
  left alone; purging is the user's call alone.
- Phase 9 rows: scrub match/run prose must name the exit-30 refusal for
  a substitution that would break a move record; the declared-moves note
  gains the chaining refusal alongside nesting.

PHASE 7 IS CLOSED (implemented, audited, remediated).

## Phase 7 closing-audit outcome and rulings (covers 7.1-7.5 + the six fixer items)

- All 26 audited requirements addressed; suite fully green under -race
  (1241 top-level PASS / 0 FAIL / 8 SKIP; the case-insensitive mv fixture
  skips on this filesystem, its forced-core.ignorecase variant runs). All
  headline scenarios independently hand-reproduced, including the
  mid-merge mv refusal ordering, dry-run uninstall removing nothing, and
  both revert doors minting identical inverses. Adversarial round:
  quote injection and literal-arrow injection into record values re-encode
  correctly; a file NAMED a valid ULID parses correctly.
- BUG 1 (medium-high), ORCHESTRATOR RULING: trailer.RewriteMessage
  re-encodes transformed pairs without re-running ValidatePair, so three
  transform shapes write records the decoder refuses (both-paths-equal,
  subtree-marker mismatch, empty token) — scrub exits 0 and the corrupt
  record is silently inert forever (RemoveMovedRecordsNaming skips
  unparseable records, proven live). Ruling: a transform whose resulting
  pair fails ValidatePair is a HARD REFUSAL before any ref moves (Tier A
  timing, exit 30 RewriteRefused), naming the commit, the record, and the
  invalid result, with guidance (different replacement, scrub file
  --delete, or retract first). Never written, never silently dropped.
  Red-first for all three shapes; assert Malformed stays empty.
- BUG 2 (low), ORCHESTRATOR RULING: --moved accepted chained declarations
  (a -> b beside b -> c) that mv refuses as Usage. A chain in one commit
  always yields a dead record (the middle path cannot be in the commit's
  own tree) — incoherent as a declaration. --moved adopts the same Usage
  refusal via ONE shared check with mv (single authority; red-first both
  ways).
- RULING (projection unconsumed): trailer.Forward/Projection/RetractedIDs'
  absence of production consumers is AS-PLANNED, not dead code — the plan
  builds the read layer with unit tests only; the first consumers are the
  future work in todo/record-file-moves-remaining.md. Malformed-record
  surfacing to operators arrives with the first production reader.
- RULING (inverse despite retraction): conclusionMovedRecords minting an
  inverse for a record a LATER commit retracted is correct as built — the
  inverse describes the revert commit's own tree delta; the source
  record's retraction status is irrelevant, and trees arbitrate any wrong
  claim. Pin test dispatched.
- Census staleness at audit time (498 committed vs 522 regenerated; all
  of mv.go's sites absent) is BY DESIGN under the releases-only
  regeneration ruling; Phase 10 regenerates and commits it as release
  prep, which also keeps .rlsbl/hooks/pre-checks.sh green at release.
- Dispatched to the remediation fixer besides the two bugs: scrub match's
  scope line names only the parent ref where scrub file names the
  submodule line too (a match that rewrote a submodule never named that
  history — parity fix); the declined force-push prints "Aborted." on
  bare stdout where uninstall routes it through infof (consistency fix);
  the vacuous uninstall-test precondition (assert path is
  .git/worktrees/linked-side/safegit — the fixture names the worktree
  linked-side); the RepoRoot->AnchorRoot anchoring unification
  (Pipeline.Execute at commit.go:244, Amend/Reword at amend.go:85/:446;
  assess rewrite_result.go:476 and main.go:88 and report each site's
  disposition).
- Phase 9 row additions from the audit: docs/internal-trailer.md's
  front-matter description predates the record grammar/projection layers
  (body regenerates); architecture.md has no move-record or mv section
  (already noted); the generated CLAUDE.md release-workflow staleness is
  Appendix row 26's existing scope.
- Audit scratch was archived by saferm (id 17194, 34 MB) because a hook
  blocks plain rm; the archive stays (purging is permanent destruction
  and is the user's call alone — the orchestrator's attempt to purge it
  was refused by the user).

## Ratified 7.3/7.4/7.5 decisions (resumed session, audited above)

- Suite 1544 PASS / 0 FAIL / 9 SKIP (short run); full -race green. The
  ninth skip is the case-insensitive mv fixture (skipped on case-sensitive
  filesystems; a forced-core.ignorecase variant runs everywhere and covers
  the two-step rename branch).
- mv commits the RENAME ONLY: each moved path carries the exact
  parent-tree blob via IndexEdits, never staged from disk — matching
  `git mv` + commit; uncommitted content changes at a moved path stay
  uncommitted; preview and execution compute the same tree; case-only
  renames fall out. DIVERGENCE-DOC SEED (git-like).
- mv mints its records via CommitRequest.MovedRecords, not --moved (the
  two validations ask opposite questions: --moved asks "is the old path
  gone", which is false under dry run and under a case-only rename; the
  shared authority is the pair grammar only).
- mv creates missing destination directories through the effects handle
  (dry run records the mkdir; rollback removes the topmost directory it
  created); git mv refuses instead. DIVERGENCE-DOC SEED (ours).
- A commit failure AFTER successful renames leaves the files moved, with
  a message saying so and pointing at `safegit commit --moved` (the
  plan's rollback covers the rename sequence only).
- A directory named in file form is refused (exit 19) naming the subtree
  spelling (grammar shape never inferred from disk); pair CHAINING
  (a -> b beside b -> c) is refused as Usage alongside nesting (the
  result would depend on performance order).
- mv's -m is REQUIRED, repeatable, no default (the mutating-default ban;
  no codebase precedent for a default commit message).
- Exit 19's registered meaning WIDENED from "a declared move (--moved)"
  to "a claim about a move (--moved, --moved-retract, a mv pair)"; the
  generated table regenerated. --moved-retract reuses 19; its refusals
  name EVERY bad id; retraction is judged against the same base as
  --moved (branch tip; the tip's first parent for amend/reword) — a
  record declared by the very commit being amended is not retractable in
  that same amend (an amend drops it by not re-declaring); in code.
- scrub --replace-with makes NO message edit (content replacement keeps
  the path; records naming it remain true); only --delete removes records
  naming the scrubbed path (as old, as new, or inside a covering subtree
  prefix), on both the top-level and submodule walks, declared per commit
  so Tier A accounts for the message change. A --delete that empties a
  whole message writes a bare newline (the walker's Message=="" sentinel
  means keep-original, which would fail Tier A).
- match trailer rewrite: transform the DECODED path tokens and re-encode
  through the one total encoder — output always parses; never refuses.
  Documented consequence: a pattern matching only the escaped spelling
  matches nothing, and Tier A then refuses the rewrite rather than a
  corrupt record ever being written.
- 7.4: inverse records minted in BOTH revert doors (clean computed revert
  and conflicted revert-continue) with fresh ULIDs; retractions are not
  inverted; queued reverts stay git-authored and record-free; the revert
  help text states the asymmetry.
- PLAN EXTENSION (defect found and fixed red-first): the pipeline's
  in-flight refusal fired at commit-build time — AFTER mv's renames — so
  mv mid-merge would move every file and then refuse. coord.GuardInFlight
  now runs inside the operation lock before the first mutation.
- Folded item 1 done (submodule-only scrub match completion prints
  summary + scope + rotation; scrub run has no such branch — it passes
  nil companions, and its result==nil case is "matched nothing", which
  correctly prints no completion). Folded item 2 was ALREADY done
  (ApplyIndexEditsTo already anchors via AnchorRoot). Queued for the
  Phase 7 audit remediation: Pipeline.Execute/Amend still call
  git.RepoRoot directly for their own repoRoot — the same
  single-authority question one level up; identical under the pin.
- ENVIRONMENT: /tmp is at its tmpfs quota (~9455M/9472M, ~110k foreign
  entries) and /home is at 100% (~3.0G free); go test can fail with
  "disk quota exceeded". Test runs point TMPDIR at a scratch under
  ~/.cache for now. Surfaced to the user.
- Phase 9 rows: commands-guide needs a mv section, a --moved-retract row
  in commit's flag table, the "Declared moves, never detected" note
  updated (the checked flag and safegit mv), scrub file --delete record
  removal, scrub match's trailer-aware rewrite, and a revert section
  stating the single-vs-queued record asymmetry; the _CLAUDE template's
  internal/trailer row underdescribes (it now owns the record grammar,
  projection, and record-aware rewriting) and its conventions need
  mv / --moved-retract / record-aware-scrub bullets; architecture's
  commit-family narrative needs mv as the fourth commit-producing path
  (validate -> rename -> commit). Generated docs (root CLAUDE.md,
  README.md, cli-*.md incl. a new cli-mv.md) heal via selfdoc gen.

- Phase 9 rows from the fixer: the confirmation-seam paragraph in
  docs/_CLAUDE.md must say the prompt goes to STDERR unsuppressed by
  --quiet; uninstall's repository-wide scope and enumeration need
  sentences (doctor sections in commands-guide, the `--action` help in
  main.go, _README's uninstall line); the scrub scope line needs a
  mention (commands-guide scrub section); integration-guide's machine-mode
  section can note scrub match/run now always carry a payload where
  no-match runs previously emitted payload null. Generated docs heal via
  selfdoc gen.
