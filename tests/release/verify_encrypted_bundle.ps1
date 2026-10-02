param([Parameter(Mandatory=$true)][string]$PackageDir)
$ErrorActionPreference = "Stop"
$exe = Join-Path $PackageDir "coral.exe"
$attestationPath = Join-Path $PackageDir "openssl-attestation.env"
if (!(Test-Path $attestationPath)) { throw "missing packaged OpenSSL attestation" }
$versionLine = Get-Content $attestationPath | Where-Object { $_ -match '^version=' } | Select-Object -First 1
if ($versionLine -match '^version=(\d+)\.\d+\.\d+$') { $cryptoMajor = $Matches[1] } else { throw "invalid packaged OpenSSL version" }
$version = $versionLine.Substring(8)
$dllName = "libcrypto-${cryptoMajor}-x64.dll"
$dll = Join-Path $PackageDir $dllName
if (!(Test-Path $exe) -or !(Test-Path $dll)) { throw "missing packaged Coral executable or OpenSSL DLL" }
if ((Get-Item $dll).VersionInfo.FileVersion -ne $version) { throw "packaged OpenSSL DLL version differs from attestation" }
$expected = @("coral.exe","launch-coral.exe","coral-board.exe","coral-agent.exe","coral-hook-agentic-state.exe","coral-hook-message-check.exe","coral-hook-session-start.exe","coral-hook-task-sync.exe","coral-tray.exe","coral-app.exe")
foreach ($name in $expected) { if (!(Test-Path (Join-Path $PackageDir $name))) { throw "missing packaged executable $name" } }
$dumpbin = Get-Command dumpbin.exe -ErrorAction SilentlyContinue
$objdump = Get-Command objdump.exe -ErrorAction SilentlyContinue
if (!$dumpbin -and !$objdump) { throw "no PE dependency inspector (dumpbin.exe or objdump.exe) is available" }
foreach ($name in $expected) {
  $path = Join-Path $PackageDir $name
  if ($dumpbin) {
    $deps = (& $dumpbin.Source /DEPENDENTS $path 2>&1 | Out-String)
  } else {
    $deps = (& $objdump.Source -p $path 2>&1 | Out-String)
  }
  if ($LASTEXITCODE -ne 0) { throw "dependency inspection failed for $name" }
  $cryptoImports = [regex]::Matches($deps, '(?i)libcrypto[-.][0-9]+[^\s]*\.dll')
  foreach ($import in $cryptoImports) {
    if ($import.Value -ine $dllName) { throw "$name imports unbundled OpenSSL DLL $($import.Value)" }
  }
}
$coralDeps = if ($dumpbin) { (& $dumpbin.Source /DEPENDENTS $exe 2>&1 | Out-String) } else { (& $objdump.Source -p $exe 2>&1 | Out-String) }
if ($coralDeps -notmatch [regex]::Escape($dllName)) {
  Write-Output "coral.exe dependency inspection output:"
  Write-Output $coralDeps
  throw "coral.exe does not declare the encrypted crypto runtime"
}
$home = Join-Path $env:TEMP ("coral-encryption-self-test-" + [guid]::NewGuid())
New-Item -ItemType Directory -Force -Path $home | Out-Null
$env:HOME = $home
& $exe --encryption-self-test
if ($LASTEXITCODE -ne 0) { throw "packaged encrypted self-test failed: $LASTEXITCODE" }
