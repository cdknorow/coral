. "$PSScriptRoot\common.ps1"
# Build this branch's coral-tray (it hosts the server) and coral-app, and put them over the installed ones: the walk then meets this branch's behaviour in the published package's layout.
Get-Process coral-tray, coral-app, coral -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
$out = 'C:\walk\overlay'; New-Item -ItemType Directory -Force $out | Out-Null
Push-Location coral-go
$env:CGO_ENABLED = '1'; $env:CC = 'gcc'
$ld = '-H windowsgui -X github.com/subgentic/coral/internal/config.Version=1.4.155'
go build -tags fts5 -ldflags $ld -o "$out\coral-tray.exe" ./cmd/coral-tray
"coral-tray build exit: $LASTEXITCODE"
go build -tags 'webview,fts5' -ldflags $ld -o "$out\coral-app.exe" ./cmd/coral-app
"coral-app build exit: $LASTEXITCODE"
Pop-Location
foreach ($f in 'coral-tray.exe', 'coral-app.exe') { Copy-Item "$out\$f" "C:\Program Files (x86)\Coral\$f" -Force; "overlaid $f" }
