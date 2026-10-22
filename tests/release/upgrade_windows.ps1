<#
.SYNOPSIS
  The real Windows upgrade from the previous release (#162, #167), on a THROWAWAY Windows machine (a CI runner): the previous release's MSI is
  installed per machine, the new release's MSI goes through coral-update's Windows apply (apply.RunMSI) with the real Windows Installer, and the
  rollback is exercised for real.

.DESCRIPTION
  Like tests/release/upgrade_linux.sh and upgrade_macos.sh it runs the Go tests in coral-go/internal/updater/apply (windows_real_test.go) with the two
  releases' packages. What it checks:

    - old MSI -> new MSI: a major upgrade by Windows Installer; the old product replaced, the new coral.exe in place (--version), the high-water
      mark moved; and msiexec with basic UI (/qb!-) starts no Coral by itself (nothing of the install runs when the apply goes to start it);
    - a tampered package (one byte changed after it was staged) is refused before Windows Installer is asked anything; the old release stays;
    - a click that is not for what is staged (none, or another version) is refused, nothing installed;
    - a new release that does not stay up is rolled back for real: the new product removed, the previous MSI installed again.

  It INSTALLS AND REMOVES software on the machine it runs on. So it refuses to run unless that machine is a CI runner (GITHUB_ACTIONS=true) or you
  pass -ThrowawayMachine, it needs an administrator (so Windows Installer asks no consent), and it refuses when any Coral is installed. It never
  starts Coral (a runner has no tray to supervise it) and shows no message box. The windows apply is OFF in releases (windowsApplyEnabled=false); this
  test drives apply.RunMSI directly.

  Check the two MSIs against the releases' SHA256SUMS.txt first, as the updater does: -SumsFile does it here.

.PARAMETER OldMsi
  The previous release's Coral-<version>-x64.msi.
.PARAMETER NewMsi
  The new release's Coral-<version>-x64.msi.
.PARAMETER OldVersion
  The previous release's version, e.g. 1.4.150.
.PARAMETER NewVersion
  The new release's version, e.g. 1.4.151.
.PARAMETER SumsFile
  Optional: a SHA256SUMS.txt that lists both MSIs; their hashes must match it.
.PARAMETER LogDir
  Optional: a folder where the msiexec logs and the apply's logs of each scenario are kept (the runner workflow uploads it).
.PARAMETER ThrowawayMachine
  Say that this machine may have Coral installed and removed (when it is not a GitHub Actions runner).
.PARAMETER DryRun
  Check everything and print what would run; install nothing.

.EXAMPLE
  pwsh tests/release/upgrade_windows.ps1 -OldMsi prev\Coral-1.4.150-x64.msi -NewMsi new\Coral-1.4.151-x64.msi -OldVersion 1.4.150 -NewVersion 1.4.151
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$OldMsi,
    [Parameter(Mandatory = $true)][string]$NewMsi,
    [Parameter(Mandatory = $true)][string]$OldVersion,
    [Parameter(Mandatory = $true)][string]$NewVersion,
    [string]$SumsFile = '',
    [string]$LogDir = '',
    [switch]$ThrowawayMachine,
    [switch]$DryRun
)

$ErrorActionPreference = 'Stop'

function Fail([string]$msg) { Write-Host "FAIL: $msg" -ForegroundColor Red; exit 1 }
function Pass([string]$msg) { Write-Host "PASS: $msg" -ForegroundColor Green }

# Versions are plain X.Y.Z (the same shape the updater accepts), and the old one is strictly older.
foreach ($v in @($OldVersion, $NewVersion)) {
    if ($v -notmatch '^\d{1,3}\.\d{1,3}\.\d{1,4}$') { Fail "'$v' is not a version like 1.4.151" }
}
if ([version]$NewVersion -le [version]$OldVersion) { Fail "the new version $NewVersion is not newer than $OldVersion" }

foreach ($f in @($OldMsi, $NewMsi)) {
    if (-not (Test-Path -LiteralPath $f -PathType Leaf)) { Fail "no such file: $f" }
}
$OldMsi = (Resolve-Path -LiteralPath $OldMsi).Path
$NewMsi = (Resolve-Path -LiteralPath $NewMsi).Path
Pass "both packages are there"

# 1. The packages are the releases' own.
if ($SumsFile -ne '') {
    if (-not (Test-Path -LiteralPath $SumsFile -PathType Leaf)) { Fail "no such file: $SumsFile" }
    $sums = @{}
    foreach ($line in Get-Content -LiteralPath $SumsFile) {
        if ($line -match '^([0-9a-fA-F]{64})\s+\*?(.+)$') { $sums[$Matches[2].Trim()] = $Matches[1].ToLower() }
    }
    foreach ($f in @($OldMsi, $NewMsi)) {
        $name = Split-Path -Leaf $f
        if (-not $sums.ContainsKey($name)) { Fail "$name is not in $SumsFile" }
        $got = (Get-FileHash -LiteralPath $f -Algorithm SHA256).Hash.ToLower()
        if ($got -ne $sums[$name]) { Fail "$name does not match $SumsFile" }
    }
    Pass "both packages match $SumsFile"
}

# 2. A throwaway machine, an administrator, no Coral installed.
if ($env:GITHUB_ACTIONS -ne 'true' -and -not $ThrowawayMachine) {
    Fail 'this installs and removes Coral: it runs on a GitHub Actions runner, or pass -ThrowawayMachine on a machine you can spoil'
}
$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Fail 'run as an administrator (a per-machine install without a consent prompt)'
}
$uninstallKeys = @('HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*', 'HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*')
$installed = Get-ItemProperty -Path $uninstallKeys -ErrorAction SilentlyContinue | Where-Object { $_.DisplayName -like 'Coral*' }
if ($installed) { Fail "Coral is installed on this machine ($($installed[0].DisplayName) $($installed[0].DisplayVersion)): this test never touches an existing install" }
Pass 'an administrator, on a throwaway machine, with no Coral installed'

# 3. The Go tests.
$root = Resolve-Path (Join-Path $PSScriptRoot '..\..')
$names = @('TestRealWindowsUpgradeFromThePreviousRelease', 'TestRealWindowsTamperedPackageIsRefused', 'TestRealWindowsStaleClickIsRefused', 'TestRealWindowsHealthFailureIsRolledBack')
if ($DryRun) {
    Write-Host "DRY RUN: would run in $root\coral-go: go test ./internal/updater/apply -run '$($names -join '|')' -count=1 -v"
    Write-Host "         with $OldMsi ($OldVersion) -> $NewMsi ($NewVersion)"
    exit 0
}
$env:CORAL_APPLY_OLD_MSI = $OldMsi
$env:CORAL_APPLY_NEW_MSI = $NewMsi
$env:CORAL_APPLY_OLD_VERSION = $OldVersion
$env:CORAL_APPLY_NEW_VERSION = $NewVersion
$env:CORAL_APPLY_WINDOWS_CI = '1'
if ($LogDir -ne '') {
    New-Item -ItemType Directory -Force -Path $LogDir | Out-Null
    $env:CORAL_APPLY_LOG_DIR = (Resolve-Path -LiteralPath $LogDir).Path
}
Push-Location (Join-Path $root 'coral-go')
try {
    $out = & go test ./internal/updater/apply -run ($names -join '|') -count=1 -v -timeout 30m 2>&1 | Tee-Object -Variable lines
    $code = $LASTEXITCODE
} finally {
    Pop-Location
    Remove-Item Env:CORAL_APPLY_OLD_MSI, Env:CORAL_APPLY_NEW_MSI, Env:CORAL_APPLY_OLD_VERSION, Env:CORAL_APPLY_NEW_VERSION, Env:CORAL_APPLY_WINDOWS_CI, Env:CORAL_APPLY_LOG_DIR -ErrorAction SilentlyContinue
}
$text = ($lines | Out-String)
if ($code -ne 0) { Fail "go test exited $code" }
# A skipped test proves nothing: every scenario must have PASSED, none skipped.
foreach ($n in $names) {
    if ($text -notmatch "--- PASS: $n ") { Fail "$n did not pass" }
}
if ($text -match '--- SKIP') { Fail 'a scenario was skipped' }
Pass 'old -> new, a tampered package refused, a stale click refused, a failed health check rolled back'

# 4. Nothing is left behind.
$left = Get-ItemProperty -Path $uninstallKeys -ErrorAction SilentlyContinue | Where-Object { $_.DisplayName -like 'Coral*' }
if ($left) { Fail "Coral is still installed after the test ($($left[0].DisplayName) $($left[0].DisplayVersion))" }
Pass 'the machine is as it was found'
