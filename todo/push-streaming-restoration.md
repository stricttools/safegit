# Restore live push output once the framework ships a tee mode (and everything adjacent)

Filed by user ruling (2026-08-22, redesign-campaign review): buffering
accepted for the release; "todo everything else about this for later,
including to check if the tee has shipped, and all the alternative
options with pros and cons."

## Context

The 2026-08 campaign switched `push` (and `backup backup`, which shares
the helper) from streamed to captured subprocess output, because retry
and lease classification need git's stderr and strictcli's `Run`
streams OR captures, never both. The switch is what made the retry loop
work at all (it had been dead since the effects-handle adoption — the
classifier matched against a framework error string that never contains
git output). Consequences shipped: push output appears only after the
push exits; under `--json`, git's stdout is re-routed to stderr so the
envelope stays the sole stdout document.

## The work, when picked up

1. CHECK whether strictcli has shipped a tee mode (the request is in
   strictcli's todo `effects-run-gaps-stdin-settledness-tee-observe-dryrun.md`,
   item 3). If not shipped, this todo stays parked — do not build a
   safegit-side workaround.
2. On adoption: restore streaming at the one call site (`execGitPush`),
   keep the captured copy feeding `isTransportError`/`leaseRejected`,
   and in the same pass switch lease detection from the `"(stale
   info)"` stderr substring to `git push --porcelain`'s structured
   per-ref result (stdout), which is robust against git rewording.
   Verify the `--json` stdout-protection stance still holds under tee.
3. Decide then whether the interim heartbeat idea (a safegit-printed
   "pushing to <remote>..." line) is still wanted; under restored
   streaming it is probably moot.

## The alternatives that were considered (recorded per the ruling)

- **Capture-and-re-emit (SHIPPED).** Pros: direct stderr evidence for
  classification; no framework change; stays inside the effects regime;
  fixed the dead retry loop; closed the stdout-envelope corruption
  hole. Cons: buffered output until exit (slow pushes look hung);
  backup inherits.
- **State-probing classification (keep streaming).** After a streamed
  failure, re-run ls-remote: probe fails = transport (retry); pinned
  ref moved = lease rejection; else terminal. Pros: live output, no
  framework change. Cons: inference not evidence; a healed transient
  classifies terminal and silently re-loses retry in exactly the flaky
  case; cannot quote git's reason; a second network op with its own
  failures; hook-vs-lease ambiguity under concurrency.
- **Off-handle tee (io.MultiWriter around a direct exec).** Pros: best
  UX and best evidence, trivial mechanically. Cons: the first escape
  hatch in the effects regime — dry-run/consent stop seeing the push;
  the exact hand-rolled-mode-branch pattern Phase 3 deleted; precedent
  cost.
- **Framework tee first, then adopt.** Pros: the correct end state;
  every consumer benefits. Cons: surgery in the shared effects core
  with real design questions (tee which streams; retention memory for
  huge outputs; interleaving), a design-review-release cycle, and a
  mid-campaign cross-project detour. Chosen as the END state via the
  upstream todo, not the inline path.
- **Shell/pty/GIT_TRACE tricks.** Rejected: boundary breach, fragility,
  undocumented formats.

## Effort

Small once the framework half exists; the porcelain switch is the only
design-bearing piece.
