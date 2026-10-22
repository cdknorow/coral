//go:build !windows

package executil

import "os/exec"

// NoCwdSearchEnv is Windows' cmd.exe variable (nocwd_windows.go); the other platforms do not look in the current folder for a program, so the helpers change nothing there.
const NoCwdSearchEnv = "NoDefaultCurrentDirectoryInExePath=1"

// WithNoCwdSearch is env unchanged off Windows.
func WithNoCwdSearch(env []string) []string { return env }

// NoCwdSearch does nothing off Windows.
func NoCwdSearch(*exec.Cmd) {}
