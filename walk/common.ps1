# helpers for the first-time walk on a Windows runner (trial branch; not for landing)
$ErrorActionPreference = 'Continue'
Add-Type -AssemblyName System.Windows.Forms, System.Drawing, UIAutomationClient, UIAutomationTypes
$script:shotDir = Join-Path $env:GITHUB_WORKSPACE 'walk-out'
New-Item -ItemType Directory -Force -Path $script:shotDir | Out-Null
$script:n = 0
function Shot([string]$name) {
    $script:n++
    $f = Join-Path $script:shotDir ('{0:00}-{1}.png' -f $script:n, $name)
    try {
        $b = [System.Windows.Forms.SystemInformation]::VirtualScreen
        $bmp = New-Object System.Drawing.Bitmap $b.Width, $b.Height
        $g = [System.Drawing.Graphics]::FromImage($bmp)
        $g.CopyFromScreen($b.Left, $b.Top, 0, 0, $bmp.Size)
        $bmp.Save($f, [System.Drawing.Imaging.ImageFormat]::Png)
        $g.Dispose(); $bmp.Dispose()
        "shot: $f ($($b.Width)x$($b.Height))"
    } catch { "shot FAILED: $name $($_.Exception.Message)" }
}
function TopWindows {
    $root = [System.Windows.Automation.AutomationElement]::RootElement
    $all = $root.FindAll([System.Windows.Automation.TreeScope]::Children, [System.Windows.Automation.Condition]::TrueCondition)
    foreach ($w in $all) { try { "{0} | pid {1} | {2}" -f $w.Current.Name, $w.Current.ProcessId, $w.Current.ClassName } catch {} }
}
function Describe($el, [int]$max = 80) {
    $all = $el.FindAll([System.Windows.Automation.TreeScope]::Descendants, [System.Windows.Automation.Condition]::TrueCondition)
    $i = 0
    foreach ($c in $all) { if ($i++ -ge $max) { break }; try { "  [{0}] '{1}' ({2})" -f $c.Current.ControlType.ProgrammaticName.Replace('ControlType.', ''), $c.Current.Name, $c.Current.AutomationId } catch {} }
}
function FindWindow([string]$like, [int]$pidFilter = 0, [int]$timeout = 60) {
    $end = (Get-Date).AddSeconds($timeout)
    while ((Get-Date) -lt $end) {
        $root = [System.Windows.Automation.AutomationElement]::RootElement
        foreach ($w in $root.FindAll([System.Windows.Automation.TreeScope]::Children, [System.Windows.Automation.Condition]::TrueCondition)) {
            try { if ($w.Current.Name -like $like -and ($pidFilter -eq 0 -or $w.Current.ProcessId -eq $pidFilter)) { return $w } } catch {}
        }
        Start-Sleep -Milliseconds 700
    }
    return $null
}
function FindControl($win, [string]$type, [string]$nameLike) {
    $cond = New-Object System.Windows.Automation.PropertyCondition ([System.Windows.Automation.AutomationElement]::ControlTypeProperty), ([System.Windows.Automation.ControlType]::$type)
    foreach ($c in $win.FindAll([System.Windows.Automation.TreeScope]::Descendants, $cond)) { if ($c.Current.Name -like $nameLike) { return $c } }
    return $null
}
function ClickButton($win, [string]$nameLike) {
    $b = FindControl $win 'Button' $nameLike
    if (-not $b) { "button not found: $nameLike"; return $false }
    try { $b.GetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern).Invoke(); "clicked button: $($b.Current.Name)"; return $true } catch { "invoke failed: $($_.Exception.Message)"; return $false }
}
function ToggleCheck($win, [string]$nameLike, [bool]$on) {
    $c = FindControl $win 'CheckBox' $nameLike
    if (-not $c) { "checkbox not found: $nameLike"; return $false }
    $p = $c.GetCurrentPattern([System.Windows.Automation.TogglePattern]::Pattern)
    $want = if ($on) { [System.Windows.Automation.ToggleState]::On } else { [System.Windows.Automation.ToggleState]::Off }
    if ($p.Current.ToggleState -ne $want) { $p.Toggle() }
    "checkbox '$($c.Current.Name)' is $($p.Current.ToggleState)"; return $true
}

function ShotRect([string]$name, $rect, [int]$pad = 6) {
    $script:n++
    $f = Join-Path $script:shotDir ('{0:00}-{1}.png' -f $script:n, $name)
    try {
        $b = [System.Windows.Forms.SystemInformation]::VirtualScreen
        $x = [Math]::Max(0, [int]$rect.X - $pad); $y = [Math]::Max(0, [int]$rect.Y - $pad)
        $w = [Math]::Min($b.Width - $x, [int]$rect.Width + 2 * $pad); $h = [Math]::Min($b.Height - $y, [int]$rect.Height + 2 * $pad)
        $bmp = New-Object System.Drawing.Bitmap $w, $h
        $g = [System.Drawing.Graphics]::FromImage($bmp)
        $g.CopyFromScreen($x, $y, 0, 0, $bmp.Size)
        $bmp.Save($f, [System.Drawing.Imaging.ImageFormat]::Png)
        $g.Dispose(); $bmp.Dispose()
        "shot: $f (${w}x${h})"
    } catch { "shot FAILED: $name $($_.Exception.Message)" }
}
function ShotWin([string]$name, $win) { ShotRect $name $win.Current.BoundingRectangle }
Add-Type -TypeDefinition @"
using System; using System.Runtime.InteropServices;
public static class Mouse {
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint f, uint dx, uint dy, uint d, UIntPtr e);
  public static void Click(int x, int y, bool right) { SetCursorPos(x, y); System.Threading.Thread.Sleep(150);
    uint down = right ? 0x8u : 0x2u, up = right ? 0x10u : 0x4u; mouse_event(down, 0, 0, 0, UIntPtr.Zero); mouse_event(up, 0, 0, 0, UIntPtr.Zero); }
}
"@
