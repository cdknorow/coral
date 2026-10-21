//go:build windows

package secfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// H5: a folder only an administrator can change (the system folder, as Program Files) has no
// writers; a folder of this user's (a portable Coral unpacked in the profile) has this user; a
// folder that grants Everyone write has Everyone.
func TestWriters(t *testing.T) {
	who, err := Writers(filepath.Join(os.Getenv("SystemRoot"), "System32"))
	require.NoError(t, err)
	assert.Empty(t, who, "System32: only administrators and the system can change it")

	mine := t.TempDir()
	who, err = Writers(mine)
	require.NoError(t, err)
	assert.Contains(t, who, "this user")

	open := t.TempDir()
	ps := `$d = Get-Item -LiteralPath $env:D; $a = $d.GetAccessControl('Access'); ` +
		`$r = New-Object Security.AccessControl.FileSystemAccessRule((New-Object Security.Principal.SecurityIdentifier('S-1-1-0')), 'Modify', 'ContainerInherit,ObjectInherit', 'None', 'Allow'); ` +
		`$a.AddAccessRule($r); $d.SetAccessControl($a)`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", ps)
	cmd.Env = append(os.Environ(), "D="+open)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	who, err = Writers(open)
	require.NoError(t, err)
	assert.Contains(t, who, "Everyone")

	_, err = Writers(filepath.Join(mine, "missing"))
	assert.Error(t, err)
}
