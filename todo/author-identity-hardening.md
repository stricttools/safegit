# author commands: three considerations from a fleet-wide identity campaign

A full retroactive identity convergence was run across ~22 repos using
`safegit author list` / `check` / `rewrite`. The commands did the job; three
gaps surfaced in the process, collected here as one todo for triage.

## 1. Stray refs make a completed rewrite look incomplete

`author rewrite` walks and updates branches, tags, and remote-tracking refs,
and reports success. `author list` walks `--all`. Anything reachable ONLY
through a ref outside the rewrite's scope — twice in this campaign it was a
stash whose parent chain reached the entire pre-rewrite history — makes the
post-rewrite audit still show the retired identity, with nothing explaining
the disagreement between the two commands' scopes.

Consider: `author list`/`check` annotating identities (or commits) that are
reachable only via refs outside the branch/tag/remote set, naming the refs
(`reachable only via refs/stash`). The operator then knows the rewrite is
complete and what keeps the old history alive, instead of diffing the two
commands' ref coverage by hand.

Related observation, possibly just a docs note: because remote-tracking refs
are rewritten locally, a later `git fetch` restores the remote's real
(pre-rewrite) history under `refs/remotes/` and the retired identities
reappear in `--all` audits until the remote is force-pushed. Expected
behavior, but surprising the first time.

## 2. Optional commit-time identity enforcement

`author check` audits history after the fact. There is no mechanism that
prevents a wrong identity from entering history at commit time — a stale
`user.name`/`user.email` in a repo's local config silently reintroduces
variants that the next campaign has to rewrite again (the campaign found live
local-config overrides carrying long-retired identities, one of which had
already produced thousands of misattributed commits).

Consider: an opt-in expected-identity declaration in safegit's config
(name + email); when declared, `safegit commit` hard-errors on a mismatch
between the resolved author/committer and the declaration, with no bypass
flag, per the agent-experience philosophy. Undeclared = current behavior.
Per-repo declarations should be able to override the global one, since
deliberate alternate identities exist (test fixtures committing as a
throwaway identity, for example).

## 3. A fleet-wide census mode

`author list` answers for one repo. Auditing a machine means walking every
repo and aggregating — done this campaign with an external script (recursive
walk, short-circuiting at each repo boundary so nested/vendored repos are not
double-visited, exclusion list, parallel per-repo probes; ~100 repos in under
a second). Consider whether that belongs in safegit itself, e.g.
`author census --root <dir>`: discover repos, run the `author list` probe in
each, and print a flat per-repo report plus a machine-wide identity
aggregate. Raw fields (no mailmap), like the existing commands, so the answer
stays truthful on machines that use a display-level mailmap.

## Effort

Item 1 is small (reachability attribution inside existing walks). Item 2 is
small-to-medium (config key, one check in the commit path, tests). Item 3 is
medium (directory walk, concurrency, output format) and is the most
optional of the three.
