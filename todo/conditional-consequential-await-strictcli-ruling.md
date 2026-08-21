# Align hand-rolled conditional consent with strictcli's coming ruling

## Context

strictcli's `consequential` is a static registration property, so safegit
hand-rolls app-level confirmation seams wherever consequential-ness depends
on a condition:

- `doctor --action uninstall` -- the condition is a typed choice-flag
  member; safegit prompts itself and honors `--approve-consequential`
  (doctor.go, the uninstall confirmation).
- `backup backup` to a public or unclassifiable remote -- the condition is
  discovered at runtime; safegit prompts itself and only the dedicated
  `--allow-public-remote` flag answers it (the blanket
  `--approve-consequential` deliberately does not, because the caller may
  not have known the condition).
- Planned (decided in the 2026-08 design round): `push --force-with-lease`
  becomes conditionally consequential in the doctor-uninstall shape -- the
  condition is a flag the caller typed, `--approve-consequential` answers
  it, decline exits nonzero, `--json` refuses to answer.

A todo has been filed in strictcli asking for a ruling: either make the
conditional form official (registration-declared condition, framework-owned
prompt/consent/decline/machine-mode semantics, plus an explicit statement
about runtime-discovered conditions) or ban it (consumers must split
destructive members into separate statically-consequential commands/flags).

## Problem

Until the ruling ships, safegit carries three slightly different hand-rolled
seams that the framework cannot see or validate. They work, but they are
exactly the fragmentation the framework's confirm protocol exists to
prevent, and each new conditional case (push force is the third) re-derives
the pattern.

## Work, once the ruling ships

- If strictcli makes the conditional form official: migrate the typed-flag
  cases (`doctor --action uninstall`, `push --force-with-lease`) onto the
  framework mechanism and delete the hand-rolled prompts; migrate the
  runtime-discovered case (`backup backup` public-remote) onto whatever the
  ruling provides for runtime conditions, or keep it app-level if the
  ruling explicitly blesses that shape -- preserving the property that only
  `--allow-public-remote` answers it.
- If strictcli bans the conditional form: split the surfaces accordingly
  (the uninstall member becomes its own statically consequential command or
  spelling; force-push likewise), and restructure or re-justify the
  runtime-discovered backup case per the ruling's statement on that class.
- Either way: update the conventions text in CLAUDE.md/docs that currently
  documents the hand-rolled seams as the standing pattern, and re-pin the
  behavior in the consent tests.

## Affected areas

- doctor.go (uninstall confirmation), backup.go (public-remote
  confirmation), push.go (the force consent added by the design round)
- main.go registrations for the three commands
- docs/CLAUDE templates describing the consent conventions
- internal/test consent/classification tests

## Effort estimate

Small-medium, dominated by which ruling arrives: migration onto an official
mechanism is mostly deletion; a ban means command-surface changes plus doc
and test updates. Blocked until the strictcli ruling ships in a release.
