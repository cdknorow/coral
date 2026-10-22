param([string]$Label)
. "$PSScriptRoot\common.ps1"
$msi = Join-Path (Resolve-Path 'dist\windows').Path "$Label.msi"
Add-Type -TypeDefinition @"
using System; using System.Runtime.InteropServices;
public static class Tok {
  [DllImport("advapi32.dll", SetLastError=true)] static extern bool OpenProcessToken(IntPtr p, uint a, out IntPtr t);
  [DllImport("advapi32.dll", SetLastError=true)] static extern bool GetTokenInformation(IntPtr t, int c, out int v, int n, out int r);
  [DllImport("kernel32.dll")] static extern IntPtr OpenProcess(uint a, bool i, int pid);
  [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr h);
  public static string Elevated(int pid) {
    IntPtr p = OpenProcess(0x1000, false, pid); if (p == IntPtr.Zero) return "n/a";
    IntPtr t; if (!OpenProcessToken(p, 0x8, out t)) { CloseHandle(p); return "n/a"; }
    int v, r; bool ok = GetTokenInformation(t, 20, out v, 4, out r); CloseHandle(t); CloseHandle(p);
    return ok ? (v != 0 ? "elevated" : "not elevated") : "n/a";
  }
}
"@
Get-Process | Where-Object { $_.ProcessName -like 'coral*' } | Stop-Process -Force -ErrorAction SilentlyContinue
$id = [Security.Principal.WindowsIdentity]::GetCurrent()
"===== $Label.msi; this walk runs as $($id.Name), session $((Get-Process -Id $PID).SessionId), elevated $(([Security.Principal.WindowsPrincipal]$id).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator))"
$log = Join-Path $env:RUNNER_TEMP "walk-$Label.log"
$p = Start-Process msiexec -ArgumentList "/i `"$msi`" /l*v `"$log`"" -PassThru
$w = FindWindow 'Coral Setup' 0 40
"first screen: $([bool]$w)"
ToggleCheck $w 'I accept*' $true | Out-Null; Start-Sleep -Milliseconds 700
ClickButton $w 'Install' | Out-Null
$w2 = $null
for ($i = 0; $i -lt 90; $i++) {
  Start-Sleep -Seconds 2
  $w2 = FindWindow 'Coral Setup' 0 1
  if ($w2 -and (FindControl $w2 'Button' 'Finish')) { "Finish dialog reached"; break }
  if ($p.HasExited) { "msiexec exited early: $($p.ExitCode)"; break }
}
ShotWin "finish-$Label" $w2
$box = FindControl $w2 'CheckBox' 'Launch*'
"Launch checkbox present: $([bool]$box); toggle state: $(if ($box) { $box.GetCurrentPattern([System.Windows.Automation.TogglePattern]::Pattern).Current.ToggleState })"
ToggleCheck $w2 'Launch*' $true | Out-Null
$t0 = Get-Date
ClickButton $w2 'Finish' | Out-Null
$null = $p.WaitForExit(90000)
"msiexec exit: $(if ($p.HasExited) { $p.ExitCode } else { 'still running' }); Finish took $([int]((Get-Date) - $t0).TotalSeconds) s"
Start-Sleep -Seconds 15
"--- coral processes after Finish"
Get-Process | Where-Object { $_.ProcessName -like 'coral*' } | ForEach-Object { "  {0} pid {1} {2}" -f $_.ProcessName, $_.Id, [Tok]::Elevated($_.Id) }
Shot "after-finish-$Label"
"--- the installer log (launch lines)"
Select-String -Path $log -Pattern 'LaunchCoral|1631|Return value 3|CustomAction' -ErrorAction SilentlyContinue | Select-Object -First 14 | ForEach-Object { "  " + $_.Line.Trim().Substring(0, [Math]::Min(190, $_.Line.Trim().Length)) }
"--- windows"
TopWindows | ForEach-Object { "  $_" }
Get-Process | Where-Object { $_.ProcessName -like 'coral*' } | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Process msiexec -ArgumentList "/x `"$msi`" /qn" -Wait
"uninstalled"
