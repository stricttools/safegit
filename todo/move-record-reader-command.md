# A reader command for move records

## Context

The move-record system writes `Moved:` trailers (declared via `--moved`,
`safegit mv`, inverse records on revert) and `Moved-Retract:` corrections.
The read layer exists in `internal/trailer`: a forward projection that
follows a path across records, validates claims against trees (trees are
the arbiter), folds retractions, resolves longest-prefix subtree matches,
and classifies malformed lines. It is property-tested and correct — and
consumed by nothing in production. Records are currently write-only
history: no command answers "where did this path come from?" or "where
did it go?", and a malformed record in history is invisible to operators
(scrub's record removal silently skips lines it cannot parse; the
Malformed classification is populated and read by tests only).

## Problem

A record system nobody can read delivers none of its value, and the
projection API sits in the built-but-unwired state the fleet's dead-code
policy exists to prevent. This todo is the recorded consumer that
justifies the API's existence; if this todo is ever abandoned, the
projection should be deleted rather than kept speculative.

## Solutions

1. **A query command** (recommended): e.g. `safegit moves <path>`
   answering the path's history across records — the chain of old/new
   answers with the commit and record id at each hop, subtree
   attributions marked, retracted claims folded, and any malformed
   record lines in the walked range surfaced loudly. Flags to consider:
   a direction selector (where-from vs where-to), a range selector
   consistent with the scrub family, `--json` payload with a declared
   schema.
   - Pros: wires the whole projection; records become useful; malformed
     surfacing gets its operator-visible home.
   - Cons: new command surface; query semantics need one design round
     (output shape, multi-hop rendering, merge arbitration display).
2. **A minimal malformed-records reporter only** (a scan mode or doctor
   check): cheap, surfaces the safety-relevant half, leaves the forward
   projection unconsumed — half an answer.
3. **Delete the projection** and rebuild when a consumer appears: honest
   hygiene, but discards correct, property-tested machinery whose
   subtleties (merge arbitration, longest-prefix, retraction folding)
   would have to be re-derived.

## Affected

- `internal/trailer` (projection API — already built)
- a new command registration in `main.go` + handler, payload schema,
  classification (read_only), pinned registries
- docs: commands guide section, the declared-moves narrative

## Effort

Medium — one design round for the query/output shape, then a
straightforward implementation over the existing tested API.
