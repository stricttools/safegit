# Effects-handle wiring: the two remaining items

Successor to `todo/effects-handle-remaining-wiring.md` (split 2026-08-13). Its
item 3 (rewrite commands render an empty would-do body) is delivered and moved
to `todo/.done/effects-handle-rewrite-preview-body.md`. That file was itself the
successor to `todo/.done/effects-adoption-residue.md` (split 2026-08-07), whose
item 1 (two confirmation layers in series) shipped in 0.26.0: per-condition
consent flags where the framework cannot see the condition, notices where the
framework's consequential check already asked. These remain:

## 1. The commit pipeline is not on the effects handle

`internal/commit/commit.go:65` still threads a `DryRun` bool and
`recordCommitRefUpdate` (`:137`) mints the ref move in dry mode only — a
hand-rolled branch of the class the regime removes. The obstacle is real: the
CAS retry loop re-reads the ref between attempts, which the executor model has
to express as declared result-capture observes rather than ad-hoc reads.

## 2. Two mutation shapes are not expressible in the closed method set

Oplog appends (flock-guarded append, `autobump.go:168`, `coord_cmd.go:92/154`,
and siblings) and hook execution (`hooks.RunAll`, `push.go:132`) have no
corresponding handle method. The framework half (append-only writes,
subprocess stdin, streaming producers) is recorded in the framework's own
closed-method-set todo; this item is the safegit-side wiring once those
methods exist. Interim state is honest as of 0.26.0: `hook run` declares
`dry_run_supported=false` naming exactly this gap; `push` documents that hooks
do not run under `--dry-run`.

## Effort

Item 1 medium (CAS loop as declared captures); item 2 blocked on the
framework, then small.
