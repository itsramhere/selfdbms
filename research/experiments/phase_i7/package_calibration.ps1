param(
    [string]$Root = 'C:\Users\ramst\I7_WINDOWS_POSTPATCH_CAEFC7B'
)

$files = Get-ChildItem -Path $Root -Recurse -File | Where-Object { 
    $_.FullName -notmatch '\\bin\\' -and 
    $_.Name -notmatch '\.zip$' -and 
    $_.Name -ne 'checksums.sha256' 
}

$checksumLines = @()
foreach ($f in ($files | Sort-Object FullName)) {
    $h = (Get-FileHash -Algorithm SHA256 $f.FullName).Hash.ToLower()
    $rel = $f.FullName.Substring($Root.Length).TrimStart('\', '/') -replace '\\', '/'
    $checksumLines += ('{0}  {1}' -f $h, $rel)
}
$chkPath = Join-Path $Root 'checksums.sha256'
[System.IO.File]::WriteAllLines($chkPath, $checksumLines)
Write-Host ('checksums written: ' + $chkPath)

$zipPath = Join-Path $Root 'I7_G3_CALIBRATION_RESULTS.zip'
if (Test-Path $zipPath) { Remove-Item $zipPath }
$toCompress = @(Get-ChildItem -Path $Root | Where-Object { $_.Name -ne 'bin' -and $_.Name -ne 'I7_G3_CALIBRATION_RESULTS.zip' } | ForEach-Object { $_.FullName })
Compress-Archive -Path $toCompress -DestinationPath $zipPath
$zipHash = (Get-FileHash -Algorithm SHA256 $zipPath).Hash
Write-Host ('ZIP created: ' + $zipPath)
Write-Host ('ZIP SHA256: ' + $zipHash)
