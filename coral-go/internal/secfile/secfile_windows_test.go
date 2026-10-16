//go:build windows

package secfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// #3693, on Windows CI: the access-list rule for secret files (coral-hub Security's and ours).

type grant struct {
	sid      *windows.SID
	mask     windows.ACCESS_MASK
	deny     bool
	inherits bool
}

func wellKnown(t *testing.T, k windows.WELL_KNOWN_SID_TYPE) *windows.SID {
	t.Helper()
	sid, err := windows.CreateWellKnownSid(k)
	require.NoError(t, err)
	return sid
}

func me(t *testing.T) principals {
	t.Helper()
	p, err := currentPrincipals()
	require.NoError(t, err)
	return p
}

// setDACL replaces path's DACL with grants (protected: nothing inherited from the folder).
func setDACL(t *testing.T, path string, grants ...grant) {
	t.Helper()
	var entries []windows.EXPLICIT_ACCESS
	for _, g := range grants {
		mode, inh := windows.ACCESS_MODE(windows.GRANT_ACCESS), uint32(windows.NO_INHERITANCE)
		if g.deny {
			mode = windows.DENY_ACCESS
		}
		if g.inherits {
			inh = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
		}
		entries = append(entries, windows.EXPLICIT_ACCESS{AccessPermissions: g.mask, AccessMode: mode, Inheritance: inh,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_UNKNOWN, TrusteeValue: windows.TrusteeValueFromSID(g.sid)}})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
}

func secretFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(path, []byte("secret"), 0o600))
	return path
}

func protected(t *testing.T, path string) bool {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	c, _, err := sd.Control()
	require.NoError(t, err)
	return c&windows.SE_DACL_PROTECTED != 0
}

// (a) A file Protect made is read back, and only this user and SYSTEM are in its DACL.
func TestWindowsProtectThenRead(t *testing.T) {
	path := secretFile(t)
	require.NoError(t, Protect(path))
	data, err := ReadPrivate(path, 4096)
	require.NoError(t, err)
	assert.Equal(t, "secret", string(data))
	assert.True(t, protected(t, path))
}

// An owner-only DACL inherited from the folder (the data folder, or a profile) passes, and the
// file is made protected.
func TestWindowsInheritedOwnerOnlyDACLIsRepaired(t *testing.T) {
	p := me(t)
	dir := t.TempDir()
	setDACL(t, dir, grant{sid: p.user, mask: windows.GENERIC_ALL, inherits: true}, grant{sid: p.system, mask: windows.GENERIC_ALL, inherits: true},
		grant{sid: p.admins, mask: windows.GENERIC_ALL, inherits: true})
	path := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(path, []byte("secret"), 0o600))
	require.False(t, protected(t, path), "inherited to begin with")
	data, err := ReadPrivate(path, 4096)
	require.NoError(t, err)
	assert.Equal(t, "secret", string(data))
	assert.True(t, protected(t, path), "repaired")
	_, err = ReadPrivate(path, 4096)
	assert.NoError(t, err)
}

// (b) A DACL that lets Users (or Everyone, or Authenticated Users) read the file is refused as
// exposed, never repaired.
func TestWindowsBroadReaderIsRefused(t *testing.T) {
	p := me(t)
	for _, k := range []windows.WELL_KNOWN_SID_TYPE{windows.WinBuiltinUsersSid, windows.WinWorldSid, windows.WinAuthenticatedUserSid} {
		path := secretFile(t)
		setDACL(t, path, grant{sid: p.user, mask: windows.GENERIC_ALL}, grant{sid: wellKnown(t, k), mask: windows.GENERIC_READ})
		_, err := ReadPrivate(path, 4096)
		assert.ErrorIs(t, err, ErrExposed, "%v", k)
		assert.False(t, errors.Is(err, os.ErrNotExist))
	}
}

// (c) A file owned by Administrators (made from an elevated shell) with this user, SYSTEM and
// Administrators in its DACL is accepted.
func TestWindowsAdministratorsOwnerIsAccepted(t *testing.T) {
	p := me(t)
	path := secretFile(t)
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, p.admins, nil, nil, nil); err != nil {
		t.Skipf("this account can't make Administrators the owner (not elevated): %v", err)
	}
	setDACL(t, path, grant{sid: p.user, mask: windows.GENERIC_ALL}, grant{sid: p.system, mask: windows.GENERIC_ALL}, grant{sid: p.admins, mask: windows.GENERIC_ALL})
	data, err := ReadPrivate(path, 4096)
	require.NoError(t, err)
	assert.Equal(t, "secret", string(data))
}

// (d) No DACL at all (everyone gets everything) is refused.
func TestWindowsNullDACLIsRefused(t *testing.T) {
	path := secretFile(t)
	require.NoError(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, nil, nil))
	_, err := ReadPrivate(path, 4096)
	assert.ErrorIs(t, err, ErrExposed)
}

// (e) A symlink, and a file with a second name, are refused.
func TestWindowsLinksAreRefused(t *testing.T) {
	path := secretFile(t)
	require.NoError(t, Protect(path))
	hard := filepath.Join(filepath.Dir(path), "hard")
	require.NoError(t, os.Link(path, hard))
	_, err := ReadPrivate(path, 4096)
	assert.ErrorIs(t, err, ErrNotPrivate, "two names")
	require.NoError(t, os.Remove(hard))
	_, err = ReadPrivate(path, 4096)
	assert.NoError(t, err, "one name again")

	link := filepath.Join(filepath.Dir(path), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlinks need Developer Mode or an elevated user here: %v", err)
	}
	_, err = ReadPrivate(link, 4096)
	assert.ErrorIs(t, err, ErrNotPrivate, "a symlink")
}

// A DENY entry for anyone takes access away: ignored. It denies only writing (GENERIC_WRITE
// would also deny READ_CONTROL and SYNCHRONIZE, which reading the file needs: that case is the
// next test).
func TestWindowsDenyEntriesAreIgnored(t *testing.T) {
	p := me(t)
	path := secretFile(t)
	setDACL(t, path, grant{sid: wellKnown(t, windows.WinWorldSid), mask: windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA, deny: true},
		grant{sid: p.user, mask: windows.GENERIC_ALL}, grant{sid: p.system, mask: windows.GENERIC_ALL})
	data, err := ReadPrivate(path, 4096)
	require.NoError(t, err)
	assert.Equal(t, "secret", string(data))
}

// A file this user can't read is refused cleanly: an error that isn't "missing", and no data.
func TestWindowsUnreadableFileIsRefused(t *testing.T) {
	p := me(t)
	path := secretFile(t)
	setDACL(t, path, grant{sid: p.user, mask: windows.GENERIC_READ, deny: true},
		grant{sid: p.user, mask: windows.GENERIC_ALL}, grant{sid: p.system, mask: windows.GENERIC_ALL})
	t.Cleanup(func() { _ = Protect(path) }) // the owner may always change the DACL: let TempDir remove it
	data, err := ReadPrivate(path, 4096)
	require.Error(t, err)
	assert.False(t, errors.Is(err, os.ErrNotExist), "not \"no key yet\": %v", err)
	assert.Nil(t, data)
}

// A failure to read the access list refuses the file: never "no check".
func TestWindowsUnreadableAccessListFailsClosed(t *testing.T) {
	path := secretFile(t)
	require.NoError(t, Protect(path))
	old := getSecurityInfo
	getSecurityInfo = func(windows.Handle, windows.SE_OBJECT_TYPE, windows.SECURITY_INFORMATION) (*windows.SECURITY_DESCRIPTOR, error) {
		return nil, windows.ERROR_ACCESS_DENIED
	}
	defer func() { getSecurityInfo = old }()
	data, err := ReadPrivate(path, 4096)
	assert.ErrorIs(t, err, ErrNotPrivate)
	assert.Nil(t, data)
}

// A missing file is os.ErrNotExist (the callers treat it as "no key yet").
func TestWindowsMissingFileIsNotExist(t *testing.T) {
	_, err := ReadPrivate(filepath.Join(t.TempDir(), "none"), 4096)
	assert.ErrorIs(t, err, os.ErrNotExist)
}
