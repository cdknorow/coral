package main

// The apply's off switch, per platform (#5879). Mac and Linux are on (#6290, the operator's decision #5231, in the build that carries the pins): the updater installs only what the
// person asked for through the relaunch control, and nothing installs by itself. Windows is off. Windows's constant counts only once windowsApplyBuilt is true,
// set together with the real apply: that is the MSI major upgrade (#162, apply.RunMSI with apply.WindowsMSI, run by runApplyWindows), so it is
// true now, and Windows stays off by applyEnabledWindows alone until the coral-go-team turns it on.
const (
	applyEnabledDarwin  = true
	applyEnabledLinux   = true
	applyEnabledWindows = false

	windowsApplyBuilt = true
)

// applyOn is the switch of goos: its own constant, and for Windows also the proof that its apply exists. Any other OS has no apply.
func applyOn(goos string, darwin, linux, windows, windowsBuilt bool) bool {
	switch goos {
	case "darwin":
		return darwin
	case "linux":
		return linux
	case "windows":
		return windows && windowsBuilt
	}
	return false
}
