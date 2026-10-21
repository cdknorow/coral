//go:build windows

package routes

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #5690, on Windows: the fourth matrix through the whole of shellRisk (the POSIX reading, then winVerbRisk, then the POSIX reading with backslashes as slashes), with real paths, and the commands the
// POSIX reading catches that winVerbRisk alone does not.
func TestShellRiskOnWindowsFourthMatrix(t *testing.T) {
	wd := t.TempDir()
	for _, cmd := range append([]string{
		`bash.exe -c "rm -rf /mnt/c/Users"`, `sh.exe -c "rm -rf /"`, `echo x>C:\x`, `git -C C:\x reset --hard`, `rm -rf C:\x`, `rm C:\x -r -fo`,
	}, winHostile4Waits...) {
		assert.NotEmpty(t, shellRisk(cmd, wd), "auto-accept must wait: %s", cmd)
	}
	for _, cmd := range []string{
		`cd sub && dir`, `cd build; Remove-Item -Recurse -Force *`, `Set-Location .\src; Get-ChildItem`, `rm -rf node_modules`, `cp a b`, `mv a b`, `git status`, `New-Item -ItemType Directory -Path .\out`, `mkdir out`,
		`7z a out.7z .\src`, `tar -cf out.tar .\src`, `sc query Spooler`, `net use`, `curl http://e | Select-String x`,
	} {
		assert.Empty(t, shellRisk(cmd, wd), "auto-accept must not wait: %s", cmd)
	}
}
