# Effects-regime adoption residue

Filed at the strictcli effects-regime migration visit (go-strictcli 0.28.2). The migration
landed: the four reserved flags are framework-owned, all 31 commands are classified, and the
mutating git/filesystem seams of `push`, `pull`, the guarded passthroughs, `config set`,
`hook install` and the submodule auto-bump self-spawn go through `ctx.Effects()`. Four things
were found and deliberately NOT done at that visit.

## 1. Two confirmation layers now sit in series

`confirmDeliberate` (main.go) is safegit's own gate on irreversible operations: scrub
file/match/run, author rewrite, `doctor --uninstall`, and `backup backup` to a public or
unclassifiable remote. The framework's confirm protocol now runs BEFORE dispatch for every
`mutating` command, which changes what that layer sees:

- **Automated callers never reach it.** They must pass `--yes` to get past the framework, and
  `confirmDeliberate` short-circuits on `flags.yes`. So the operation-specific question
  ("this remote may be public") is no longer asked of any script or agent.
- **Interactive callers are asked twice.** A human at a terminal answers the framework's
  `about to run mutating command 'backup.backup'. Proceed? [y/N]` and then safegit's own
  `... is public ... [y/N]`.

Options: (a) collapse safegit's layer into the framework's and accept that the operation
-specific reason is lost from the prompt; (b) keep the layer but decouple its consent from
`--yes` by giving each condition its own flag (`--allow-public-remote` beside the existing
`--overwrite-remote-backup`), so `--yes` answers "run this command" and the specific flag
answers "yes, to THAT target"; (c) leave as is and document the narrowing.

(b) is the one that preserves the ratified property that a machine-readable, non-interactive
run cannot publish a branch to a public remote without saying so deliberately.

Affected: main.go (`confirmDeliberate`), backup.go (`confirmExposure`), scrub.go,
rewrite_author.go, doctor.go, internal/test/confirm_deliberate_test.go,
internal/test/backup_test.go.

## 2. The commit pipeline is not on the effects handle

`internal/commit` carries its own `DryRun` field: it builds the tree and the commit object,
then returns without the compare-and-swap ref update. That seam is correct and covered by
tests, but it is not the framework's. The consequence is that `recordCommitRefUpdate`
(commit.go) mints the ref move into the would-do log **in dry mode only** -- in a real run the
pipeline performs it -- which is the one place in the repo where a handler's effect record and
its execution do not come from the same call.

The clean shape is for the pipeline to return the planned ref update and let the caller mint
it on the handle in both modes. The obstacle is the CAS retry loop: the update-ref sits inside
it, and a caller-side mint would have to own the retry too.

Affected: internal/commit/commit.go, internal/commit/amend.go, commit.go.

## 3. Two mutations cannot be expressed in the closed method set

The effects handle has exactly eight methods, and two of safegit's mutations do not fit any:

- **Oplog appends.** `oplog.Append` is an atomic `O_APPEND` write of one JSONL line, which is
  what makes it safe under concurrent safegit invocations. `write` is whole-content: minting
  the append as a `write` would mean read-modify-write and would destroy that guarantee.
  Every oplog append is therefore outside the handle and simply skipped in dry mode.
- **Pre-pre-push hook execution.** `hooks.RunAll` feeds each hook script stdin in git's
  pre-push format. `run` has no stdin parameter, so the hook run cannot be minted. Under
  `--dry-run` the hooks are not run at all and `push` says so.

Both are recorded upstream as a framework gap; nothing is pending here unless the method set
grows.

Affected: push.go, coord_cmd.go, backup.go, internal/oplog, internal/hooks.

## 4. The rewrite commands render an empty would-do body

`scrub file`/`match`/`run`, `author rewrite`, `doctor`, `undo`, `unlock` and
`backup backup` each return from their own dry-run branch before any mutation, so the
framework's would-do log renders its header with nothing under it. The preview those commands
print is safegit's own text (and it is the better preview -- it names commit counts and
affected SHAs). But a reader who has learned that the would-do log lists what would change
will read the empty body as "this would change nothing", which is exactly the reading §3.5 of
the effects contract exists to prevent.

Either mint the rewrite's ref-update pass and post-rewrite cleanup (`reflog expire`,
`prune`) on the handle so the log carries them, or have those commands mint a single summary
effect describing the rewrite.

Affected: scrub.go, scrub_match.go, scrub_run.go, rewrite_author.go, rewrite_result.go,
cleanup.go, doctor.go, undo.go, unlock.go, backup.go.

## Effort

1: half a day including the flag design decision. 2: one day (the retry loop is the work).
3: nothing until the framework moves. 4: half a day.
