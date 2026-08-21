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

## Phase 9 additions (locking rework falsified these claims)

The atomic publication and flock-based reclamation invalidated every
`O_CREAT|O_EXCL` claim: `docs/architecture.md` (~:82, :133, :138, :355,
:360 — creation story AND the "kill(pid,0) then remove and retry" stale
story), `docs/concurrency-guide.md` (~:45), `docs/_CLAUDE.md` lock
convention line (and its generated CLAUDE.md copy), and the
selfdoc-generated `docs/internal-lock.md` (heals on the next selfdoc
gen; the in-code package doc is already correct). Phase 9 must also
DOCUMENT two new environment constraints: lock acquisition now requires
hard-link support on the filesystem holding .git, and stale-lock
reclamation requires working flock(2) (without it, contenders time out
instead of reclaiming, and doctor is the recovery path).

## Open rulings (need the user's decision; as-built stands meanwhile)

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
