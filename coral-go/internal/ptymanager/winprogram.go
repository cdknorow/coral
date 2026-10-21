package ptymanager

import (
	"fmt"
	"strings"
)

// windowsProgram is name as the program at the start of a CreateProcessW command line: in
// double quotes when it has a space or tab, so C:\Program Files\...\pwsh.exe is never read as
// C:\Program.exe plus arguments (a planted prefix program, #26 G1A F1). A name with a double
// quote can't be quoted that way and is refused (Windows file names can't hold one).
func windowsProgram(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "\"\x00") {
		return "", fmt.Errorf("can't start %q: not a program path", name)
	}
	if strings.ContainsAny(name, " \t") {
		return `"` + name + `"`, nil
	}
	return name, nil
}
