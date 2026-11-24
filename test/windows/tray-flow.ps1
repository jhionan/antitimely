# Exercise the Windows tray event handlers in its WPF dispatcher with an isolated fake CLI.
$ErrorActionPreference = 'Stop'
$tray = (Resolve-Path (Join-Path $PSScriptRoot '..\..\scripts\windows-tray.ps1')).Path
$work = Join-Path ([IO.Path]::GetTempPath()) ('Antitimely-Tray-' + [Guid]::NewGuid().ToString('N'))
New-Item -Type Directory -Path $work | Out-Null
try {
    $fixture = Join-Path $work 'fixture.exe'
    Add-Type -OutputAssembly $fixture -OutputType ConsoleApplication -TypeDefinition @'
using System;
using System.IO;
public class Fixture {
    public static int Main(string[] args) {
        string action = args.Length > 1 ? args[1] : "";
        string dir = AppDomain.CurrentDomain.BaseDirectory;
        string active = Path.Combine(dir, "active.txt");
        if (args.Length > 0) File.AppendAllText(Path.Combine(dir, "calls.txt"), args[0] + " " + action + Environment.NewLine);
        const string task = "\"ConnectionID\":\"fixture\",\"WorkspaceID\":\"1\",\"TaskID\":\"a\",\"Name\":\"Review import validation\"";
        const string localName = "Plan next week's work";
        string localActive = Path.Combine(dir, "local-active.txt");
        string localSaved = Path.Combine(dir, "local-saved.txt");
        if (args.Length > 0 && args[0] == "timer") {
            switch (action) {
            case "start":
                if (!Console.In.ReadToEnd().Replace("\\u0027", "'").Contains(localName)) return 2;
                File.WriteAllText(localActive, DateTimeOffset.UtcNow.ToUnixTimeSeconds().ToString());
                Console.WriteLine("{\"Local\":true,\"Name\":\"" + localName + "\",\"StartedUnix\":" + File.ReadAllText(localActive) + "}");
                return 0;
            case "stop":
                File.Move(localActive, localSaved);
                Console.WriteLine("{\"StartedUnix\":0}");
                return 0;
            case "status":
                string running = File.Exists(localActive) ? "\"Local\":true,\"Name\":\"" + localName + "\",\"StartedUnix\":" + File.ReadAllText(localActive) :
                    task + ",\"StartedUnix\":" + (File.Exists(active) ? File.ReadAllText(active) : "0");
                string history = "[]";
                if (File.Exists(localSaved)) {
                    long start = long.Parse(File.ReadAllText(localSaved));
                    history = "[{\"Name\":\"" + localName + "\",\"Start\":" + start + ",\"End\":" + (start + 40) + ",\"State\":\"local\"}]";
                }
                Console.WriteLine("{\"Timer\":{" + running + "},\"Entries\":" + history + "}");
                return 0;
            default: return 1;
            }
        }
        switch (action) {
        case "catalog":
        case "refresh":
            if (!File.Exists(Path.Combine(dir, "connected.txt"))) {
                Console.WriteLine("{\"Connected\":false,\"Stale\":true,\"Error\":\"ClickUp requires a configured sync device\"}");
                break;
            }
            Console.WriteLine("{\"Connected\":true,\"Username\":\"Tester\",\"Tasks\":[{" + task + ",\"Workspace\":\"Platform\"}]}");
            break;
        case "details":
            Console.In.ReadToEnd();
            Console.WriteLine("{\"Name\":\"Review import validation\",\"Description\":\"Verify missing columns\",\"Attachments\":[{\"Name\":\"Import report\",\"URL\":\"https://attachments.clickup.com/report.pdf\"}],\"Comments\":[{\"Author\":\"Ada\",\"Text\":\"Please check order_id.\"}]}");
            break;
        case "start":
            Console.In.ReadToEnd();
            File.WriteAllText(active, DateTimeOffset.UtcNow.ToUnixTimeSeconds().ToString());
            Console.WriteLine("{" + task + ",\"StartedUnix\":" + File.ReadAllText(active) + "}");
            break;
        case "status":
            string timer = File.Exists(active) ? File.ReadAllText(active) : "0";
            Console.WriteLine("{\"Timer\":{" + task + ",\"StartedUnix\":" + timer + "},\"Entries\":[]}");
            break;
        default: return 1;
        }
        return 0;
    }
}
'@
    $source = Get-Content -LiteralPath $tray -Raw
    $source = $source.Replace('Local\AntitimelyTray', ('Local\AntitimelyTrayTest-' + [Guid]::NewGuid().ToString('N')))
    $anchor = '    [void]$app.Run()'
    if (-not $source.Contains($anchor)) { throw 'Tray dispatcher entry point changed.' }
    $driver = @'
function Wait-TrayState([scriptblock]$expected) {
    $deadline = [DateTime]::UtcNow.AddSeconds(8)
    do {
        if (& $expected) { return }
        $frame = New-Object Windows.Threading.DispatcherFrame
        [void][Windows.Threading.Dispatcher]::CurrentDispatcher.BeginInvoke(
            [Windows.Threading.DispatcherPriority]::Background,
            [Action]{ $frame.Continue = $false })
        [Windows.Threading.Dispatcher]::PushFrame($frame)
        Start-Sleep -Milliseconds 40
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "Tray did not reach expected state. View: $script:view. Notice: $($ui.Notice.Text)"
}

function Click-Control($button) {
    $event = New-Object Windows.RoutedEventArgs([Windows.Controls.Button]::ClickEvent, $button)
    $button.RaiseEvent($event)
}

$driverTimer = New-Object Windows.Threading.DispatcherTimer
$driverTimer.Interval = [TimeSpan]::FromMilliseconds(100)
$driverTimer.Add_Tick({
    $driverTimer.Stop()
    try {
        if (-not $form.Icon -or $icon.Icon -eq [Drawing.SystemIcons]::Application) {
            throw 'The window and notification area should use the Antitimely icon.'
        }
        Wait-TrayState { $script:catalog -and -not $script:pending }
        if ($script:view -ne 'Local' -or -not $ui.StartLocal.IsVisible -or $ui.Notice.IsVisible) {
            throw 'A standalone timer should be available immediately when signed out.'
        }
        $ui.SessionName.Text = "Plan next week's work"
        Click-Control $ui.StartLocal
        Wait-TrayState { $script:view -eq 'Timer' -and $ui.ActiveTitle.Text -eq "Plan next week's work" }
        if ($ui.ActiveDetails.IsVisible -or $ui.SwitchButton.IsVisible) { throw 'Signed-out timer showed ClickUp actions.' }
        Click-Control $ui.StopButton
        Wait-TrayState { $script:view -eq 'Local' }
        Click-Control $ui.ShowHistory
        Wait-TrayState {
            $script:view -eq 'History' -and ($ui.HistoryRows.Children | Where-Object { $_.Text -eq "Plan next week's work" })
        }
        if (-not ($ui.HistoryRows.Children | Where-Object { $_.Text -like '*Saved on this device*' })) {
            throw 'History should identify the saved local session.'
        }
        Click-Control $ui.BackHistory
        [IO.File]::WriteAllText((Join-Path $PSScriptRoot 'connected.txt'), 'connected')
        Refresh-Tasks
        Wait-TrayState { $script:view -eq 'Tasks' -and $ui.TaskRows.Children.Count -eq 1 }
        Click-Control $ui.TaskRows.Children[0].Children[0]
        Wait-TrayState { $script:view -eq 'Detail' -and $ui.AssignButton.Visibility -eq 'Visible' }
        $body = $ui.DetailBody.Children | ForEach-Object { $_.Text }
        $links = $ui.DetailBody.Children | Where-Object { $_ -is [Windows.Controls.Button] }
        if ($body -notcontains 'Verify missing columns' -or $body -notcontains 'Please check order_id.' -or
            $links.Content -ne 'Import report' -or $links.Tag.AbsoluteUri -ne 'https://attachments.clickup.com/report.pdf') {
            throw 'Task details did not show the description, attachment, and comments.'
        }
        $calls = Join-Path $PSScriptRoot 'calls.txt'
        $activity = Get-Content -LiteralPath $calls
        if ($activity -notcontains 'clickup details' -or $activity -contains 'clickup start') { throw 'Opening details started a timer.' }
        Click-Control $ui.BackDetails
        Click-Control $ui.TaskRows.Children[0].Children[1]
        Wait-TrayState { $script:view -eq 'Timer' }
        $activity = Get-Content -LiteralPath $calls
        if (@($activity | Where-Object { $_ -eq 'clickup start' }).Count -ne 1 -or
            @($activity | Where-Object { $_ -eq 'clickup details' }).Count -ne 1) {
            throw 'Play should start exactly one timer without querying details.'
        }
        $script:flowTestPassed = $true
    } catch {
        $script:flowTestError = $_.Exception.Message
    } finally {
        $script:quitting = $true
        $app.Shutdown()
    }
})
$driverTimer.Start()
    [void]$app.Run()
    if (-not $script:flowTestPassed) { throw $script:flowTestError }
'@
    $instrumented = Join-Path $work 'tray-test.ps1'
    Copy-Item -LiteralPath (Join-Path (Split-Path -Parent $tray) 'antitimely.ico') -Destination $work
    $source.Replace($anchor, $driver) | Set-Content -LiteralPath $instrumented -Encoding UTF8
    & "$PSHOME\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -STA -File $instrumented -ShowOnLaunch -BinaryPath $fixture
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Write-Output 'Standalone time was saved while signed out. ClickUp details and direct Play also worked.'
} finally {
    Remove-Item -LiteralPath $work -Recurse -Force
}
