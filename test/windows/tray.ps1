[CmdletBinding()]
param([Parameter(Mandatory)][string]$BinaryPath)

$ErrorActionPreference = 'Stop'
$tray = Join-Path $PSScriptRoot '..\..\scripts\windows-tray.ps1'
$cli = (Get-Item -LiteralPath $BinaryPath).FullName
$log = [IO.Path]::GetTempFileName()
$launch = @{
    FilePath = "$PSHOME\powershell.exe"
    ArgumentList = "-NoProfile -NonInteractive -ExecutionPolicy Bypass -STA -File `"$tray`" -BinaryPath `"$cli`""
    RedirectStandardError = $log
    PassThru = $true
}
try {
    $process = Start-Process @launch
    Start-Sleep -Seconds 3
    if ($process.HasExited) {
        throw "Tray exited ($($process.ExitCode)): $(Get-Content -LiteralPath $log -Raw)"
    }
    Write-Output 'Tray initialized and entered its WPF dispatcher loop.'
} finally {
    if ($process) {
        if (-not $process.HasExited) { Stop-Process -Id $process.Id -Force }
        $process.Dispose()
    }
    Remove-Item -LiteralPath $log -Force
}
