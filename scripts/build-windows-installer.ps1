[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidatePattern('^\d+\.\d+\.\d+$')][string]$Version,
    [ValidateSet('amd64', 'arm64')][string]$Arch = 'amd64',
    [string]$OutputDirectory,
    [string]$Wix = 'wix.exe',
    [string]$SignTool = 'signtool.exe',
    [string]$CertificateThumbprint,
    [string]$TimestampServer = 'http://timestamp.digicert.com',
    [switch]$Unsigned
)

$ErrorActionPreference = 'Stop'
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $PSScriptRoot '..\dist' }
if (-not $Unsigned -and $CertificateThumbprint -notmatch '^[A-Fa-f0-9]{40}$') {
    throw 'Supply a code-signing certificate thumbprint or use -Unsigned for local testing.'
}
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$out = (New-Item -ItemType Directory -Force -Path $OutputDirectory).FullName
$work = (New-Item -ItemType Directory -Path (Join-Path ([IO.Path]::GetTempPath()) "Antitimely-$([Guid]::NewGuid())")).FullName
$cli = Join-Path $work 'antitimely.exe'
$daemon = Join-Path $work 'antitimely-daemon.exe'
$msi = Join-Path $work "Antitimely-$Version-$Arch.msi"

function Invoke-BuildTool {
    param([string]$Tool, [string[]]$ArgList)
    & $Tool @ArgList
    if ($LASTEXITCODE -ne 0) {
        throw "$Tool exited with code $LASTEXITCODE."
    }
}

function Protect-ReleaseFile {
    param([string]$File)
    if ($Unsigned) { return }
    Invoke-BuildTool $SignTool @('sign', '/sha1', $CertificateThumbprint, '/fd', 'SHA256', '/tr', $TimestampServer, '/td', 'SHA256', $File)
    Invoke-BuildTool $SignTool @('verify', '/pa', $File)
}

$oldArch = $env:GOARCH
$oldOS = $env:GOOS
$oldCgo = $env:CGO_ENABLED
Push-Location -LiteralPath $repo
try {
    $env:GOOS = 'windows'
    $env:GOARCH = $Arch
    $env:CGO_ENABLED = '0'
    Invoke-BuildTool 'go.exe' @('build', '-trimpath', '-o', $cli, '.')
    Invoke-BuildTool 'go.exe' @('build', '-trimpath', '-ldflags=-H=windowsgui', '-o', $daemon, '.')
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'windows-startup.ps1'), (Join-Path $PSScriptRoot 'windows-tray.ps1'),
        (Join-Path $PSScriptRoot 'antitimely.ico') -Destination $work
    Protect-ReleaseFile $cli
    Protect-ReleaseFile $daemon
    $wixArch = if ($Arch -eq 'amd64') { 'x64' } else { 'arm64' }
    Invoke-BuildTool $Wix @('build', '-arch', $wixArch, '-d', "BinDir=$work", '-d', "Version=$Version",
        '-o', $msi, (Join-Path $repo 'packaging\windows\Antitimely.wxs'))
    Protect-ReleaseFile $msi
    Move-Item -LiteralPath $msi -Destination $out -Force
    Write-Output (Join-Path $out ([IO.Path]::GetFileName($msi)))
} finally {
    Pop-Location
    $env:GOARCH = $oldArch
    $env:GOOS = $oldOS
    $env:CGO_ENABLED = $oldCgo
    Remove-Item -LiteralPath $work -Recurse -Force
}
