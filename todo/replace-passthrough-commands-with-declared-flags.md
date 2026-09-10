# Replace the passthrough commands with declared flags and args

## Context

strictcli is removing passthrough commands. A passthrough command hands its raw
argv to a handler and bypasses the framework's parser, help renderer, schema
export and MCP tool export entirely. The removal ships in a future strictcli
release; after it, `app.Passthrough(...)`, `WithPassthrough(...)` and the
`PassthroughHandler` type no longer exist, and a safegit built against that
release does not compile until the commands below are re-declared.

safegit registers these commands as passthrough, all through the shared `pt`
closure in `main.go`:

- `switch`
- `merge`
- `rebase`
- `reset`
- `bisect`
- `cherry-pick`
- `revert`

## Problem

The handler does not forward argv verbatim. It re-parses the raw args with
safegit's own git-style parser (`parseGitArgs` in `sequencer_argv.go`, producing
options, revisions and the tokens after a bare `--`), measures the result
against a per-command allowlist (`argvSubset` tables in `subset_allowlist.go`
and `switch_cmd.go`, each with an allowed list and a refused list carrying a
reason), and consults a separate per-command value-arity table (`valueFlags`)
to know which options consume the next token. That is a second flag parser
living inside a consumer, kept in step by hand with the help strings, the
docs table in `docs/divergences.md`, and the refusal messages.

Because the framework never sees the options, safegit's own help for these
commands is prose only, `--dump-schema` exports nothing about them, and the MCP
tool export publishes an empty parameter schema, so an agent driving safegit
over MCP cannot call these commands at all.

## Solutions

### Option A: declare the subset as real flags and args (recommended)

Each command declares its allowed options as strictcli flags and its revisions
as positional args, and the hand parser, the allowlist tables and the arity
table are deleted. The declaration IS the allowlist: an undeclared option is
refused by the framework at parse time.

Sketch, per command, from the existing allowlists:

- `merge`: string flags for `-m`/`--message` (repeatable), `-F`/`--file`,
  `-s`/`--strategy`, `-X`/`--strategy-option` (repeatable), `--cleanup`,
  `--into-name`; bool flags for `--no-edit`, `--signoff`, `--log`, `--stat`,
  `--ff`, `--ff-only`, `--no-commit`, `--continue`, `--abort`, `--quit`; a
  variadic positional for the branches or commits.
- `cherry-pick` and `revert`: `-m`/`--mainline`, `-X`, `--strategy`,
  `--cleanup`, the sequencer control bools, and a variadic positional for the
  commits.
- `switch`: `-c`/`--create`, `-C`/`--force-create`, `--orphan`, `-t`/`--track`
  plus one positional for the branch.
- `reset`: the five mode bools and one positional for the commit. Extra
  positionals (the pathspec form) are then refused by the framework as
  unexpected arguments, which is the refusal safegit performs by hand today.
- `rebase`: `--onto`, `-i`, `--autostash`, `--rebase-merges`, `-s`, `-X`, one
  positional for the upstream.
- `bisect`: a positional `subcommand` constrained with `choices=` over the
  stepping and reporting subcommands (each choice carries its help), plus a
  variadic positional for the revisions.

Things the framework cannot express as-is, and what to do about each:

- **Refusals with a reason.** strictcli's unknown-flag error is one fixed
  sentence, while safegit's refused capabilities each carry prose. strictcli
  is considering a declaration for a flag refused by name with a reason,
  rendered in help and enforced at parse time. If that ships, use it for every
  entry in the refused lists. If it does not, the refusals fall back to the
  framework's generic unknown-flag refusal and the reasons move to
  `docs/divergences.md` only.
- **`--no-*` spellings where only the negative is allowed.** Flag names
  starting with `no-` are a registration error in strictcli, because `--no-x`
  is auto-generated for negatable bools. `--no-rerere-autoupdate` (allowed)
  with `--rerere-autoupdate` (refused), and `--no-commit` (allowed) with
  `--commit` (refused), therefore become a negatable bool whose positive
  spelling is refused, through the refused-flag declaration above or a
  `validate` callback.
- **git's loose spellings are no longer accepted.** Attached short values
  (`-Xours`, `-m2`), bundled shorts (`-en`), optional attached values
  (`--rebase-merges=rebase-cousins` alongside a bare `--rebase-merges`) and
  last-one-wins repetition are rejected by strictcli for every command, on
  purpose. An operator or agent writes `-X ours`, `-e -n`, and one spelling of
  `--rebase-merges`. This is a user-visible change and belongs in
  `docs/divergences.md`.
- **The bare `--` separator.** strictcli already treats every token after a
  bare `--` as positional data, so a variadic positional absorbs post-`--`
  operands and a command declaring no such positional refuses them.
- **Framework flags after the command name.** safegit's bespoke refusal for a
  reserved flag written after the command name exists only because the
  pre-scan stopped at a passthrough's name. With ordinary commands the
  reserved flags are recognized anywhere, so that refusal path is deleted.

Pros: one declaration replaces the allowlist, the arity table, the help
prose, the docs table and the refusal messages. Help, schema export and MCP
work for these commands. The parser and its tests are deleted. The machine
payloads already declared on `merge`, `cherry-pick` and `revert` carry over
unchanged.

Cons: the accepted spellings narrow to strictcli's strict forms. The refused
reasons need the framework declaration to stay in help text. Every command's
docs and tests are rewritten.

### Option B: keep the hand parser behind a single variadic positional

Each command becomes an ordinary command with one required variadic positional
that receives every token, and the existing `parseGitArgs`, allowlists and
arity table keep running on it.

Pros: the smallest edit; git's loose spellings keep working.

Cons: nothing improves. The second parser stays, help stays prose, the schema
and MCP export show one opaque list, the five hand copies stay in step by
hand. strictcli's own parser would also see the tokens first, so any
`--`-prefixed token not declared would be refused before the hand parser
runs, which makes this option only work if every option is passed after a bare
`--`. That is a worse interface than today.

## Affected files

- `main.go` (the registrations and the `pt` closure)
- `sequencer_argv.go` (`parseGitArgs`, `valueFlags`, `gitArgs`)
- `subset_allowlist.go` and `switch_cmd.go` (the `argvSubset` tables and the
  refusal functions)
- the per-command `run*` functions that consume `gitArgs`
- `docs/divergences.md` and the command help strings
- every test exercising these commands' argv handling

## Effort

Large. The declarations themselves are mechanical, derived from the existing
tables. The cost is deleting the parser and its tests, rewriting the
divergences documentation for the strict spellings, and deciding the refusal
spelling once the strictcli declaration is settled.
