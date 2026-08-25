# The closing round: implementation plan (revision 2)

Self-contained: every decision this round executes is stated in full in
THIS file. It executes the rulings from the pre-release design review
that followed campaign 2 (whose plan, `todo/campaign2-plan.md`, is a
completed historical record — where the two disagree about what to do
NEXT, this file wins). A session with zero conversation context can
implement any subphase from this file plus the cited code. Anchors are
claim text (file + function + a distinctive phrase), verified by four
grounding investigations at HEAD `ef4b07e` and re-verified by an
adversarial critique at `9855c75`, whose findings — plus the design
rulings they prompted — this revision absorbs; expect line drift,
never claim drift.

**EXECUTION starts on the user's explicit go, IN A LATER SESSION** —
the user ruled that the session which wrote this plan does not execute
it. This file existing is not that go. Also ruled at the same review:
ONE release, after this round completes (main is green and could ship
today, but the round is this release's own work — the
release-once-at-the-end principle governs; the v0.28.0 defect exposure
ends at that single release).

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
in the same commit, never adds a second; and the entry's
provisional/deliberate ruling marker is PART of the entry `[plan]`, so a
phase that rewrites a provisional entry's text also flips its marker in
that same commit (Phase 5 sweeps only the text-unchanged remainder).
`.rlsbl/changes/unreleased.jsonl` takes APPENDS and IN-PLACE EDITS only
(`rlsbl changelog edit --id` preserves the line count; the
batch-exclusion list keys entries by version+line, so inserting or
deleting lines invalidates it — never do either). `.strictcli/schema.json` is
regenerated ONCE, on the quiescent tree at the end of Phase 5, never
per-phase (its embedded version member churns on every commit; build
from a normal checkout with `GOWORK=off` — the gitignored `go.work`
overlays a local dependency checkout, and without the switch the schema
embeds the unreleased dependency version — or the member becomes
"dev").

**Decision-origin marks.** `[user]` = ruled by the user in the review
(recommended-option picks weakly held per the standing convention).
`[plan]` = orchestrator resolution of a grounding-discovered gap,
reversible on request.

---

## Phase 0 — Groundwork

The round's housekeeping was EXECUTED in the planning session, before
this plan's go (recorded here so the audit and reconciliation know the
origin): the closing baseline is committed
(`testdata/closing-baseline.txt`, zero failures, HEAD recorded in its
header) — the reconciliation anchor for Phase 6 and the audit; the
exit-code registry's package doc carries the one-code-per-recovery-path
policy; the coincidence pin
(`TestTwoUnrelatedFilesSharingContentAreRecordedAsAMove`) is green with
its ruling comment and the catalog's observed-record entry carries the
consequence sentence; the two promoted tools exist and are demonstrated
(`scripts/counted-edit` — the batch-edit discipline as a tool, dry-run
default, counted, abort-on-mismatch; `scripts/verify-coverage-partition`
— the exact-partition check Phase 6's changelog step uses);
CONTRIBUTING.md's release section states the real flow. One item
remains, the phase's single subphase:

### 0.1 The exemption predicate tightens to fail-closed `[plan]`
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
(an amend is never a conclusion), and the comments near those call
sites gain a sentence stating the new fact (no old framing exists at
the call sites — the OR argument lives solely on the predicate's own
comment).
**Verify:** the full commit/amend suites green; the one edited test
green; the unmerged-guard suite green.

---

## Phase 1 — Sequencer-state behavior

ONE implementor — the three items collide in `cherry_pick_cmd.go`,
`revert_cmd.go`, `coord_cmd.go`, `sequencer_continue.go` (1.1's actual
insertion point, also home of 1.3's revert sibling and the unpark
precedent), and
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
  failure never swallowed — the residue is named in the message
  `[plan]`. Stated acceptance `[user]`: the refusal already exits the
  general code, so a failed cleanup is EXIT-INDISTINGUISHABLE from the
  clean case — and refusals carry `payload:null` (recorded structural
  fact), so the named residue on stderr is the only signal; this is
  deliberately accepted (no new exit code) and the code comment says
  so. The comment also states that the mirrored sync is
  `read-tree --reset -u` — a destructive primitive that is a no-op on
  this path (after an empty compute the tree already equals HEAD's
  content) — so nobody later "improves" it away.
- Grounded facts: the empty pick leaves CHERRY_PICK_HEAD (safegit's
  own write) + AUTO_MERGE + MERGE_MSG; the empty revert leaves
  REVERT_HEAD + AUTO_MERGE + MERGE_MSG; `sequencer.Paths` covers both
  as supersets (absent paths are not errors). Merge is exempt by
  construction (allow-empty; an already-merged `--no-commit` parks
  nothing — probed) and `merge --no-commit` with real work MUST keep
  parking (the operator's explicit request).
- Interaction with Phase 2, stated for both implementors: the sync's
  `HEAD` treeish becomes unborn-safe INSIDE the shared helper in Phase
  2 (see the seam list there); this subphase needs no edit for it, and
  before Phase 2 no unborn path can reach the auto-clean.
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
  `pull` are already covered (verified behaviorally; the existing pins
  exercise only the PLAIN forms — this subphase ADDS the missing
  `merge --no-commit`-over-parked-state pin).
- REBASE JOINS `[user — ruled at the review]`: `safegit rebase` gains
  the same in-flight entry refusal (probed: a rebase over a parked
  revert on a clean tree exits 0 and STRANDS the revert's state files
  behind it, blocking every later commit). The check inserts in
  rebase's handler before git runs; git's own state-control forms
  (`rebase --continue`/`--abort`/`--skip`) stay exempt — they operate
  on the REBASE's own state. THE PREDICATE, stated outright `[plan —
  the earlier wording was ambiguous, and one reading breaks every
  ordinary rebase]`: refuse when `sequencer.Read` reports an in-flight
  state whose kind is NOT the rebase kind. Never implement this by
  declaring a rebase context through `coord.GuardInFlight` — that path
  hard-errors when a context is declared and nothing is in flight, so
  it would refuse every clean-repo rebase. Red-first: rebase over a
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
  empty-tree name is obtained per hash algorithm via ONE new
  `internal/git` helper using `hash-object -t tree --stdin` with empty
  input `[plan]` — the codebase bans path-taking hash-object helpers
  (bytes-not-paths policy) and `/dev/null` is not portable; probed
  identical on sha1 and sha256. The helper lives in `internal/git`
  because the seams straddle `internal/coord` and `package main` and
  all git plumbing goes through that package; it is the single home
  every seam imports. On this cold path the invocation is the right
  form (the hardcoded-table precedent's cost argument does not apply
  here — stated in code).
- The SAME substitution extends to THREE other HEAD-resolution seams
  `[plan — forced: fixing coord alone makes unborn picks WORSE, parking
  broken state]`: `sequencer_markers.go`'s first-parent constant (three
  uses plus the index-changed listing); `sequencer_preview.go`'s two
  HEAD reads; and `git.SyncMainIndexWithWorktree`'s `"HEAD"` treeish
  (`read-tree --reset -u HEAD`, fatal on unborn — probed). That third
  seam is substituted INSIDE the helper so every caller inherits it —
  including `refuseParkedRawGitShape` and 1.1's empty-park auto-clean;
  without it, 1.1's cleanup half fails on unborn ("Not a valid object
  name HEAD") and this phase's own revert-refuses-as-empty Verify
  cannot pass.
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
- The unborn REVERT refuses as empty `[plan]`: at HEAD today an unborn
  revert dies earlier, at the coordination check; probed on a build
  with the coord and markers seams patched, it then mints a nonsense
  empty root commit because the tree-unchanged comparison has no
  parent tree (`internal/commit/commit.go`'s `!isRootCommit` arm — a
  required edit this phase owns). Compare against the empty tree on
  the root path so the standing empty-result refusal (and 1.1's
  auto-clean) fire exactly as on a born branch.
- FRIENDLY PRE-FLIGHT REFUSALS `[user — the starting set of the
  adopted additive friendly-errors direction; wholesale wrapping of
  git errors is permanently ruled out]`: the unsupported unborn forms
  — `merge --no-commit`, `merge --no-ff`, `rebase`, and `bisect start`
  — are refused BY SAFEGIT before git runs, each with a clear message
  naming the situation (an unborn branch) and the way forward; exit is
  the general code. Honesty note (probed, git 2.55): raw git
  FAST-FORWARDS an unborn `merge --no-commit` at exit 0 — safegit's
  refusal there is a DELIBERATE DIVERGENCE caused by the parked-merge
  model (safegit always injects `--no-ff` when parking, and an unborn
  head cannot take a non-fast-forward); for unborn `rebase` and
  `bisect start`, git's own errors merely name the wrong thing.
  Catalog, same commit, markers flipped with the edits: the
  parked-merge entry's "`--no-commit` parks in every case an operator
  can reach" sentence is edited (unborn is now reachable and refused
  instead), and the friendly-refusal divergence is recorded.
- Grounded residue: a successful `switch -c` on a still-unborn repo
  records an ok navigation entry with an empty observed tip —
  harmless (the observed spelling is deliberately unconsumed), noted
  in the entry-writing comment.
- The dead "unreachable TODAY" comment on merge's unborn arm is
  deleted with the fix. The guide GAINS an unborn-branch section
  (none exists today — created, not edited) covering what works, the
  four friendly refusals, and the dirty-unborn refusal; the
  entry-writing comment in `coord_cmd.go` gains the empty-observed-tip
  note. Catalog, same commit, marker flipped with the edit: the
  dirty-tree entry's
  "Dirt is a diff of the working tree against HEAD" sentence gains the
  unborn clause (entry EDITED). No test pins the current refusal
  (verified — pure addition).
**Verify (red first):** integration tests on a `git init` +
`fetch <src> side:side` fixture (the clean unborn-with-a-branch shape;
`git checkout --orphan` would NOT produce it — its index keeps the old
content — while `git switch --orphan` would; the fetch shape is the
scenario's natural form): merge fast-forwards with a clean tree
and the fast-forward outcome; switch both forms; dirty unborn refuses
with both paths listed; commit still roots; pick roots with the
author preserved; a CONFLICTED unborn pick parks and concludes
end-to-end (the only path exercising three of the four substituted
first-parent uses); unborn `pull` fast-forwards (it shares the merge
path — the most useful unlock, pinned); unborn `reset --hard` works;
revert refuses as empty with no residue; each of the four friendly
refusals fires with its message and runs no git compute; previews
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
  convention); the request fields, the three intake functions
  (`escapingLinkTarget`, `noticeEscapingLinks`, `refuseEscapingLinks`)
  plus `resolveFiles`'s parameter, and the exit-29 constant — new
  spelling `NonPortableTarget` — all rename (registry precedent: exit
  25's rename); the registry meaning, generated
  table, and flag help reword to the portability rationale, and every
  prose mention writes "non-portable" hyphenated. KWARGS TRAP, stated
  so the rename cannot ship half-done: the `optBool` lookup key is
  derived from the flag name (`allow_escaping_targets` today) — the
  lookup key MUST change together with the flag, or the new flag is
  SILENTLY IGNORED (the fallback would always be returned). The eight
  doc-comment blocks arguing the old "escaping" framing are REWRITTEN
  to the portability rationale, not trimmed (the 0.1 precedent). The
  seven `*Escaping*` test functions and
  `wave2_symlink_escape_refusal_test.go` rename to the non-portable
  vocabulary (Appendix A; Phase 6's reconciliation classifies the
  renames as sanctioned).
- Both operator messages branch by shape `[plan — grounded: the
  current advice is actively misleading for absolute-inside]`: an
  absolute-inside target's refusal says the link is inside the
  repository but its absolute spelling will not resolve in a checkout
  at another path — spell it relative, or elect; the outside shape
  keeps its point-it-inside advice; the election notice rewords to the
  portability rationale covering both. MIXED SHAPES `[user]`: a commit
  naming offenders of both shapes gets ONE refusal with the offenders
  GROUPED BY SHAPE, each group followed by its own remedy — preserving
  the pinned names-every-offender property; the pin's wording
  assertions follow (Appendix A).
- MIGRATION CONSEQUENCE, stated for the changelog: a repository with a
  tracked absolute in-repo symlink starts refusing on the next commit
  that names it or sweeps it up by directory expansion — a breaking
  entry.
- Sanctioned rewrites (Appendix A): the absolute-inside pin inverts
  into the refusal test (its own comment says it is the one to
  rewrite); the wording assertions across the suite follow the new
  messages; the inside-target and traversing pins RETARGET to a
  distinctive phrase of the new notice — they currently assert the
  ABSENCE of "outside the repository" and would go silently vacuous
  under the reword; the directory-symlink pin stays green untouched;
  the mv carve-out pin updates only for the flag rename (mv reaches
  `resolveFiles` but passes no file specs, so its offender set is
  empty by construction — the carve-out is structural, verified).
- Docs, same commit where entry-overturning: the catalog entry is
  REWRITTEN including its heading (it names "leaves the repository"),
  its marker flipped, and its "listed for the review" sentence REMOVED
  — the one such sentence in the catalog; Phase 5's grep for it
  catches a miss here; the guide's symlink section INCLUDING its
  heading (it also says "leave the repository"), the commit flag-table
  row, the template
  bullet; the generated exit table regenerates (freshness test
  enforces); cli-commit and the schema regenerate later (Phase 5).
  CHANGELOG, same phase: the existing unreleased entry that ships
  `--allow-escaping-targets` and the "leaves the repository" class is
  edited IN PLACE (`rlsbl changelog edit --id`) to the new flag and
  class — 0.29.0's notes must never name a flag that never existed.
**Verify (red first):** absolute-inside refuses naming the portability
reason and the remedy; absolute-outside still refuses; the election
commits with the notice; relative-inside/traversing/dangling stay
committable; directory expansion still judges swept-up links; amend
covered; the registry and generated table current.

---

## Phase 4 — Allowlist flips and the mv payload

Two independent halves; one implementor (or two, the halves share no
files). After Phase 1 for the catalog file only.

### 4.1 The allowlist changes `[user]`
All rows live in `subset_allowlist.go`; the argv machinery already
handles the spellings involved (attached and detached strategy-option
values normalize to one name; the rebase flag takes its value attached
only, and NO value-flags entry may be added for it or it would swallow
a revision — the file's comment states this rule for optional-value
options generally; EXTEND it to name the rebase flag. Known accepted
limit, stated in the row comment: the short-cluster spelling
`-rno-rebase-cousins` refuses letter-by-letter rather than as one
token).
- Strategy OPTIONS become allowed on merge, cherry-pick, and revert
  (move the two-spelling row from refused to allowed in all three
  tables); strategy SELECTION stays refused everywhere (mind the
  grounded asymmetry: on pick/revert the short spelling is signoff and
  already allowed — preserve it). Probed: the compute stays ort under
  strategy options and AUTO_MERGE is written on all three verbs, so
  every conclusion protection sees what it sees today; the
  no-AUTO_MERGE refusal remains accurate as a selection signature.
  The new allowed rows carry the standard per-row comment; its
  rationale sentence: strategy options tune the ort compute's content
  decisions without changing authorship, parking, or any conclusion
  protection.
- THE PREVIEW FORWARDS, never re-refuses `[user]`, with the mechanism
  stated `[plan — the naive branch-deletion reading previews the
  UNOPTIONED tree, the banned silent-degradation shape]`: remove the
  strategy-option branch from the preview refusal AND thread the
  options into BOTH preview builders (`previewMerge` and
  `previewReplay`) — the merge-tree wrapper's extra-arguments
  parameter exists but has ZERO callers today, so the threading is the
  real work. Collect EVERY `-X`/`--strategy-option` occurrence (the
  argv reader's Find returns only the FIRST — a single-option read
  silently drops the second of `-X ours -X ignore-space-change`) and
  forward the operator's own spellings in their order. Forwarding is
  probed exact — the previewed tree is byte-identical to the real
  compute's under the same option, both spellings. `previewRefusal`'s
  doc comment ("safegit's merge-tree argv is fixed") goes false —
  rewrite it.
- Merging unrelated histories flips to REFUSED (delete its allowed row
  and the comment arguing for it; the refusal reason is the footgun,
  not a protection hole — and the way out for the legitimate
  once-per-lifetime import is stated: raw git computes with no-commit,
  safegit merge-continue concludes it). ALSO `[user — part of the
  friendly-errors starting set]`: a PRE-FLIGHT refusal when the merge
  has NO MERGE BASE (one merge-base read before compute). Without it,
  git's own "refusing to merge unrelated histories" message recommends
  `--allow-unrelated-histories` — the exact flag this row refuses; the
  pre-flight refuses first and names the documented way out instead.
  Red-first; any existing fixture merging unrelated histories is a
  sanctioned adjustment (Appendix A). New catalog entry (none exists),
  written deliberate at birth, covering the flag refusal and the
  pre-flight together.
- The rerere auto-update flag flips to REFUSED on all THREE tables
  `[plan — the ruled rationale is verb-independent; the ruling named
  merge because the question did]`: cache-driven auto-staging is
  remembered resolution by the back door; the refusal reason
  cross-references the existing rerere catalog entry rather than
  restating it. THE NEGATIVE SPELLING `--no-rerere-autoupdate` is
  ALLOWED on all three verbs `[user]` — it DISABLES the objected-to
  mechanism and is the operator's only per-run off-switch against the
  honored config key — and a small comment beside the rows states this
  deliberately confusing asymmetry's rationale `[user — the comment is
  part of the ruling]`. New catalog entry, written deliberate at
  birth: it honestly states the OPEN CONFIG ROUTE
  (`rerere.autoUpdate=true` in git config produces the same
  auto-staging and stays honored this round `[user]`), names the
  config-twin class (`merge.autostash` / `rebase.autoStash` twin the
  refused `--autostash` the same way), and points at
  `todo/git-config-audit-and-pin-table.md` for the deferred full
  treatment.
- Preserving merge topology through a rebase flips to ALLOWED (move
  the row; the replay runs entirely inside the one git-authored door,
  which is verb-scoped and already admits it — an optional row in the
  door-admission test is consistent). The rebase help string updates
  (it enumerates the refusal), which regenerates four doc surfaces at
  Phase 5's regen. Catalog, same commits, markers flipped with the
  edits: the EXISTING "Merge strategies and strategy options are
  refused" entry (provisional) is REWRITTEN including its heading —
  selection stays refused, options now allowed; this is the entry the
  flip overturns, and it must not reach Phase 5's sweep unedited; the
  rebase entry's refusal bullet and framing update; the allowed-sets
  table updates its rows for all three verbs AND rebase. The
  selection-vs-options split lands in all THREE guide paragraphs that
  state the refusal (the merge paragraph, the cherry-pick/revert
  paragraph, and the subset-overview paragraph of the commands guide),
  not only the merge one. CHANGELOG, same phase, in-place edits
  (`rlsbl changelog edit --id`): the existing merge entry lists `-X`
  among the shapes "refused by name" and the allowlist entry lists
  `--rebase-merges` among rebase's refusals — both edited to the new
  truth; 0.29.0's notes must not contradict their own entries.
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
  carry its origin instead of being dropped. THE SCHEMA `[plan]`:
  `mvPayloadSchema` declares `"moves"` with a three-member entry
  object in its required list — the reshape renames the property to
  `moved_records`, adds `origin`, and updates the required list (or
  emission-time validation fails); the entry-object fragment is
  FACTORED into ONE shared schema fragment consumed by commit's and
  mv's schemas, never duplicated (the single-authority rule).
- Probed end-to-end in a scratch copy: the reshape builds and the full
  suite is green; the preview reports the declared origin.
- Sanctioned rewrites (Appendix A), BOTH in the payload-moves test
  file — and one is a TRAP: the hook-strip mv test asserts an
  empty-length member, so a bare rename would leave it green while
  testing a member that no longer exists; retarget it to the new
  member DELIBERATELY. The preview test fails loudly and retargets
  with the origin assertion added.
- Changelog: mv has never shipped, so nothing breaks; the existing
  unreleased entry about reword/mv narrowing stays true (the pipeline
  still narrows — the deletion is of a reimplementation, not the
  behavior). COVERAGE `[plan]`: this subphase's commits are APPENDED
  to the existing mv feature entry's commit list via in-place edit
  (`rlsbl changelog edit`), the entry's batch-exclusion reason updated
  if the commit limit is exceeded — they must not be left for Phase 6
  to discover uncovered.
**Verify (red first on the retargeted tests):** real and preview mv
payloads carry the unified member with origin; the hook-strip case
reports only what the committed message carries; no reference to the
old member or helpers survives.

---

## Phase 5 — Documentation closure and regeneration

After Phases 1-4 (it closes over their doc debts). One implementor.

- THE ROSTER REWRITE: the catalog preamble's provisional roster has a
  factually wrong blanket clause (it declares every subset-boundary
  entry provisional while five entries there are deliberate AND the
  allowed-sets table carries no ruling line at all — six exceptions,
  not five). Rewrite the roster to reflect the end state: after this
  round, NO provisional entries remain. Phases 1-4 edit AND flip the
  entries their rulings touch (the same-commit marker rule in the
  preamble); THIS phase flips the text-unchanged remainder, FULLY
  ENUMERATED — no "en masse" clause: the autostash entry (its
  limit-awaits clause becomes the accepted-limit statement `[user]`),
  the DWIM paragraph's entry, the untrack-fence entry, the
  crash-window entry, the overwrite entry, the switch-c entry, the
  FETCH_HEAD entry, the fast-forward trio, squash, BOTH
  commit-step-options entries (two adjacent entries — the earlier
  "family" wording was ambiguous; name each in the edit), skip,
  reset-pathspec, pull-rebase, bisect-vocabulary, the dirty-tree entry
  Phase 2 does NOT edit (the one it does flips there), and the
  forwarded-command-line bookkeeping entry ("The forwarded command
  line is an allowlist, not a refusal list") — previously orphaned
  from every list. The identical provisional ruling line appears on a
  fixed set of entries — count the occurrences before the batch edit,
  assert the count, review the diff (the batch-edit rule). The
  preamble's "newly cataloged here is provisional until reviewed"
  sentence is REWRITTEN to permit entries born deliberate when they
  record a ruling made at review time (4.1's two new entries are
  exactly that).
- The any-committish sentence in the merge-sides entry is already
  deliberate and now user-ratified — no edit needed (verified).
- Accumulated template/guide debts from Phases 1-4 that were not
  same-commit items — including the NEW conventions bullet in the
  CLAUDE template recording the adopted friendly-errors direction
  `[user]`: safegit MAY add probe-backed pre-flight refusals where a
  known corner produces a misleading or hostile raw git failure, each
  individually cataloged; wholesale wrapping of git stderr is
  permanently ruled out. Then `--dump-schema` from a clean build with
  `GOWORK=off` (its own
  commit via rlsbl commit — ONCE, here), then bare `selfdoc gen`, then
  `selfdoc check` must be exit 0.
**Verify:** zero provisional ruling lines remain (grep count) AND zero
"listed for the review" phrases remain (second grep — Phase 3 removes
the one instance; this catches a miss); the
roster names no entry; every Phase 1-4 doc debt closed; regeneration
clean; selfdoc check green; the generated root files carry the new
texts.

---

## Phase 6 — Changelog and full verification

After Phase 5.

- Changelog entries for the round, APPENDED (plus the in-place edits
  earlier phases ordered). THE TYPE BASELINE IS RELEASE-RELATIVE
  `[user]`: an entry is breaking only if it refuses or changes input
  that worked in the LAST RELEASED version (v0.28.0); restrictions on
  surfaces introduced this cycle ride INSIDE those features' entries,
  named in the entry text. Therefore: breaking — the symlink
  portability widening ONLY (v0.28.0's commit accepted all symlinks;
  tracked absolute in-repo links start refusing); feature — strategy
  options allowed with forwarding previews, topology-preserving rebase
  allowed, unborn-branch support (its entry names the friendly unborn
  refusals), with the unrelated-histories refusal (flag + pre-flight)
  and the rerere-autoupdate refusal (positive refused, negative
  allowed) stated inside the merge/cherry-pick/revert feature-surface
  entries they belong to; fix —
  the compute-guard extension to the no-commit forms (the description
  states the silent-staging hole it closes), the empty-park
  auto-clean, the out-of-band leftover naming; no-user-facing — the
  plan file and its ruling commits (`--allow-batch` with a reason —
  they exceed the per-entry commit limit), the pins, the predicate
  hardening,
  the roster/doc work, and EVERY pre-round housekeeping commit sitting
  uncovered from the planning session (the registry policy doc, the
  coincidence pin and catalog sentence, the two promoted tools, the
  CONTRIBUTING correction, the closing baseline).
  Run `scripts/verify-coverage-partition` on the proposed
  clusters BEFORE writing any entry.
- The census regenerates (`scripts/exit-inventory` into the testdata
  file, committed with the established message) as the LAST content
  commit — the release hook blocks on its freshness.
- The full battery on the quiescent tree: build, vet, gofmt; full
  race suite; the stress run at its 40-minute budget; the
  released-dependency run (`GOWORK=off go test ./... -race`); selfdoc
  check; the changelog checks (`rlsbl check --tag changelog`).
- Reconciliation against `testdata/closing-baseline.txt`
  (`scripts/test-baseline` generates the fresh snapshot; its `--check`
  is a strict diff and cannot serve — the classification is the
  work): classify
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
  plan move to `todo/.done/`. THE DEFERRED-WORK LEDGER `[user]`: the
  triage ALSO writes one new todo (e.g.
  `todo/next-cycle-candidates.md`) collecting the deferred items now
  scattered in plan text, each SELF-CONTAINED — its context is COPIED
  INTO the ledger from `todo/campaign2-plan.md`'s text BEFORE that
  file moves to `.done/`; a pointer into a `.done` file is not context
  `[plan]`. The items:
  envelope-always adoption when the framework's error-payload channel
  ships; the doctorFix uninitialized-repo notice; the doctor-fix
  output's connective wording; the unminted conclusion execute-path
  worktree writes; the reset-over-parked-state and empty-compute-oplog
  observations (both ruled parked); and the friendly-errors
  probe-backed INVENTORY `[user]` — force each failure corner of the
  supported surface in scratch repos, capture git's actual message,
  grade it; everything graded misleading or hostile is a candidate
  pre-flight refusal, the evidence source for future additions to the
  adopted additive direction. Staying active, each its own file: the
  await files (conditional-consequential, dry-run-network — whose
  now-stale doc claim the triage notes — exit-codes-registry,
  effects-handle-closed-method-set), the contingent files
  (reader-writer-operation-lock, scrub-strict-mode-selector,
  push-streaming-restoration), reversibility-gaps,
  `todo/git-config-audit-and-pin-table.md` (the deferred git-config
  audit; 4.1's rerere catalog entry points at it), and
  `todo/pipeline-authored-rebase.md` (post-campaign work; cited by
  path from two shipped docs AND from production source —
  `internal/gitexec/authoring.go` — so it must not move). The deferred
  and obsolete subdirectories untouched.
- `rlsbl release init`; the release file: bump MINOR (0.28.0 to
  0.29.0), the two-campaign-plus-closing-round description and
  context DRAFTED BY THE ORCHESTRATOR AND APPROVED BY THE USER before
  the release file is committed.
- Post-audit coverage `[plan]`: changelog entries for Phase 7's
  remediation commits and THIS phase's own triage commits (the todo
  moves, the ledger) are added HERE, before the release file is
  committed; the census re-runs only if remediation touched exit sites
  (the release hook's freshness check is the backstop, not the plan).
- `actionlint` over the three workflows (installed; cheap insurance
  before their first-ever run on this history).
- ON THE USER'S GO: `rlsbl release run --no-allow-dirty --watch
  --approve-consequential`. The candidate push is the first push of
  the entire two-campaign history; a red CI verdict is fixed forward
  at the same version with `rlsbl release resume` — never a new
  version, never a manual push.
- Post-release (recorded, outside the release): the fleet sweep for
  legacy placeholder pre-pre-push hooks is SCRIPTED and run in
  ONE pass right after the new version installs `[user]` (the script
  finds every repo's legacy pre-pre-push file, `hook migrate`s or
  deletes the no-op placeholder per repo, and verifies a push probe
  per repo — the script discovers the set; no hand count) — no session
  ever hits a surprise hooks-not-migrated refusal; the
  dependent projects' parked todos unblock on their own triage;
  `.rlsbl/config.json`'s pre-release hook and `selfdoc.json`'s version
  are release-pipeline-owned and need no hand edits.

---

## Dependency spine

| Phase | Depends on | Notes |
|---|---|---|
| 0 | — | one item (the predicate); the rest was executed pre-round |
| 1 | — (the baseline is already committed, pre-round) | ONE implementor; internal order 1.1 then 1.2 then 1.3 is mandatory (fixtures) |
| 2 | 1 (catalog file; sequencer adjacency) | |
| 3 | 1-2 (catalog + guide files only) | parallelizable with 2 in code — sequence the shared docs |
| 4 | 1 (catalog + guide serialization with 2-3) | 4.1 and 4.2 share no files |
| 5 | 1-4 | sole roster writer; the ONE schema regen |
| 6 | 5 | census last; battery on the quiescent tree |
| 7 | 6 | the Fable-orchestrated audit |
| 8 | 7 + the user's release-file approval + the user's go | |

Sequential execution is the default; Phases 2, 3, and 4 collide on
the catalog file AND the commands guide (Phase 2's unborn section,
Phase 3's symlink section and regenerated exit table, 4.1's
paragraphs) — interleaving requires serializing edits to both files;
sequential is simpler and the default.

## Recorded observations — out of scope, not lost

- `reset --hard` silently clears parked sequencer state — git's own
  behavior under a non-authoring passthrough. Next-cycle candidate,
  surfaced to the user, deliberately not this round's scope. (The
  sibling rebase-over-parked-state observation was RULED INTO SCOPE
  and is 1.2's rebase item, not an observation.)
- No oplog entry is written for an empty compute today, before or
  after 1.1 — whether "computed and unwound" deserves an audit-trail
  entry is a separate future decision.
- The campaign-era plan files' internal claims are historical once
  triaged; the catalog and the code are the living authorities.

## Appendix A — sanctioned rewrites (a break not listed here is a
plan defect)

- 0.1: the pipeline-honors-declared-context test gains the
  shared-index base in its request literal.
- 1.1: BOTH fixture builders in the in-flight refusal test file
  re-fixture on raw git; the two state-producer pins INVERT (state
  gone, no abort advice); the seven consumer test FUNCTIONS
  re-fixture in place — the two pick/revert-refuse-over-parked tests,
  the pick preview variant, the two merge-over-parked tests, the
  pull-over-parked test's inline fixture, and the way-out test, whose
  three subtests are why an item count would mislead. The
  already-applied-refusal test keeps its "no change" phrase and stays
  green.
- 1.2: none beyond the new tests (the two no-commit-stays-passthrough
  pins run with nothing in flight and stay green — verified).
- 2: none (no test pins the unborn refusal — verified; pure addition;
  the friendly refusals replace raw git fatals no test asserts).
- 3: the absolute-inside-committed pin inverts into the refusal test;
  the wording assertions across the suite follow the new messages;
  the inside-target and traversing pins RETARGET to a distinctive
  phrase of the new notice (they assert the ABSENCE of "outside the
  repository" and would go silently vacuous under the reword — the
  same trap as the mv hook-strip test); the mv carve-out pin updates
  for the flag rename only; the seven `*Escaping*` test functions and
  the wave2 symlink file rename to the non-portable vocabulary; every
  test spelling the old flag or constant name updates mechanically
  with the rename.
- 4.1: the four strategy-option refusal rows leave their tests (the
  merge raw-shapes row, the merge preview row, the pick/revert
  selection test's option member and its doc comment); the
  pick/revert preview-refusal test takes `-s resolve` as its
  substitute unsupported option (strategy selection stays refused;
  its only case was the strategy option); the rebase refusal
  table drops its topology row and gains a positive sibling beside
  the allowed-form control; any existing fixture that merges
  unrelated histories adjusts for the new pre-flight refusal.
- 4.2: the two mv payload tests retarget to the unified member — the
  hook-strip one DELIBERATELY (it passes silently on a bare rename);
  the preview one gains the origin assertion.
- 5: the batch flip of the provisional ruling lines (count asserted
  before, diff reviewed after); the roster rewrite.
