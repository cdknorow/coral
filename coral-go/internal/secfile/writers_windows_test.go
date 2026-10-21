//go:build windows

package secfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
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

// grantFolder gives sid the rights on dir, through .NET's access-only calls (no audit-list privilege).
func grantFolder(t *testing.T, dir, sid, rights, inherit string) {
	t.Helper()
	ps := `$d = Get-Item -LiteralPath $env:D; $a = $d.GetAccessControl('Access'); ` +
		`$r = New-Object Security.AccessControl.FileSystemAccessRule((New-Object Security.Principal.SecurityIdentifier($env:S)), $env:R, $env:I, 'None', 'Allow'); ` +
		`$a.AddAccessRule($r); $d.SetAccessControl($a)`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", ps)
	cmd.Env = append(os.Environ(), "D="+dir, "S="+sid, "R="+rights, "I="+inherit)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
}

// H5 review N1: any account that can write is reported, not only the broad groups: a group of
// its own (here Guests) by name, and an account that doesn't resolve by its SID.
func TestWritersReportsEveryOtherAccount(t *testing.T) {
	dir := t.TempDir()
	grantFolder(t, dir, "S-1-5-32-546", "Modify", "ContainerInherit,ObjectInherit")
	grantFolder(t, dir, "S-1-5-21-1111111111-2222222222-3333333333-1234", "Write", "None")
	who, err := Writers(dir)
	require.NoError(t, err)
	assert.Contains(t, who, `BUILTIN\Guests`)
	assert.Contains(t, who, "S-1-5-21-1111111111-2222222222-3333333333-1234")
}

// H5 review N2: an account that can delete the folder's parent's children can move the folder away
// and put its own at the same path, whatever the folder's own access list says.
func TestWritersReadsTheParentFolder(t *testing.T) {
	parent := t.TempDir()
	tools := filepath.Join(parent, "tools")
	require.NoError(t, os.Mkdir(tools, 0o700))
	grantFolder(t, parent, "S-1-1-0", "DeleteSubdirectoriesAndFiles", "None") // not inherited by tools
	who, err := Writers(tools)
	require.NoError(t, err)
	assert.Contains(t, who, "Everyone (through the parent folder)")
	assert.NotContains(t, who, "Everyone", "the folder itself grants Everyone nothing")
}

// H5 review: naming a domain account can wait seconds for an unreachable domain controller, on
// Coral's startup path. Writers waits lookupWait at most, reports the account by its SID string,
// and a later call takes the name the lookup cached when it finished.
func TestWritersDoesNotWaitForASlowAccountLookup(t *testing.T) {
	const sid = "S-1-5-21-1111111111-2222222222-3333333333-4321"
	release := make(chan struct{})
	oldLookup, oldWait := lookupAccount, lookupWait
	lookupAccount = func(s *windows.SID) (string, string, error) {
		if s.String() == sid {
			<-release // a domain controller that doesn't answer
			return "slow", "CORP", nil
		}
		return oldLookup(s)
	}
	lookupWait = 200 * time.Millisecond
	defer func() { close(release); lookupAccount, lookupWait = oldLookup, oldWait }()

	dir := t.TempDir()
	grantFolder(t, dir, sid, "Modify", "None")
	start := time.Now()
	who, err := Writers(dir)
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "Writers waited for the lookup")
	assert.Contains(t, who, sid, "an account not named in time is reported by its SID")

	release <- struct{}{} // the lookup answers now and caches the name
	require.Eventually(t, func() bool {
		who, err := Writers(dir)
		return err == nil && slices.Contains(who, `CORP\slow`)
	}, 5*time.Second, 20*time.Millisecond)
}
