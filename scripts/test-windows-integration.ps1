param([int]$Shard = 0, [int]$Shards = 4)
$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT') { throw 'Native Windows only' }
$keys = @('GIT_CONFIG_COUNT', 'GIT_CONFIG_KEY_0', 'GIT_CONFIG_VALUE_0')
$saved = @{}
foreach ($key in $keys) { $saved[$key] = [Environment]::GetEnvironmentVariable($key, 'Process') }
if ($Shard -lt 0 -or $Shard -ge $Shards) { throw 'Invalid shard' }
$root = Split-Path $PSScriptRoot -Parent
Push-Location $root
try {
    $env:GIT_CONFIG_COUNT = '1'
    $env:GIT_CONFIG_KEY_0 = 'protocol.file.allow'
    $env:GIT_CONFIG_VALUE_0 = 'always'
    $names = @(go test ./internal/test -list '^Test' | Where-Object { $_ -match '^Test' })
    if ($LASTEXITCODE -ne 0) { throw 'Integration test discovery failed' }
    if ($Shard -eq 0) {
        $names | Set-Content '.strictmetadata/validation/windows-integration-test-names.txt' -Encoding UTF8
    }
    $selected = @()
    for ($i = 0; $i -lt $names.Count; $i++) { if (($i % $Shards) -eq $Shard) { $selected += $names[$i] } }
    $pattern = '^(' + ($selected -join '|') + ')$'
    $destination = Join-Path $root ('.strictmetadata/validation/windows-integration-' + $Shard + '.jsonl')
    # Stream events while retaining Go's exit status, so a timeout cannot hide
    # which individual test was active or which tests had already passed.
    go test ./internal/test -run $pattern -count=1 -timeout=35m -json | Out-File -FilePath $destination -Encoding utf8
    $code = $LASTEXITCODE
    if ($code -ne 0) { throw ('Integration shard failed; see ' + $destination) }
    Write-Output ('Integration shard ' + $Shard + '/' + $Shards + ' passed (' + $selected.Count + ' top-level tests)')
} finally {
    foreach ($key in $keys) { [Environment]::SetEnvironmentVariable($key, $saved[$key], 'Process') }
    Pop-Location
}
