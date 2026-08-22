# Reader-writer operation lock — CONTINGENT: requires proof of real contention first

Filed by user ruling (2026-08-22, redesign-campaign review): "option 2
filed as a todo, contingent on this actually proving to be a problem,
not a general todo, proof is needed."

## Precondition — do not start this work without it

Demonstrated, measured contention on the worktree operation lock in a
real workflow: sessions observably waiting on each other's commits at a
frequency and duration that a human or agent actually notices. A
benchmark constructed to produce contention does not count; the
campaign already measured ~30ms holds and ~30 sequential commits/second
after the backoff fix, and judged real-world waiting imperceptible.
Evidence belongs in this file (append measurements) before any design
work begins.

## Context

The per-worktree operation lock (2026-08 campaign, subphase 1.5) is
taken by every state-changing operation — commit/amend/reword/undo and
every tree-mutating passthrough — so plain commits in one worktree
serialize with each other, although the race the lock closes
(sequencer state appearing between a commit's check and its ref update)
strictly needs only commit-vs-passthrough exclusion. Hold time is the
command's own execution: milliseconds for commits; the one long hold is
an open `rebase -i` editor.

## The deferred design

A shared/exclusive (reader-writer) lock: commits take it shared and
overlap each other; passthroughs take it exclusive. Rejected during the
campaign because the obvious primitive (flock) leaves no file on disk,
and the recovery story requires visible lock files (`safegit unlock`
names them; `doctor` lists and reclaims them). A qualifying design must
keep an on-disk, crash-recoverable representation of shared holders
(e.g. per-holder files in a directory, with the staleness identity the
current lock files carry) — that bookkeeping is the actual work, not
the locking.

## Effort

Medium-plus once justified: new primitive with staleness/recovery per
holder, unlock/doctor integration, contention tests.
