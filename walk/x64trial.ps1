. "$PSScriptRoot\common.ps1"
function Section($t) { "`n=== $t" }
function Coral-Installs { Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*','HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*' -ErrorAction SilentlyContinue | Where-Object { $_.DisplayName -like 'Coral*' } | ForEach-Object { "  {0} {1} (key {2})" -f $_.DisplayName, $_.DisplayVersion, $_.PSChildName } }
function PathEntries { ([Environment]::GetEnvironmentVariable('Path', 'Machine') -split ';') | Where-Object { $_ -like '*Coral*' } | ForEach-Object { "  PATH: $_" } }
$ver = '1.4.155'
$dl = Join-Path $env:RUNNER_TEMP 'dl'; New-Item -ItemType Directory -Force $dl | Out-Null
gh release download "v$ver" --repo $env:GITHUB_REPOSITORY --dir $dl --pattern "Coral-$ver-x64.msi"
Section 'A. the published 1.4.155 (32-bit) package, installed'
$p = Start-Process msiexec -ArgumentList "/i `"$dl\Coral-$ver-x64.msi`" /qn /norestart /l*v `"$env:RUNNER_TEMP\old.log`"" -Wait -PassThru
"exit $($p.ExitCode)"; Coral-Installs; PathEntries
"x86 folder: " + (Test-Path 'C:\Program Files (x86)\Coral\coral.exe') + "; x64 folder: " + (Test-Path 'C:\Program Files\Coral\coral.exe')
Section 'B. a new x64 package (version 9.9.9, built here with -arch x64) installed over it'
$bin = Join-Path $env:RUNNER_TEMP 'bin'; New-Item -ItemType Directory -Force "$bin\licenses" | Out-Null
foreach ($n in 'coral','launch-coral','coral-board','coral-app','coral-hook-agentic-state','coral-hook-task-sync','coral-hook-message-check','coral-hook-session-start','coral-agent','coral-update') { Set-Content "$bin\$n.exe" 'x' }
Set-Content "$bin\licenses\a.txt" 'x'
Push-Location coral-go; $env:CGO_ENABLED = '1'; $env:CC = 'gcc'
go build -tags fts5 -ldflags "-H windowsgui -X github.com/subgentic/coral/internal/config.Version=9.9.9" -o "$bin\coral-tray.exe" ./cmd/coral-tray
"tray build exit: $LASTEXITCODE"; Pop-Location
Copy-Item tools\license.rtf $env:RUNNER_TEMP
dotnet tool install --global wix --version 5.0.2 | Out-Null; wix extension add WixToolset.UI.wixext/5.0.2 | Out-Null
wix build tools\coral.wxs -arch x64 -ext WixToolset.UI.wixext -d "Version=9.9.9" -d "BinDir=$bin" -d "BuildDir=$env:RUNNER_TEMP" -o "$env:RUNNER_TEMP\new.msi"
"wix build exit: $LASTEXITCODE"
$txt = [Text.Encoding]::GetEncoding(28591).GetString([IO.File]::ReadAllBytes("$env:RUNNER_TEMP\new.msi"))
"template: " + $(if ($txt.Contains('x64;')) { 'x64' } elseif ($txt.Contains('Intel;')) { 'Intel (32-bit)' } else { 'unknown' })
$p = Start-Process msiexec -ArgumentList "/i `"$env:RUNNER_TEMP\new.msi`" /qn /norestart /l*v `"$env:RUNNER_TEMP\new.log`"" -Wait -PassThru
"upgrade exit: $($p.ExitCode)"; Coral-Installs; PathEntries
"x86 folder still there: " + (Test-Path 'C:\Program Files (x86)\Coral\coral.exe') + "; x64 folder: " + (Test-Path 'C:\Program Files\Coral\coral.exe')
Get-ChildItem 'C:\Program Files\Coral' -ErrorAction SilentlyContinue | ForEach-Object { "  C:\Program Files\Coral\" + $_.Name }
Select-String -Path "$env:RUNNER_TEMP\new.log" -Pattern 'FindRelatedProducts|RemoveExistingProducts|Return value 3' | Select-Object -First 6 | ForEach-Object { $_.Line.Substring(0, [Math]::Min(150, $_.Line.Length)) }
Section 'C. the installer-launched tray, elevated (as an administrator-run installer would start it)'
$tray = Start-Process 'C:\Program Files\Coral\coral-tray.exe' -ArgumentList '--from-installer' -PassThru
Start-Sleep -Seconds 4
$w = FindWindow 'Coral' $tray.Id 15
if ($w) { "message box found, class $($w.Current.ClassName)"; Describe $w 12; ShotWin 'elevated-launch-note' $w } else { "no message box from the refused launch"; TopWindows }
"tray process still running (waiting on OK): " + (-not $tray.HasExited)
if ($w) { ClickButton $w 'OK' | Out-Null; Start-Sleep -Seconds 2; "after OK, tray exited: " + $tray.HasExited }
Get-Process coral-tray -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Section 'D. uninstall'
$p = Start-Process msiexec -ArgumentList "/x `"$env:RUNNER_TEMP\new.msi`" /qn /norestart" -Wait -PassThru
"exit $($p.ExitCode)"; Coral-Installs; PathEntries
"x64 folder: " + (Test-Path 'C:\Program Files\Coral'); "x86 folder: " + (Test-Path 'C:\Program Files (x86)\Coral')
