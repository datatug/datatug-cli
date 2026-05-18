# DataTug CLI installer (Windows / PowerShell)
#
# Usage:
#   powershell -c "irm https://datatug.io/install/get-cli.ps1 | iex"
#
# Environment variables:
#   DATATUG_VERSION      Version tag to install (default: latest)
#   DATATUG_INSTALL_DIR  Install location (default: %LOCALAPPDATA%\Programs\datatug\bin)

$ErrorActionPreference = 'Stop'

# Ensure modern TLS on older PowerShell hosts (Win10 / WMF 5.1 default to TLS 1.0).
try {
    [Net.ServicePointManager]::SecurityProtocol =
        [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {}

$Repo    = 'datatug/datatug-cli'
$Project = 'datatug'
$BinName = "$Project.exe"

function Write-Info($msg) { Write-Host $msg }
function Die($msg) { Write-Error $msg; exit 1 }

# --- Detect architecture ---------------------------------------------------
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { Die "unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}
if ($arch -eq 'arm64') {
    Die 'windows/arm64 is not released; please build from source with `go install`.'
}

# --- Resolve version -------------------------------------------------------
$version = if ($env:DATATUG_VERSION) { $env:DATATUG_VERSION } else { 'latest' }
if ($version -eq 'latest') {
    try {
        $rel = Invoke-RestMethod -UseBasicParsing `
            -Uri "https://api.github.com/repos/$Repo/releases/latest"
        $version = $rel.tag_name
    } catch {
        Die "failed to resolve latest release tag from GitHub: $_"
    }
    if (-not $version) { Die 'failed to resolve latest release tag from GitHub' }
}
$verNoV = $version.TrimStart('v')

$archive       = "${Project}_${verNoV}_windows_${arch}.zip"
$baseUrl       = "https://github.com/$Repo/releases/download/$version"
$archiveUrl    = "$baseUrl/$archive"
$checksumsUrl  = "$baseUrl/${Project}_${verNoV}_checksums.txt"

# --- Resolve install directory --------------------------------------------
$installDir = if ($env:DATATUG_INSTALL_DIR) {
    $env:DATATUG_INSTALL_DIR
} else {
    Join-Path $env:LOCALAPPDATA "Programs\$Project\bin"
}
New-Item -ItemType Directory -Path $installDir -Force | Out-Null

# --- Download, verify, install --------------------------------------------
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("$Project-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp -Force | Out-Null

try {
    Write-Info "$Project $version (windows/$arch)"
    Write-Info "  archive: $archiveUrl"

    $archivePath = Join-Path $tmp $archive
    try {
        Invoke-WebRequest -UseBasicParsing -Uri $archiveUrl -OutFile $archivePath
    } catch {
        Die "download failed: $archiveUrl ($_)"
    }

    # Verify checksum if the manifest is available.
    try {
        $checksumsPath = Join-Path $tmp 'checksums.txt'
        Invoke-WebRequest -UseBasicParsing -Uri $checksumsUrl -OutFile $checksumsPath
        $expectedLine = (Get-Content $checksumsPath) | Where-Object { $_ -match "\s$([regex]::Escape($archive))\s*$" }
        if ($expectedLine) {
            $expected = ($expectedLine -split '\s+')[0].ToLower()
            $actual   = (Get-FileHash -Algorithm SHA256 -Path $archivePath).Hash.ToLower()
            if ($expected -ne $actual) {
                Die "checksum mismatch for $archive (expected $expected, got $actual)"
            }
            Write-Info '  checksum: OK'
        } else {
            Write-Info '  checksum: skipped (entry not found in manifest)'
        }
    } catch {
        Write-Info '  checksum: skipped (manifest not available)'
    }

    Write-Info '  extracting...'
    $extractDir = Join-Path $tmp 'extract'
    Expand-Archive -Path $archivePath -DestinationPath $extractDir -Force

    $src = Join-Path $extractDir $BinName
    if (-not (Test-Path $src)) {
        Die "binary not found in archive: $BinName"
    }

    $dst = Join-Path $installDir $BinName
    Copy-Item -Path $src -Destination $dst -Force

    Write-Info "installed $Project $version to $dst"
}
finally {
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $tmp
}

# --- PATH advisory --------------------------------------------------------
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$pathEntries = if ($userPath) { $userPath -split ';' } else { @() }
$alreadyOnPath = $pathEntries | Where-Object {
    $_ -and ($_.TrimEnd('\') -ieq $installDir.TrimEnd('\'))
}

if (-not $alreadyOnPath) {
    $newUserPath = if ($userPath) { "$userPath;$installDir" } else { $installDir }
    [Environment]::SetEnvironmentVariable('Path', $newUserPath, 'User')
    Write-Info ''
    Write-Info "added $installDir to your user PATH."
    Write-Info 'open a new terminal (or sign out and back in) for the change to take effect.'
} else {
    Write-Info ''
    Write-Info "$installDir is already on your user PATH."
}
