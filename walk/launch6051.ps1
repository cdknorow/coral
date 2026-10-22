. "$PSScriptRoot\common.ps1"
function Section($t) { "`n=== $t" }
$ver = $env:WALK_VERSION
Section "install Coral $ver silently, as a first-time person's computer would have it"
$dl = Join-Path $env:RUNNER_TEMP 'dl'; New-Item -ItemType Directory -Force $dl | Out-Null
gh release download "v$ver" --repo $env:GITHUB_REPOSITORY --dir $dl --pattern "Coral-$ver-x64.msi" --pattern 'SHA256SUMS.txt'
$msi = Join-Path $dl "Coral-$ver-x64.msi"
$want = ((Get-Content (Join-Path $dl 'SHA256SUMS.txt') | Where-Object { $_ -match "Coral-$ver-x64.msi" }) -split '\s+')[0].ToLower()
"msi sha256 ok: $($want -eq (Get-FileHash $msi -Algorithm SHA256).Hash.ToLower())"
$p = Start-Process msiexec -ArgumentList "/i `"$msi`" /qn /norestart /l*v `"$env:RUNNER_TEMP\walk-msi.log`"" -Wait -PassThru
"msiexec exit $($p.ExitCode)"; if ($p.ExitCode -ne 0) { throw "install failed" }
$coralDir = if (Test-Path 'C:\Program Files\Coral\coral-tray.exe') { 'C:\Program Files\Coral' } else { 'C:\Program Files (x86)\Coral' }
"installed in $coralDir"

Section "overlay the 156 build plus #6009 and #6018 (this checkout) on the installed programs"
Get-Process coral-tray, coral-app, coral -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
$ov = 'C:\walk\overlay'; New-Item -ItemType Directory -Force $ov | Out-Null
Push-Location coral-go
$ld = '-s -w -X github.com/subgentic/coral/internal/config.Version=1.4.156'
$gui = @('coral-tray', 'coral-app')
$cmds = Get-Content ..\tests\release\commands.txt | Where-Object { $_ -notmatch '^\s*(#|$)' } | ForEach-Object { $_.Trim() }
$env:CGO_ENABLED = '0'
foreach ($c in $cmds) { if ($gui -contains $c) { continue }; go build -tags fts5 -ldflags $ld -o "$ov\$c.exe" "./cmd/$c/"; if ($LASTEXITCODE -ne 0) { throw "build $c failed" } }
$env:CGO_ENABLED = '1'; $env:CC = 'gcc'
go build -tags fts5 -ldflags "-H windowsgui $ld" -o "$ov\coral-tray.exe" ./cmd/coral-tray/; if ($LASTEXITCODE -ne 0) { throw "build coral-tray failed" }
go build -tags 'webview,fts5' -ldflags "-H windowsgui $ld" -o "$ov\coral-app.exe" ./cmd/coral-app/; if ($LASTEXITCODE -ne 0) { throw "build coral-app failed" }
Pop-Location
Get-ChildItem $ov -Filter *.exe | ForEach-Object { Copy-Item $_.FullName "$coralDir\$($_.Name)" -Force }
"overlaid " + (Get-ChildItem $ov -Filter *.exe).Count + " programs: coral-tray is " + (& "$coralDir\coral.exe" --version 2>&1 | Select-Object -First 1)

Section "the real CLIs, installed the way their docs say (npm -g), NOT signed in"
npm install -g --no-audit --no-fund --loglevel=error '@anthropic-ai/claude-code' '@openai/codex@0.163.0' 2>&1 | Select-Object -Last 3
$npmBin = (npm config get prefix).Trim()
"npm prefix: $npmBin"; Get-ChildItem $npmBin -Filter 'claude*' | ForEach-Object { "  " + $_.Name }; Get-ChildItem $npmBin -Filter 'codex*' | ForEach-Object { "  " + $_.Name }

Section "the PowerShell execution policy, as a Windows 11 client has it by default (Restricted)"
Set-ExecutionPolicy Restricted -Scope LocalMachine -Force -ErrorAction Continue
Set-ExecutionPolicy Undefined -Scope CurrentUser -Force -ErrorAction Continue
Get-ExecutionPolicy -List | ForEach-Object { "  {0,-14} {1}" -f $_.Scope, $_.ExecutionPolicy }

Section "what the CLIs do in each shell under that policy"
"--- claude --version through cmd.exe: " + (cmd /c "claude --version" 2>&1)
"--- codex --version through cmd.exe: " + (cmd /c "codex --version" 2>&1)
"--- claude --version through powershell.exe (Windows PowerShell 5.1) under Restricted:"
powershell.exe -NoProfile -Command "claude --version" 2>&1 | Select-Object -First 4
"--- claude --version through pwsh (PowerShell 7) under Restricted:"
pwsh -NoProfile -Command "claude --version" 2>&1 | Select-Object -First 4
"--- Git for Windows (Claude Code needs its bash on Windows): " + (Get-Command bash -ErrorAction SilentlyContinue).Source

Section "start Coral from its Start Menu target with the CLIs on the PATH of the person's session"
New-Item -ItemType Directory -Force C:\walk\data, C:\walk\work, ([string]::Concat('C:\walk\Ordner mit Leerzeichen und ', [char]0xDC, 'mlauten ', [char]0xC4, [char]0xD6)) | Out-Null
$env:CORAL_DATA_DIR = 'C:\walk\data'; $env:CORAL_CLOUD_URL = 'off'
$env:PATH = "$npmBin;" + $env:PATH
Start-Process "$coralDir\coral-tray.exe" -ArgumentList '--backend', 'pty' -WorkingDirectory $coralDir
$up = $false
for ($i = 0; $i -lt 60; $i++) { Start-Sleep -Seconds 1; try { $r = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:8450/api/health' -TimeoutSec 2; if ($r.StatusCode -eq 200) { $up = $true; break } } catch {} }
"server answers: $up after $i s"
if (-not $up) { throw "Coral did not start" }

Section "the launches"
$env:WALK_OUT = Join-Path $env:GITHUB_WORKSPACE 'walk-out'
node walk/launch6051.mjs phase1
Section "tray.log tail (what Coral itself logged)"
Get-Content C:\walk\data\tray.log -Tail 60 -ErrorAction SilentlyContinue | ForEach-Object { if ($_.Length -gt 240) { $_.Substring(0, 240) } else { $_ } }
Section "logs folder"
Get-ChildItem C:\walk\data\logs -ErrorAction SilentlyContinue | ForEach-Object { "  {0} {1} bytes" -f $_.Name, $_.Length }

Section "phase 2: Coral started again with a leaner session (no PowerShell 7, no Git for Windows on the PATH), as a computer that never installed them"
Get-Process coral-tray, coral-app, coral -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 3
$lean = ($env:PATH -split ';' | Where-Object { $_ -and $_ -notlike 'C:\Program Files\PowerShell*' -and $_ -notlike 'C:\Program Files\Git*' -and $_ -notlike '*\Git\*' }) -join ';'
$env:PATH = $lean
"is pwsh on the PATH: " + [bool](Get-Command pwsh -ErrorAction SilentlyContinue) + "; bash: " + [bool](Get-Command bash -ErrorAction SilentlyContinue) + "; git: " + [bool](Get-Command git -ErrorAction SilentlyContinue)
Start-Process "$coralDir\coral-tray.exe" -ArgumentList '--backend', 'pty' -WorkingDirectory $coralDir
$up = $false
for ($i = 0; $i -lt 60; $i++) { Start-Sleep -Seconds 1; try { $r = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:8450/api/health' -TimeoutSec 2; if ($r.StatusCode -eq 200) { $up = $true; break } } catch {} }
"server answers: $up after $i s"
node walk/launch6051.mjs phase2
Section "tray.log tail after phase 2"
Get-Content C:\walk\data\tray.log -Tail 40 -ErrorAction SilentlyContinue | ForEach-Object { if ($_.Length -gt 240) { $_.Substring(0, 240) } else { $_ } }
