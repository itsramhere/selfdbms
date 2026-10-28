<#
.SYNOPSIS
  Builds I7_G2_REVIEW.zip from the committed sources, the raw test logs and the
  frozen manifest/runlist evidence.

.DESCRIPTION
  Read-only with respect to the repository: everything is staged under %TEMP% and
  written to a single ZIP next to this script. No trial is executed.
#>

[CmdletBinding()]
param(
    [string]$RepoRoot = '',
    [string]$EvidenceDir = ''
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2

if ([string]::IsNullOrWhiteSpace($RepoRoot)) {
    $RepoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..\..\..'))
}
$phaseDir = Join-Path $RepoRoot 'research\experiments\phase_i7'
$results = Join-Path $phaseDir 'I7_WINDOWS_RESULTS'
if ([string]::IsNullOrWhiteSpace($EvidenceDir)) { $EvidenceDir = Join-Path $phaseDir 'evidence\g2' }

$zipPath = Join-Path $phaseDir 'I7_G2_REVIEW.zip'
$stage = Join-Path $env:TEMP ('i7_g2_review_' + (Get-Date).ToUniversalTime().ToString('yyyyMMddHHmmss'))
$utf8 = New-Object System.Text.UTF8Encoding($false)

New-Item -ItemType Directory -Force -Path $stage | Out-Null
function Ensure([string]$p) { if (-not (Test-Path -LiteralPath $p)) { New-Item -ItemType Directory -Force -Path $p | Out-Null } }
function Copy-Into([string]$src, [string]$dstRel) {
    if (-not (Test-Path -LiteralPath $src)) { Write-Warning ('missing: ' + $src); return }
    $dst = Join-Path $stage $dstRel
    Ensure ([System.IO.Path]::GetDirectoryName($dst))
    Copy-Item -LiteralPath $src -Destination $dst -Force
}

# 1. Packet + commands
Copy-Into (Join-Path $phaseDir 'I7_G2_REVIEW_PACKET.md') 'README.md'

$cmd = @'
# Exact commands for the 12-trial preflight (gate G3)
# Run from the repository root in Windows PowerShell. No elevation.

$env:CGO_ENABLED = '0'

# 1. Platform validation + build both runners (no trial)
.\research\experiments\phase_i7\run_i7.ps1 -Check

# 2. Reviewer action: generate the gate-G3 approval template, then fill it in.
.\research\experiments\phase_i7\run_i7.ps1 -Preflight -WriteApprovalTemplate -Gate G3
#    Edit APPROVAL.template.G3.json: set approved=true, approved_by, approved_at.
#    allowed_blocks must include E1, E3, E5, PF for the 12-trial preflight.
#    Rename it to APPROVAL.json in I7_WINDOWS_RESULTS\.

# 3. Inspect (launches nothing)
.\research\experiments\phase_i7\run_i7.ps1 -DryRun -Preflight

# 4. Execute the preregistered 12-trial preflight
.\research\experiments\phase_i7\run_i7.ps1 -Preflight

# 5. Package
.\research\experiments\phase_i7\run_i7.ps1 -Package

# Gate-G4 sequence (NOT authorized yet)
# .\research\experiments\phase_i7\run_i7.ps1 -Calibrate      # G3: 9 runs -> calibration.json
# .\research\experiments\phase_i7\run_i7.ps1 -DryRun -Campaign
# .\research\experiments\phase_i7\run_i7.ps1 -Campaign
# .\research\experiments\phase_i7\run_i7.ps1 -Resume
# .\research\experiments\phase_i7\run_i7.ps1 -Package
'@
[System.IO.File]::WriteAllText((Join-Path $stage 'PRECFLIGHT_COMMANDS.md'), $cmd, $utf8)

# 2. A-min source diff
$patch = Join-Path $stage 'diff\a_min.patch'
Ensure ([System.IO.Path]::GetDirectoryName($patch))
Push-Location -LiteralPath $RepoRoot
try {
    $diff = (& git diff f1a561a..0317d0b -- lsmengine research/experiments/harness/runner.go 2>&1) -join "`n"
}
finally { Pop-Location }
[System.IO.File]::WriteAllText($patch, $diff + "`n", $utf8)

# 3. Sources
Copy-Into (Join-Path $RepoRoot 'lsmengine\manager.go') 'source\lsmengine\manager.go'
Copy-Into (Join-Path $RepoRoot 'lsmengine\compaction.go') 'source\lsmengine\compaction.go'
Copy-Into (Join-Path $RepoRoot 'lsmengine\sstableReader.go') 'source\lsmengine\sstableReader.go'
Copy-Into (Join-Path $RepoRoot 'lsmengine\snapshot.go') 'source\lsmengine\snapshot.go'
Copy-Into (Join-Path $RepoRoot 'lsmengine\approach_a_min_test.go') 'source\lsmengine\approach_a_min_test.go'
Copy-Into (Join-Path $RepoRoot 'lsmengine\flush_gate_test.go') 'source\lsmengine\flush_gate_test.go'
Copy-Into (Join-Path $RepoRoot 'research\experiments\harness\runner.go') 'source\harness\runner.go'
foreach ($f in (Get-ChildItem (Join-Path $phaseDir 'harness2') -File -Filter '*.go')) {
    Copy-Into $f.FullName ('source\harness2\' + $f.Name)
}
foreach ($sub in 'cmd\i7trial', 'cmd\i7stub') {
    foreach ($f in (Get-ChildItem (Join-Path $phaseDir $sub) -File -Filter '*.go')) {
        Copy-Into $f.FullName ('source\' + $sub.Replace('\', '\') + '\' + $f.Name)
    }
}
Copy-Into (Join-Path $phaseDir 'run_i7.ps1') 'source\run_i7.ps1'
Copy-Into (Join-Path $phaseDir 'tests\orchestration_tests.ps1') 'source\tests\orchestration_tests.ps1'
Copy-Into (Join-Path $phaseDir 'tests\build_g2_review_package.ps1') 'source\tests\build_g2_review_package.ps1'
foreach ($f in (Get-ChildItem (Join-Path $phaseDir 'pebble_runner') -File | Where-Object { $_.Extension -ne '.exe' })) {
    Copy-Into $f.FullName ('source\pebble_runner\' + $f.Name)
}

# 4. Test logs
foreach ($f in (Get-ChildItem $EvidenceDir -File -Filter '*.log' | Where-Object { $_.Name -notmatch 'orchestration' })) {
    Copy-Into $f.FullName ('tests\' + $f.Name)
}
$orchLog = Get-ChildItem $EvidenceDir -File -Filter 'orchestration_*.log' | Sort-Object LastWriteTime | Select-Object -Last 1
$orchJson = Get-ChildItem $EvidenceDir -File -Filter 'orchestration_*.json' | Sort-Object LastWriteTime | Select-Object -Last 1
if ($orchLog) { Copy-Into $orchLog.FullName 'tests\orchestration_final.log' }
if ($orchJson) { Copy-Into $orchJson.FullName 'tests\orchestration_final.json' }

# 5. Evidence
Copy-Into (Join-Path $results 'platform_manifest.json') 'evidence\platform_manifest.json'
Copy-Into (Join-Path $results 'campaign_manifest.json') 'evidence\campaign_manifest.json'
Copy-Into (Join-Path $results 'runlist.csv') 'evidence\runlist.csv'

$rows = Import-Csv (Join-Path $results 'runlist.csv')
$summary = New-Object System.Text.StringBuilder
[void]$summary.AppendLine(('rows: ' + $rows.Count))
[void]$summary.AppendLine(('first: ' + $rows[0].runlist_position + ' ' + $rows[0].trial_id + ' (' + $rows[0].block + '/' + $rows[0].cell + ')'))
[void]$summary.AppendLine(('last:  ' + $rows[-1].runlist_position + ' ' + $rows[-1].trial_id + ' (' + $rows[-1].block + '/' + $rows[-1].cell + ')'))
[void]$summary.AppendLine('block counts:')
foreach ($g in ($rows | Group-Object block | Sort-Object Name)) { [void]$summary.AppendLine(('  ' + $g.Name + ' = ' + $g.Count)) }
[void]$summary.AppendLine(('runlist sha256: ' + (Get-FileHash (Join-Path $results 'runlist.csv') -Algorithm SHA256).Hash.ToLowerInvariant()))
[System.IO.File]::WriteAllText((Join-Path $stage 'evidence\runlist_summary.txt'), $summary.ToString(), $utf8)

# 6. Checksums
$files = @(Get-ChildItem $stage -Recurse -File | Sort-Object FullName)
$sb = New-Object System.Text.StringBuilder
foreach ($f in $files) {
    $rel = $f.FullName.Substring($stage.Length).TrimStart('\')
    [void]$sb.AppendLine((Get-FileHash $f.FullName -Algorithm SHA256).Hash.ToLowerInvariant() + '  ' + $rel)
}
[System.IO.File]::WriteAllText((Join-Path $stage 'CHECKSUMS.sha256'), $sb.ToString(), $utf8)

# 7. ZIP
if (Test-Path -LiteralPath $zipPath) { Remove-Item -LiteralPath $zipPath -Force }
Add-Type -AssemblyName System.IO.Compression.FileSystem
[System.IO.Compression.ZipFile]::CreateFromDirectory($stage, $zipPath, [System.IO.Compression.CompressionLevel]::Optimal, $false)
$hash = (Get-FileHash $zipPath -Algorithm SHA256).Hash.ToLowerInvariant()
[System.IO.File]::WriteAllText($zipPath + '.sha256', ($hash + '  I7_G2_REVIEW.zip'), $utf8)

Write-Host ''
Write-Host ('ZIP:     ' + $zipPath)
Write-Host ('SHA-256: ' + $hash)
Write-Host ('files:   ' + $files.Count)
Write-Host ('staged:  ' + $stage)
