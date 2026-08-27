# The macOS CI lane runs the integration suite ~3x slower than Linux

## Context

The v0.29.0 release was the first CI run over the repository's full test
suite on both lanes. The Linux runner finished the ordinary integration
suite in about five minutes with the race detector on; the macOS runner
took 14m38s without it, and the release's first attempt was killed by the
then-15-minute workflow timeout twenty seconds short of finishing. The
timeout was raised to 30 minutes to absorb the gap, which is a shim, not
an answer.

## Problem

The integration suite (internal/test) builds the safegit binary once per
run but then SPAWNS it as a subprocess for nearly every assertion —
thousands of process launches per run — plus a git subprocess fan-out
under each. macOS pays a much higher per-spawn cost than Linux, so the
suite's wall-clock scales with spawn count there, not with test logic.
Every future test added makes the macOS lane slower, and the 30-minute
budget will erode the same way the 15-minute one did.

## Candidate directions

- **Measure first.** Profile one macOS CI run (per-test timings are
  already in the go test output) to confirm spawn cost dominates and to
  find the heaviest spawners. Cheap, and everything below depends on it.
- **Reduce spawns per test.** Many tests run several safegit invocations
  where one invocation plus richer assertions would do; the biggest
  offenders could batch their setup git calls (fixture builders run many
  `git` subprocesses each).
- **Share fixtures.** Fixture repos are built per test; a read-only
  fixture cache for the common shapes (born repo with one commit, parked
  conflict, unborn-with-branch) would cut both git and safegit spawns.
  Needs care: tests that mutate a fixture cannot share it.
- **Prune the macOS matrix.** Run the full suite on Linux only and a
  platform-relevant subset on macOS (the case-insensitive filesystem
  fixture is the one test that genuinely NEEDS macOS). Most correct
  regardless of effort if the suite's purpose on macOS is platform
  coverage rather than logic coverage — the logic is platform-independent
  Go, and the race detector already runs on Linux.
- **Accept and monitor.** Keep the 30m budget and revisit when it erodes.

## Affected

`.github/workflows/` (the test workflow's macOS job and its timeout),
`internal/test` (fixture builders and per-test spawn counts) if spawn
reduction is chosen.

## Effort

Measurement: small. Spawn reduction or fixture sharing: medium, spread
across many test files. Matrix pruning: small, one workflow edit plus a
stated rationale in the workflow file.
