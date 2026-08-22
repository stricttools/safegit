# Effects-handle wiring: the commit pipeline (split from effects-handle-commit-pipeline-and-method-set.md, 2026-08-22)

Item 1 of the predecessor file, verbatim below. Delivered by the 2026-08
redesign campaign's Phase 3.3: the pipeline carries a required RefUpdate
port whose production implementation is the effects handle; the ref update
inside the CAS retry loop goes through it in both modes; the dry-mode-only
recordCommitRefUpdate mint is deleted; exactly one mint site remains.

## 1. The commit pipeline is not on the effects handle

`internal/commit/commit.go:65` still threads a `DryRun` bool and
`recordCommitRefUpdate` (`:137`) mints the ref move in dry mode only — a
hand-rolled branch of the class the regime removes. The obstacle is real: the
CAS retry loop re-reads the ref between attempts, which the executor model has
to express as declared result-capture observes rather than ad-hoc reads.
