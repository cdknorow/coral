# build_windows.ps1 — Build Coral for Windows and create MSI installer
#
# Prerequisites:
#   - Go 1.23+ in PATH
#   - WiX Toolset v4+ (dotnet tool install --global wix)
#
# Usage:
#   .\scripts\build_windows.ps1              # build + package
#   .\scripts\build_windows.ps1 -SkipMSI     # build only

param(
    [switch]$SkipMSI,
    [string]$Version = "1.0.0",
    [string]$CertPath = "",
    [string]$CertPassword = "",
    [string]$TimestampServer = "http://timestamp.digicert.com"
)

$ErrorActionPreference = "Stop"

$RepoRoot = Split-Path -Parent (Split-Path -Parent $PSCommandPath)
$CoralGoDir = Join-Path $RepoRoot "coral-go"
$BuildDir = Join-Path $RepoRoot "build\windows"
$BinDir = Join-Path $BuildDir "bin"

Write-Host "=== Building Coral $Version for Windows ===" -ForegroundColor Cyan

# Clean build directory
if (Test-Path $BuildDir) { Remove-Item -Recurse -Force $BuildDir }
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null

# Build the Go binary
Write-Host "Building coral.exe..." -ForegroundColor Yellow
Push-Location $CoralGoDir
$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"

go build -ldflags "-s -w -X main.version=$Version" -o (Join-Path $BinDir "coral.exe") ./cmd/coral/
if ($LASTEXITCODE -ne 0) { throw "Go build failed for coral" }

Write-Host "Building launch-coral.exe..." -ForegroundColor Yellow
go build -ldflags "-s -w -X main.version=$Version" -o (Join-Path $BinDir "launch-coral.exe") ./cmd/launch-coral/
if ($LASTEXITCODE -ne 0) { throw "Go build failed for launch-coral" }

Write-Host "Building coral-board.exe..." -ForegroundColor Yellow
go build -ldflags "-s -w -X main.version=$Version" -o (Join-Path $BinDir "coral-board.exe") ./cmd/coral-board/
if ($LASTEXITCODE -ne 0) { throw "Go build failed for coral-board" }

Write-Host "Building coral-agent.exe..." -ForegroundColor Yellow
go build -ldflags "-s -w -X main.version=$Version" -o (Join-Path $BinDir "coral-agent.exe") ./cmd/coral-agent/
if ($LASTEXITCODE -ne 0) { throw "Go build failed for coral-agent" }

# Hook binaries
foreach ($hook in @("coral-hook-agentic-state", "coral-hook-task-sync", "coral-hook-message-check", "coral-hook-session-start")) {
    Write-Host "Building $hook.exe..." -ForegroundColor Yellow
    go build -ldflags "-s -w" -o (Join-Path $BinDir "$hook.exe") "./cmd/$hook/"
    if ($LASTEXITCODE -ne 0) { throw "Go build failed for $hook" }
}

# CGO binaries (systray, webview)
$env:CGO_ENABLED = "1"

Write-Host "Building coral-tray.exe..." -ForegroundColor Yellow
go build -ldflags "-s -w -X main.version=$Version -H windowsgui" -o (Join-Path $BinDir "coral-tray.exe") ./cmd/coral-tray/
if ($LASTEXITCODE -ne 0) {
    Write-Host "  WARN: coral-tray build failed (requires C compiler) — skipping" -ForegroundColor Yellow
} else {
    Write-Host "  Built coral-tray.exe" -ForegroundColor Green
}

Write-Host "Building coral-app.exe..." -ForegroundColor Yellow
go build -tags webview -ldflags "-s -w -H windowsgui" -o (Join-Path $BinDir "coral-app.exe") ./cmd/coral-app/
if ($LASTEXITCODE -ne 0) {
    Write-Host "  WARN: coral-app build failed (requires C compiler + WebView2) — skipping" -ForegroundColor Yellow
} else {
    Write-Host "  Built coral-app.exe" -ForegroundColor Green
}

Pop-Location
Write-Host "Binaries built in $BinDir" -ForegroundColor Green

# Copy icon if available
$IconSrc = Join-Path $RepoRoot "icons\coral.ico"
if (Test-Path $IconSrc) {
    Copy-Item $IconSrc $BuildDir
}

# Code signing
if ($CertPath -and (Test-Path $CertPath)) {
    Write-Host "Signing executables..." -ForegroundColor Yellow
    foreach ($exe in @("coral.exe", "launch-coral.exe", "coral-board.exe", "coral-hook-agentic-state.exe", "coral-hook-task-sync.exe", "coral-hook-message-check.exe", "coral-hook-session-start.exe", "coral-agent.exe", "coral-tray.exe", "coral-app.exe")) {
        $exePath = Join-Path $BinDir $exe
        if (Test-Path $exePath) {
            $signArgs = @("sign", "/fd", "SHA256", "/tr", $TimestampServer, "/td", "SHA256", "/f", $CertPath)
            if ($CertPassword) { $signArgs += @("/p", $CertPassword) }
            $signArgs += $exePath
            & signtool @signArgs
            if ($LASTEXITCODE -ne 0) { throw "Signing failed for $exe" }
            Write-Host "  Signed $exe" -ForegroundColor Green
        }
    }
} else {
    Write-Host "No certificate provided — skipping code signing" -ForegroundColor Yellow
}

if ($SkipMSI) {
    Write-Host "Skipping MSI (use -SkipMSI:$false to build installer)" -ForegroundColor Yellow
    exit 0
}

# Build MSI with WiX
Write-Host "Building MSI installer..." -ForegroundColor Yellow
$WixSrc = Join-Path $RepoRoot "scripts\coral.wxs"

if (-not (Test-Path $WixSrc)) {
    Write-Host "WiX source not found at $WixSrc — skipping MSI" -ForegroundColor Yellow
    exit 0
}

$MsiPath = Join-Path $BuildDir "Coral-$Version-x64.msi"

# Copy license RTF for WiX UI dialog
$LicenseSrc = Join-Path $RepoRoot "tools\license.rtf"
if (Test-Path $LicenseSrc) {
    Copy-Item $LicenseSrc $BuildDir
}

wix build $WixSrc `
    -ext WixToolset.UI.wixext `
    -d "Version=$Version" `
    -d "BinDir=$BinDir" `
    -d "BuildDir=$BuildDir" `
    -o $MsiPath

if ($LASTEXITCODE -ne 0) { throw "WiX build failed" }

# Sign MSI
if ($CertPath -and (Test-Path $CertPath)) {
    Write-Host "Signing MSI..." -ForegroundColor Yellow
    $signArgs = @("sign", "/fd", "SHA256", "/tr", $TimestampServer, "/td", "SHA256", "/f", $CertPath)
    if ($CertPassword) { $signArgs += @("/p", $CertPassword) }
    $signArgs += $MsiPath
    & signtool @signArgs
    if ($LASTEXITCODE -ne 0) { throw "MSI signing failed" }
    Write-Host "  MSI signed" -ForegroundColor Green
}

Write-Host "Installer built: $MsiPath" -ForegroundColor Green
Write-Host "=== Done ===" -ForegroundColor Cyan
