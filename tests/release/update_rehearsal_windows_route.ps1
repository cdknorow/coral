<#
.SYNOPSIS
  The Windows update rehearsal, stage 2 (#6290): the whole click, through the real server, on a throwaway GitHub-hosted runner. A person's "Install update"
  (POST /api/system/update/install with a signed-in dashboard session) hands Coral to the updater, which verifies the staged MSI again and installs it through
  the real Windows Installer, starts the new Coral and checks it; and every way the click must NOT start an install is shown to start none.

.DESCRIPTION
  Two releases are built from THIS source, N = 1.4.900 and N+1 = 1.4.901, with the "dev" tag (a server with no EULA). Version N installs per machine and carries the
  rehearsal's own updater (tag windowsrehearsal, internal/updater/testrig/cmd/rehearsal-update-windows) AS coral-update.exe: it is the real server's
  preflight, launcher and click marker, then the real stage.Check and apply.RunMSI, in a fake world (a fake Sigstore, the rig's pins, a fake GitHub on loopback).
  The real coral-update cannot be pointed at a fake world; this one is never released (internal/updater/testrig's census). Nothing is uploaded but redacted logs.

  What a runner cannot show: UAC (it is an elevated administrator with the prompt off), so a person's consent or refusal at the prompt is not rehearsed here.

.PARAMETER Work
  A folder for the build and the data (default: $env:RUNNER_TEMP\rehearsal-route). Removed at the end.
.PARAMETER LogDir
  Where each scenario's result and logs are kept (the workflow uploads only these, after redaction).
#>
[CmdletBinding()]
param(
    [string]$Work = '',
    [string]$LogDir = ''
)
$ErrorActionPreference = 'Stop'
function Fail([string]$msg) { Write-Host "FAIL: $msg" -ForegroundColor Red; exit 1 }
function Pass([string]$msg) { Write-Host "PASS: $msg" -ForegroundColor Green }

$hostedRunner = ($env:GITHUB_ACTIONS -eq 'true' -and $env:RUNNER_ENVIRONMENT -eq 'github-hosted')
if (-not $hostedRunner) { Fail 'this installs and removes Coral: it runs on a GitHub-hosted runner only' }
$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { Fail 'run as an administrator' }
$uninstallKeys = @('HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*', 'HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*', 'HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*')
function Get-Installed { Get-ItemProperty -Path $uninstallKeys -ErrorAction SilentlyContinue | Where-Object { $_.DisplayName -like 'Coral*' } }
if (Get-Installed) { Fail 'Coral is installed on this machine: this test never touches an existing install' }

$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
if ($Work -eq '') { $Work = Join-Path $(if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { $env:TEMP }) 'rehearsal-route' }
if (Test-Path -LiteralPath $Work) { Remove-Item -Recurse -Force -LiteralPath $Work }
New-Item -ItemType Directory -Force -Path $Work | Out-Null
if ($LogDir -ne '') { New-Item -ItemType Directory -Force -Path $LogDir | Out-Null; $LogDir = (Resolve-Path -LiteralPath $LogDir).Path }
$N = '1.4.900'; $N1 = '1.4.901'
$port = 8461; $base = "http://127.0.0.1:$port"
$installDir = Join-Path $env:ProgramFiles 'Coral'
$data = Join-Path $Work 'data'
. (Join-Path $PSScriptRoot 'rehearsal_build.ps1')

function Stop-Coral {
    Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.Path -and ($_.Path -like "$installDir*" -or $_.Path -like "$Work*") } | Stop-Process -Force -ErrorAction SilentlyContinue
    Get-Process ping -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 2
}
function Remove-Coral {
    Stop-Coral
    foreach ($p in @(Get-Installed)) {
        $code = $p.PSChildName
        if ($code -match '^\{[0-9A-Fa-f-]{36}\}$') {
            $pr = Start-Process msiexec.exe -ArgumentList '/x', $code, '/qn', '/norestart' -Wait -PassThru
            if ($pr.ExitCode -ne 0 -and $pr.ExitCode -ne 3010) { Fail "msiexec /x $code exited $($pr.ExitCode)" }
        }
    }
    if (Get-Installed) { Fail 'Coral is still installed after the removal' }
}
function Install-Msi([string]$msi, [string]$name) {
    $log = Join-Path $(if ($LogDir) { $LogDir } else { $Work }) "msiexec-$name.log"
    $pr = Start-Process msiexec.exe -ArgumentList '/i', "`"$msi`"", '/qn', '/norestart', '/l*v', "`"$log`"" -Wait -PassThru
    if ($pr.ExitCode -ne 0 -and $pr.ExitCode -ne 3010) { Fail "msiexec /i $name exited $($pr.ExitCode)" }
}
function Installed-Version { $i = Get-Installed; if ($i) { @($i)[0].DisplayVersion } else { '' } }

# The environment the updater and the server take: the data folder only (the server and the updater read nothing else of ours).
$env:CORAL_DATA_DIR = $data
$env:CORAL_TELEMETRY_DISABLED = '1'

function Wait-Health([int]$seconds = 90) {
    $end = (Get-Date).AddSeconds($seconds)
    while ((Get-Date) -lt $end) {
        try { if ((Invoke-WebRequest -Uri "$base/api/health" -UseBasicParsing -TimeoutSec 3).StatusCode -eq 200) { return $true } } catch { }
        Start-Sleep -Seconds 2
    }
    return $false
}
function Write-Plan([hashtable]$extra) {
    $plan = @{ installed_msi = $script:oldMsi; new_msi = $script:newMsi; new_version = $N1; server_args = @('--port', "$port", '--host', '127.0.0.1') }
    foreach ($k in $extra.Keys) { $plan[$k] = $extra[$k] }
    $plan | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $data 'rehearsal.json') -Encoding ascii
}
function Read-Result([string]$name) {
    $f = Join-Path $data 'rehearsal-result.json'
    if (-not (Test-Path -LiteralPath $f)) { return $null }
    $r = Get-Content -Raw -LiteralPath $f | ConvertFrom-Json
    if ($LogDir) { Copy-Item -LiteralPath $f (Join-Path $LogDir "result-$name.json") -Force }
    return $r
}
function Keep-Logs([string]$name) {
    if (-not $LogDir) { return }
    $sc = Join-Path $Work 'server-child.log'
    if (Test-Path -LiteralPath $sc) { Copy-Item -LiteralPath $sc (Join-Path $LogDir "$name-server-child.log") -Force }
    foreach ($p in @('tray.log', 'updates\*.log')) {
        Get-ChildItem -Path (Join-Path $data $p) -ErrorAction SilentlyContinue | ForEach-Object { Copy-Item -LiteralPath $_.FullName (Join-Path $LogDir "$name-$($_.Name)") -Force }
    }
}
# A fresh machine state for a scenario: N installed, its server up, a person signed in, N+1 staged.
function New-Scenario([string]$name, [hashtable]$plan, [string]$from = $installDir) {
    Remove-Coral
    if (Test-Path -LiteralPath $data) { Remove-Item -Recurse -Force -LiteralPath $data }
    New-Item -ItemType Directory -Force -Path $data | Out-Null
    Install-Msi $script:oldMsi "$name-install"
    if ((Installed-Version) -ne $N) { Fail "$name`: version $N was not installed" }
    # Coral will not start in a data folder other users can use (a runner's temp folder is one): owner-only, as Coral itself makes its own
    icacls $data /inheritance:r /grant:r "$($env:USERNAME):(OI)(CI)F" | Out-Null
    if ($LASTEXITCODE -ne 0) { Fail "$name`: icacls on the data folder failed" }
    Write-Plan $plan
    & (Join-Path $installDir 'coral-update.exe') serve $installDir
    if ($LASTEXITCODE -ne 0) { Fail "$name`: the server was not started" }
    if (-not (Wait-Health)) {
        Keep-Logs $name
        $sl = Join-Path $Work 'server-child.log'
        if (Test-Path -LiteralPath $sl) { Get-Content -LiteralPath $sl -Tail 30 | ForEach-Object { Write-Host "server: $_" } } else { Write-Host 'server: no server-child.log' }
        Get-Process | Where-Object { $_.Path -like "$installDir*" } | ForEach-Object { Write-Host "running: $($_.Name)" }
        Fail "$name`: the server did not come up"
    }
    & (Join-Path $installDir 'coral-update.exe') check
    $r = Read-Result "$name-check"
    if (-not $r -or $r.result -ne 'staged') { Fail "$name`: the rig's release was not staged ($($r | ConvertTo-Json -Compress))" }
    Remove-Item -LiteralPath (Join-Path $data 'rehearsal-result.json') -Force
    # a person: the sign-in link `coral open` prints, used once, gives the session cookie
    $out = & (Join-Path $installDir 'coral.exe') open --print --url $base --home $data 2>&1 | Out-String
    if ($out -notmatch '(http\S*login_token=\S+)') { Fail "$name`: no sign-in link" }
    $link = $Matches[1]
    $lr = Invoke-WebRequest -Uri $link -SessionVariable sv -SkipHttpErrorCheck -UseBasicParsing # the redirect after the one use of the link lands on the dashboard, with the cookie set
    $script:session = $sv
    $names = @($sv.Cookies.GetCookies($base) | ForEach-Object { $_.Name })
    Write-Host "sign-in: status $($lr.StatusCode), $($names.Count) cookie(s): $($names -join ',')"
    if ($names.Count -eq 0) { Fail "$name`: the sign-in link gave no session cookie (link host: $(([uri]$link).Host))" }
}
function Click([hashtable]$extraHeaders = @{}, [string]$body = '', $websession = $script:session) {
    $h = @{ Origin = $base; 'Sec-Fetch-Site' = 'same-origin' }
    foreach ($k in $extraHeaders.Keys) { $h[$k] = $extraHeaders[$k] }
    $args2 = @{ Uri = "$base/api/system/update/install"; Method = 'POST'; Headers = $h; SkipHttpErrorCheck = $true; UseBasicParsing = $true }
    if ($websession) { $args2.WebSession = $websession }
    if ($body -ne '') { $args2.Body = $body; $args2.ContentType = 'application/json' }
    $resp = Invoke-WebRequest @args2
    # -Headers are kept in the web session for its later calls: a header of this call (an agent's token) must not stay on the person's session
    if ($websession) { foreach ($k in $h.Keys) { $null = $websession.Headers.Remove($k) } }
    return $resp
}
function Wait-Result([int]$minutes = 8) {
    $end = (Get-Date).AddMinutes($minutes)
    while ((Get-Date) -lt $end) {
        if (Test-Path -LiteralPath (Join-Path $data 'rehearsal-result.json')) { Start-Sleep -Seconds 1; return }
        Start-Sleep -Seconds 3
    }
    Fail 'the updater wrote no result'
}

try {
    Push-Location (Join-Path $root 'coral-go'); try { go mod download; if ($LASTEXITCODE -ne 0) { throw 'go mod download failed' } } finally { Pop-Location }
    dotnet tool install --global wix --version 5.0.2 | Out-Null
    wix extension add WixToolset.UI.wixext/5.0.2 | Out-Null
    Build-RehearsalRelease $root $Work $N 'dev' -standIn | Out-Host; $script:oldMsi = $script:builtMsi
    Build-RehearsalRelease $root $Work $N1 'dev' | Out-Host; $script:newMsi = $script:builtMsi
    Pass "built $N (with the rehearsal's updater) and $N1 from this source (not uploaded)"

    # Row 2: every way a click must NOT start an install starts none.
    New-Scenario 'refusals' @{}
    $r = Click -websession $null
    if ($r.StatusCode -ne 401) { Fail "no session cookie: expected 401, got $($r.StatusCode)" }
    $r = Click @{ 'X-Coral-Session-Token' = 'not-a-session' }
    if (($r.StatusCode -ne 401 -or $r.Content -notmatch 'session_token_required') -and ($r.StatusCode -ne 403 -or $r.Content -notmatch 'operator_only')) { Fail "an agent's session token: expected 401 session_token_required or 403 operator_only, got $($r.StatusCode) $($r.Content)" }
    $r = Click @{} '{"version":"1.4.999"}'
    if ($r.StatusCode -ne 400) { Fail "a body naming a version: expected 400, got $($r.StatusCode) $($r.Content)" }
    & (Join-Path $installDir 'coral-update.exe') apply # by hand, no click: it answers and asks nothing
    $res = Read-Result 'refusals-by-hand'
    if (-not $res -or $res.result -ne 'not_requested') { Fail "apply by hand: expected not_requested, got $($res | ConvertTo-Json -Compress)" }
    if ((Installed-Version) -ne $N) { Fail "a refused click changed the install to $(Installed-Version)" }
    if (-not (Wait-Health 10)) { Fail 'a refused click stopped the server' }
    Pass 'no cookie 401; an agent token refused; a body naming a version 400; apply by hand not_requested; nothing installed, the server kept running'

    # Row 8: not a registered install. (A copy run BESIDE a registered product is judged by the registered product's folder, not its own: run 39938757641's
    # finding, reported to Backend; so the copy here runs with no registered product at all.)
    New-Scenario 'unregistered' @{}
    Stop-Coral
    $portable = Join-Path $Work 'portable'
    Copy-Item -Recurse -LiteralPath $installDir -Destination $portable
    Remove-Coral
    & (Join-Path $portable 'coral-update.exe') serve $portable
    if (-not (Wait-Health)) { Fail 'the copied Coral did not come up' }
    & (Join-Path $portable 'coral-update.exe') check
    $out = & (Join-Path $portable 'coral.exe') open --print --url $base --home $data 2>&1 | Out-String
    if ($out -notmatch '(http\S*login_token=\S+)') { Fail 'no sign-in link for the copy' }
    $null = Invoke-WebRequest -Uri $Matches[1] -SessionVariable sv2 -SkipHttpErrorCheck -UseBasicParsing # the redirect after the one use of the link lands on the dashboard, with the cookie set
    $r = Click -websession $sv2
    if ($r.StatusCode -ne 409 -or $r.Content -notmatch 'install_not_supported') { Fail "a copy that is no registered install: expected 409 install_not_supported, got $($r.StatusCode) $($r.Content)" }
    if (-not (Wait-Health 10)) { Fail 'the refusal stopped the server' }
    Pass 'a Coral with no registered per-machine install: 409 install_not_supported, the server kept running'

    # Rows 1, 3, 4, 5: the click that installs, and the ones that must not install.
    $scenarios = @(
        @{ name = 'update';      plan = @{};                           result = 'relaunched';  version = $N1 },
        @{ name = 'tampered';    plan = @{ tamper = $true };           result = 'failed';      version = $N  },
        @{ name = 'stale-click'; plan = @{ stale_click = $true };      result = 'refused';     version = $N  },
        @{ name = 'unhealthy';   plan = @{ unhealthy = $true };        result = 'rolled_back'; version = $N  }
    )
    foreach ($s in $scenarios) {
        New-Scenario $s.name $s.plan
        $r = Click
        if ($r.StatusCode -ne 202) { Keep-Logs $s.name; Fail "$($s.name): the click answered $($r.StatusCode) $($r.Content)" }
        Wait-Result
        $res = Read-Result $s.name
        Keep-Logs $s.name
        if ($res.result -ne $s.result) { Fail "$($s.name): expected $($s.result), got $($res | ConvertTo-Json -Compress)" }
        if ((Installed-Version) -ne $s.version) { Fail "$($s.name): expected version $($s.version) installed, got $(Installed-Version)" }
        if (-not (Wait-Health 120)) { Fail "$($s.name): no Coral answered after the update" }
        Pass "$($s.name): the click gave '$($res.result)', version $($s.version) installed, a Coral running"
    }
} finally {
    Stop-Coral
    Remove-Coral
    Remove-Item Env:CORAL_DATA_DIR, Env:CORAL_TELEMETRY_DISABLED -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $Work) { Remove-Item -Recurse -Force -LiteralPath $Work -ErrorAction SilentlyContinue }
}
if (Get-Installed) { Fail 'Coral is still installed after the rehearsal' }
Pass 'the machine is as it was found'
