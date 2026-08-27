# Repeated -m flags join with a single newline, not a blank line

## Problem

`safegit commit -m "subject" -m "body paragraph" -- <files>` produces a
commit whose body is merged into the subject: the two message parts are
joined with a single `\n` rather than the `\n\n` separator git uses for
repeated `-m` flags. `git log --oneline` then shows the whole paragraph as
the subject line.

Observed live: a commit made with two `-m` flags came out as one long
subject; sibling commits made with a single `-m` carrying embedded newlines
formatted correctly, which isolates the joining behavior as the cause.

## Expected

Match `git commit`'s documented behavior: multiple `-m` values are
concatenated as separate paragraphs, i.e. joined with a blank line.

## Affected

The commit-message assembly path for the `commit` command wherever repeated
`-m` values are collected. Likely a `"\n".join(...)` that should be
`"\n\n".join(...)`.

## Effort

Small: one joining fix plus a regression test asserting subject/body
separation for two `-m` flags (e.g. `git log --format=%s/%b` on the
produced commit).
