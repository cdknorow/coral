package main

// The apply's off switch, per platform (#5879; the operator turns Mac and Linux on separately, Windows later). All three are off. Turning
// Mac or Linux on is a one-line change of its constant below, reviewed as such. Windows's constant counts only once windowsApplyBuilt is true,
// set together with the real apply: that is the MSI major upgrade (#162, apply.RunMSI with apply.WindowsMSI, run by runApplyWindows), so it is
// true now, and Windows stays off by applyEnabledWindows alone until the coral-go-team turns it on.
const (
	applyEnabledDarwin  = false
	applyEnabledLinux   = false
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
