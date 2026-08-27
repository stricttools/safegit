# Next-cycle candidates

The deferred-work ledger written at the close of the 2026-08 closing
round, immediately before the two-campaign release. Every item below
was deliberately left out of that round's scope; each is recorded here
with enough context to be picked up cold, because the plan files that
carried the original reasoning move to `todo/.done/` in the same
triage that writes this file — a pointer into a completed plan is not
context.

Nothing here is a commitment and nothing here is ordered. Items are
independent unless an item says otherwise. Several items are
observations recorded so that a later session does not "fix" correct
code by reading a stale plan sentence; those say so in their own text.

---

## Envelope-always: adopt it when the framework's error-payload channel ships

The campaign ruled that every command answers a machine consumer with
the framework envelope on stdout, including when it refuses. That is
as far as the framework can currently be pushed: strictcli has **no
error-payload channel**. A refusal can emit the envelope with
`payload: null`, but it cannot carry a structured description of what
was refused, because the command's declared payload schema describes a
performed operation, not a refusal.

The concrete case that forced the record: `safegit mv` must name
**every** dirty path it refuses over, never truncated (a user ruling).
That listing cannot ride the payload for the reason above, so as built
it goes to stderr — which machine mode never suppresses — while the
envelope carries `exit_code` 19 and `payload: null`. This is the
documented refusal shape today.

The deferred decision is whether a refusal payload should exist at
all, and if the framework ships an error-payload channel, which
refusals adopt it. Two other items in this ledger (the seven
`payload: null` reporting commands, and `commit`'s envelope-less
refusals) are the same decision seen from different commands and
should be decided together with this one.

Source: `todo/campaign2-plan.md`, section 3.1 ("The family code (exit
26) and envelope-always") and the open ruling recorded in its Phase 5
mv material.

## `doctor --action fix` in an uninitialized repository repairs nothing, and does not say so

`doctorFix` carries an `IsInitialized` precondition. In a repository
that is not initialized, the fix path repairs nothing and emits no
notice explaining why. The state is unreachable from the exit-28
(`NotInitialized`) path that fronts the command, so nothing is broken
today; the gap is that if it were ever reached, the command would
succeed silently having done nothing.

The candidate work is a notice on that arm — one sentence naming the
uninitialized repository as the reason no check ran.

Source: `todo/campaign2-plan.md`, Phase 4 notes (the row recording
"doctorFix's IsInitialized precondition … unreachable from the exit-28
path, noted for the audit").

## Conclusion execute-path working-tree writes are not minted through the effects handle

The effects-handle adoption covered a ruled list of six commands. The
conclusion commands (`merge-continue`, `cherry-pick-continue`,
`revert-continue`) write the working tree when they materialize a
`--resolve` declaration, and those writes are **not** minted as
effects — so they do not appear in the `preview` record a machine
consumer reads, and they are reported in prose instead.

This was ruled out of the adoption's scope deliberately (the
conclusions are outside the six-command list), not overlooked. The
candidate work is extending the mint to the conclusion execute path so
the machine record is complete.

Source: `todo/campaign2-plan.md`, Phase 4 audit-noted list ("conclusion
execute-path worktree writes stay unminted (outside the ruled
six-command list; reported in prose)").

## `doctor --action fix` output reads as disconnected finding/fix lines

**Context reconstructed.** The original reasoning for this item was
lost with the session that raised it; the sentence below is a
reconstruction and is marked as such so nobody treats it as a verbatim
record.

`doctor --action fix` prints what it found and what it did as separate
lines with no connective wording between them, so a reader has to
infer which repair answers which finding. The candidate work is
wording polish: make each fix line state what it is repairing.

This is presentation only — no behavior, no exit code, no machine
payload changes.

## `reset --hard` silently clears parked sequencer state

Running `safegit reset --hard` over a parked merge, cherry-pick or
revert discards that parked state without a word. This is git's own
behavior under a non-authoring passthrough: safegit takes the worktree
operation lock and the uncommitted-work check, then hands the reset to
git, and git clears the state files.

It was surfaced during the closing round's design review and
deliberately ruled out of that round's scope. The sibling observation
about rebase over parked state **was** ruled into scope and is
implemented; this one is the remaining half.

The candidate work, if it is wanted at all, is a pre-flight notice or
refusal when a reset that writes the working tree would clear parked
state. Note that refusing would diverge from git under a command
documented as a guarded passthrough, so it needs a catalog entry.

Source: the closing round plan's "Recorded observations" section.

## No oplog entry is written for a compute that turns out empty

When `merge`, `cherry-pick` or `revert` computes a result that changes
nothing, the operation unwinds and no oplog entry is written — before
or after the closing round's cleanup work. Whether "computed and
unwound" deserves an audit-trail entry is an open question, not a
defect: the oplog's consumers (undo's arithmetic, doctor's bypass
baseline) do not need it, and an entry claiming a ref position would
have to be a failed-outcome entry to stay out of their way.

Source: the closing round plan's "Recorded observations" section.

## A probe-backed inventory of git's own failure messages across the supported surface

The closing round adopted an **additive friendly-errors direction**:
safegit may add probe-backed pre-flight refusals where a known corner
produces a misleading or hostile raw git failure, each one individually
cataloged in `docs/divergences.md`. Wholesale wrapping of git's stderr
is permanently ruled out. The round shipped the starting set — the
unborn-branch refusals (`merge --no-commit`, `merge --no-ff`, `pull
--merge-strategy no-ff`, `rebase`, `bisect start`) and the
no-merge-base refusal for unrelated histories.

What is deferred is the **evidence base** for any future addition: a
systematic inventory rather than one-off discoveries. The method, as
ruled: force each failure corner of the supported surface in scratch
repositories, capture git's actual message verbatim, and grade it.
Everything graded misleading or hostile becomes a candidate pre-flight
refusal; everything graded clear stays git's to answer. The inventory
is the artifact — a table of (command line, git's message, grade) —
and it is what a later cycle would work from instead of re-probing.

A concrete first entry is already known and recorded in this ledger:
an unborn `merge <unresolvable-ref>` surfacing git's "Non-fast-forward
commit does not make sense into an empty head".

Scratch repositories for this work go in `experiments/` per the
project conventions.

Source: the closing round plan's Phase 2 and Phase 4.1 friendly-refusal
items, and the conventions bullet Phase 5 added to the CLAUDE template.

## Note: the dry-run-network await todo's immediate work is already done

`todo/dry-run-network-await-strictcli-ruling.md` stays active — it
awaits a framework ruling. But its "Work / Now (independent of the
ruling)" paragraph asks for the docs' "dry runs never touch the
network" overclaim to be struck, and **that has already happened**:
the phrase no longer occurs anywhere in the tracked docs.

The todo itself is immutable and is not edited. This is the note that
a future triage should read the todo's remaining scope as the
ruling-dependent half only.

## The `residue` schema fragment is written out seven times

An identical entry-object schema fragment appears in the payload
schemas of seven commands: `cherry_pick_cmd.go`, `commit.go`,
`merge_cmd.go`, `mv.go`, `revert_cmd.go`, `sequencer_continue_cmd.go`
and `undo.go`. Each spells the same `strictcli.SchemaArray(
strictcli.SchemaObject(...))` shape by hand.

The finding is the duplication, and the deliverable is the N-to-1
reduction: one shared fragment constructor the seven call, so a change
to the residue shape cannot land in six places and miss the seventh.
The closing round did exactly this reduction for the moved-records
fragment; the residue one was deliberately deferred, not judged
unnecessary.

Source: the closing round plan's Phase 8 ledger list (the item names
the same shape as the moved-records fragment its Phase 4.2 unifies).

## Hand-typed structural counts in shipped prose are a stale-risk class

Shipped documentation contains hand-typed structural claims about the
code — "guarded twice", "both layers", command enumerations, and the
commands guide's "two coordination layers" section title (which has
been imprecise since before the closing round and was deliberately
left unrenamed). Nothing generates or verifies them. They are correct
today because each round sweeps them by hand.

The class-level fix is to make them enumeration-derived or generated
with a freshness test, the way the exit-code table and the doctor
health-check table already are. Any such number that cannot be
generated should be reworded so it carries no count at all.

Source: the closing round plan's falsified-surfaces discipline (its
subphase 1.2 item) and its Phase 8 ledger list.

## The release hook runs `-short` while CI runs the full suite

`.rlsbl/config.json`'s `hooks.pre_release` runs `go test ./... -race
-short -count=1`, while the CI workflow deliberately runs without
`-short` (its own comment says so) on the same candidate commit.
Nothing ships unexercised — the candidate is pushed untagged and CI
runs the full suite before the release finalizes — so this is an
asymmetry, not a hole.

It was examined during the closing round and parked there by an
explicit ruling. Two neighbours are **deliberate and must not be
"fixed" by a sweep**: `scripts/test-baseline`'s `-short` is required
for artifact determinism (the committed baseline's header records that
argv), and `-short` genuinely changes what runs (it skips the hooks
timeout test and shortens the reconcile property test).

The candidate work is deciding whether the release hook should match
CI, and if so, what the local pre-release wall-clock cost of dropping
`-short` is.

## Seven reporting commands answer `payload: null` under `--json`

`config show`, `config get`, `hook list`, `hook run`, `hook migrate`,
`backup list` and `doctor` all emit the envelope with a null payload
under `--json`, so their information reaches a machine consumer only
as prose on stderr. `version`, `scan`, `author list` and `author
check` do emit structured payloads.

The final audit raised this as a MAJOR finding. It was resolved
conservatively before the release: the documentation sentence that
claimed machine-mode payload coverage was **narrowed to the truth**
(it now names which commands carry a payload) rather than payloads
being added under release pressure. That is a documentation fix, not a
design answer.

The deferred decision is whether these seven should carry payloads.
It rides the envelope-always item above, because both turn on what the
framework's payload channel can express. **Flagged for user review** —
this is the one item in this ledger where the shipped behavior was
chosen by a pre-release judgment call rather than by a deliberate
design ruling.

## `commit`'s refusals emit no JSON envelope at all, while `mv`'s emit one

`safegit commit`'s entire refusal surface exits through `die()`, which
bypasses the dispatch seam that writes the envelope — so a machine
consumer gets nothing at all on stdout. `safegit mv`'s refusals emit
the envelope with `payload: null` for the same refusal class. Two
different answers to the same question.

The exit-29 symlink refusal is the documented example of the
no-envelope shape; the closing round widened the documentation to say
the shape covers commit's refusals generally, not just that one.

Rides the envelope-always item — the honest end state is one answer
for both commands.

## A fast-forward merge preview's machine answer is empty

`--dry-run merge <ff-able>` now exits 0 with `payload: null` and an
**empty** `preview` list. That is the state after the closing round
suppressed a false record: the preview used to record a `git merge`
invocation that a real fast-forward never runs (safegit decides the
fast-forward itself and moves the ref under compare-and-swap). Removing
the lie was the right half of the fix; the remaining half is that the
preview now records nothing at all, so a machine consumer cannot see
that a ref move would happen.

The honest end state is a would-do record naming the **real** effect:
the ref move, with its target. That needs `effectsRefUpdate.Update` to
accept a known new tip instead of the placeholder it currently
substitutes for a commit that has not been written yet — in a
fast-forward the new tip is known before the effect is recorded, which
is exactly the "placeholders are for unknowable values" doctrine.

This is documented in the `--dry-run` entry of `docs/divergences.md`.

## Two preview sentences describe an outcome the real run would not take

Both are wording defects in the human preview line; the recorded
effects are correct in both cases.

- `--dry-run merge --no-commit <ff-able>` says the merge would be "a
  fast-forward", but the real run **parks** — safegit always injects
  `--no-ff` when parking, so a `--no-commit` merge is never a
  fast-forward. The fast-forward predicate the preview asks omits the
  `--no-commit` condition the real path applies.
- The same sentence appears on a detached HEAD, where the real
  `performMerge` requires a resolvable ref and refuses; `previewMerge`
  does not ask.

The fix is to give the preview the real path's predicate rather than a
second, looser copy of it — which is also the structural point: the
two predicates should be one function.

## The way out of a CONFLICTED park is git's abort, not safegit's

With a cherry-pick or revert parked on a **conflict**, `safegit
cherry-pick --abort` / `safegit revert --abort` refuse at exit 5: the
conflict markers in the working tree are uncommitted work, so the
dirty-tree coordination layer refuses before the abort reaches git.
Raw `git cherry-pick --abort` works and is what the refusal points at.
This is pinned as current behavior by an end-to-end test.

The design question, deferred: `--abort` is a plain passthrough that
authors nothing, and its whole purpose is to destroy the state the
dirty-tree check is protecting. Whether safegit's own abort should
pass the dirty-tree layer over a conflicted park — and if so, whether
it should be limited to the paths the conflict itself owns — is a
behavior question for the user, not a bug to fix unilaterally.

## Conclusion `--resolve` stages content without the non-portable-symlink-target judgment

The closing round made a non-portable symlink target (any absolute
target, or a relative one resolving outside the repository) a refusal
at exit 29 on the commit intake path, with
`--allow-non-portable-targets` electing to commit it anyway.

A conclusion command's `--resolve path=worktree|ours|theirs` stages
content through a different path and is **not** subject to that
judgment: concluding a merge whose resolution is a symlink with a
non-portable target commits it without a word.

This is pre-existing — the conclusions never had the judgment — and
the round documented the scope honestly rather than widening it under
release pressure. Whether conclusions should judge link content is a
design question: a conclusion's content comes from an index stage git
itself wrote, which is a different situation from a path the operator
named on a commit line.

## `--dry-run revert <root-commit>` fails while the real run works

Previewing a revert of a root commit fails while resolving the
commit's parent, which a root commit does not have. The real run
handles the case (it substitutes the empty tree). The candidate fix is
the same substitution in `previewReplay`: where the parent does not
resolve and the commit is a root commit, use the empty tree.

Small and self-contained; needs a red-first preview test on a
single-root fixture.

## An unborn `merge <unresolvable-ref>` surfaces git's misleading empty-head fatal

On an unborn branch, `safegit merge <ref-that-does-not-resolve>`
reaches git and comes back with `Non-fast-forward commit does not make
sense into an empty head` — a message about the wrong thing entirely
(the caller's ref does not exist; whether the merge could
fast-forward is not the issue).

The round documented this in the commands guide rather than adding a
refusal, because the friendly-refusal set was closed at its ruled
starting members. It is a named first candidate for the friendly-errors
inventory item above.

## Unborn `pull --dry-run --merge-strategy no-ff` refuses before previewing

On an unborn branch, `pull` with `--merge-strategy no-ff` refuses at
the friendly pre-flight — including under `--dry-run`, where it
therefore never produces a preview. The refusal sits before the fetch
by design (so an unborn pull never pays for a network round-trip
before dying), and the pre-flight predicate does not distinguish the
dry path.

The final audit endorsed this as the literal reading of the ruling
that put the refusal before the fetch. Recorded as reviewable: a
preview that refuses rather than previewing is defensible for a
command line the real run also refuses, and that is the case here — so
this is a consistency question, not a defect.

## The unrelated-histories pre-flight fires before the `--ff-only` refusal

`safegit merge --ff-only <branch-with-no-merge-base>` reports the
**unrelated-histories** refusal, not the `--ff-only` / ancestry one:
the no-merge-base pre-flight is asked first. Both refusals are correct
answers to that command line (an unrelated branch is also not a
fast-forward), so the question is only which one is more useful to
read. As-built ordering; recorded as reviewable.

## Smaller recorded items

Each of these was observed during the closing round and is too small
to carry its own section. One line of context each; none is blocking.

- **The exit-site census grouping is structurally vacuous.** The
  generated `testdata/exit-sites.txt` has one section
  (`== exit non-literal`) and ends with `total exit sites: 623 across
  1 distinct codes`, because every site now uses a named
  `exitcode.` constant rather than a literal — while
  `scripts/exit-inventory`'s own usage comment still advertises "one
  section per code". Either group by the resolved constant name or
  reword the script's comment and drop the count sentence.
- **`doctor --action uninstall`'s machine mode loses information the
  text carries.** The human output marks which enumerated paths are
  outside the invoking worktree; the payload does not.
- **The framework's no-terminal consequential refusal does not name
  `--approve-consequential`.** The message says stdin is not
  interactive and stops; the flag that answers it is left for the
  reader to know. Framework-owned — this belongs upstream, not here.
- **The autostash repair failure hides git's stderr.** In
  `doctor_repair.go`, the arm that fails to store an orphaned
  `MERGE_AUTOSTASH` as a stash entry reports the failure without
  git's own message, which is the only thing that would say why.
- **Bypass detection flattens multi-line git stderr into a table
  cell.** `bypass_detect` renders git's stderr into a one-line table
  column, so a multi-line message is unreadable at exactly the moment
  it is needed.
- **One preview-forwarding integration pin cannot fail for its stated
  reason.** The two-option integration test asserting that strategy
  options reach the preview passes under conditions its own comment
  says would break it; the unit pin beside it is the real guard. Fix
  the test or delete it in favour of the unit pin — a test that cannot
  go red is not a test.
- **The allowed/refused option sets are stated in three places.**
  `subset_allowlist.go`, the divergences catalog table, and the
  commands guide each enumerate them, with no freshness mechanism
  between them and no test that reads `docs/divergences.md` at all.
  The reduction: generate the doc tables from the allowlist tables,
  the way the exit and doctor tables are generated.
- **A valueless trailing `-X` in a merge preview leaks internal
  argv.** The preview surfaces safegit's own `merge-tree` argv in the
  error instead of an option error naming `-X`.
- **The cleanup-failure branch of the empty-park auto-clean has no
  test.** When a pick or revert computes empty, safegit removes the
  state it parked; the branch where that removal itself fails (which
  names the leftovers and restores the abort advice) is unexercised
  because it needs fault injection.
- **A contrived `merge --unknown-value-flag --no-ff <branch>` could
  strip `--no-ff`.** In `withoutMergeSelectors`, an unknown flag
  consuming the next argument as its value would swallow the `--no-ff`
  selector. No spelling reachable today produces it — every value flag
  merge accepts is known — so this is a latent shape, not a live bug.

## Recorded with NO action: two stale plan sentences the tree already answers better

Both are recorded here so that a later session reading a completed
plan does not "fix" correct code to match a sentence that was wrong.

- Campaign 1's plan claims `push` "re-observes and re-pins per
  attempt". The tree pins **once**, and its comment argues why:
  re-observing the remote before each retry would defeat the point of
  the lease, which is to reject a concurrent write rather than adopt
  it. The tree is more correct than the plan.
- The closing round plan's subphase 1.2 Verify block says "a
  conflicted rebase's own `--continue` reaches git". What the tree
  actually does: a rebase carrying staged resolutions refuses at exit
  5 (the dirty-tree check), and the shape that reaches git is the
  **clean parked interactive** one. The passthrough is correct; the
  plan sentence describes the wrong fixture.
