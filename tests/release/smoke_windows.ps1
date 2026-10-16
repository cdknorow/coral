<#
.SYNOPSIS
  Smoke test for the packaged Windows build (the portable ZIP).

.DESCRIPTION
  The release workflow builds the MSI and the portable ZIP but never starts
  them, so a startup break (the v1.4.67 strict-local-auth gap would have hit
  Windows the same way, unseen) ships undetected. This runs the SHIPPED
  binaries from the portable ZIP in an isolated data directory and checks the
  server starts under strict local auth and the command-line tools work:

    - unzip the portable ZIP into scratch;
    - start coral.exe with an isolated data dir and --cloud-url off;
    - GET /api/health is 200 (the one route open without a credential);
    - a request with no credential is 401 (strict local auth, on by default);
    - `coral api-key` prints the key and a keyed request is 200;
    - `coral-board` joins, posts and reads as a person (the command-line token
      the server writes to the data dir; no agent-session variables are set);
    - `coral setup-code` prints a code;
    - the server is stopped by its recorded PID.

  No network beyond loopback (--cloud-url off), no secrets, nothing published.
  Secrets (the API key, the session/command-line token, the setup code) are
  never printed, and the startup log is redacted before it is shown on failure.

.PARAMETER Zip
  Path to the Coral-<version>-x64-portable.zip to test.

.PARAMETER Port
  Loopback port to bind (default 8457, a non-default port so it never meets a
  real install on 8420).

  Exit code 0 on success, 1 on any failed check.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Zip,
    [int]$Port = 8457
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$script:failures = 0
function Pass([string]$m) { Write-Host "[PASS] $m" -ForegroundColor Green }
function Fail([string]$m) { Write-Host "[FAIL] $m" -ForegroundColor Red; $script:failures++ }
function Info([string]$m) { Write-Host "[INFO] $m" -ForegroundColor Yellow }

# Status code of a request without throwing on 4xx/5xx (PowerShell 7's -SkipHttpErrorCheck).
function Get-Status {
    param([string]$Url, [hashtable]$Headers = @{})
    try {
        $r = Invoke-WebRequest -Uri $Url -Headers $Headers -UseBasicParsing -TimeoutSec 10 -SkipHttpErrorCheck
        return [pscustomobject]@{ Code = [int]$r.StatusCode; Body = $r.Content }
    } catch {
        return [pscustomobject]@{ Code = 0; Body = '' }
    }
}

$base = "http://127.0.0.1:$Port"
$root = Join-Path ([System.IO.Path]::GetTempPath()) ("coral-win-smoke-" + [guid]::NewGuid().ToString('N').Substring(0, 10))
$extract = Join-Path $root 'pkg'
$dataDir = Join-Path $root 'data'
$logOut = Join-Path $root 'coral.out.log'
$logErr = Join-Path $root 'coral.err.log'
$proc = $null

Info "ZIP: $Zip"
Info "Port: $Port"
Info "Scratch: $root"

if (-not (Test-Path -LiteralPath $Zip)) {
    Fail "portable ZIP not found: $Zip"
    exit 1
}

try {
    New-Item -ItemType Directory -Force -Path $extract, $dataDir | Out-Null
    Expand-Archive -LiteralPath $Zip -DestinationPath $extract -Force
    $coral = Join-Path $extract 'coral.exe'
    $board = Join-Path $extract 'coral-board.exe'
    if (-not (Test-Path -LiteralPath $coral)) { Fail "coral.exe not in the ZIP"; exit 1 }
    if (-not (Test-Path -LiteralPath $board)) { Fail "coral-board.exe not in the ZIP"; exit 1 }
    Pass "portable ZIP unpacked (coral.exe and coral-board.exe present)"

    # Isolate the install: the data dir and, so the EULA marker (os.UserHomeDir()\.coral) lands in
    # scratch and the prod build doesn't block on the terms dialog, the home too. No agent-session
    # variables, so coral-board runs as a person. Nothing here touches a real ~\.coral.
    $env:USERPROFILE = $root
    $env:HOME = $root
    $env:CORAL_DATA_DIR = $dataDir
    $env:CORAL_DIR = $dataDir
    $env:CORAL_URL = $base
    $env:CORAL_TELEMETRY_DISABLED = '1'
    foreach ($v in 'CORAL_SESSION_NAME', 'CORAL_SESSION_ID', 'CORAL_SESSION_TOKEN', 'CORAL_SUBSCRIBER_ID', 'CORAL_CLOUD_URL') {
        Remove-Item "env:$v" -ErrorAction SilentlyContinue
    }
    New-Item -ItemType Directory -Force -Path (Join-Path $root '.coral') | Out-Null
    Set-Content -LiteralPath (Join-Path $root '.coral\.eula-accepted') -Value "accepted`n" -NoNewline

    # An empty stdin, so it is not a terminal: the first-launch "Enable remote access?" prompt is
    # skipped (it reads stdin and would otherwise block the server before it listens), as it is for
    # a detached service. The server then starts non-interactively.
    $stdin = Join-Path $root 'stdin.txt'
    New-Item -ItemType File -Force -Path $stdin | Out-Null

    # ── Start the shipped server ────────────────────────────────────────────
    $proc = Start-Process -FilePath $coral -PassThru -WindowStyle Hidden `
        -RedirectStandardInput $stdin -RedirectStandardOutput $logOut -RedirectStandardError $logErr `
        -ArgumentList '--home', $dataDir, '--host', '127.0.0.1', '--port', "$Port", '--no-browser', '--cloud-url', 'off'
    Info "coral.exe PID: $($proc.Id)"

    # ── /api/health, the one open route, means the server is up ──────────────
    $up = $false
    for ($i = 0; $i -lt 90; $i++) {
        if ($proc.HasExited) { Fail "coral.exe exited early (code $($proc.ExitCode))"; break }
        if ((Get-Status "$base/api/health").Code -eq 200) { $up = $true; break }
        Start-Sleep -Milliseconds 500
    }
    if ($up) { Pass "GET /api/health is 200 (the server is up)" }
    else { Fail "GET /api/health never returned 200"; throw "server did not start" }

    # ── Strict local auth: no credential is refused ─────────────────────────
    $noCred = (Get-Status "$base/api/system/status").Code
    if ($noCred -eq 401) { Pass "GET /api/system/status with no credential is 401 (strict local auth)" }
    else { Fail "GET /api/system/status with no credential is $noCred (expected 401)" }

    # ── coral api-key, and a keyed request ──────────────────────────────────
    $key = (& $coral api-key --home $dataDir 2>$null | Select-Object -First 1)
    if ($key -and $key.Trim().Length -gt 0) { Pass "coral api-key printed the API key" }
    else { Fail "coral api-key printed no key" }
    if ($key) {
        $keyed = Get-Status "$base/api/system/status" @{ 'X-API-Key' = $key.Trim() }
        if ($keyed.Code -eq 200 -and $keyed.Body -match 'startup_complete') {
            Pass "a keyed GET /api/system/status is 200 and reports startup_complete"
        } else {
            Fail "a keyed GET /api/system/status is $($keyed.Code) (expected 200 with startup_complete)"
        }
    }

    # ── coral-board as a person (the command-line token from the data dir) ───
    $joined = (& $board join windows-smoke --as Tester 2>&1 | Out-String)
    if ($LASTEXITCODE -eq 0) { Pass "coral-board join (as a person, command-line token)" }
    else { Fail "coral-board join failed: $joined" }
    & $board post 'windows portable smoke message' 2>&1 | Out-Null
    if ($LASTEXITCODE -eq 0) { Pass "coral-board post" }
    else { Fail "coral-board post failed" }
    $read = (& $board read --id 1 2>&1 | Out-String)
    if ($read -match 'windows portable smoke message') { Pass "coral-board read returns the posted message" }
    else { Fail "coral-board read did not return the posted message" }

    # ── coral setup-code prints a code (never shown here) ────────────────────
    $sc = (& $coral setup-code --url $base --home $dataDir 2>$null | Out-String)
    if ($sc -match '[A-Za-z0-9]{4}-[A-Za-z0-9]{4}') { Pass "coral setup-code printed a setup code" }
    else { Fail "coral setup-code did not print a code" }

    # ── No crash signals in the startup log ─────────────────────────────────
    $crash = Select-String -Path $logOut, $logErr -Pattern 'panic:', 'fatal error:', 'runtime error:', 'Exception 0x' -ErrorAction SilentlyContinue
    if ($crash) { Fail "crash signal in the startup log" } else { Pass "no crash signals in the startup log" }
}
catch {
    Fail "smoke test error: $($_.Exception.Message)"
    # Show the startup logs, with any secret-bearing line dropped.
    foreach ($f in $logOut, $logErr, (Join-Path $dataDir 'coral.log')) {
        if (Test-Path -LiteralPath $f) {
            Write-Host "--- $(Split-Path -Leaf $f) ---"
            Get-Content -LiteralPath $f | Where-Object { $_ -notmatch '(?i)key|token|secret|password|code' } | Write-Host
        }
    }
}
finally {
    if ($proc -and -not $proc.HasExited) {
        Info "stopping coral.exe (PID $($proc.Id))"
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
        $proc.WaitForExit(10000) | Out-Null
    }
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

if ($script:failures -gt 0) {
    Write-Host "FAILED: $script:failures check(s)" -ForegroundColor Red
    exit 1
}
Write-Host "PASS: the packaged Windows build starts and the command-line tools work" -ForegroundColor Green
exit 0
