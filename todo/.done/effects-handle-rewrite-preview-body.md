# Rewrite commands render an empty would-do body

Split from `todo/effects-handle-remaining-wiring.md` (2026-08-13): this was its
item 3, and it is delivered. `recordHistoryRewrite` (`scrub_preview.go`) mints
the rewrite's four mutations through the effects handle, so `scrub file`,
`scrub match`, `scrub run` and `author rewrite` render a real would-do body in
human mode and carry the same records in the envelope's preview member in
machine mode. Items 1 and 2 of the original file are still open and live in
`todo/effects-handle-commit-pipeline-and-method-set.md`.

Original text follows, unchanged.

## 3. Rewrite commands render an empty would-do body

`scrub file`/`match`/`run` and `author rewrite` return from their own dry-run
branch before any mint, so their preview is the summary text with no recorded
effects beneath it. Expressing a history rewrite as effects is a design round
(the rewrite is thousands of object writes; the honest preview is probably a
declared plan table, the pattern the release-tooling flagship established) —
not a mechanical migration.

## Effort

item 3 a design round + medium implementation.
