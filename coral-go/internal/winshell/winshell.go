// Package winshell picks the PowerShell that runs a Windows agent: the terminal's shell
// (ptymanager's pickShell), the shell a launch command is wrapped in (shellWrap) and the one
// the launch line is written for (agent's quoting). One choice for all three, so a line
// escaped for Windows PowerShell 5.1 is only ever run by 5.1, and one written for pwsh only by
// pwsh (#29, Security's #20 F3).
package winshell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// PowerShells are the shells Coral runs Windows agents with, in order of preference.
var PowerShells = []string{"pwsh.exe", "powershell.exe"}

// IsPowerShell reports whether shell, a path or name, is pwsh or Windows PowerShell.
func IsPowerShell(shell string) bool {
	switch base(shell) {
	case "pwsh", "powershell":
		return true
	}
	return false
}

// LegacyNativeArgs reports whether shell passes arguments to native programs the way Windows
// PowerShell 5.1 does, escaping nothing, so a launch line must escape them itself
// (psquote.NativeArg): every shell but pwsh. pwsh 7.3 and later escape each argument for the
// program's argv parser ($PSNativeCommandArgumentPassing), so an argument escaped for 5.1 would
// arrive with the escapes in it; pwsh 7.2 and earlier, out of support, are not told apart.
func LegacyNativeArgs(shell string) bool {
	return base(shell) != "pwsh"
}

// Pick is the PowerShell for a Windows agent: CORAL_SHELL (coralShell) when it names pwsh or
// powershell, otherwise the first of PowerShells that lookPath finds; "" when there is none.
func Pick(coralShell string, lookPath func(string) (string, error)) string {
	if IsPowerShell(coralShell) {
		return coralShell
	}
	for _, ps := range PowerShells {
		if _, err := lookPath(ps); err == nil {
			return ps
		}
	}
	return ""
}

// PowerShell is Pick for this process's CORAL_SHELL and PATH. The answer is remembered for the
// same CORAL_SHELL and PATH: a launch asks several times, and on Windows each PATH miss is a
// stat in every PATH folder.
func PowerShell() string {
	coralShell := os.Getenv("CORAL_SHELL")
	key := coralShell + "\x00" + os.Getenv("PATH")
	cache.Lock()
	defer cache.Unlock()
	if !cache.ok || cache.key != key {
		cache.key, cache.shell, cache.ok = key, Pick(coralShell, exec.LookPath), true
	}
	return cache.shell
}

var cache struct {
	sync.Mutex
	key, shell string
	ok         bool
}

// base is a shell's name: its last path element (either slash), lower case, without .exe.
func base(shell string) string {
	b := strings.ToLower(filepath.Base(strings.ReplaceAll(shell, `\`, "/")))
	return strings.TrimSuffix(b, ".exe")
}
