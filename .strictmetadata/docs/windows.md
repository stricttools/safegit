+++
title = "Native Windows port validation"
+++
# Native Windows port validation

**Status: the complete native Windows ordinary suite passed in disjoint groups,
with two pre-existing case-sensitive-filesystem cases explicitly inapplicable
on the current folding NTFS, and seven opt-in stress scenarios not run.** These
nine skips are not passing coverage. All 37 declared leaf commands have passing
real normal-execution and important-refusal evidence, including all 12 bisect
subcommands. `scripts/check-windows-validation.ps1` passed against the final
records: 21 non-integration packages passed (procutil has no standalone tests),
1,086 integration top-level tests discovered, 1,078 top-level passes and 1,528
passing cases including subtests. No test failure or package timeout occurred.
Help dispatch alone is never functional command coverage. Race, cross-platform
execution, CI and release limitations remain below.

## Environment and scope

Native validation on 2026-10-21 UTC used Windows amd64 build 26200,
Go 1.26.6 and Git 2.44.0.windows.1. No WSL execution is counted. Tests use
temporary repositories and local bare remotes, not the checkout's history or
user data. No global Git configuration was changed, and no push/PR was made.
`protocol.file.allow=always` was supplied only through the test process's
`GIT_CONFIG_COUNT/KEY_0/VALUE_0` environment for local submodule fixtures.

Build and `go vet ./...` pass natively. Linux and macOS amd64 cross-builds
also pass; they are build checks, not non-Windows execution claims. Exact
versions and build outcomes are in `../validation/windows-build-vet.txt`.

The ordinary suite is run without `-short` and without platform test exclusions:
all non-integration packages in one invocation, and all integration test names
in four disjoint groups. This avoids repeating the previous single-package
30-minute timeout while retaining individually locatable results. It is not a
claim that one monolithic `go test ./...` invocation completed. The suite's
pre-existing `--stress` opt-in scenarios are listed as skipped in the summary;
they are not counted as passing execution or safety certification.

## Reproduce and check evidence

From native PowerShell in the checkout:

```powershell
go build ./...
go vet ./...
$env:GIT_CONFIG_COUNT = '1'
$env:GIT_CONFIG_KEY_0 = 'protocol.file.allow'
$env:GIT_CONFIG_VALUE_0 = 'always'
$packages = @(go list ./... | Where-Object { $_ -ne 'github.com/stricttools/safegit/internal/test' })
go test $packages -count=1 -timeout=10m -json | Out-File .strictmetadata/validation/windows-nonintegration-current.jsonl -Encoding utf8
if ($LASTEXITCODE -ne 0) { throw 'Non-integration suite failed' }
./scripts/test-windows-integration.ps1 -Shard 0
./scripts/test-windows-integration.ps1 -Shard 1
./scripts/test-windows-integration.ps1 -Shard 2
./scripts/test-windows-integration.ps1 -Shard 3
./scripts/test-windows-inventory.ps1
./scripts/check-windows-validation.ps1
```

The four integration commands may also run in separate PowerShell processes;
their repositories and interception shims are independent temporary fixtures.
The checker requires exactly one completion for every currently discovered
integration test, passing package completions, no failures, no unexpected skips,
and a passing normal/refusal test for each declared leaf command. It rejects
help-only, missing or skipped functional evidence. Only the seven existing
stress opt-ins and the two exact case-folding skips below are allowed; they
remain separately reported, not counted as passing coverage. Results live in:

- `../validation/windows-nonintegration-current.jsonl`: current ordinary package run.
- `../validation/windows-integration-[0-3].jsonl`: current full sharded integration run.
- `../validation/windows-integration-test-names.txt`: discovered integration manifest.
- `../validation/windows-summary.json`: verified totals and exact opt-in skips.
- `../validation/windows-command-matrix.json`: functional normal/refusal test names.
- `../validation/windows-command-help.json`: 42 help-dispatched paths, including
  five groups and 37 leaves; this is an inventory, not a feature test result.

Earlier failures remain as historical evidence: `windows-full-attempt.txt`,
`windows-unresolved-packages.txt`, `windows-nonintegration-final.jsonl`,
`windows-integration-attempt1-*.jsonl`, and symlink/focused rerun logs.
The current records supersede only the same tests actually rerun. In particular,
the old full-attempt timeout and old failure-name list are not current results.

## Implemented native safety and functional behavior

- Append journals use real whole-file `LockFileEx` locks, including future
  growth. Concurrent append integrity tests execute on Windows.
- Ref locks retain complete temporary-sibling publication through atomic hard
  links. Reclaim is nonblocking, serialized by `LockFileEx`, checks held-file
  identity and rechecks staleness from the held descriptor. Readers share
  delete access. Descriptor identity avoids Windows path-backed `os.Stat`
  laziness after a temporary name has been removed.
- Liveness fails closed on access denied. Lock records use `GetProcessTimes`
  creation FILETIME to detect PID reuse; ownership still checks immutable
  owner records and `os.SameFile`, not a polling-only inode hint.
- Config and sequencer replacement use same-volume
  `MoveFileEx(REPLACE_EXISTING | WRITE_THROUGH)` with bounded sharing-violation
  retries, never copy/delete or pre-delete. An open-destination test proves
  failed replacement preserves complete original contents.
- Pre-pre-push hook code starts suspended, enters a non-breakaway Job Object
  with `KILL_ON_JOB_CLOSE`, then resumes. Containment failure refuses execution.
  Tests exercise success, failure, timeout, cancellation, leftovers, descendant
  termination and denied breakaway, including detached standard streams.
- Native execute permission means the `FILE_EXECUTE` ACL, even for interpreted
  scripts. A denied hook is refused, never silently ignored. Fixture tests use
  `icacls` on temporary files; POSIX chmod is not an execute-denial substitute.
- Git for Windows interpreters are located on PATH or beside Git. Native
  executables and PowerShell scripts are supported; other scripts require a
  supported shebang. PowerShell execution policy is not disabled. Unsupported
  formats fail closed.
- A native `.exe` test interception proxy restores actual CAS, lease, backup,
  retry and crash-injection tests. It invokes Git Bash only for fixture logic;
  executable lookup and parent-process crash targeting use native Windows PIDs,
  not extensionless wrappers silently bypassed by Windows or MSYS PIDs.
- Console-interruption coverage sends Ctrl-Break through a detached controller
  attached only to a fixture's private console, proving exit 130 and lock
  release without broadcasting into the user's console.
- Repository-boundary detection passes slash-form gitfile paths to Git's
  resolver. Linked root comparisons use native relative-path semantics.
  Tests cover bare, ordinary nested, submodule and linked-worktree boundaries,
  including actionable refusal fixes and mv within another repository.
- Native symlink conclusions use `CreateSymbolicLink`, not MSYS `ln` (which can
  silently copy instead). Paths and targets are quoted data in an encoded
  PowerShell command. Symlink-side conclusion and mv tests assert link type.

## Limits, not passing claims

- Two existing tests cannot construct their required distinct `a.txt` / `A.txt`
  entries on the current case-folding fixture filesystem:
  `TestMvCaseOnlyRefusesAnOccupiedDestination` and
  `TestMvRenamesOnlyTheCase/ignorecase=false`. They remain explicitly
  inapplicable, not passing. Actual native case-only moves with `ignorecase=true`,
  ordinary moves and occupied-destination refusals execute successfully.
- Seven existing opt-in stress scenarios did not run: `TestStress50`,
  `TestStress100`, `TestStressDifferentBranches`, `TestStressOpLog`,
  `TestHookTimeout`, `TestConcurrentPush`, `TestConcurrentDifferentBranchPush`.
  The ordinary hook package's native timeout tests DO execute; the separate
  integration stress timeout scenario above is not claimed as covered.

- `CGO_ENABLED=0` is the ordinary run configuration. A race probe with
  `CGO_ENABLED=1 CC=clang` failed while building `runtime/cgo`: this machine's
  discovered Visual Studio clang environment lacks usable standard C/Windows
  headers (`stdlib.h`, `windows.h`). See `windows-race-probe.txt`. No race suite
  was executed successfully; compiler presence alone is not race support.
- Long-running `--stress` scenarios, Windows ARM64, UNC/very long paths, network
  filesystems, antivirus/share-denial stress and protected-process behavior are
  not certified by this run. The configured Windows CI job has not run here.
- Symlink creation requires Windows Developer Mode or the applicable privilege.
  Execute-denied ACLs and execution policy remain enforced. Windows stops a Job
  Object forcibly; it does not emulate Unix TERM-then-KILL grace semantics.
- Windows cannot materialize all POSIX worktree names. Worktree fixtures that
  contain forbidden colon/control/backslash characters use distinct legal
  Unicode spellings, testing Unicode/quoting/whitespace, NOT the impossible
  original worktree names. Raw byte codecs and object/tree-reader tests retain
  original byte names through Git plumbing. Executable tree-mode tests set
  index modes explicitly because Windows chmod cannot create Git mode 100755.
  Passing a transformed fixture is not a promise to support an illegal Windows
  filename or NTFS alternate data stream as a normal tracked filename.
- Release binaries remain Linux/macOS-only; no Windows release artifact,
  upstream CI success, push, PR or publication is claimed.

## Functional command matrix

The JSON matrix links every one of the 37 declared leaves to a real normal
execution and an important refusal. This is representative functional coverage,
not an assertion that every optional flag and every possible failure was tested.
Bisect additionally executes all 12 declared subcommands (start, good, bad, old,
new, skip, run, replay, reset, terms, log, view); view uses a local native fixture
viewer, not an interactive GUI. The following table is generated from that JSON.

| Command | Normal execution test | Important refusal test |
|---|---|---|
| `commit` | `TestAmendRootCommit` | `TestCommitSamePathWholeAndInHunksIsRefused` |
| `mv` | `TestMvMovesRecordsAndCommitsInOneInvocation` | `TestMvRefusesADestinationThatAlreadyExists` |
| `merge-continue` | `TestMergeCanBeConcludedThroughSafegit` | `TestMergeConclusionAbortsOnACommitMsgRejection` |
| `cherry-pick-continue` | `TestCherryPickConclusionPreservesTheSourceAuthor` | `TestEmptyPickConclusionIsRefused` |
| `revert-continue` | `TestRevertConclusionAuthorsAsTheOperator` | `TestConclusionRefusesAnUnresolvedPath` |
| `switch` | `TestSwitchMovesToAnExistingBranch` | `TestSwitchRefusedDirty` |
| `merge` | `TestCleanMergeIsPipelineAuthored` | `TestMergeFFOnlyRefusesANonFastForward` |
| `rebase` | `TestRebaseStillRunsWithNothingInFlight` | `TestRebaseRefusesTheApplyBackendAndItsOptions` |
| `reset` | `TestResetPassesAnAllowedMode` | `TestResetRefusesThePathspecFormAndInteractiveSelection` |
| `bisect` | `TestBisectDeclaredSubcommandsNative` | `TestBisectRefusesASubcommandOutsideTheClassifiedVocabulary` |
| `push` | `TestPushOnlyHead` | `TestPushLeaseRefusesWhenTheRemoteMovedUnderUs` |
| `pull` | `TestPullFastForwardMovesTheRefAndSyncs` | `TestPullFfOnlyRefusesADivergedBranch` |
| `doctor` | `TestDoctorFixCleansMainRepoStaleLocks` | `TestDoctorExitsNonzeroWhileHooksAreUnmigrated` |
| `cherry-pick` | `TestCleanCherryPickIsPipelineAuthored` | `TestCherryPickRefusesRawGitShapes` |
| `revert` | `TestSingleCommitRevertIsPipelineAuthored` | `TestRevertRefusesRawGitShapes` |
| `undo` | `TestUndo` | `TestUndoRefusesAGitCommitOnTop` |
| `unlock` | `TestUnlockStaleLock` | `TestUnlockLiveProcessRefused` |
| `scan` | `TestScanBasic` | `TestReadOnlyCommandRefusals/scan-invalid-regexp` |
| `backup backup` | `TestBackupCreatesFirstSlotAndLists` | `TestBackupRefusesDivergedSlot` |
| `backup list` | `TestBackupCreatesFirstSlotAndLists` | `TestReadOnlyCommandRefusals/backup-list-missing-local-remote` |
| `backup restore` | `TestBackupRestoreRoundTrip` | `TestBackupRestoreRefusesNonFastForward` |
| `config show` | `TestConfigSetRefusesRetiredLogKey` | `TestReadOnlyCommandRefusals/config-show-outside-repository` |
| `config get` | `TestConfigOverride` | `TestReadOnlyCommandRefusals/config-get-unknown-key` |
| `config set` | `TestConfigSetDryRunWritesNothing` | `TestConfigSetRefusesRetiredLogKey` |
| `hook list` | `TestHookInstallArbitraryBasenameIsDiscoverable` | `TestReadOnlyCommandRefusals/hook-list-outside-repository` |
| `hook run` | `TestHookInstallArbitraryBasenameIsDiscoverable` | `TestHookRunPayloadRecordsAFailingHook` |
| `hook install` | `TestHookInstallArbitraryBasenameIsDiscoverable` | `TestHookInstallRefusesExistingDestination` |
| `hook remove` | `TestHookRemoveDeletesInstalledHooks` | `TestHookRemoveRefusesCommittedHook` |
| `hook migrate` | `TestHookMigrateMovesBothSafegitOwnedNames` | `TestHookMigrationRefusesToOverwriteLiveChecks` |
| `author list` | `TestAuthorList` | `TestReadOnlyCommandRefusals/author-list-outside-repository` |
| `author check` | `TestAuthorCheckPass` | `TestAuthorCheckFail` |
| `author rewrite` | `TestRewriteAuthorBasic` | `TestRewriteAuthorDirtyTreeAlwaysRejected` |
| `scrub file` | `TestScrubFileEntireHistoryRewritesEveryCommit` | `TestScrubFileWithoutModeIsRefused` |
| `scrub match` | `TestScrubMatchBlobReplace` | `TestScrubMatchRefusesATransformThatBreaksARecord` |
| `scrub run` | `TestScrubRunMultiOperationRecipePassesTierA` | `TestScrubRunJSONDoesNotConfirm` |
| `scrub squash` | `TestScrubSquashFoldsARangeAndJournalsEveryFoldedCommit` | `TestScrubSquashRefusesAMergeInsideTheRange` |
| `scrub verify` | `TestScrubVerifyPatternPassesOnACleanStore` | `TestScrubVerifyWithoutInputIsAnError` |
