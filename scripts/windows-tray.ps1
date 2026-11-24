[CmdletBinding()]
param([string]$BinaryPath, [switch]$ShowOnLaunch)

$ErrorActionPreference = 'Stop'
if (-not $BinaryPath) { $BinaryPath = Join-Path $PSScriptRoot '..\antitimely.exe' }
Add-Type -AssemblyName PresentationFramework, PresentationCore, WindowsBase, System.Windows.Forms, System.Drawing
[Windows.Forms.Application]::EnableVisualStyles()
$binary = (Get-Item -LiteralPath $BinaryPath).FullName

$firstInstance = $false
$instance = New-Object Threading.Mutex($true, 'Local\AntitimelyTray', [ref]$firstInstance)
if (-not $firstInstance) { $instance.Dispose(); return }

[xml]$xaml = @'
<Window xmlns="http://schemas.microsoft.com/winfx/2006/xaml/presentation"
 xmlns:x="http://schemas.microsoft.com/winfx/2006/xaml"
 Title="Antitimely" Width="436" MinWidth="360" SizeToContent="Height" ResizeMode="CanMinimize"
 WindowStartupLocation="CenterScreen" Background="#1F1729" Foreground="#F6EEFC" FontFamily="Segoe UI" FontSize="14">
 <Window.Resources>
  <SolidColorBrush x:Key="Accent" Color="#CFB0FF"/>
  <SolidColorBrush x:Key="Muted" Color="#C1AFD0"/>
  <SolidColorBrush x:Key="Outline" Color="#463654"/>
  <SolidColorBrush x:Key="Surface" Color="#2B1E38"/>
  <Style TargetType="TextBlock">
   <Setter Property="Foreground" Value="#F6EEFC"/>
   <Setter Property="TextWrapping" Value="Wrap"/>
  </Style>
  <Style x:Key="TitleText" TargetType="TextBlock">
   <Setter Property="FontSize" Value="25"/>
   <Setter Property="FontWeight" Value="SemiBold"/>
   <Setter Property="TextWrapping" Value="Wrap"/>
  </Style>
  <Style TargetType="Button">
   <Setter Property="Background" Value="Transparent"/>
   <Setter Property="Foreground" Value="#F6EEFC"/>
   <Setter Property="BorderBrush" Value="{StaticResource Outline}"/>
   <Setter Property="BorderThickness" Value="1"/>
   <Setter Property="Padding" Value="12,8"/>
   <Setter Property="MinHeight" Value="40"/>
   <Setter Property="FontWeight" Value="SemiBold"/>
   <Setter Property="Template">
    <Setter.Value>
     <ControlTemplate TargetType="Button">
      <Border Name="Chrome" CornerRadius="8" Background="{TemplateBinding Background}"
       BorderBrush="{TemplateBinding BorderBrush}" BorderThickness="{TemplateBinding BorderThickness}"
       Padding="{TemplateBinding Padding}">
       <ContentPresenter HorizontalAlignment="{TemplateBinding HorizontalContentAlignment}" VerticalAlignment="Center"/>
      </Border>
      <ControlTemplate.Triggers>
       <Trigger Property="IsMouseOver" Value="True"><Setter TargetName="Chrome" Property="BorderBrush" Value="{StaticResource Accent}"/></Trigger>
       <Trigger Property="IsKeyboardFocused" Value="True"><Setter TargetName="Chrome" Property="BorderBrush" Value="{StaticResource Accent}"/></Trigger>
       <Trigger Property="IsPressed" Value="True"><Setter TargetName="Chrome" Property="Opacity" Value="0.83"/></Trigger>
       <Trigger Property="IsEnabled" Value="False"><Setter TargetName="Chrome" Property="Opacity" Value="0.45"/></Trigger>
      </ControlTemplate.Triggers>
     </ControlTemplate>
    </Setter.Value>
   </Setter>
  </Style>
  <Style x:Key="Primary" TargetType="Button" BasedOn="{StaticResource {x:Type Button}}">
   <Setter Property="Background" Value="{StaticResource Accent}"/>
   <Setter Property="Foreground" Value="#321653"/>
   <Setter Property="BorderBrush" Value="{StaticResource Accent}"/>
  </Style>
  <Style x:Key="Quiet" TargetType="Button" BasedOn="{StaticResource {x:Type Button}}">
   <Setter Property="Foreground" Value="{StaticResource Accent}"/>
   <Setter Property="BorderBrush" Value="Transparent"/>
   <Setter Property="Padding" Value="7,4"/>
   <Setter Property="MinHeight" Value="32"/>
  </Style>
 </Window.Resources>
 <StackPanel>
  <Border BorderBrush="{StaticResource Outline}" BorderThickness="0,0,0,1" Padding="20,13">
   <DockPanel LastChildFill="True">
    <TextBlock Name="Workspace" DockPanel.Dock="Right" Foreground="{StaticResource Muted}" FontSize="12" VerticalAlignment="Center" Margin="8,0,0,0"/>
    <Border DockPanel.Dock="Left" Background="{StaticResource Accent}" CornerRadius="8" Width="27" Height="27" Margin="0,0,9,0">
     <TextBlock Text="&#x25F7;" Foreground="#321653" FontSize="18" HorizontalAlignment="Center" VerticalAlignment="Center"/>
    </Border>
    <TextBlock Text="Antitimely" FontWeight="SemiBold" VerticalAlignment="Center"/>
   </DockPanel>
  </Border>
  <StackPanel Margin="20,22,20,19">
   <StackPanel Name="LocalView">
    <TextBlock Text="Track time" Style="{StaticResource TitleText}"/>
    <TextBlock Text="Session name (optional)" Foreground="{StaticResource Muted}" Margin="0,15,0,6"/>
    <Border BorderBrush="{StaticResource Outline}" BorderThickness="1" CornerRadius="8" Background="{StaticResource Surface}">
     <TextBox Name="SessionName" AutomationProperties.Name="Session name (optional)" MaxLength="120" Style="{x:Null}"
      Background="Transparent" Foreground="#F6EEFC" BorderThickness="0" Padding="12,8" Height="42"
      VerticalContentAlignment="Center" CaretBrush="#F6EEFC"/>
    </Border>
    <Button Name="StartLocal" Content="Start timer" Style="{StaticResource Primary}" IsDefault="True" Margin="0,12,0,0"/>
    <WrapPanel Margin="0,8,0,0">
     <Button Name="SignInButton" Style="{StaticResource Quiet}" Content="Sign in with ClickUp" />
     <Button Name="CheckConnection" Style="{StaticResource Quiet}" Content="Check connection" Visibility="Collapsed"/>
     <Button Name="TaskListButton" Style="{StaticResource Quiet}" Content="ClickUp tasks" Visibility="Collapsed"/>
     <Button Name="LocalReturnTimer" Style="{StaticResource Quiet}" Content="Back to timer" Visibility="Collapsed"/>
    </WrapPanel>
   </StackPanel>

   <StackPanel Name="TasksView" Visibility="Collapsed">
    <DockPanel>
     <Button Name="RefreshButton" DockPanel.Dock="Right" Content="Refresh" Style="{StaticResource Quiet}"/>
     <TextBlock Name="TaskHeading" Text="Choose a task" Style="{StaticResource TitleText}" VerticalAlignment="Center"/>
    </DockPanel>
    <Border BorderBrush="{StaticResource Outline}" BorderThickness="1" CornerRadius="8" Background="{StaticResource Surface}" Margin="0,13,0,12">
     <Grid>
      <TextBlock Name="SearchPlaceholder" Text="Search assigned tasks" IsHitTestVisible="False" Foreground="{StaticResource Muted}" Margin="12,0" VerticalAlignment="Center"/>
      <TextBox Name="Search" AutomationProperties.Name="Search assigned tasks" Style="{x:Null}"
       Background="Transparent" Foreground="#F6EEFC" BorderThickness="0" Padding="12,8" Height="42"
       VerticalContentAlignment="Center" CaretBrush="#F6EEFC"/>
     </Grid>
    </Border>
    <ScrollViewer MaxHeight="330" VerticalScrollBarVisibility="Auto">
     <StackPanel Name="TaskRows"/>
    </ScrollViewer>
    <WrapPanel>
     <Button Name="LocalFromTasks" Content="Start without a task" Style="{StaticResource Quiet}"/>
     <Button Name="ReturnTimer" Content="Back to timer" Style="{StaticResource Quiet}" Visibility="Collapsed"/>
    </WrapPanel>
   </StackPanel>

   <StackPanel Name="TimerView" Visibility="Collapsed">
    <TextBlock Text="&#x2022; Tracking" Foreground="{StaticResource Accent}" FontSize="12" FontWeight="SemiBold" Margin="0,0,0,12"/>
    <TextBlock Name="ActiveWorkspace" Foreground="{StaticResource Muted}" FontSize="12"/>
    <TextBlock Name="ActiveTitle" Style="{StaticResource TitleText}" Margin="0,5,0,0"/>
    <TextBlock Name="Elapsed" FontSize="43" FontWeight="SemiBold" Margin="0,10,0,15" FontFamily="Segoe UI"/>
    <DockPanel>
     <Button Name="SwitchButton" DockPanel.Dock="Right" Content="Switch task" Margin="8,0,0,0"/>
     <Button Name="StopButton" Content="Stop timer" Style="{StaticResource Primary}"/>
    </DockPanel>
    <Button Name="ActiveDetails" Content="Task details" Style="{StaticResource Quiet}" HorizontalAlignment="Left" Margin="0,9,0,0"/>
   </StackPanel>

   <StackPanel Name="DetailView" Visibility="Collapsed">
    <Button Name="BackDetails" Content="Back to tasks" Style="{StaticResource Quiet}" HorizontalAlignment="Left" Margin="0,0,0,7"/>
    <TextBlock Name="DetailWorkspace" Foreground="{StaticResource Muted}" FontSize="12"/>
    <TextBlock Name="DetailTitle" Style="{StaticResource TitleText}" Margin="0,5,0,10"/>
    <ScrollViewer MaxHeight="410" VerticalScrollBarVisibility="Auto">
     <StackPanel Name="DetailBody"/>
    </ScrollViewer>
    <Button Name="AssignButton" Content="Assign tracked time" Style="{StaticResource Quiet}" Visibility="Collapsed" HorizontalAlignment="Left" Margin="0,7,0,0"/>
   </StackPanel>

   <StackPanel Name="HistoryView" Visibility="Collapsed">
    <Button Name="BackHistory" Content="Back" Style="{StaticResource Quiet}" HorizontalAlignment="Left"/>
    <TextBlock Text="History" Style="{StaticResource TitleText}" Margin="0,3,0,10"/>
    <ScrollViewer MaxHeight="350" VerticalScrollBarVisibility="Auto"><StackPanel Name="HistoryRows"/></ScrollViewer>
   </StackPanel>
   <TextBlock Name="Notice" Foreground="{StaticResource Muted}" FontSize="12" Margin="0,10,0,0" Visibility="Collapsed" AutomationProperties.LiveSetting="Polite"/>
  </StackPanel>
  <Border Name="Footer" BorderBrush="{StaticResource Outline}" BorderThickness="0,1,0,0" Padding="20,5">
   <DockPanel>
    <Button Name="ShowHistory" Content="History" DockPanel.Dock="Right" Style="{StaticResource Quiet}"/>
    <TextBlock Name="FooterStatus" Foreground="{StaticResource Muted}" FontSize="12" VerticalAlignment="Center"/>
   </DockPanel>
  </Border>
 </StackPanel>
</Window>
'@
$reader = New-Object Xml.XmlNodeReader($xaml)
try { $form = [Windows.Markup.XamlReader]::Load($reader) } finally { $reader.Dispose() }
$ui = @{}
foreach ($name in @('Workspace','LocalView','SessionName','StartLocal','SignInButton','CheckConnection','TaskListButton',
        'LocalReturnTimer','TasksView','LocalFromTasks','RefreshButton','TaskHeading',
        'Search','SearchPlaceholder','TaskRows','ReturnTimer','TimerView','ActiveWorkspace','ActiveTitle','Elapsed',
        'StopButton','SwitchButton','ActiveDetails','DetailView','BackDetails','DetailWorkspace','DetailTitle',
        'DetailBody','AssignButton','HistoryView','BackHistory','HistoryRows','Notice','Footer','ShowHistory','FooterStatus')) {
    $ui[$name] = $form.FindName($name)
}
$script:view = 'Local'
$script:pending = $null
$script:catalog = $null
$script:status = [pscustomobject]@{Timer=$null;Entries=@()}
$script:nextStatus = [DateTime]::UtcNow
$script:quitting = $false
$script:detailTask = $null
$script:detailFrom = 'Tasks'
$script:previewSelection = $null

function Set-Notice([string]$message) {
    $ui.Notice.Text = $message
    $ui.Notice.Visibility = if ($message) { 'Visible' } else { 'Collapsed' }
}

function Show-View([string]$view) {
    Set-Notice ''
    $script:view = $view
    foreach ($name in @('Local','Tasks','Timer','Detail','History')) {
        $ui[$name + 'View'].Visibility = if ($name -eq $view) { 'Visible' } else { 'Collapsed' }
    }
    $ui.Footer.Visibility = if ($view -in @('Local','Tasks','Timer')) { 'Visible' } else { 'Collapsed' }
    $timer = $script:status.Timer
    $ui.LocalReturnTimer.Visibility = if ($timer.StartedUnix -gt 0) { 'Visible' } else { 'Collapsed' }
    if ($view -eq 'Tasks') {
        $ui.ReturnTimer.Visibility = if ($timer.StartedUnix -gt 0) { 'Visible' } else { 'Collapsed' }
        $ui.TaskHeading.Text = if ($timer.StartedUnix -gt 0 -and -not $timer.Local) { 'Switch task' } else { 'Choose a task' }
        [void]$ui.Search.Focus()
    }
}

function Add-Text($parent, [string]$text, [switch]$Muted, [switch]$Heading) {
    $line = New-Object Windows.Controls.TextBlock
    $line.Text = $text
    $line.TextWrapping = 'Wrap'
    $line.Margin = New-Object Windows.Thickness(0,0,0,7)
    if ($Muted) { $line.Foreground = $form.Resources['Muted']; $line.FontSize = 12 }
    if ($Heading) { $line.FontWeight = 'SemiBold'; $line.Margin = New-Object Windows.Thickness(0,8,0,5) }
    [void]$parent.Children.Add($line)
}

function Update-Rows {
    $ui.TaskRows.Children.Clear()
    $term = $ui.Search.Text.Trim()
    $ui.SearchPlaceholder.Visibility = if ($term) { 'Collapsed' } else { 'Visible' }
    foreach ($task in $script:catalog.Tasks) {
        if ($term -and ("$($task.Name) $($task.Workspace)".IndexOf($term,[StringComparison]::OrdinalIgnoreCase) -lt 0)) { continue }
        $row = New-Object Windows.Controls.Grid
        $row.Margin = New-Object Windows.Thickness(0,0,0,5)
        $col = New-Object Windows.Controls.ColumnDefinition
        $playCol = New-Object Windows.Controls.ColumnDefinition
        $playCol.Width = New-Object Windows.GridLength(44)
        $row.ColumnDefinitions.Add($col)
        $row.ColumnDefinitions.Add($playCol)
        $title = New-Object Windows.Controls.Button
        $title.Style = $form.Resources['Quiet']
        $title.HorizontalContentAlignment = 'Left'
        $title.Tag = $task
        [Windows.Automation.AutomationProperties]::SetName($title, $task.Name)
        $labels = New-Object Windows.Controls.StackPanel
        $name = New-Object Windows.Controls.TextBlock
        $name.Text = $task.Name
        $name.TextWrapping = 'Wrap'
        [void]$labels.Children.Add($name)
        $workspace = New-Object Windows.Controls.TextBlock
        $workspace.Text = $task.Workspace
        $workspace.Foreground = $form.Resources['Muted']
        $workspace.FontSize = 12
        [void]$labels.Children.Add($workspace)
        $title.Content = $labels
        $title.Add_Click({ param($sender,$eventArgs) Show-TaskDetails $sender.Tag })
        $play = New-Object Windows.Controls.Button
        $play.Style = $form.Resources['Quiet']
        $play.Content = [string][char]0x25B6
        $play.MinHeight = 43
        $play.Tag = $task
        [Windows.Automation.AutomationProperties]::SetName($play, "Start timer for $($task.Name)")
        $play.Add_Click({ param($sender,$eventArgs) Start-Task $sender.Tag })
        [Windows.Controls.Grid]::SetColumn($play,1)
        [void]$row.Children.Add($title)
        [void]$row.Children.Add($play)
        [void]$ui.TaskRows.Children.Add($row)
    }
    if ($ui.TaskRows.Children.Count -eq 0) {
        $empty = if ($term) { 'No matching tasks.' } else { 'No assigned tasks.' }
        Add-Text $ui.TaskRows $empty -Muted
    }
}

function Task-Identity($task) {
    @{ConnectionID=$task.ConnectionID;WorkspaceID=$task.WorkspaceID;TaskID=$task.TaskID;Name=$task.Name}
}

function Start-Task($task) {
    Start-RPC 'clickup start' @{Task=(Task-Identity $task)} { param($timer) Show-StartedTimer $timer }
}

function Show-StartedTimer($timer) {
    $script:status.Timer = $timer
    Show-View 'Timer'
    Update-Header
    $script:nextStatus = [DateTime]::UtcNow
}

function ClickUp-Link([string]$value) {
    $url = $null
    if (-not [uri]::TryCreate($value,[UriKind]::Absolute,[ref]$url)) { return }
    if ($url.Scheme -ne 'https' -or $url.UserInfo -or -not $url.IsDefaultPort) { return }
    if ($url.Host -eq 'clickup.com' -or $url.Host.EndsWith('.clickup.com') -or $url.Host.EndsWith('.clickup-attachments.com')) { return $url }
}

function Add-Attachment($attachment) {
    $url = ClickUp-Link $attachment.URL
    if (-not $url) { return }
    $link = New-Object Windows.Controls.Button
    $link.Style = $form.Resources['Quiet']
    $link.Content = if ($attachment.Name) { $attachment.Name } else { 'Open attachment' }
    $link.HorizontalAlignment = 'Left'
    $link.Tag = $url
    $link.Add_Click({ param($sender,$eventArgs) Start-Process -FilePath $sender.Tag.AbsoluteUri })
    if ($attachment.Image) {
        try {
            $bitmap = New-Object Windows.Media.Imaging.BitmapImage
            $bitmap.BeginInit()
            $bitmap.UriSource = $url
            $bitmap.DecodePixelWidth = 320
            $bitmap.EndInit()
            $thumbnail = New-Object Windows.Controls.Image
            $thumbnail.Source = $bitmap
            $thumbnail.MaxWidth = 215
            $thumbnail.MaxHeight = 112
            $thumbnail.HorizontalAlignment = 'Left'
            $thumbnail.Stretch = 'Uniform'
            [Windows.Automation.AutomationProperties]::SetName($thumbnail, $link.Content)
            [void]$ui.DetailBody.Children.Add($thumbnail)
        } catch {
            # The attachment link remains available when the image needs browser authentication.
        }
    }
    [void]$ui.DetailBody.Children.Add($link)
}

function Update-Details($result) {
    if ($script:view -ne 'Detail') { return }
    $ui.DetailBody.Children.Clear()
    if ($result.Name) { $ui.DetailTitle.Text = $result.Name }
    $meta = @()
    if ($result.Status) { $meta += $result.Status }
    if ($result.Priority) { $meta += "$($result.Priority) priority" }
    if ($result.DueUnixMillis -gt 0) {
        $due = [DateTimeOffset]::FromUnixTimeMilliseconds($result.DueUnixMillis).LocalDateTime.ToString('MMM d')
        $meta += "Due $due"
    }
    if ($meta.Count) { Add-Text $ui.DetailBody ($meta -join '  |  ') -Muted }
    Add-Text $ui.DetailBody 'Description' -Heading
    $description = if ($result.Description) { $result.Description } else { 'No description.' }
    Add-Text $ui.DetailBody $description
    if ($result.Attachments) {
        Add-Text $ui.DetailBody 'Attachments' -Heading
        foreach ($attachment in $result.Attachments) { Add-Attachment $attachment }
    }
    Add-Text $ui.DetailBody 'Comments' -Heading
    if (-not $result.Comments) { Add-Text $ui.DetailBody 'No comments.' -Muted }
    foreach ($comment in $result.Comments) {
        if ($comment.Author) { Add-Text $ui.DetailBody $comment.Author -Muted }
        Add-Text $ui.DetailBody $comment.Text
    }
    $ui.AssignButton.Visibility = 'Visible'
}

function Show-TaskDetails($task) {
    if ($script:pending -and $script:pending.Action -notin @('timer status','clickup catalog')) { return }
    $script:detailFrom = $script:view
    $script:detailTask = $task
    Show-View 'Detail'
    $ui.BackDetails.Content = if ($script:detailFrom -eq 'Timer') { 'Back to timer' } else { 'Back to tasks' }
    $ui.DetailTitle.Text = $task.Name
    $ui.DetailWorkspace.Text = $task.Workspace
    $ui.AssignButton.Visibility = 'Collapsed'
    $ui.DetailBody.Children.Clear()
    Add-Text $ui.DetailBody 'Loading from ClickUp...' -Muted
    Start-RPC 'clickup details' @{TaskID=$task.TaskID} { param($r) Update-Details $r }
}

function Update-Catalog($result) {
    $script:catalog = $result
    $ui.Workspace.Text = if ($result.Connected) { $result.Username } else { '' }
    $ui.TaskListButton.Visibility = if ($result.Connected) { 'Visible' } else { 'Collapsed' }
    $ui.SignInButton.Visibility = if ($result.Connected) { 'Collapsed' } else { 'Visible' }
    if ($result.Connected) { $ui.CheckConnection.Visibility = 'Collapsed' }
    Update-Rows
    if ($script:view -eq 'Tasks' -and -not $result.Connected) { Show-View 'Local' }
    Update-Header
}

function Update-Header {
    $timer = $script:status.Timer
    if ($timer.StartedUnix -gt 0) {
        $elapsed = [Math]::Max(0,[DateTimeOffset]::UtcNow.ToUnixTimeSeconds()-$timer.StartedUnix)
        $duration = [TimeSpan]::FromSeconds($elapsed)
        $ui.Elapsed.Text = '{0:00}:{1:00}:{2:00}' -f [int][math]::Floor($duration.TotalHours),$duration.Minutes,$duration.Seconds
        $ui.ActiveTitle.Text = $timer.Name
        if ($timer.Local) {
            $ui.ActiveWorkspace.Text = 'Local session'
        } else {
            $choice = $script:catalog.Tasks | Where-Object {
                $_.TaskID -eq $timer.TaskID -and $_.WorkspaceID -eq $timer.WorkspaceID -and $_.ConnectionID -eq $timer.ConnectionID
            } | Select-Object -First 1
            $ui.ActiveWorkspace.Text = if ($choice) { $choice.Workspace } else { '' }
        }
        $ui.ActiveDetails.Visibility = if ($timer.Local) { 'Collapsed' } else { 'Visible' }
        $ui.SwitchButton.Visibility = if ($script:catalog.Connected) { 'Visible' } else { 'Collapsed' }
        $ui.SwitchButton.Content = if ($timer.Local) { 'Choose a task' } else { 'Switch task' }
    }
    $queued = @($script:status.Entries | Where-Object { $_.State -in @('queued','needs_review') }).Count
    $ui.FooterStatus.Text = if ($queued -gt 0) { "$queued saved on this device" }
        elseif ($timer.StartedUnix -gt 0) { 'Timer running' }
        elseif ($script:status.Entries) { 'Time entries available' } else { 'No time logged yet' }
}

function Update-Status($result) {
    $previous = $script:status.Timer
    $script:status = $result
    if ($result.Timer.StartedUnix -gt 0 -and -not ($previous.StartedUnix -gt 0) -and $script:view -in @('Local','Tasks')) {
        Show-View 'Timer'
    } elseif ($result.Timer.StartedUnix -eq 0 -and $script:view -eq 'Timer') {
        if ($script:catalog.Connected -and -not $previous.Local) { Show-View 'Tasks' } else { Show-View 'Local' }
    }
    Update-Header
    if ($script:view -eq 'History') { Update-History }
}

function Show-History {
    $script:historyFrom = $script:view
    Show-View 'History'
    Update-History
    $script:nextStatus = [DateTime]::UtcNow
}

function Update-History {
    $ui.HistoryRows.Children.Clear()
    if (-not $script:status.Entries) { Add-Text $ui.HistoryRows 'No time entries yet.' -Muted }
    foreach ($entry in $script:status.Entries) {
        $duration = [TimeSpan]::FromSeconds($entry.End - $entry.Start)
        Add-Text $ui.HistoryRows $entry.Name -Heading
        $label = switch ($entry.State) {
            'local' { 'Saved on this device' }
            'queued' { 'Waiting to sync' }
            'published' { 'Sent to ClickUp' }
            'needs_review' { 'Needs review' }
            'failed' { 'Failed to sync' }
            default { $entry.State }
        }
        $started = [DateTimeOffset]::FromUnixTimeSeconds($entry.Start).LocalDateTime.ToString('MMM d, HH:mm')
        Add-Text $ui.HistoryRows "$started  |  $duration  |  $label" -Muted
        if ($entry.Message) { Add-Text $ui.HistoryRows $entry.Message -Muted }
    }
}

function Start-RPC([string]$action, $data, [scriptblock]$done) {
    if ($script:pending) {
        if ($action -eq 'timer status' -or $script:pending.Action -notin @('timer status','clickup catalog')) {
            Set-Notice 'Wait for the current request to finish.'
            return
        }
        if (-not $script:pending.Process.HasExited) { $script:pending.Process.Kill() }
        $script:pending.Process.Dispose()
        $script:pending = $null
    }
    $info = New-Object Diagnostics.ProcessStartInfo
    $info.FileName = $binary
    $info.Arguments = $action
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardInput = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $info.StandardOutputEncoding = [Text.Encoding]::UTF8
    $info.StandardErrorEncoding = [Text.Encoding]::UTF8
    $process = [Diagnostics.Process]::Start($info)
    try {
        if ($null -ne $data) {
            $json = ConvertTo-Json -InputObject $data -Compress -Depth 8
            $bytes = [Text.Encoding]::UTF8.GetBytes($json)
            $process.StandardInput.BaseStream.Write($bytes, 0, $bytes.Length)
        }
        $process.StandardInput.Close()
        $script:pending = @{
            Process = $process
            Action = $action
            Output = $process.StandardOutput.ReadToEndAsync()
            Error = $process.StandardError.ReadToEndAsync()
            Done = $done
            Deadline = [DateTime]::UtcNow.AddSeconds(18)
        }
    } catch {
        if (-not $process.HasExited) { $process.Kill() }
        $process.Dispose()
        throw
    }
}

function Choose-Range($task) {
    $dialog = New-Object Windows.Forms.Form
    $dialog.Text = 'Assign tracked time'
    $dialog.Size = New-Object Drawing.Size(410, 245)
    $dialog.StartPosition = 'CenterScreen'
    $grid = New-Object Windows.Forms.TableLayoutPanel
    $grid.Dock = 'Fill'
    $grid.Padding = New-Object Windows.Forms.Padding(12)
    $grid.ColumnCount = 2
    $dialog.Controls.Add($grid)
    $from = New-Object Windows.Forms.DateTimePicker
    $to = New-Object Windows.Forms.DateTimePicker
    foreach ($picker in @($from, $to)) {
        $picker.Format = 'Custom'
        $picker.CustomFormat = 'yyyy-MM-dd HH:mm'
        $picker.Width = 170
    }
    $from.Value = [DateTime]::Today
    $to.Value = [DateTime]::Now
    $amount = New-Object Windows.Forms.NumericUpDown
    $amount.Maximum = 2678400
    $labels = @('From (local time)', 'To (local time)', 'Seconds (0 for all)')
    $inputs = @($from, $to, $amount)
    for ($i = 0; $i -lt $labels.Length; $i++) {
        $caption = New-Object Windows.Forms.Label
        $caption.AutoSize = $true
        $caption.Text = $labels[$i]
        $grid.Controls.Add($caption, 0, $i)
        $grid.Controls.Add($inputs[$i], 1, $i)
    }
    $ok = New-Object Windows.Forms.Button
    $ok.Text = 'Preview'
    $ok.DialogResult = 'OK'
    $grid.Controls.Add($ok, 1, 3)
    $dialog.AcceptButton = $ok
    try {
        if ($dialog.ShowDialog() -ne 'OK') { return }
        $task.StartUnix = ([DateTimeOffset]$from.Value).ToUnixTimeSeconds()
        $task.EndUnix = ([DateTimeOffset]$to.Value).ToUnixTimeSeconds()
        $task.Seconds = [int64]$amount.Value
        $task
    } finally { $dialog.Dispose() }
}

function Start-ClickUpSignIn {
    Start-RPC 'clickup sign-in' $null {
        param($result)
        $url = ClickUp-Link $result.AuthorizationURL
        if (-not $url -or $url.Host -ne 'app.clickup.com' -or $url.AbsolutePath -ne '/api') {
            Set-Notice 'ClickUp returned an invalid sign-in link.'
            return
        }
        Start-Process -FilePath $url.AbsoluteUri
        $ui.CheckConnection.Visibility = 'Visible'
        Set-Notice 'Finish signing in in your browser, then check the connection.'
    }
}

function Refresh-Tasks {
    Start-RPC 'clickup refresh' $null {
        param($result)
        Update-Catalog $result
        if ($script:view -eq 'Local' -and $result.Connected) { Show-View 'Tasks' }
        if ($result.Error) { Set-Notice $result.Error }
    }
}

$ui.Search.Add_TextChanged({ Update-Rows })
$ui.StartLocal.Add_Click({
    Start-RPC 'timer start --stdin' @{Name=$ui.SessionName.Text} {
        param($timer)
        $ui.SessionName.Clear()
        Show-StartedTimer $timer
    }
})
$ui.SignInButton.Add_Click({ Start-ClickUpSignIn })
$ui.CheckConnection.Add_Click({ Refresh-Tasks })
$ui.RefreshButton.Add_Click({ Refresh-Tasks })
$ui.TaskListButton.Add_Click({ Show-View 'Tasks' })
$ui.LocalFromTasks.Add_Click({ Show-View 'Local'; [void]$ui.SessionName.Focus() })
$ui.SwitchButton.Add_Click({ Show-View 'Tasks' })
$ui.ReturnTimer.Add_Click({ Show-View 'Timer' })
$ui.LocalReturnTimer.Add_Click({ Show-View 'Timer' })
$ui.BackDetails.Add_Click({ Show-View $script:detailFrom })
$ui.ShowHistory.Add_Click({ Show-History })
$ui.BackHistory.Add_Click({ Show-View $script:historyFrom })
$ui.ActiveDetails.Add_Click({
    if ($script:status.Timer.StartedUnix -gt 0 -and -not $script:status.Timer.Local) { Show-TaskDetails $script:status.Timer }
})
$ui.StopButton.Add_Click({
    Start-RPC 'timer stop' $null {
        param($timer)
        Update-Status ([pscustomobject]@{Timer=$timer;Entries=$script:status.Entries})
        $script:nextStatus = [DateTime]::UtcNow
    }
})
$ui.AssignButton.Add_Click({
    if ($script:pending -and $script:pending.Action -notin @('timer status','clickup catalog')) { return }
    $selection = Choose-Range (Task-Identity $script:detailTask)
    if (-not $selection) { return }
    $script:previewSelection = $selection
    Start-RPC 'clickup preview' $selection {
        param($preview)
        $selection = $script:previewSelection
        $script:previewSelection = $null
        if ($preview.Seconds -le 0) { Set-Notice "$($preview.AvailableSeconds) seconds available in this range."; return }
        $answer = [Windows.MessageBox]::Show($form, "Queue exactly $($preview.Seconds) seconds for $($selection.Name)?", 'Confirm ClickUp time', 'YesNo', 'Question')
        if ($answer -eq 'Yes') {
            $selection.Confirmation = $preview.Confirmation
            Start-RPC 'clickup allocate' $selection { param($r) Set-Notice "$($r.Seconds) seconds queued." }
        }
    }
})

$menu = New-Object Windows.Forms.ContextMenuStrip
$open = $menu.Items.Add('Open Antitimely')
$signin = $menu.Items.Add('Sign in with ClickUp')
$quit = $menu.Items.Add('Quit tray')
$iconPath = Join-Path $PSScriptRoot 'antitimely.ico'
$form.Icon = [Windows.Media.Imaging.BitmapFrame]::Create([uri]$iconPath,
    [Windows.Media.Imaging.BitmapCreateOptions]::None, [Windows.Media.Imaging.BitmapCacheOption]::OnLoad)
$trayIcon = [Drawing.Icon]::new($iconPath, [Windows.Forms.SystemInformation]::SmallIconSize)
$icon = New-Object Windows.Forms.NotifyIcon
$icon.Icon = $trayIcon
$icon.Text = 'Antitimely'
$icon.ContextMenuStrip = $menu

function Show-Panel([switch]$SkipCatalog) {
    $form.Show()
    if ($form.WindowState -eq 'Minimized') { $form.WindowState = 'Normal' }
    [void]$form.Activate()
    $script:nextStatus = [DateTime]::UtcNow
    if (-not $SkipCatalog -and -not $script:catalog -and -not $script:pending) {
        Start-RPC 'clickup catalog' $null {
            param($result)
            Update-Catalog $result
            if ($result.Connected -and $script:view -eq 'Local' -and -not $ui.SessionName.Text) { Show-View 'Tasks' }
        }
    }
}

$form.Add_Closing({
    param($sender,$eventArgs)
    if (-not $script:quitting) { $eventArgs.Cancel = $true; $form.Hide() }
})
$open.Add_Click({ Show-Panel })
$signin.Add_Click({ Show-Panel -SkipCatalog; Start-ClickUpSignIn })
$icon.Add_MouseClick({ param($sender,$eventArgs) if ($eventArgs.Button -eq 'Left') { Show-Panel } })
$quit.Add_Click({ $script:quitting = $true; $app.Shutdown() })

$app = New-Object Windows.Application
$app.ShutdownMode = 'OnExplicitShutdown'
$clock = New-Object Windows.Threading.DispatcherTimer
$clock.Interval = [TimeSpan]::FromMilliseconds(250)
$clock.Add_Tick({
    try {
        if ($script:pending) {
            $request = $script:pending
            if (-not $request.Process.HasExited -and [DateTime]::UtcNow -gt $request.Deadline) { $request.Process.Kill() }
            if ($request.Process.HasExited -and $request.Output.IsCompleted -and $request.Error.IsCompleted) {
                $script:pending = $null
                try {
                    if ($request.Process.ExitCode -eq 0) {
                        & $request.Done (ConvertFrom-Json -InputObject $request.Output.Result)
                    } else {
                        $problem = $request.Error.Result.Trim()
                        if ($request.Action -eq 'clickup sign-in' -and $problem -like '*OAuth app configured*') {
                            $problem = 'Your administrator still needs to enable ClickUp sign-in.'
                        }
                        if ($request.Action -eq 'clickup details' -and $script:view -eq 'Detail') { $ui.DetailBody.Children.Clear() }
                        Set-Notice $problem
                    }
                } finally { $request.Process.Dispose() }
            }
        } elseif ($form.IsVisible -and [DateTime]::UtcNow -ge $script:nextStatus) {
            $script:nextStatus = [DateTime]::UtcNow.AddSeconds(5)
            Start-RPC 'timer status' $null { param($r) Update-Status $r }
        }
        if ($script:view -eq 'Timer' -and $script:status.Timer.StartedUnix -gt 0) { Update-Header }
    } catch { Set-Notice $_.Exception.Message }
})

try {
    Update-Header
    $icon.Visible = $true
    $clock.Start()
    if ($ShowOnLaunch) { Show-Panel }
    [void]$app.Run()
} finally {
    $clock.Stop()
    if ($script:pending) {
        if (-not $script:pending.Process.HasExited) { $script:pending.Process.Kill() }
        $script:pending.Process.Dispose()
    }
    $icon.Visible = $false
    $icon.Dispose()
    $trayIcon.Dispose()
    $menu.Dispose()
    $instance.ReleaseMutex()
    $instance.Dispose()
}
