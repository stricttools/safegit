# The closing round: implementation plan (revision 4)

Self-contained in DECISIONS: every decision this round executes is
stated in full in THIS file, and a session with zero conversation
context can implement any subphase from this file plus the cited code.
Ambient discipline is NOT restated here `[user — re-scoped at the
second critique round]`: the auto-loaded rules files are the living
authority for standing discipline (safe deletion, no raw git, scratch
placement, red-first, permanent tests, batch-edit discipline, the
live-tree/quiescent-tree rule), and implementor briefs cite them
rather than this file. The plan executes the rulings from the
pre-release design review that followed campaign 2 (whose plan,
`todo/campaign2-plan.md`, is a completed historical record — where the
two disagree about what to do NEXT, this file wins). Anchors are claim
text (file + function + a distinctive phrase), grounded at HEAD
`ef4b07e` and re-verified by three adversarial critiques (at
`9855c75`, `3d61659f` and `22f70d88`); the Catalog actions table and
the enumerations below
were re-extracted from the tree at revision time. Expect line drift,
never claim drift. Revision protocol: every NEW factual claim entering
a revision is verified against the tree at write time or explicitly
marked not-re-verified.

**EXECUTION starts on the user's explicit go, IN A LATER SESSION** —
the user ruled that the session which wrote this plan does not execute
it. This file existing is not that go. Also ruled at the same review:
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
- Verify style rules `[plan]`: a pin asserts the PRESENCE of a
  distinctive phrase of the current text, never the absence of old
  text; every grep-based Verify step uses `git grep` (tracked files
  only — the on-disk build artifacts under `docs/_build/` would
  otherwise produce false hits).
- Falsified-texts sweep `[plan]`: before committing any behavior
  change, `git grep` the changed flag/behavior names across ALL
  TRACKED TEXT — code, docs prose, templates, the changelog JSONL,
  `.rlsbl/config.json`, help strings, doc comments — with generated
  files that a later regeneration rebuilds as the ONLY carve-out
  (deny-by-default scope; an enumerated store list is how the last
  miss happened). Enumerate the hits and give each a disposition
  (edited here / owned by a named later phase / stays true). The
  per-phase lists in this file are the revision-time enumerations; the
  sweep re-checks them at execution time.
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
  (`scripts/counted-edit` — dry-run first, occurrence count asserted,
  full diff reviewed), one commit per edit, and
  `rlsbl check --tag changelog` must pass afterwards. The ban stands
  everywhere outside the named set.
- `.strictcli/schema.json` is regenerated ONCE, on the quiescent tree
  at the end of Phase 5, never per-phase. `--dump-schema` WRITES the
  file (nothing goes to stdout). Its embedded version member is
  safegit's OWN pseudo-version, which churns on every commit — hence
  once, at the end. The schema is independent of the `go.work` overlay
  (probed byte-identical with and without), so no GOWORK switch
  applies to the dump; `GOWORK=off` belongs only to Phase 6's
  released-dependency test run.

**Decision-origin marks.** `[user]` = ruled by the user in the review
or at a critique round (recommended-option picks weakly held per the
standing convention). `[plan]` = orchestrator resolution of a
grounding- or critique-discovered gap, reversible on request.

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
| The symlink-target entry (its heading names "leaves the repository") | deliberate | REWRITTEN including its heading to the portability class; its "listed for the review" sentence REMOVED; marker stays deliberate | 3 |
| Merge strategies and strategy options are refused | provisional | REWRITTEN including its heading (selection stays refused; options now allowed) + flip | 4.1 |
| `rebase` is one upstream and a replay, and nothing else | provisional | text edit (refusal bullet and framing; the topology row moves to allowed) + flip | 4.1 |
| NEW: merging unrelated histories is refused (flag + pre-flight) | — | new entry, born DELIBERATE | 4.1 |
| NEW: the rerere auto-update refusal and its open config route | — | new entry, born DELIBERATE | 4.1 |
| The "What each guarded command allows" table | (no ruling line) | row edits for merge, cherry-pick, revert AND rebase | 4.1 |
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
| The catalog preamble AND the section-body duplicate | — | the roster blockquote ("**The provisional entries**, listed rather than counted, are these:") AND its companion clause ("every one of them is open at the review") rewritten to the end state; the SECOND instance of the same claim in the subset-boundary section BODY ("They are all open at the review, and overturning one is cheap…") rewritten too — no table row's entry edit covers it and Phase 5's greps do not match it; the blanket clause declaring every subset-boundary entry provisional corrected (the deliberate entries there, and the allowed-sets table which carries no ruling line, are the exceptions — enumerate at edit time); the sentence "A behavior newly cataloged here is provisional until it has been reviewed as an entry, whatever its direction says" rewritten to permit entries born deliberate when they record a review-time ruling — this table's three born-deliberate entries are exactly that | 5 |

## Phase interactions (the seams where phases meet — checked pairwise)

- **1.1 and 2**: 1.1's empty-park auto-clean mirrors the unpark
  precedent, whose index/worktree sync resolves `"HEAD"`; Phase 2
  makes that resolution unborn-safe INSIDE the shared helper, so the
  auto-clean inherits it with no edit in 1.1. Before Phase 2, no
  unborn path can reach the auto-clean; after it, the unborn revert's
  auto-clean works — Phase 2's revert Verify depends on this.
- **2 and 4.1**: the no-merge-base pre-flight's predicate REQUIRES a
  resolvable HEAD ("HEAD resolves AND merge-base reports no base") — a
  bare merge-base failure must not refuse, or every unborn merge Phase
  2 ships would refuse (probed: `merge-base` fails on an unborn HEAD
  while the merge itself legitimately fast-forwards).
- **2, 3, 4 and 5**: the divergences catalog and the commands guide
  are shared by Phases 2, 3 and 4 (see the spine note); the
  Catalog actions table owns every marker action; Phase 5 owns the
  roster and the flip batch.
- **1.2, 3, 4 and 6**: the in-place changelog work ordered in Phases
  1.2, 3, 4.1 and 4.2 is part of Phase 6's coverage accounting (listed
  there by entry id).

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
already set both, so no behavior reachable through the CLI changes —
the amend/reword call sites flip from exempt-whenever-context-declared
to never-exempt, which IS observable through the package API (a
Sequencer with the parent-tree base over an unmerged index), though no
production caller constructs that shape today (grep-verified). Order
the package-level red-first unit pin in `internal/commit` for exactly
that shape (red under the OR, green under the AND) `[plan — the
red-first rule applied honestly; the pin is what catches a future
half-configured caller]`. The doc comment currently ARGUES the
OR and must be rewritten, not trimmed. One sanctioned test edit:
`TestPipelineHonorsADeclaredSequencerContext`'s request literal gains
the shared-index base. Consequence stated in code `[plan]`: the
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
  precise scope boundary; `gitDir` is in scope). THE ONE deliberate
  signature change `[plan — forced by the per-branch message]`: the
  `onEmpty` callbacks gain the cleanup outcome as a parameter (three
  callers), because the abort advice is hard-coded INSIDE the
  callbacks and must branch on an outcome their caller computes — a
  zero-argument callback cannot. Mirror the unpark precedent
  (`refuseParkedRawGitShape`): `sequencer.Cleanup` for the operation's
  kind plus the index/worktree sync, both halves attempted, a cleanup
  failure never swallowed — the residue is named `[plan]`.
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
  exit code), and the code comment says so. The comment also states
  that the mirrored sync is `read-tree --reset -u` — a destructive
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
- The refusal messages are rewritten; keep the phrase "no change" (an
  existing restructure test asserts it). The `-continue` door's OWN
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
The merge `--no-commit` park preserved. The pre-existing
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
  rebase's handler before git runs. THE PREDICATE, stated outright
  `[plan — the earlier wording was ambiguous, and one reading breaks
  every ordinary rebase]`: refuse when `sequencer.Read` reports an
  in-flight state whose kind is NOT the rebase kind. Never implement
  this by declaring a rebase context through `coord.GuardInFlight` —
  that path hard-errors when a context is declared and nothing is in
  flight, so it would refuse every clean-repo rebase. The
  state-control exemption (`rebase --continue`/`--abort`/`--skip`) is
  a CONSEQUENCE of the predicate — mid-rebase state reports the rebase
  kind, so those forms pass by construction; no second argv-based
  exemption list `[plan]`. Red-first: rebase over a parked revert
  refuses naming the revert and the way out; an ordinary rebase still
  runs; a conflicted rebase's own --continue still reaches git.
- Catalog: per the Catalog actions table.
- Changelog, same phase, in-place edit `[plan]`: entry id
  `18cee9d5500c2822ba19eac36bdb4ba99c45ea0f13bb5f76` ("merge,
  cherry-pick, revert and pull refuse to compute over an operation git
  already has in flight") is edited to name rebase and the
  `--no-commit` forms — the same edit makes its existing claim true.
**Verify (red first, on the re-fixtured raw-git parked states):**
`cherry-pick --no-commit` over a parked revert and `revert --no-commit`
over a parked pick each exit with the coordination-busy code, name the
in-flight operation and the safegit conclusion command, move no HEAD,
stage nothing, write no state file of their own, and leave the parked
state intact; a `--dry-run` variant refuses identically; with nothing
in flight both forms still work.

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
the file still holds the picked commit.
**Verify (red first):** the reproduction; the message names
CHERRY_PICK_HEAD and the staged content; nothing rolled back.

---

## Phase 2 — Unborn-branch support `[user — support now]`

After Phase 1 (catalog + commands guide; sequencer adjacency). One
implementor.

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
  every seam imports. The invocation form is right on this cold path —
  hardcoding the two known empty-tree names per algorithm would only
  avoid one subprocess, and the invocation self-adapts to any future
  hash algorithm; the helper's comment states this `[plan]`.
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
  `refuseParkedRawGitShape` and 1.1's
  empty-park auto-clean (see Phase interactions; without it, the
  auto-clean's cleanup half fails on unborn and this phase's own
  revert Verify cannot pass). The MERGE preview is the new-arm seam:
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
  spelling on the root path for every OTHER consumer (`git.DiffTree`,
  `refOrEmptyTree`, and the inference path key off it — only the
  tree-unchanged comparison changes); and the arm's "Root commits are
  never empty" comment states the premise being overturned and is
  rewritten, not trimmed.
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
- Grounded residue: a successful `switch -c` on a still-unborn repo
  records an ok navigation entry with an empty observed tip, and a
  `switch <branch>` FROM an unborn HEAD records an empty observed
  parent — both harmless (the observed spelling is deliberately
  unconsumed); the entry-writing comment in `coord_cmd.go` gains a
  note covering BOTH spellings.
- The FINAL PARAGRAPH of the "unreachable TODAY" comment block on
  merge's unborn arm is deleted with the fix (the block's earlier
  paragraphs carry live rationale for the ZeroSHA pin and stay). The
  guide GAINS an unborn-branch section (none exists today — created,
  not edited) covering what works, the friendly refusals, and the
  dirty-unborn refusal.
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
friendly refusal fires with its message and runs no git compute;
previews work. Unit tests in `internal/coord` for the unborn
clean/dirty arms.

---

## Phase 3 — Symlink portability refusal `[user]`

Independent of Phases 1-2 in code; after them for the shared catalog
and commands-guide files. One implementor.

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
  not trimmed (the 0.1 precedent) — enumerated `[re-extracted at
  revision time]`: in `internal/commit/intake.go`, the
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
  file's header comment naming `noticeEscapingLinks`.
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
  bullet update; cli-commit and the schema regenerate later (Phase 5).
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
files). After Phase 1 for the catalog and guide files only.

### 4.1 The allowlist changes `[user]`
All rows live in `subset_allowlist.go`; the argv machinery already
handles the spellings involved (attached and detached strategy-option
values normalize to one name; the rebase topology flag takes its value
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
  forward each as SEPARATE argv elements, Name then Value — never the
  space-joined `Raw` member (probed: merge-tree rejects `-X ours` as
  one element). Forwarding is probed exact — the previewed tree is
  byte-identical to the real compute's under the same option, both
  spellings. `previewRefusal`'s doc comment ("safegit's merge-tree
  argv is fixed") goes false — rewrite it. Version floor `[plan —
  resolved at revision time]`: merge-tree learned `-X` in git 2.43
  (its release notes: "git merge-tree learned to take strategy backend
  specific options via the -X option"), which exceeds the existing
  2.38 merge-tree floor — a floor row at 2.43 for the
  strategy-option-forwarding preview is REQUIRED and this subphase
  adds it.
- Merging unrelated histories flips to REFUSED (delete its allowed row
  and the comment arguing for it; the refusal reason is the footgun,
  not a protection hole — and the way out for the legitimate
  once-per-lifetime import is stated: raw git computes with no-commit,
  safegit merge-continue concludes it). ALSO `[user — part of the
  friendly-errors starting set]`: a PRE-FLIGHT refusal when the merge
  has NO MERGE BASE. Predicate: HEAD RESOLVES and `merge-base` reports
  no base (see Phase interactions — a bare merge-base failure must not
  refuse, or Phase 2's unborn merges break). Anchor: the shared merge
  path both merge and pull call, after the sides resolve and before
  the compute/preview branch — so PULL INHERITS the pre-flight
  `[user]`, and the `--dry-run` path refuses identically (probed:
  under safegit's own merge-tree argv the preview dies on unrelated
  histories with the same bare fatal — merge-tree does have an
  allow-unrelated flag, but safegit never forwards it — so a preview
  could never succeed anyway). Rationale, stated correctly
  `[corrected at the second critique]`: git's own refusal is the bare
  `fatal: refusing to merge unrelated histories` — it names nothing
  actionable; safegit's pre-flight refuses first and supplies the
  documented import route. Red-first (Appendix A records the verified
  fact that NO existing fixture merges unrelated histories).
- The rerere auto-update flag flips to REFUSED on all THREE tables
  `[plan — the ruled rationale is verb-independent; the ruling named
  merge because the question did]`: cache-driven auto-staging is
  remembered resolution by the back door; the refusal reason points at
  the NEW catalog entry below (the EXISTING rerere entry — about
  conclusions not teaching rerere — is an adjacent subject, not the
  reference). THE NEGATIVE SPELLING `--no-rerere-autoupdate` is
  ALLOWED on all three verbs `[user]` — it DISABLES the objected-to
  mechanism and is the operator's only per-run off-switch against the
  honored config key — and a small comment beside the rows states this
  deliberately confusing asymmetry's rationale `[user — the comment is
  part of the ruling]`. The new catalog entry (per the table, born
  deliberate) honestly states the OPEN CONFIG ROUTE
  (`rerere.autoUpdate=true` in git config produces the same
  auto-staging and stays honored this round `[user]`), names the
  config-twin class (`merge.autostash` / `rebase.autoStash` twin the
  refused `--autostash` the same way), and points at
  `todo/git-config-audit-and-pin-table.md` for the deferred full
  treatment.
- Preserving merge topology through a rebase flips to ALLOWED (move
  the row; the replay runs entirely inside the one git-authored door,
  which is verb-scoped and already admits it — an optional row in the
  door-admission test is consistent). The rebase help string in
  `main.go` updates BOTH clauses (it states the allowed set AND
  enumerates the refusals), which regenerates its doc surfaces
  (README.md, the generated CLAUDE.md, docs/cli-rebase.md,
  docs/cli-index.md) at Phase 5's regen.
- Catalog: per the Catalog actions table. The selection-vs-options
  split lands in the three per-command refusal paragraphs of the
  commands guide (merge's, cherry-pick's, and revert's — each has its
  OWN paragraph) and their matching allowed paragraphs, plus rebase's
  pair; the subset-overview paragraph illustrates with selection only,
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
  reason (its entry gained text its reason copy did not — pre-existing
  at revision time) is updated alongside it (rlsbl commit).
**Verify (red first per flip):** strategy options pass end-to-end on
all three verbs (a conflicted compute under one parks normally and
concludes); their previews compute the identical tree, INCLUDING a
two-option case (`-X ours -X ignore-space-change` — both forwarded);
unrelated histories refuses pre-compute on merge AND pull, in real and
dry-run modes, naming the import route; rerere auto-update refuses
naming the catalog entry while the negative spelling passes; topology-
preserving rebase runs (git-authored, the door admits the replay);
every catalog row and guide paragraph agrees with the code tables.

### 4.2 The mv payload unifies on moved_records `[user]`
- mv adopts the exact entry shape and member name commit uses,
  emitting from the pipeline's OWN record list — grounded: that list
  is already narrowed to the committed message, so the hand-rolled
  ID-narrowing helper and the mv-local entry struct become DEAD and
  are deleted (the fleet dead-surface rule); the reasoning in the
  deleted helper's comment migrates to the emission site. Strictly
  more correct: an observed record reaching mv's payload would now
  carry its origin instead of being dropped. THE SCHEMA `[plan]`:
  `mvPayloadSchema` declares `"moves"` with its entry object in the
  required list — the reshape renames the property to `moved_records`,
  adds `origin`, and updates the required list (or emission-time
  validation fails); the entry-object fragment is FACTORED into ONE
  shared schema fragment consumed by commit's and mv's schemas, never
  duplicated (the single-authority rule). NIL-VS-EMPTY hazard, stated
  `[plan]`: the pipeline's record list is NIL when nothing survives
  (where the deleted mv helper returned a non-nil empty slice), and
  the member's never-null contract breaks exactly on the hook-strip
  path unless the emission site normalizes — the retargeted hook-strip
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
  SANCTIONED HAND EDIT (see the discipline block — `changelog edit`
  cannot modify commit lists); the entry has NO batch exclusion today,
  so appending past the commit limit CREATES one, with a
  purpose-written reason — the commits must not be left for Phase 6 to
  discover uncovered.
**Verify (red first on the retargeted tests):** real and preview mv
payloads carry the unified member with origin; the hook-strip case
reports only what the committed message carries; no reference to the
old member or helpers survives.

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
  docs/cli-rebase.md, docs/cli-index.md) carry the new texts after the
  regen below.
- Then the ONE schema regeneration (`--dump-schema`, which WRITES
  `.strictcli/schema.json`; its own commit via rlsbl commit), then
  bare `selfdoc gen`, then `selfdoc check` must be exit 0.
**Verify:** `git grep` finds zero occurrences of the table preamble's
EXACT flip spelling in `docs/divergences.md` (the vocabulary
definition's own "provisional — awaiting review" wording legitimately
survives) and zero "listed for the review" / "open at the review"
phrases; the roster names no entry; the enumerated debt set closed;
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
    for the empty-pick auto-clean, the one never-shipped fix whose
    final behavior an upgrading user will meet `[user — the hybrid
    ruling]`.
  - Fix: NONE for this round's behavior items — the empty-park
    auto-clean and the out-of-band leftover naming fix symptoms that
    never shipped (their commit coverage rides no-user-facing
    clusters).
  - No-user-facing: the plan file and its ruling commits
    (`--allow-batch` with a reason — they exceed the per-entry commit
    limit), the pins, the predicate hardening, the roster/doc work,
    the 1.1/1.3 behavior commits, and EVERY pre-round housekeeping
    commit sitting uncovered from the planning session (the registry
    policy doc, the coincidence pin and catalog sentence, the two
    promoted tools, the CONTRIBUTING correction, the closing baseline,
    and the git-config-audit todo commit).
  Run `scripts/verify-coverage-partition` on the proposed clusters
  BEFORE writing any entry.
- The census regenerates (`scripts/exit-inventory` into the testdata
  file, committed with its established message — `testdata: regenerate
  the exit-site census`) as the LAST content commit — the release
  hook (`.rlsbl/hooks/pre-checks.sh`) blocks on its freshness.
- The full battery on the quiescent tree: build, vet, gofmt; full
  race suite; the stress run at its 40-minute budget; the
  released-dependency run (`GOWORK=off go test ./... -race`); selfdoc
  check; the changelog checks (`rlsbl check --tag changelog`).
- Reconciliation against `testdata/closing-baseline.txt`
  (`scripts/test-baseline` generates the fresh snapshot; its `--check`
  is a strict diff and cannot serve — the classification is the work):
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
  `todo/next-cycle-candidates.md`), each item SELF-CONTAINED with its
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
    ledger is where the triage NOTES it (the todo itself is immutable).
  Staying active, each its own file: the await-class files (the three
  named await-strictcli, plus
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
  committed; the census re-runs only if remediation touched exit sites
  (the release hook's freshness check is the backstop, not the plan).
- `rlsbl release init`; the release file: bump MINOR (0.28.0 to
  0.29.0), the two-campaign-plus-closing-round description and
  context DRAFTED BY THE ORCHESTRATOR AND APPROVED BY THE USER before
  the release file is committed.
- `actionlint` over the workflows (installed; cheap insurance before
  their first-ever run on this history).
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
| 2 | 1 (catalog + guide; sequencer adjacency) | |
| 3 | 1-2 (catalog + guide files only) | parallelizable with 2 in code — sequence the shared docs |
| 4 | 1 (catalog + guide serialization with 2-3) | 4.1 and 4.2 share no files |
| 5 | 1-4 | executes the Catalog actions table's Phase-5 rows; the ONE schema regen |
| 6 | 5 | census last; battery on the quiescent tree; baseline refresh after reconciliation |
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

## Appendix A — sanctioned rewrites and renames

A baseline break not listed here is a plan defect. Each list below is
THE single statement of its set — phase text points here and never
restates. New tests are not rewrites and are not listed (Phase 6's
reconciliation classifies them separately).

- 0.1: the pipeline-honors-declared-context test gains the
  shared-index base in its request literal.
- 1.1: BOTH fixture builders in the in-flight refusal test file
  re-fixture on raw git (probed working: a raw `git cherry-pick` of an
  already-applied commit parks CHERRY_PICK_HEAD on a clean tree and
  exits 1; a raw `git revert --no-commit` of an already-reverted
  commit parks REVERT_HEAD and exits 0 — the current builders assert
  nonzero on BOTH arms, so the revert arm's exit assertion adjusts;
  neither park creates a `.git/sequencer` directory); the two
  state-producer pins INVERT AND RENAME
  (`TestANoChangeSecondPickParksItsState` /
  `TestANoChangeSecondRevertParksItsState` — their names claim
  parking; the renamed tests assert the state is GONE and pin a
  distinctive phrase of the new cleaned-up sentence); the consumer
  test functions re-fixture in place — the two
  pick/revert-refuse-over-parked tests, the pick preview variant, the
  two merge-over-parked tests, the pull-over-parked test's inline
  fixture, and the way-out test (whose subtests are why an item count
  would mislead). The already-applied-refusal test keeps its
  "no change" phrase and stays green.
- 1.2: none beyond the new tests (the two no-commit-stays-passthrough
  pins run with nothing in flight and stay green — verified).
- 1.3: none (verified — no test asserts the current one-line message).
- 2: none (no test pins the unborn refusal — verified; the friendly
  refusals replace raw git fatals no test asserts).
- 3: the absolute-inside-committed pin inverts into the refusal test
  (its own comment says it is the one to rewrite); the wording
  assertions across the suite follow the new messages; the
  inside-target and traversing pins RETARGET to a distinctive phrase
  of the new notice (they assert the ABSENCE of "outside the
  repository" and would go silently vacuous under the reword — the
  same trap as the mv hook-strip test); the mv carve-out pin updates
  for the flag rename only, and ITS COMMENT is corrected (it claims
  "mv simply never calls the intake that judges links" — false: mv
  reaches `resolveFiles` with no file specs, so the offender set is
  empty by construction); the test functions renamed to the
  non-portable vocabulary, enumerated `[re-extracted at revision
  time]`: `TestCommitSymlinkEscapingTargetIsCommittedWhenElected`,
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
  messages) update as a COUNTED mechanical pass — enumerate by
  `git grep` at execution time, assert the count, review the diff —
  not an open sweep.
- 4.1: the strategy-option refusal rows leave their tests (the merge
  raw-shapes row, the merge preview row, the pick/revert selection
  test's option member and its doc comment); the pick/revert
  preview-refusal test takes `--strategy resolve` as its substitute
  unsupported option (NOT `-s resolve` — on pick/revert `-s` is the
  allowed signoff spelling and `resolve` would parse as a second
  revision; `-s resolve` remains valid only in merge's tests); the
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
- 5: the flip-only batch per the Catalog actions table (count asserted
  before, diff reviewed after); the autostash entry's edit.
