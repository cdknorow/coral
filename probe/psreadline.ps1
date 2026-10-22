$ErrorActionPreference = 'Continue'
function Probe($shell, $label, [string[]]$flags) {
  $out = & $shell @flags -NoLogo -Command 'try { Import-Module PSReadLine -ErrorAction Stop; "loaded " + (Get-Module PSReadLine).Version } catch { "FAILED: " + $_.Exception.Message }' 2>&1 | Out-String
  Write-Host ("[{0}] {1} => {2}" -f $shell, $label, $out.Trim())
}
$pwsh = (Get-Command pwsh.exe).Source
$ps5 = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
foreach ($policy in 'Default','Restricted','AllSigned','RemoteSigned') {
  if ($policy -ne 'Default') { Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy $policy -Force; Set-ExecutionPolicy -Scope LocalMachine -ExecutionPolicy $policy -Force -ErrorAction SilentlyContinue }
  Write-Host "===== policy $policy (list: $((& $pwsh -NoProfile -Command 'Get-ExecutionPolicy -List | ForEach-Object { $_.Scope + "=" + $_.ExecutionPolicy }') -join ', '))"
  foreach ($sh in $pwsh, $ps5) {
    Probe $sh 'bare' @()
    Probe $sh '-NoProfile' @('-NoProfile')
    Probe $sh '-ExecutionPolicy Bypass' @('-ExecutionPolicy','Bypass')
    Probe $sh '-ExecutionPolicy RemoteSigned' @('-ExecutionPolicy','RemoteSigned')
  }
}
$m = Get-Module -ListAvailable PSReadLine | Select-Object Name,Version,Path
$m | Format-Table | Out-String | Write-Host
