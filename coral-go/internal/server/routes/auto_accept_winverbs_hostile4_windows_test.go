//go:build windows

package routes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5690, on Windows: the fourth matrix through the whole of shellRisk (the POSIX reading, then winVerbRisk, then the POSIX reading with backslashes as slashes), with real paths, and the commands the
// POSIX reading catches that winVerbRisk alone does not.
func TestShellRiskOnWindowsFourthMatrix(t *testing.T) {
	// The folder as the server sees it: symlinks and 8.3 names resolved (t.TempDir can be C:\Users\RUNNER~1\...), with the folders and files the benign commands name.
	wd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	for _, d := range []string{"build", "src", "sub", "node_modules", "out"} {
		require.NoError(t, os.MkdirAll(filepath.Join(wd, d), 0o755))
	}
	for _, f := range []string{"a", "b"} {
		require.NoError(t, os.WriteFile(filepath.Join(wd, f), []byte("x"), 0o644))
	}
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
