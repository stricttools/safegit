# Contributing to safegit

## Prerequisites

- Go 1.26.3+ (the go.mod toolchain directive selects Go 1.26.6)
- git (Git for Windows for native Windows development)

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
internal/hooks' wall-clock hook-timeout tests skip under it, and
internal/git's index-reconcile property test shortens from 200 generated cases
to 40. `scripts/test-baseline` passes it
deliberately, to keep its artifact deterministic.

## Commit

Use `safegit commit` instead of `git commit` if safegit is installed.

## Release

Releases are managed via [rlsbl](https://github.com/stricttools/rlsbl). The bump
type is not a command-line argument: `rlsbl release init` scaffolds
`.rlsbl/releases/unreleased.toml`, you set the bump type and the mandatory
description there and commit that file, then run:

```sh
rlsbl release run --watch --approve-consequential
```

## Native Windows validation

Use PowerShell or cmd with native Windows Go and Git for Windows, not WSL.
Build with `go build -o safegit.exe .`. The same ordinary-suite command above
is the Windows CI gate too; it is not replaced by a short or skipped suite.
For local submodule fixtures, allow file transport only in the test process:

```powershell
$env:GIT_CONFIG_COUNT = '1'
$env:GIT_CONFIG_KEY_0 = 'protocol.file.allow'
$env:GIT_CONFIG_VALUE_0 = 'always'
go test ./... -count=1 -timeout=60m
```

Do not set a global `protocol.file.allow` value. Go's Windows race detector
also needs a compatible C compiler; ordinary tests do not require one.
See [the current validation record](.strictmetadata/docs/windows.md) before
interpreting a successful build as complete Windows support.

For more easily located failures, the same ordinary integration test names can
be divided into four disjoint groups (run each command in its own PowerShell,
or sequentially). No `-short` or platform exclusion is used:

```powershell
./scripts/test-windows-integration.ps1 -Shard 0
./scripts/test-windows-integration.ps1 -Shard 1
./scripts/test-windows-integration.ps1 -Shard 2
./scripts/test-windows-integration.ps1 -Shard 3
```

The scripts discover current test names, stream JSON events, retain Go's exit
status, and restore their process-scoped Git configuration. Shard 0 records the
discovered manifest. `scripts/check-windows-validation.ps1` verifies all four
records, the recorded non-integration package run, and normal/refusal evidence
for every leaf command against the current inventory. It rejects incomplete
runs, failures, duplicate/missing tests and unexpected skips. The suite's seven
existing opt-in stress scenarios and two exact case-folding filesystem skips
are reported separately (see windows.md), never counted as passing functional
coverage. Any other skipped ordinary test fails the checker.

## Fork prereleases (ci010/safegit)

This fork does not run upstream rlsbl/GoReleaser publishing. `CI` has read-only
repository permissions and builds five archives (Linux and macOS amd64/arm64,
plus native Windows amd64). macOS ordinary regression uses the standard Intel
runner pool; CGO-disabled macOS archives are cross-compiled on Linux to avoid
unnecessary hosted macOS runner capacity dependencies. Both macOS and Windows ordinary integration tests run in four
exhaustive disjoint shards; non-integration tests run separately, without
`-short`. Linux retains the race detector and macOS retains ordinary regression.

For `v0.33.0-windows.1`, wait for every job in CI on the exact commit to succeed,
download its `safegit-*` build artifacts, calculate `checksums.txt` with SHA-256,
and create an explicitly marked prerelease in **ci010/safegit**. Never create a
release from partial/failed CI, overwrite upstream releases, or reuse a tag for
changed code. All archives contain only the native executable and MIT LICENSE.
