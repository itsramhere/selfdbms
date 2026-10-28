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
# S4: approval binding refusals
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host 'S4: approval binding' -ForegroundColor Cyan
$null = New-Approval -Root $root1 -Gate 'G4' -AllowedBlocks @('D3') -RunlistHash $prep.Hash -RunnerHash $stubHash

$badHashApproval = New-Approval -Root $root1 -Gate 'G4' -AllowedBlocks @('D3') -RunlistHash ('0' * 64) -RunnerHash $stubHash
$r = Invoke-Driver -Root $root1 -ArgList @('-Campaign', '-Blocks', 'D3', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
Record 'S4-runlist-binding' ($r.ExitCode -ne 0 -and ($r.Out -match 'runlist_sha256')) 'wrong runlist hash refused'

$null = New-Approval -Root $root1 -Gate 'G4' -AllowedBlocks @('D3') -RunlistHash $prep.Hash -RunnerHash ('0' * 64)
$r = Invoke-Driver -Root $root1 -ArgList @('-Campaign', '-Blocks', 'D3', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
Record 'S4-runner-binding' ($r.ExitCode -ne 0 -and ($r.Out -match 'runner_sha256')) 'wrong runner hash refused'

$null = New-Approval -Root $root1 -Gate 'G4' -AllowedBlocks @('E1') -RunlistHash $prep.Hash -RunnerHash $stubHash
$r = Invoke-Driver -Root $root1 -ArgList @('-Campaign', '-Blocks', 'D3', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
Record 'S4-block-binding' ($r.ExitCode -ne 0 -and ($r.Out -match 'not in the approved set')) 'unapproved block refused'

$null = New-Approval -Root $root1 -Gate 'G4' -AllowedBlocks @('D3') -RunlistHash $prep.Hash -RunnerHash $stubHash -AllowDirty $false
$r = Invoke-Driver -Root $root1 -ArgList @('-Campaign', '-Blocks', 'D3', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
Record 'S4-dirty-binding' ($r.ExitCode -ne 0 -and ($r.Out -match 'allow_dirty')) 'dirty tree refused without allow_dirty'

$null = New-Approval -Root $root1 -Gate 'G3' -AllowedBlocks @('D3') -RunlistHash $prep.Hash -RunnerHash $stubHash
$r = Invoke-Driver -Root $root1 -ArgList @('-Campaign', '-Blocks', 'D3', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
Record 'S4-gate-binding' ($r.ExitCode -ne 0 -and ($r.Out -match 'requires G4')) 'wrong gate refused'

# ---------------------------------------------------------------------------
# S5: a clean 6-trial run (stub), then packaging
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host 'S5: clean run + package' -ForegroundColor Cyan
$null = New-Approval -Root $root1 -Gate 'G4' -AllowedBlocks @('D3') -RunlistHash $prep.Hash -RunnerHash $stubHash
$env:I7_STUB_MODE = 'ok'
$r = Invoke-Driver -Root $root1 -ArgList @('-Campaign', '-Blocks', 'D3', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
$ledger = Read-Lines (Join-Path $root1 'results.jsonl')
$finished = @($ledger | Where-Object { $_.event -eq 'FINISHED' })
$success = @($finished | Where-Object { $_.status -eq 'SUCCESS' })
Record 'S5-run' (($r.ExitCode -eq 0) -and ($success.Count -eq 6)) ('finished=' + $finished.Count + ' success=' + $success.Count)

$zip = Join-Path $root1 'I7_WINDOWS_RESULTS.zip'
Record 'S5-package' ((Test-Path $zip) -and (Test-Path (Join-Path $root1 'checksums.sha256')) -and (Test-Path (Join-Path $root1 'summary.json'))) 'zip + checksums + summary present'

# ---------------------------------------------------------------------------
# S6: crash recovery and resume
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host 'S6: crash then resume' -ForegroundColor Cyan
$root2 = New-Root 's6_crash'
$prep2 = Prepare-Root $root2
$null = New-Approval -Root $root2 -Gate 'G4' -AllowedBlocks @('D3') -RunlistHash $prep2.Hash -RunnerHash $stubHash
$env:I7_STUB_MODE = 'crash'
$r = Invoke-Driver -Root $root2 -ArgList @('-Campaign', '-Blocks', 'D3', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
$ledger = Read-Lines (Join-Path $root2 'results.jsonl')
$started = @($ledger | Where-Object { $_.event -eq 'STARTED' })
$finished = @($ledger | Where-Object { $_.event -eq 'FINISHED' })
$bad = @($finished | Where-Object { $_.status -ne 'SUCCESS' })
Record 'S6-crash-recorded' (($started.Count -eq 6) -and ($bad.Count -eq 6)) ('started=' + $started.Count + ' failed=' + $bad.Count)

$env:I7_STUB_MODE = 'ok'
$r = Invoke-Driver -Root $root2 -ArgList @('-Resume', '-Blocks', 'D3', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
$ledger = Read-Lines (Join-Path $root2 'results.jsonl')
$finished = @($ledger | Where-Object { $_.event -eq 'FINISHED' })
$success = @($finished | Where-Object { $_.status -eq 'SUCCESS' })
$m2 = Get-Content (Join-Path $root2 'campaign_manifest.json') -Raw | ConvertFrom-Json
Record 'S6-resume' (($r.ExitCode -eq 0) -and ($success.Count -eq 6)) ('success after resume = ' + $success.Count)
Record 'S6-replacements' ([int]$m2.replacements_used -eq 6) ('replacements_used = ' + $m2.replacements_used)

# A second resume must be a no-op (nothing left to run).
$before = @(Read-Lines (Join-Path $root2 'results.jsonl')).Count
$r = Invoke-Driver -Root $root2 -ArgList @('-Resume', '-Blocks', 'D3', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
$after = @(Read-Lines (Join-Path $root2 'results.jsonl')).Count
Record 'S6-idempotent-resume' ($after -eq $before) ('ledger lines unchanged: ' + $before)

# ---------------------------------------------------------------------------
# S7: budget enforcement
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host 'S7: trial budget' -ForegroundColor Cyan
$root3 = New-Root 's7_budget'
$prep3 = Prepare-Root $root3
$null = New-Approval -Root $root3 -Gate 'G4' -AllowedBlocks @('D3') -RunlistHash $prep3.Hash -RunnerHash $stubHash
$env:I7_STUB_MODE = 'ok'
$r = Invoke-Driver -Root $root3 -ArgList @('-Campaign', '-Blocks', 'D3', '-MaxTrials', '3', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
$s3 = @(Read-Lines (Join-Path $root3 'results.jsonl') | Where-Object { $_.event -eq 'STARTED' })
Record 'S7-max-trials' (($r.ExitCode -eq 0) -and ($s3.Count -eq 3)) ('started = ' + $s3.Count + ' with -MaxTrials 3')

$r = Invoke-Driver -Root $root3 -ArgList @('-Campaign', '-Blocks', 'D3', '-MaxTrials', '3', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
$s3b = @(Read-Lines (Join-Path $root3 'results.jsonl') | Where-Object { $_.event -eq 'STARTED' })
Record 'S7-ceiling-blocks' (($r.ExitCode -ne 0) -and ($s3b.Count -eq 3) -and ($r.Out -match 'ceiling')) 'second run refused: attempts already at the -MaxTrials ceiling'

$env:I7_STUB_MODE = 'hang'
$root4 = New-Root 's7_timeout'
$prep4 = Prepare-Root $root4
$null = New-Approval -Root $root4 -Gate 'G4' -AllowedBlocks @('D3') -RunlistHash $prep4.Hash -RunnerHash $stubHash
$r = Invoke-Driver -Root $root4 -ArgList @('-Campaign', '-Blocks', 'D3', '-MaxTrials', '1', '-TrialTimeoutSec', '2', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
$fin4 = @(Read-Lines (Join-Path $root4 'results.jsonl') | Where-Object { $_.event -eq 'FINISHED' })
Record 'S7-timeout' (($fin4.Count -eq 1) -and ($fin4[0].status -eq 'TIMEOUT')) ('status = ' + $(if ($fin4.Count -gt 0) { $fin4[0].status } else { 'none' }))
$env:I7_STUB_MODE = 'ok'

# ---------------------------------------------------------------------------
# S8: an invalid envelope is rejected, never recorded as success
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host 'S8: envelope validation' -ForegroundColor Cyan
$root5 = New-Root 's8_invalid'
$prep5 = Prepare-Root $root5
$null = New-Approval -Root $root5 -Gate 'G4' -AllowedBlocks @('D3') -RunlistHash $prep5.Hash -RunnerHash $stubHash
$env:I7_STUB_MODE = 'invalid'
$r = Invoke-Driver -Root $root5 -ArgList @('-Campaign', '-Blocks', 'D3', '-MaxTrials', '1', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
$fin5 = @(Read-Lines (Join-Path $root5 'results.jsonl') | Where-Object { $_.event -eq 'FINISHED' })
Record 'S8-invalid-envelope' (($fin5.Count -eq 1) -and ($fin5[0].status -eq 'INVALID')) ('status = ' + $(if ($fin5.Count -gt 0) { $fin5[0].status } else { 'none' }))
$env:I7_STUB_MODE = 'ok'

# ---------------------------------------------------------------------------
# S9: frozen-root rejection and mode gating
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host 'S9: safety and mode gating' -ForegroundColor Cyan
$frozenRoot = Join-Path $RepoRoot 'research\experiments\data\targeted_validation_v1\i7_should_refuse'
$r = Invoke-Driver -Root $frozenRoot -ArgList @('-Check')
Record 'S9-frozen-root' (($r.ExitCode -ne 0) -or ($r.Out -match 'frozen')) 'results root inside a frozen campaign directory refused'

$r = Invoke-Driver -Root $root3 -ArgList @('-Diagnostics')
Record 'S9-diagnostics-gate' (($r.ExitCode -ne 0) -and ($r.Out -match 'G5')) 'diagnostics requires the G5 gate'

$r = Invoke-Driver -Root $root3 -ArgList @('-Preflight', '-Gate', 'G4')
Record 'S9-gate-mode' (($r.ExitCode -ne 0) -and ($r.Out -match 'requires G3')) 'preflight rejects a G4 gate'

$r = Invoke-Driver -Root $root3 -ArgList @('-Campaign', '-RunnerOverride', $script:Stub)
Record 'S9-override-guard' (($r.ExitCode -ne 0) -and ($r.Out -match 'TestMode')) 'runner override without -TestMode refused'

# ---------------------------------------------------------------------------
# S10: calibration idempotency, restart-safety, scope binding, and budget
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host 'S10: calibration correctness' -ForegroundColor Cyan
$rootCal = New-Root 's10_cal'
$prepCal = Prepare-Root $rootCal

# S10-scope-refusal: Preflight approval (scope not matching calibration) must be refused by -Calibrate
$null = New-Approval -Root $rootCal -Gate 'G3' -AllowedBlocks @('PF') -RunlistHash $prepCal.Hash -RunnerHash $stubHash -Scope 'preflight'
$r = Invoke-Driver -Root $rootCal -ArgList @('-Calibrate', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
Record 'S10-cal-scope-refusal' (($r.ExitCode -ne 0) -and ($r.Out -match 'calibration requires scope')) 'calibration rejects non-calibration approval scope'

# Generate calibration approval template with -Scope calibration
$r = Invoke-Driver -Root $rootCal -ArgList @('-WriteApprovalTemplate', '-Gate', 'G3', '-Scope', 'calibration')
$calTpl = Join-Path $rootCal 'APPROVAL.template.G3.calibration.json'
Record 'S10-cal-template' (($r.ExitCode -eq 0) -and (Test-Path -LiteralPath $calTpl)) 'calibration approval template generated'

# Approve for calibration
$null = New-Approval -Root $rootCal -Gate 'G3' -AllowedBlocks @('PF') -RunlistHash $prepCal.Hash -RunnerHash $stubHash -Scope 'calibration'

# Run calibration (all 9 runs with stub runner)
$env:I7_STUB_MODE = 'ok'
$r = Invoke-Driver -Root $rootCal -ArgList @('-Calibrate', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
$calLedger = Join-Path $rootCal 'calibration\calibration_ledger.jsonl'
$calStarted = @(Read-Lines $calLedger | Where-Object { $_.event -eq 'STARTED' })
$calFinished = @(Read-Lines $calLedger | Where-Object { $_.event -eq 'FINISHED' -and $_.status -eq 'SUCCESS' })
$calJson = Join-Path $rootCal 'calibration.json'
Record 'S10-cal-run' (($r.ExitCode -eq 0) -and ($calFinished.Count -eq 9) -and (Test-Path -LiteralPath $calJson)) ('calibration completed: finished=' + $calFinished.Count)

# Verify balanced Latin-square seeds and execution order across arms
$specA1 = Get-Content -Raw (Join-Path $rootCal 'work\specs\cal_A_01.json') | ConvertFrom-Json
$specAmin1 = Get-Content -Raw (Join-Path $rootCal 'work\specs\cal_AMIN_01.json') | ConvertFrom-Json
$specB1 = Get-Content -Raw (Join-Path $rootCal 'work\specs\cal_B_01.json') | ConvertFrom-Json
$seedsMatchTriplet1 = ($specA1.seed -eq $specAmin1.seed) -and ($specAmin1.seed -eq $specB1.seed)
$orderCorrect = ($specA1.order_position -eq 1) -and ($specAmin1.order_position -eq 2) -and ($specB1.order_position -eq 3)
Record 'S10-cal-seeds-and-order' ($seedsMatchTriplet1 -and $orderCorrect) ('triplet 1 shared seed=' + $specA1.seed + ' balanced orders=(1,2,3)')

# Idempotency: Invoking -Calibrate again must execute 0 new processes and return immediately
$beforeLines = @(Read-Lines $calLedger).Count
$r = Invoke-Driver -Root $rootCal -ArgList @('-Calibrate', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
$afterLines = @(Read-Lines $calLedger).Count
Record 'S10-cal-idempotent' (($r.ExitCode -eq 0) -and ($afterLines -eq $beforeLines) -and ($r.Out -match 'already complete')) 'second -Calibrate is idempotent (0 new runs)'

# Incomplete calibration refusal: if any cell is missing, -Calibrate must fail closed and refuse to produce calibration.json
$rootCalPartial = New-Root 's10_cal_partial'
$prepCalPartial = Prepare-Root $rootCalPartial
$null = New-Approval -Root $rootCalPartial -Gate 'G3' -AllowedBlocks @('PF') -RunlistHash $prepCalPartial.Hash -RunnerHash $stubHash -Scope 'calibration'
$calDirPartial = Join-Path $rootCalPartial 'calibration'
New-Item -ItemType Directory -Force -Path $calDirPartial | Out-Null
$partialLedger = Join-Path $calDirPartial 'calibration_ledger.jsonl'
# Populate only 8 of 9 cells (omit cal_AMIN_03)
$allPlanCells = @('cal_A_01','cal_AMIN_01','cal_B_01','cal_AMIN_02','cal_B_02','cal_A_02','cal_B_03','cal_A_03')
foreach ($cellTid in $allPlanCells) {
    [System.IO.File]::AppendAllText($partialLedger, ((@{ event = 'STARTED'; trial_id = $cellTid } | ConvertTo-Json -Compress) + "`n"))
    [System.IO.File]::AppendAllText($partialLedger, ((@{ event = 'FINISHED'; trial_id = $cellTid; status = 'SUCCESS' } | ConvertTo-Json -Compress) + "`n"))
    $fake = @{ status = 'SUCCESS'; result = @{ throughput = @{ read_ops_per_sec = 1200.0 } } }
    [System.IO.File]::WriteAllText((Join-Path $calDirPartial ($cellTid + '.json')), ($fake | ConvertTo-Json -Compress))
}
# With stub mode set to fail, the missing 9th run will fail
$env:I7_STUB_MODE = 'fail'
$r = Invoke-Driver -Root $rootCalPartial -ArgList @('-Calibrate', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
$calJsonPartial = Join-Path $rootCalPartial 'calibration.json'
Record 'S10-cal-incomplete-refusal' (($r.ExitCode -ne 0) -and (-not (Test-Path -LiteralPath $calJsonPartial)) -and ($r.Out -match 'calibration incomplete')) 'calibration refuses partial completion (no calibration.json produced)'
$env:I7_STUB_MODE = 'ok'

# Restart-safety: Simulate crash after 4 runs in a fresh root
$rootCalCrash = New-Root 's10_cal_crash'
$prepCalCrash = Prepare-Root $rootCalCrash
$null = New-Approval -Root $rootCalCrash -Gate 'G3' -AllowedBlocks @('PF') -RunlistHash $prepCalCrash.Hash -RunnerHash $stubHash -Scope 'calibration'
$calDirCrash = Join-Path $rootCalCrash 'calibration'
New-Item -ItemType Directory -Force -Path $calDirCrash | Out-Null
$crashLedger = Join-Path $calDirCrash 'calibration_ledger.jsonl'
$preArms = @('A', 'A', 'A', 'AMIN')
$preReps = @(1, 2, 3, 1)
for ($idx = 0; $idx -lt 4; $idx++) {
    $a = $preArms[$idx]; $rep = $preReps[$idx]
    $tid = ('cal_{0}_{1:D2}' -f $a, $rep)
    [System.IO.File]::AppendAllText($crashLedger, ((@{ event = 'STARTED'; trial_id = $tid; arm = $a; rep = $rep } | ConvertTo-Json -Compress) + "`n"))
    [System.IO.File]::AppendAllText($crashLedger, ((@{ event = 'FINISHED'; trial_id = $tid; arm = $a; rep = $rep; status = 'SUCCESS' } | ConvertTo-Json -Compress) + "`n"))
    $fakeResult = @{ status = 'SUCCESS'; result = @{ throughput = @{ read_ops_per_sec = 1000.0 } } }
    [System.IO.File]::WriteAllText((Join-Path $calDirCrash ($tid + '.json')), ($fakeResult | ConvertTo-Json -Compress))
}
$r = Invoke-Driver -Root $rootCalCrash -ArgList @('-Calibrate', '-TestMode', '-RunnerOverride', $script:Stub, '-PebbleRunnerOverride', $pebbleArg)
$crashFinished = @(Read-Lines $crashLedger | Where-Object { $_.event -eq 'FINISHED' -and $_.status -eq 'SUCCESS' })
Record 'S10-cal-restart-safe' (($r.ExitCode -eq 0) -and ($crashFinished.Count -eq 9)) ('resumed partial calibration: total success = ' + $crashFinished.Count)

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
