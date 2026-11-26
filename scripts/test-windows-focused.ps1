param([string]$OutputPath = '.strictmetadata/validation/windows-focused.jsonl')
$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT') { throw 'Native Windows only' }
$root = Split-Path $PSScriptRoot -Parent
Push-Location $root
$keys = @('GIT_CONFIG_COUNT', 'GIT_CONFIG_KEY_0', 'GIT_CONFIG_VALUE_0')
$saved = @{}
foreach ($key in $keys) { $saved[$key] = [Environment]::GetEnvironmentVariable($key, 'Process') }
try {
    $env:GIT_CONFIG_COUNT = '1'
    $env:GIT_CONFIG_KEY_0 = 'protocol.file.allow'
    $env:GIT_CONFIG_VALUE_0 = 'always'
    $tests = @(
        'AuthorList', 'AuthorCheckPass', 'AuthorCheckFail', 'AmendRootCommit',
        'BackupCreatesFirstSlotAndLists', 'BackupRestoreRoundTrip', 'BackupRefusesDivergedSlot',
        'CommitSamePathWholeAndInHunksIsRefused', 'CleanCherryPickIsPipelineAuthored', 'CherryPickRefusesRawGitShapes',
        'MergeCanBeConcludedThroughSafegit', 'SwitchMovesToAnExistingBranch', 'SwitchCreatesABranchWithDashC', 'SwitchRefusedDirty',
        'ResetPassesAnAllowedMode', 'ResetRefusesThePathspecFormAndInteractiveSelection',
        'BisectPassesAClassifiedSubcommand', 'BisectRefusesASubcommandOutsideTheClassifiedVocabulary',
        'RebaseStillRunsWithNothingInFlight', 'RebaseRefusesTheApplyBackendAndItsOptions',
        'ScanBasic', 'ScanNoMatches', 'ScanJSON', 'HookInstallArbitraryBasenameIsDiscoverable',
        'HookInstallRefusesExistingDestination', 'HookRemoveDeletesInstalledHooks', 'HookRemoveUnknownNameFails',
        'HookMigrateMovesBothSafegitOwnedNames', 'HookMigrateWithNothingToMoveSucceeds',
        'UnlockStaleLock', 'UnlockLiveProcessRefused', 'ConfigOverride', 'ConfigSetRefusesRetiredLogKey',
        'Undo', 'UndoRefusesAGitCommitOnTop', 'ScrubFileEntireHistoryRewritesEveryCommit', 'ScrubFileWithoutModeIsRefused',
        'ScrubMatchZeroCandidatesSaysSoAndSucceeds', 'ScrubRunMultiOperationRecipePassesTierA',
        'PushOnlyHead', 'PushMutexEnforcement', 'PullFastForwardMovesTheRefAndSyncs', 'PullFfOnlyRefusesADivergedBranch'
    )
    $pattern = '^Test(' + ($tests -join '|') + ')$'
    $lines = @(go test ./internal/test -run $pattern -count=1 -timeout=5m -json)
    $code = $LASTEXITCODE
    $destination = Join-Path $root $OutputPath
    [IO.Directory]::CreateDirectory((Split-Path $destination -Parent)) | Out-Null
    [IO.File]::WriteAllLines($destination, [string[]]$lines, (New-Object Text.UTF8Encoding $false))
    if ($code -ne 0) { throw "Focused native suite failed; see $destination" }
    Write-Output ('Focused native command suite passed; results: ' + $destination)
} finally {
    foreach ($key in $keys) { [Environment]::SetEnvironmentVariable($key, $saved[$key], 'Process') }
    Pop-Location
}
