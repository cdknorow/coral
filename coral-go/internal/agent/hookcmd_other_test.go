//go:build !windows

package agent

import "os/exec"

// cmdExe: cmd.exe is Windows only.
func cmdExe(string) *exec.Cmd { return nil }
