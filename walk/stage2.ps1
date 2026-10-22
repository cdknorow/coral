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
$up = $false
for ($i = 0; $i -lt 40; $i++) {
    Start-Sleep -Seconds 1
    try { $r = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:8450/api/health' -TimeoutSec 2; if ($r.StatusCode -eq 200) { $up = $true; break } } catch {}
}
"[{0:n1}s] server answers /api/health: {1}" -f $sw.Elapsed.TotalSeconds, $up
Start-Sleep -Seconds 4; Shot 'after-start-4s'
Start-Sleep -Seconds 12; Shot 'after-start-16s'
"--- processes"
Get-Process | Where-Object { $_.ProcessName -match 'coral|msedge|chrome|claude' } | Group-Object ProcessName | ForEach-Object { "  {0} x{1}" -f $_.Name, $_.Count }
"--- top windows"; TopWindows
"--- data dir"
Get-ChildItem C:\walk\data -ErrorAction SilentlyContinue | ForEach-Object { "  " + $_.Name }
"--- user .coral"
Get-ChildItem "$env:USERPROFILE\.coral" -Force -ErrorAction SilentlyContinue | ForEach-Object { "  " + $_.Name }
"--- tray log tail"
foreach ($f in @('C:\walk\data\tray.log', "$env:USERPROFILE\.coral\tray.log")) { if (Test-Path $f) { "== $f"; Get-Content $f -Tail 25 } }

"--- the app window, once more with a debugging port, so the walk can drive it (the tray's own window above is what a person first sees)"
Get-Process coral-app -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Seconds 2
$env:WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS = '--remote-debugging-port=9333 --remote-allow-origins=*'
Start-Process 'C:\Program Files (x86)\Coral\coral-app.exe' -ArgumentList '--url', 'http://localhost:8450', '--data-dir', 'C:\walk\data'
$ok = $false
for ($i = 0; $i -lt 30; $i++) { Start-Sleep -Seconds 1; try { $t = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:9333/json/version' -TimeoutSec 2; $ok = $true; break } catch {} }
"debug port 9333 answers: $ok"
