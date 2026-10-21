//go:build windows

package config

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func daclSIDs(t *testing.T, path string) (sids []*windows.SID, protected bool) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	control, _, err := sd.Control()
	require.NoError(t, err)
	dacl, _, err := sd.DACL()
	require.NoError(t, err)
	require.NotNil(t, dacl)
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		require.NoError(t, windows.GetAce(dacl, i, &ace))
		sids = append(sids, (*windows.SID)(unsafe.Pointer(&ace.SidStart)))
	}
	return sids, control&windows.SE_DACL_PROTECTED != 0
}

// #3637 on Windows: a new data folder's DACL is protected and grants only the current user and
// SYSTEM, with no entry for Everyone, Authenticated Users or Users; a file Coral writes in it
// inherits that.
func TestNewDataFolderHasAnOwnerOnlyDACL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "coral-data")
	require.NoError(t, EnsureDataDir(dir))
	user, err := currentUserSID()
	require.NoError(t, err)
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	require.NoError(t, err)

	sids, protected := daclSIDs(t, dir)
	assert.True(t, protected, "nothing is inherited from the parent")
	require.NotEmpty(t, sids)
	for _, s := range sids {
		assert.True(t, s.Equals(user) || s.Equals(system), "unexpected entry %s", s.String())
	}
	groups, err := DataDirBroadReaders(dir)
	require.NoError(t, err)
	assert.Empty(t, groups)
	assert.Empty(t, DataDirWarning(dir), "no warning for a private folder")

	f := filepath.Join(dir, "api_key")
	require.NoError(t, os.WriteFile(f, []byte("k"), 0o600))
	fileSIDs, _ := daclSIDs(t, f)
	for _, s := range fileSIDs {
		assert.True(t, s.Equals(user) || s.Equals(system), "the file inherits only the owner and SYSTEM, not %s", s.String())
	}
}

// A folder that Everyone can read is reported; EnsureDataDir then makes an existing folder this
// user owns private.
func TestBroadDataFolderIsReported(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	require.NoError(t, os.Mkdir(dir, 0o700))
	grantEveryoneRead(t, dir)

	groups, err := DataDirBroadReaders(dir)
	require.NoError(t, err)
	assert.Contains(t, groups, "Everyone")
	msg := DataDirWarning(dir)
	assert.NotContains(t, msg, "WARNING", "the dashboard's toast is already titled Security warning")
	assert.Contains(t, msg, "Everyone")
	assert.Contains(t, msg, "API key")

	require.NoError(t, EnsureDataDir(dir))
	groups, err = DataDirBroadReaders(dir)
	require.NoError(t, err)
	assert.Empty(t, groups, "an existing folder this user owns is made private (#96)")
}

// grantEveryoneRead adds an Everyone read entry to dir's DACL (a test stands in for a folder
// someone shared).
func grantEveryoneRead(t *testing.T, dir string) {
	t.Helper()
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	require.NoError(t, err)
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	old, _, err := sd.DACL()
	require.NoError(t, err)
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_READ,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(everyone),
		},
	}}, old)
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
}
