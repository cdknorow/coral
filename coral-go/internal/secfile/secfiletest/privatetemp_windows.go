//go:build windows

package secfiletest

import (
	"golang.org/x/sys/windows"
)

// protectInheritable gives dir a protected access list of this user and SYSTEM only, which the files and folders made in it INHERIT.
// secfile.Protect does not inherit: a file made in such a folder takes the process token's default access list, which on some
// sessions names the logon session's SID, and the private-file checks refuse that. With an inherited list a test's secret file has
// exactly this user's and SYSTEM's access, whatever the real %TEMP% grants.
func protectInheritable(dir string) error {
	tok := windows.GetCurrentProcessToken()
	user, err := tok.GetTokenUser()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	entry := func(sid *windows.SID) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		}
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry(user.User.Sid), entry(system)}, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
