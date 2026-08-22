# Record file moves — the blob-pairing mechanism (split from record-file-moves-in-history.md, 2026-08-22)

Superseded, verifiably not built and no longer wanted: the campaign's
Phase 7 ruling is that BLOB EQUALITY NEVER DECIDES RECORD EXISTENCE —
declared moves (`--moved 'old -> new'` on commit and amend, validated
against the parent tree and the disk, never against blob content)
replaced this both-path-pairing inference entirely. The pairing would
have re-introduced a weaker cousin of the inference the campaign
removed. Original text verbatim:

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
