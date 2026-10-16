//go:build windows

package secfiletest

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/cdknorow/coral/internal/secfile"
)

// GrantUsersRead gives path an explicit, protected DACL that lets this user do anything and the
// built-in Users group read: a secret file another account can read.
func GrantUsersRead(t *testing.T, path string) {
	t.Helper()
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	users, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	require.NoError(t, err)
	entry := func(sid *windows.SID, mask windows.ACCESS_MASK) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{AccessPermissions: mask, AccessMode: windows.GRANT_ACCESS, Inheritance: windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_UNKNOWN, TrusteeValue: windows.TrusteeValueFromSID(sid)}}
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry(tu.User.Sid, windows.GENERIC_ALL), entry(users, windows.GENERIC_READ)}, nil)
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
}

// OwnerOnlyFolder gives dir an explicit, protected DACL that its new files inherit: this user
// and SYSTEM only (a private data folder, as Coral sets up a new one).
func OwnerOnlyFolder(t *testing.T, dir string) {
	t.Helper()
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	require.NoError(t, err)
	entry := func(sid *windows.SID) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{AccessPermissions: windows.GENERIC_ALL, AccessMode: windows.GRANT_ACCESS, Inheritance: windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_UNKNOWN, TrusteeValue: windows.TrusteeValueFromSID(sid)}}
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry(tu.User.Sid), entry(system)}, nil)
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
}

// RequirePrivate fails the test unless path passes the secret-file check with an explicit,
// protected DACL (what secfile.Protect makes).
func RequirePrivate(t *testing.T, path string) {
	t.Helper()
	explicit, err := secfile.Explicit(path)
	require.NoError(t, err, path)
	require.True(t, explicit, "%s: its access list is inherited, not its own", path)
}
