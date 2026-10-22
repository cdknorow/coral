. "$PSScriptRoot\common.ps1"
"--- where did the installer put Coral"
Get-ChildItem 'C:\Program Files\Coral' -ErrorAction SilentlyContinue | ForEach-Object { "  C:\Program Files\Coral\" + $_.Name }
Get-ChildItem 'C:\Program Files (x86)\Coral' -ErrorAction SilentlyContinue | ForEach-Object { "  C:\Program Files (x86)\Coral\" + $_.Name }
$lnk = Get-ChildItem 'C:\ProgramData\Microsoft\Windows\Start Menu\Programs' -Recurse -Filter '*Coral*.lnk' -ErrorAction SilentlyContinue | Select-Object -First 1
"start menu shortcut: $($lnk.FullName)"
if ($lnk) { $sh = New-Object -ComObject WScript.Shell; $l = $sh.CreateShortcut($lnk.FullName); "  target: $($l.TargetPath)  args: '$($l.Arguments)'  dir: $($l.WorkingDirectory)" }
$env:CORAL_DATA_DIR = 'C:\walk\data'; $env:CORAL_CLOUD_URL = 'off'
New-Item -ItemType Directory -Force -Path C:\walk\data | Out-Null
$env:PATH = 'C:\walk\bin;' + $env:PATH
$sw = [Diagnostics.Stopwatch]::StartNew()
Start-Process $lnk.FullName
"launched the Start Menu entry"
Start-Sleep -Seconds 8
if (-not (Get-Process coral-tray -ErrorAction SilentlyContinue)) {
    "NOTE: the Start Menu entry started nothing within 8s on this run; starting its target the same way (a runner quirk is possible: ShellExecute of a .lnk)"
    Start-Process 'C:\Program Files (x86)\Coral\coral-tray.exe' -ArgumentList '--backend', 'pty' -WorkingDirectory 'C:\Program Files (x86)\Coral'
}
$up = $false
for ($i = 0; $i -lt 40; $i++) {
    Start-Sleep -Seconds 1
    try { $r = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:8450/api/health' -TimeoutSec 2; if ($r.StatusCode -eq 200) { $up = $true; break } } catch {}
}
"[{0:n1}s] server answers /api/health: {1}" -f $sw.Elapsed.TotalSeconds, $up
Start-Sleep -Seconds 4; $aw = FindWindow 'Coral' 0 5; if ($aw) { ShotWin 'app-window-4s' $aw } else { Shot 'after-start-4s' }
Start-Sleep -Seconds 12; $aw = FindWindow 'Coral' 0 5; if ($aw) { ShotWin 'app-window-16s' $aw; "app window: $($aw.Current.BoundingRectangle)" } else { Shot 'after-start-16s' }
"--- processes"
Get-Process | Where-Object { $_.ProcessName -match 'coral|msedge|chrome|claude' } | Group-Object ProcessName | ForEach-Object { "  {0} x{1}" -f $_.Name, $_.Count }
"--- top windows"; TopWindows
"--- data dir"
Get-ChildItem C:\walk\data -ErrorAction SilentlyContinue | ForEach-Object { "  " + $_.Name }
"--- user .coral"
Get-ChildItem "$env:USERPROFILE\.coral" -Force -ErrorAction SilentlyContinue | ForEach-Object { "  " + $_.Name }
"--- tray log tail"
foreach ($f in @('C:\walk\data\tray.log', "$env:USERPROFILE\.coral\tray.log")) { if (Test-Path $f) { "== $f"; Get-Content $f -Tail 25 } }


"--- the notification area (tray icon)"
$root = [System.Windows.Automation.AutomationElement]::RootElement
$tray = $root.FindFirst([System.Windows.Automation.TreeScope]::Children, (New-Object System.Windows.Automation.PropertyCondition ([System.Windows.Automation.AutomationElement]::ClassNameProperty), 'Shell_TrayWnd'))
if ($tray) {
  foreach ($c in $tray.FindAll([System.Windows.Automation.TreeScope]::Descendants, [System.Windows.Automation.Condition]::TrueCondition)) { try { if ($c.Current.ControlType.ProgrammaticName -match 'Button') { "  tray button: '{0}'" -f $c.Current.Name } } catch {} }
  $chev = FindControl $tray 'Button' '*hidden icons*'
  if (-not $chev) { $chev = FindControl $tray 'Button' '*Show Hidden*' }
  if ($chev) { "found the overflow chevron: $($chev.Current.Name)"; try { $chev.GetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern).Invoke() } catch { "invoke chevron failed: $($_.Exception.Message)" }; Start-Sleep -Seconds 2 }
  Shot 'tray-overflow-open'
  $icon = $null
  foreach ($c in $root.FindAll([System.Windows.Automation.TreeScope]::Descendants, [System.Windows.Automation.Condition]::TrueCondition)) {
    try { if ($c.Current.Name -like '*Coral*' -and $c.Current.ClassName -notmatch 'TaskList|webview|MsiDialog') { "tray candidate: '{0}' type {1} class {2} at {3}" -f $c.Current.Name, $c.Current.ControlType.ProgrammaticName, $c.Current.ClassName, $c.Current.BoundingRectangle; if (-not $icon) { $icon = $c } } } catch {}
  }
  if ($icon) {
    $r = $icon.Current.BoundingRectangle
    [Mouse]::Click([int]($r.X + $r.Width / 2), [int]($r.Y + $r.Height / 2), $true)
    Start-Sleep -Seconds 2; Shot 'tray-menu'
    "--- tray menu"; TopWindows | Where-Object { $_ -match '32768|Menu|Context' }
    foreach ($m in $root.FindAll([System.Windows.Automation.TreeScope]::Children, [System.Windows.Automation.Condition]::TrueCondition)) { try { if ($m.Current.ClassName -eq '#32768') { Describe $m 30 } } catch {} }
    [System.Windows.Forms.SendKeys]::SendWait('{ESC}')
  } else { "no Coral icon found in the notification area or its overflow" }
}
