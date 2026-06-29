# Scrub policy file commits the patterns being scrubbed

## Problem

`.safegit/scrub-policies.jsonl` is tracked and committed in the repo. After a scrub, it contains JSONL entries with the regex patterns used to find and remove sensitive content. If the pattern contains the sensitive content itself (which is the common case — you scrub a secret by matching the secret), the policy file re-introduces the exact content you just scrubbed.

Example: you scrub an API key with `--pattern 'sk-live-abc123'`. The scrub rewrites history to remove `sk-live-abc123` from all blobs. Then safegit commits a policy entry containing `{"pattern": "sk-live-abc123", ...}` to `.safegit/scrub-policies.jsonl`. The secret is back in the repo, committed, pushed, visible to anyone who clones.

This also happened with non-secret scrubs: we scrubbed a project name from history, and the policy file contained the regex patterns which included the project name. We had to scrub the policy file itself afterward, which is circular and defeats the purpose.

## The fundamental tension

The policy file exists so `scrub verify` can re-check that scrubbed content stays gone. But storing the pattern in the repo means the scrubbed content (or something close to it) is in the repo. These two goals conflict:

1. Portable verification: policies travel with the repo so `scrub verify` works on any clone
2. Secret safety: patterns used to find secrets must not be committed to the repo

## Possible directions

- **Hash the patterns.** Store `sha256(pattern)` instead of the literal pattern. `scrub verify` scans all blobs and checks if any match produces a hash that matches a policy entry. The literal pattern never appears in the file. Downside: `scrub verify` must try every blob against every possible interpretation — this might not be feasible without the original pattern.
- **Store patterns outside the repo.** Move policies back to `.git/safegit/` (untracked) or `~/.config/safegit/policies/` (per-user). Lose portability. `scrub verify` only works on machines where the scrub was run.
- **Encrypt the policy file.** Encrypt at rest with a key that's not in the repo. `scrub verify` requires the key. Portable if the key is distributed out-of-band.
- **Separate pattern storage from verification metadata.** The policy file stores only a policy ID, reason, timestamp, and scope. The actual pattern is stored in a local-only file (`.git/safegit/patterns.jsonl`). `scrub verify` needs both files. The committed file is safe to push; the local file stays on the machine.
- **Don't store patterns at all.** `scrub verify` takes `--pattern` as a flag, like `scrub match` does. No persistent policy file. The user must remember (or script) which patterns to verify. Simplest but least convenient.

## Severity

High. The most common scrub use case (removing leaked secrets) is undermined by the current design. A user who scrubs a secret and pushes has a false sense of security — the secret is in the policy file they just pushed.
