package main

// The apply's off switch, per platform (#5879; the operator turns Mac and Linux on separately, Windows later). All three are off. Turning
// Mac or Linux on is a one-line change of its constant below, reviewed as such. Windows has no apply yet (internal/updater/apply/pid_windows.go
// is a stub), so its constant counts only once windowsApplyBuilt is true, and that is set together with the real apply, never before.
const (
	applyEnabledDarwin  = false
	applyEnabledLinux   = false
	applyEnabledWindows = false

	windowsApplyBuilt = false
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
