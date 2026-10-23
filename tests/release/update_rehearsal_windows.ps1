<#
.SYNOPSIS
  The Windows update rehearsal, stage 1 (#6290): two releases built from THIS source, N = 1.4.900 and N+1 = 1.4.901, put through the real Windows upgrade
  tests (tests/release/upgrade_windows.ps1: Windows Installer's major upgrade through apply.RunMSI, a tampered package, a stale click, a health failure rolled
  back for real), on a throwaway GitHub-hosted runner, without any published release.

.DESCRIPTION
  The programs and the MSI are built the way windows-tests' package job and release.yml build them (same tags, same commands list, WiX with -arch x64), with only
  the Version flag and the MSI's Version differing. The two versions are higher than any real release, so these packages must never leave the runner: nothing
  here is uploaded (only the table and the redacted logs are). The signer is the tests' own (the Go tests of internal/updater/apply stage the new MSI under their
  own Sigstore stand-in), which is the rig's identity, so nothing is fetched from anywhere.

  GitHub-hosted runner only (upgrade_windows.ps1 refuses anything else without -ThrowawayMachine).

.PARAMETER Work
  A folder for the build (default: $env:RUNNER_TEMP\rehearsal). It is removed at the end.
.PARAMETER LogDir
  Where the msiexec and apply logs of each scenario are kept (the workflow uploads only these, after redaction).
#>
[CmdletBinding()]
param(
    [string]$Work = '',
    [string]$LogDir = ''
)
$ErrorActionPreference = 'Stop'
function Fail([string]$msg) { Write-Host "FAIL: $msg" -ForegroundColor Red; exit 1 }
function Pass([string]$msg) { Write-Host "PASS: $msg" -ForegroundColor Green }

$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
if ($Work -eq '') { $Work = Join-Path $(if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { $env:TEMP }) 'rehearsal' }
if (Test-Path -LiteralPath $Work) { Remove-Item -Recurse -Force -LiteralPath $Work }
New-Item -ItemType Directory -Force -Path $Work | Out-Null
$N = '1.4.900'; $N1 = '1.4.901'

. (Join-Path $PSScriptRoot 'rehearsal_build.ps1')

try {
    Push-Location (Join-Path $root 'coral-go'); try { go mod download; if ($LASTEXITCODE -ne 0) { throw 'go mod download failed' } } finally { Pop-Location } # the notices come from the module cache
    dotnet tool install --global wix --version 5.0.2 | Out-Null
    wix extension add WixToolset.UI.wixext/5.0.2 | Out-Null
    Build-RehearsalRelease $root $Work $N | Out-Host; $oldMsi = $script:builtMsi
    Build-RehearsalRelease $root $Work $N1 | Out-Host; $newMsi = $script:builtMsi
    Pass "built $N and $N1 from this source (not uploaded: the versions are higher than any release)"
    $params = @{ OldMsi = $oldMsi; NewMsi = $newMsi; OldVersion = $N; NewVersion = $N1 } # a hashtable: an array would be passed by position
    if ($LogDir -ne '') { $params.LogDir = $LogDir }
    & (Join-Path $root 'tests\release\upgrade_windows.ps1') @params
    if ($LASTEXITCODE -ne 0) { Fail "upgrade_windows.ps1 exited $LASTEXITCODE" }
    Pass 'the Windows upgrade, the tampered package, the stale click and the rollback: all through the real Windows Installer, from this source'
} finally {
    if (Test-Path -LiteralPath $Work) { Remove-Item -Recurse -Force -LiteralPath $Work -ErrorAction SilentlyContinue }
}
