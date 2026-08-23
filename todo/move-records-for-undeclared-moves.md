# Move records for undeclared moves

## Status

Direction set by the user: move knowledge must exist for moves that were
NOT made via `safegit mv` or `--moved`, because in practice most moves
are made with plain `mv`, scripts, editors, and refactoring tools. This
overturns the earlier "blob equality never decides record existence"
stance in direction. The design below was drafted in response and is the
recommended shape; it has not been through an adversarial critique yet,
and implementation details flagged under "Decisions to settle" await a
final ruling.

## Context

safegit records file moves as structured commit-message trailers:
`Moved: <ulid> old -> new` (C-quoted paths, a trailing-slash subtree
form for whole-directory moves), with `Moved-Retract: <ulid>`
corrections folded by readers. As shipped, records are DECLARED ONLY:
minted by `--moved` on commit/amend, by `safegit mv`, and as automatic
inverses on a single revert. A read-side projection exists in
`internal/trailer` (records are claims, trees are the arbiter: a record
applies only when the old path is in a parent tree and the new path is
in the commit's own tree; longest-prefix subtree matching; retraction
folding; malformed classification). A reader command is tracked in
strictcli's todo directory. Scrub is planned to project path patterns
backward through records so that directory-layout migrations in
consuming repositories do not require rewrite tools to hard-code old
layouts.

Historical constraint that shaped the declared-only stance: safegit
previously had automatic move detection and it was deleted for a proven
bug — but the bug was detection-as-STAGING, not detection-as-knowledge.
The old mechanism paired a caller-named new file with a staged deletion
found in the SHARED index (possibly staged by a different concurrent
session) and auto-staged that deletion into the caller's commit,
changing what got committed. Empty-blob collisions and alphabetical
tie-breaks made it deterministically adopt other sessions' deletions.
Regression tests pin that deletions are never auto-staged and that
cross-session adoption cannot happen. Nothing in this todo may weaken
those properties.

## Problem

Under declared-only, an agent that runs `mv a.go b.go` and then
`safegit commit -- dir/` produces a commit containing the deletion and
the addition — and no record. The reader answers nothing, the scrub
projection has a hole, and the record system's value collapses to
deliberate operations only. Most real moves are exactly this shape.

## The design (recommended)

One principle: safegit records what it can observe from the commit's
OWN delta at the moment it authors the commit; everything else is
computed on demand at read time and never silently written. Every
record states its origin (a claim someone made vs an observation
safegit made).

### Write-time minting (the core)

In the commit pipeline, after the tree delta is computed from objects
and before message assembly, pair THIS COMMIT'S OWN deletions against
THIS COMMIT'S OWN additions by exact blob hash. Every unambiguous
one-to-one pair mints a `Moved:` record automatically, carrying an
origin marker distinguishing it from a declared record. Declarations
always win and suppress inference for any path they name on either
side.

Safety fences (each one load-bearing against the historical bug class):

- Inference reads ONLY the commit's own delta. It cannot see another
  session's staged deletion; cross-session adoption is structurally
  impossible. It changes only the message, never the tree.
- Ambiguity is REFUSED, never resolved. Two candidates for one blob on
  either side means no record for any of them, plus a one-line notice
  suggesting the explicit `--moved` declaration. No tie-break of any
  kind ever returns (the alphabetical tie-break was the old bug's
  mechanism).
- Empty blobs never pair. (Consider whether a minimum-size floor is
  wanted; empty is the mandatory exclusion.)
- Commits built from the shared index (the conclusion commands, whose
  delta can legitimately contain another session's staged work) skip
  inference entirely.
- A false inferred record is retractable by id like any record, and the
  projection's tree arbitration ignores records history contradicts.

### Read-time computation (history and split moves)

The reader command answers from records first; where records are
silent, it computes: same-commit blob pairs, then cross-commit pairs
(the deletion committed in a later commit than the addition — the other
common undeclared pattern), then content similarity for
modified-while-moved files. Computed answers are labeled computed and
NEVER written — a wrong computation is fixed by fixing the algorithm,
and nothing pollutes history.

### What scrub consumes

Written records only, both origins — they are fixed text in history and
therefore deterministic. Computed read-time answers are never scrub
input (a rewrite may not depend on which safegit version asked). Where
old undeclared history leaves the projection blank, the blank is
reported as a blank.

### The subtree rule stays absolute

No inference ever mints a directory record: a subtree line claims
things about descendants the commit does not contain, and only a
person's declaration may assert that. Layout migrations keep using
`safegit mv` (one command, one commit, one record per directory) while
ordinary file moves around them are covered automatically.

### Deliberate reversals this design requires

- The published contract line in commit's help ("nothing is inferred
  from file contents", main.go near the --moved registration).
- The declared-only doctrine comments (internal/commit/moved.go top
  comment; internal/trailer/project.go's "nothing is stored about
  confidence" stance — an origin marker IS stored confidence).
- A new entry in docs/divergences.md describing the
  declared-plus-observed model and the refusal-on-ambiguity rule.

## Alternatives considered

- Advisory notice only (suggest the --amend --moved command line,
  record nothing): captures no knowledge automatically; converts moves
  to declarations only at the rate notices are obeyed. Kept as the
  handler for REFUSED ambiguous pairs, rejected as the whole answer.
- Post-hoc adoption command (propose pairs over the tip, approve, amend
  them in): every record stays declared, but reaches only the unpushed
  tip and depends on someone running it — the original problem one step
  removed. Note the structural fact that makes retroactive declaration
  impossible without rewriting: a record only validates on the commit
  whose parent held the old path, so records about old moves cannot be
  attached to later commits.
- A separate trailer key for inferred records instead of an origin
  token inside the value: same information, but a second key must be
  taught everywhere records are handled and tends to get absorbed into
  an origin/basis model later anyway; building the origin token
  directly avoids a grammar migration.
- Filesystem rename witnessing (inode identity at commit time, or a
  rename-watching ledger): covers modified-while-moved and eliminates
  identical-content collisions, at the cost of platform machinery and a
  process lifecycle. A coherent later extension, not core.
- A knowledge layer outside commit messages (notes-style refs with a
  deliberate promotion step turning reviewed computed answers into
  permanent records for OLD commits): closes the retroactivity gap
  without rewriting; deferred until the projection holes in old history
  hurt in practice.
- Similarity matching at write time: more coverage, more false
  positives, heavier; read-time-only for now.

## Affected

- internal/commit/commit.go — the mint site sits between the delta
  computation and message assembly in tryCommit; note that declared
  records are resolved once before the CAS retry loop while inference
  recomputes per attempt against the winning parent (correct: the
  record describes the step from the parent that won; ids differing per
  attempt is fine, only one commit survives).
- internal/trailer — the record grammar gains the origin token
  (moved.go encoder/decoder); the projection's precedence gains
  declared-beats-inferred at the same commit (project.go); THREE
  handling sites must learn any grammar extension or it breaks quietly:
  MovedLines (preserves record lines verbatim across amend/reword — an
  unrecognized shape gets dropped), RewriteMessage (transforms
  unrecognized trailers as free text and would corrupt quoting during a
  scrub), RemoveMovedRecordsNaming (scrub file --delete record
  removal).
- Amend/reword semantics: inferred records are DERIVED data, recomputed
  per authoring event against the new delta; declared records are
  preserved claims (existing preservation rules unchanged). The
  re-declaration refusal (an identical un-retracted pair refuses naming
  the existing id) needs a defined interaction: a declaration matching
  an existing INFERRED pair should supersede it, not refuse.
- internal/git — DiffTree deliberately passes --no-renames; write-time
  pairing works from the existing delta and needs no rename plumbing,
  but read-time similarity would.
- Tests: the pinned regression strings in the move-detection removal
  tests ("rename detected", "auto-staged deletion") and the
  cross-session cannot-happen guards must be re-pointed DELIBERATELY at
  the property that actually needs pinning (no content adoption, no
  auto-staging) so the new minting notice cannot trip them; new
  red-first tests for minting, ambiguity refusal, empty-blob exclusion,
  shared-index skip, declaration suppression, retraction of an inferred
  record, and the amend recompute.
- Payload: the commit payload should carry minted-inferred pairs and
  refused-ambiguity facts so machine consumers see them.
- Docs: commands-guide declared-moves section, the divergence entry,
  the reader-command design in strictcli's todo gains "records may be
  declared or observed; computed answers are labeled".

## Decisions to settle before implementation

- The origin token's exact grammar (a token between the id and the
  pair is the natural slot; must be unambiguous against C-quoted
  paths).
- Whether scrub recipes ever need an origin filter (current stance: no
  — all written records are consumed; revisit only if false inferred
  records are observed in practice).
- Whether the ambiguity notice is quiet-suppressed (payload carries the
  fact regardless).
- Minimum-blob-size floor beyond the empty exclusion, if any.

## Effort

Medium. The mint site, the origin token, and the three trailer-handling
sites are the core; the deliberate test re-pointing and the contract
reversals are the careful part; read-time computation rides the reader
command's design (tracked in strictcli) rather than this todo.
