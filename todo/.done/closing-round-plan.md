# The closing round: implementation plan

Self-contained in DECISIONS: every decision this round executes is
stated in full in THIS file, and a session with zero conversation
context can implement any subphase from this file plus the cited code.
Ambient discipline is NOT restated here `[user]`: the
auto-loaded rules files are the living
authority for standing discipline (safe deletion, no raw git, scratch
placement, red-first, permanent tests, batch-edit discipline, the
live-tree/quiescent-tree rule), and implementor briefs cite them
rather than this file. The plan executes the rulings from the
pre-release design review that followed campaign 2 (whose plan,
`todo/campaign2-plan.md`, is a completed historical record — where the
two disagree about what to do NEXT, this file wins). Anchors are claim
text (file + function + a distinctive phrase), verified against the
tree (most recently at HEAD `7728f364` — the anchor names the last
commit BEFORE this file's own latest edit, since a file cannot carry
its own commit's SHA; every commit past the anchor touches only
`todo/`, so the tree the claims describe is unchanged); the Catalog
actions
table and the enumerations below are extracted from the tree. Expect
line drift, never claim drift. Every factual claim in this file is
verified against the tree or explicitly marked not-re-verified; the
origin marks (`[user]`/`[plan]`) record DECISION origin, never
verification status.

**EXECUTION starts on the user's explicit go, IN A LATER SESSION** —
the user ruled that the session which wrote this plan does not execute
it. This file existing is not that go. THAT GO COVERS THE ENTIRE ROUND
`[user]`: once started, execution runs Phases 0-8 to the PUBLISHED
release with ZERO mid-run touchpoints — no approval stops, no waiting
on the user for anything. Also ruled at the same review:
ONE release, after this round completes (main is green and could ship
today, but the round is this release's own work — the
release-once-at-the-end principle governs; the v0.28.0 defect exposure
ends at that single release).

**Plan-specific discipline** (everything else lives in the rules
files):
- Commits via the INSTALLED safegit (single `-m`, plain paths, repo
  root) — the campaign rule the rules files do not carry. The
  installed binary is NOT refreshed during the round; the release
  installs the new version `[plan]`.
- The CAMPAIGN-ERA single-writer override on the divergences catalog
  is OVER; the standing rule is back in force: a change that overturns
  a catalog entry EDITS that entry in the same commit, never adds a
  second. ALL marker actions are enumerated in the Catalog actions
  table below — the single authority; no phase restates marker
  mechanics in prose `[plan]`.
- Verify style rules `[plan]`: a PIN (a test assertion) asserts the
  PRESENCE of a distinctive phrase of the current text, never the
  absence of old text; grep-based SWEEP steps in Verify blocks (a
  different instrument — zero-occurrence checks over files, not test
  assertions) are legitimate and use `git grep` (tracked files only —
  the on-disk build artifacts under `docs/_build/` would otherwise
  produce false hits).
- Falsified-texts sweep `[plan]`: before committing any behavior
  change, `git grep` the changed flag/behavior names across ALL
  TRACKED TEXT — code, docs prose, templates, the changelog JSONL,
  `.rlsbl/config.json`, help strings, doc comments — with generated
  files that a later regeneration rebuilds as the ONLY carve-out
  (deny-by-default scope; the carve-out includes
  `.selfdoc/manifest.json` — tracked, generated, rebuilt by `selfdoc
  gen` and the wholly-generated census — and note the commands guide
  contains generated table FRAGMENTS, the exit and doctor tables,
  which the carve-out covers while the guide's surrounding prose is
  swept).
  A subphase that changes a guard COUNT or an
  ENUMERATION rather than a name additionally sweeps on those phrases
  ("guarded twice", "both layers", "plain passthrough", "guarded
  passthrough", the command lists) — count changes
  carry no name to grep. And a subphase that changes an item's
  CLASSIFICATION additionally sweeps the stated CRITERION for that
  classification — the rule sentences beside every table it touches,
  and any claim of set-completeness ("every", "all", "the highest") —
  because moving an item across a line for a NEW reason falsifies the
  stated reason while every name in it stays spelled the same.
  Enumerate the hits and give each a
  disposition (edited here / owned by a named later phase / stays
  true). The per-phase lists in this file are ADVISORY CACHES of what
  the sweep finds — the sweep is the authority; a sweep hit absent
  from a list is an expected cache miss (disposition it and continue,
  no escalation). Appendix A is the opposite — see its header.
- `.rlsbl/changes/unreleased.jsonl` takes APPENDS and IN-PLACE EDITS
  only (`rlsbl changelog edit --id` preserves the line count; the
  batch-exclusion list keys entries by version+line, so inserting or
  deleting lines invalidates it — never do either). SANCTIONED HAND
  EDITS `[user]`: `changelog edit` modifies only type, description and
  user-facing — it cannot modify an entry's COMMIT LIST or create a
  batch exclusion for an existing entry, and this round needs both
  (the upstream capability todo is filed in the release tooling's
  repo). For exactly the entries this plan names, a careful hand edit
  of the JSONL (and of `.rlsbl/config.json` for exclusions) is
  sanctioned: line-count-preserving, via the counted-edit discipline
  (`scripts/counted-edit <spec-file>` — it takes a spec file of
  (path, old, new, expected-count) substitutions; dry-run default,
  `--apply` to execute, any count mismatch aborts the whole run;
  NOTE: it enforces OCCURRENCE counts — the line-count invariant is
  verified by reviewing the dry-run diff, not by the tool, so a
  replacement containing a newline must be spotted there), one
  commit per edit, and `rlsbl check --tag changelog` must pass
  afterwards. Edited lines must match rlsbl's own serialization
  byte-for-byte (compact separators, its key order, and
  ASCII-escaped non-ASCII — an em-dash in hand-written text would be
  re-escaped to `\uXXXX` by the next tool write; every current line
  conforms), or the next tool edit silently reformats them into
  diff noise; `.rlsbl/config.json` hand edits likewise preserve its
  exact dump shape (two-space indent, trailing newline) and use PLAIN
  ASCII only — rlsbl's two writers of that file disagree on non-ASCII
  escaping, so an em-dash in a hand-written reason would churn between
  spellings. The ban
  stands everywhere outside the named set.
- `.strictcli/schema.json` is regenerated ONCE, on the quiescent tree
  at the end of Phase 5, never per-phase — and BY A WORKING-TREE BUILD:
  `go run . --dump-schema` (the invocation rlsbl's own release step
  uses). The installed 0.28.0 binary's dump DIFFERS and would silently
  restore the pre-round surfaces into every generated doc; the
  installed-binary rule above covers COMMITS only. The dump writes the
  file and prints its path — the document itself never goes to stdout.
  Its embedded version member is
  safegit's OWN pseudo-version, which churns on every commit — hence
  once, at the end (the release pipeline re-dumps and patches that
  member itself, so the committed value is transient by design; the
  conclusion holds). The schema is independent of the `go.work` overlay
  (probed byte-identical with and without), so no GOWORK switch
  applies to the dump; `GOWORK=off` belongs only to Phase 6's
  released-dependency test run.

**Decision-origin marks.** `[user]` = ruled by the user in the review
(recommended-option picks weakly held per the
standing convention). `[plan]` = orchestrator resolution of a
discovered gap, reversible on request.

---

## Catalog actions — the single authority for divergences-catalog work

Every catalog edit and marker action of the round, in one table.
Entries are named by their headings (claim-text anchors; find by
searching the heading text). "flip" = the entry's provisional ruling
line becomes deliberate IN THE SAME COMMIT as that entry's text edit.
The identical provisional ruling line (the `provisional, newly
cataloged, awaiting review` spelling) appears on exactly the rows
marked provisional below; before Phase 5's batch flip, `git grep -c`
that spelling in `docs/divergences.md`, assert the count equals this
table's then-remaining provisional rows, review the diff (the
batch-edit rule).

| Entry (heading) | Marker now | Action | Phase |
|---|---|---|---|
| `safegit commit` refuses while an operation is in flight | provisional | text edit (rebase joins the refusing commands and the entry records its narrower predicate — spelled once, in 1.2; the "before they compute anything" sentence becomes true) + flip | 1.2 |
| An empty commit is refused; an empty merge is not | deliberate | text edit (the pick/revert refusal sentence now describes only the `-continue` door; the auto-clean fact added, counts-free: git offers its own ways out of an empty pick, safegit cleans up and the next command just works); marker stays deliberate | 1.1 |
| A dirty working tree refuses the guarded commands, untracked files included | provisional | text edit ("Dirt is a diff of the working tree against `HEAD`" gains the unborn clause) + flip | 2 |
| A parked merge stays parked, even when it could have fast-forwarded | provisional | text edit ("`--no-commit` parks in every case an operator can reach" — unborn is now reachable and refused instead) + flip | 2 |
| NEW: the friendly unborn pre-flight refusals | — | new entry, born DELIBERATE (records a review-time ruling) | 2 |
| `safegit rebase` is the one door where git authors the commits | deliberate | text edit (its closed what-safegit-adds enumeration gains the in-flight refusal in 1.2 and the unborn pre-flight in 2 — each phase edits in its own commit); marker stays deliberate | 1.2 + 2 |
| The symlink-target entry (its heading names "leaves the repository") | deliberate | REWRITTEN including its heading to the portability class; its "listed for the review" sentence REMOVED; marker stays deliberate | 3 |
| Merge strategies and strategy options are refused | provisional | REWRITTEN including its heading (selection stays refused; options now allowed) + flip | 4.1 |
| `rebase` is one upstream and a replay, and nothing else | provisional | text edit (refusal bullet and framing; the topology row moves to allowed) + flip | 4.1 |
| NEW: merging unrelated histories is refused (flag + pre-flight) | — | new entry, born DELIBERATE | 4.1 |
| NEW: the rerere auto-update refusal and its open config route | — | new entry, born DELIBERATE | 4.1 |
| A conclusion does not teach rerere the resolution it just made | deliberate | text edit (its "scope decision rather than an argument against rerere" sentence reconciled with the new refusal's remembered-resolution rationale); marker stays deliberate | 4.1 |
| The "What each guarded command allows" table | (no ruling line) | row edits for merge, cherry-pick, revert AND rebase, and the rule paragraph beneath it gains the second clause (a compute-step option may also be refused for a footgun or remembered-resolution reason) | 4.1 |
| An autostash is applied only when it belongs to the merge being concluded | provisional | text edit (the limit-awaits clause becomes the accepted-limit statement `[user]`) + flip | 5 |
| A working-tree write that would destroy a hand edit is refused | provisional | flip only | 5 |
| A conclusion whose commit already stands finishes the cleanup, and commits nothing | provisional | flip only | 5 |
| The forwarded command line is an allowlist, not a refusal list | provisional | flip only | 5 |
| `switch` takes a branch name, and only a branch name | provisional | flip only | 5 |
| `switch -c` starts the new branch where you are standing | provisional | flip only | 5 |
| A fetch that marked several branches is not a merge safegit will make | provisional | flip only | 5 |
| `--squash` is refused | provisional | flip only | 5 |
| Options that would hand the commit or the ref back to git are refused | provisional | flip only | 5 |
| Options that govern git's own commit step are refused, not ignored | provisional | flip only | 5 |
| `--skip` is refused | provisional | flip only | 5 |
| The fast-forward-only refusal is safegit's, not git's | provisional | flip only | 5 |
| A fast-forward is safegit's own ref move, and undo refuses it | provisional | flip only | 5 |
| `reset` takes a commit; the pathspec form is refused | provisional | flip only | 5 |
| `pull --rebase` is refused, naming the two commands | provisional | flip only | 5 |
| `bisect`'s subcommand vocabulary is its allowlist, and it takes no options | provisional | flip only | 5 |
| A path the commit stops tracking never pairs into an observed move | provisional | flip only | 5 |
| `reset` is refused when the tree is dirty, in exactly the modes that write to it | provisional | flip only | 5 |
| The catalog preamble AND the section-body duplicate | — | the roster blockquote ("**The provisional entries**, listed rather than counted, are these:") AND its companion clause ("every one of them is open at the review") rewritten to the end state; the SECOND instance of the same claim in the subset-boundary section BODY ("They are all open at the review, and overturning one is cheap…") rewritten too — no table row's entry edit covers it (the Phase 5 "open at the review" grep is the backstop); the blanket clause declaring every subset-boundary entry provisional DELETED OUTRIGHT (it is already false today — deliberate entries and a ruling-line-less table sit in its scope — and by this round's end any narrowed replacement would be vacuous; its absence joins Phase 5's grep list); the sentence "A behavior newly cataloged here is provisional until it has been reviewed as an entry, whatever its direction says" rewritten to permit entries born deliberate when they record a review-time ruling — this table's born-deliberate rows are exactly that (the quoted sentence is line-wrapped in the file; grep a partial phrase). ACCEPTED WINDOW: between a born-deliberate entry's creation (Phases 2 and 4.1) and this row's rewrite, that sentence is knowingly contradicted — pre-release, invisible, resolved here | 5 |

## Phase interactions (the seams where phases meet — checked pairwise)

- **1.1 and 2**: 1.1's empty-park auto-clean mirrors the unpark
  precedent, whose index/worktree sync resolves `"HEAD"`; Phase 2
  makes that resolution unborn-safe INSIDE the shared helper, so the
  auto-clean inherits it with no edit in 1.1. Before Phase 2, no
  unborn path can reach the auto-clean; after it, the unborn revert's
  auto-clean works — Phase 2's revert Verify depends on this.
- **2 and 4.1**: the no-merge-base pre-flight must not refuse unborn
  merges — its predicate (stated in 4.1) requires a resolvable HEAD.
- **2, 3, 4 and 5**: the Catalog actions table owns every marker
  action; Phase 5 owns the roster and the flip batch; file sharing per
  the spine note.
- **1.2, 3, 4 and 6**: in-place changelog work is accounted for in
  Phase 6's disposition list.

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
consequence sentence; the two promoted tools exist and behave as
described
(`scripts/counted-edit` — the batch-edit discipline as a tool, its
contract in the discipline block; `scripts/verify-coverage-partition`
— the exact-partition check Phase 6's changelog step uses);
CONTRIBUTING.md's release section states the real flow. One item
remains, the phase's single subphase:

### 0.1 The exemption predicate tightens to fail-closed `[plan]`
`internal/commit/sequencer.go`'s `concludesInFlightOperation` becomes
AND (a conclusion declares BOTH the sequencer context and the
shared-index base; a future half-configured request refuses instead of
silently bypassing the unmerged-index guard). Grounded: both production
construction sites (the `-continue` door and `concludeParkedOperation`)
already set both, so no behavior reachable through the CLI changes —
the amend/reword call sites flip from exempt-whenever-context-declared
to never-exempt, which IS observable through the package API (a
Sequencer with the parent-tree base over an unmerged index), though no
production caller constructs that shape today (grep-verified). Order
the package-level red-first unit pin in `internal/commit` for exactly
that shape (red under the OR, green under the AND) `[plan — the
red-first rule applied honestly; the pin is what catches a future
half-configured caller]`. The doc comment currently ARGUES the
OR and must be rewritten, not trimmed. One sanctioned test edit: per
Appendix A. Consequence stated in code `[plan]`: the
amend/reword call sites pass the parent-tree base hard-coded, so their
sequencer parameter can no longer exempt the unmerged guard — correct
(an amend is never a conclusion), and the comments near those call
sites gain a sentence stating the new fact (no old framing exists at
the call sites — the OR argument lives solely on the predicate's own
comment).
**Verify:** the full commit/amend suites green; the one edited test
green; the unmerged-guard suite green; the new package-level pin
red-first then green.

---

## Phase 1 — Sequencer-state behavior

ONE implementor — the three items collide in `cherry_pick_cmd.go`,
`revert_cmd.go` (home of 1.3's revert sibling), `coord_cmd.go`,
`sequencer_continue.go` (1.1's actual insertion point and home of the
unpark precedent), and
`internal/test/inflight_compute_refusal_test.go`, and the first item
rewrites the fixtures the second item's tests need. Internal order is
mandatory: 1.1, then 1.2, then 1.3.

### 1.1 The empty park auto-cleans `[user]`
When safegit's own compute produces a no-change result, it removes the
state it just parked and the refusal says so.
- The single insertion point is `concludeParkedOperation`'s
  tree-unchanged branch in `sequencer_continue.go` (its only three
  callers are the restructured cherry-pick, merge, and revert — a
  precise scope boundary; `gitDir` is in scope). The FIRST of the
  round's two declared signature changes (the second is 4.1's
  `previewRefusal`) `[plan — forced by the per-branch message]`: the
  `onEmpty` callbacks gain the cleanup outcome as a parameter (two
  callbacks exist — cherry-pick's and revert's; merge sets none and
  its caller literal compiles unchanged), because the abort advice is
  hard-coded INSIDE the
  callbacks and must branch on an outcome their caller computes — a
  zero-argument callback cannot. Mirror the unpark precedent
  (`refuseParkedRawGitShape`): `sequencer.Cleanup` for the operation's
  kind plus the index/worktree sync — THROUGH THE HELPER
  `git.SyncMainIndexWithWorktree`, exactly as the precedent calls it
  (the helper carries the tracked-but-gitignored protection; a raw
  `read-tree` call would silently lose it) — both halves attempted, a
  cleanup failure never swallowed — the residue is named `[plan]`.
- THE MESSAGE IS TRUTHFUL PER BRANCH `[plan — the precedent's own
  shape: its undone-state sentence prints only when both halves
  succeeded]`: on cleanup SUCCESS the refusal says the parked state
  was cleaned and gives no abort advice (there is nothing to abort);
  on cleanup FAILURE the operation IS still in flight — the residue
  files are named AND the abort advice REMAINS, because it is then
  correct. Residue detail prints before the one-line refusal (the
  `onEmpty` callbacks in the command files emit it; state the order in
  code). Stated acceptance `[user]`: both branches exit the general
  code — a failed cleanup is EXIT-INDISTINGUISHABLE from the clean
  case, and refusals carry `payload:null` (recorded structural fact),
  so the message is the only signal; deliberately accepted (no new
  exit code), and the code comment says so — INCLUDING that this
  deliberately diverges from the precedent, which degrades to the
  general code on a failed cleanup half where this path already sits
  there. The comment also states
  that the sync underneath is `read-tree --reset -u` — a destructive
  primitive that is a no-op on this path (after an empty compute the
  tree already equals HEAD's content) — so nobody later "improves" it
  away.
- Grounded facts: the empty pick leaves CHERRY_PICK_HEAD (safegit's
  own write) + AUTO_MERGE + MERGE_MSG; the empty revert leaves
  REVERT_HEAD + AUTO_MERGE + MERGE_MSG; `sequencer.Paths` covers both
  as supersets (absent paths are not errors). Merge is exempt by
  construction (allow-empty; an already-merged `--no-commit` parks
  nothing — probed) and `merge --no-commit` with real work MUST keep
  parking (the operator's explicit request).
- Interaction with Phase 2: see the Phase interactions section (the
  sync helper's unborn safety arrives there; no edit here).
- The refusal messages are rewritten; keep the phrase "no change"
  (`TestCherryPickOfAnAlreadyAppliedCommitIsRefused` asserts it). The `-continue` door's OWN
  empty refusal (`refuseEmptyConclusion`) is NOT touched — the state
  there is the operator's, not this invocation's; state the two-door
  distinction in code. Comments this item falsifies, rewritten (not
  trimmed) in the same commit `[plan]`:
  `cherry_pick_cmd.go`'s "the state it left is still in flight"
  comment, `refuseEmptyRevert`'s equivalent in `revert_cmd.go`, and
  `cherry_pick_restructure_test.go`'s "names the way out of the state
  git left" comment.
- Sanctioned fixture rewrite (the round's largest): per Appendix A,
  which carries the probe facts.
- Catalog: per the Catalog actions table. Changelog: owned by Phase 6
  (the hybrid-ruling sentence and the no-user-facing coverage).
**Verify (red first):** after an empty pick/revert — no sequencer
residue (reuse `assertNoSequencerResidue`), clean status, HEAD unmoved,
exit unchanged, message contains "no change" and, on the success path,
a distinctive phrase of the new cleaned-up sentence (a PRESENCE pin,
per the Verify style rule), and THE POINT: the next `safegit commit`
succeeds.
The merge `--no-commit` park preserved.

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
  — a THIN WRAPPER distinguishes them from the
  state-control sites (not a parameter — `runGuardedPassthrough`'s
  signature stays, keeping the round's declared-signature-change count
  true), which MUST stay exempt (`--abort`/`--quit` are
  the way out; probed working and staying so). `merge --no-commit` and
  `pull` are already covered (verified behaviorally; the existing pins
  exercise only the PLAIN forms — this subphase ADDS the missing
  `merge --no-commit`-over-parked-state pin).
- REBASE JOINS `[user — ruled at the review]`: `safegit rebase` gains
  the same in-flight entry refusal (probed: a rebase over a parked
  revert on a clean tree exits 0 and STRANDS the revert's state files
  behind it, blocking every later commit). The check inserts in
  rebase's handler — PLACEMENT stated `[plan — every sibling in-flight
  check sits inside the operation lock, and mv's comment states the
  rule: read inside the lock so no passthrough can start an operation
  between the check and the work]`: INSIDE the operation lock,
  immediately after `coordGuard` and before `readOplogPosition` — so
  no oplog entry is written for the refusal (like every pre-git
  refusal) and a `--dry-run` rebase refuses too, matching the
  `--no-commit` forms. The refusal exits `exitcode.CoordinationBusy`
  and renders through the same `coord` refusal text as the sibling
  refusals, so it cannot name a different way out `[plan]`. THE
  PREDICATE, stated outright
  `[plan — the alternative reading breaks
  every ordinary rebase]`: refuse when `sequencer.Read` reports an
  in-flight state whose kind is NOT the rebase kind — never implement
  this by declaring a rebase context through `coord.GuardInFlight` —
  that path hard-errors when a context is declared and nothing is in
  flight, so it would refuse every clean-repo rebase. The
  state-control exemption (`rebase --continue`/`--abort`/`--skip`) is
  a CONSEQUENCE of the predicate — mid-rebase state reports the rebase
  kind, so those forms pass by construction; no second argv-based
  exemption list `[plan]`. Red-first specs: in the Verify block.
- Catalog: per the Catalog actions table.
- Changelog, same phase, in-place edit `[plan]`: entry id
  `18cee9d5500c2822ba19eac36bdb4ba99c45ea0f13bb5f76` ("merge,
  cherry-pick, revert and pull refuse to compute over an operation git
  already has in flight") is edited to name rebase and the
  `--no-commit` forms — the same edit makes its existing claim true.
- FALSIFIED SURFACES, enumerated from the tree (this change alters a
  guard COUNT and a command ENUMERATION — the sweep runs on the
  phrases per the discipline block; no name exists to grep).
  All edited in THIS phase, same commits as the code, stating rebase's
  NARROWER predicate (above): the rebase help string's
  "guarded twice before git runs" clause in `main.go`, AND the
  identical "`--abort, --quit and --no-commit` stay plain
  passthroughs, because they author nothing" closing clause of BOTH
  `cherryPickHelp` and `revertHelp` (false once `--no-commit` takes
  the entry refusal; `--abort`/`--quit` genuinely stay plain — the
  rewrite says exactly that; all three strings regenerate their doc
  surfaces at Phase 5's regen); in
  `docs/commands-guide.md` —
  the section's INTRO content (its "two coordination layers" TITLE
  stays — the section already documents the compute commands' third
  check under that title today, so rebase joining the set adds no new
  falsehood; the title's pre-existing imprecision rides the
  next-cycle docs-architecture ledger item, and the verbatim
  cross-references to it stay untouched), the "An in-flight operation
  does NOT by itself refuse
  one of these commands" paragraph with its
  commands-that-COMPUTE enumeration (rebase joins it — three separate
  paragraphs, not one; note the compute-forms sentence is false TODAY,
  since `--no-commit` routes to the unguarded passthrough: this edit
  fixes a live doc bug, not just an enumeration), rebase's
  own "Coordination guard, both layers" bullet — the vocabulary
  decision for all six identical bullets, stated once: they name the
  TWO-LAYER coordination guard specifically, so only rebase's is
  falsified (it gains a third, different guard) and the other five
  stay true and untouched — and
  the "`safegit rebase --continue` passes through to git untouched"
  line, which gains its parked-non-rebase-state qualification;
  `docs/_CLAUDE.md`'s guarded-passthroughs "same two guards" bullet;
  `docs/_README.md`'s "two coordination guards" sentence;
  `docs/concurrency-guide.md`'s in-flight paragraph (whose own example
  is rebase); and the falsified COMMENTS: `coord_cmd.go`'s
  entry-check doc comment ("the three RESTRUCTURED commands … owe
  before they compute anything" — already imprecise today, pull calls
  it too), `cherry_pick_cmd.go`'s "both for the same reason … they
  author nothing" comment AND its exact twin in `revert_cmd.go` ("Two
  routes stay guarded passthroughs, and both for the same reason"),
  the cherry-pick allowlist row comment for `-n`/`--no-commit` in
  `subset_allowlist.go` ("authors nothing, so it stays a guarded
  passthrough" — the rationale no longer exempts, three lines from
  the state-control rows' identical justification),
  `revert_restructure_test.go`'s
  "therefore stay plain passthroughs" comment, the commands guide's
  cherry-pick allowed paragraph's "`-n`/`--no-commit` (… it authors
  nothing)" twin of that row comment, and the exit-code
  registry's `CoordinationBusy` doc comment ("merge, cherry-pick,
  revert and pull refuse outright there too, in the form that
  COMPUTES an operation" — the sentence stays literally true but the
  enumeration goes incomplete; it gains rebase's clause with its own
  stranded-state rationale and the kind-scoped predicate).
**Verify (red first, on the re-fixtured raw-git parked states):**
`cherry-pick --no-commit` over a parked revert and `revert --no-commit`
over a parked pick each exit with the coordination-busy code, name the
in-flight operation and the safegit conclusion command, move no HEAD,
stage nothing, write no state file of their own, and leave the parked
state intact; a `--dry-run` variant refuses identically; with nothing
in flight both forms still work; rebase over a parked revert refuses
(red-first) naming the revert and the way out while an ordinary
rebase still runs and a conflicted rebase's own --continue reaches
git; and the added `merge --no-commit`-over-parked pin is green on
arrival (the behavior already holds).

### 1.3 The out-of-band refusal names its leftovers `[user]`
`cherry_pick_cmd.go`'s state-changed-during-compute branch (the
sibling of revert's already-fixed one, in `revert_cmd.go`) currently
prints one line naming neither the CHERRY_PICK_HEAD safegit itself
wrote nor the staged result. Rewrite it to name the leftovers — a NEW
shape, fully specified here (the revert sibling, whose "something else
started or cleared a git operation in this worktree while it ran"
wording is the LOCATION anchor, names the in-flight kind and the
staged result but no state file — it is not a template): name the
leftover state file(s) including safegit's own CHERRY_PICK_HEAD,
report the staged result,
leave everything in place (an out-of-band writer is demonstrably
active; report, never destroy — the established stance), point at
`git status`. Grounded reproduction device for the red test: a
`post-index-change` hook that writes MERGE_HEAD fires during the
compute (probed); the test asserts the exit, the named file, and that
the file still holds the picked commit. Sweep: none (verified — no
shipped doc quotes the one-line message).
**Verify (red first):** the reproduction; the message names
CHERRY_PICK_HEAD and the staged content; nothing rolled back.

---

## Phase 2 — Unborn-branch support `[user — support now]`

After Phase 1 (serialized per the spine note; sequencer adjacency).
One implementor.

- The coordination check: when HEAD does not resolve (the cheap test
  is `rev-parse --verify --quiet` — without `--quiet` it is loud and
  exits 128 instead of 1; probed), diff against the EMPTY TREE instead
  —
  probed as semantically identical (tree-vs-worktree, reports staged
  and unstaged-tracked changes in the same parseable shape). The
  empty-tree name is obtained per hash algorithm via ONE new
  `internal/git` helper using `hash-object -t tree --stdin` with empty
  input `[plan]` — the codebase bans path-taking hash-object helpers
  (bytes-not-paths policy) and `/dev/null` is not portable; probed
  identical on sha1 and sha256. The helper lives in `internal/git`
  because the seams straddle `internal/coord` and `package main` and
  all git plumbing goes through that package; it is the single home
  every seam imports. The invocation form is right on this cold path —
  hardcoding the two known empty-tree names per algorithm would only
  avoid one subprocess, and the invocation self-adapts to any future
  hash algorithm; the helper's comment states this, AND why
  `git.MkTree(ctx, nil)` — which also yields the empty tree — is not
  reused: mktree WRITES the object (the preview-quarantine class),
  while `hash-object` without `-w` writes nothing `[plan]`.
- The SAME substitution extends to two more HEAD-resolution seams, and
  one seam gains a new arm instead `[plan — forced: fixing coord alone
  makes unborn picks WORSE, parking broken state]`:
  TWO distinct sites in `sequencer_markers.go` — the `firstParent`
  constant (its uses inside `verifyMarkers`) AND the separate
  hard-coded `"HEAD"` in `verifiablePaths`' index-changed listing (two
  edits, not one) — and `git.SyncMainIndexWithWorktree`'s
  `"HEAD"` treeish (`read-tree --reset -u HEAD`, fatal on unborn —
  probed) — the latter substituted INSIDE the helper so every caller
  inherits it, WITH THE SCOPE STATED `[plan — one misreading is
  destructive]`: the substitution applies ONLY when the caller's
  treeish is literally `"HEAD"` and HEAD is unborn; the helper takes
  its treeish from the caller (merge passes real SHAs through it), and
  an unresolvable non-HEAD treeish stays a HARD error — substituting
  the empty tree for failed resolution generally would
  `read-tree --reset -u` the tracked files away. Inheriting callers:
  `refuseParkedRawGitShape`, 1.1's
  empty-park auto-clean (see Phase interactions), and the post-scrub
  sync
  (`rewrite_result.go`'s literal-HEAD call — harmless on unborn, where
  no history exists to rewrite; named so this list is exhaustive). The MERGE preview is the new-arm seam:
  its HEAD read feeds `git.IsAncestorOf`, which needs a COMMIT (probed:
  merge-tree without a merge-base rejects ANY tree — both sides must
  dereference to commits), so an
  unborn merge preview SHORT-CIRCUITS — it is by definition a
  fast-forward and answers without computing; the REPLAY preview
  substitutes the empty tree for ours, which merge-tree accepts WHEN a
  merge-base is given (probed) — and a replay always has one.
- What then works (all probed on a patched build): `switch -c` and
  `switch <branch>`; `merge <branch>` takes the already-built unborn
  fast-forward arm (ZeroSHA-pinned CAS, synced tree, the
  fast-forward oplog outcome); a dirty unborn tree refuses at the
  coordination-busy code with the listing; `cherry-pick <c>` works
  end-to-end (a root commit with the source author preserved);
  `pull` inherits the merge path; `reset --hard` works.
- The unborn REVERT refuses as empty `[plan]`: at HEAD today an unborn
  revert dies earlier, at the coordination check; probed on a build
  with the coord and markers seams patched, it then mints a nonsense
  empty root commit because the tree-unchanged comparison has no
  parent tree (`internal/commit/commit.go`'s `!isRootCommit` arm — a
  required edit this phase owns). Compare against the empty tree on
  the root path so the standing empty-result refusal (and 1.1's
  auto-clean) fire exactly as on a born branch. Constraints on that
  edit, stated `[plan]`: the `!req.AllowEmpty` guard SURVIVES — the
  fresh-repo `--allow-empty` behavior (an empty ROOT commit, tree =
  the empty tree) is probed real but currently UNPINNED (verified: no
  test in the suite exercises it), so this phase writes that pin
  FIRST, green on arrival, and the comparison edit must keep it green;
  `parentTree` keeps its empty-string
  spelling on the root path for every OTHER consumer (`git.DiffTree`
  and the inference path key off it; `refOrEmptyTree` keys off
  `isRootCommit`, not the spelling — only the
  tree-unchanged comparison changes); and the arm's "Root commits are
  never empty" comment states the premise being overturned and is
  rewritten, not trimmed.
- FRIENDLY PRE-FLIGHT REFUSALS `[user — the starting set of the
  adopted additive friendly-errors direction (Phase 5 records its
  canonical wording in the template)]`: the unsupported unborn forms
  — `merge --no-commit`, `merge --no-ff`, `rebase`, and `bisect start`
  — are refused BY SAFEGIT before git runs, each with a clear message
  naming the situation (an unborn branch) and the way forward; exit is
  the general code. THE MERGE-FAMILY SITES, stated `[plan]`: the
  refusal predicate is "HEAD unborn AND (`req.noFF` OR `req.park`)",
  asked at THREE sites — merge's dry-run branch, the shared
  `performMerge` BEFORE the fast-forward arm, and `runPull` beside its
  existing pre-fetch in-flight check, whose own comment states the
  governing precedent ("Before the FETCH, not merely before the merge
  step") — so (a) PULL refuses BEFORE the network round-trip (its
  no-ff mode sets the very field that skips the unborn fast-forward
  arm and would otherwise fetch and THEN die on the raw fatal; the
  message names the operator's own spelling — `--merge-strategy no-ff`
  for pull, `--no-ff`/`--no-commit` for merge; pull's dry run stays
  silent about the merge step, its documented shape), and (b) merge's
  preview refuses IDENTICALLY to the real run — the unborn
  short-circuit ("it is by definition a fast-forward") is reached only
  by the PLAIN unborn merge, never by a command line the real run
  refuses, preserving the pinned preview-matches-reality property.
  OPLOG `[user]`: the real-run sites (`performMerge`, `runPull`)
  append a FAILED entry — unborn-ness is a fact about where the branch
  stands, the `--ff-only` neighbor's stated rule — a deliberate split
  from the octopus mirror, which records nothing because it is purely
  about what was typed; the dry-run site records nothing (previews
  never write the oplog). 1.2's rebase refusal stays entry-less (it is
  about ANOTHER operation's state, the class every pre-git refusal
  leaves unrecorded). Honesty note (probed, git
  2.55): raw git
  FAST-FORWARDS an unborn `merge --no-commit` at exit 0 — safegit's
  refusal there is a DELIBERATE DIVERGENCE caused by the parked-merge
  model (safegit always injects `--no-ff` when parking, and an unborn
  head cannot take a non-fast-forward); for unborn `rebase` and
  `bisect start`, git's own errors merely name the wrong thing.
- Grounded residue: a successful `switch -c` on a still-unborn repo
  records an ok navigation entry with an empty observed tip, and a
  `switch <branch>` FROM an unborn HEAD records an empty observed
  parent — both harmless (the observed spelling is deliberately
  unconsumed); the entry-writing comment in `coord_cmd.go` gains a
  note covering BOTH spellings.
- The FINAL PARAGRAPH of the "unreachable TODAY" comment block on
  merge's unborn arm is deleted with the fix (the block's earlier
  paragraphs carry live rationale for the ZeroSHA pin and stay). The
  COMMANDS GUIDE gains an unborn-branch section (none exists today —
  created, not edited) covering what works, the friendly refusals, and
  the dirty-unborn refusal.
- FALSIFIED SURFACES beyond the new section, enumerated from the tree
  (phrase-swept per the discipline block): the parked-merge sentence's
  guide twin (`docs/commands-guide.md`'s "`--no-commit` computes and
  PARKS … every case an operator can reach" bullet) is edited with the
  catalog copy; the dirt-is-`git diff HEAD` claim's two prose twins
  (the commands guide's guarded-commands section and
  `docs/concurrency-guide.md`) gain the unborn clause alongside the
  catalog's; the commands guide's "`--no-ff` elects a merge commit
  even where a fast-forward was available" bullet gains its unborn
  exception (the sibling bullet directly ABOVE the `--no-commit`
  one); the commands guide's existing root-commit-undo sentence — the
  one place it already sends a reader into unborn territory — gains a
  cross-reference to the new unborn section; and
  `mergeHelp`'s "leaves it parked even when it is clean"
  clause, the bisect help, AND the rebase help each gain one
  unborn-refusal mention (rebase's help is already open for 1.2's and
  4.1's edits — one clause keeps the three refused commands
  symmetrical; all regenerate their doc surfaces at Phase 5's regen).
- Catalog: per the Catalog actions table. No test pins the current
  unborn refusal (verified — pure addition).
**Verify (red first):** integration tests on a `git init` +
`fetch <src> side:side` fixture (the clean unborn-with-a-branch shape;
`git checkout --orphan` would NOT produce it — its index keeps the old
content — while `git switch --orphan` would; the fetch shape is the
scenario's natural form): merge fast-forwards with a clean tree
and the fast-forward outcome; switch both forms; dirty unborn refuses
with both paths listed; commit still roots; pick roots with the
author preserved; a CONFLICTED unborn pick parks and concludes
end-to-end — fixture stated because the shape is non-obvious `[plan,
probed]`: an add-only pick onto unborn succeeds cleanly, so the
conflict must be MODIFY/DELETE (source history: one commit creates the
file, a second commit modifies it; fetch, then pick the SECOND commit
onto the unborn head), and the resulting index holds stages 1 and 3
ONLY — no stage 2 — which the `--resolve` assertions must expect; this
is the only path exercising the substituted first-parent uses. Unborn
`pull` fast-forwards (it shares the merge path — pinned); unborn
`reset --hard` works; revert refuses as empty with no residue; each
friendly refusal fires with its message and runs no git compute —
including unborn `pull --merge-strategy no-ff` refusing BEFORE the
fetch, and `--dry-run merge --no-ff` refusing identically to the real
run; previews work. Unit tests in `internal/coord` for the unborn
clean/dirty arms.

---

## Phase 3 — Symlink portability refusal `[user]`

Independent of Phases 1-2 in code; after them for the shared doc
files (serialized per the spine note). One implementor.

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
  plus `resolveFiles`'s parameter, and the exit-29 constant
  (`EscapingSymlinkTarget` today — new spelling `NonPortableTarget`; a
  rename, not a new code: the recovery path is unchanged, per the
  registry's one-code-per-recovery-path policy) all rename; the
  registry meaning, the generated table (`scripts/gen-exit-table`;
  freshness test enforces), and the flag help reword to the
  portability rationale, and every prose mention writes "non-portable"
  hyphenated. KWARGS TRAP, stated so the rename cannot ship half-done:
  the `optBool` lookup key is derived from the flag name
  (`allow_escaping_targets` today) — the lookup key MUST change
  together with the flag, or the new flag is SILENTLY IGNORED (the
  fallback would always be returned). The comment sites arguing the
  old "escaping" framing are REWRITTEN to the portability rationale,
  not trimmed (the 0.1 precedent) — enumerated from the tree: in
  `internal/commit/intake.go`, the
  `escapingLinkTarget`, `noticeEscapingLinks`, and
  `refuseEscapingLinks` doc comments plus the refusal text, the
  `allowEscapingTargets` parameter doc on `resolveFiles`, and the
  expansion-members comment; the `AllowEscapingTargets` field docs in
  `internal/commit/commit.go` and `internal/commit/amend.go`; the
  root-package plumbing in `commit.go` (`runCommit`/`runCommitAmend`
  parameters and call sites) and BOTH the flag's help string AND its
  separate doc comment in `main.go`; the
  registry doc comment and table row in
  `internal/exitcode/exitcode.go`; and — hand-written surfaces no
  regeneration touches — `docs/integration-guide.md`'s
  "escaping-symlink refusal (exit 29)" mention and the wave2 test
  file's header comment, rewritten WHOLE (beyond naming
  `noticeEscapingLinks` it describes the pre-refusal behavior as
  current, claims the election flag does not exist yet, and names a
  test function that no longer exists).
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
  that names it or sweeps it up by directory expansion — breaking
  relative to v0.28.0, whose commit accepted all symlinks.
- CHANGELOG, same phase, in-place edit `[plan]`: the existing breaking
  entry id `18cee1b9909303d607c1f2850b24418980be50178da2237e` (it
  ships `--allow-escaping-targets` and the "leaves the repository"
  class) is edited to the new flag, the new class, and the migration
  consequence — 0.29.0's notes must never name a flag that never
  existed.
- Catalog: per the Catalog actions table (Phase 5's grep additionally
  catches a missed sentence-removal). The guide's
  symlink section is rewritten INCLUDING its heading (it also says
  "leave the repository"); the commit flag-table row and the template
  bullet update; the generated surfaces regenerate at Phase 5's regen.
- Sanctioned rewrites: per Appendix A.
**Verify (red first):** absolute-inside refuses naming the portability
reason and the remedy; absolute-outside still refuses; a mixed-shape
commit gets one grouped refusal naming every offender; the election
commits with the notice; relative-inside/traversing/dangling stay
committable; directory expansion still judges swept-up links; amend
covered; the registry and generated table current.

---

## Phase 4 — Allowlist flips and the mv payload

Two independent halves; one implementor (or two, the halves share no
files). After Phases 1-3 for the shared catalog and guide files
(serialized per the spine note).

### 4.1 The allowlist changes `[user]`
All rows live in `subset_allowlist.go`; the argv machinery already
handles the spellings involved (each flag's attached and detached
spellings collapse to its one name, but `-X` and `--strategy-option`
remain TWO distinct names — the forwarding collects both, never
`Find("-X")` alone; the rebase topology flag takes its value
attached only, and NO value-flags entry may be added for it or it
would swallow a revision — that rule is stated generally in
`sequencer_argv.go`'s value-flags comment: EXTEND it to name the
rebase flag, and in the same edit FIX the `--gpg-sign` row that
currently violates it (`valueFlags["rebase"]` lists `--gpg-sign`, an
attached-only optional-value flag; no user-visible bug — it is refused
as unlisted first — but the map contradicts its own rule) `[plan]`.
Known accepted limit, stated in the row comment: the short-cluster
spelling `-rno-rebase-cousins` refuses letter-by-letter rather than as
one token.
- Strategy OPTIONS become allowed on merge, cherry-pick, and revert
  (move the two-spelling row from refused to allowed in all three
  tables); strategy SELECTION stays refused everywhere — by name on
  the three verbs, and on rebase as an unlisted subset-law refusal (no
  named row; the git-authored door has no emission-based rationale to
  state). Mind the
  grounded asymmetry: on pick/revert the short spelling is signoff and
  already allowed — preserve it. Probed: the compute stays ort under
  strategy options and AUTO_MERGE is written on all three verbs, so
  every conclusion protection sees what it sees today; the
  no-AUTO_MERGE refusal remains accurate as a selection signature.
  The new allowed rows carry the standard per-row comment; its
  rationale sentence: strategy options tune the ort compute's content
  decisions without changing authorship, parking, or any conclusion
  protection. CRITERION STATEMENTS this phase's classification moves
  falsify (the classification sweep's finds), edited same-commit:
  `mergeSubset`'s type doc ("an option that changes how the message is
  DRAFTED is honored … while one that changes how git would COMMIT is
  not") and the catalog's rule paragraph beneath the
  allowed-sets table, which states the same rule in its own words —
  both gain the second clause: a compute-step
  option may ALSO be refused for a footgun or remembered-resolution
  reason (unrelated histories; rerere auto-update); and
  `rebaseSubset`'s doc comment's allowed-set enumeration gains the
  topology flag (fixing its pre-existing `--autostash` omission in the
  same edit); and the preview test's set-completeness comment
  ("safegit's merge, cherry-pick and revert do not implement those
  options at ALL any more" — falsified by the allowance) rewrites with
  its test.
- THE PREVIEW FORWARDS, never re-refuses `[user]`, with the mechanism
  stated `[plan — the naive branch-deletion reading previews the
  UNOPTIONED tree, the banned silent-degradation shape]`: remove the
  strategy-option branch from the preview refusal AND the two
  strategy-SELECTION branches beside it — already unreachable today
  (the allowlist refuses before the dry-run branch on all three verbs;
  verified) and unreachable after the flip, so the dead-surface rule
  applies — and thread the
  options into BOTH preview builders (`previewMerge` and
  `previewReplay`) — the merge-tree wrapper's extra-arguments
  parameter exists but has ZERO callers today, so the threading is the
  real work. Collect EVERY `-X`/`--strategy-option` occurrence (the
  argv reader's Find returns only the FIRST — a single-option read
  silently drops the second of `-X ours -X ignore-space-change`) and
  forward each as SEPARATE argv elements, Name then Value — never the
  space-joined `Raw` member (probed: merge-tree rejects `-X ours` as
  one element). Forwarding is probed exact — the previewed tree is
  byte-identical to the real compute's under the same option, both
  spellings. `previewRefusal`'s doc comment ("safegit's merge-tree
  argv is fixed") goes false — rewrite it. Version floor `[user]`:
  merge-tree learned `-X` in git 2.43 (its release
  notes, verbatim: `"git merge-tree" learned to take strategy backend
  specific options via the "-X" option, like "git merge" does.`),
  which exceeds the existing 2.38 merge-tree floor. The 2.43 floor is
  DECLARED IN THE GLOBAL FEATURE SET — one floor authority: the
  preview refusal calls the standard floor-check path, the
  installed-git floor test covers the declaration automatically, the
  floors package's completeness claims stay true with no exception
  clauses, and `doctor`'s WARN-severity git-version check (warn
  findings never affect doctor's exit, by its own stated rule) gains
  one honest warning line for git 2.40-2.42 operators — the
  codebase's own definition of a warning an operator may live with
  (no test pins that message — checked). Consequences owned here
  `[plan]`:
  - THE REFUSAL'S HOME: `previewRefusal` — the round's SECOND declared
    signature change (it takes no context today; the floor check needs
    one). The floor question is asked only when a
    `-X`/`--strategy-option` occurrence is present (the check itself
    is cached and free), CONDITIONAL — an optionless preview never
    refuses on old git — and it fires BEFORE the would-do record like
    every preview refusal (the pinned no-`run: git`-on-refusal
    property holds for it too). A unit pin asserts the conditional
    trigger: the refusal fires only with options present.
  - THE PINS ARE VERSION-DEPENDENT: the forwarding-identity pins
    (single- and two-option) SKIP in the suite's existing
    capability-probe idiom (probe `merge-tree --write-tree -X` once
    in the fixture, skip when this git rejects it — never version
    parsing).
  The remaining consequence: on git 2.38-2.42 the real
  merge accepts the option while the preview refuses with the named
  floor — that version-dependent split is recorded as ONE sentence in
  the rewritten strategy-options catalog entry `[plan]`.
- Merging unrelated histories moves to REFUSED (delete its allowed row
  and the comment arguing for it; the refusal reason is the footgun,
  not a protection hole — and the way out for the legitimate
  once-per-lifetime import is stated: raw git computes with no-commit,
  safegit merge-continue concludes it). ALSO `[user — part of the
  friendly-errors starting set]`: a PRE-FLIGHT refusal when the merge
  has NO MERGE BASE. Predicate: HEAD RESOLVES and `merge-base` reports
  no base (see Phase interactions — a bare merge-base failure must not
  refuse, or Phase 2's unborn merges break). Anchor `[plan]`: TWO
  insertion sites mirroring the FETCH_HEAD-octopus precedent
  (`refuseFetchHeadOctopus` is already called once inside merge's
  dry-run branch — with its comment explaining why — and once inside
  `performMerge`, because the shared path contains NO preview branch:
  merge's dry-run returns before reaching it, and pull's dry-run
  deliberately performs no merge step at all, per its own comment).
  One shared predicate helper, called from both sites — so PULL
  INHERITS the real-mode pre-flight `[user]`, merge's `--dry-run`
  refuses identically (probed: under safegit's own merge-tree argv the
  preview dies on unrelated histories with the same bare fatal —
  merge-tree does have an allow-unrelated flag, but safegit never
  forwards it — so a preview could never succeed anyway), and pull's
  dry run stays silent about the merge step (its existing deliberate
  shape — nothing has been fetched to compute a base against).
  EXIT AND OPLOG, decided by the code's own stated rule `[plan]`: the
  `performMerge` site exits `exitcode.General` and appends a FAILED
  oplog entry — exactly like its `--ff-only` neighbor, whose comment
  draws the line ("those are about what was typed … this one is about
  where the branch stands, and only the second is a fact about the
  repository that an audit trail is for"; no merge base is a fact
  about the branches, not the command line — so NOT `Usage`, which
  would also add pull to that code's producer list); the dry-run site
  records nothing (previews write no oplog).
  Rationale: git's own refusal is the bare
  `fatal: refusing to merge unrelated histories` — it names nothing
  actionable; safegit's pre-flight refuses first and supplies the
  documented import route. Red-first; fixtures per Appendix A.
- The rerere auto-update flag moves to REFUSED on all THREE tables
  `[plan — the ruled rationale is verb-independent; the ruling named
  merge because the question did]`: cache-driven auto-staging is
  remembered resolution by the back door; the refusal reason points at
  the NEW catalog entry below (the EXISTING rerere entry is an
  adjacent subject, not the reference — its edit is per the Catalog
  actions table; merge's rerere row comment — the ONLY commented
  rerere row — is rewritten with the split, and the other two tables'
  rows carry a one-word pointer at it, the asymmetry note written
  once). THE NEGATIVE SPELLING
  `--no-rerere-autoupdate` is
  ALLOWED on all three verbs `[user]` — it DISABLES the objected-to
  mechanism and is the operator's only per-run off-switch against the
  honored config key. Both spellings are allowed TODAY, so the change
  is a ROW SPLIT (positive to refused, negative kept allowed) — and a
  small comment beside the rows states this
  deliberately confusing asymmetry's rationale `[user — the comment is
  part of the ruling]`. The new catalog entry (per the table)
  honestly states the OPEN CONFIG ROUTE
  (`rerere.autoUpdate=true` in git config produces the same
  auto-staging and stays honored this round `[user]`), illustrates the
  config-key class ACCURATELY `[user]`: the autostash config keys
  (`merge.autostash` / `rebase.autoStash`) exist but are INERT through
  safegit — the clean-tree guard runs before git, so there is never
  anything to stash, the same reason the `--autostash` flag is dead on
  merge (and it is ALLOWED on rebase) — a CONTRAST with rerere's
  genuinely open route that is the entry's point; and points at
  `todo/git-config-audit-and-pin-table.md` for the deferred full
  treatment.
- Preserving merge topology through a rebase moves to ALLOWED (move
  the row; the replay runs entirely inside the one git-authored door,
  which is verb-scoped and already admits it — the mandatory positive
  test is Appendix A's rebase sibling, one artifact). The rebase help string in
  `main.go` updates BOTH clauses (it states the allowed set AND
  enumerates the refusals), which regenerates its doc surfaces at
  Phase 5's regen.
- Catalog: per the Catalog actions table. ALL THREE flips land in the
  three per-command refusal paragraphs of the
  commands guide (merge's, cherry-pick's, and revert's — each has its
  OWN paragraph) and their matching allowed paragraphs, plus rebase's
  pair: the selection-vs-options split, the unrelated-histories
  removal from the allowed lists, and the rerere pair's split; the
  subset-overview paragraph illustrates with selection only,
  stays literally true, and needs NO change (stated so nobody edits
  it).
- CHANGELOG, same phase, in-place edits `[plan]`: entry id
  `18cee1b928a1515842fe491d21764db5b03a9fb79a6d2e8b` (the merge entry
  — lists `-X` among the shapes refused by name) and entry id
  `18cee1b958c49502e085a5615a4044ac867bd9d68475d219` (the allowlist
  entry — lists `--rebase-merges` among rebase's refusals) are edited
  to the new truth; the same edits ADD the newly-refused shapes
  (unrelated-histories flag + pre-flight; rerere-autoupdate positive
  spelling) to the entries covering the allowlist's restrictions —
  these amend EXISTING breaking surface (v0.28.0's plain passthroughs
  forwarded every flag; the allowlist entry is where that restriction
  lives), which satisfies the release-relative baseline. The
  batch-exclusion REASON in `.rlsbl/config.json` that copies the merge
  entry's description verbatim (including the `-X` clause) is edited
  in the same pass, and the ALREADY-DRIFTED observed-moves exclusion
  reason (pre-existing: its entry gained text its reason copy did
  not) is updated alongside it (rlsbl commit).
- Sanctioned rewrites: per Appendix A.
**Verify (red first per change):** strategy options pass end-to-end on
all three verbs (a conflicted compute under one parks normally and
concludes); their previews compute the identical tree, INCLUDING a
two-option case (`-X ours -X ignore-space-change` — both forwarded);
unrelated histories refuses pre-compute on merge (real AND dry-run
modes) and on pull's real mode, naming the import route (pull's dry
run: untested here, per the body); rerere auto-update refuses
naming the catalog entry while the negative spelling passes; topology-
preserving rebase runs (git-authored, the door admits the replay);
the floor's conditional trigger pinned (fires only with options
present); `valueFlags["rebase"]` no longer lists `--gpg-sign`;
`rebaseSubset`'s doc enumeration names the topology flag and
`--autostash`;
every catalog row and guide paragraph agrees with the code tables.

### 4.2 The mv payload unifies on moved_records `[user]`
- mv adopts the exact entry shape and member name commit uses,
  emitting from the pipeline's OWN record list — grounded: that list
  is already narrowed to the committed message, so the hand-rolled
  ID-narrowing helper and the mv-local entry struct become DEAD and
  are deleted (the fleet dead-surface rule); the reasoning in the
  deleted helper's comment migrates to the emission site. Strictly
  more correct as FUTURE-PROOFING (no observed record can reach mv's
  payload today — every pair is declared and suppression covers them —
  but one that ever did would now carry its origin instead of being
  dropped). THE SCHEMA `[plan]`:
  `mvPayloadSchema` declares `"moves"` with its entry object in the
  required list — the reshape renames the property to `moved_records`,
  adds `origin`, and updates the required list (or emission-time
  validation fails); the entry-object fragment is FACTORED into ONE
  shared schema fragment consumed by commit's and mv's schemas, never
  duplicated (the single-authority rule). NIL-VS-EMPTY hazard, stated
  `[plan]`: the pipeline's record list is NIL when nothing survives —
  emit through the EXISTING `movedRecordEntries` renderer, which
  already normalizes with a non-nil make (a second renderer would
  reintroduce the hazard); the retargeted hook-strip
  test pins that path.
- Probed end-to-end in a scratch copy: the reshape builds and the full
  suite is green; the preview reports the declared origin.
- Sanctioned rewrites: per Appendix A.
- Changelog: mv has never shipped, so nothing breaks; the existing
  unreleased entry about reword/mv narrowing (id
  `18cee9d6c8450656ca5c24b003044851990157328086f4d1`) stays true (the
  pipeline still narrows — the deletion is of a reimplementation, not
  the behavior). COVERAGE `[plan]`: this subphase's commits are
  APPENDED to the mv feature entry's commit list (entry id
  `18ce4135d301c7c192c89bb3ae214a84a44125df51ef9dcc`) via the
  SANCTIONED HAND EDIT (see the discipline block); the exclusion
  procedure for a grown list is Phase 6's — the commits must not be
  left for Phase 6 to discover uncovered.
**Verify (red first on the retargeted tests):** real and preview mv
payloads carry the unified member with origin; the hook-strip case
reports only what the committed message carries; a `git grep` sweep
for the old member name and the deleted helpers returns zero hits; the
entry-object fragment is ONE named symbol referenced from both schemas
(grep).

---

## Phase 5 — Documentation closure and regeneration

After Phases 1-4 (it closes over their doc debts). One implementor.

- THE ROSTER AND THE FLIP BATCH: execute the Phase-5 rows of the
  Catalog actions table. The autostash entry's text edit and flip is
  its own commit FIRST; then the flip-only rows are ONE batch edit,
  per the count-assert procedure stated in the table's preamble (the
  count taken AFTER the autostash commit). The preamble-and-duplicate
  row is executed exactly as the table states.
- The debt set, ENUMERATED so the Verify is checkable: the NEW
  conventions bullet in the CLAUDE template recording the adopted
  friendly-errors direction `[user]` (safegit MAY add probe-backed
  pre-flight refusals where a known corner produces a misleading or
  hostile raw git failure, each individually cataloged; wholesale
  wrapping of git stderr is permanently ruled out); plus verifying the
  named regeneration outputs of the earlier phases (Phase 3:
  cli-commit and the schema; 4.1: README.md, the generated CLAUDE.md,
  docs/cli-rebase.md, docs/cli-index.md; the help strings 1.2 and 2
  edit — in `main.go` AND the command files — regenerate
  docs/cli-cherry-pick.md, docs/cli-revert.md, docs/cli-merge.md and
  docs/cli-bisect.md the same way; and `.strictcli/schema.json`,
  which embeds the edited help strings and 4.2's payload members)
  carry the new texts after the
  regen below; plus `scripts/test-baseline`'s two stale comments (its
  "the one test keyed on `testing.Short()`" claim — there are two —
  and its "suite deliberately red" line, stale since the suite went
  green); plus CONTRIBUTING.md's Test section (pre-existing
  staleness: its `-short` line — NOT redundant: `-short` skips the
  hooks timeout test and shortens the reconcile property test, so the
  section should state what a contributor actually runs and what
  `-short` skips — and a stress line lacking
  `--stress` at a 15-minute timeout against the 40-minute budget).
  Dispositions on the `-short` siblings, stated so no sweep "fixes"
  them: `scripts/test-baseline`'s `-short` is DELIBERATE (artifact
  determinism — the committed baseline's header records that argv;
  touching it invalidates Phase 6's reconciliation anchor) and STAYS;
  the release hook's `-short` STAYS this round `[user]` — it rides the
  Phase 8 ledger item.
- Then the ONE schema regeneration — `go run . --dump-schema` per the
  discipline block; its own commit via rlsbl commit — then
  bare `selfdoc gen`, then `selfdoc check` must be exit 0.
**Verify:** `git grep` — scoped `-- docs/divergences.md`, since
repo-wide the phrases also live in THIS plan file — finds zero
occurrences of the table preamble's
EXACT flip spelling (the vocabulary definition's own "provisional —
awaiting review" wording legitimately survives, and is line-wrapped
in the file — match partial phrases) and zero "listed for the
review" / "open at the review" phrases and no surviving
every-subset-boundary-entry blanket clause; the roster names no
entry; the enumerated debt set closed;
regeneration clean; selfdoc check green; the generated root files
carry the new texts.

---

## Phase 6 — Changelog and full verification

After Phase 5.

- Changelog work, by disposition. THE TYPE BASELINE IS
  RELEASE-RELATIVE `[user]`: an entry is breaking only if it refuses
  or changes input that worked in the LAST RELEASED version (v0.28.0);
  restrictions on surfaces introduced this cycle ride inside the
  entries that cover those surfaces, named in the entry text.
  - Breaking: NO new breaking entries — the symlink widening's
    breaking coverage IS Phase 3's in-place edit of its existing
    breaking entry; the compute-guard/rebase coverage IS 1.2's
    in-place edit of its existing breaking entry; the two allowlist
    refusals ride 4.1's in-place edits of the existing
    allowlist-restriction entries (all entry ids in those phases).
    IMPLEMENTATION COMMITS of 1.2, 3 and 4.1 are appended to those
    same entries' commit lists via the sanctioned hand edit; the
    exclusion analysis covers ALL the edited entries (only the merge
    entry has an exclusion today — the others gain one if their grown
    lists need it).
  - Feature, APPENDED: unborn-branch support ONLY (its entry names the
    friendly unborn refusals). The strategy-option and
    topology-preserving re-allowances are NET-ZERO relative to v0.28.0
    (the plain passthroughs forwarded those flags; the mid-cycle
    refusals never shipped) — per the baseline rule they ride 4.1's
    in-place edits of the entries that refused them, with the
    preview-forwarding detail riding the entry that ships previews.
    One sentence is ADDED to the combined cherry-pick/revert entry
    (typed breaking, id
    `18cee1b93d8e8aebcb97dcdc2e944b9fa24710899c3a1531`) via
    `changelog edit` — a description edit, which the tool supports —
    for the empty-park auto-clean, covering BOTH the empty pick and
    the empty revert (the entry is the combined one), the one
    never-shipped fix whose
    final behavior an upgrading user will meet `[user — the hybrid
    ruling]`. The mv feature entry (id
    `18ce4135d301c7c192c89bb3ae214a84a44125df51ef9dcc`) carries 4.2's
    appended commits — if the grown list exceeds the per-entry commit
    limit, its exclusion is created here (`rlsbl check --tag
    changelog` is the arbiter of that limit; the plan declares no
    number).
  - Fix: NONE for this round's behavior items — the empty-park
    auto-clean and the out-of-band leftover naming fix symptoms that
    never shipped (their commit coverage rides no-user-facing
    clusters).
  - No-user-facing: the plan file and its ruling commits
    (`--allow-batch` — the negatable `--allow-batch`/`--no-allow-batch`
    pair, taking no reason; its auto-created exclusion
    carries a placeholder reason, which is then REPLACED by hand in
    `.rlsbl/config.json` with a purpose-written one, inside the
    sanctioned-hand-edit scope), the pins, the predicate hardening,
    the roster/doc work,
    the 1.1/1.3 behavior commits, and EVERY pre-round housekeeping
    commit sitting uncovered from the planning session (the registry
    policy doc, the coincidence pin and catalog sentence, the two
    promoted tools, the CONTRIBUTING correction, the closing baseline,
    and the git-config-audit todo commit).
  Run `scripts/verify-coverage-partition <clusters-file>` (its input:
  one cluster per line, a name followed by its commit SHAs) on the
  proposed clusters BEFORE writing any entry.
- The census regenerates (`scripts/exit-inventory` into the testdata
  file, committed with its established message — `testdata: regenerate
  the exit-site census`) as the LAST content commit — the release
  hook (`.rlsbl/hooks/pre-checks.sh`) blocks on its freshness.
- The full battery on the quiescent tree: build, vet, gofmt; full
  race suite; the stress run at its 40-minute budget; the
  released-dependency run (`GOWORK=off go test ./... -race`);
  `selfdoc check --no-auto-commit` (verified: the flag exists; bare
  `check` WRITES its hashes file and auto-commits, which would break
  the quiescent-tree and census-last properties); the changelog checks
  (`rlsbl check --tag changelog`).
- Reconciliation against `testdata/closing-baseline.txt`
  (`scripts/test-baseline <path>` writes the fresh snapshot to its
  positional path argument — write it to a `testdata/*.local-only`
  path per the existing gitignored convention, NEVER over the
  committed anchor before the diff; the script's `--check` is a
  strict diff and cannot serve — the classification is the work):
  classify every differing line — a red-first heal (cite the
  subphase), a sanctioned rewrite (cite Appendix A), or a new test —
  zero unexplained.
- AFTER the reconciliation `[user]`: regenerate
  `testdata/closing-baseline.txt` in place and commit it — the tree
  carries the post-round record; git history keeps the pre-round
  anchor.
**Verify:** all checks green; coverage total; the classification has
zero unexplained lines; the refreshed baseline committed.

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
  1, historical); the baselines in testdata; the divergences catalog
  (its own inclusion rule and direction table); the allowlist code
  file as the single authority the catalog and guide mirror; the
  census and its generator; `docs/req.md`'s historical-document marker
  (never score against it); the experiments-directory convention; the
  build/test commands and the live-tree hazard. The brief ALSO
  carries the verification territory map `[plan]`: which claim
  classes were verified pre-execution and where the thin ground is
  (execution-produced code; the execution-verified-only items — the
  case-insensitive filesystem variant that first runs on the release's
  macOS lane, the two-option preview forwarding pin, full-suite
  greenness, each bound to its named Verify step), so audit budget
  goes where verification is thinnest. And the CONSEQUENCE-TRACING
  lens `[plan]`: for every behavior change shipped, trace its
  downstream effects — exit codes and the registry, doctor's checks
  and output, generated surfaces (schema and docs), payload schemas,
  the oplog, version-dependent paths, and the catalog — the one
  defect class only tracing finds.
- Findings remediated red-first; the battery re-run after. A finding
  the auditor grades as needing a user ruling does NOT stop execution
  `[user — zero touchpoints]`: the orchestrator resolves it with the
  most conservative available choice (prefer refusing/documenting over
  new behavior) and marks it in the round's record for post-release
  review.
**Verify:** the audit report addresses every phase of both campaigns
and this round; zero unremediated findings.

---

## Phase 8 — Release

- Todo triage: `todo/campaign2-plan.md`,
  `todo/redesign-campaign-plan.md` (campaign 1's plan, still sitting
  in todo — its move was ruled at that campaign's close),
  `todo/move-records-for-undeclared-moves.md` (consumed), and THIS
  plan move to `todo/.done/`. THE DEFERRED-WORK LEDGER `[user]`: the
  triage ALSO writes one new todo,
  `todo/next-cycle-candidates.md`, each item SELF-CONTAINED with its
  context copied from its named source BEFORE any source file moves
  `[plan — a pointer into a `.done` file is not context]`. The items
  and their context sources:
  - envelope-always adoption when the framework's error-payload
    channel ships — context in `todo/campaign2-plan.md`;
  - the doctorFix uninitialized-repo notice — context in
    `todo/campaign2-plan.md`;
  - the unminted conclusion execute-path worktree writes — context in
    `todo/campaign2-plan.md`;
  - the doctor-fix output's connective wording — context RECONSTRUCTED
    `[user]`: doctor `--action fix`'s output reads as disconnected
    finding/fix lines and the connective wording between them is
    deferred polish; the original reasoning was lost with a session
    and this sentence is its reconstruction, marked as such in the
    ledger;
  - the reset-over-parked-state and empty-compute-oplog observations —
    context in THIS plan's Recorded observations section;
  - the friendly-errors probe-backed INVENTORY `[user]` — context in
    THIS plan's Phase 2 and 4.1 friendly-refusal items: force each
    failure corner of the supported surface in scratch repos, capture
    git's actual message, grade it; everything graded misleading or
    hostile is a candidate pre-flight refusal, the evidence source for
    future additions to the adopted additive direction;
  - the stale doc claim inside the dry-run-network await todo — the
    ledger is where the triage NOTES it (the todo itself is
    immutable);
  - the `residue` schema-fragment duplication `[plan]`: an identical
    entry-object fragment exists in the payload schemas of seven
    commands (two of them files 4.2 edits) — a deliberately deferred
    N-to-1 reduction, same shape as the moved-records fragment 4.2
    does unify;
  - the docs-architecture observation `[plan]`: hand-typed structural
    counts in shipped prose ("guarded twice", command enumerations,
    and the commands guide's "two coordination layers" section title —
    imprecise since before this round and deliberately left unrenamed)
    are a stale-risk class — a future cycle may want them
    enumeration-derived or generated (context: THIS plan's
    falsified-surfaces item in 1.2);
  - the release-hook `-short` asymmetry `[user — parked this round]`:
    the release's own pre-release test command runs `-short` while CI
    runs the full suite on the same candidate commit (nothing ships
    unexercised; the asymmetry is the observation).
  Staying active, each its own file: the await-class files
  (await-strictcli: conditional-consequential, dry-run-network,
  exit-codes-registry; plus
  `todo/effects-handle-closed-method-set.md`, awaiting the same
  framework despite its name), the contingent files
  (reader-writer-operation-lock, scrub-strict-mode-selector,
  push-streaming-restoration),
  `todo/reversibility-gaps-missing-inverse-commands.md`,
  `todo/git-config-audit-and-pin-table.md` (the deferred git-config
  audit; 4.1's rerere catalog entry points at it), and
  `todo/pipeline-authored-rebase.md` (post-campaign work; cited by
  path from two shipped docs AND from production source —
  `internal/gitexec/authoring.go` — so it must not move). The deferred
  and obsolete subdirectories untouched.
- Post-audit coverage `[plan]`: changelog entries for Phase 7's
  remediation commits and THIS phase's own triage commits (the todo
  moves, the ledger) are added HERE, before the release file is
  committed; the census re-runs only if remediation added or moved
  lines in any file holding an exit site — the hook's own invalidation
  condition (its freshness check is the backstop, not the plan).
- `rlsbl release init`; the release file: bump MINOR (0.28.0 to
  0.29.0), the two-campaign-plus-closing-round description and
  context DRAFTED BY THE ORCHESTRATOR and committed — no approval stop
  `[user — zero touchpoints]`.
- `actionlint` over the workflows (installed; cheap insurance before
  their first-ever run on this history).
- `rlsbl release run --no-allow-dirty --watch
  --approve-consequential` — run directly; the round's single starting
  go covers the release `[user — zero touchpoints]`. The candidate
  push is the first push of
  the entire two-campaign history; a red CI verdict is fixed forward
  at the same version with `rlsbl release resume --watch` (the
  watch choice is a required flag on resume too) — never a new
  version, never a manual push.
- Post-release (recorded, outside the release): the fleet sweep for
  legacy placeholder pre-pre-push hooks is SCRIPTED and run in
  ONE pass right after the new version installs `[user]` (the script
  finds every repo's legacy pre-pre-push file, `hook migrate`s or
  deletes the no-op placeholder per repo, and verifies a push probe
  per repo — the script discovers the set; no hand count) — no session
  ever hits a surprise hooks-not-migrated refusal; the
  dependent projects' parked todos unblock on their own triage;
  `selfdoc.json`'s version is release-pipeline-owned and
  `.rlsbl/config.json`'s pre-release hook is user-owned config the
  pipeline only reads — neither needs hand edits this round.

---

## Dependency spine

| Phase | Depends on | Notes |
|---|---|---|
| 0 | — | one item (the predicate); the rest was executed pre-round |
| 1 | — (the baseline is already committed, pre-round) | ONE implementor; internal order 1.1 then 1.2 then 1.3 is mandatory (fixtures) |
| 2 | 1 (catalog + guide; sequencer adjacency) | |
| 3 | 1-2 (catalog + guide files only) | parallelizable with 2 in code — sequence the shared docs |
| 4 | 1-3 (catalog + guide serialization) | 4.1 and 4.2 share no files |
| 5 | 1-4 | executes the Catalog actions table's Phase-5 rows; the ONE schema regen |
| 6 | 5 | census last; battery on the quiescent tree; baseline refresh after reconciliation |
| 7 | 6 | the Fable-orchestrated audit |
| 8 | 7 (the round's single starting go covers it — zero touchpoints) | |

Sequential execution is the default; Phases 1.2, 2, 3, and 4 collide
on the catalog file AND the commands guide (1.2's guard-count edits
and table row, Phase 2's unborn section,
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

## Appendix A — sanctioned rewrites and renames

A baseline break not listed here is a plan defect. Each list below is
THE single statement of its set — phase text points here and never
restates. New tests are not rewrites and are not listed (Phase 6's
reconciliation classifies them separately).

- 0.1: the pipeline-honors-declared-context test gains the
  shared-index base in its request literal — which also switches the
  commit's temp-index seed from the parent tree to a shared-index
  copy; the test stays green (it asserts parents and branch only) and
  the edit is mandatory under the AND (the fixture's unmerged,
  never-added index would otherwise refuse). The edit also disables
  observed-move inference for that request (inference keys on the
  parent-tree base) — harmless for this test, stated so nobody hunts
  the difference.
- 1.1: BOTH fixture builders in the in-flight refusal test file
  re-fixture on raw git (probed working: a raw `git cherry-pick` of an
  already-applied commit parks CHERRY_PICK_HEAD on a clean tree and
  exits 1; a raw `git revert --no-commit` of an already-reverted
  commit parks REVERT_HEAD and exits 0 — the current builders assert
  nonzero on BOTH arms, so the revert arm's exit assertion adjusts;
  neither park creates a `.git/sequencer` directory; and a clean raw
  `git cherry-pick` leaves `AUTO_MERGE` behind at exit 0 — residue
  assertions over raw-git fixtures must expect that file); the two
  state-producer pins INVERT AND RENAME
  (`TestANoChangeSecondPickParksItsState` /
  `TestANoChangeSecondRevertParksItsState` — their names claim
  parking; both currently ALSO assert the abort advice, which the
  success path removes; the renamed tests assert the state is GONE
  and pin a
  distinctive phrase of the new cleaned-up sentence); the consumer
  test functions re-fixture VIA THE SHARED BUILDERS (only the
  pull-over-parked test's fixture is inline and needs its own edit) —
  the two
  pick/revert-refuse-over-parked tests, the pick preview variant, the
  two merge-over-parked tests, the pull-over-parked test, and the
  way-out test (whose subtests are why an item count
  would mislead).
- 1.2: none beyond the new tests (the two no-commit-stays-passthrough
  pins run with nothing in flight and stay green — verified).
- 1.3: none (verified — no test asserts the current one-line message).
- 2: none (no test pins the unborn refusal — verified; the friendly
  refusals replace raw git fatals no test asserts).
- 3: the absolute-inside-committed pin
  (`TestCommitAbsoluteSymlinkTargetInsideTheRepositoryIsCommitted`)
  inverts into the refusal test AND RENAMES — its current name claims
  the overturned behavior (its own comment says it is the one to
  rewrite); the wording
  assertions across the suite follow the new messages; the
  inside-target and traversing pins RETARGET to a distinctive phrase
  of the new notice (they assert the ABSENCE of "outside the
  repository" and would go silently vacuous under the reword — the
  same trap as the mv hook-strip test); the mv carve-out pin updates
  for the flag rename only, and ITS COMMENT is corrected (it claims
  "mv simply never calls the intake that judges links" — false: mv
  reaches `resolveFiles` with no file specs, so the offender set is
  empty by construction); the test functions renamed to the
  non-portable vocabulary, enumerated from the tree:
  `TestCommitSymlinkEscapingTargetIsCommittedWhenElected`,
  `TestCommitEscapingSymlinkRefusalNamesTheLiteralTarget`,
  `TestCommitEscapingSymlinkRefusalNamesEveryOffender`,
  `TestAmendEscapingSymlinkIsRefusedAndElects`,
  `TestCommitEscapingSymlinkFoundByDirectoryExpansionIsRefused` (all
  `commit_symlink_test.go`), `TestMvOfATrackedEscapingSymlinkProceeds`
  (`mv_test.go`), and `TestWave2CommitEscapingSymlinkIsRefused` +
  `TestWave2CommitNonEscapingSymlinkStillCommits` in
  `wave2_symlink_escape_refusal_test.go`, which itself renames; the
  remaining old-spelling sites (flag and constant spellings inside
  test bodies, and the wording assertions that quote the old
  messages) update as a counted mechanical pass (the batch-edit
  discipline), not an open sweep.
- 4.1: the strategy-option refusal rows leave their tests (the merge
  raw-shapes row, the merge preview row, the pick/revert selection
  test's option member and its doc comment); the pick/revert
  preview-refusal test takes `--strategy resolve` as its substitute
  unsupported option (NOT `-s resolve` — on pick/revert `-s` is the
  allowed signoff spelling and `resolve` would parse as a second
  revision; `-s resolve` remains valid only in merge's tests) and its
  own doc comment — which quotes the preview refusal this subphase
  deletes — rewrites with it; the
  merge preview test's doc comment ("These three cases used to be
  preview-specific refusals") is rewritten — one of its cases leaves;
  the rebase refusal table drops its topology row and gains a positive
  sibling beside the allowed-form control; unrelated-histories
  fixtures: NONE (verified — zero uses of the flag and no two-root
  fixture in the suite; the currently-allowed row has no pin, so that
  flip itself produces no baseline diff).
- 4.2: the two mv payload tests retarget to the unified member — the
  hook-strip one DELIBERATELY (it decodes the payload and asserts an
  EMPTY length on the OLD member; a renamed member decodes to nil,
  length zero, so a bare rename leaves it TRIVIALLY green while
  testing a member that no longer exists); TWO false doc comments in
  that test file rewrite with it: the hook-strip comment's claim that
  the human line "is pinned here too" (the test runs `--json` and
  asserts nothing about prose) and the payload-member comment above it
  claiming the member "is built from the PAIR LIST it was given rather
  than from what the commit ended up with" (already false today — the
  emission narrows via the committed-records helper before it); the
  preview one gains the origin assertion.
- 5: the flip-only batch and the autostash entry's edit, per the
  Catalog actions table and its preamble's procedure.
