# Git-config audit and a declared config-pin table at the execution boundary

## Context

The pre-release design review refused the `--rerere-autoupdate` flag on
merge, cherry-pick and revert (rerere's cache auto-staging
previously-seen conflict resolutions is remembered resolution by the
back door). The review then found that the git config key
`rerere.autoUpdate=true` produces the identical auto-staging with no
flag typed — and that this is a class, not an instance:
`merge.autostash` and `rebase.autoStash` are config twins of the
already-refused `--autostash` flag. The ruling for that round was
deliberately narrow (refuse the flag, honor config, document the open
route in the divergences catalog); this todo is the deferred full
treatment.

## Problem

safegit's behavioral guarantees are argv-scoped: allowlists, refusals
and guards all police what the operator types. Git config is a parallel
input channel that can alter the semantics of the git invocations
safegit makes, and it has never been audited. Known classes, with
examples (not exhaustive — producing the exhaustive list is this todo's
job):

- **Config twins of refused or managed flags**: `rerere.autoUpdate`,
  `rerere.enabled`, `merge.autostash`, `rebase.autoStash`, `merge.ff`,
  `pull.ff`, `pull.rebase` (the pull command's required
  `--merge-strategy` exists precisely to neutralize these last — the
  one place this class is already handled deliberately),
  `commit.gpgSign` (honored even by `git commit-tree`, a known plumbing
  exception — whether the commit pipeline inherits config-driven
  signing is unverified).
- **Arbitrary-command-execution keys**: `filter.<name>.clean/smudge`,
  `merge.<driver>.driver`, `core.fsmonitor`, `core.sshCommand`,
  `credential.helper`, `core.hooksPath` (already resolved deliberately
  via `rev-parse --git-path`).
- **Output-format keys that could corrupt parsers**: `color.*=always`,
  `core.quotePath`, `diff.renames`. safegit is largely immune because
  it speaks plumbing with `-z` modes — but that immunity is discipline,
  not a checked invariant; one future porcelain call without `-z`
  inherits the whole class.
- **Content transformation**: `core.autocrlf`, `core.eol`,
  `core.symlinks`, `core.fileMode`, `core.ignorecase` (already read
  deliberately for mv), `merge.renormalize`.
- **Operational interference**: `gc.auto` / `gc.autoDetach` (git can
  spawn background maintenance mid-operation — an unaccounted
  concurrent writer beside safegit's lock model), `core.splitIndex`
  (the shared index safegit byte-copies gains a base-file indirection),
  `index.version`, `feature.manyFiles`.
- **Ref/transport rewriting**: `url.<base>.insteadOf` /
  `pushInsteadOf` silently redirect the remotes that push, backup and
  pull actually contact.

Config layering compounds the problem: system, global (two files, both
read), local, worktree (`extensions.worktreeConfig`), and the
command-line/environment layer — plus conditional includes
(`includeIf "gitdir:..."`, `"onbranch:..."`, `"hasconfig:..."`) that
change effective config per repository or even per branch, and
submodules carrying their own config plus tracked `.gitmodules`.

## Solutions

1. **Audit producing a per-key disposition catalog.** Enumerate every
   git invocation safegit makes (the gitexec boundary makes the
   inventory mechanical), cross it against the config keys that alter
   each invocation's semantics, and record a disposition per key:
   irrelevant / immune-by-plumbing / honored-deliberately /
   must-neutralize / must-refuse.
   - Pros: turns an unknown surface into a finite ruled one, exactly as
     the flag allowlists did for argv; prerequisite for any honest
     enforcement.
   - Cons: a sizable multi-agent investigation; needs revisiting as git
     grows keys.
2. **Declared config-pin table at the execution boundary**, derived
   from the audit. `internal/gitexec` already prepends
   `--no-optional-locks` to every invocation; a declared table of
   `-c key=value` pins (candidates the audit must confirm:
   `rerere.autoUpdate=false`, `merge.autostash=false`,
   `rebase.autoStash=false`, `gc.auto=0`, `color.ui=false`) is the same
   shape: one authority, enforced on every invocation including future
   call sites.
   - Pros: makes whole classes structurally impossible instead of
     individually policed; a single reviewable place.
   - Cons: safegit overriding operator config is a real stance — every
     pin must be cataloged as a deliberate divergence, and a wrong pin
     overrides a legitimate preference.
3. **No global treatment; keep handling cases as they surface.**
   - Pros: zero scope.
   - Cons: the class is now known to be real; every future instance
     re-litigates the same question from scratch.

Recommended shape: solution 1 feeding solution 2 — audit first, pin
what the audit grades must-neutralize, catalog every pin.

## Affected files

- `internal/gitexec/` (the execution boundary; the pin table's natural
  home)
- `subset_allowlist.go` and the per-command tables (flag/config twin
  cross-references)
- `docs/divergences.md` (one entry per pin; the rerere entry records
  the class's first instance)
- Architecture and commands-guide sections describing the boundary

## Effort

Campaign-sized: the audit is a multi-agent investigation across every
git call site; the pin table itself is small once the audit derives it.
Post-release work — do not fold into a release round.
