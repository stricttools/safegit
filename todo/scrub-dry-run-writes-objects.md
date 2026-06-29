# scrub dry-run writes real objects to the object store

## Problem

`safegit scrub run --dry-run` (and likely `scrub match --dry-run`) creates real commit and blob objects in `.git/objects/`. The rewritten commits exist as unreachable objects — refs aren't updated, but the objects are on disk.

This contradicts the `--dry-run` flag's own description: "preview what would happen without writing any changes to disk."

## Consequences

- Repeated dry-runs accumulate garbage objects until `git gc --prune=now`.
- On large repos, each dry-run permanently grows the object store.
- When testing patterns for a sensitive scrub, the scrubbed (cleaned) blob content is written as a new object alongside the original. Both versions exist in the store until GC. This weakens the security posture of the scrub workflow — the point of dry-run is to test safely, but it leaves artifacts.

## Reproduction

```bash
safegit scrub run recipe.toml --entire-history --dry-run --yes --reason "test"
# Output shows "New HEAD: ee862f4b3a83"
git cat-file -t ee862f4b3a83
# Output: commit (the object exists)
git rev-parse HEAD
# Output: original SHA (refs unchanged, but objects are real)
```

## Fix

Dry-run should compute what would change (match counts, affected files, before/after diffs) without calling `git hash-object -w`, `git mktree`, or `git commit-tree`. The SHAs reported in dry-run output can be omitted or shown as placeholders. The `--diff` flag already provides the content preview without needing real objects.
