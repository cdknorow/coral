//go:build windows

package stage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/subgentic/coral/internal/secfile"
	"github.com/subgentic/coral/internal/updater/verify"
)

// openToEveryone gives path an access list of full control for this user and Everyone (protected; inherited by what is created
// in it when inherits is set): a folder or file any account on the computer may change.
func openToEveryone(t *testing.T, path string, inherits bool) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	require.NoError(t, err)
	inh := uint32(windows.NO_INHERITANCE)
	if inherits {
		inh = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	var entries []windows.EXPLICIT_ACCESS
	for _, sid := range []*windows.SID{user.User.Sid, everyone} {
		entries = append(entries, windows.EXPLICIT_ACCESS{AccessPermissions: windows.GENERIC_ALL, AccessMode: windows.GRANT_ACCESS, Inheritance: inh,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_UNKNOWN, TrusteeValue: windows.TrusteeValueFromSID(sid)}})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
}

// #126 F8: the updater's folders are private whatever their parent allows. EnsureDir gives the folder a protected access list
// of this user and SYSTEM, inherited by what is made in it (the staging folders, the package, the state files). Before, it set a
// non-inheriting one and ignored its error, so what was made in it (the package) got the process token's default access list.
func TestUpdaterFoldersArePrivateWhateverTheParentAllows(t *testing.T) {
	parent := t.TempDir()
	openToEveryone(t, parent, true)
	dir := filepath.Join(parent, UpdatesDir)
	require.NoError(t, verify.EnsureDir(dir))
	partial := filepath.Join(dir, ".1.2.3.partial")
	require.NoError(t, os.Mkdir(partial, 0o700)) // as stage makes it
	file := filepath.Join(partial, SumsFile)
	require.NoError(t, verify.WritePrivate(file, []byte("sums")))
	// The package, opened as stage opens it: its mode means nothing on Windows, so its access list is what it inherits.
	pkgPath := filepath.Join(partial, "coral-windows-amd64-1.2.3.zip")
	pkg, err := os.OpenFile(pkgPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, verify.PrivateMode)
	require.NoError(t, err)
	require.NoError(t, pkg.Close())

	who, err := secfile.Writers(partial)
	require.NoError(t, err)
	for _, w := range who {
		assert.True(t, strings.HasPrefix(w, "this user"), "only this user can change the staging folder: %v", who)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	require.NoError(t, err)
	for _, p := range []string{partial, file, pkgPath} {
		sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		require.NoError(t, err)
		dacl, _, err := sd.DACL()
		require.NoError(t, err)
		require.NotNil(t, dacl)
		inherited := 0
		for i := uint32(0); i < uint32(dacl.AceCount); i++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			require.NoError(t, windows.GetAce(dacl, i, &ace))
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			assert.True(t, sid.Equals(user.User.Sid) || sid.Equals(system), "%s: only this user and SYSTEM, not %s", p, sid)
			if ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
				inherited++
			}
		}
		if p == pkgPath {
			assert.Equal(t, int(dacl.AceCount), inherited, "the package takes its folder's rules, not the token's default")
		}
	}
	f, err := OpenPlain(file)
	require.NoError(t, err, "a file the updater wrote passes the load check")
	f.Close()
}

// #126 F8: a staged file another account can write is refused when it is opened (secfile.CheckOpen, as the state file is).
func TestOpenPlainRefusesAFileAnotherAccountCanWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), ManifestFile)
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0o600))
	openToEveryone(t, path, false)
	f, err := OpenPlain(path)
	if f != nil {
		f.Close()
	}
	require.ErrorIs(t, err, secfile.ErrExposed)
}
