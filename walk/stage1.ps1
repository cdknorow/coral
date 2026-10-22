. "$PSScriptRoot\common.ps1"
$ver = $env:WALK_VERSION
$dl = Join-Path $env:RUNNER_TEMP 'dl'; New-Item -ItemType Directory -Force $dl | Out-Null
gh release download "v$ver" --repo $env:GITHUB_REPOSITORY --dir $dl --pattern "Coral-$ver-x64.msi" --pattern 'SHA256SUMS.txt'
$msi = Join-Path $dl "Coral-$ver-x64.msi"
$want = ((Get-Content (Join-Path $dl 'SHA256SUMS.txt') | Where-Object { $_ -match "Coral-$ver-x64.msi" }) -split '\s+')[0].ToLower()
$got = (Get-FileHash $msi -Algorithm SHA256).Hash.ToLower()
"msi sha256 ok: $($want -eq $got)  ($got)"
"=== desktop before"; Shot 'desktop-before'; TopWindows
"=== running as: " + [Security.Principal.WindowsIdentity]::GetCurrent().Name + " session " + (Get-Process -Id $PID).SessionId
$p = Start-Process msiexec -ArgumentList "/i `"$msi`" /l*v `"$env:RUNNER_TEMP\walk-msi.log`"" -PassThru
Start-Sleep -Seconds 8
"=== windows after msiexec start"; TopWindows
Shot 'msi-first-screen'
$w = FindWindow '*Coral*' 0 20
if ($w) { "setup window: $($w.Current.Name)"; Describe $w 80 } else { "no Coral window found" }
