<#
.SYNOPSIS
  Safe orchestration tests for run_i7.ps1 (Phase I7 G2).

.DESCRIPTION
  Exercises resume, crash recovery, budget enforcement, approval binding, runlist
  integrity, frozen-root rejection, packaging and mode gating — using a stub
  runner (cmd/i7stub) so no engine trial is executed.

  Every scenario uses its own results root under %TEMP%. Nothing under the
  repository's frozen data directories is read or written beyond hashing.

  Exit code 0 = all scenarios passed.
#>

[CmdletBinding()]
param(
    [string]$RepoRoot = '',
    [string]$OutDir = ''
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2

if ([string]::IsNullOrWhiteSpace($RepoRoot)) {
    $RepoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..\..\..'))
}
if ([string]::IsNullOrWhiteSpace($OutDir)) {
    $OutDir = Join-Path $PSScriptRoot '..\evidence\g2'
}
$OutDir = [System.IO.Path]::GetFullPath($OutDir)
if (-not (Test-Path -LiteralPath $OutDir)) { New-Item -ItemType Directory -Force -Path $OutDir | Out-Null }

$script = Join-Path $RepoRoot 'research\experiments\phase_i7\run_i7.ps1'
$stubPkg = '.\research\experiments\phase_i7\cmd\i7stub'
$pebbleExe = Join-Path $RepoRoot 'research\experiments\phase_i7\pebble_runner\pebble_runner.exe'

$stamp = (Get-Date).ToUniversalTime().ToString('yyyyMMddHHmmss')
$base = Join-Path $env:TEMP ('i7_orch_' + $stamp)
New-Item -ItemType Directory -Force -Path $base | Out-Null

$script:Results = New-Object System.Collections.ArrayList
$script:Log = New-Object System.Text.StringBuilder

function Log([string]$Text) {
    Write-Host $Text
    [void]$script:Log.AppendLine($Text)
}

function Record([string]$Name, [bool]$Pass, [string]$Detail) {
    [void]$script:Results.Add([pscustomobject]@{ scenario = $Name; pass = $Pass; detail = $Detail })
    if ($Pass) { Write-Host ("  [PASS] {0} - {1}" -f $Name, $Detail) -ForegroundColor Green }
    else { Write-Host ("  [FAIL] {0} - {1}" -f $Name, $Detail) -ForegroundColor Red }
}

# Run the driver in a child process so its `exit` cannot terminate this script.
function Invoke-Driver {
    param([string]$Root, [string[]]$ArgList)
    $all = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $script, '-Root', $Root) + $ArgList
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = 'powershell.exe'
    $psi.Arguments = (($all | ForEach-Object { '"' + ($_ -replace '"', '\"') + '"' }) -join ' ')
    $psi.UseShellExecute = $false
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $psi.CreateNoWindow = $true
    if ($null -ne $env:I7_STUB_MODE) { $psi.EnvironmentVariables['I7_STUB_MODE'] = $env:I7_STUB_MODE }
    $p = New-Object System.Diagnostics.Process
    $p.StartInfo = $psi
    [void]$p.Start()
    $o = $p.StandardOutput.ReadToEndAsync()
    $e = $p.StandardError.ReadToEndAsync()
    if (-not $p.WaitForExit(300000)) { try { $p.Kill() } catch { } }
    $out = $o.Result
    $err = $e.Result
    [void]$script:Log.AppendLine('--- $ run_i7.ps1 ' + ($ArgList -join ' '))
    [void]$script:Log.AppendLine($out)
    if ($err) { [void]$script:Log.AppendLine('[stderr] ' + $err) }
    return [pscustomobject]@{ ExitCode = $(if ($p.HasExited) { $p.ExitCode } else { -1 }); Out = $out; Err = $err }
}

function New-Root([string]$Name) {
    $r = Join-Path $base $Name
    New-Item -ItemType Directory -Force -Path $r | Out-Null
    return $r
}

function New-Approval {
    param(
        [string]$Root,
        [string]$Gate,
        [string[]]$AllowedBlocks,
        [string]$RunlistHash,
        [string]$RunnerHash,
        [int]$MaxTrials = 20,
        [bool]$AllowDirty = $true,
        [string]$Scope = ''
    )
    $obj = [ordered]@{
        campaign_id      = 'I7_STRENGTHENING_V1'
        protocol_version = '1.2.0-windows'
        gate             = $Gate
        approved         = $true
        approved_by      = 'orchestration-test'
        approved_at      = (Get-Date).ToUniversalTime().ToString('o')
        scope            = if ($Scope) { $Scope } else { 'SYNTHETIC - orchestration test only, no engine trials' }
        runlist_sha256   = $RunlistHash
        runner_sha256    = $RunnerHash
        allowed_blocks   = $AllowedBlocks
        allowed_phases   = if ($Scope -eq 'calibration') { @('calibration') } else { @('preflight', 'confirmatory') }
        max_trials       = $MaxTrials
        allow_dirty      = $AllowDirty
    }
    $path = Join-Path $Root 'APPROVAL.json'
    [System.IO.File]::WriteAllText($path, ($obj | ConvertTo-Json -Depth 8), (New-Object System.Text.UTF8Encoding($false)))
    return $path
}

function Get-FileHashHex([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return '' }
    (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Read-Lines([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return @() }
    return @([System.IO.File]::ReadAllLines($Path) | Where-Object { $_.Trim() -ne '' } | ForEach-Object { $_ | ConvertFrom-Json })
}

function Prepare-Root([string]$Root) {
    $r = Invoke-Driver -Root $Root -ArgList @('-Check', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $script:Stub)
    if ($r.ExitCode -ne 0) { throw ('-Check failed for ' + $Root + ': ' + $r.Out) }
    $d = Invoke-Driver -Root $Root -ArgList @('-DryRun', '-Campaign', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $script:Stub)
    if ($d.ExitCode -ne 0) { throw ('-DryRun failed for ' + $Root + ': ' + $d.Out) }
    return @{
        Runlist = Join-Path $Root 'runlist.csv'
        Hash    = Get-FileHashHex (Join-Path $Root 'runlist.csv')
    }
}

# ---------------------------------------------------------------------------
# Build the stub runner
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host 'Building orchestration stub runner' -ForegroundColor Cyan
$env:CGO_ENABLED = '0'
Push-Location -LiteralPath $RepoRoot
try {
    $null = & go build -trimpath -o (Join-Path $base 'i7stub.exe') $stubPkg 2>&1
    if ($LASTEXITCODE -ne 0) { throw 'stub build failed' }
}
finally { Pop-Location }
$script:Stub = Join-Path $base 'i7stub.exe'
$stubHash = Get-FileHashHex $script:Stub
Log ('stub: ' + $script:Stub + ' sha256 ' + $stubHash)
$pebbleArg = $script:Stub
if (Test-Path -LiteralPath $pebbleExe) { $pebbleArg = $pebbleExe }

# ---------------------------------------------------------------------------
# S1 + S2: -Check and -DryRun produce a frozen runlist
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host 'S1/S2: -Check and -DryRun' -ForegroundColor Cyan
$root1 = New-Root 's1_basic'
$prep = Prepare-Root $root1
$manifest1 = Get-Content (Join-Path $root1 'campaign_manifest.json') -Raw | ConvertFrom-Json
$rows = @(Import-Csv $prep.Runlist)
Record 'S1-check' (Test-Path (Join-Path $root1 'platform_manifest.json')) 'platform manifest written'
Record 'S2-runlist' ($rows.Count -eq 366) ('runlist rows = ' + $rows.Count)
Record 'S2-anchor-start' ($rows[0].block -eq 'D3' -and $rows[0].cell -eq 'start' -and $rows[2].cell -eq 'start') ('first rows: ' + $rows[0].block + '/' + $rows[0].cell)
Record 'S2-anchor-end' ($rows[-1].block -eq 'D3' -and $rows[-3].cell -eq 'end') ('last rows: ' + $rows[-1].block + '/' + $rows[-1].cell)
Record 'S2-hash-recorded' ($manifest1.runlist_sha256 -eq $prep.Hash) 'manifest binds the runlist hash'
Record 'S2-authority-revoked' ($manifest1.model_authority -match 'REVOKED') ('model_authority = ' + $manifest1.model_authority)
Record 'S2-r12e-budget' (([int]$manifest1.hard_ceiling -eq 400) -and ([int]$manifest1.replacement_cap -eq 16)) ('ceiling=' + $manifest1.hard_ceiling + ' replacement_cap=' + $manifest1.replacement_cap)

# ---------------------------------------------------------------------------
# S3: approval template is unapproved
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host 'S3: approval template' -ForegroundColor Cyan
$tplRun = Invoke-Driver -Root $root1 -ArgList @('-Campaign', '-WriteApprovalTemplate', '-Gate', 'G4')
$tplPath = Join-Path $root1 'APPROVAL.template.G4.json'
$tplOk = (Test-Path $tplPath)
if ($tplOk) {
    $tpl = Get-Content $tplPath -Raw | ConvertFrom-Json
    $tplOk = (-not $tpl.approved) -and ($tpl.runlist_sha256 -eq $prep.Hash)
}
Record 'S3-template' ($tplRun.ExitCode -eq 0 -and $tplOk) 'template written with approved=false and the current runlist hash'

# ---------------------------------------------------------------------------
# S4: execution authority revocation (R2 engineering acceptance, blocker 1).
# The G2-era execution path fails closed unconditionally: no approval state,
# override, or test mode can resurrect it. The approval-binding refusals the
# old S4 exercised are now covered by Go unit tests on i7trial (see
# cmd/i7trial/main_test.go, TestCheckApprovalBindings).
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host 'S4: legacy execution authority revoked' -ForegroundColor Cyan
$null = New-Approval -Root $root1 -Gate 'G4' -AllowedBlocks @('D3') -RunlistHash $prep.Hash -RunnerHash $stubHash

$r = Invoke-Driver -Root $root1 -ArgList @('-Campaign', '-Blocks', 'D3', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
Record 'S4-campaign-revoked' (($r.ExitCode -eq 5) -and ($r.Out -match 'revoked')) ('exit=' + $r.ExitCode + ' campaign refused with a valid approval present')

$badHashApproval = New-Approval -Root $root1 -Gate 'G4' -AllowedBlocks @('D3') -RunlistHash ('0' * 64) -RunnerHash $stubHash
$r = Invoke-Driver -Root $root1 -ArgList @('-Campaign', '-Blocks', 'D3', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
Record 'S4-campaign-revoked-invalid-approval' (($r.ExitCode -eq 5) -and ($r.Out -match 'revoked')) 'revocation fires before any approval evaluation'

$r = Invoke-Driver -Root $root1 -ArgList @('-Preflight', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
Record 'S4-preflight-revoked' (($r.ExitCode -eq 5) -and ($r.Out -match 'revoked')) 'exit=5 preflight refused'

$r = Invoke-Driver -Root $root1 -ArgList @('-Resume', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
Record 'S4-resume-revoked' (($r.ExitCode -eq 5) -and ($r.Out -match 'revoked')) 'exit=5 resume refused'

# -DryRun must remain available (inspection only, launches nothing).
$r = Invoke-Driver -Root $root1 -ArgList @('-DryRun', '-Campaign', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
Record 'S4-dryrun-still-allowed' ($r.ExitCode -eq 0) 'dry-run inspection remains available (exit 0)'

# ---------------------------------------------------------------------------
# S5-S8: all trial-executing flows are revoked (R1.2e). The stub-runner
# scenarios they previously exercised (clean run, crash/resume/replacement,
# budget ceilings, timeout, invalid envelope) become unreachable until the
# pilot gate rebuilds the execution path around runlistctl; the underlying
# runner semantics remain covered by Go unit tests (cmd/i7trial,
# harness2) and will be re-covered at the driver level post-rebuild.
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host 'S5-S8: executing flows revoked' -ForegroundColor Cyan
$root2 = New-Root 's5_revoked'
$prep2 = Prepare-Root $root2
$null = New-Approval -Root $root2 -Gate 'G4' -AllowedBlocks @('D3') -RunlistHash $prep2.Hash -RunnerHash $stubHash
$env:I7_STUB_MODE = 'ok'
$r = Invoke-Driver -Root $root2 -ArgList @('-Campaign', '-Blocks', 'D3', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
Record 'S5-campaign-revoked' (($r.ExitCode -eq 5) -and ($r.Out -match 'revoked')) ('exit=' + $r.ExitCode + ' no trial process launched')

$r = Invoke-Driver -Root $root2 -ArgList @('-Resume', '-Blocks', 'D3', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
Record 'S6-resume-revoked' (($r.ExitCode -eq 5) -and ($r.Out -match 'revoked')) 'exit=5 resume refused'

$r = Invoke-Driver -Root $root2 -ArgList @('-Campaign', '-Blocks', 'D3', '-MaxTrials', '3', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
Record 'S7-budget-revoked' (($r.ExitCode -eq 5) -and ($r.Out -match 'revoked')) 'exit=5 even under -MaxTrials (no partial authority)'

$env:I7_STUB_MODE = 'invalid'
$r = Invoke-Driver -Root $root2 -ArgList @('-Campaign', '-Blocks', 'D3', '-MaxTrials', '1', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
Record 'S8-invalid-envelope-path-revoked' (($r.ExitCode -eq 5) -and ($r.Out -match 'revoked')) 'exit=5 (envelope validation itself is covered by Go unit tests)'
$env:I7_STUB_MODE = 'ok'

# ---------------------------------------------------------------------------
# S9: frozen-root rejection and mode gating
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host 'S9: safety and mode gating' -ForegroundColor Cyan
$frozenRoot = Join-Path $RepoRoot 'research\experiments\data\targeted_validation_v1\i7_should_refuse'
$r = Invoke-Driver -Root $frozenRoot -ArgList @('-Check')
Record 'S9-frozen-root' (($r.ExitCode -ne 0) -or ($r.Out -match 'frozen')) 'results root inside a frozen campaign directory refused'

$r = Invoke-Driver -Root $root2 -ArgList @('-Diagnostics')
Record 'S9-diagnostics-revoked' (($r.ExitCode -eq 5) -and ($r.Out -match 'revoked')) 'diagnostics execution refused (R1.2e revocation precedes gate checks)'

$r = Invoke-Driver -Root $root2 -ArgList @('-Preflight', '-Gate', 'G4')
Record 'S9-preflight-revoked' (($r.ExitCode -eq 5) -and ($r.Out -match 'revoked')) 'preflight execution refused regardless of gate'

$r = Invoke-Driver -Root $root2 -ArgList @('-Campaign', '-RunnerOverride', $script:Stub)
Record 'S9-override-guard' (($r.ExitCode -ne 0) -and ($r.Out -match 'TestMode')) 'runner override without -TestMode refused (pre-dispatch guard still first)'

# ---------------------------------------------------------------------------
# S10: calibration authority revoked; template generation still available
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host 'S10: calibration authority' -ForegroundColor Cyan
$rootCal = New-Root 's10_cal'
$prepCal = Prepare-Root $rootCal

$r = Invoke-Driver -Root $rootCal -ArgList @('-Calibrate', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
Record 'S10-cal-revoked' (($r.ExitCode -eq 5) -and ($r.Out -match 'revoked')) 'exit=5 -Calibrate refused (the 9-run G3 calibration is legacy authority)'

# Generate calibration approval template (non-executing, still available)
$r = Invoke-Driver -Root $rootCal -ArgList @('-WriteApprovalTemplate', '-Gate', 'G3', '-Scope', 'calibration')
$calTpl = Join-Path $rootCal 'APPROVAL.template.G3.calibration.json'
Record 'S10-cal-template' (($r.ExitCode -eq 0) -and (Test-Path -LiteralPath $calTpl)) 'calibration approval template generated'

# ---------------------------------------------------------------------------
# S11: the authoritative R1.2e run-list model (runlistctl) validates end to end
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host 'S11: runlistctl authority' -ForegroundColor Cyan
$rlDir = Join-Path $base 's11_runlistctl'
New-Item -ItemType Directory -Force -Path $rlDir | Out-Null
$rlCsv = Join-Path $rlDir 'runlist.csv'
$rlReport = Join-Path $rlDir 'validation.json'
$env:CGO_ENABLED = '0'
Push-Location -LiteralPath $RepoRoot
try {
    $null = & go run .\research\experiments\phase_i7\cmd\runlistctl generate -out $rlCsv -seed 20261101 2>&1
    $genOk = ($LASTEXITCODE -eq 0)
    $null = & go run .\research\experiments\phase_i7\cmd\runlistctl validate -runlist $rlCsv -report $rlReport 2>&1
    $valOk = ($LASTEXITCODE -eq 0)
}
finally { Pop-Location }
$valRep = $null
if (Test-Path -LiteralPath $rlReport) { $valRep = Get-Content $rlReport -Raw | ConvertFrom-Json }
Record 'S11-generate-validate' ($genOk -and $valOk) ('runlistctl generate+validate exit 0')
Record 'S11-ceiling-model' (($null -ne $valRep) -and ($valRep.ok) -and ([int]$valRep.worst_case_total -le 400) -and ([int]$valRep.guaranteed_buffer -ge 12)) ('worst case=' + $valRep.worst_case_total + ' buffer=' + $valRep.guaranteed_buffer + ' ceiling=400')

# S10 (continued): every legacy calibration execution path is revoked. The
# idempotency/restart-safety/partial-completion machinery of the G3
# calibration is unreachable until the pilot gate rebuilds it; its semantics
# remain specified by I7_G3_CALIBRATION_CORRECTIVE_PACKET.md.
# (Each root supports exactly one -Check: a second -Check rewrites the
# platform manifest and the frozen-manifest integrity check rightly refuses
# the follow-up DryRun.)
$r = Invoke-Driver -Root $rootCal -ArgList @('-Calibrate', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg, '-Scope', 'calibration')
Record 'S10-cal-revoked-with-scope' (($r.ExitCode -eq 5) -and ($r.Out -match 'revoked')) 'exit=5 even with a calibration-scope approval (no bypass exists)'

$calLedgerExists = Test-Path -LiteralPath (Join-Path $rootCal 'calibration\calibration_ledger.jsonl')
Record 'S10-cal-no-ledger-created' (-not $calLedgerExists) 'refused calibration created no execution ledger'

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
$failed = @($script:Results | Where-Object { -not $_.pass })
Log ''
Log ('scenarios: {0}  passed: {1}  failed: {2}' -f $script:Results.Count, ($script:Results.Count - $failed.Count), $failed.Count)
$logPath = Join-Path $OutDir ('orchestration_' + $stamp + '.log')
[System.IO.File]::WriteAllText($logPath, $script:Log.ToString(), (New-Object System.Text.UTF8Encoding($false)))
$jsonPath = Join-Path $OutDir ('orchestration_' + $stamp + '.json')
[System.IO.File]::WriteAllText($jsonPath, (($script:Results | ConvertTo-Json -Depth 6)), (New-Object System.Text.UTF8Encoding($false)))
Write-Host ''
Write-Host ('log: ' + $logPath)
Write-Host ('json: ' + $jsonPath)
if ($failed.Count -gt 0) {
    foreach ($f in $failed) { Write-Host ('  FAILED: ' + $f.scenario + ' - ' + $f.detail) -ForegroundColor Red }
    exit 1
}
exit 0
