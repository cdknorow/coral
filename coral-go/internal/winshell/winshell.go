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

// Pick is the PowerShell for a Windows agent, as an absolute path: CORAL_SHELL (coralShell)
// when it names pwsh or powershell and lookPath finds it, otherwise the first of PowerShells
// that lookPath finds, otherwise Windows PowerShell in the system folder
// (%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe) when it is there; "" when
// there is none. Never a bare name, which Windows would look up from the current folder (the
// agent's repository) before PATH (#26 G1A). Go's exec.LookPath refuses a result from the
// current folder (exec.ErrDot), so a decoy there is never picked.
func Pick(coralShell string, lookPath func(string) (string, error)) string {
	if IsPowerShell(coralShell) {
		if p, err := lookPath(coralShell); err == nil && absolute(p) {
			return p
		}
	}
	for _, ps := range PowerShells {
		if p, err := lookPath(ps); err == nil && absolute(p) {
			return p
		}
	}
	return systemPowerShell()
}

// systemPowerShell is Windows PowerShell in the system folder, or "" (tests replace it).
var systemPowerShell = func() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		return ""
	}
	p := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
		return p
	}
	return ""
}

// absolute reports whether p is an absolute path on a local drive (C:\x, C:/x), on any OS. A
// UNC path (\\server\share\pwsh.exe, from a PATH entry) is not: the shell is never a program
// on another machine (#26 G1A F2).
func absolute(p string) bool {
	return len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		(p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z')
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

// SetSystemPowerShellForTest replaces the system-folder fallback (a test with no PowerShell on
// a machine that has one) and returns the function that restores it.
func SetSystemPowerShellForTest(f func() string) (restore func()) {
	prev := systemPowerShell
	systemPowerShell = f
	return func() { systemPowerShell = prev }
}
