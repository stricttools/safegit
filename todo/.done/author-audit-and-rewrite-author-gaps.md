# Author audit command + rewrite-author gaps

## Context

`safegit rewrite-author` exists for fixing wrong commit authorship (custom plumbing rewriter shared with scrub, dry-run support, 12-category verification). But two gaps surfaced while investigating a suspected wrong-identity incident:

## Problem 1: No author audit command

There is no read-only command to discover authorship problems in the first place. A user asking "which identities have commits in this repo?" must use raw `git shortlog -sne --all` (which only shows authors, not committers). For checking many repos at once — e.g., all projects under a parent directory, to spot accidental commits under the wrong identity across a workspace — there is nothing; a shell loop is required.

Discovery and remediation are naturally paired: an audit command's output (name/email pairs) is exactly the input `rewrite-author` needs.

## Problem 2: Force-push instruction conflicts with release-managed repositories

After a rewrite, safegit instructs: `safegit push --both-branches-and-tags --force-with-lease`. But repositories managed by a release-orchestration tool have a hard rule: never push manually — all pushes go through the release pipeline. The release tool already wraps scrub (post-rewrite metadata cleanup + coordinated force-push), but `rewrite-author` has no equivalent integration. A user who follows safegit's printed instruction on such a repo violates that repo's push policy, and the pre-push hook may block them — stuck between two tools' contradictory instructions.

## Possible solutions

### Audit command

1. New `safegit authors` command: distinct author AND committer identities with commit counts, table or `--json` output.
2. `--recursive <dir>` (or `--scan <dir>`): walk directories, detect `.git`, report per-repo author lists. Highlight repos with multiple identities.
3. `--expect name=X,email=Y`: only show repos/commits that deviate from the expected identity — a one-shot "what needs fixing" report.
4. Cross-link: audit output prints ready-to-run `safegit rewrite-author --old-name ... --new-name ...` suggestions.

### Push-policy conflict

1. Detect a release-tool marker directory (or add `--no-push-hint`) and replace the force-push instruction with "complete the rewrite via your release tooling."
2. Document the `--json` output (old→new SHA map, tag rewrites) as a stable integration contract so release tools can wrap rewrite-author the same way scrub is wrapped.
3. Document the interaction: when a repo is release-managed, the wrapping tool is the entry point, not raw safegit.

## Affected files

- New command file (e.g., `authors.go`), CLI registration
- `rewrite_author.go` (final instruction printing)
- Docs

## Effort

Medium overall. Single-repo `authors` is small; recursive scanner and expected-identity diffing are moderate. Push-hint suppression is small; JSON contract documentation is small-medium.
