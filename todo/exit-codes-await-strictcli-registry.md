# Align exit codes with strictcli's coming registry and usage-code ruling

## Context

The 2026-08 design round decided safegit consolidates its exit-code
vocabulary into a single `internal/exitcode` registry (all die()/return
sites routed through it, the docs table derived from it or pinned by a
test), adds exit 14 (binary file + hunk spec) and exit 8 (lock acquisition
timeout, fixing the rewrite commands' discarded lock errors on the way),
and hardens the three `CommitError` consumers to `errors.As`.

Two related rulings were then requested from strictcli via a filed todo:
a framework-level declared exit-code registry (`WithExitCodes`-style, with
schema exposure so generated docs render the table, and possibly
enforcement via framework-performed exits plus a bare-exit check), and a
ruling on the usage code -- framework parse errors exit 1 while safegit's
~23 in-handler argument guards exit 2, and the docs' claim that 2 is "the"
usage code is only half true. The campaign decision: leave the guards at 2
for now, document the split honestly ("2 = safegit's post-parse validation;
framework parse/usage errors are 1"), and defer the collapse-vs-keep call
to the framework ruling so the sites are not changed twice.

## Work, once the rulings ship

- Framework registry ships: migrate `internal/exitcode` onto the declared
  mechanism (the internal registry becomes the declaration), adopt schema
  exposure so the docs table generates from `--dump-schema`, and adopt the
  enforcement level if one ships (handlers return codes, direct exit sites
  migrate off `die()` where the framework performs exits).
- Usage-code ruling ships: either collapse the ~23 exit-2 guards to 1 (one
  usage code everywhere) or keep 2 if the framework adopts a distinct usage
  code itself -- whichever the ruling says, applied in one pass with the
  docs table and any exit-code-asserting tests updated together.

## Affected areas

- `internal/exitcode` (the campaign-built registry) and every routed site
- main.go registration if a `WithExitCodes` surface ships
- docs/commands-guide.md exit-code table (hand-pinned until schema-derived)
- internal/test assertions on exit codes (2-asserting tests change if the
  guards collapse)

## Effort estimate

Small once the rulings exist: the campaign's registry is designed to be the
migration source. Blocked until strictcli ships the registry and/or the
usage-code ruling.
