# VS Code extension: a visual front end for safegit

## Context

safegit is a deliberate subset of git for repositories where several agent sessions work at once: commits go through a two-phase pipeline with a per-invocation temporary index and a compare-and-swap ref update, so concurrent sessions never race on `.git/index`; every mutation is recorded in the append-only operation log; tree-mutating commands run behind the worktree operation lock and the uncommitted-work check; and whole classes of git operations (file checkout over the working tree, stash, sequenced cherry-picks and reverts, octopus merges) are refused or inexpressible.

The humans supervising those sessions mostly look at the repository through an editor. VS Code's built-in Git integration is built on the full git surface and on the shared index, which is the opposite of safegit's design. A visual front end for safegit inside VS Code gives that human a source-control view that is safe to use in the same worktree the agents are committing in.

## Problem

- The built-in Git view stages by writing the shared `.git/index`, the file safegit's pipeline is designed to keep out of. A human clicking "Stage" while an agent commits reproduces the race safegit exists to prevent.
- The built-in view offers actions safegit deliberately omits: discarding working-tree changes (restoring files over uncommitted edits with no record), stash, amend through the index, force push, and sync. One click performs an operation safegit would refuse.
- safegit's own state is invisible in the editor: the operation log, which operations `undo` can reverse, parked merges, cherry-picks, and reverts waiting for a conclusion, pre-pre-push hooks and their last results, stale locks, and `doctor` findings. All of it is reachable only from a terminal.
- Concluding a parked merge needs one `--resolve 'path=ours|theirs|worktree|delete'` per conflicted path; typing those by hand is tedious and error-prone, and a GUI is the natural way to pick a side per file.

## Proposed extension

The extension is written in TypeScript. Every write goes through the `safegit` binary; reads that safegit does not provide go through git in read-only form with `--no-optional-locks` (the same prefix safegit itself uses at its git-execution boundary, so a read never takes the index lock). Framework flags go before the command name (`safegit --json merge feature`), because after git-vocabulary commands argv belongs to the command's own parser.

### Features

1. **A source-control provider.** `vscode.scm.createSourceControl` registers a safegit provider with two resource groups: "Changes" (working tree against HEAD, from `git --no-optional-locks status --porcelain=v2 -z`) and "Selected for commit". Selecting a file for commit is extension state only, never an index write. The commit input box and its commit button run `safegit --json commit -m <message> -- <selected paths>`. A `QuickDiffProvider` supplies HEAD content (read with `git --no-optional-locks show HEAD:<path>`) for gutter diffs.
2. **Move.** Renaming a tracked file through a command runs `safegit mv`, so the move and its record are committed together.
3. **Operation log view.** A `TreeDataProvider` lists the operation log newest first: operation, branch, the tip it moved from and to, outcome, and which entries `undo` can reverse. An "Undo" action runs `safegit --dry-run undo` first, shows what would move, and then runs `safegit undo` on confirmation. Fast-forwards and parked or failed entries are shown as not undoable, with the reason.
4. **Conclusion view for parked operations.** When a merge, cherry-pick, or revert is parked, a view lists each conflicted path with a picker for `ours`, `theirs`, `worktree`, or `delete`, and a diff of the stages. For reverts the picker states which side keeps the revert, since the stage keywords are inverted there. "Conclude" runs the matching `merge-continue`, `cherry-pick-continue`, or `revert-continue` with every `--resolve`, and shows safegit's refusals verbatim: a declaration that does not match the conflicted set (exit 17), content that still holds a conflict block (exit 18), and a working-tree write that would destroy an unmatched hand edit (exit 27). Queued sequences and octopus merges, which safegit refuses, are shown as refused with safegit's own message and nothing else.
5. **Branch operations through safegit's subset.** Switch (an existing branch or a new one), merge of one branch, and pull, each running the safegit command. `pull` requires `--merge-strategy` with no default, so the extension asks for it every time rather than storing an answer.
6. **Hooks view.** `safegit hook list` in a tree (local, tracked, and legacy origins, with non-executable entries flagged), and "Run hooks" through `safegit --json hook run`, rendering the payload's per-hook exit code, timeout, duration, and leftover processes.
7. **Push through safegit only.** A push action runs `safegit --json push` and renders the hook results from the payload; a push a hook stopped is shown with the hook that stopped it.
8. **Health.** A `doctor` view showing its checks, with the repairs doctor itself offers.
9. **Status bar.** Branch, a parked-operation indicator, and a held-lock indicator (with `safegit unlock` offered only for a lock whose holder is dead, which safegit enforces anyway).

### What the extension never offers

- No staging to the shared index, no discard of working-tree changes, no stash, no file checkout, and no raw git command that writes. There is no "run git" escape action.
- No force push, and no `--force-with-lease` on `push`.
- None of the consequential commands (`scrub file`, `scrub match`, `scrub run`, `author rewrite`) or `doctor --action uninstall`; the extension never passes `--approve-consequential`. `backup backup` to a public remote needs `--allow-public-remote`, which the extension does not pass either. These stay terminal operations.
- No `--discard-unmatched-worktree` on conclusions. Exit 27 is shown with safegit's message; electing the destruction stays a deliberate command-line act.
- No rebase UI in the first version: `safegit rebase` is the declared exception where git replays and authors, and an interactive rebase holds the worktree operation lock for its whole editor session, which a GUI flow would hide.

### Coexisting with the built-in Git extension

The built-in Git extension stays active by default and keeps offering its stage and discard actions on the same repository.

- **Refuse to activate the commit UI while `git.enabled` is on for the workspace**, with an error naming the setting and a command that sets it to `false` at workspace scope. Pros: the unsafe path is closed rather than warned about. Cons: the user loses the built-in blame, timeline, and history features, which read git and are harmless.
- **Coexist and warn.** Pros: nothing is taken away. Cons: the stage and discard buttons stay one click away, and a warning is ignored.
- **Coexist, and disable only the writing parts** through the built-in extension's settings where settings exist. Cons: there is no setting for every writing action, so the result is partial and depends on another extension's settings surface.

Recommendation: the first option, with the read-only features the built-in extension provided (history, blame) reintroduced where the extension needs them.

## Prerequisites in safegit

- **A read-only operation-log verb with a JSON payload.** The oplog is append-only JSON lines under `.git/safegit/`, and there is no command that reads it. Reading the file directly couples the extension to its format and to the rules about unparseable lines (undo and doctor refuse on a nonzero skipped-line count). A read-only command that returns the entries, and whether each is undoable and why not, keeps one authority; `undo --dry-run` alone answers only for the next entry.
- **A status verb, or an explicit decision not to have one.** The extension can read status through git with `--no-optional-locks`; a safegit status payload would additionally carry safegit's own state (parked operation, held locks, the uncommitted-work verdict the guarded commands use). Deciding which is the design question.
- **Machine-mode coverage the extension relies on.** `commit`'s refusals exit before dispatch with no envelope on stdout, while other refusals carry an envelope with `payload: null`. The extension has to handle both shapes (and read stderr for the reason); giving every refusal the same shape would simplify every consumer, not only this one.

## Affected files and new components

- New: the extension package (TypeScript sources, `package.json` manifest, bundler config, tests driving a real `safegit` binary against fixture repositories created per test).
- safegit: the operation-log read verb (new command beside `undo.go`, reading through `internal/oplog`), a possible status verb, and any machine-mode shape changes, each with its own tests and docs (`stricttools/docs/commands-guide.md`, `stricttools/docs/integration-guide.md`).
- Release configuration (`.rlsbl/config.json`, CI workflow) if the extension ships from this repository.

## Where the extension lives

- **A subdirectory of this repository.** Pros: payload schema changes and the parser that consumes them change in one commit; tests can build the binary from the same tree. Cons: a Node toolchain enters a Go repository.
- **A separate repository.** Pros: its own release cadence. Cons: the extension must pin against `safegit --dump-schema` output to notice drift.

## Distribution

- Publish to both the Visual Studio Marketplace (`vsce publish`) and Open VSX (`ovsx publish`), the open registry VSCodium, Cursor, and other VS Code derivatives install from.
- The extension requires the `safegit` binary on `PATH` (or a configured path) and refuses to activate its features without it; it does not bundle the binary. `extensionKind: ["workspace"]` so it runs beside the repository in remote workspaces. Linux and macOS only, matching safegit (WSL works through the remote extension host).
- safegit releases through rlsbl, and rlsbl has no VS Code extension publishing target, so either rlsbl gains one or the extension is published by its own workflow.
- The extension's name, publisher ID, and package name are to be chosen.

## Effort estimate

- Source-control provider with commit, quick diff, and move: about 3 days.
- Operation log view and undo, once the read verb exists: about 2 days; the read verb itself, about 1 day.
- Conclusion view for parked merges, cherry-picks, and reverts: about 3 days, most of it tests over real conflicted fixtures.
- Branch operations, hooks, push, doctor, and status bar: about 3 days.
- Publishing setup for both registries and CI: about 1 day.
