# scrub match: preview mode without requiring --mangle/--replace

## Problem

`safegit scrub match` requires `--mangle` or `--replace` even with `--dry-run`. This means you must commit to a replacement strategy before seeing what files and lines would be affected. During a privacy scrub of claudewheel, we had to guess scopes and iterate with `--dry-run --mangle` to see the impact.

## Suggested fix

Add a `--preview` mode (or allow `--dry-run` without `--mangle`/`--replace`) that shows matched files, line numbers, and surrounding context without requiring a replacement strategy. Output format: file path, line number, matched text with context. This lets the user review the blast radius before deciding whether to mangle, replace with a specific string, or narrow the scope.

## Example

```
safegit scrub match --pattern 'smm-h' --entire-history --preview --scope '*.py'
# Output:
# claude_launcher/defaults.py:42  "github": {"values": ["smm-h", "mhxv"]},
# claudewheel/config.py:41       "github": {"smm-h", "mhxv"},
# 2 files, 2 matches across 5 commits
```
