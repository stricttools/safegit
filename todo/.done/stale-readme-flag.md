# README documents a nonexistent flag: `unlock --force`

Filed 2026-08-03.

## Problem

`docs/_README.md:90` documents `safegit unlock --force`. Per the current schema (`.strictcli/schema.json`), `unlock` has zero flags — the invocation as documented is a parse error. The generated `README.md:126` (chmod 444, selfdoc-generated) carries the stale form into the repo's public front page.

Additionally, a bare `--force` flag is banned framework-wide at registration time (qualified names like `--force-overwrite` are required), so the documented form was likely retired in that migration and the doc line missed.

## Work

1. Fix `docs/_README.md:90` — either remove the flag from the example or replace it with the current mechanism for whatever "force unlock" became (check the `unlock` handler and help text for the intended workflow).
2. Regenerate with `selfdoc gen` (bare, so it auto-commits with the exemption trailer). Never edit the generated `README.md` directly.
3. While in there: sweep `docs/*.md` fenced examples against the current schema for other stale flags/commands (a fleet-wide scan found the docs generally clean apart from this; a quick verification pass closes it).

## Affected files

`docs/_README.md`, regenerated `README.md`, possibly other `docs/*.md`.

## Effort

S.
