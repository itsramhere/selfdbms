<#
.SYNOPSIS
  Phase I7 Windows-native campaign driver (G2 revision).

.DESCRIPTION
  Runs the preregistered I7 trial inventory on native Windows (PowerShell 5.1 or
  7.x, no WSL, no POSIX tools, no administrator rights).

  Modes (exactly one required):
    -Check          Validate host, storage, toolchain, build both runners, measure
                    timers, record affinity, hash frozen artifacts. Writes
                    platform_manifest.json.
    -Calibrate      Gate G3. Nine short closed-loop runs -> calibration.json (C_ref).
    -Preflight      Gate G3. Engineering preflight, 12 trials.
    -Campaign       Gate G4. Preregistered confirmatory trials.
    -Resume         Gate G4. Continue an interrupted campaign.
    -Diagnostics    Gate G5. D1/D2 profiling and instrumented runs (not implemented
                    in this build; the mode exists so the gate cannot be confused).
    -Package        Write summary.json + checksums.sha256 + ZIP.

  Add -DryRun to any executing mode to generate and validate specs without
  launching a trial process.

  Safety:
    * never requires or requests elevation;
    * never changes power settings, Defender, partitions or filesystem config;
    * never writes below the frozen campaign data directories (and refuses a
      results root inside them);
    * enforces the planned trial count, the hard ceiling and the persisted
      replacement budget.

.NOTES
  Design: research/experiments/phase_i7/12_WINDOWS_EXECUTION_AMENDMENT.md
  Inventory: research/experiments/phase_i7/06_TRIAL_INVENTORY.md
  G2 packet: research/experiments/phase_i7/I7_G2_REVIEW_PACKET.md
#>

[CmdletBinding()]
param(
    [switch]$Check,
    [switch]$Calibrate,
    [switch]$Preflight,
    [switch]$Campaign,
    [switch]$Resume,
    [switch]$Diagnostics,
    [switch]$Package,

    [switch]$DryRun,
    [switch]$RefreshRunlist,
    [switch]$RefreezeManifest,
    [switch]$WriteApprovalTemplate,

    [string]$Root = '',
    [string]$Affinity = '',
    [string]$ApprovalFile = '',
    [string]$Gate = '',
    [string]$Scope = '',
    [string]$CalibrationFile = '',
    [string[]]$Blocks = @(),
    [switch]$AllowPartial,
    [switch]$TestMode,
    [string]$RunnerOverride = '',
    [string]$PebbleRunnerOverride = '',

    [int]$MaxTrials = 0,
    [int]$TrialTimeoutSec = 600,
    [int]$MinFreeGB = 20,
    [int]$MaxCampaignMinutes = 480,
    [int]$MaxFailures = 10,
    [int]$MaxLeaseFailures = 2,
    [switch]$KeepData
)

Set-StrictMode -Version 2
$ErrorActionPreference = 'Stop'

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------
$script:I7 = [ordered]@{
    CampaignId      = 'I7_STRENGTHENING_V1'
    ProtocolVersion = '1.2.0-windows'
    Seed            = '20261101'
    # R1.2e budget model: 12 cells x N in [8,10] triplets x 3 arms (300..372
    # planned, drift anchors included) + replacement budget 16 -> worst case
    # 388 <= 400 strict global ceiling.
    PlannedTrials   = 366   # legacy 366-row inventory; execution authority REVOKED (see Assert-ExecutionRevoked)
    HardCeiling     = 400
    ReplacementCap  = 16
    Platform        = 'windows-native-ntfs'
    # Model-authority record written into the campaign manifest.
    ModelAuthority  = 'legacy_inventory_366_REVOKED_by_R1_2e_execution_fails_closed'
}

$script:ArmPerms = @(
    @('A', 'AMIN', 'B'), @('A', 'B', 'AMIN'), @('AMIN', 'A', 'B'),
    @('AMIN', 'B', 'A'), @('B', 'A', 'AMIN'), @('B', 'AMIN', 'A')
)

$script:Utf8NoBom = New-Object System.Text.UTF8Encoding($false)
$script:Failures = New-Object System.Collections.ArrayList

# Paced write rate for E3 equal-work, fixed at G2 from frozen A-arm medians.
$script:PacedWrites = @{ 'r01' = 235; 'r04' = 210 }

# Open-loop level multipliers for E4.
$script:OpenLoopLevels = @{ 'L1' = 0.30; 'L2' = 0.60; 'L3' = 0.90 }

if ([string]::IsNullOrWhiteSpace($Root)) {
    if ([string]::IsNullOrWhiteSpace($PSScriptRoot)) {
        $Root = Join-Path (Get-Location).Path 'research\experiments\phase_i7\I7_WINDOWS_RESULTS'
    }
    else {
        $Root = Join-Path $PSScriptRoot 'I7_WINDOWS_RESULTS'
    }
}
$Root = [System.IO.Path]::GetFullPath($Root)
$script:RepoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..\..'))

$script:FrozenRoots = @(
    [System.IO.Path]::GetFullPath((Join-Path $script:RepoRoot 'research\experiments\data\campaign_wsl2_ext4_restart_v1')),
    [System.IO.Path]::GetFullPath((Join-Path $script:RepoRoot 'research\experiments\data\targeted_validation_v1'))
)

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
function Write-Section([string]$Text) {
    Write-Host ''
    Write-Host ('=' * 78) -ForegroundColor DarkCyan
    Write-Host $Text -ForegroundColor Cyan
    Write-Host ('=' * 78) -ForegroundColor DarkCyan
}
function Write-Ok([string]$Text) { Write-Host ('  [ OK ] ' + $Text) -ForegroundColor Green }
function Write-Warn2([string]$Text) { Write-Host ('  [WARN] ' + $Text) -ForegroundColor Yellow }
function Write-Fail([string]$Text) {
    Write-Host ('  [FAIL] ' + $Text) -ForegroundColor Red
    [void]$script:Failures.Add($Text)
}
function Write-Info([string]$Text) { Write-Host ('  ' + $Text) }

function Get-UtcStamp { (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ss.fffZ') }

function Get-Sha256([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return '' }
    (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Get-TextSha256([string]$Text) {
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        $bytes = $script:Utf8NoBom.GetBytes($Text)
        ($sha.ComputeHash($bytes) | ForEach-Object { $_.ToString('x2') }) -join ''
    }
    finally { $sha.Dispose() }
}

# Safe property access for operator-authored JSON and older manifests, so a
# missing optional field cannot turn into a StrictMode crash.
function PropOr($Obj, [string]$Name, $Default) {
    if ($null -eq $Obj) { return $Default }
    $p = $Obj.PSObject.Properties[$Name]
    if ($null -eq $p) { return $Default }
    if ($null -eq $p.Value) { return $Default }
    return $p.Value
}

function Write-JsonFile([string]$Path, $Object) {
    $dir = [System.IO.Path]::GetDirectoryName($Path)
    if ($dir -and -not (Test-Path -LiteralPath $dir)) {
        New-Item -ItemType Directory -Force -Path $dir | Out-Null
    }
    [System.IO.File]::WriteAllText($Path, ($Object | ConvertTo-Json -Depth 16), $script:Utf8NoBom)
}

function Read-JsonFile([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    (Get-Content -LiteralPath $Path -Raw) | ConvertFrom-Json
}

function Add-JsonLine([string]$Path, $Object) {
    $dir = [System.IO.Path]::GetDirectoryName($Path)
    if ($dir -and -not (Test-Path -LiteralPath $dir)) {
        New-Item -ItemType Directory -Force -Path $dir | Out-Null
    }
    $line = ($Object | ConvertTo-Json -Depth 16 -Compress)
    [System.IO.File]::AppendAllText($Path, $line + "`n", $script:Utf8NoBom)
}

function Read-JsonLines([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return @() }
    $out = New-Object System.Collections.ArrayList
    foreach ($line in [System.IO.File]::ReadAllLines($Path)) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        try { [void]$out.Add(($line | ConvertFrom-Json)) } catch { }
    }
    return $out.ToArray()
}

function Quote-Arg([string]$Value) {
    if ($null -eq $Value) { return '""' }
    '"' + ($Value -replace '"', '\"') + '"'
}

function Get-PopCount([uint64]$Mask) {
    $n = 0
    while ($Mask -ne 0) {
        if (($Mask -band 1) -eq 1) { $n++ }
        $Mask = $Mask -shr 1
    }
    return $n
}

function Get-CurrentAffinityMask {
    $p = [System.Diagnostics.Process]::GetCurrentProcess()
    $v = $p.ProcessorAffinity.ToInt64()
    if ($v -lt 0) { return [uint64]0 }
    return [uint64]$v
}

function Set-CurrentAffinityMask([uint64]$Mask) {
    $p = [System.Diagnostics.Process]::GetCurrentProcess()
    $p.ProcessorAffinity = [IntPtr]([int64]$Mask)
}

function Ensure-Dir([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) {
        New-Item -ItemType Directory -Force -Path $Path | Out-Null
    }
    [System.IO.Path]::GetFullPath($Path)
}

function Test-UnderFrozenRoot([string]$Path) {
    $full = [System.IO.Path]::GetFullPath($Path)
    foreach ($f in $script:FrozenRoots) {
        if ($full.StartsWith($f, [System.StringComparison]::OrdinalIgnoreCase)) { return $true }
    }
    return $false
}

function Get-PathLayout {
    [ordered]@{
        Root           = $Root
        Bin            = Ensure-Dir (Join-Path $Root 'bin')
        Trials         = Ensure-Dir (Join-Path $Root 'trials')
        Attempts       = Ensure-Dir (Join-Path $Root 'trials\attempts')
        Specs          = Ensure-Dir (Join-Path $Root 'work\specs')
        Logs           = Ensure-Dir (Join-Path $Root 'logs')
        Profiles       = Ensure-Dir (Join-Path $Root 'profiles')
        Data           = Ensure-Dir (Join-Path $Root 'work\data')
        Calibration    = Ensure-Dir (Join-Path $Root 'calibration')
        Runlist        = Join-Path $Root 'runlist.csv'
        Manifest       = Join-Path $Root 'campaign_manifest.json'
        Platform       = Join-Path $Root 'platform_manifest.json'
        CalibrationOut = Join-Path $Root 'calibration.json'
        Results        = Join-Path $Root 'results.jsonl'
        FailuresF      = Join-Path $Root 'failures.jsonl'
        Summary        = Join-Path $Root 'summary.json'
        Checksums      = Join-Path $Root 'checksums.sha256'
        Zip            = Join-Path $Root 'I7_WINDOWS_RESULTS.zip'
        Runner         = Join-Path $Root 'bin\i7trial.exe'
        PebbleRunner   = Join-Path $Root 'bin\pebble_runner.exe'
        Approval       = if ([string]::IsNullOrWhiteSpace($ApprovalFile)) { Join-Path $Root 'APPROVAL.json' } else { [System.IO.Path]::GetFullPath($ApprovalFile) }
    }
}

# ---------------------------------------------------------------------------
# Platform probing
# ---------------------------------------------------------------------------
function Get-WorktreeFingerprint([string]$Rev) {
    $diff = ''
    $untracked = ''
    try {
        Push-Location -LiteralPath $script:RepoRoot
        $diff = ((& git diff --binary HEAD 2>$null) -join "`n")
        $untracked = ((& git ls-files -o --exclude-standard 2>$null) -join "`n")
    }
    catch { }
    finally { Pop-Location }
    return (Get-TextSha256 ($Rev + "`n" + (Get-TextSha256 $diff) + "`n" + (Get-TextSha256 $untracked)))
}

function Get-PlatformInfo {
    $os = Get-CimInstance -ClassName Win32_OperatingSystem
    $cs = Get-CimInstance -ClassName Win32_ComputerSystem
    $cpu = Get-CimInstance -ClassName Win32_Processor | Select-Object -First 1

    $driveLetter = ([System.IO.Path]::GetPathRoot($Root)).TrimEnd('\').TrimEnd(':')
    $volume = $null; $pd = $null
    $nvmeVerified = $false
    $busType = 'unknown'; $mediaType = 'unknown'; $diskModel = 'unknown'; $diskSerial = ''

    try {
        $volume = Get-Volume -DriveLetter $driveLetter -ErrorAction Stop
        $part = Get-Partition -DriveLetter $driveLetter -ErrorAction Stop | Select-Object -First 1
        $disk = Get-Disk -Number $part.DiskNumber -ErrorAction Stop
        try {
            $pd = Get-PhysicalDisk -DeviceNumber $part.DiskNumber -ErrorAction Stop
            $busType = [string]$pd.BusType
            $mediaType = [string]$pd.MediaType
            $diskModel = [string]$pd.FriendlyName
            $diskSerial = [string]$pd.SerialNumber
        }
        catch {
            $wmi = Get-CimInstance -ClassName Win32_DiskDrive -ErrorAction Stop |
                Where-Object { $_.Index -eq $disk.Number } | Select-Object -First 1
            if ($wmi) {
                $diskModel = [string]$wmi.Model
                $busType = if ($wmi.Model -match 'NVMe') { 'NVMe' } else { [string]$wmi.InterfaceType }
                $mediaType = [string]$wmi.MediaType
            }
        }
        if ($busType -match 'NVMe' -or $diskModel -match 'NVMe') { $nvmeVerified = $true }
    }
    catch { Write-Warn2 ('storage topology query failed: ' + $_.Exception.Message) }

    $fs = 'unknown'
    try {
        $drv = New-Object System.IO.DriveInfo($driveLetter + ':\')
        $fs = [string]$drv.DriveFormat
    }
    catch { }

    $power = 'unknown'
    try { $power = (powercfg /getactivescheme 2>$null) -join ' ' } catch { }

    $defender = $null
    try {
        $mp = Get-MpComputerStatus -ErrorAction Stop
        $defender = [bool]$mp.RealTimeProtectionEnabled
    }
    catch { $defender = $null }

    $goVersion = ''; $goEnv = @{}
    try {
        $goVersion = (& go version 2>$null)
        foreach ($k in @('GOOS', 'GOARCH', 'CGO_ENABLED', 'GOMODCACHE', 'GOTOOLCHAIN', 'GOAMD64', 'GOVERSION')) {
            $goEnv[$k] = (& go env $k 2>$null)
        }
    }
    catch { }

    $gitRev = ''; $gitDirty = $true
    try {
        Push-Location -LiteralPath $script:RepoRoot
        $gitRev = (& git rev-parse HEAD 2>$null)
        $status = (& git status --porcelain 2>$null)
        $gitDirty = -not [string]::IsNullOrWhiteSpace(($status -join ''))
    }
    catch { }
    finally { Pop-Location }
    $fingerprint = Get-WorktreeFingerprint $gitRev

    $freeBytes = 0
    if ($volume) { $freeBytes = [int64]$volume.SizeRemaining }

    $bootUtc = ''
    try {
        $bootUtc = ([System.Management.ManagementDateTimeConverter]::ToDateTime($os.LastBootUpTime)).ToUniversalTime().ToString('o')
    }
    catch { $bootUtc = [string]$os.LastBootUpTime }

    $affinityActual = Get-CurrentAffinityMask

    [ordered]@{
        schema                  = 'i7.platform.v1'
        captured_utc            = Get-UtcStamp
        host                    = $cs.Name
        os                      = $os.Caption
        os_version              = $os.Version
        os_build                = $os.BuildNumber
        os_architecture         = $os.OSArchitecture
        powershell_version      = $PSVersionTable.PSVersion.ToString()
        cpu_model               = ([string]$cpu.Name).Trim()
        physical_cores          = [int]$cpu.NumberOfCores
        logical_cores           = [int]$cpu.NumberOfLogicalProcessors
        total_ram_bytes         = [int64]$cs.TotalPhysicalMemory
        filesystem              = $fs
        volume_name             = if ($volume) { [string]$volume.FileSystemLabel } else { '' }
        volume_size_bytes       = if ($volume) { [int64]$volume.Size } else { 0 }
        volume_free_bytes       = $freeBytes
        disk_model              = $diskModel
        disk_serial             = $diskSerial
        disk_bus_type           = $busType
        disk_media_type         = $mediaType
        nvme_placement_verified = $nvmeVerified
        affinity_requested      = $Affinity
        affinity_actual         = ('0x{0:x}' -f $affinityActual)
        gomaxprocs_env          = $env:GOMAXPROCS
        power_scheme            = $power
        defender_realtime       = $defender
        boot_time_utc           = $bootUtc
        go_version              = $goVersion
        go_env                  = $goEnv
        git_revision            = $gitRev
        git_dirty               = $gitDirty
        worktree_fingerprint    = $fingerprint
        repo_root               = $script:RepoRoot
        results_root            = $Root
        planned_trials          = $script:I7.PlannedTrials
        hard_ceiling            = $script:I7.HardCeiling
        protocol_version        = $script:I7.ProtocolVersion
        platform_id             = $script:I7.Platform
    }
}

function Test-StorageThroughput($Layout) {
    $probe = Join-Path $Layout.Data '_storage_probe.bin'
    $block = New-Object byte[] (1MB)
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    $fs = [System.IO.File]::Create($probe)
    try {
        for ($i = 0; $i -lt 32; $i++) { $fs.Write($block, 0, $block.Length) }
        $fs.Flush($true)
        $sw.Stop()
    }
    finally { $fs.Dispose() }
    $writeMs = $sw.Elapsed.TotalMilliseconds
    Remove-Item -LiteralPath $probe -Force -ErrorAction SilentlyContinue
    [ordered]@{
        bytes             = 32 * 1MB
        buffered_write_ms = [math]::Round($writeMs, 3)
        buffered_MBps     = if ($writeMs -gt 0) { [math]::Round((32 / ($writeMs / 1000.0)), 1) } else { 0 }
        flushed_to_disk   = $true
    }
}

# ---------------------------------------------------------------------------
# Runners
# ---------------------------------------------------------------------------
function Build-Runner([string]$Package, [string]$OutPath) {
    $env:CGO_ENABLED = '0'
    Push-Location -LiteralPath $script:RepoRoot
    try {
        $out = (& go build -trimpath -buildvcs=false -o $OutPath $Package 2>&1)
        $code = $LASTEXITCODE
    }
    finally { Pop-Location }
    if ($code -ne 0) {
        Write-Warn2 ("go build failed for {0} (exit {1}): {2}" -f $Package, $code, ($out -join ' '))
        return $null
    }
    if (-not (Test-Path -LiteralPath $OutPath)) {
        Write-Warn2 ("build produced no binary for " + $Package)
        return $null
    }
    $hash = Get-Sha256 $OutPath
    Write-Ok ("built {0}  sha256 {1}" -f $OutPath, $hash)
    [ordered]@{ path = $OutPath; sha256 = $hash; package = $Package; built_utc = Get-UtcStamp }
}

function Build-PebbleRunner($Layout) {
    if (-not [string]::IsNullOrWhiteSpace($PebbleRunnerOverride)) {
        if (-not (Test-Path -LiteralPath $PebbleRunnerOverride)) {
            Write-Warn2 ('pebble runner override not found: ' + $PebbleRunnerOverride)
            return $null
        }
        $hash = Get-Sha256 $PebbleRunnerOverride
        Write-Ok ('pebble runner (override): ' + $PebbleRunnerOverride)
        return [ordered]@{ path = [System.IO.Path]::GetFullPath($PebbleRunnerOverride); sha256 = $hash; package = 'override'; built_utc = Get-UtcStamp }
    }
    $pkgDir = Join-Path $script:RepoRoot 'research\experiments\phase_i7\pebble_runner'
    if (-not (Test-Path -LiteralPath (Join-Path $pkgDir 'go.mod'))) {
        Write-Warn2 'pebble_runner module not present; E7 will be blocked'
        return $null
    }
    $env:CGO_ENABLED = '0'
    Push-Location -LiteralPath $pkgDir
    try {
        $out = (& go build -trimpath -o $Layout.PebbleRunner . 2>&1)
        $code = $LASTEXITCODE
    }
    finally { Pop-Location }
    if ($code -ne 0 -or -not (Test-Path -LiteralPath $Layout.PebbleRunner)) {
        Write-Warn2 ('pebble_runner build failed: ' + ($out -join ' '))
        return $null
    }
    $hash = Get-Sha256 $Layout.PebbleRunner
    Write-Ok ('built pebble_runner  sha256 ' + $hash)
    return [ordered]@{ path = $Layout.PebbleRunner; sha256 = $hash; package = 'research/experiments/phase_i7/pebble_runner'; built_utc = Get-UtcStamp }
}

function Get-BlockRunner($Layout, [string]$Block) {
    if ($Block -eq 'E7') { return $Layout.PebbleRunner }
    return $Layout.Runner
}

# ---------------------------------------------------------------------------
# Runlist
# ---------------------------------------------------------------------------
function Add-PhaseRows {
    param(
        [System.Collections.ArrayList]$Rows,
        [string]$Block,
        [string]$Cell,
        [string]$Phase,
        [string]$Workload,
        [int]$Triplets,
        [hashtable]$Spec,
        [string]$RunnerSupport,
        [string]$Note
    )
    for ($t = 1; $t -le $Triplets; $t++) {
        $perm = $script:ArmPerms[($t - 1) % $script:ArmPerms.Count]
        $tripletKey = ('{0}|{1}|{2:D2}' -f $Block, $Cell, $t)
        for ($o = 0; $o -lt $perm.Count; $o++) {
            $arm = $perm[$o]
            $tid = ('i7_{0}_{1}_t{2:D2}_{3}' -f $Block, $Cell, $t, $arm)
            [void]$Rows.Add([pscustomobject][ordered]@{
                trial_id                       = $tid
                block                          = $Block
                cell                           = $Cell
                phase                          = $Phase
                triplet                        = $t
                triplet_key                    = $tripletKey
                arm                            = $arm
                order_in_triplet               = $o + 1
                workload_id                    = $Workload
                reader_concurrency             = [int]$Spec.reader_concurrency
                writer_concurrency             = [int]$Spec.writer_concurrency
                measurement_duration_ms        = [int]$Spec.measurement_duration_ms
                warmup_duration_ms             = [int]$Spec.warmup_duration_ms
                cooldown_duration_ms           = [int]$Spec.cooldown_duration_ms
                keyspace_cardinality           = [int]$Spec.keyspace_cardinality
                initial_sstable_count          = [int]$Spec.initial_sstable_count
                initial_keys_per_sstable       = [int]$Spec.initial_keys_per_sstable
                target_read_hit_ratio          = [double]$Spec.target_read_hit_ratio
                memtable_threshold_bytes       = [int64]$Spec.memtable_threshold_bytes
                flush_trigger_interval_ms      = [int]$Spec.flush_trigger_interval_ms
                compaction_trigger_interval_ms = [int]$Spec.compaction_trigger_interval_ms
                wal_sync                       = $true
                read_distribution              = 'uniform'
                expected_reader_set_size       = [int]$Spec.initial_sstable_count
                publication_events             = if ($Spec.ContainsKey('publication_events')) { [int]$Spec.publication_events } else { 0 }
                publication_mode               = if ($Spec.ContainsKey('publication_mode')) { [string]$Spec.publication_mode } else { '' }
                paced_write_rate_per_sec       = if ($Spec.ContainsKey('paced_write_rate_per_sec')) { [double]$Spec.paced_write_rate_per_sec } else { 0 }
                open_loop_level                = if ($Spec.ContainsKey('open_loop_level')) { [string]$Spec.open_loop_level } else { '' }
                runner_support                 = $RunnerSupport
                note                           = $Note
            })
        }
    }
}

function New-I7Runlist {
    $rows = New-Object System.Collections.ArrayList

    foreach ($r in 1, 8) {
        Add-PhaseRows -Rows $rows -Block 'E1' -Cell ('r{0:D2}' -f $r) -Phase 'confirmatory' `
            -Workload 'WORKLOAD_B1_NATURAL_WRITE_PROGRESS' -Triplets 12 -RunnerSupport 'ready' `
            -Note 'starvation decomposition' -Spec @{
            reader_concurrency = $r; writer_concurrency = 2
            measurement_duration_ms = 10000; warmup_duration_ms = 2000; cooldown_duration_ms = 200
            keyspace_cardinality = 10000; initial_sstable_count = 4; initial_keys_per_sstable = 2500
            target_read_hit_ratio = 0.95; memtable_threshold_bytes = 1048576
            flush_trigger_interval_ms = 0; compaction_trigger_interval_ms = 0
        }
    }

    foreach ($mode in 'none', 'physical') {
        Add-PhaseRows -Rows $rows -Block 'E2' -Cell ('pub_' + $mode) -Phase 'confirmatory' `
            -Workload 'WORKLOAD_B2_CONTROLLED_FLUSH_INTERFERENCE' -Triplets 10 -RunnerSupport 'ready' `
            -Note 'publication-only driver (H-07)' -Spec @{
            reader_concurrency = 4; writer_concurrency = 0
            measurement_duration_ms = 10000; warmup_duration_ms = 2000; cooldown_duration_ms = 200
            keyspace_cardinality = 10000; initial_sstable_count = 4; initial_keys_per_sstable = 2500
            target_read_hit_ratio = 0.95; memtable_threshold_bytes = 67108864
            flush_trigger_interval_ms = 0; compaction_trigger_interval_ms = 0
            publication_events = 20; publication_mode = $mode
        }
    }

    foreach ($r in 1, 4) {
        Add-PhaseRows -Rows $rows -Block 'E3' -Cell ('free_r{0:D2}' -f $r) -Phase 'confirmatory' `
            -Workload 'WORKLOAD_D_READER_CHURN' -Triplets 8 -RunnerSupport 'ready' `
            -Note 'free-work churn (2 unpaced writers)' -Spec @{
            reader_concurrency = $r; writer_concurrency = 2
            measurement_duration_ms = 10000; warmup_duration_ms = 2000; cooldown_duration_ms = 200
            keyspace_cardinality = 10000; initial_sstable_count = 4; initial_keys_per_sstable = 2500
            target_read_hit_ratio = 0.95; memtable_threshold_bytes = 16384
            flush_trigger_interval_ms = 0; compaction_trigger_interval_ms = 1500
        }
        $rkey = 'r{0:D2}' -f $r
        Add-PhaseRows -Rows $rows -Block 'E3' -Cell ('equal_' + $rkey) -Phase 'confirmatory' `
            -Workload 'WORKLOAD_D_READER_CHURN' -Triplets 8 -RunnerSupport 'ready' `
            -Note ('equal-work paced writer at ' + $script:PacedWrites[$rkey] + ' writes/s (H-08)') -Spec @{
            reader_concurrency = $r; writer_concurrency = 1
            measurement_duration_ms = 10000; warmup_duration_ms = 2000; cooldown_duration_ms = 200
            keyspace_cardinality = 10000; initial_sstable_count = 4; initial_keys_per_sstable = 2500
            target_read_hit_ratio = 0.95; memtable_threshold_bytes = 16384
            flush_trigger_interval_ms = 0; compaction_trigger_interval_ms = 1500
            paced_write_rate_per_sec = [double]$script:PacedWrites[$rkey]
        }
    }

    foreach ($lvl in 'L1', 'L2', 'L3') {
        Add-PhaseRows -Rows $rows -Block 'E4' -Cell $lvl -Phase 'confirmatory' `
            -Workload 'WORKLOAD_A_READ_ONLY' -Triplets 6 -RunnerSupport 'ready' `
            -Note 'open-loop exponential schedule (H-05); rate = level x C_ref from calibration.json' -Spec @{
            reader_concurrency = 16; writer_concurrency = 0
            measurement_duration_ms = 10000; warmup_duration_ms = 2000; cooldown_duration_ms = 200
            keyspace_cardinality = 10000; initial_sstable_count = 4; initial_keys_per_sstable = 2500
            target_read_hit_ratio = 0.95; memtable_threshold_bytes = 67108864
            flush_trigger_interval_ms = 0; compaction_trigger_interval_ms = 0
            publication_events = 20; publication_mode = 'physical'
            open_loop_level = $lvl
        }
    }

    Add-PhaseRows -Rows $rows -Block 'E5' -Cell 'hit0' -Phase 'confirmatory' `
        -Workload 'WORKLOAD_B1_NATURAL_WRITE_PROGRESS' -Triplets 6 -RunnerSupport 'ready' `
        -Note 'all-miss read path (TargetReadHitRatio=0)' -Spec @{
        reader_concurrency = 8; writer_concurrency = 2
        measurement_duration_ms = 10000; warmup_duration_ms = 2000; cooldown_duration_ms = 200
        keyspace_cardinality = 10000; initial_sstable_count = 4; initial_keys_per_sstable = 2500
        target_read_hit_ratio = 0.0; memtable_threshold_bytes = 1048576
        flush_trigger_interval_ms = 0; compaction_trigger_interval_ms = 0
    }

    $shapeSpecs = @{
        Y1 = @{ reader_concurrency = 8; writer_concurrency = 0; memtable_threshold_bytes = 67108864; compaction_trigger_interval_ms = 0 }
        Y2 = @{ reader_concurrency = 8; writer_concurrency = 2; memtable_threshold_bytes = 67108864; compaction_trigger_interval_ms = 0 }
        Y3 = @{ reader_concurrency = 8; writer_concurrency = 2; memtable_threshold_bytes = 16384; compaction_trigger_interval_ms = 1500 }
    }
    foreach ($shape in 'Y1', 'Y2', 'Y3') {
        $s = $shapeSpecs[$shape]
        for ($rep = 1; $rep -le 8; $rep++) {
            [void]$rows.Add([pscustomobject][ordered]@{
                trial_id = ('i7_E7_{0}_t{1:D2}_PEBBLE' -f $shape, $rep); block = 'E7'; cell = $shape
                phase = 'confirmatory'; triplet = $rep; triplet_key = ('E7|{0}|{1:D2}' -f $shape, $rep)
                arm = 'PEBBLE'; order_in_triplet = 1
                workload_id = 'WORKLOAD_B1_NATURAL_WRITE_PROGRESS'
                reader_concurrency = [int]$s.reader_concurrency; writer_concurrency = [int]$s.writer_concurrency
                measurement_duration_ms = 10000; warmup_duration_ms = 2000; cooldown_duration_ms = 200
                keyspace_cardinality = 10000; initial_sstable_count = 4; initial_keys_per_sstable = 2500
                target_read_hit_ratio = 0.95; memtable_threshold_bytes = [int64]$s.memtable_threshold_bytes
                flush_trigger_interval_ms = 0; compaction_trigger_interval_ms = [int]$s.compaction_trigger_interval_ms
                wal_sync = $true; read_distribution = 'uniform'; expected_reader_set_size = 4
                publication_events = 0; publication_mode = ''; paced_write_rate_per_sec = 0; open_loop_level = ''
                runner_support = 'ready'; note = 'external anchor; descriptive only; executed by pebble_runner'
            })
        }
    }

    foreach ($when in 'start', 'end') {
        Add-PhaseRows -Rows $rows -Block 'D3' -Cell $when -Phase 'confirmatory' `
            -Workload 'WORKLOAD_B1_NATURAL_WRITE_PROGRESS' -Triplets 1 -RunnerSupport 'ready' `
            -Note 'drift reference; anchored first/last; excluded from confirmatory contrasts' -Spec @{
            reader_concurrency = 8; writer_concurrency = 2
            measurement_duration_ms = 10000; warmup_duration_ms = 2000; cooldown_duration_ms = 200
            keyspace_cardinality = 10000; initial_sstable_count = 4; initial_keys_per_sstable = 2500
            target_read_hit_ratio = 0.95; memtable_threshold_bytes = 1048576
            flush_trigger_interval_ms = 0; compaction_trigger_interval_ms = 0
        }
    }

    $pfSpecs = @(
        @{ B = 'E1'; C = 'pf_r08'; W = 'WORKLOAD_B1_NATURAL_WRITE_PROGRESS'; S = @{
                reader_concurrency = 8; writer_concurrency = 2; measurement_duration_ms = 1500; warmup_duration_ms = 300
                cooldown_duration_ms = 100; keyspace_cardinality = 10000; initial_sstable_count = 4
                initial_keys_per_sstable = 2500; target_read_hit_ratio = 0.95; memtable_threshold_bytes = 1048576
                flush_trigger_interval_ms = 0; compaction_trigger_interval_ms = 0 } }
        @{ B = 'E1'; C = 'pf_r01'; W = 'WORKLOAD_B1_NATURAL_WRITE_PROGRESS'; S = @{
                reader_concurrency = 1; writer_concurrency = 2; measurement_duration_ms = 1500; warmup_duration_ms = 300
                cooldown_duration_ms = 100; keyspace_cardinality = 10000; initial_sstable_count = 4
                initial_keys_per_sstable = 2500; target_read_hit_ratio = 0.95; memtable_threshold_bytes = 1048576
                flush_trigger_interval_ms = 0; compaction_trigger_interval_ms = 0 } }
        @{ B = 'E3'; C = 'pf_free_r04'; W = 'WORKLOAD_D_READER_CHURN'; S = @{
                reader_concurrency = 4; writer_concurrency = 2; measurement_duration_ms = 1500; warmup_duration_ms = 300
                cooldown_duration_ms = 100; keyspace_cardinality = 10000; initial_sstable_count = 4
                initial_keys_per_sstable = 2500; target_read_hit_ratio = 0.95; memtable_threshold_bytes = 16384
                flush_trigger_interval_ms = 0; compaction_trigger_interval_ms = 1500 } }
        @{ B = 'E5'; C = 'pf_hit0'; W = 'WORKLOAD_B1_NATURAL_WRITE_PROGRESS'; S = @{
                reader_concurrency = 8; writer_concurrency = 2; measurement_duration_ms = 1500; warmup_duration_ms = 300
                cooldown_duration_ms = 100; keyspace_cardinality = 10000; initial_sstable_count = 4
                initial_keys_per_sstable = 2500; target_read_hit_ratio = 0.0; memtable_threshold_bytes = 1048576
                flush_trigger_interval_ms = 0; compaction_trigger_interval_ms = 0 } }
    )
    foreach ($pf in $pfSpecs) {
        Add-PhaseRows -Rows $rows -Block 'PF' -Cell ('{0}_{1}' -f $pf.B, $pf.C) -Phase 'preflight' `
            -Workload $pf.W -Triplets 1 -RunnerSupport 'ready' -Note 'preflight (engineering only)' -Spec $pf.S
    }

    foreach ($arm in 'A', 'AMIN', 'B') {
        for ($i = 1; $i -le 3; $i++) {
            [void]$rows.Add([pscustomobject][ordered]@{
                trial_id = ('i7_D1_profile_{0}_r{1:D2}' -f $arm, $i); block = 'D1'; cell = 'profile'; phase = 'diagnostic'
                triplet = $i; triplet_key = ('D1|profile|{0:D2}' -f $i); arm = $arm; order_in_triplet = 1
                workload_id = 'WORKLOAD_D_READER_CHURN'; reader_concurrency = 4; writer_concurrency = 2
                measurement_duration_ms = 5000; warmup_duration_ms = 1000; cooldown_duration_ms = 200
                keyspace_cardinality = 10000; initial_sstable_count = 4; initial_keys_per_sstable = 2500
                target_read_hit_ratio = 0.95; memtable_threshold_bytes = 16384
                flush_trigger_interval_ms = 0; compaction_trigger_interval_ms = 1500
                wal_sync = $true; read_distribution = 'uniform'; expected_reader_set_size = 4
                publication_events = 0; publication_mode = ''; paced_write_rate_per_sec = 0; open_loop_level = ''
                runner_support = 'g5_pending'; note = 'profiling run; G5 only'
            })
        }
        for ($i = 1; $i -le 5; $i++) {
            [void]$rows.Add([pscustomobject][ordered]@{
                trial_id = ('i7_D2_instrumented_{0}_r{1:D2}' -f $arm, $i); block = 'D2'; cell = 'instrumented'; phase = 'diagnostic'
                triplet = $i; triplet_key = ('D2|instrumented|{0:D2}' -f $i); arm = $arm; order_in_triplet = 1
                workload_id = 'WORKLOAD_D_READER_CHURN'; reader_concurrency = 4; writer_concurrency = 2
                measurement_duration_ms = 5000; warmup_duration_ms = 1000; cooldown_duration_ms = 200
                keyspace_cardinality = 10000; initial_sstable_count = 4; initial_keys_per_sstable = 2500
                target_read_hit_ratio = 0.95; memtable_threshold_bytes = 16384
                flush_trigger_interval_ms = 0; compaction_trigger_interval_ms = 1500
                wal_sync = $true; read_distribution = 'uniform'; expected_reader_set_size = 4
                publication_events = 0; publication_mode = ''; paced_write_rate_per_sec = 0; open_loop_level = ''
                runner_support = 'g5_pending'; note = '-tags i7instr build; G5 only'
            })
        }
    }

    # Deterministic randomized order, then anchor the drift controls: the D3
    # 'start' triplet must be the first three trials and D3 'end' the last three.
    $tripletOrder = @{}
    foreach ($g in ($rows | Group-Object triplet_key)) {
        $tripletOrder[$g.Name] = Get-TextSha256 ($script:I7.Seed + '|' + $g.Name)
    }
    $sorted = $rows | Sort-Object `
        @{ Expression = { $tripletOrder[$_.triplet_key] } }, `
        @{ Expression = { $_.triplet } }, `
        @{ Expression = { $_.order_in_triplet } }

    $front = @($sorted | Where-Object { $_.block -eq 'D3' -and $_.cell -eq 'start' })
    $back = @($sorted | Where-Object { $_.block -eq 'D3' -and $_.cell -eq 'end' })
    $middle = @($sorted | Where-Object { -not ($_.block -eq 'D3') })

    $pos = 0
    $final = New-Object System.Collections.ArrayList
    foreach ($group in @($front, $middle, $back)) {
        foreach ($row in $group) {
            $pos++
            $copy = $row | Select-Object *
            $copy | Add-Member -NotePropertyName runlist_position -NotePropertyValue $pos -Force
            [void]$final.Add($copy)
        }
    }
    return $final.ToArray()
}

$script:RunlistColumns = @(
    'runlist_position', 'trial_id', 'block', 'cell', 'phase', 'triplet', 'triplet_key', 'arm',
    'order_in_triplet', 'workload_id', 'reader_concurrency', 'writer_concurrency',
    'measurement_duration_ms', 'warmup_duration_ms', 'cooldown_duration_ms',
    'keyspace_cardinality', 'initial_sstable_count', 'initial_keys_per_sstable',
    'target_read_hit_ratio', 'memtable_threshold_bytes', 'flush_trigger_interval_ms',
    'compaction_trigger_interval_ms', 'wal_sync', 'read_distribution',
    'expected_reader_set_size', 'publication_events', 'publication_mode',
    'paced_write_rate_per_sec', 'open_loop_level', 'runner_support', 'note'
)

# Columns that define the experiment. A refresh may not change any of them.
$script:SemanticColumns = @(
    'trial_id', 'block', 'cell', 'phase', 'triplet', 'triplet_key', 'arm', 'order_in_triplet',
    'workload_id', 'reader_concurrency', 'writer_concurrency', 'measurement_duration_ms',
    'warmup_duration_ms', 'cooldown_duration_ms', 'keyspace_cardinality', 'initial_sstable_count',
    'initial_keys_per_sstable', 'target_read_hit_ratio', 'memtable_threshold_bytes',
    'flush_trigger_interval_ms', 'compaction_trigger_interval_ms', 'wal_sync',
    'read_distribution', 'expected_reader_set_size', 'publication_events', 'publication_mode',
    'paced_write_rate_per_sec', 'open_loop_level'
)

function Write-Runlist([string]$Path, $Rows) {
    $sb = New-Object System.Text.StringBuilder
    [void]$sb.AppendLine(($script:RunlistColumns -join ','))
    foreach ($r in $Rows) {
        $vals = foreach ($h in $script:RunlistColumns) {
            $v = [string]$r.$h
            if ($v -match '[,"\r\n]') { '"' + ($v -replace '"', '""') + '"' } else { $v }
        }
        [void]$sb.AppendLine(($vals -join ','))
    }
    [System.IO.File]::WriteAllText($Path, $sb.ToString(), $script:Utf8NoBom)
}

function Read-Runlist([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return @() }
    return (Import-Csv -LiteralPath $Path)
}

function Compare-Semantics($Old, $New) {
    $oldById = @{}
    foreach ($r in $Old) { $oldById[[string]$r.trial_id] = $r }
    $diffs = New-Object System.Collections.ArrayList
    if (@($Old).Count -ne @($New).Count) {
        [void]$diffs.Add(("row count {0} -> {1}" -f @($Old).Count, @($New).Count))
    }
    foreach ($n in $New) {
        if (-not $oldById.ContainsKey([string]$n.trial_id)) {
            [void]$diffs.Add('added ' + $n.trial_id)
            continue
        }
        $o = $oldById[[string]$n.trial_id]
        foreach ($c in $script:SemanticColumns) {
            if ([string]$o.$c -ne [string]$n.$c) {
                [void]$diffs.Add(("{0}.{1}: {2} -> {3}" -f $n.trial_id, $c, $o.$c, $n.$c))
            }
        }
    }
    return $diffs.ToArray()
}

function Get-OrCreateRunlist($Layout) {
    $rows = @(Read-Runlist $Layout.Runlist)
    if ($rows.Count -gt 0 -and -not $RefreshRunlist) {
        $hash = Get-Sha256 $Layout.Runlist
        Write-Ok ("runlist loaded: {0} rows, sha256 {1}" -f $rows.Count, $hash)
        return @{ Rows = $rows; Sha256 = $hash; Created = $false }
    }
    Write-Section 'Generating the preregistered runlist'
    $new = New-I7Runlist
    if ($rows.Count -gt 0 -and $RefreshRunlist) {
        $diffs = Compare-Semantics -Old $rows -New $new
        if (@($diffs).Count -gt 0) {
            $hasResults = (Test-Path -LiteralPath $Layout.Results)
            if ($hasResults) {
                throw ('runlist refresh would change trial semantics after results exist: ' + (($diffs | Select-Object -First 5) -join '; '))
            }
            Write-Warn2 'runlist refresh changes semantics; refreezing the campaign manifest'
            foreach ($d in ($diffs | Select-Object -First 10)) { Write-Warn2 ('  ' + $d) }
            Remove-Item -LiteralPath $Layout.Manifest -Force -ErrorAction SilentlyContinue
        }
        else {
            Write-Ok 'runlist refresh: only runner capability columns changed'
        }
    }
    Write-Runlist -Path $Layout.Runlist -Rows $new
    $hash = Get-Sha256 $Layout.Runlist
    $counts = $new | Group-Object block | Sort-Object Name | ForEach-Object { '{0}={1}' -f $_.Name, $_.Count }
    Write-Ok ('runlist written: {0} rows' -f $new.Count)
    Write-Info ('blocks: ' + ($counts -join ' '))
    Write-Info ('sha256: ' + $hash)
    Write-Info ('drift anchoring: first 3 rows = D3 start, last 3 rows = D3 end')
    return @{ Rows = (Read-Runlist $Layout.Runlist); Sha256 = $hash; Created = $true }
}

function Initialize-CampaignManifest($Layout, $Runlist, $Platform, $RunnerInfo) {
    # Refreezing is only legal before the first attempt: the manifest exists to
    # bind a frozen artifact set, not to be rewritten mid-campaign.
    if ($RefreezeManifest) {
        $priorStarted = @(Read-JsonLines $Layout.Results | Where-Object { $_.event -eq 'STARTED' })
        if ($priorStarted.Count -gt 0) { throw 'refusing to refreeze the campaign manifest after attempts were recorded' }
        Remove-Item -LiteralPath $Layout.Manifest -Force -ErrorAction SilentlyContinue
        Write-Warn2 'campaign manifest removed for refreeze (no attempts recorded)'
    }
    $manifest = Read-JsonFile $Layout.Manifest
    if ($null -eq $manifest) {
        $manifest = [ordered]@{
            schema                = 'i7.campaign.v1'
            campaign_id           = $script:I7.CampaignId
            protocol_version      = $script:I7.ProtocolVersion
            platform_id           = $script:I7.Platform
            seed                  = $script:I7.Seed
            planned_trials        = $script:I7.PlannedTrials
            hard_ceiling          = $script:I7.HardCeiling
            replacement_cap       = $script:I7.ReplacementCap
            model_authority       = $script:I7.ModelAuthority
            created_utc           = Get-UtcStamp
            runlist_sha256        = $Runlist.Sha256
            runlist_rows          = $Runlist.Rows.Count
            runlist_revision      = 1
            runner_sha256         = $RunnerInfo.sha256
            platform_sha256       = Get-Sha256 $Layout.Platform
            git_revision          = $Platform.git_revision
            worktree_fingerprint  = $Platform.worktree_fingerprint
            replacements_used     = 0
            execution_started_utc = ''
            partial               = $false
            partial_reason        = ''
            allow_dirty           = $false
            test_mode             = [bool]$TestMode
        }
        Write-JsonFile -Path $Layout.Manifest -Object $manifest
        Write-Ok 'campaign manifest created'
        return $manifest
    }
    if (-not ($manifest.PSObject.Properties['runlist_revision'])) { $manifest | Add-Member -NotePropertyName runlist_revision -NotePropertyValue 1 -Force }
    if (-not ($manifest.PSObject.Properties['replacements_used'])) { $manifest | Add-Member -NotePropertyName replacements_used -NotePropertyValue 0 -Force }
    if (-not ($manifest.PSObject.Properties['execution_started_utc'])) { $manifest | Add-Member -NotePropertyName execution_started_utc -NotePropertyValue '' -Force }
    if ((PropOr $manifest 'runlist_sha256' '') -ne $Runlist.Sha256) {
        if ($Runlist.Created) {
            $manifest.runlist_sha256 = $Runlist.Sha256
            $manifest.runlist_revision = [int]$manifest.runlist_revision + 1
            $manifest.updated_utc = Get-UtcStamp
            Write-JsonFile -Path $Layout.Manifest -Object $manifest
            Write-Ok ('campaign manifest updated to runlist revision ' + $manifest.runlist_revision)
            return $manifest
        }
        throw ("runlist hash mismatch: manifest {0} vs file {1}" -f $manifest.runlist_sha256, $Runlist.Sha256)
    }
    Write-Ok ('campaign manifest verified (runlist revision ' + $manifest.runlist_revision + ')')
    return $manifest
}

function Save-Manifest($Layout, $Manifest) {
    if (-not ($Manifest.PSObject.Properties['updated_utc'])) {
        $Manifest | Add-Member -NotePropertyName updated_utc -NotePropertyValue '' -Force
    }
    $Manifest.updated_utc = Get-UtcStamp
    Write-JsonFile -Path $Layout.Manifest -Object $Manifest
}

# ---------------------------------------------------------------------------
# Approval
# ---------------------------------------------------------------------------
function Get-ApprovalBinding($Layout, $Runlist, [string[]]$BlocksNeeded) {
    $runnerInfo = Read-JsonFile $Layout.Platform
    $i7hash = Get-Sha256 $Layout.Runner
    $pebbleHash = Get-Sha256 $Layout.PebbleRunner
    [ordered]@{
        campaign_id        = $script:I7.CampaignId
        protocol_version   = $script:I7.ProtocolVersion
        runlist_sha256     = $Runlist.Sha256
        runner_sha256      = $i7hash
        pebble_sha256      = $pebbleHash
        allowed_blocks     = $BlocksNeeded
        allowed_phases     = @('preflight', 'confirmatory')
        approved           = $false
        approved_by        = ''
        approved_at        = ''
        gate               = ''
        max_trials         = $script:I7.HardCeiling
        allow_dirty        = $false
        scope              = ''
    }
}

function Write-ApprovalTemplate($Layout, $Runlist, [string]$ForGate, [string]$ForScope = '') {
    $rows = $Runlist.Rows
    $blocks = if ($ForScope -eq 'calibration') { @('PF') } else { @($rows | ForEach-Object { $_.block } | Sort-Object -Unique) }
    $tpl = Get-ApprovalBinding -Layout $Layout -Runlist $Runlist -BlocksNeeded $blocks
    $tpl.gate = $ForGate
    if ($ForScope -eq 'calibration') {
        $tpl.scope = 'calibration'
        $tpl.allowed_phases = @('calibration')
        $tpl.allowed_blocks = @('PF')
        $tpl.max_trials = 12
    } else {
        $tpl.scope = if ($ForScope) { $ForScope } else { ('gate ' + $ForGate) }
    }
    $suffix = if ($ForScope) { $ForGate + '.' + $ForScope } else { $ForGate }
    $path = Join-Path $Root ('APPROVAL.template.' + $suffix + '.json')
    Write-JsonFile -Path $path -Object $tpl
    Write-Ok ('approval template written: ' + $path)
    Write-Info 'Set approved=true, approved_by, approved_at, then save as APPROVAL.json.'
    return $path
}

function Assert-Approval($Layout, $Runlist, [string]$RequiredGate, [string[]]$SelectedBlocks, [int]$SelectedCount, [string]$RequiredScope = '') {
    if ([string]::IsNullOrWhiteSpace($RequiredGate)) { throw 'no gate specified for this mode' }
    if (-not (Test-Path -LiteralPath $Layout.Approval)) {
        throw ("approval file not found: {0}`n  Generate a template with -WriteApprovalTemplate -Gate {1}, then have the reviewer fill it in." -f $Layout.Approval, $RequiredGate)
    }
    $a = Read-JsonFile $Layout.Approval
    if ($null -eq $a) { throw 'approval file is not valid JSON' }
    if ((PropOr $a 'campaign_id' '') -ne $script:I7.CampaignId) { throw ('approval campaign_id mismatch: ' + (PropOr $a 'campaign_id' '')) }
    if ((PropOr $a 'protocol_version' '') -ne $script:I7.ProtocolVersion) { throw ('approval protocol_version mismatch: ' + (PropOr $a 'protocol_version' '')) }
    if (-not [bool](PropOr $a 'approved' $false)) { throw 'approval file does not set approved=true' }
    if ((PropOr $a 'gate' '') -ne $RequiredGate) { throw ("approval is for gate {0}, this mode requires {1}" -f (PropOr $a 'gate' ''), $RequiredGate) }
    if ((PropOr $a 'runlist_sha256' '') -ne $Runlist.Sha256) { throw 'approval runlist_sha256 does not match the committed runlist' }
    $i7hash = Get-Sha256 $Layout.Runner
    if ((PropOr $a 'runner_sha256' '') -ne $i7hash) { throw 'approval runner_sha256 does not match the built i7trial.exe' }
    $allowed = @(PropOr $a 'allowed_blocks' @())
    if ($allowed.Count -eq 0) { throw 'approval does not list allowed_blocks' }
    foreach ($b in $SelectedBlocks) {
        if (-not ($allowed -contains $b)) { throw ("block {0} is not in the approved set {1}" -f $b, ($allowed -join ',')) }
    }
    $cap = [int](PropOr $a 'max_trials' 0)
    if ($cap -gt 0 -and $SelectedCount -gt $cap) {
        throw ("selected {0} trials but the approval caps at {1}" -f $SelectedCount, $cap)
    }
    $scope = [string](PropOr $a 'scope' '')
    $allowedPhases = @(PropOr $a 'allowed_phases' @())
    if ($RequiredScope -eq 'calibration') {
        if ($scope -notmatch 'calibration' -and -not ($allowedPhases -contains 'calibration')) {
            throw ("approval scope is '{0}', calibration requires scope containing 'calibration'" -f $scope)
        }
    } else {
        if ($scope -match '^calibration$' -and -not ($allowedPhases -contains 'preflight' -or $allowedPhases -contains 'confirmatory')) {
            throw ("approval is scoped exclusively for calibration ('{0}'); this mode requires {1}" -f $scope, (if ($RequiredScope) { $RequiredScope } else { 'preflight/confirmatory' }))
        }
    }
    if (-not [bool](PropOr $a 'allow_dirty' $false)) {
        $plat = Read-JsonFile $Layout.Platform
        if ([bool](PropOr $plat 'git_dirty' $true)) { throw 'working tree is dirty and the approval does not set allow_dirty=true' }
    }
    $pebbleApproved = [string](PropOr $a 'pebble_sha256' '')
    if ($SelectedBlocks -contains 'E7' -and $pebbleApproved -ne '') {
        $ph = Get-Sha256 $Layout.PebbleRunner
        if ($pebbleApproved -ne $ph) { throw 'approval pebble_sha256 does not match the built pebble_runner.exe' }
    }
    Write-Ok ("approval verified: gate {0}, scope {1}, blocks {2}, max {3}, by {4}" -f (PropOr $a 'gate' ''), $scope, ($allowed -join ','), $cap, (PropOr $a 'approved_by' ''))
    return $a
}

# ---------------------------------------------------------------------------
# Integrity
# ---------------------------------------------------------------------------
function Assert-ExecutionIntegrity($Layout, $Manifest) {
    $plat = Read-JsonFile $Layout.Platform
    if ($null -eq $plat) { throw 'platform_manifest.json missing; run -Check first' }

    $rev = ''
    $dirty = $true
    Push-Location -LiteralPath $script:RepoRoot
    try {
        $rev = (& git rev-parse HEAD 2>$null)
        $dirty = -not [string]::IsNullOrWhiteSpace(((& git status --porcelain 2>$null) -join ''))
    }
    finally { Pop-Location }
    if ($rev -ne $plat.git_revision) { throw ("git revision changed since -Check: {0} -> {1}" -f $plat.git_revision, $rev) }
    $fp = Get-WorktreeFingerprint $rev
    if ($fp -ne $plat.worktree_fingerprint) { throw 'worktree fingerprint changed since -Check (uncommitted edits or new untracked files)' }

    $runnerHash = Get-Sha256 $Layout.Runner
    if ($plat.runner.sha256 -and $plat.runner.sha256 -ne $runnerHash) {
        throw ("i7trial.exe hash changed since -Check: {0} -> {1}" -f $plat.runner.sha256, $runnerHash)
    }
    if ($Manifest.runner_sha256 -and $Manifest.runner_sha256 -ne $runnerHash) {
        throw ("i7trial.exe hash changed since the campaign manifest: {0} -> {1}" -f $Manifest.runner_sha256, $runnerHash)
    }
    $platHash = Get-Sha256 $Layout.Platform
    if ($Manifest.platform_sha256 -and $Manifest.platform_sha256 -ne $platHash) {
        throw 'platform_manifest.json changed since the campaign manifest was frozen'
    }
    $frozenNow = PropOr $plat 'frozen_artifact_sha256' $null
    if ($null -eq $frozenNow) { throw 'platform manifest has no frozen artifact hashes' }
    foreach ($rel in $frozenNow.PSObject.Properties.Name) {
        $p = Join-Path $script:RepoRoot $rel
        $now = Get-Sha256 $p
        $then = [string]$plat.frozen_artifact_sha256.$rel
        if ($then -and $now -and $then -ne $now) { throw ('frozen artifact changed: ' + $rel) }
    }
    Write-Ok ('integrity verified: rev {0}, worktree {1}, runner {2}' -f $rev.Substring(0, 8), $fp.Substring(0, 8), $runnerHash.Substring(0, 8))
}

function Assert-Resources($Layout, $Manifest) {
    $free = (Get-Volume -DriveLetter (([System.IO.Path]::GetPathRoot($Root)).TrimEnd('\').TrimEnd(':'))).SizeRemaining
    if ($free -lt ($MinFreeGB * 1GB)) {
        throw ('stopping rule: free space {0:N1} GiB below the {1} GiB floor' -f ($free / 1GB), $MinFreeGB)
    }
    if ($Manifest.execution_started_utc) {
        $started = [datetime]::Parse($Manifest.execution_started_utc).ToUniversalTime()
        $elapsed = ((Get-Date).ToUniversalTime() - $started).TotalMinutes
        if ($elapsed -gt $MaxCampaignMinutes) {
            throw ('stopping rule: campaign duration {0:N0} min exceeds the {1} min limit' -f $elapsed, $MaxCampaignMinutes)
        }
    }
    $ledger = @(Read-JsonLines $Layout.Results)
    $finished = @($ledger | Where-Object { $_.event -eq 'FINISHED' })
    $failed = @($finished | Where-Object { $_.status -ne 'SUCCESS' })
    if ($failed.Count -ge $MaxFailures) {
        throw ('stopping rule: {0} failed trials (limit {1})' -f $failed.Count, $MaxFailures)
    }
    $leaseFails = @($failed | Where-Object { ([string]$_.error) -match 'lease' })
    if ($leaseFails.Count -ge $MaxLeaseFailures) {
        throw ('stopping rule: {0} lease/lifetime failures (limit {1})' -f $leaseFails.Count, $MaxLeaseFailures)
    }
}

# ---------------------------------------------------------------------------
# Trial execution
# ---------------------------------------------------------------------------
function New-TrialSpec($Row, $Layout, $Runlist, $Calibration) {
    $spec = [ordered]@{
        schema                         = 'i7.trial-spec.v1'
        trial_id                       = $Row.trial_id
        block                          = $Row.block
        cell                           = $Row.cell
        triplet                        = [int]$Row.triplet
        arm                            = $Row.arm
        order_position                 = [int]$Row.order_in_triplet
        runlist_position               = [int]$Row.runlist_position
        seed                           = [int64]([Convert]::ToUInt64((Get-TextSha256 ($script:I7.Seed + '|' + $Row.triplet_key)).Substring(0, 15), 16))
        workload_id                    = $Row.workload_id
        reader_concurrency             = [int]$Row.reader_concurrency
        writer_concurrency             = [int]$Row.writer_concurrency
        measurement_duration_ms        = [int]$Row.measurement_duration_ms
        warmup_duration_ms             = [int]$Row.warmup_duration_ms
        cooldown_duration_ms           = [int]$Row.cooldown_duration_ms
        keyspace_cardinality           = [int]$Row.keyspace_cardinality
        initial_sstable_count          = [int]$Row.initial_sstable_count
        initial_keys_per_sstable       = [int]$Row.initial_keys_per_sstable
        target_read_hit_ratio          = [double]$Row.target_read_hit_ratio
        memtable_threshold_bytes       = [int64]$Row.memtable_threshold_bytes
        flush_trigger_interval_ms      = [int]$Row.flush_trigger_interval_ms
        compaction_trigger_interval_ms = [int]$Row.compaction_trigger_interval_ms
        wal_sync                       = [bool]::Parse([string]$Row.wal_sync)
        read_distribution              = [string]$Row.read_distribution
        data_dir                       = Join-Path $Layout.Data $Row.trial_id
        expected_reader_set_size       = [int]$Row.expected_reader_set_size
        notes                          = [string]$Row.note
    }
    $ev = 0
    if ($Row.publication_events) { $ev = [int]$Row.publication_events }
    if ($ev -gt 0) {
        $spec.publication_events = $ev
        $spec.publication_mode = [string]$Row.publication_mode
    }
    if ([double]$Row.paced_write_rate_per_sec -gt 0) {
        $spec.paced_write_rate_per_sec = [double]$Row.paced_write_rate_per_sec
    }
    if (-not [string]::IsNullOrWhiteSpace([string]$Row.open_loop_level)) {
        if ($null -eq $Calibration -or -not $Calibration.c_ref) {
            throw ('E4 row {0} requires calibration.json (C_ref); run -Calibrate under gate G3 first' -f $Row.trial_id)
        }
        $mult = $script:OpenLoopLevels[[string]$Row.open_loop_level]
        $spec.open_loop_rate_per_sec = [double]$Calibration.c_ref * [double]$mult
    }
    return $spec
}

function Test-Envelope([string]$Path, [string]$TrialID) {
    if (-not (Test-Path -LiteralPath $Path)) { return 'no output envelope' }
    $e = Read-JsonFile $Path
    if ($null -eq $e) { return 'envelope is not valid JSON' }
    $etid = [string](PropOr $e 'trial_id' '')
    if ($etid -ne $TrialID) { return ('envelope trial_id mismatch: ' + $etid) }
    $schema = [string](PropOr $e 'schema' '')
    if ($schema -ne 'i7.trial.v1' -and $schema -ne 'i7.smoke.v1') { return ('unexpected envelope schema: ' + $schema) }
    if ([string](PropOr $e 'status' '') -ne 'SUCCESS') { return '' }
    $res = PropOr $e 'result' $null
    if ($null -eq $res) { return 'SUCCESS without a result payload' }
    $ops = PropOr $res 'operations' $null
    if ($null -eq $ops) { return 'SUCCESS without an operations block' }
    if ([int64](PropOr $ops 'read_ops_completed' 0) -le 0) { return 'SUCCESS with zero completed reads' }
    if ([int64](PropOr $ops 'validation_error_count' 0) -gt 0) { return 'SUCCESS with validation errors' }
    $ts = PropOr $res 'timestamps' $null
    if ($null -eq $ts) { return 'SUCCESS without a timestamps block' }
    if ([int64](PropOr $ts 'measured_window_ns' 0) -le 0) { return 'SUCCESS with a non-positive measured window' }
    return ''
}

function Invoke-Trial($Row, $Layout, $Runlist, $Manifest, [string]$Gate, [string]$ApprovalPath, [string]$GitRev, $Calibration, [int]$Attempt) {
    $spec = New-TrialSpec -Row $Row -Layout $Layout -Runlist $Runlist -Calibration $Calibration
    $specPath = Join-Path $Layout.Specs ($Row.trial_id + '.json')
    Write-JsonFile -Path $specPath -Object $spec

    $outPath = Join-Path $Layout.Trials ($Row.trial_id + '.json')
    $attemptPath = Join-Path $Layout.Attempts ('{0}_a{1:D2}.json' -f $Row.trial_id, $Attempt)
    $stdoutLog = Join-Path $Layout.Logs ('{0}_a{1:D2}.stdout.log' -f $Row.trial_id, $Attempt)
    $stderrLog = Join-Path $Layout.Logs ('{0}_a{1:D2}.stderr.log' -f $Row.trial_id, $Attempt)

    # Clear stale engine output, then archive any previous envelope.
    $dataDir = [string]$spec.data_dir
    if (Test-Path -LiteralPath $dataDir) {
        Remove-Item -LiteralPath $dataDir -Recurse -Force -ErrorAction SilentlyContinue
    }
    if (Test-Path -LiteralPath $outPath) {
        try { Copy-Item -LiteralPath $outPath -Destination ($attemptPath -replace '\.json$', '.prev.json') -Force } catch { }
        Remove-Item -LiteralPath $outPath -Force -ErrorAction SilentlyContinue
    }

    $runner = Get-BlockRunner -Layout $Layout -Block $Row.block
    $argLine = @(
        (Quote-Arg '-spec'), (Quote-Arg $specPath),
        (Quote-Arg '-out'), (Quote-Arg $outPath),
        (Quote-Arg '-gate'), (Quote-Arg $Gate),
        (Quote-Arg '-approval'), (Quote-Arg $ApprovalPath),
        (Quote-Arg '-platform'), (Quote-Arg $Layout.Platform),
        (Quote-Arg '-git-rev'), (Quote-Arg $GitRev)
    ) -join ' '
    if ($KeepData) { $argLine += ' ' + (Quote-Arg '-keep-data') }

    Add-JsonLine -Path $Layout.Results -Object ([ordered]@{
        schema = 'i7.ledger.v1'; event = 'STARTED'; trial_id = $Row.trial_id; block = $Row.block
        cell = $Row.cell; arm = $Row.arm; triplet = [int]$Row.triplet; runlist_position = [int]$Row.runlist_position
        attempt = $Attempt; started_utc = Get-UtcStamp; spec_path = $specPath; runner = $runner
    })

    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    $exitCode = $null
    $timedOut = $false
    $stdoutText = ''
    $stderrText = ''
    try {
        $psi = New-Object System.Diagnostics.ProcessStartInfo
        $psi.FileName = $runner
        $psi.Arguments = $argLine
        $psi.UseShellExecute = $false
        $psi.RedirectStandardOutput = $true
        $psi.RedirectStandardError = $true
        $psi.CreateNoWindow = $true
        $psi.WorkingDirectory = $script:RepoRoot
        $proc = New-Object System.Diagnostics.Process
        $proc.StartInfo = $psi
        [void]$proc.Start()
        $outTask = $proc.StandardOutput.ReadToEndAsync()
        $errTask = $proc.StandardError.ReadToEndAsync()
        if (-not $proc.WaitForExit($TrialTimeoutSec * 1000)) {
            $timedOut = $true
            try { $proc.Kill() } catch { }
            [void]$proc.WaitForExit(5000)
        }
        $stdoutText = $outTask.Result
        $stderrText = $errTask.Result
        if ($proc.HasExited) { $exitCode = $proc.ExitCode }
    }
    catch {
        $stderrText = 'launch failed: ' + $_.Exception.Message
    }
    $sw.Stop()
    [System.IO.File]::WriteAllText($stdoutLog, $stdoutText, $script:Utf8NoBom)
    [System.IO.File]::WriteAllText($stderrLog, $stderrText, $script:Utf8NoBom)

    $status = 'INCOMPLETE'
    $err = ''
    $envelope = $null
    if (Test-Path -LiteralPath $outPath) {
        $envelope = Read-JsonFile $outPath
        if ($envelope) {
            $status = [string](PropOr $envelope 'status' 'INVALID')
            $err = [string](PropOr $envelope 'error' '')
        }
    }
    $validation = Test-Envelope -Path $outPath -TrialID $Row.trial_id
    if ($timedOut) {
        $status = 'TIMEOUT'; $err = ('exceeded {0}s' -f $TrialTimeoutSec)
    }
    elseif ($validation -ne '') {
        $status = 'INVALID'; $err = $validation
    }
    elseif ($null -ne $exitCode -and $exitCode -ne 0 -and $status -eq 'SUCCESS') {
        $status = 'FAILED'; $err = ('runner exit code ' + $exitCode)
    }

    if (Test-Path -LiteralPath $outPath) {
        try { Copy-Item -LiteralPath $outPath -Destination $attemptPath -Force } catch { }
    }

    $ledgerRow = [ordered]@{
        schema = 'i7.ledger.v1'; event = 'FINISHED'; trial_id = $Row.trial_id; block = $Row.block
        cell = $Row.cell; arm = $Row.arm; triplet = [int]$Row.triplet; runlist_position = [int]$Row.runlist_position
        attempt = $Attempt; status = $status; exit_code = $exitCode; wall_ms = [int64]$sw.Elapsed.TotalMilliseconds
        error = $err; attempt_json = $attemptPath; stdout_log = $stdoutLog; stderr_log = $stderrLog
        finished_utc = Get-UtcStamp; runner = $runner
    }
    Add-JsonLine -Path $Layout.Results -Object $ledgerRow
    if ($status -ne 'SUCCESS') {
        Add-JsonLine -Path $Layout.FailuresF -Object $ledgerRow
        Write-Host ('  [FAIL] {0}: {1} {2}' -f $Row.trial_id, $status, $err) -ForegroundColor Red
    }
    else {
        Write-Host ('  [ ok ] {0} ({1} ms)' -f $Row.trial_id, $ledgerRow.wall_ms) -ForegroundColor Green
    }
    return $ledgerRow
}

# ---------------------------------------------------------------------------
# Package
# ---------------------------------------------------------------------------
function New-ResultPackage($Layout, $Platform, $Runlist, $Manifest) {
    Write-Section 'Packaging results'
    $ledger = @(Read-JsonLines $Layout.Results)
    $failures = @(Read-JsonLines $Layout.FailuresF)
    $finished = @($ledger | Where-Object { $_.event -eq 'FINISHED' })

    $byStatus = @{}
    foreach ($l in $finished) {
        $s = [string]$l.status
        if (-not $byStatus.ContainsKey($s)) { $byStatus[$s] = 0 }
        $byStatus[$s] = $byStatus[$s] + 1
    }
    $byBlock = @{}
    foreach ($l in $finished) {
        $b = [string]$l.block
        if (-not $byBlock.ContainsKey($b)) { $byBlock[$b] = 0 }
        $byBlock[$b] = $byBlock[$b] + 1
    }
    $distinct = @{}
    foreach ($l in $finished) { if ($l.status -eq 'SUCCESS') { $distinct[[string]$l.trial_id] = $true } }

    $drift = $null
    $d3 = @($finished | Where-Object { $_.block -eq 'D3' -and $_.status -eq 'SUCCESS' })
    if ($d3.Count -ge 2) {
        $gets = { param($cell) foreach ($l in $d3) { if ($l.cell -eq $cell) { $ap = [string](PropOr $l 'attempt_json' ''); if ($ap -ne '') { $j = Read-JsonFile $ap; if ($j) { $res = PropOr $j 'result' $null; if ($null -ne $res) { $tp = PropOr $res 'throughput' $null; if ($null -ne $tp) { $v = [double](PropOr $tp 'read_ops_per_sec' 0); if ($v -gt 0) { $v } } } } } } } }
        $startVals = @(& $gets 'start')
        $endVals = @(& $gets 'end')
        if ($startVals.Count -gt 0 -and $endVals.Count -gt 0) {
            $sm = ($startVals | Measure-Object -Average).Average
            $em = ($endVals | Measure-Object -Average).Average
            $drift = [ordered]@{
                start_read_ops_per_sec = [math]::Round($sm, 2)
                end_read_ops_per_sec   = [math]::Round($em, 2)
                ratio                  = if ($sm -gt 0) { [math]::Round($em / $sm, 4) } else { 0 }
                exceeded               = ($sm -gt 0) -and ([math]::Abs($em / $sm - 1) -gt 0.15)
            }
        }
    }

    $summary = [ordered]@{
        schema           = 'i7.summary.v1'
        campaign_id      = $script:I7.CampaignId
        protocol_version = $script:I7.ProtocolVersion
        generated_utc    = Get-UtcStamp
        platform         = $script:I7.Platform
        git_revision     = $Platform.git_revision
        git_dirty        = $Platform.git_dirty
        test_mode        = [bool]$TestMode
        runlist_sha256   = $Runlist.Sha256
        runlist_revision = $Manifest.runlist_revision
        planned_trials   = $script:I7.PlannedTrials
        hard_ceiling     = $script:I7.HardCeiling
        ledger_entries   = @($ledger).Count
        finished_entries = $finished.Count
        successful_trials = $distinct.Count
        replacements_used = [int]$Manifest.replacements_used
        by_status        = $byStatus
        by_block         = $byBlock
        failures         = @($failures).Count
        drift            = $drift
        note             = 'Counts and integrity only. No effect estimates are computed here.'
    }
    Write-JsonFile -Path $Layout.Summary -Object $summary

    $files = New-Object System.Collections.ArrayList
    foreach ($f in (Get-ChildItem -LiteralPath $Root -Recurse -File -ErrorAction SilentlyContinue)) {
        if ($f.FullName -eq $Layout.Zip) { continue }
        if ($f.Name -eq 'checksums.sha256') { continue }
        [void]$files.Add($f.FullName)
    }
    $sb = New-Object System.Text.StringBuilder
    foreach ($f in ($files | Sort-Object)) {
        $rel = $f.Substring($Root.Length).TrimStart('\')
        [void]$sb.AppendLine((Get-Sha256 $f) + '  ' + $rel)
    }
    [System.IO.File]::WriteAllText($Layout.Checksums, $sb.ToString(), $script:Utf8NoBom)

    if (Test-Path -LiteralPath $Layout.Zip) { Remove-Item -LiteralPath $Layout.Zip -Force }
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [System.IO.Compression.ZipFile]::Open($Layout.Zip, 'Create')
    try {
        foreach ($f in ($files | Sort-Object)) {
            $rel = $f.Substring($Root.Length).TrimStart('\')
            [void][System.IO.Compression.ZipFileExtensions]::CreateEntryFromFile($zip, $f, $rel, 'Optimal')
        }
        [void][System.IO.Compression.ZipFileExtensions]::CreateEntryFromFile($zip, $Layout.Checksums, 'checksums.sha256', 'Optimal')
    }
    finally { $zip.Dispose() }

    $zipHash = Get-Sha256 $Layout.Zip
    [System.IO.File]::WriteAllText($Layout.Zip + '.sha256', ($zipHash + '  I7_WINDOWS_RESULTS.zip'), $script:Utf8NoBom)
    Write-Ok ('package: ' + $Layout.Zip)
    Write-Info ('files: ' + $files.Count)
    Write-Info ('sha256: ' + $zipHash)
    Write-Host ''
    Write-Host ('UPLOAD THIS ZIP: ' + $Layout.Zip) -ForegroundColor Cyan
    Write-Host ('SHA-256:         ' + $zipHash) -ForegroundColor Cyan
    return $summary
}

# ---------------------------------------------------------------------------
# -Check
# ---------------------------------------------------------------------------
# In TestMode the driver must use the overridden binaries everywhere, not just
# during -Check, otherwise the approval hash would bind the wrong executable.
function Resolve-Runners($Layout) {
    if ($TestMode) {
        if (-not [string]::IsNullOrWhiteSpace($RunnerOverride)) {
            $Layout.Runner = [System.IO.Path]::GetFullPath($RunnerOverride)
        }
        if (-not [string]::IsNullOrWhiteSpace($PebbleRunnerOverride)) {
            $Layout.PebbleRunner = [System.IO.Path]::GetFullPath($PebbleRunnerOverride)
        }
    }
    return $Layout
}

function Invoke-Check {
    $Layout = Resolve-Runners (Get-PathLayout)
    Write-Section 'I7 platform validation (-Check)'

    if (Test-UnderFrozenRoot $Root) { Write-Fail 'results root is inside a frozen campaign directory' }

    $platform = Get-PlatformInfo
    Write-Ok ('host: {0} / {1} (build {2})' -f $platform.host, $platform.os, $platform.os_build)
    Write-Ok ('cpu: {0} | {1} physical / {2} logical' -f $platform.cpu_model, $platform.physical_cores, $platform.logical_cores)
    Write-Ok ('ram: {0:N1} GiB' -f ($platform.total_ram_bytes / 1GB))
    Write-Ok ('filesystem: {0} on {1} ({2})' -f $platform.filesystem, $platform.disk_model, $platform.disk_bus_type)

    if ($platform.os -notmatch 'Windows') { Write-Fail 'not running on Windows' }
    if ($platform.os_architecture -notmatch '64') { Write-Fail 'not a 64-bit OS' }
    if ($platform.filesystem -notmatch 'NTFS') { Write-Fail ('results root is not NTFS: ' + $platform.filesystem) }
    else { Write-Ok 'filesystem is NTFS' }
    if ($platform.nvme_placement_verified) { Write-Ok 'results root resides on an NVMe device' }
    else { Write-Warn2 ('NVMe placement not verified (bus type: {0})' -f $platform.disk_bus_type) }
    if ($platform.volume_free_bytes -lt ($MinFreeGB * 1GB)) {
        Write-Fail ('free space below {0} GiB: {1:N1} GiB' -f $MinFreeGB, ($platform.volume_free_bytes / 1GB))
    }
    else { Write-Ok ('free space: {0:N1} GiB' -f ($platform.volume_free_bytes / 1GB)) }
    if ($platform.total_ram_bytes -lt 8GB) { Write-Fail 'less than 8 GiB RAM' } else { Write-Ok 'RAM >= 8 GiB' }

    if ($platform.go_version -notmatch 'go1\.') { Write-Fail 'Go toolchain not found on PATH' }
    else { Write-Ok ('go: ' + $platform.go_version) }
    if ($platform.go_env['GOOS'] -ne 'windows') { Write-Fail ('GOOS is ' + $platform.go_env['GOOS']) } else { Write-Ok 'GOOS=windows' }
    if ($platform.go_env['GOARCH'] -ne 'amd64') { Write-Fail ('GOARCH is ' + $platform.go_env['GOARCH']) } else { Write-Ok 'GOARCH=amd64' }

    $raceAvailable = $false
    try {
        Push-Location -LiteralPath $script:RepoRoot
        $env:CGO_ENABLED = '1'
        $null = (& go test -race -run '^$' ./lsmengine 2>&1)
        $raceAvailable = ($LASTEXITCODE -eq 0)
    }
    catch { $raceAvailable = $false }
    finally { Pop-Location; $env:CGO_ENABLED = '0' }
    if ($raceAvailable) { Write-Ok 'race detector available' }
    else { Write-Warn2 'race detector NOT available (requires cgo + a C compiler)' }

    $frozen = [ordered]@{}
    foreach ($rel in @(
            'experiment_runner.exe', 'experiment_runner_linux',
            'research\experiments\data\campaign_wsl2_ext4_restart_v1\manifest_checksums.json',
            'research\experiments\data\targeted_validation_v1\manifest_checksums.json')) {
        $frozen[$rel] = Get-Sha256 (Join-Path $script:RepoRoot $rel)
    }
    Write-Ok 'frozen artifact hashes recorded'

    $storage = Test-StorageThroughput -Layout $Layout
    Write-Ok ('storage probe: {0} MB/s buffered write, flush(true) in {1} ms' -f $storage.buffered_MBps, $storage.buffered_write_ms)

    if ($TestMode -and -not [string]::IsNullOrWhiteSpace($RunnerOverride)) {
        $runner = [ordered]@{ path = [System.IO.Path]::GetFullPath($RunnerOverride); sha256 = (Get-Sha256 $RunnerOverride); package = 'override'; built_utc = Get-UtcStamp }
        Write-Warn2 ('TEST MODE: runner overridden with ' + $runner.path)
    }
    else {
        $runner = Build-Runner -Package '.\research\experiments\phase_i7\cmd\i7trial' -OutPath $Layout.Runner
    }
    if (-not $runner) { Write-Fail 'i7trial build failed' }
    $pebble = Build-PebbleRunner -Layout $Layout

    $selftest = $null
    if ($runner -and -not ($TestMode -and $RunnerOverride)) {
        $json = & $Layout.Runner -selftest 2>&1
        if ($LASTEXITCODE -ne 0) { Write-Fail ('runner self-test failed: ' + ($json -join ' ')) }
        else {
            try {
                $selftest = (($json -join "`n") | ConvertFrom-Json)
                Write-Ok ('runner self-test: qpc step={0} ns, time.Now step={1} ns' -f $selftest.timers.qpc_min_positive_delta_ns, $selftest.timers.now_min_positive_delta_ns)
            }
            catch { Write-Fail 'runner self-test produced invalid JSON' }
        }
    }

    if (-not [string]::IsNullOrWhiteSpace($Affinity)) {
        $mask = [uint64]0
        try { $mask = [Convert]::ToUInt64(($Affinity -replace '^0x', ''), 16) } catch { Write-Fail ('invalid -Affinity value: ' + $Affinity) }
        if ($mask -ne 0) {
            try {
                Set-CurrentAffinityMask -Mask $mask
                $actual = Get-CurrentAffinityMask
                if ($actual -ne $mask) { Write-Fail ('affinity not applied: requested 0x{0:x}, actual 0x{1:x}' -f $mask, $actual) }
                else { Write-Ok ('affinity applied: 0x{0:x} ({1} logical CPUs)' -f $actual, (Get-PopCount $actual)) }
                $env:GOMAXPROCS = [string](Get-PopCount $actual)
                Write-Ok ('GOMAXPROCS=' + $env:GOMAXPROCS)
            }
            catch { Write-Fail ('setting affinity failed: ' + $_.Exception.Message) }
        }
    }
    else {
        Write-Info ('affinity: not requested; process mask 0x{0:x}' -f (Get-CurrentAffinityMask))
    }

    $platform['affinity_actual'] = ('0x{0:x}' -f (Get-CurrentAffinityMask))
    $platform['gomaxprocs_env'] = $env:GOMAXPROCS
    $platform['race_detector_available'] = $raceAvailable
    $platform['storage_probe'] = $storage
    $platform['frozen_artifact_sha256'] = $frozen
    $platform['runner'] = $runner
    $platform['pebble_runner'] = $pebble
    $platform['selftest'] = $selftest
    $platform['test_mode'] = [bool]$TestMode
    Write-JsonFile -Path $Layout.Platform -Object $platform
    Write-Ok ('platform manifest: ' + $Layout.Platform)

    if ($platform.git_dirty) { Write-Warn2 'git working tree is dirty (recorded in worktree_fingerprint)' }
    else { Write-Ok ('git revision: ' + $platform.git_revision) }

    Write-Section 'Check summary'
    if ($script:Failures.Count -gt 0) {
        foreach ($f in $script:Failures) { Write-Host ('  BLOCKING: ' + $f) -ForegroundColor Red }
        Write-Host ''
        Write-Host ('Check FAILED with {0} blocking issue(s).' -f $script:Failures.Count) -ForegroundColor Red
        exit 1
    }
    Write-Host 'Check PASSED. No trial was executed.' -ForegroundColor Green
    exit 0
}

# ---------------------------------------------------------------------------
# -Calibrate (G3)
# ---------------------------------------------------------------------------
function Invoke-Calibrate {
    $Layout = Resolve-Runners (Get-PathLayout)
    Write-Section 'I7 calibration (gate G3): C_ref for E4'
    Write-Info 'Workload plan: 366 inventory trials (12 preflight + 330 confirmatory + 24 diagnostic) + 9 calibration runs = 375 total planned executions.'
    $platform = Read-JsonFile $Layout.Platform
    if ($null -eq $platform) { throw 'run -Check first' }

    $run = Get-OrCreateRunlist -Layout $Layout
    $manifest = Initialize-CampaignManifest -Layout $Layout -Runlist $run -Platform $platform -RunnerInfo ([ordered]@{ sha256 = (Get-Sha256 $Layout.Runner) })
    $null = Assert-Approval -Layout $Layout -Runlist $run -RequiredGate 'G3' -SelectedBlocks @('PF') -SelectedCount 9 -RequiredScope 'calibration'
    Assert-ExecutionIntegrity -Layout $Layout -Manifest $manifest

    $ledgerPath = Join-Path $Layout.Calibration 'calibration_ledger.jsonl'
    $existingEntries = if (Test-Path -LiteralPath $ledgerPath) { @(Read-JsonLines $ledgerPath) } else { @() }
    $finishedSuccess = @($existingEntries | Where-Object { $_.event -eq 'FINISHED' -and $_.status -eq 'SUCCESS' })
    $startedAttempts = @($existingEntries | Where-Object { $_.event -eq 'STARTED' })

    # Balanced Latin-square rotation across 3 triplets:
    # Triplet 1: A, AMIN, B
    # Triplet 2: AMIN, B, A
    # Triplet 3: B, A, AMIN
    # All arms in Triplet r share the same logical request-stream seed.
    $plan = @(
        [ordered]@{ Arm = 'A';    Rep = 1; Triplet = 1; Order = 1 },
        [ordered]@{ Arm = 'AMIN'; Rep = 1; Triplet = 1; Order = 2 },
        [ordered]@{ Arm = 'B';    Rep = 1; Triplet = 1; Order = 3 },
        [ordered]@{ Arm = 'AMIN'; Rep = 2; Triplet = 2; Order = 4 },
        [ordered]@{ Arm = 'B';    Rep = 2; Triplet = 2; Order = 5 },
        [ordered]@{ Arm = 'A';    Rep = 2; Triplet = 2; Order = 6 },
        [ordered]@{ Arm = 'B';    Rep = 3; Triplet = 3; Order = 7 },
        [ordered]@{ Arm = 'A';    Rep = 3; Triplet = 3; Order = 8 },
        [ordered]@{ Arm = 'AMIN'; Rep = 3; Triplet = 3; Order = 9 }
    )

    # Check if all 9 calibration runs are already complete (idempotency)
    $completedMap = @{}
    foreach ($item in $plan) {
        $tid = ('cal_{0}_{1:D2}' -f $item.Arm, $item.Rep)
        $outPath = Join-Path $Layout.Calibration ($tid + '.json')
        $hasSuccessInLedger = @($finishedSuccess | Where-Object { $_.trial_id -eq $tid }).Count -gt 0
        if ($hasSuccessInLedger -and (Test-Path -LiteralPath $outPath)) {
            $j = Read-JsonFile $outPath
            if ($j -and $j.status -eq 'SUCCESS' -and $j.result -and $j.result.throughput) {
                $completedMap[$tid] = $j
            }
        }
    }
    $allSuccessful = ($completedMap.Count -eq $plan.Count)

    if ($allSuccessful) {
        Write-Ok ('calibration already complete (9/9 runs); no trials executed')
    } else {
        if ($DryRun) {
            Write-Host 'DRY RUN: 9 calibration runs planned under gate G3 (PF block, balanced Latin-square order, r=16, 5s window, 3 reps x 3 arms).' -ForegroundColor Yellow
            foreach ($item in $plan) {
                $tid = ('cal_{0}_{1:D2}' -f $item.Arm, $item.Rep)
                $status = if ($completedMap.ContainsKey($tid)) { 'ALREADY_COMPLETED' } else { 'PLANNED' }
                Write-Host ('  order={0} {1,-15} arm={2,-5} rep={3} triplet={4} status={5}' -f $item.Order, $tid, $item.Arm, $item.Rep, $item.Triplet, $status)
            }
            exit 0
        }

        # Execute remaining runs, enforcing attempt ceiling (hard ceiling = 12 attempts: 9 planned + 3 replacements)
        $maxCalibrationAttempts = 12

        foreach ($item in $plan) {
            $arm = $item.Arm
            $rep = $item.Rep
            $tid = ('cal_{0}_{1:D2}' -f $arm, $rep)
            if ($completedMap.ContainsKey($tid)) {
                $j = $completedMap[$tid]
                Write-Info ('[cal] {0} already completed ({1:N1} ops/s); skipping' -f $tid, [double]$j.result.throughput.read_ops_per_sec)
                continue
            }

            # Enforce execution budget
            $startedCount = @($startedAttempts).Count
            if ($startedCount -ge $maxCalibrationAttempts) {
                throw ('stopping rule: calibration attempts ({0}) reached hard ceiling ({1})' -f $startedCount, $maxCalibrationAttempts)
            }

            $seed = [int64]([Convert]::ToUInt64((Get-TextSha256 ('cal|triplet|' + $item.Triplet)).Substring(0, 15), 16))
            $spec = [ordered]@{
                schema = 'i7.trial-spec.v1'; trial_id = $tid; block = 'PF'; cell = 'calibration'; triplet = $item.Triplet
                arm = $arm; order_position = $item.Order; runlist_position = 0; seed = $seed
                workload_id = 'WORKLOAD_A_READ_ONLY'; reader_concurrency = 16; writer_concurrency = 0
                measurement_duration_ms = 5000; warmup_duration_ms = 1000; cooldown_duration_ms = 100
                keyspace_cardinality = 10000; initial_sstable_count = 4; initial_keys_per_sstable = 2500
                target_read_hit_ratio = 0.95; memtable_threshold_bytes = 67108864
                flush_trigger_interval_ms = 0; compaction_trigger_interval_ms = 0
                wal_sync = $true; read_distribution = 'uniform'
                data_dir = Join-Path $Layout.Calibration ('data_' + $tid); expected_reader_set_size = 4
            }
            $specPath = Join-Path $Layout.Specs ($tid + '.json')
            Write-JsonFile -Path $specPath -Object $spec
            $outPath = Join-Path $Layout.Calibration ($tid + '.json')

            # Record STARTED
            Add-JsonLine -Path $ledgerPath -Object ([ordered]@{ event = 'STARTED'; trial_id = $tid; arm = $arm; rep = $rep; triplet = $item.Triplet; order = $item.Order; ts = Get-UtcStamp })
            $startedAttempts = @(Read-JsonLines $ledgerPath | Where-Object { $_.event -eq 'STARTED' })

            $argLine = @(
                (Quote-Arg '-spec'), (Quote-Arg $specPath), (Quote-Arg '-out'), (Quote-Arg $outPath),
                (Quote-Arg '-gate'), (Quote-Arg 'G3'), (Quote-Arg '-approval'), (Quote-Arg $Layout.Approval),
                (Quote-Arg '-platform'), (Quote-Arg $Layout.Platform), (Quote-Arg '-git-rev'), (Quote-Arg $platform.git_revision)
            ) -join ' '
            $p = Start-Process -FilePath $Layout.Runner -ArgumentList $argLine -Wait -PassThru -NoNewWindow
            $exitCode = $p.ExitCode

            $j = Read-JsonFile $outPath
            $status = 'INVALID'
            if ($j -and $j.status) { $status = [string]$j.status }
            if ($exitCode -ne 0 -and $status -eq 'SUCCESS') { $status = 'FAILED' }

            Add-JsonLine -Path $ledgerPath -Object ([ordered]@{ event = 'FINISHED'; trial_id = $tid; arm = $arm; rep = $rep; triplet = $item.Triplet; order = $item.Order; status = $status; exit_code = $exitCode; ts = Get-UtcStamp })

            if ($status -eq 'SUCCESS' -and $j.result -and $j.result.throughput) {
                $completedMap[$tid] = $j
                Write-Ok ('[cal] {0} SUCCESS ({1:N1} ops/s)' -f $tid, [double]$j.result.throughput.read_ops_per_sec)
            } else {
                Write-Warn2 ('[cal] {0} {1} (exit={2})' -f $tid, $status, $exitCode)
            }
        }
    }

    # Strict completeness: all 9 planned calibration cells must have valid successful records.
    $missingCells = New-Object System.Collections.ArrayList
    foreach ($item in $plan) {
        $tid = ('cal_{0}_{1:D2}' -f $item.Arm, $item.Rep)
        if (-not $completedMap.ContainsKey($tid)) {
            [void]$missingCells.Add($tid)
        }
    }
    if ($missingCells.Count -gt 0) {
        throw ('calibration incomplete: {0}/9 cells succeeded. Missing cells: {1}' -f ($plan.Count - $missingCells.Count), ($missingCells -join ', '))
    }

    $perArm = @{}
    $runs = New-Object System.Collections.ArrayList
    foreach ($arm in 'A', 'AMIN', 'B') {
        $vals = New-Object System.Collections.ArrayList
        for ($rep = 1; $rep -le 3; $rep++) {
            $tid = ('cal_{0}_{1:D2}' -f $arm, $rep)
            $j = $completedMap[$tid]
            [void]$vals.Add([double]$j.result.throughput.read_ops_per_sec)
            [void]$runs.Add([ordered]@{ trial_id = $tid; arm = $arm; rep = $rep; read_ops_per_sec = [double]$j.result.throughput.read_ops_per_sec })
        }
        $sorted = @($vals | Sort-Object)
        $perArm[$arm] = [double]$sorted[1]
    }
    $cRef = ($perArm.Values | Measure-Object -Minimum).Minimum
    $cal = [ordered]@{
        schema = 'i7.calibration.v1'; created_utc = Get-UtcStamp; protocol_version = $script:I7.ProtocolVersion
        per_arm_read_ops_per_sec = $perArm; c_ref = [double]$cRef; rule = 'min over arms of median read ops/s at r=16, 5 s, 3 reps (balanced Latin-square order)'
        runs = $runs
    }
    Write-JsonFile -Path $Layout.CalibrationOut -Object $cal
    Write-Ok ('C_ref = {0:N1} read ops/s (min over arms: A={1:N1}, AMIN={2:N1}, B={3:N1})' -f $cRef, $perArm['A'], $perArm['AMIN'], $perArm['B'])
    Write-Info ('calibration written: ' + $Layout.CalibrationOut)
    exit 0
}

# ---------------------------------------------------------------------------
# Executing modes
# ---------------------------------------------------------------------------
function Invoke-Run {
    param([string]$Mode)

    $Layout = Resolve-Runners (Get-PathLayout)
    $requiredGate = switch ($Mode) {
        'Preflight' { 'G3' }
        'Campaign' { 'G4' }
        'Resume' { 'G4' }
        'Diagnostics' { 'G5' }
        default { '' }
    }
    if ([string]::IsNullOrWhiteSpace($Gate)) { $Gate = $requiredGate }
    if ($Gate -ne $requiredGate) { throw ("gate {0} cannot be used for mode {1} (requires {2})" -f $Gate, $Mode, $requiredGate) }

    Write-Section ("I7 {0} (gate {1})" -f $Mode, $Gate)
    if (Test-UnderFrozenRoot $Root) { throw 'results root is inside a frozen campaign directory' }

    if ($Mode -eq 'Diagnostics') {
        throw 'G5 diagnostics are not implemented in this build; the gate exists so it cannot be confused with G3/G4.'
    }

    $platform = Read-JsonFile $Layout.Platform
    if ($null -eq $platform) { throw 'platform_manifest.json missing; run -Check first' }
    if (-not (Test-Path -LiteralPath $Layout.Runner)) {
        $runner = Build-Runner -Package '.\research\experiments\phase_i7\cmd\i7trial' -OutPath $Layout.Runner
        if (-not $runner) { throw 'i7trial build failed' }
        $platform.runner = $runner
        Write-JsonFile -Path $Layout.Platform -Object $platform
    }

    $run = Get-OrCreateRunlist -Layout $Layout
    $runnerInfo = [ordered]@{ sha256 = (Get-Sha256 $Layout.Runner) }
    $manifest = Initialize-CampaignManifest -Layout $Layout -Runlist $run -Platform $platform -RunnerInfo $runnerInfo

    $phase = if ($Mode -eq 'Preflight') { 'preflight' } else { 'confirmatory' }
    $candidates = @($run.Rows | Where-Object { $_.phase -eq $phase })
    if ($Blocks.Count -gt 0) {
        $candidates = @($candidates | Where-Object { $Blocks -contains $_.block })
    }
    if ($candidates.Count -eq 0) { throw 'no runlist rows selected' }

    $unsupported = @($candidates | Where-Object { $_.runner_support -ne 'ready' })
    $ready = @($candidates | Where-Object { $_.runner_support -eq 'ready' })
    Write-Info ('selected rows: {0} (ready: {1}, blocked: {2})' -f $candidates.Count, $ready.Count, $unsupported.Count)
    if ($unsupported.Count -gt 0) {
        foreach ($b in ($unsupported | Group-Object block)) {
            Write-Warn2 ('  {0}: {1} rows - {2}' -f $b.Name, $b.Count, ($b.Group | Select-Object -First 1).note)
        }
        if (-not $AllowPartial -and -not $DryRun) {
            Write-Host 'REFUSING: the selected set includes blocks that are not ready.' -ForegroundColor Red
            exit 3
        }
        if ($AllowPartial) {
            foreach ($prop in 'partial', 'partial_reason') {
                if (-not ($manifest.PSObject.Properties[$prop])) { $manifest | Add-Member -NotePropertyName $prop -NotePropertyValue $null -Force }
            }
            $manifest.partial = $true
            $manifest.partial_reason = 'blocked blocks excluded: ' + (($unsupported | Group-Object block | ForEach-Object { $_.Name }) -join ',')
            Save-Manifest -Layout $Layout -Manifest $manifest
            $candidates = $ready
        }
    }

    $selectedBlocks = @($candidates | ForEach-Object { $_.block } | Sort-Object -Unique)
    $approval = $null
    if (-not $DryRun) {
        $approval = Assert-Approval -Layout $Layout -Runlist $run -RequiredGate $Gate -SelectedBlocks $selectedBlocks -SelectedCount $candidates.Count
        if (-not ($manifest.PSObject.Properties['allow_dirty'])) { $manifest | Add-Member -NotePropertyName allow_dirty -NotePropertyValue $false -Force }
        if (-not ($manifest.PSObject.Properties['execution_started_utc'])) { $manifest | Add-Member -NotePropertyName execution_started_utc -NotePropertyValue '' -Force }
        $manifest.allow_dirty = [bool](PropOr $approval 'allow_dirty' $false)
        if ([string](PropOr $manifest 'execution_started_utc' '') -eq '') { $manifest.execution_started_utc = Get-UtcStamp }
        Save-Manifest -Layout $Layout -Manifest $manifest
        Assert-ExecutionIntegrity -Layout $Layout -Manifest $manifest
    }
    else {
        Write-Warn2 ("dry run: approval gate {0} is NOT checked" -f $Gate)
        Assert-ExecutionIntegrity -Layout $Layout -Manifest $manifest
    }

    $ledger = @(Read-JsonLines $Layout.Results)
    $started = @($ledger | Where-Object { $_.event -eq 'STARTED' })
    $finished = @($ledger | Where-Object { $_.event -eq 'FINISHED' })
    $executedAttempts = $started.Count
    if ($MaxTrials -le 0) { $MaxTrials = $script:I7.HardCeiling }
    if ($MaxTrials -gt $script:I7.HardCeiling) { throw ("-MaxTrials {0} exceeds the hard ceiling {1}" -f $MaxTrials, $script:I7.HardCeiling) }
    $remaining = $MaxTrials - $executedAttempts
    Write-Info ('attempts recorded: {0}; ceiling: {1}; remaining: {2}' -f $executedAttempts, $MaxTrials, $remaining)
    if ($remaining -le 0) { throw 'hard trial ceiling reached; no further trials may run' }

    $successByTrial = @{}
    foreach ($f in $finished) { if ($f.status -eq 'SUCCESS') { $successByTrial[[string]$f.trial_id] = $true } }
    $attemptsByTrial = @{}
    foreach ($s in $started) {
        $k = [string]$s.trial_id
        if (-not $attemptsByTrial.ContainsKey($k)) { $attemptsByTrial[$k] = 0 }
        $attemptsByTrial[$k] = $attemptsByTrial[$k] + 1
    }

    $calibration = $null
    if (-not [string]::IsNullOrWhiteSpace($CalibrationFile)) {
        $calibration = Read-JsonFile ([System.IO.Path]::GetFullPath($CalibrationFile))
        if ($null -eq $calibration) { throw ('calibration file not found or invalid: ' + $CalibrationFile) }
    }
    elseif (Test-Path -LiteralPath $Layout.CalibrationOut) {
        $calibration = Read-JsonFile $Layout.CalibrationOut
    }

    $plan = New-Object System.Collections.ArrayList
    foreach ($row in $candidates) {
        $tid = [string]$row.trial_id
        if ($successByTrial.ContainsKey($tid)) { continue }
        $prior = 0
        if ($attemptsByTrial.ContainsKey($tid)) { $prior = $attemptsByTrial[$tid] }
        if ($prior -ge 2) {
            Write-Warn2 ('{0}: already attempted {1} times; not retried' -f $tid, $prior)
            continue
        }
        if ($prior -ge 1) {
            if ([int]$manifest.replacements_used -ge $script:I7.ReplacementCap) {
                Write-Warn2 'replacement budget exhausted; remaining failures stay as failures'
                continue
            }
            $manifest.replacements_used = [int]$manifest.replacements_used + 1
            Save-Manifest -Layout $Layout -Manifest $manifest
            Write-Info ('replacement {0}/{1} for {2}' -f $manifest.replacements_used, $script:I7.ReplacementCap, $tid)
        }
        if ($plan.Count -ge $remaining) { break }
        [void]$plan.Add([pscustomobject]@{ Row = $row; Attempt = ($prior + 1) })
    }
    Write-Info ('trials to execute now: {0}; replacements used: {1}/{2}' -f $plan.Count, $manifest.replacements_used, $script:I7.ReplacementCap)

    if ($DryRun) {
        $skipped = 0
        foreach ($item in $plan) {
            try {
                $spec = New-TrialSpec -Row $item.Row -Layout $Layout -Runlist $run -Calibration $calibration
                Write-JsonFile -Path (Join-Path $Layout.Specs ($item.Row.trial_id + '.json')) -Object $spec
            }
            catch {
                $skipped++
                if ($skipped -le 3) { Write-Warn2 ('spec skipped: ' + $_.Exception.Message) }
            }
        }
        Write-Section 'Dry run summary'
        $byBlock = $plan | Group-Object { $_.Row.block } | Sort-Object Name | ForEach-Object { '{0}={1}' -f $_.Name, $_.Count }
        Write-Info ('specs written to: ' + $Layout.Specs)
        Write-Info ('specs generated: {0}; skipped: {1}' -f ($plan.Count - $skipped), $skipped)
        Write-Info ('would execute: ' + ($byBlock -join ' '))
        Write-Info ('estimated wall clock: {0:N1} h' -f (($plan.Count * 27) / 3600.0))
        Write-Host ''
        Write-Host 'DRY RUN COMPLETE - no trial process was launched.' -ForegroundColor Yellow
        exit 0
    }

    foreach ($item in $plan) {
        Assert-Resources -Layout $Layout -Manifest $manifest
        $result = Invoke-Trial -Row $item.Row -Layout $Layout -Runlist $run -Manifest $manifest -Gate $Gate `
            -ApprovalPath $Layout.Approval -GitRev $platform.git_revision -Calibration $calibration -Attempt $item.Attempt
        if ($result.status -ne 'SUCCESS') { Start-Sleep -Milliseconds 200 }
    }

    New-ResultPackage -Layout $Layout -Platform $platform -Runlist $run -Manifest $manifest | Out-Null
    exit 0
}

# ---------------------------------------------------------------------------
# Dispatch
# ---------------------------------------------------------------------------
$modes = @()
if ($Check) { $modes += 'Check' }
if ($Calibrate) { $modes += 'Calibrate' }
if ($Preflight) { $modes += 'Preflight' }
if ($Campaign) { $modes += 'Campaign' }
if ($Resume) { $modes += 'Resume' }
if ($Diagnostics) { $modes += 'Diagnostics' }
if ($Package) { $modes += 'Package' }

if ($WriteApprovalTemplate -and $modes.Count -le 1) {
    $Layout = Get-PathLayout
    $run = Get-OrCreateRunlist -Layout $Layout
    $scope = if ($Scope) { $Scope } elseif ($Calibrate) { 'calibration' } else { '' }
    $null = Write-ApprovalTemplate -Layout $Layout -Runlist $run -ForGate ($(if ($Gate) { $Gate } else { 'G3' })) -ForScope $scope
    exit 0
}

# Assert-ExecutionRevoked refuses every mode that can launch a trial process.
#
# R1.2e re-registered the campaign model (12 cells, confirmatory N in [8,10]
# per cell, D3-01..04 milestone anchors, a strict 400-run global ceiling, and
# a Rule S-1 replacement budget of 16). The G2-era execution authority
# embedded in this driver (a 366-row inventory, ceiling 384, replacement 18)
# conflicts with that model. Per the R2 engineering acceptance directive
# ("remove conflicting legacy execution authority or make the old execution
# path fail closed") the legacy path fails closed: it cannot be re-enabled by
# any flag, approval file, or -TestMode. The authoritative run-list model is
# the runlist package (runlistctl generate|validate|ceiling); rebuilding the
# execution path around it is the pilot gate's work.
function Assert-ExecutionRevoked([string]$Mode) {
    Write-Host ''
    Write-Host 'EXECUTION REFUSED (exit 5): the G2-era execution authority was revoked by the R1.2e protocol.' -ForegroundColor Red
    Write-Host ('  refused mode : ' + $Mode)
    Write-Host '  reason       : the embedded 366-row inventory (ceiling 384, replacement 18) conflicts with'
    Write-Host '                 the R1.2e campaign model (12 cells, N in [8,10], D3 milestones, ceiling 400,'
    Write-Host '                 replacement budget 16) and can no longer authorize an execution.'
    Write-Host '  authority    : research/experiments/phase_i7/runlist  (runlistctl generate|validate|ceiling)'
    Write-Host '  still usable : -Check, -DryRun (spec and run-list inspection only; launches nothing),'
    Write-Host '                 -WriteApprovalTemplate, -Package'
    Write-Host '  next step    : the pilot-authorization gate must rebuild the execution path around the'
    Write-Host '                 validated runlistctl generator before any trial is launched.'
    exit 5
}

if ($modes.Count -eq 0) {
    Write-Host 'Phase I7 Windows campaign driver' -ForegroundColor Cyan
    Write-Host ''
    Write-Host '  .\run_i7.ps1 -Check                                  validate host, build runners'
    Write-Host '  .\run_i7.ps1 -WriteApprovalTemplate -Gate G3         emit an unapproved template'
    Write-Host '  .\run_i7.ps1 -DryRun -Preflight                      generate specs, launch nothing'
    Write-Host '  .\run_i7.ps1 -Calibrate                              G3: nine runs -> calibration.json'
    Write-Host '  .\run_i7.ps1 -Preflight                              G3: 12-trial preflight'
    Write-Host '  .\run_i7.ps1 -DryRun -Campaign                       generate campaign specs'
    Write-Host '  .\run_i7.ps1 -Campaign                               G4: confirmatory campaign'
    Write-Host '  .\run_i7.ps1 -Resume                                 G4: continue after interruption'
    Write-Host '  .\run_i7.ps1 -Diagnostics                            G5: profiling (not implemented)'
    Write-Host '  .\run_i7.ps1 -Package                                summary + checksums + ZIP'
    Write-Host ''
    Write-Host 'EXECUTION NOTICE: -Calibrate/-Preflight/-Campaign/-Resume/-Diagnostics are REVOKED (exit 5)' -ForegroundColor Yellow
    Write-Host 'by the R1.2e protocol; only -DryRun inspection, -Check, -WriteApprovalTemplate and -Package run.' -ForegroundColor Yellow
    Write-Host 'The authoritative run-list model is research/experiments/phase_i7/runlist (runlistctl).' -ForegroundColor Yellow
    Write-Host ''
    Write-Host 'Options: -Root <dir> -Affinity 0x3f -Blocks E1,E3 -MaxTrials N -AllowPartial'
    Write-Host '         -MaxCampaignMinutes N -MinFreeGB N -TestMode -RunnerOverride <exe>'
    exit 2
}
if ($modes.Count -gt 1) {
    Write-Host ('Specify exactly one mode; got: ' + ($modes -join ', ')) -ForegroundColor Red
    exit 2
}
if (($RunnerOverride -or $PebbleRunnerOverride) -and -not $TestMode) {
    Write-Host 'Runner overrides require -TestMode.' -ForegroundColor Red
    exit 2
}

$mode = $modes[0]

# The frozen campaigns must never be written to, not even by a layout helper.
# This guard runs before any mode can create a directory.
if (Test-UnderFrozenRoot $Root) {
    Write-Host ('ERROR: results root is inside a frozen campaign directory: ' + $Root) -ForegroundColor Red
    exit 1
}

try {
    switch ($mode) {
        'Check' { Invoke-Check }
        'Calibrate' {
            if (-not $DryRun) { Assert-ExecutionRevoked $mode }
            Invoke-Calibrate
        }
        'Package' {
            $Layout = Get-PathLayout
            $platform = Read-JsonFile $Layout.Platform
            if ($null -eq $platform) { throw 'platform_manifest.json missing; run -Check first' }
            $run = Get-OrCreateRunlist -Layout $Layout
            $manifest = Read-JsonFile $Layout.Manifest
            if ($null -eq $manifest) { throw 'campaign_manifest.json missing; run a campaign mode first' }
            New-ResultPackage -Layout $Layout -Platform $platform -Runlist $run -Manifest $manifest | Out-Null
            exit 0
        }
        default {
            # Preflight / Campaign / Resume / Diagnostics can launch trial
            # processes; only their -DryRun (no launch) inspection path remains.
            if (-not $DryRun) { Assert-ExecutionRevoked $mode }
            Invoke-Run -Mode $mode
        }
    }
}
catch {
    Write-Host ''
    Write-Host ('ERROR: ' + $_.Exception.Message) -ForegroundColor Red
    exit 1
}
