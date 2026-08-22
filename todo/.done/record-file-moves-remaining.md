# Record file moves — the remaining parts (split from record-file-moves-in-history.md, 2026-08-22)

Successor to `todo/record-file-moves-in-history.md`. The delivered
parts (detection removal, trailer storage, grammar, encoding,
corrections, declared moves on commit/amend) moved to
`todo/.done/record-file-moves-format-and-detection-removal.md`; the
superseded blob-pairing mechanism moved to
`todo/.obsolete/record-file-moves-blob-pairing.md`. The authoritative
design for what remains is the campaign plan's Phase 7 (subphases
7.3-7.5) plus the execution log's rulings; this file carries the
original prose for the parts not yet shipped. Original text verbatim:

## Decision: moves are declared, two ways (the mv half)

1. **`safegit mv <old> <new>`** -- a new verb that performs the filesystem
   move and commits it in one operation (one commit per move; safegit has no
   persistent staging area by design, so a stage-only mv has no home in the
   architecture). The move record is written as part of the commit it creates.

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

## Open items besides encoding

- Whether `safegit mv` accepts multiple old/new pairs in one invocation (one
  commit, several records) or exactly one pair per commit.

(Note appended at split time: the plan settles this — variadic pairs,
one commit — and the campaign plan's 7.4/7.5 settle the scrub and
revert consumption; the derived query index remains an explicit
campaign non-goal, built when a consumer needs it.)
