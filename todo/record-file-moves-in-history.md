# Record file and directory moves in repository history

## Context

Git stores snapshots, not operations. A move is never recorded: the commit
contains a deletion at the old path and an addition at the new path, and every
consumer (git log --follow, diff -M/-C, blame) reconstructs the move at read
time with similarity heuristics that are allowed to be wrong. safegit
additionally has automatic move detection inside commit: a newly-added file
whose blob matches a parent-tree blob triggers an auto-staged deletion of the
"old" path.

An earlier version of this file surveyed ten candidate approaches. The design
has since been settled; this file records the decisions and the remaining open
points. Red tests for the defects in the current behavior are committed in
internal/test/moves_cross_session_test.go.

## Decision: automatic move detection is removed

Adversarial testing proved the detection heuristic unsafe by construction: a
same-session move and another session's unrelated uncommitted deletion produce
byte-identical repository state, so no similarity floor, blob-uniqueness rule,
or directory restriction can tell them apart -- the discriminating information
does not exist. Measured consequences (all red-tested): detection
deterministically adopts another session's deletion into an unrelated commit
(empty files collide universally), ties between victims are settled
alphabetically, the adoption is completely invisible under --quiet and --json,
the stderr notice asserts a rename that never happened, and the victim session
is afterwards hard-blocked from committing its own deletion.

Automatic detection and its auto-staged deletions are deleted entirely,
including the detectMoves pass in internal/commit/moves.go and its invocation
from both commit and amend. Moves become declared.

## Decision: moves are declared, two ways

1. **`safegit mv <old> <new>`** -- a new verb that performs the filesystem
   move and commits it in one operation (one commit per move; safegit has no
   persistent staging area by design, so a stage-only mv has no home in the
   architecture). The move record is written as part of the commit it creates.
2. **Explicit both-path pairing in `safegit commit`** -- when the caller names
   BOTH the old path (absent from disk, tracked in the parent) and the new
   path in the pathspec, and the old path's blob in the parent commit hashes
   identically to the new path's blob, the commit records the pair as a move.
   No blob match, no record. This makes `mv old new` via the shell followed by
   `safegit commit -- old new` produce a record, closing the hole where an
   ordinary manual move went unrecorded.

Anything else (a hand-written record via the existing `--trailer` flag) is
possible because the record format is an open convention, but safegit vouches
only for records it writes itself.

## Decision: storage is commit-message trailers

Each record is a `Moved:` trailer on the commit that performs the move. One
trailer line per record; repeated keys are the normal trailer idiom (as with
Signed-off-by) and git's tooling extracts them per-line natively. A
from/to-split across two trailer keys was considered and rejected: nothing
guarantees two lines stay adjacent or both present, so pairing by position is
fragile, while a single line is a self-contained record that survives
reordering and filtering. A single key carrying a JSON array of all moves was
also rejected: it fights the line-oriented carrier, collapses per-record
extraction and grep, and makes corrections unable to reference one record.

Why trailers over any separate ledger (local journal, tracked file, notes ref,
dedicated ref):

- **Concurrency-safe by construction.** No shared mutable file exists, so
  concurrent sessions cannot conflict over the record store -- the hazard
  safegit exists to remove. A tracked central file appended by every moving
  commit is merge conflicts by construction and would have to be silently
  staged into commits whose pathspec never named it.
- **Rewrite-immune.** The record rides the commit through scrub, and being
  paths-only it never needs hash remapping.
- **Undo-correct automatically.** `safegit undo` of a move commit removes the
  record with the commit; no compensation logic. A later semantic move-back is
  simply a new record.
- **DAG-anchored by construction.** "As of commit X" is answered by X's
  position in history; no anchors are stored.
- **Queryable today.** `safegit scan --target trailers` already searches
  trailers across all of history.

Multi-hop history (A to B in one commit, B to C in a later one) is the chain
of per-commit records; nothing is ever overwritten because each record lives
in an immutable commit.

## Decision: record grammar

- **Paths only, never commit hashes.** This is what keeps records stable
  under history rewrites.
- **File form:** old path and new path, e.g. `Moved: src/a.py -> lib/a.py`
  (exact syntax pending the encoding decision below).
- **Subtree form:** a trailing slash means everything under the prefix, e.g.
  `Moved: src/old-name/ -> src/new-name/` -- one record for a directory move
  regardless of file count, with per-file answers derived by prefix
  application at query time.
- **Corrections are append-only.** Commit messages cannot be edited after the
  fact, so a wrong record is repaired by a later commit carrying a correction
  record that retracts or amends it -- an ordinary commit, never a history
  rewrite. Readers fold corrections when projecting. Exact correction
  semantics (retract vs replace, and how a correction identifies its target
  record by content) are an implementation-plan detail.
- **No confidence field is stored.** Whether a move was content-identical is
  recomputable by any reader: compare the old path's blob in the commit's
  parent tree with the new path's blob in the commit's tree. Storing a claim
  that can be recomputed creates a second authority that can contradict the
  first; readers derive exact-vs-declared at read time instead. (Records
  safegit writes are verified before writing: mv is exact by construction,
  pairing records only on a verified blob match.)
- **No similarity scoring.** A declared move needs no corroborating score;
  similarity was only ever needed to power inference, which is removed.

## OPEN: value encoding

No separator character can be guaranteed absent from filenames (Unix allows
every byte except NUL and the path separator, and both of those are unusable
in a trailer line), so an escape mechanism is unavoidable. Two candidates,
both established conventions rather than inventions:

1. **Arrow form with git's C-style path quoting.** Bare `old -> new` by
   default; a path containing whitespace, a double quote, a backslash, a
   newline, or the literal arrow sequence MUST be C-quoted (slightly stricter
   than git's default core.quotePath trigger, so the bare token stream parses
   unambiguously). Maximally readable in git log; quoting fires only on
   pathological names; the convention is git's own.
2. **One JSON object per trailer line**, e.g.
   `Moved: {"from": "src/a.py", "to": "lib/a.py"}`. Zero invented grammar,
   escaping solved by JSON strings, uniform machine parsing; every record
   pays the syntax cost and log output reads as payload rather than prose.

Same schema and semantics either way; decide before implementation.

## Consumers

- `safegit scan --target trailers` works with no new code.
- A **derived local query index** under `.git/safegit/` for fast "where did
  this path live at commit X" queries: built by walking trailers (folding
  corrections), regenerable at any time, never authoritative -- a doubted or
  stale index is regenerated, not repaired.
- A future scrub could consume the records for rename-aware remapping of
  file-targeted rewrites across a file's whole life. Design note only;
  nothing to build now.

## Documentation

safegit's docs describe the `Moved:` trailer as a repo-level convention --
any tool may write or read these records; the grammar is stated fully
(writer and reader behavior, subtree form, corrections, encoding). No
separate specification artifact: the docs section is the reference, and can
be promoted to a standalone document if a second independent implementer
ever appears.

## Affected areas

- `internal/commit/moves.go` -- deleted (detection removal), along with its
  call sites in commit and amend
- `main.go` -- `mv` command registration; pairing wiring in commit
- `internal/trailer` -- writing is one AppendCustom call; reading needs a
  key-value trailer parser plus correction folding (does not exist today)
- `internal/git` -- blob-compare helpers for pairing verification (hash
  plumbing exists)
- `internal/test` -- existing TestMoveDetection_* green tests retire with the
  detection; moves_cross_session_test.go flips green via the removal; new
  tests for mv, pairing, subtree records, corrections, the derived index,
  and the encoding rule
- docs -- the convention section; removal of auto-detection from all
  descriptions

## Open items besides encoding

- Whether `safegit mv` accepts multiple old/new pairs in one invocation (one
  commit, several records) or exactly one pair per commit.
- Correction-record semantics (retract vs replace; target identification).

## Effort estimate

Detection removal: small (deletion plus test retirement). mv + pairing +
trailer emission: a few days including tests. Trailer parser + correction
folding + derived index: a few days. Docs: a day.
