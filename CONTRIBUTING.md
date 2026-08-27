# Contributing to safegit

## Prerequisites

- Go 1.24+
- git

## Build

```sh
go build -o safegit .
```

## Test

The whole ordinary suite, unit and integration alike, which is what you run
while working:

```sh
go test ./... -race
```

The long-running stress scenarios are not part of it. They are opt-in behind
`--stress`, a flag registered on the integration test binary (`internal/test`),
so a bare run stays fast and needs no `-short` to dodge them:

```sh
go test ./internal/test/ -race -count=5 -timeout=40m --stress
```

`testdata/stress [count]` runs the same thing and passes `--stress` for you. A
`-count=5` run takes around 25 minutes, which is what the 40-minute timeout is
sized for.

`-short` is a separate and much smaller thing, and neither run above needs it:
exactly two tests key on it — internal/hooks' wall-clock hook-timeout test,
which it skips, and internal/git's index-reconcile property test, which it
shortens from 200 generated cases to 40. `scripts/test-baseline` passes it
deliberately, to keep its artifact deterministic.

## Commit

Use `safegit commit` instead of `git commit` if safegit is installed.

## Release

Releases are managed via [rlsbl](https://github.com/smm-h/rlsbl). The bump
type is not a command-line argument: `rlsbl release init` scaffolds
`.rlsbl/releases/unreleased.toml`, you set the bump type and the mandatory
description there and commit that file, then run:

```sh
rlsbl release run --no-allow-dirty --watch --approve-consequential
```
