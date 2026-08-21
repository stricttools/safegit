# Align dry-run network behavior with strictcli's coming ruling

## Context

safegit's docs claim a tool-wide promise that "dry runs never touch the
network" (docs/commands-guide.md, the dry-run section; echoed in the
docs/_CLAUDE.md conventions block). That promise is safegit-authored, not a
strictcli guarantee -- the framework's model actually permits observes to
execute for real in dry mode. And the promise is false today: `backup
backup --dry-run` deliberately previews from local state with no network
contact, but `backup restore --dry-run` performs an `ls-remote` before its
dry-run branch (verified: previewing restore against an unreachable remote
exits 1 with a resolution error), and `push --dry-run`'s remote-ref
resolution likely also reads the network (unverified).

A todo has been filed in strictcli asking for the framework ruling: declare
observes-may-read-network as the dry-run contract, forbid network observes
in dry mode via declaration-based enforcement (network-marked observe
allowlist entries -- the framework sees argv, not sockets, so it can never
infer this), or a per-command offline-preview declaration.

## Problem

Per-consumer promises about a framework concept fragment that concept, and
safegit's version of the promise is both invented and broken. Until the
ruling ships, the honest state is: no tool-wide network promise, and one
sibling-command inconsistency (backup vs restore).

## Work

Now (independent of the ruling): strike or reword the "dry runs never touch
the network" overclaim in the docs -- it is false under any future ruling,
and per the ruling request the stance is not safegit's to promise. Describe
`backup backup`'s local-only preview as that command's behavior, not a
dry-run guarantee.

Once the ruling ships:

- If observes-may-read-network is declared the contract: leave restore and
  push as they are; optionally keep `backup backup` local-only as a
  documented command-level choice.
- If no-network-in-dry-run (or the per-command declaration) ships: mark
  safegit's network observes (`ls-remote`, fetch) in the allowlist
  declaration, adopt the enforcement, and reorder `backup restore` (and
  `push` if confirmed) so their dry-run branches precede any network read;
  verify with a sweep test only if the framework does not already enforce
  it.

## Affected areas

- docs/commands-guide.md (dry-run section), docs/_CLAUDE.md conventions
  block
- backup.go (restore's ls-remote ordering), push.go (remote-ref resolution
  in dry mode -- verify first)
- The observe allowlist declaration, once safegit declares one
- internal/test dry-run tests for backup/push

## Effort estimate

The doc reword: small, can ride the current campaign's docs healing. The
alignment: small-medium depending on the ruling; blocked until strictcli
ships it.
