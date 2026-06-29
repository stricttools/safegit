# dry-run should not record scrub policies

## Problem

`safegit scrub run --dry-run` (v0.20.0, before the fix) recorded scrub policies to `.safegit/scrub-policies.jsonl` as a side effect. The policy entries contain the literal regex patterns, which may include the exact content being scrubbed. This creates a new source of the sensitive content that the scrub was meant to remove.

v0.20.1 fixed the object-writing bug but should also be verified to not record policies during dry-run.

## Reproduction

After running scrub dry-run in v0.20.0, `.safegit/scrub-policies.jsonl` contains entries with the patterns. If those patterns contain sensitive words, the policy file itself becomes a scrub target.

## Fix

Dry-run and `--diff` preview must not write to the policy file. Policies should only be recorded after a real (non-dry-run) scrub execution completes successfully.
