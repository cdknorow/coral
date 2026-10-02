param([Parameter(Mandatory=$true)][string]$PackageDir)
$ErrorActionPreference = 'Stop'

$commands = @('coral', 'launch-coral', 'coral-board', 'coral-agent',
  'coral-hook-agentic-state', 'coral-hook-message-check',
  'coral-hook-session-start', 'coral-hook-task-sync', 'coral-tray', 'coral-app')
foreach ($cmd in $commands) {
  if (!(Test-Path (Join-Path $PackageDir "$cmd.exe"))) {
    throw "Standard package is missing $cmd.exe"
  }
}
if (Get-ChildItem $PackageDir -Recurse -File | Where-Object {
  $_.Name -match '^(?i:libcrypto.*|libssl.*|openssl-attestation\.env|THIRD_PARTY_NOTICES_SQLCIPHER\.md|OPENSSL-LICENSE\.txt|SQLCIPHER-LICENSE\.md)$'
}) {
  throw 'Standard package contains SQLCipher or OpenSSL files'
}

# PE import tables provide the binary-level proof that the standard package
# does not require a machine-installed OpenSSL DLL at launch.
foreach ($cmd in $commands) {
  $binary = Join-Path $PackageDir "$cmd.exe"
  $imports = (& objdump -p $binary) | Select-String 'DLL Name:'
  if ($LASTEXITCODE -ne 0) { throw "cannot inspect imports in $cmd.exe" }
  if ($imports -match '(?i)libcrypto|libssl|sqlcipher') {
    throw "Standard $cmd.exe imports OpenSSL or SQLCipher"
  }
}
Write-Host "Standard package dependency checks passed: $PackageDir"
