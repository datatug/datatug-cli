$ErrorActionPreference = "Stop"

# Install an official DataTug CLI release without a language toolchain.
# DATATUG_INSTALL_DIR may override the default per-user destination.
# DATATUG_VERSION may pin a release tag such as v0.32.0.
$Repo = "https://github.com/datatug/datatug-cli"
$InstallDir = if ($env:DATATUG_INSTALL_DIR) {
    $env:DATATUG_INSTALL_DIR
} else {
    Join-Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)) "DataTug\bin"
}
$Version = $env:DATATUG_VERSION
$WorkDir = $null
$DestinationTemp = $null

function Fail([string]$Message) {
    throw "datatug install: $Message"
}

try {
    $Architecture = if ($env:PROCESSOR_ARCHITEW6432) {
        $env:PROCESSOR_ARCHITEW6432
    } else {
        $env:PROCESSOR_ARCHITECTURE
    }
    if ($Architecture -notin @("AMD64", "x86_64")) {
        Fail "unsupported Windows architecture: $Architecture (the current release supports amd64)"
    }

    if (-not $Version) {
        try {
            $Release = Invoke-RestMethod -Uri "https://api.github.com/repos/datatug/datatug-cli/releases/latest"
            $Version = $Release.tag_name
        } catch {
            Fail "could not resolve the latest release: $($_.Exception.Message)"
        }
    }
    if ($Version -notmatch '^v[0-9]') {
        Fail "release version must be a tag such as v0.32.0"
    }

    $AssetVersion = $Version.Substring(1)
    $Archive = "datatug_${AssetVersion}_windows_amd64.zip"
    $Checksums = "datatug_${AssetVersion}_checksums.txt"
    $ReleaseUrl = "$Repo/releases/download/$Version"
    $WorkDir = Join-Path ([IO.Path]::GetTempPath()) ("datatug-install." + [Guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $WorkDir | Out-Null
    $ChecksumsPath = Join-Path $WorkDir $Checksums
    $ArchivePath = Join-Path $WorkDir $Archive

    try {
        Invoke-WebRequest -Uri "$ReleaseUrl/$Checksums" -OutFile $ChecksumsPath
        Invoke-WebRequest -Uri "$ReleaseUrl/$Archive" -OutFile $ArchivePath
    } catch {
        Fail "could not download $Archive and its checksums: $($_.Exception.Message)"
    }

    $EscapedArchive = [Regex]::Escape($Archive)
    $ChecksumLine = Get-Content $ChecksumsPath | Where-Object { $_ -match "^([0-9a-fA-F]{64})\s+\*?$EscapedArchive$" } | Select-Object -First 1
    if (-not $ChecksumLine) {
        Fail "$Archive is missing from $Checksums"
    }
    $Expected = ([Regex]::Match($ChecksumLine, '^([0-9a-fA-F]{64})')).Groups[1].Value.ToLowerInvariant()
    $Actual = (Get-FileHash -Algorithm SHA256 -Path $ArchivePath).Hash.ToLowerInvariant()
    if ($Actual -ne $Expected) {
        Fail "SHA-256 checksum verification failed"
    }

    $Expanded = Join-Path $WorkDir "expanded"
    Expand-Archive -Path $ArchivePath -DestinationPath $Expanded
    $Executable = Join-Path $Expanded "datatug.exe"
    if (-not (Test-Path -LiteralPath $Executable -PathType Leaf)) {
        Fail "release archive does not contain datatug.exe"
    }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $Destination = Join-Path $InstallDir "datatug.exe"
    $DestinationTemp = Join-Path $InstallDir ".datatug.install.$PID.exe"
    Copy-Item -LiteralPath $Executable -Destination $DestinationTemp
    Move-Item -LiteralPath $DestinationTemp -Destination $Destination -Force
    $DestinationTemp = $null

    Write-Host "Installed datatug $Version at $Destination"
    $PathEntries = $env:PATH -split ';'
    if ($InstallDir -notin $PathEntries) {
        Write-Host "Add DataTug to PATH for this PowerShell session:"
        Write-Host "`$env:PATH = `"$InstallDir;`$env:PATH`""
    }
} finally {
    if ($DestinationTemp -and (Test-Path -LiteralPath $DestinationTemp)) {
        Remove-Item -LiteralPath $DestinationTemp -Force
    }
    if ($WorkDir -and (Test-Path -LiteralPath $WorkDir)) {
        Remove-Item -LiteralPath $WorkDir -Recurse -Force
    }
}
