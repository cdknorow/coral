//go:build windows

package board

import "os"

// platformCheckRunnerEnv: what a Windows program needs to run at all (SystemRoot, PATHEXT, a
// temp folder), and the folders go keeps its build cache and GOPATH under, set to the temp
// folder as HOME is. No secret is passed.
func platformCheckRunnerEnv() []string {
	t := os.TempDir()
	return []string{"SystemRoot=" + os.Getenv("SystemRoot"), "PATHEXT=" + os.Getenv("PATHEXT"),
		"TEMP=" + t, "TMP=" + t, "LOCALAPPDATA=" + t, "USERPROFILE=" + t}
}
