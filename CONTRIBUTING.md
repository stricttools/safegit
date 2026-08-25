# Contributing to safegit

## Prerequisites

- Go 1.24+
- git

## Build

```sh
go build -o safegit .
```

## Test

Unit and fast integration tests:

```sh
go test ./... -race -short
```

Stress tests (slow):

```sh
go test ./internal/test/ -race -count=5 -timeout=15m
```

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
