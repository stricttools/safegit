# Windows support via LockFileEx (deferred)

## Context

The 2026-08 redesign campaign removed Windows release targets from goreleaser
and deleted the //go:build windows source files outright: with the oplog's
4096-byte line cap removed, the Windows flock no-op left a from-source
Windows build with no oplog integrity mechanism at all, so GOOS=windows now
fails at compile time -- "unsupported" made structural instead of a README
sentence.

## The deferred work

If Windows support is ever wanted as a product decision, it is a project,
not a lock fix:

- Implement lockFile/unlockFile via LockFileEx/UnlockFileEx
  (golang.org/x/sys is already a dependency) in a restored
  internal/filelock windows file.
- Restore the other platform files (doctor network-FS check with a real
  skipped status rather than a false ok; process liveness; lock cleanup on
  termination -- SIGTERM has no Windows equivalent, needs its own design;
  process-group kill for hook timeouts).
- Add windows-latest to CI so the platform is actually tested; adapt the
  integration suite's path/permission assumptions.
- Re-add the goreleaser targets (and the .rlsbl/bases copy) only once CI is
  green on Windows.

Deferred deliberately: no observed demand, and shipping untested
platform-specific locking is a quiet promise the fleet rules disallow.
