[CmdletBinding()]
param([Parameter(Mandatory)][string]$BinaryPath)

$ErrorActionPreference = 'Stop'
$setup = Join-Path $PSScriptRoot '..\..\scripts\windows-startup.ps1'
$instance = 'test-' + [Guid]::NewGuid().ToString('N').Substring(0, 16)
$state = Join-Path ([Environment]::GetFolderPath('UserProfile')) ".antitimely-$instance"
$sid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$taskName = "Antitimely-$sid-$instance"
$log = Join-Path $state 'daemon.log'
$pidFile = Join-Path $state 'antitimely.pid'

function Wait-ForTracker($previousId, $timeout, $readyCount) {
    $deadline = [DateTime]::UtcNow.AddSeconds($timeout)
    do {
        Start-Sleep -Milliseconds 250
        if (-not (Test-Path -LiteralPath $pidFile)) { continue }
        $id = [int](Get-Content -LiteralPath $pidFile -Raw -ErrorAction SilentlyContinue)
        if ($id -le 0 -or $id -eq $previousId) { continue }
        $process = Get-Process -Id $id -ErrorAction SilentlyContinue
        if ($process -and $process.SessionId -gt 0 -and $process.Path -eq $fullBinary -and
            (Test-Path -LiteralPath $log) -and @(Select-String -Path $log -SimpleMatch 'antitimely daemon ready').Count -ge $readyCount) {
            return $id
        }
    } while ([DateTime]::UtcNow -lt $deadline)
    $task = Get-ScheduledTask -TaskName $taskName
    $info = Get-ScheduledTaskInfo -TaskName $taskName
    $recentLog = if (Test-Path -LiteralPath $log) { Get-Content -LiteralPath $log -Tail 12 | Out-String } else { 'Log was not created.' }
    throw "Waiting for startup $readyCount after PID $previousId failed. Task state $($task.State), last result $($info.LastTaskResult).`n$recentLog"
}

$fullBinary = (Get-Item -LiteralPath $BinaryPath).FullName
try {
    New-Item -ItemType Directory -Path $state | Out-Null
    Set-Content -LiteralPath (Join-Path $state 'config.yaml') -Value 'transcript_tracking: false' -Encoding ascii
    & $setup -Action Install -Instance $instance -BinaryPath $BinaryPath
    $task = Get-ScheduledTask -TaskName $taskName
    if ($task.Triggers.CimClass.CimClassName -notcontains 'MSFT_TaskLogonTrigger' -or
        $task.Principal.LogonType -ne 'Interactive' -or $task.Settings.RestartCount -lt 1) {
        throw 'Expected a per-user interactive logon task with crash recovery.'
    }
    $original = Wait-ForTracker 0 30 1
    Stop-Process -Id $original -Force
    $recovered = Wait-ForTracker $original 25 2
    & $setup -Action Restart -Instance $instance
    $restarted = Wait-ForTracker $recovered 30 3
    $session = (Get-Process -Id $restarted).SessionId
    & $setup -Action Stop -Instance $instance
    $oldProcess = Get-Process -Id $restarted -ErrorAction SilentlyContinue
    if ($oldProcess -and -not $oldProcess.WaitForExit(5000)) {
        throw 'Stopping the scheduled task left the daemon process running.'
    }
    Write-Output "Task launched in session $session, recovered from process termination, and stopped cleanly."
} finally {
    try {
        & $setup -Action Uninstall -Instance $instance
    } finally {
        if (Test-Path -LiteralPath $state) {
            Remove-Item -LiteralPath $state -Recurse -Force
        }
    }
}
