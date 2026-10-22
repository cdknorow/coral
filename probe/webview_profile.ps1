$ErrorActionPreference = 'Continue'
$exe = 'C:\walk\coral-app.exe'
New-Item -ItemType Directory -Force C:\walk | Out-Null
Push-Location coral-go
$env:CGO_ENABLED = '1'; $env:CC = 'gcc'
go build -tags 'webview,fts5,dev' -ldflags '-H windowsgui' -o $exe ./cmd/coral-app
if ($LASTEXITCODE -ne 0) { throw 'coral-app did not build' }
Pop-Location
$roaming = Join-Path $env:APPDATA 'coral-app.exe'
$local = Join-Path $env:LOCALAPPDATA 'Coral\WebView2'
Remove-Item -Recurse -Force $roaming, $local -ErrorAction SilentlyContinue
Write-Host "before: roaming exists=$(Test-Path $roaming) local exists=$(Test-Path $local)"
$env:CORAL_DATA_DIR = 'C:\walk\data'; New-Item -ItemType Directory -Force $env:CORAL_DATA_DIR | Out-Null
Remove-Item Env:WEBVIEW2_USER_DATA_FOLDER -ErrorAction SilentlyContinue
$p = Start-Process -FilePath $exe -ArgumentList '--url', 'about:blank' -PassThru
Start-Sleep -Seconds 25
Write-Host "app running after 25 s: $(-not $p.HasExited)"
Write-Host "after: roaming exists=$(Test-Path $roaming) (EBWebView: $(Test-Path (Join-Path $roaming 'EBWebView')))"
Write-Host "after: local exists=$(Test-Path $local)"
if (Test-Path $local) { Get-ChildItem $local | Select-Object -First 12 | ForEach-Object { Write-Host "  local: $($_.Name)" } }
if (Test-Path $roaming) { Get-ChildItem $roaming -Recurse -Depth 1 | Select-Object -First 8 | ForEach-Object { Write-Host "  roaming: $($_.FullName.Replace($env:USERNAME,'<user>'))" } }
Get-Process -Name 'coral-app','msedgewebview2' -ErrorAction SilentlyContinue | Stop-Process -Force
Get-ChildItem C:\walk\data -Filter *.log -Recurse -ErrorAction SilentlyContinue | Select-Object -First 2 | ForEach-Object { Write-Host "log $($_.Name):"; Get-Content $_.FullName -Tail 12 | ForEach-Object { Write-Host "   $($_ -replace [regex]::Escape($env:USERNAME),'<user>')" } }
