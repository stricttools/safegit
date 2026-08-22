# Effects-handle wiring: the closed-method-set gap

Successor to `todo/effects-handle-commit-pipeline-and-method-set.md` (split
2026-08-22). Its item 1 (the commit pipeline on the effects handle) is
delivered and moved to
`todo/.done/effects-handle-commit-pipeline-on-the-handle.md`. The remaining
item, verbatim:

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

Blocked on the framework, then small.
