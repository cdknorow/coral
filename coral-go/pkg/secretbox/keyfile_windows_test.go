//go:build windows

package secretbox

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/cdknorow/coral/internal/secfile"
)

// #3693: on Windows a key file Coral made loads again (before, every existing key file was
// refused for not being 0600, which Windows never reports), and one other accounts can read is
// refused as exposed.
func TestWindowsKeyFileCreateReloadAndExposed(t *testing.T) {
	t.Setenv(EnvKey, "")
	dir := t.TempDir()
	b, err := CreateKeyFile(dir)
	require.NoError(t, err)
	env, err := b.Seal("x", []byte("v"))
	require.NoError(t, err)
	b2, err := LoadKey(dir)
	require.NoError(t, err, "a key file Coral made loads")
	pt, err := b2.Open("x", env)
	require.NoError(t, err)
	assert.Equal(t, "v", string(pt))

	// This user keeps full access; Users can read too.
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	users, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	require.NoError(t, err)
	entry := func(sid *windows.SID, mask windows.ACCESS_MASK) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{AccessPermissions: mask, AccessMode: windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_UNKNOWN, TrusteeValue: windows.TrusteeValueFromSID(sid)}}
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry(tu.User.Sid, windows.GENERIC_ALL), entry(users, windows.GENERIC_READ)}, nil)
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(KeyFilePath(dir), windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
	_, err = LoadKey(dir)
	assert.ErrorIs(t, err, ErrKeyPermissions)
	assert.ErrorIs(t, err, secfile.ErrExposed)
	assert.NotErrorIs(t, err, os.ErrNotExist)
}
