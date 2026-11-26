# Verify a complete sharded ordinary run and its functional command matrix.
# Help dispatch is deliberately not used as execution evidence.
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
Push-Location $root
try {
    $expected = @(Get-Content '.strictmetadata/validation/windows-integration-test-names.txt' | Where-Object { $_ })
    $live = @(go test ./internal/test -list '^Test' | Where-Object { $_ -match '^Test' })
    if ($LASTEXITCODE -ne 0) { throw 'Integration test discovery failed' }
    if (@(Compare-Object $expected $live).Count) { throw 'Integration manifest is stale; rerun every shard' }
    $events = @()
    for ($i = 0; $i -lt 4; $i++) {
        $file = '.strictmetadata/validation/windows-integration-' + $i + '.jsonl'
        $batch = @(Get-Content $file | ForEach-Object { $_ | ConvertFrom-Json })
        $last = $batch | Select-Object -Last 1
        if ($last.Action -ne 'pass' -or $last.Test) { throw "Incomplete or failed shard: $file" }
        if (@($batch | Where-Object Action -eq 'fail').Count) { throw "Failed test in $file" }
        $events += $batch
    }
    $completions = @{}
    $passed = @{}
    $stressSkips = @{}
    foreach ($event in $events) {
        if ($event.Test -and $event.Action -in @('pass', 'fail', 'skip')) {
            $completions[$event.Test] = 1 + $completions[$event.Test]
            if ($event.Action -eq 'pass') { $passed[$event.Test] = $true }
        }
        if ($event.Test -and $event.Action -eq 'output' -and $event.Output -like '*stress scenario: pass --stress to run it*') {
            $stressSkips[$event.Test] = $true
        }
    }
    foreach ($test in $expected) {
        if ($completions[$test] -ne 1) { throw "Missing or duplicate result: $test ($($completions[$test]))" }
    }
    $ordinary = @(Get-Content '.strictmetadata/validation/windows-nonintegration-current.jsonl' | ForEach-Object { $_ | ConvertFrom-Json })
    if (@($ordinary | Where-Object Action -eq 'fail').Count) { throw 'Non-integration suite has failures' }
    if (@($ordinary | Where-Object { $_.Action -eq 'skip' -and $_.Test }).Count) { throw 'Non-integration suite skipped a test' }
    $packages = @(go list ./... | Where-Object { $_ -ne 'github.com/stricttools/safegit/internal/test' })
    if ($LASTEXITCODE -ne 0) { throw 'Package discovery failed' }
    foreach ($package in $packages) {
        $done = @($ordinary | Where-Object { $_.Package -eq $package -and -not $_.Test -and $_.Action -in @('pass', 'skip') })
        if ($done.Count -ne 1) { throw "Missing ordinary package result: $package" }
    }
    $matrix = Get-Content '.strictmetadata/validation/windows-command-matrix.json' -Raw | ConvertFrom-Json
    $inventory = Get-Content '.strictmetadata/validation/windows-command-help.json' -Raw | ConvertFrom-Json
    $leaves = @($inventory | Where-Object {
        $prefix = $_.command + ' '
        -not @($inventory | Where-Object { $_.command.StartsWith($prefix) }).Count
    } | ForEach-Object { $_.command })
    $difference = @(Compare-Object ($leaves | Sort-Object) (@($matrix.command) | Sort-Object))
    if ($difference.Count) { throw 'Functional matrix does not match declared leaf command inventory' }
    foreach ($row in $matrix) {
        foreach ($kind in @('normal', 'refusal')) {
            $test = $row.$kind
            if (-not $passed[$test]) {
                throw "No passing functional evidence for $($row.command) $kind : $test"
            }
        }
    }
    $skipped = @($events | Where-Object { $_.Action -eq 'skip' -and $_.Test })
    # These two pre-existing tests require two DISTINCT case-only filenames.
    # They are inapplicable on this folding filesystem, never passing coverage.
    $foldingCases = @('TestMvCaseOnlyRefusesAnOccupiedDestination', 'TestMvRenamesOnlyTheCase/ignorecase=false')
    $inapplicable = @()
    foreach ($skip in $skipped) {
        if ($stressSkips[$skip.Test]) { continue }
        $foldingReason = @($events | Where-Object { $_.Test -eq $skip.Test -and $_.Action -eq 'output' -and $_.Output -like '*the test filesystem folds case*' })
        if ($skip.Test -notin $foldingCases -or -not $foldingReason.Count) { throw "Unexpected skipped ordinary test: $($skip.Test)" }
        $inapplicable += $skip.Test
    }
    $report = [ordered]@{
        nonIntegrationPackagesPassed = @($ordinary | Where-Object { $_.Action -eq 'pass' -and -not $_.Test }).Count
        packagesWithoutTests = @($ordinary | Where-Object { $_.Action -eq 'skip' -and -not $_.Test } | ForEach-Object { $_.Package })
        integrationTopLevelTests = $expected.Count
        integrationTopLevelPassed = @($events | Where-Object { $_.Action -eq 'pass' -and $_.Test -and $_.Test -notmatch '/' }).Count
        passedCases = @($events | Where-Object { $_.Action -eq 'pass' -and $_.Test }).Count
        skippedTests = @($skipped | ForEach-Object { $_.Test })
        optInStressSkippedTests = @($skipped | Where-Object { $stressSkips[$_.Test] } | ForEach-Object { $_.Test })
        filesystemInapplicableTests = $inapplicable
        functionalCommandRows = @($matrix).Count
        evidence = 'windows-integration-[0-3].jsonl'
    }
    $report | ConvertTo-Json -Depth 5 | Set-Content '.strictmetadata/validation/windows-summary.json' -Encoding UTF8
    $report | ConvertTo-Json -Depth 5
} finally { Pop-Location }
