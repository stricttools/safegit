# scrub match: comprehensive improvements

## Context

During a real scrub operation (removing project references from a 800-commit repo), several friction points emerged. Each on its own is minor; together they make complex scrubs error-prone and slow. The existing `scrub-preview-mode.md` todo covers one of these (preview without requiring replacement strategy). This todo covers everything else.

## Problems

### 1. Dry-run disagrees with execution on multi-line patterns

The dry-run scanner splits blob content by `\n` and matches line-by-line. Execution uses `ReplaceAll` on the full blob content. A pattern containing `\n` (e.g., matching a heading + the line below it) shows 0 matches in dry-run but works correctly in execution. This means the only way to verify a multi-line pattern is to run it for real, which is a history rewrite. Dry-run must match the same way execution does.

**Fix:** Dry-run should apply the same `ReplaceAll` on the full blob content and report matches from the diff between original and replaced content, not from line-by-line scanning.

### 2. No line deletion mode

`--replace ''` removes matched text but leaves the newline character, producing blank lines. Every content-removal scrub leaves cosmetic blank line debris requiring a follow-up commit. There is no way to say "remove the entire line (including newline) if this pattern matches."

**Fix:** Add `--delete-line` flag. When set, if the pattern matches anywhere on a line, the entire line (including its trailing newline) is removed from the blob. Mutually exclusive with `--replace` and `--mangle`. This also naturally solves the multi-line section removal problem (match a heading, delete that line, match content lines, delete those lines).

### 3. One pattern and one replacement per run

Each run rewrites the entire commit graph. A scrub needing 3 different pattern/replacement pairs requires 3 full history rewrites. This is O(N * commits) where N is the number of distinct operations, when it should be O(commits).

**Fix:** Support a recipe file (TOML) that specifies multiple operations applied in a single rewrite pass:

```toml
[[operations]]
pattern = '- \*\*.*ark.*cross-project orchestrator\..*'
mode = "delete-line"
scope = "*CLAUDE.md"

[[operations]]
pattern = 'remove ark meta-orchestrator prescription from'
replace = 'update'

[[operations]]
pattern = ' \(ark\)'
replace = ''
```

Invocation: `safegit scrub match --recipe scrub.toml --entire-history --reason "Remove project references"`. All operations apply to each blob/message in a single pass. One history rewrite instead of N.

### 4. No blob-only or message-only mode

`--scope` filters blob matches, but commit messages are always scanned. There is no way to scrub only commit messages without touching blobs, or only blobs without touching messages. If a pattern matches in both but you only want to change one, you are stuck.

**Fix:** Add `--target` flag with values `all` (default), `blobs`, `messages`. `--target blobs` skips message scanning. `--target messages` skips blob scanning. The `--scope` glob filter continues to apply only to blobs (messages have no file path to filter on).

### 5. No content preview (before/after diff)

Dry-run shows match counts and locations but not what the file looks like after replacement. For destructive history rewrites, seeing the before/after diff is essential for confidence. (The existing `scrub-preview-mode.md` todo covers the "preview without replacement" case; this is about showing the actual replacement result.)

**Fix:** Add `--diff` flag to dry-run. When set, output a unified diff for each affected blob showing the original content vs. replaced content. For large repos, limit to the first N affected blobs (e.g., `--diff --limit 10`).

### 6. No targeted commit list

`--from` sets a starting commit, `--entire-history` rewrites everything. There is no way to say "only rewrite these specific commits." If 3 of 800 commits are affected, all 800 are rewritten. This is wasteful and increases the blast radius of the force push.

**Fix:** Add `--commits` flag accepting a comma-separated list of commit hashes. Only those commits (and their descendants, since parent SHAs change) are rewritten. Commits before the earliest listed commit are untouched. This is an optimization, not a semantic change — the result is identical to `--entire-history` for the specified pattern, but unchanged commits keep their original SHAs.

### 7. No standalone verification command

After scrubbing, the tool re-scans automatically. But there is no way to verify later (e.g., after GC, after push, in CI) that a pattern no longer exists in history.

**Fix:** Add `safegit scrub verify --pattern '...' [--scope '...'] [--entire-history | --from ...]`. Returns exit 0 if zero matches, exit 1 if matches remain. Output shows match locations. Useful for: post-scrub confirmation, CI checks preventing re-introduction of scrubbed content, periodic audits.

### 8. No section-level operations for structured files

Removing a Markdown section (heading + all content until the next same-or-higher-level heading) requires a multi-line regex, which cannot be verified in dry-run (problem 1). This is a common enough pattern for documentation scrubs that it warrants first-class support.

**Fix:** Add `--markdown-section` flag accepting a heading string (e.g., `--markdown-section "### Consumers"`). Matches the heading line and all lines until the next heading of equal or higher level (or EOF). Combines naturally with `--delete-line` (remove the section) or `--replace` (replace it with something). Mutually exclusive with `--pattern`.

## Priority

Problems 1 (dry-run accuracy) and 2 (line deletion) are the most impactful — they affect every non-trivial scrub. Problem 3 (recipe file) saves the most time on complex scrubs. The rest are quality-of-life improvements that compound.

## Affected files

The scrub match implementation. The dry-run scanner, the blob rewriter, the CLI flag definitions, and the commit message rewriter.
