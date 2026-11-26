param([string]$OutputPath = '.strictmetadata/validation/windows-command-help.json')
$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT') { throw 'Run this with native Windows PowerShell/pwsh and Windows Go.' }
$root = Split-Path $PSScriptRoot -Parent
Push-Location $root
$scratch = Join-Path ([IO.Path]::GetTempPath()) ('safegit-command-inventory-' + [guid]::NewGuid())
try {
    New-Item -ItemType Directory -Path $scratch | Out-Null
    $binary = Join-Path $scratch 'safegit.exe'
    go build -o $binary .
    if ($LASTEXITCODE -ne 0) { throw 'Native build failed' }
    $schema = Get-Content '.strictmetadata/.cli-schema/schema.json' -Raw -Encoding UTF8 | ConvertFrom-Json
    $commands = @()
    foreach ($entry in $schema.commands.PSObject.Properties) { $commands += ,@($entry.Name) }
    foreach ($group in $schema.groups.PSObject.Properties) {
        $commands += ,@($group.Name)
        foreach ($entry in $group.Value.commands.PSObject.Properties) { $commands += ,@($group.Name, $entry.Name) }
    }
    $results = @()
    foreach ($command in $commands) {
        $argv = @($command) + @('--help')
        $text = & $binary @argv 2>&1
        $code = $LASTEXITCODE
        $results += [pscustomobject]@{ command = ($command -join ' '); probe = 'help-only'; exit_code = $code; nonempty_output = ($text.Length -gt 0) }
    }
    $destination = Join-Path $root $OutputPath
    [IO.Directory]::CreateDirectory((Split-Path $destination -Parent)) | Out-Null
    [IO.File]::WriteAllText($destination, ($results | ConvertTo-Json -Depth 5) + "`n", (New-Object Text.UTF8Encoding $false))
    $failed = @($results | Where-Object { $_.exit_code -ne 0 -or -not $_.nonempty_output })
    if ($failed.Count -gt 0) { throw ('Command help failures: ' + ($failed.command -join ', ')) }
    Write-Output ('Native help dispatch: {0} command/group paths passed. This does NOT certify normal execution/refusal coverage.' -f $results.Count)
} finally {
    Remove-Item $scratch -Recurse -Force -ErrorAction Stop
    Pop-Location
}
