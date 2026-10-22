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
    - the ZIP holds every program of commands.txt and gui_commands.txt, each a 64-bit
      Windows executable, and `coral.exe --version` prints the version (-Version);
    - with -StandIn (an executable that stands for the agent CLI: release.yml builds
      cmd/mock-agent), a launch starts it under the pty backend, it shows on the live
      list and its process runs;
    - with -Msi, the MSI is installed silently into scratch (INSTALLFOLDER), holds the
      programs, `coral.exe --version` runs from it, and it uninstalls (this changes the
      machine's PATH and writes the EULA marker, then removes them: for a CI runner);
    - the server is stopped (Ctrl+C first: the way a console Coral is stopped; killed only
      if that does not work, which is reported), its port closes and no agent process
      it started is left.

  No network beyond loopback (--cloud-url off), no secrets, nothing published.
  Secrets (the API key, the session/command-line token, the setup code) are
  never printed, and the startup log is redacted before it is shown on failure.

.PARAMETER Zip
  Path to the Coral-<version>-x64-portable.zip to test.

.PARAMETER Port
  Loopback port to bind (default 8457, a non-default port so it never meets a
  real install on 8420).

.PARAMETER Version
  The version coral.exe must print (no leading v). Without it the version is only
  checked to be non-empty.

.PARAMETER StandIn
  A stand-in agent CLI executable (set as cli_path_claude). Without it the launch and
  the agent part of the stop check are skipped (reported).

.PARAMETER Msi
  The Coral-<version>-x64.msi to install silently into scratch, check and uninstall.

  Exit code 0 on success, 1 on any failed check.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Zip,
    [int]$Port = 8457,
    [string]$Version = '',
    [string]$StandIn = '',
    [string]$Msi = ''
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

# A request with a JSON body, the same way (never throws).
function Send-Json {
    param([string]$Method, [string]$Url, [hashtable]$Headers, $Body)
    try {
        $r = Invoke-WebRequest -Uri $Url -Method $Method -Headers $Headers -Body ($Body | ConvertTo-Json -Compress) `
            -ContentType 'application/json' -UseBasicParsing -TimeoutSec 30 -SkipHttpErrorCheck
        return [pscustomobject]@{ Code = [int]$r.StatusCode; Body = $r.Content }
    } catch {
        return [pscustomobject]@{ Code = 0; Body = '' }
    }
}

# 'ok' when the file is a 64-bit (AMD64) Windows executable, else why not.
function Test-Pe64 {
    param([string]$Path)
    $fs = [System.IO.File]::OpenRead($Path)
    try {
        $h = New-Object byte[] 1024
        $n = $fs.Read($h, 0, 1024)
    } finally { $fs.Dispose() }
    if ($n -lt 64 -or $h[0] -ne 0x4D -or $h[1] -ne 0x5A) { return 'not a Windows executable' }
    $pe = [BitConverter]::ToInt32($h, 0x3C)
    if ($pe -lt 0 -or $pe + 6 -gt $n -or $h[$pe] -ne 0x50 -or $h[$pe + 1] -ne 0x45) { return 'no PE header' }
    $machine = [BitConverter]::ToUInt16($h, $pe + 4)
    if ($machine -ne 0x8664) { return ('machine 0x{0:X4}, not AMD64' -f $machine) }
    return 'ok'
}

# The programs the package must hold: commands.txt and gui_commands.txt, one per line (# and blanks ignored).
function Get-Programs {
    $names = @()
    foreach ($f in 'commands.txt', 'gui_commands.txt') {
        $names += Get-Content -LiteralPath (Join-Path $PSScriptRoot $f) | Where-Object { $_.Trim() -and -not $_.Trim().StartsWith('#') } | ForEach-Object { ($_.Trim()) + '.exe' }
    }
    return $names
}

# Every program is present and a 64-bit executable: '' when so, else what is wrong.
function Test-Programs {
    param([string]$Dir)
    $bad = @()
    foreach ($n in Get-Programs) {
        $f = Join-Path $Dir $n
        if (-not (Test-Path -LiteralPath $f)) { $bad += "$n (missing)"; continue }
        $r = Test-Pe64 $f
        if ($r -ne 'ok') { $bad += "$n ($r)" }
    }
    return ($bad -join ', ')
}

# Ctrl+C to a console program, as a user stops a console Coral: attach to its console from a child
# PowerShell (so this script keeps its own), ignore the event there, send it.
function Send-CtrlC {
    param([int]$ProcessId)
    $code = @'
Add-Type -Namespace CoralSmoke -Name Con -MemberDefinition '[DllImport("kernel32.dll")] public static extern bool FreeConsole(); [DllImport("kernel32.dll")] public static extern bool AttachConsole(uint pid); [DllImport("kernel32.dll")] public static extern bool SetConsoleCtrlHandler(System.IntPtr h, bool add); [DllImport("kernel32.dll")] public static extern bool GenerateConsoleCtrlEvent(uint ev, uint grp);'
[void][CoralSmoke.Con]::FreeConsole()
if (-not [CoralSmoke.Con]::AttachConsole(__PID__)) { exit 3 }
[void][CoralSmoke.Con]::SetConsoleCtrlHandler([System.IntPtr]::Zero, $true)
[void][CoralSmoke.Con]::GenerateConsoleCtrlEvent(0, 0)
Start-Sleep -Seconds 1
'@.Replace('__PID__', "$ProcessId")
    $exe = (Get-Process -Id $PID).Path
    $enc = [Convert]::ToBase64String([System.Text.Encoding]::Unicode.GetBytes($code)) # an encoded command: Start-Process does not quote its arguments
    $p = Start-Process -FilePath $exe -ArgumentList '-NoProfile', '-EncodedCommand', $enc -PassThru -Wait -WindowStyle Hidden
    return $p.ExitCode
}

# The processes of the agent a launch started: the stand-in and the shell that runs it, by the session id on their command lines.
function Get-AgentProcesses {
    param([string]$SessionId)
    return @(Get-CimInstance Win32_Process -Filter "CommandLine LIKE '%$SessionId%'" -ErrorAction SilentlyContinue |
        Where-Object { $_.ProcessId -ne $PID })
}

$base = "http://127.0.0.1:$Port"
$root = Join-Path ([System.IO.Path]::GetTempPath()) ("coral-win-smoke-" + [guid]::NewGuid().ToString('N').Substring(0, 10))
$extract = Join-Path $root 'pkg'
$dataDir = Join-Path $root 'data'
$logOut = Join-Path $root 'coral.out.log'
$logErr = Join-Path $root 'coral.err.log'
$proc = $null
$agentPids = @()

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
    $badProgs = Test-Programs $extract
    if (-not $badProgs) { Pass "the ZIP holds all $((Get-Programs).Count) programs, each a 64-bit Windows executable" }
    else { Fail "programs: $badProgs" }

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

    # The updater program ships and is inert: its status says the apply is off (applyEnabled is a compile-time false until a platform passes its upgrade tests).
    $upd = Join-Path $extract 'coral-update.exe'
    if (Test-Path -LiteralPath $upd) {
        $st = (& $upd status --json 2>&1 | Out-String)
        if ($st -match '"apply_enabled":\s*false') { Pass "coral-update status says apply_enabled false (the updater ships inert)" }
        else { Fail "coral-update status: $st" }
    } else { Fail "coral-update.exe not in the ZIP" }
    New-Item -ItemType Directory -Force -Path (Join-Path $root '.coral') | Out-Null
    Set-Content -LiteralPath (Join-Path $root '.coral\.eula-accepted') -Value "accepted`n" -NoNewline

    # ── version ─────────────────────────────────────────────────────────────
    $ver = ((& $coral --version 2>&1 | Out-String).Trim() -split "`r?`n" | Select-Object -First 1)
    if ($Version) {
        if ($ver -and $ver.Contains($Version)) { Pass "coral --version: $ver" }
        else { Fail "coral --version printed [$ver], not $Version" }
    } elseif ($ver) { Pass "coral --version: $ver (no -Version given to compare)" }
    else { Fail "coral --version printed nothing" }

    # ── the MSI: a silent install into scratch, the programs, a version, the uninstall ──
    if ($Msi) {
        # The package's platform is the template in its summary information: "x64;..." for a 64-bit package, "Intel;..." for a 32-bit one, which installs into
        # Program Files (x86) (the walk of v1.4.155, #6004's W1). The programs are 64-bit, so the package is too.
        $msiText = [Text.Encoding]::GetEncoding(28591).GetString([IO.File]::ReadAllBytes($Msi))
        if ($msiText.Contains('Intel;') -and -not $msiText.Contains('x64;')) { Fail "the MSI is a 32-bit package (template Intel): it would install into Program Files (x86); build it with wix build -arch x64" }
        elseif ($msiText.Contains('x64;')) { Pass "the MSI is an x64 package (it installs into Program Files)" }
        else { Fail "the MSI's platform could not be read from its summary information" }
        $msiDir = Join-Path $root 'msi'
        $msiLog = Join-Path $root 'msi-install.log'
        $inst = Start-Process -FilePath 'msiexec.exe' -Wait -PassThru -ArgumentList '/i', "`"$Msi`"", '/qn', '/norestart', "INSTALLFOLDER=`"$msiDir`"", '/l*v', "`"$msiLog`""
        if ($inst.ExitCode -ne 0) { Fail "msiexec /i exited $($inst.ExitCode) (log: msi-install.log)" }
        else {
            $badMsi = Test-Programs $msiDir
            if (-not $badMsi) { Pass "the MSI installed into scratch with all its programs, each a 64-bit Windows executable" }
            else { Fail "the MSI's programs: $badMsi" }
            $mv = ((& (Join-Path $msiDir 'coral.exe') --version 2>&1 | Out-String).Trim() -split "`r?`n" | Select-Object -First 1)
            if ($mv -and (-not $Version -or $mv.Contains($Version))) { Pass "the installed coral.exe --version: $mv" }
            else { Fail "the installed coral.exe --version printed [$mv]" }
            $un = Start-Process -FilePath 'msiexec.exe' -Wait -PassThru -ArgumentList '/x', "`"$Msi`"", '/qn', '/norestart'
            if ($un.ExitCode -eq 0 -and -not (Test-Path -LiteralPath (Join-Path $msiDir 'coral.exe'))) { Pass "the MSI uninstalled (its coral.exe is gone)" }
            else { Fail "msiexec /x exited $($un.ExitCode); coral.exe present: $(Test-Path -LiteralPath (Join-Path $msiDir 'coral.exe'))" }
        }
    } else { Info "no -Msi: the MSI is not checked" }

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

    # ── launch: the stand-in CLI under the pty backend ──────────────────────
    if ($StandIn -and $key) {
        $h = @{ 'X-API-Key' = $key.Trim() }
        $wd = Join-Path $root 'wd'
        New-Item -ItemType Directory -Force -Path $wd | Out-Null
        $set = Send-Json 'PUT' "$base/api/settings" $h @{ cli_path_claude = (Resolve-Path -LiteralPath $StandIn).Path }
        $launch = Send-Json 'POST' "$base/api/sessions/launch" $h @{ working_dir = $wd; display_name = 'smoke' }
        $sid = ''
        try { $sid = [string](($launch.Body | ConvertFrom-Json).session_id) } catch { }
        if (-not $sid) { Fail "launch: settings PUT $($set.Code), launch $($launch.Code), no session_id" }
        else {
            $listed = $false
            for ($i = 0; $i -lt 40 -and -not $listed; $i++) {
                $live = Get-Status "$base/api/sessions/live" $h
                # the raw body: PowerShell's ConvertFrom-Json refuses the live list (it did on the Windows runner)
                $listed = $live.Code -eq 200 -and $live.Body -match ('"session_id"\s*:\s*"' + [regex]::Escape($sid) + '"')
                if (-not $listed) { Start-Sleep -Milliseconds 500 }
            }
            $standName = [System.IO.Path]::GetFileName($StandIn)
            $running = $false
            for ($i = 0; $i -lt 40 -and -not $running; $i++) {
                $agentPids = @(Get-AgentProcesses $sid | ForEach-Object { $_.ProcessId })
                $running = [bool](Get-AgentProcesses $sid | Where-Object { $_.Name -ieq $standName })
                if (-not $running) { Start-Sleep -Milliseconds 500 }
            }
            if ($listed -and $running) { Pass "launch: the stand-in CLI started under pty, is on the live list, and its process runs ($($agentPids.Count) process(es) carry the session id)" }
            else {
                $shape = "body " + $live.Body.Length + " bytes"
                Fail "launch: on the live list: $listed (last list: HTTP $($live.Code), $shape; launched $sid); $standName running: $running"
            }
        }
    } else { Info "no -StandIn (or no key): the launch is not checked" }

    # ── No crash signals in the startup log ─────────────────────────────────
    $crash = Select-String -Path $logOut, $logErr -Pattern 'panic:', 'fatal error:', 'runtime error:', 'Exception 0x' -ErrorAction SilentlyContinue
    if ($crash) { Fail "crash signal in the startup log" } else { Pass "no crash signals in the startup log" }

    # ── stop: Ctrl+C, the port closes, no agent process is left ──────────────
    $sent = Send-CtrlC $proc.Id
    $exited = $proc.WaitForExit(20000)
    if ($exited) { Pass "Ctrl+C stopped the server within 20 s (exit code $($proc.ExitCode))" }
    else { Fail "the server was still running 20 s after Ctrl+C (the console event was $(if ($sent -eq 0) { 'sent' } else { "not sent: exit $sent" })); it is killed below" }
    $portCode = (Get-Status "$base/api/health").Code
    if ($exited -and $portCode -eq 0) { Pass "port $Port is closed" }
    elseif ($exited) { Fail "port $Port still answers ($portCode)" }
    if ($agentPids.Count -gt 0) {
        $left = @()
        for ($i = 0; $i -lt 50; $i++) {
            $left = @($agentPids | Where-Object { Get-Process -Id $_ -ErrorAction SilentlyContinue })
            if ($left.Count -eq 0) { break }
            Start-Sleep -Milliseconds 200
        }
        if ($left.Count -eq 0) { Pass "no agent process is left 10 s after the server exited" }
        else { Fail "agent process(es) still running 10 s after the server exited: $($left -join ', ')" }
    }
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
    foreach ($a in $agentPids) { Stop-Process -Id $a -Force -ErrorAction SilentlyContinue } # a stand-in the stop check found still running
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

if ($script:failures -gt 0) {
    Write-Host "FAILED: $script:failures check(s)" -ForegroundColor Red
    exit 1
}
Write-Host "PASS: the packaged Windows build starts and the command-line tools work" -ForegroundColor Green
exit 0
