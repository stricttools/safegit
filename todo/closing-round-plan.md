# The closing round: implementation plan

Self-contained: every decision this round executes is stated in full in
THIS file. It executes the rulings from the pre-release design review
that followed campaign 2 (whose plan, `todo/campaign2-plan.md`, is a
completed historical record — where the two disagree about what to do
NEXT, this file wins). A session with zero conversation context can
implement any subphase from this file plus the cited code. Anchors are
claim text (file + function + a distinctive phrase), verified by four
grounding investigations at HEAD `ef4b07e`; expect line drift, never
claim drift.

**EXECUTION starts on the user's explicit go.** This file existing is
not that go.

**Standing discipline** (unchanged from the campaigns): commits via the
INSTALLED safegit (single `-m`, plain paths, repo root); never raw
`git add`/`git commit`/any restore form; deletions via saferm with
`--on-error abort` and a description; scratch repos via t.TempDir or
`mktemp -d -p ~/.cache`, never /tmp, never in-repo outside
`experiments/`; red-first for every behavior change; tests are
permanent, in the established suites; bulk edits dry-run first with
occurrence counts and full diff review; `internal/test`'s TestMain
compiles the live tree, so mid-wave results are advisory and
authoritative runs need a quiescent tree. The CAMPAIGN-ERA single-writer
override on the divergences catalog is OVER: the repo's standing rule is
back in force — a change that overturns a catalog entry EDITS that entry
in the same commit, never adds a second. `.rlsbl/changes/unreleased.jsonl`
is APPEND-ONLY (the batch-exclusion list keys entries by line number;
inserting anywhere else invalidates it). `.strictcli/schema.json` is
regenerated ONCE, on the quiescent tree at the end of Phase 5, never
per-phase (its embedded version member churns on every commit; build
from a normal checkout or the member becomes "dev").

**Decision-origin marks.** `[user]` = ruled by the user in the review
(recommended-option picks weakly held per the standing convention).
`[plan]` = orchestrator resolution of a grounding-discovered gap,
reversible on request.

---

## Phase 0 — Groundwork

Zero-dependency items; one implementor, any internal order.

### 0.1 The closing baseline
Run `scripts/test-baseline testdata/closing-baseline.txt` on the
untouched tree and commit it — the reconciliation anchor for Phase 6's
verification and Phase 7's audit. The campaign artifacts
(`testdata/campaign-baseline.txt`, `testdata/campaign2-baseline.txt`)
stay untouched.
**Verify:** committed; `--check` clean on the same tree.

### 0.2 The recovery-path policy in the registry doc `[user]`
`internal/exitcode/exitcode.go`'s package doc gains a new heading
(sibling of "What the framework owns", NOT folded into the
campaign-scoped standing-rule section, which is campaign-flavored and
should not carry a permanent policy): a new exit code is registered
only when the CALLER'S RECOVERY differs from every existing code's
(retry-safe vs fix-and-rerun vs do-not-retry); the same recovery joins
an existing family and the payload discriminates detail. Grounded: no
registry test parses the package doc prose; `docs/internal-exitcode.md`
pulls it via a selfdoc directive, so Phase 5's `selfdoc gen` picks it
up with no committed copy to edit.
**Verify:** registry tests green; the doc states the rule.

### 0.3 The coincidence pin `[user — "we should" demonstrate it]`
New test in `internal/test/moves_inferred_test.go`, beside
`TestInferredFileMoveIsRecorded` (it is the same fence PASSING, not a
refusal): seed a file with unique content, remove it, write a
differently-named file with identical bytes, commit both paths; assert
exactly one inferred pair old-to-new with origin `observed` and no
declare notice (probed: this is current behavior). The doc comment
records the ruling: the fences' conditions are exact, so identical
unique content IS the evidence; safegit records what the delta proves,
never guesses intent, and the record is retractable. Test-only — the
catalog's observed-record entry already states the exact conditions;
this consequence is a pin, not a new git-facing divergence `[plan]`.
Helpers exist (`assertInferredPairs`, `assertOrigins`,
`commitMessageOf`); no new machinery.
**Verify:** the pin is green on arrival and its comment carries the
ruling.

### 0.4 The exemption predicate tightens to fail-closed `[plan]`
`internal/commit/sequencer.go`'s `concludesInFlightOperation` becomes
AND (a conclusion declares BOTH the sequencer context and the
shared-index base; a future half-configured request refuses instead of
silently bypassing the unmerged-index guard). Grounded: both production
construction sites (the `-continue` door and `concludeParkedOperation`)
already set both, so no behavior changes; the doc comment currently
ARGUES the OR and must be rewritten, not trimmed. One sanctioned test
edit: `TestPipelineHonorsADeclaredSequencerContext`'s request literal
gains the shared-index base. Consequence stated in code `[plan]`: the
amend/reword call sites pass the parent-tree base hard-coded, so their
sequencer parameter can no longer exempt the unmerged guard — correct
(an amend is never a conclusion), and the comment near those call sites
states the new fact instead of the old framing.
**Verify:** the full commit/amend suites green; the one edited test
green; the unmerged-guard suite green.

---

## Phase 1 — Sequencer-state behavior

ONE implementor — the three items collide in `cherry_pick_cmd.go`,
`revert_cmd.go`, `coord_cmd.go`, and
`internal/test/inflight_compute_refusal_test.go`, and the first item
rewrites the fixtures the second item's tests need. Internal order is
mandatory: 1.1, then 1.2, then 1.3.

### 1.1 The empty park auto-cleans `[user]`
When safegit's own compute produces a no-change result, it removes the
state it just parked and the refusal says so.
- The single insertion point is `concludeParkedOperation`'s
  tree-unchanged branch in `sequencer_continue.go` (its only three
  callers are the restructured cherry-pick, merge, and revert — a
  precise scope boundary; `gitDir` is in scope; the `onEmpty`
  signatures need no change). Mirror the unpark precedent
  (`refuseParkedRawGitShape`): `sequencer.Cleanup` for the operation's
  kind plus the index/worktree sync, both halves attempted, a cleanup
  failure downgrading to the general exit with the residue named —
  never swallowed `[plan]`.
- Grounded facts: the empty pick leaves CHERRY_PICK_HEAD (safegit's
  own write) + AUTO_MERGE + MERGE_MSG; the empty revert leaves
  REVERT_HEAD + AUTO_MERGE + MERGE_MSG; `sequencer.Paths` covers both
  as supersets (absent paths are not errors). Merge is exempt by
  construction (allow-empty; an already-merged `--no-commit` parks
  nothing — probed) and `merge --no-commit` with real work MUST keep
  parking (the operator's explicit request).
- The refusal messages are rewritten: they currently instruct
  `git <verb> --abort`, which after the auto-clean would name nothing
  in progress. Keep the phrase "no change" (an existing restructure
  test asserts it). Exit stays the general code `[plan — nothing in
  the ruling moves it]`. The `-continue` door's OWN empty refusal
  (`refuseEmptyConclusion`) is NOT touched — the state there is the
  operator's, not this invocation's; state the two-door distinction in
  code.
- SANCTIONED FIXTURE REWRITE (the round's largest):
  `inflight_compute_refusal_test.go`'s two fixture builders create
  their parked state by running the exact empty second pick/revert
  this item deletes. Re-fixture on RAW git (probed working: a raw
  `git cherry-pick` of an already-applied commit parks
  CHERRY_PICK_HEAD on a clean tree; a raw `git revert --no-commit` of
  an already-reverted commit parks REVERT_HEAD) — a better fixture
  anyway, since the suite's scenario is raw git beside safegit. The
  two state-producer pins INVERT (they now assert the state is GONE
  and the message no longer names `--abort`); the seven consumer
  tests re-fixture in place. Appendix A lists them all.
- Catalog, same commit: the empty-commit entry's sentence about the
  pick/revert refusal naming `--skip`/`--abort` now describes only the
  `-continue` door — split it; the entry gains the new fact (git's own
  empty pick parks and offers three ways out; safegit cleans up and
  the next command just works) `[user-ruled direction; entry edited,
  never duplicated]`.
**Verify (red first):** after an empty pick/revert — no sequencer
residue (reuse `assertNoSequencerResidue`), clean status, HEAD unmoved,
exit unchanged, message contains "no change" and does NOT contain
"--abort", and THE POINT: the next `safegit commit` succeeds. The
merge `--no-commit` park preserved. The pre-existing
already-applied-refusal test stays green.

### 1.2 The compute guard covers the --no-commit passthroughs `[user]`
No compute of any kind runs over parked state.
- The two holes (probed, real): `cherry-pick --no-commit` staged its
  content over a parked revert with no CHERRY_PICK_HEAD written (a
  later revert-continue would commit the pick's files into the revert
  commit); `revert --no-commit` over a parked pick left TWO operations
  in flight.
- Mechanism `[plan]`: the entry check inserts in
  `runGuardedPassthrough` after the coordination guard and BEFORE the
  dry-run branch (a preview computes with git's merge engine over the
  same state), reached only from the two `--no-commit` dispatch sites
  — a parameter or thin wrapper distinguishes them from the
  state-control sites, which MUST stay exempt (`--abort`/`--quit` are
  the way out; probed working and staying so). `merge --no-commit` and
  `pull` are already covered (verified; pinned).
- REBASE JOINS `[user — ruled at the review]`: `safegit rebase` gains
  the same in-flight entry refusal (probed: a rebase over a parked
  revert on a clean tree exits 0 and STRANDS the revert's state files
  behind it, blocking every later commit). The check inserts in
  rebase's handler before git runs; git's own state-control forms
  (`rebase --continue`/`--abort`/`--skip`) stay exempt — they operate
  on the REBASE's own state, which the sequencer reader reports as
  in-flight, so the exemption keys on the rebase-kind state exactly as
  the conclusion commands' exemptions do. Red-first: rebase over a
  parked revert refuses naming the revert and the way out; an
  ordinary rebase still runs; a conflicted rebase's own --continue
  still reaches git.
- Catalog, same commit: the commit-refuses-in-flight entry's claim
  that the check covers merge/cherry-pick/revert/pull "before they
  compute anything" is currently INACCURATE for the --no-commit route
  — this item makes the sentence true; edit the entry to say so.
**Verify (red first, on the re-fixtured raw-git parked states):**
`cherry-pick --no-commit` over a parked revert and `revert --no-commit`
over a parked pick each exit with the coordination-busy code, name the
in-flight operation and the safegit conclusion command, move no HEAD,
stage nothing, write no state file of their own, and leave the parked
state intact; a `--dry-run` variant refuses identically; with nothing
in flight both forms still work.

### 1.3 The out-of-band refusal names its leftovers `[user-adjacent
polish, ruled with the round]`
`cherry_pick_cmd.go`'s state-changed-during-compute branch (the
sibling of revert's already-fixed one) currently prints one line naming
neither the CHERRY_PICK_HEAD safegit itself wrote nor the staged
result. Rewrite it to the revert sibling's shape (anchored by its
"something else started or cleared a git operation in this worktree
while it ran" wording): name the leftover state file(s) including
safegit's own CHERRY_PICK_HEAD, report the staged result, leave
everything in place (an out-of-band writer is demonstrably active;
report, never destroy — the established stance), point at `git status`.
Grounded reproduction device for the red test: a `post-index-change`
hook that writes MERGE_HEAD fires during the compute (probed); the test
asserts the exit, the named file, and that the file still holds the
picked commit.
**Verify (red first):** the reproduction; the message names
CHERRY_PICK_HEAD and the staged content; nothing rolled back.

---

## Phase 2 — Unborn-branch support `[user — support now]`

After Phase 1 (shared catalog file; light adjacency in the sequencer
files). One implementor.

- The coordination check: when HEAD does not resolve (the cheap test is
  a quiet rev-parse verify), diff against the EMPTY TREE instead —
  probed as semantically identical (tree-vs-worktree, reports staged
  and unstaged-tracked changes in the same parseable shape). The
  empty-tree name is obtained per hash algorithm; on this cold path,
  one `git hash-object -t tree /dev/null` invocation is the right form
  (the hardcoded-table precedent's cost argument does not apply here —
  stated in code) `[plan]`.
- The SAME substitution extends to the two other HEAD-resolution seams
  the ruling's wording missed `[plan — forced: fixing coord alone makes
  unborn picks WORSE, parking broken state]`:
  `sequencer_markers.go`'s first-parent constant (three uses plus the
  index-changed listing) and `sequencer_preview.go`'s two HEAD reads.
  For the preview: an unborn merge preview can short-circuit (it is by
  definition a fast-forward); the replay preview substitutes the empty
  tree for ours, which merge-tree accepts WHEN a merge-base is given
  (probed) — and a replay always has one.
- What then works (all probed on a patched build): `switch -c` and
  `switch <branch>`; `merge <branch>` takes the already-built unborn
  fast-forward arm (ZeroSHA-pinned CAS, synced tree, the
  fast-forward oplog outcome); a dirty unborn tree refuses at the
  coordination-busy code with the listing; `cherry-pick <c>` works
  end-to-end (a root commit with the source author preserved).
- The unborn REVERT refuses as empty `[plan]`: probed, a revert on
  unborn currently mints a nonsense empty root commit because the
  tree-unchanged comparison has no parent tree — compare against the
  empty tree on the root path so the standing empty-result refusal
  (and 1.1's auto-clean) fire exactly as on a born branch.
- What stays git's own fatal, deliberately (parity, stated in the
  guide): `merge --no-ff` and `merge --no-commit` on unborn (git
  itself refuses a non-fast-forward into an empty head), `rebase`,
  `bisect start`. Supported does not mean every form works; the
  refusals are git's own and honest.
- Grounded residue: a successful `switch -c` on a still-unborn repo
  records an ok navigation entry with an empty observed tip —
  harmless (the observed spelling is deliberately unconsumed), noted
  in the entry-writing comment.
- The dead "unreachable TODAY" comment on merge's unborn arm is
  deleted with the fix. Catalog, same commit: the dirty-tree entry's
  "Dirt is a diff of the working tree against HEAD" sentence gains the
  unborn clause (entry EDITED). No test pins the current refusal
  (verified — pure addition).
**Verify (red first):** integration tests on a `git init` +
`fetch <src> side:side` fixture (the clean unborn-with-a-branch shape;
an orphan switch defeats it): merge fast-forwards with a clean tree
and the fast-forward outcome; switch both forms; dirty unborn refuses
with both paths listed; commit still roots; pick roots with the
author preserved; revert refuses as empty with no residue; previews
work. Unit tests in `internal/coord` for the unborn clean/dirty arms.

---

## Phase 3 — Symlink portability refusal `[user]`

Independent of Phases 1-2 in code; after them only for the shared
catalog file. One implementor.

- The judgment (`internal/commit/intake.go`, the escape-judgment
  function): the refused class becomes ANY ABSOLUTE target (inside or
  outside the repository) plus, as today, a relative target resolving
  outside. Relative targets resolving inside — including traversing
  and dangling ones — stay committable, unchanged. Grounded: the
  judgment is purely lexical after cleaning; absolute targets skip the
  join, so the change is one arm of that function.
- THE RENAME FAMILY `[user — spelling settled at the review]`: the
  property the refusal protects is checkout portability, and
  "escaping" no longer describes the class. The flag becomes
  `--allow-non-portable-targets` (standard technical English is
  "non-portable", not "unportable"; each word hyphenated per the flag
  convention); the request fields, the four intake functions, and the
  exit-29 constant rename to the `NonPortable` spelling (registry
  precedent exists: exit 25's rename); the registry meaning, generated
  table, and flag help reword to the portability rationale, and every
  prose mention writes "non-portable" hyphenated.
- Both operator messages branch by shape `[plan — grounded: the
  current advice is actively misleading for absolute-inside]`: an
  absolute-inside target's refusal says the link is inside the
  repository but its absolute spelling will not resolve in a checkout
  at another path — spell it relative, or elect; the outside shape
  keeps its point-it-inside advice; the election notice rewords to the
  portability rationale covering both.
- MIGRATION CONSEQUENCE, stated for the changelog: a repository with a
  tracked absolute in-repo symlink starts refusing on the next commit
  that names it or sweeps it up by directory expansion — a breaking
  entry.
- Sanctioned rewrites (Appendix A): the absolute-inside pin inverts
  into the refusal test (its own comment says it is the one to
  rewrite); the wording assertions across the escaping suite follow
  the new messages; the traversing-inside, inside-target, and
  directory-symlink pins stay green untouched; the mv carve-out pin
  updates only for the flag rename (mv never judges link targets —
  verified structurally, the one call site is commit/amend intake).
- Docs, same commit where entry-overturning: the catalog entry is
  REWRITTEN including its heading (it names "leaves the repository");
  the guide's symlink section, the commit flag-table row, the template
  bullet; the generated exit table regenerates (freshness test
  enforces); cli-commit and the schema regenerate later (Phase 5).
**Verify (red first):** absolute-inside refuses naming the portability
reason and the remedy; absolute-outside still refuses; the election
commits with the notice; relative-inside/traversing/dangling stay
committable; directory expansion still judges swept-up links; amend
covered; the registry and generated table current.

---

## Phase 4 — Allowlist flips and the mv payload

Two independent halves; one implementor (or two, the halves share no
files). After Phase 1 for the catalog file only.

### 4.1 The five allowlist changes `[user]`
All rows live in `subset_allowlist.go`; the argv machinery already
handles every spelling involved (attached and detached strategy-option
values normalize to one name; the rebase flag takes its value attached
only, and NO value-flags entry may be added for it or it would swallow
a revision — stated in the file's own comment).
- Strategy OPTIONS become allowed on merge, cherry-pick, and revert
  (move the two-spelling row from refused to allowed in all three
  tables); strategy SELECTION stays refused everywhere (mind the
  grounded asymmetry: on pick/revert the short spelling is signoff and
  already allowed — preserve it). Probed: the compute stays ort under
  strategy options and AUTO_MERGE is written on all three verbs, so
  every conclusion protection sees what it sees today; the
  no-AUTO_MERGE refusal remains accurate as a selection signature.
- THE PREVIEW FORWARDS, never re-refuses `[plan — leaving the
  preview's own strategy-option branch would silently downgrade a
  runnable command line to a different refusal at a different exit]`:
  remove that branch from the preview refusal; the merge-tree wrapper
  already appends extra arguments, and forwarding is probed exact —
  the previewed tree is byte-identical to the real compute's under the
  same option, both spellings.
- Merging unrelated histories flips to REFUSED (delete its allowed row
  and the comment arguing for it; the refusal reason is the footgun,
  not a protection hole — and the way out for the legitimate
  once-per-lifetime import is stated: raw git computes with no-commit,
  safegit merge-continue concludes it). New catalog entry (none
  exists).
- The rerere auto-update flag flips to REFUSED on all THREE tables
  `[plan — the ruled rationale is verb-independent; the ruling named
  merge because the question did]`: cache-driven auto-staging is
  remembered resolution by the back door; the refusal reason
  cross-references the existing rerere catalog entry rather than
  restating it. New catalog entry.
- Preserving merge topology through a rebase flips to ALLOWED (move
  the row; the replay runs entirely inside the one git-authored door,
  which is verb-scoped and already admits it — an optional row in the
  door-admission test is consistent). The rebase help string updates
  (it enumerates the refusal), which regenerates four doc surfaces at
  Phase 5's regen. Catalog: the rebase entry's refusal bullet and
  framing update; the allowed-sets rows update.
- Sanctioned rewrites (Appendix A): the strategy-option rows leave
  four refusal tests; one preview-refusal test loses its only case and
  takes a substitute unsupported option; the rebase refusal table
  loses one row and gains a positive sibling; a new refusal test each
  for the two flips; the pick/revert strategy-selection test's doc
  comment rewrites to be about selection only.
- The mergeHelp "no strategy selection" phrasing is verified still
  literally true and left; the guide's four per-command allowed/refused
  paragraphs update (the merge one currently names selection and
  options in one clause — split).
**Verify (red first per flip):** strategy options pass end-to-end on
all three verbs (a conflicted compute under one parks normally and
concludes); their previews compute the identical tree; unrelated
histories and rerere auto-update refuse naming their reasons and the
catalog; topology-preserving rebase runs (git-authored, the door
admits the replay); every catalog row and guide paragraph agrees with
the code tables.

### 4.2 The mv payload unifies on moved_records `[user]`
- mv adopts the exact entry shape and member name commit uses,
  emitting from the pipeline's OWN record list — grounded: that list
  is already narrowed to the committed message, so the hand-rolled
  ID-narrowing helper and the mv-local entry struct become DEAD and
  are deleted (the fleet dead-surface rule); the reasoning in the
  deleted helper's comment migrates to the emission site. Strictly
  more correct: an observed record reaching mv's payload would now
  carry its origin instead of being dropped.
- Probed end-to-end in a scratch copy: the reshape builds and the full
  suite is green; the preview reports the declared origin.
- Sanctioned rewrites (Appendix A), BOTH in the payload-moves test
  file — and one is a TRAP: the hook-strip mv test asserts an
  empty-length member, so a bare rename would leave it green while
  testing a member that no longer exists; retarget it to the new
  member DELIBERATELY. The preview test fails loudly and retargets
  with the origin assertion added.
- No changelog break: mv has never shipped; the existing unreleased
  entry about reword/mv narrowing stays true (the pipeline still
  narrows — the deletion is of a reimplementation, not the behavior).
**Verify (red first on the retargeted tests):** real and preview mv
payloads carry the unified member with origin; the hook-strip case
reports only what the committed message carries; no reference to the
old member or helpers survives.

---

## Phase 5 — Documentation closure and regeneration

After Phases 1-4 (it closes over their doc debts). One implementor.

- THE ROSTER REWRITE: the catalog preamble's provisional roster has a
  factually wrong blanket clause (it declares every subset-boundary
  entry provisional while five there are deliberate). Rewrite the
  roster to reflect the end state: after this round, NO provisional
  entries remain — the ruled entries were edited by Phases 1-4 in
  their own commits; the remaining provisional dispositions flip to
  DELIBERATE here, en masse, per the user's review closure (the
  accepted-on-their-rationales set). The identical provisional ruling
  line appears on a fixed set of entries — count the occurrences
  before the batch edit, assert the count, review the diff (the
  batch-edit rule). The flipped-with-text-update entries this phase
  owns (not already edited by 1-4): the autostash entry's
  limit-awaits clause becomes the accepted-limit statement `[user]`;
  the DWIM paragraph's entry, the untrack-fence entry, the
  crash-window entry, the overwrite entry, the switch-c entry, the
  FETCH_HEAD entry, the fast-forward trio, squash, the
  commit-step-options family, skip, reset-pathspec, pull-rebase,
  bisect-vocabulary, and the two dirty-tree entries flip with their
  text intact (their rationales follow from explicit rulings).
- The any-committish sentence in the merge-sides entry is already
  deliberate and now user-ratified — no edit needed (verified).
- CONTRIBUTING.md's retired release-command form is corrected to the
  current release flow (the one stale claim found).
- Accumulated template/guide debts from Phases 1-4 that were not
  same-commit items; then `--dump-schema` from a clean build (its own
  commit via rlsbl commit — ONCE, here), then bare `selfdoc gen`, then
  `selfdoc check` must be exit 0.
**Verify:** zero provisional ruling lines remain (grep count); the
roster names no entry; every Phase 1-4 doc debt closed; regeneration
clean; selfdoc check green; the generated root files carry the new
texts.

---

## Phase 6 — Changelog and full verification

After Phase 5.

- Changelog entries for the round, APPENDED: breaking — the symlink
  portability widening (tracked absolute in-repo links start
  refusing), unrelated-histories refused, rerere-autoupdate refused;
  feature — strategy options allowed with forwarding previews,
  topology-preserving rebase allowed, unborn-branch support; fix —
  the compute-guard extension to the no-commit forms (the description
  states the silent-staging hole it closes), the empty-park
  auto-clean, the out-of-band leftover naming; no-user-facing — the
  plan file, the pins, the predicate hardening, the registry doc, the
  roster/doc work, the baseline. Type judgments follow the ruled
  refuses-previously-working-input-means-breaking rule.
- The census regenerates (`scripts/exit-inventory` into the testdata
  file, committed with the established message) as the LAST content
  commit — the release hook blocks on its freshness.
- The full battery on the quiescent tree: build, vet, gofmt; full
  race suite; the stress run at its 40-minute budget; the
  released-dependency run with the workspace off; selfdoc check; the
  12 changelog checks.
- Reconciliation against `testdata/closing-baseline.txt`: classify
  every differing line — a red-first heal (cite the subphase), a
  sanctioned rewrite (cite Appendix A), or a new test — zero
  unexplained.
**Verify:** all checks green; coverage total; the classification has
zero unexplained lines.

---

## Phase 7 — The final audit `[user — mechanics specified]`

- ONE fresh auditor on the FABLE model, granted the right to
  orchestrate OPUS subagents (every worker prompt opens with the
  no-further-spawning instruction; the auditor itself spawns only
  Opus workers). History-blind; the working tree is the truth.
- Scope: the full two-campaign result PLUS this closing round — with
  emphasis on everything that shipped after the last fresh audits
  (the campaign's final remediation fixers, the changelog top-up, and
  all of Phases 0-6 here).
- Briefing inputs (grounded): this plan; `todo/campaign2-plan.md`
  (WITH the note that its "EXECUTION IS ON HOLD" header is stale —
  the campaign is complete; without the note a fresh reader reports
  the campaign unstarted); `todo/redesign-campaign-plan.md` (campaign
  1, historical); the three baselines in testdata; the divergences
  catalog (its own inclusion rule and direction table); the allowlist
  code file as the single authority the catalog and guide mirror; the
  census and its generator; the req file's historical-document marker
  (never score against it); the experiments-directory convention; the
  build/test commands and the live-tree hazard.
- Findings remediated red-first; the battery re-run after; a finding
  the auditor grades as needing a user ruling goes to the user, not
  to a fixer.
**Verify:** the audit report addresses every phase of both campaigns
and this round; zero unremediated findings (or the user's explicit
acceptance of any remainder).

---

## Phase 8 — Release

- Todo triage: `todo/campaign2-plan.md`,
  `todo/redesign-campaign-plan.md` (campaign 1's plan, still sitting
  in todo — its move was ruled at that campaign's close),
  `todo/move-records-for-undeclared-moves.md` (consumed), and THIS
  plan move to `todo/.done/`. Staying active: the four strictcli-await
  files, the three contingent files
  (reader-writer-operation-lock, scrub-strict-mode-selector,
  push-streaming-restoration), reversibility-gaps, and
  `todo/pipeline-authored-rebase.md` (post-campaign work; ALSO cited
  by path from two shipped docs, so it must not move). The deferred
  and obsolete subdirectories untouched.
- `rlsbl release init`; the release file: bump MINOR (0.28.0 to
  0.29.0), the two-campaign-plus-closing-round description and
  context DRAFTED BY THE ORCHESTRATOR AND APPROVED BY THE USER before
  the release file is committed.
- `actionlint` over the three workflows (installed; cheap insurance
  before their first-ever run on this history).
- ON THE USER'S GO: `rlsbl release run --no-allow-dirty --watch
  --approve-consequential`. The candidate push is the first push of
  the entire two-campaign history; a red CI verdict is fixed forward
  at the same version with `rlsbl release resume` — never a new
  version, never a manual push.
- Post-release (recorded, outside the release): the fleet sweep for
  the six legacy placeholder pre-pre-push hooks (they will refuse at
  the hooks-not-migrated exit once the new version installs); the
  dependent projects' parked todos unblock on their own triage;
  `.rlsbl/config.json`'s pre-release hook and `selfdoc.json`'s version
  are release-pipeline-owned and need no hand edits.

---

## Dependency spine

| Phase | Depends on | Notes |
|---|---|---|
| 0 | — | four independent items, one implementor |
| 1 | 0.1 (the baseline predates behavior changes) | ONE implementor; internal order 1.1 then 1.2 then 1.3 is mandatory (fixtures) |
| 2 | 1 (catalog file; sequencer adjacency) | |
| 3 | 1 (catalog file only) | parallelizable with 2 except the catalog — sequence them |
| 4 | 1 (catalog file only) | 4.1 and 4.2 share no files |
| 5 | 1-4 | sole roster writer; the ONE schema regen |
| 6 | 5 | census last; battery on the quiescent tree |
| 7 | 6 | the Fable-orchestrated audit |
| 8 | 7 + the user's release-file approval + the user's go | |

Sequential execution is the default; Phases 2, 3, and 4 collide only
on the catalog file and may interleave if their catalog edits are
serialized.

## Recorded observations — out of scope, not lost

- `rebase` over a parked revert (clean tree) exits 0 and strands the
  revert state behind it, blocking later commits; `reset --hard`
  silently clears parked sequencer state; both are git's own behaviors
  under non-authoring passthroughs. Next-cycle candidates, surfaced to
  the user, deliberately not this round's scope.
- No oplog entry is written for an empty compute today, before or
  after 1.1 — whether "computed and unwound" deserves an audit-trail
  entry is a separate future decision.
- `merge --no-ff` / `merge --no-commit` on an unborn branch remain
  git's own fatal refusals (parity); a still-unborn switch-create
  records an ok navigation entry with an empty observed tip
  (harmless, unconsumed spelling).
- The campaign-era plan files' internal claims are historical once
  triaged; the catalog and the code are the living authorities.

## Appendix A — sanctioned rewrites (a break not listed here is a
plan defect)

- 0.4: the pipeline-honors-declared-context test gains the
  shared-index base in its request literal.
- 1.1: BOTH fixture builders in the in-flight refusal test file
  re-fixture on raw git; the two state-producer pins INVERT (state
  gone, no abort advice); the seven consumers re-fixture in place
  (the two pick/revert-refuse-over-parked tests, the pick preview
  variant, the two merge-over-parked tests, the pull-over-parked
  test's inline fixture, the three way-out subtests). The
  already-applied-refusal test keeps its "no change" phrase and stays
  green.
- 1.2: none beyond the new tests (the two no-commit-stays-passthrough
  pins run with nothing in flight and stay green — verified).
- 2: none (no test pins the unborn refusal — verified; pure
  addition). The dead unreachable-today comment on merge's unborn arm
  deletes.
- 3: the absolute-inside-committed pin inverts into the refusal test;
  the escaping suite's wording assertions follow the new messages;
  the mv carve-out pin updates for the flag rename only; every test
  spelling the old flag or constant name updates mechanically with
  the rename.
- 4.1: the four strategy-option refusal rows leave their tests (the
  merge raw-shapes row, the merge preview row, the pick/revert
  selection test's option member and its doc comment); the
  pick/revert preview-refusal test takes a substitute unsupported
  option (its only case was the strategy option); the rebase refusal
  table drops its topology row and gains a positive sibling beside
  the allowed-form control.
- 4.2: the two mv payload tests retarget to the unified member — the
  hook-strip one DELIBERATELY (it passes silently on a bare rename);
  the preview one gains the origin assertion.
- 5: the batch flip of the provisional ruling lines (count asserted
  before, diff reviewed after); the roster rewrite.
