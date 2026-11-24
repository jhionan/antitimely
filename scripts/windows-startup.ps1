[CmdletBinding()]
param(
    [ValidateSet('Install', 'Start', 'Stop', 'Restart', 'Status', 'Uninstall')]
    [string]$Action = 'Status',
    [string]$BinaryPath,
    [ValidatePattern('^[a-zA-Z0-9-]{1,40}$')]
    [string]$Instance
)

$ErrorActionPreference = 'Stop'
if (-not $BinaryPath) { $BinaryPath = Join-Path $PSScriptRoot '..\antitimely-daemon.exe' }
$identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
$sid = $identity.User.Value
$taskName = "Antitimely-$sid"
$profilePath = [Environment]::GetFolderPath('UserProfile')
$state = Join-Path $profilePath '.antitimely'
if ($Instance) {
    $taskName += "-$Instance"
    $state += "-$Instance"
}

function Stop-TrackerTask {
    $task = Get-ScheduledTask -TaskName $taskName
    if ($task.State -eq 'Running' -or $task.State -eq 'Queued') {
        Stop-ScheduledTask -InputObject $task
        $deadline = [DateTime]::UtcNow.AddSeconds(15)
        do {
            Start-Sleep -Milliseconds 150
            $task = Get-ScheduledTask -TaskName $taskName
        } while (($task.State -eq 'Running' -or $task.State -eq 'Queued') -and [DateTime]::UtcNow -lt $deadline)
        if ($task.State -eq 'Running' -or $task.State -eq 'Queued') {
            throw "Task $taskName did not stop."
        }
    }
}

switch ($Action) {
    'Install' {
        $binary = (Get-Item -LiteralPath $BinaryPath).FullName
        $logFile = Join-Path $state 'daemon.log'
        $arguments = 'supervise --log="{0}"' -f $logFile
        if ($Instance) {
            $pipe = "\\.\pipe\antitimely-$sid-$Instance"
            $arguments += ' --socket="{0}" --db="{1}" --pid="{2}" --config="{3}"' -f $pipe, (Join-Path $state 'db.sqlite'), (Join-Path $state 'antitimely.pid'), (Join-Path $state 'config.yaml')
        }
        $taskAction = New-ScheduledTaskAction -Execute $binary -Argument $arguments -WorkingDirectory (Split-Path -LiteralPath $binary)
        $trigger = New-ScheduledTaskTrigger -AtLogOn -User $identity.Name
        $principal = New-ScheduledTaskPrincipal -UserId $sid -LogonType Interactive -RunLevel Limited
        $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
        Register-ScheduledTask -TaskName $taskName -Action $taskAction -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
        Start-ScheduledTask -TaskName $taskName
        Write-Output "Antitimely startup installed. Daemon log is $logFile"
    }
    'Start' { Start-ScheduledTask -TaskName $taskName }
    'Stop' { Stop-TrackerTask }
    'Restart' {
        Stop-TrackerTask
        Start-ScheduledTask -TaskName $taskName
    }
    'Status' { Get-ScheduledTask -TaskName $taskName }
    'Uninstall' {
        if (-not (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue)) { return }
        Stop-TrackerTask
        Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
        Write-Output 'Antitimely startup removed.'
    }
}
