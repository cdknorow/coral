. "$PSScriptRoot\common.ps1"
$ver = $env:WALK_VERSION
$dl = Join-Path $env:RUNNER_TEMP 'dl'; New-Item -ItemType Directory -Force $dl | Out-Null
gh release download "v$ver" --repo $env:GITHUB_REPOSITORY --dir $dl --pattern "Coral-$ver-x64.msi" --pattern 'SHA256SUMS.txt'
$msi = Join-Path $dl "Coral-$ver-x64.msi"
$want = ((Get-Content (Join-Path $dl 'SHA256SUMS.txt') | Where-Object { $_ -match "Coral-$ver-x64.msi" }) -split '\s+')[0].ToLower()
"msi sha256 ok: $($want -eq (Get-FileHash $msi -Algorithm SHA256).Hash.ToLower())"
"running as: " + [Security.Principal.WindowsIdentity]::GetCurrent().Name + ", session " + (Get-Process -Id $PID).SessionId + ", elevated " + ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
Shot 'desktop-before'
$sw = [Diagnostics.Stopwatch]::StartNew()
$p = Start-Process msiexec -ArgumentList "/i `"$msi`" /l*v `"$env:RUNNER_TEMP\walk-msi.log`"" -PassThru
$w = FindWindow 'Coral Setup' 0 40
"[{0:n1}s] first screen: {1}" -f $sw.Elapsed.TotalSeconds, [bool]$w
Start-Sleep -Seconds 2; Shot 'msi-license'
"--- before accepting: Install enabled? " + (FindControl $w 'Button' 'Install').Current.IsEnabled
ToggleCheck $w 'I accept*' $true | Out-Null
Start-Sleep -Milliseconds 700; Shot 'msi-license-accepted'
"--- after accepting: Install enabled? " + (FindControl $w 'Button' 'Install').Current.IsEnabled
ClickButton $w 'Install' | Out-Null
$t0 = $sw.Elapsed.TotalSeconds
$seen = @{}
for ($i = 0; $i -lt 90; $i++) {
    Start-Sleep -Seconds 2
    $names = @(TopWindows | Where-Object { $_ -match 'Coral|Windows Installer|User Account|Setup' })
    $key = ($names -join ';')
    if (-not $seen.ContainsKey($key)) { $seen[$key] = 1; "[{0:n1}s] windows: {1}" -f $sw.Elapsed.TotalSeconds, $key; Shot ('msi-progress-' + $i) }
    $w2 = FindWindow 'Coral Setup' 0 1
    if ($w2) {
        $finish = FindControl $w2 'Button' 'Finish'
        if ($finish) { "[{0:n1}s] Finish dialog reached" -f $sw.Elapsed.TotalSeconds; Describe $w2 30; break }
    }
    if ($p.HasExited) { "msiexec exited: $($p.ExitCode)"; break }
}
Start-Sleep -Seconds 1; Shot 'msi-finish'
"install took {0:n0}s from clicking Install" -f ($sw.Elapsed.TotalSeconds - $t0)
if ($w2) { ToggleCheck $w2 'Launch*' $true | Out-Null; ClickButton $w2 'Finish' | Out-Null }
Start-Sleep -Seconds 12
"--- after Finish: coral processes"
Get-Process | Where-Object { $_.ProcessName -like 'coral*' } | ForEach-Object { "  {0} pid {1}" -f $_.ProcessName, $_.Id }
Shot 'after-finish'
"--- installed files"
Get-ChildItem 'C:\Program Files\Coral' -ErrorAction SilentlyContinue | ForEach-Object { "  " + $_.Name }
"--- start menu"
Get-ChildItem 'C:\ProgramData\Microsoft\Windows\Start Menu\Programs' -Filter '*Coral*' -ErrorAction SilentlyContinue | ForEach-Object { "  " + $_.Name }
"--- msiexec exit: " + $(if ($p.HasExited) { $p.ExitCode } else { 'still running' })
Select-String -Path "$env:RUNNER_TEMP\walk-msi.log" -Pattern 'LaunchCoral|WriteEULAMarker|Return value|Installation (success|failed)' -ErrorAction SilentlyContinue | Select-Object -First 12 | ForEach-Object { $_.Line.Substring(0, [Math]::Min(170, $_.Line.Length)) }
"--- eula marker: " + (Test-Path "$env:USERPROFILE\.coral\.eula-accepted")
