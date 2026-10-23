<#
.SYNOPSIS
  Builds one rehearsal release (an MSI) from this source, the way windows-tests' package job and release.yml build one (#6290). Dot-sourced by
  update_rehearsal_windows.ps1 and update_rehearsal_windows_route.ps1; it only defines Build-RehearsalRelease. The MSI path is left in $script:builtMsi.

  -ExtraTags adds build tags to every program (the route rehearsal adds "dev": a server with no EULA to click through). -StandIn builds the rehearsal's own
  updater (tag windowsrehearsal, never released) AS coral-update.exe instead of the real one.
#>
function Build-RehearsalRelease([string]$root, [string]$Work, [string]$version, [string]$extraTags = '', [switch]$standIn) {
    $gui = Get-Content (Join-Path $root 'tests\release\gui_commands.txt') | Where-Object { $_ -notmatch '^\s*(#|$)' } | ForEach-Object { $_.Trim() }
    $commands = Get-Content (Join-Path $root 'tests\release\commands.txt') | Where-Object { $_ -notmatch '^\s*(#|$)' } | ForEach-Object { $_.Trim() }
    $tags = (Select-String -Path (Join-Path $root 'tests\release\resolve_release.sh') -Pattern '^build_tags=(\S+)').Matches[0].Groups[1].Value
    if ($extraTags -ne '') { $tags = "$tags,$extraTags" }
    $bin = Join-Path $Work "bin-$version"
    New-Item -ItemType Directory -Force -Path $bin | Out-Null
    Push-Location (Join-Path $root 'coral-go')
    try {
        $env:GOOS = 'windows'; $env:GOARCH = 'amd64'
        $ldflags = "-s -w -X github.com/subgentic/coral/internal/config.Version=$version"
        bash ../tools/gen_windows_resources.sh $version Coral
        if ($LASTEXITCODE -ne 0) { throw 'gen_windows_resources.sh failed' }
        $env:CGO_ENABLED = '0'
        foreach ($cmd in $commands) {
            if ($gui -contains $cmd) { continue }
            if ($standIn -and $cmd -eq 'coral-update') {
                go build -trimpath -tags "$tags,windowsrehearsal" -ldflags "$ldflags" -o (Join-Path $bin 'coral-update.exe') ./internal/updater/testrig/cmd/rehearsal-update-windows/
                if ($LASTEXITCODE -ne 0) { throw 'go build the rehearsal updater failed' }
                continue
            }
            go build -trimpath -tags $tags -ldflags "$ldflags" -o (Join-Path $bin "$cmd.exe") "./cmd/$cmd/"
            if ($LASTEXITCODE -ne 0) { throw "go build $cmd failed" }
        }
        $env:CGO_ENABLED = '1'; $env:CC = 'gcc'
        go build -trimpath -tags $tags -ldflags "-H windowsgui $ldflags" -o (Join-Path $bin 'coral-tray.exe') ./cmd/coral-tray/
        if ($LASTEXITCODE -ne 0) { throw 'go build coral-tray failed' }
        go build -trimpath -tags "webview,$tags" -ldflags "-H windowsgui $ldflags" -o (Join-Path $bin 'coral-app.exe') ./cmd/coral-app/
        if ($LASTEXITCODE -ne 0) { throw 'go build coral-app failed' }
    } finally {
        Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED, Env:CC -ErrorAction SilentlyContinue
        Pop-Location
    }
    $exes = Get-ChildItem -Path $bin -Filter '*.exe' | ForEach-Object { $_.FullName -replace '\\', '/' }
    $licenses = ($bin -replace '\\', '/') + '/licenses'
    Push-Location $root
    try {
        bash installers/third-party-notices.sh $licenses @exes
        if ($LASTEXITCODE -ne 0) { throw 'third-party-notices.sh failed' }
    } finally { Pop-Location }
    $msi = Join-Path $Work "Coral-$version-x64.msi"
    Copy-Item (Join-Path $root 'tools\license.rtf') $Work -Force
    wix build (Join-Path $root 'tools\coral.wxs') -arch x64 -ext WixToolset.UI.wixext -d "Version=$version" -d "BinDir=$bin" -d "BuildDir=$Work" -o $msi
    if ($LASTEXITCODE -ne 0) { throw "wix build $version failed" }
    $script:builtMsi = $msi # not a return value: the native commands above write to the function's output too
}
