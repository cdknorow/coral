param([Parameter(Mandatory=$true)][string]$PackageDir)
$ErrorActionPreference = 'Stop'

# The shipped programs: commands.txt and gui_commands.txt beside this script (#3855).
$commands = @(foreach ($f in 'commands.txt', 'gui_commands.txt') {
  Get-Content (Join-Path $PSScriptRoot $f) | Where-Object { $_ -notmatch '^\s*(#|$)' } | ForEach-Object { $_.Trim() }
})
if ($commands.Count -lt 2) { throw "commands.txt and gui_commands.txt list no programs" }
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
# The dashboard ships minified (#6208): the server binary carries none of the source's comments (strip_marker.txt is a comment line of a static file; a Go test keeps it in the source).
$marker = (Get-Content (Join-Path $PSScriptRoot 'strip_marker.txt') -TotalCount 1).Trim()
if (-not $marker) { throw 'strip_marker.txt is empty' }
# ISO-8859-1 by code page: [Text.Encoding]::Latin1 is $null on Windows PowerShell 5.1 (it exists only in .NET 5+), which would make this check throw instead of search
$bytes = [System.IO.File]::ReadAllBytes((Join-Path $PackageDir 'coral.exe'))
if ([System.Text.Encoding]::GetEncoding(28591).GetString($bytes).Contains($marker)) {
  throw "The server binary carries the dashboard's source comments: it was built without the strip-static overlay (#6208)"
}
Write-Host "Standard package dependency checks passed: $PackageDir"
