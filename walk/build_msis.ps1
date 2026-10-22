# Builds the programs (as windows-tests' package job does) and two MSIs: before.msi from v158's installer, after.msi from this branch's.
$ErrorActionPreference = 'Stop'
$version = '0.0.0-ci'
Push-Location coral-go
$env:GOOS = 'windows'; $env:GOARCH = 'amd64'
$ldflags = "-s -w -X github.com/subgentic/coral/internal/config.Version=$version"
bash ../tools/gen_windows_resources.sh $version Coral
if ($LASTEXITCODE -ne 0) { throw 'gen_windows_resources.sh failed' }
New-Item -ItemType Directory -Force -Path ..\dist\windows\bin | Out-Null
$gui = Get-Content ..\tests\release\gui_commands.txt | Where-Object { $_ -notmatch '^\s*(#|$)' } | ForEach-Object { $_.Trim() }
$commands = Get-Content ..\tests\release\commands.txt | Where-Object { $_ -notmatch '^\s*(#|$)' } | ForEach-Object { $_.Trim() }
$env:CGO_ENABLED = '0'
foreach ($cmd in $commands) { if ($gui -contains $cmd) { continue }; go build -tags fts5 -ldflags "$ldflags" -o "..\dist\windows\bin\$cmd.exe" "./cmd/$cmd/"; if ($LASTEXITCODE -ne 0) { throw "go build $cmd failed" } }
$env:CGO_ENABLED = '1'; $env:CC = 'gcc'
go build -tags fts5 -ldflags "-H windowsgui $ldflags" -o '..\dist\windows\bin\coral-tray.exe' ./cmd/coral-tray/
if ($LASTEXITCODE -ne 0) { throw 'go build coral-tray failed' }
go build -tags 'webview,fts5' -ldflags "-H windowsgui $ldflags" -o '..\dist\windows\bin\coral-app.exe' ./cmd/coral-app/
if ($LASTEXITCODE -ne 0) { throw 'go build coral-app failed' }
Pop-Location
bash installers/third-party-notices.sh dist/windows/bin/licenses (Get-ChildItem dist/windows/bin/*.exe | ForEach-Object { $_.FullName -replace '\\','/' })
dotnet tool install --global wix --version 5.0.2
wix extension add WixToolset.UI.wixext/5.0.2
$binDir = (Resolve-Path 'dist\windows\bin').Path; $buildDir = (Resolve-Path 'dist\windows').Path
Copy-Item 'tools\license.rtf' $buildDir
foreach ($v in @(@('before', 'tools\coral_before.wxs'), @('after', 'tools\coral.wxs'))) {
  $out = Join-Path $buildDir ("{0}.msi" -f $v[0])
  wix build $v[1] -arch x64 -ext WixToolset.UI.wixext -d "Version=$version" -d "BinDir=$binDir" -d "BuildDir=$buildDir" -o $out
  if ($LASTEXITCODE -ne 0) { throw "wix build $($v[0]) failed" }
  "built $out"
}
