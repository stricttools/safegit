# scan/scrub --scope filter returns zero matches (v0.20.1 regression)

## Problem

In v0.20.1, the `--scope` glob filter on `safegit scan` and `safegit scrub run` produces zero matches for any glob pattern. Without `--scope`, the same pattern finds matches correctly. This is a regression — v0.20.0 correctly matched 36 blobs with `--scope '*CLAUDE.md'` for the same pattern and repo.

## Reproduction

```bash
# Without scope: finds 18 matches
safegit scan --pattern 'cross-project orchestrator' --entire-history --target blobs
# Summary: 18 blob matches

# With scope (any glob): finds 0
safegit scan --pattern 'cross-project orchestrator' --scope '*.md' --entire-history --target blobs
# No matches found.

# Even exact filename: finds 0
safegit scan --pattern 'cross-project orchestrator' --scope 'CLAUDE.md' --entire-history --target blobs
# No matches found.
```

The content exists (confirmed by the scopeless scan). The scope filter is rejecting all blobs regardless of their path.

## Impact

- `safegit scrub run` with a recipe containing `scope` filters will silently skip all scoped operations (they show "no matches")
- `safegit scan --scope` is unusable for targeted searches
- Blocks any scrub operation that needs to limit changes to specific files

## Likely cause

The v0.20.0 changelog mentions "consolidated the scan package from 6 functions to a unified `ScanObjects(ctx, pattern, opts)` API with `ScanOpts` struct." The scope filter may have been broken during this consolidation, or the v0.20.1 dry-run changes may have introduced a regression in how blob paths are resolved for scope matching.
