//go:build windows

package config

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// protectDataDir gives dir a protected DACL: full control for the current user and SYSTEM,
// inherited by every file and folder below, and nothing inherited from the parent (#3637).
func protectDataDir(dir string) error {
	user, err := currentUserSID()
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
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry(user), entry(system)}, nil)
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return fmt.Errorf("make the data folder %s private: %w", dir, err)
	}
	return nil
}

func currentUserSID() (*windows.SID, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return tu.User.Sid.Copy()
}

// broadGroups are the groups that mean "other people on this computer".
var broadGroups = []struct {
	name string
	sid  windows.WELL_KNOWN_SID_TYPE
}{
	{"Everyone", windows.WinWorldSid},
	{"Authenticated Users", windows.WinAuthenticatedUserSid},
	{"Users", windows.WinBuiltinUsersSid},
}

// readAccess is any right that lets a holder read files in the folder or list it.
const readAccess = windows.GENERIC_ALL | windows.GENERIC_READ | windows.FILE_READ_DATA | windows.FILE_LIST_DIRECTORY

// DataDirBroadReaders lists the broad groups (Everyone, Authenticated Users, Users) that the
// folder's DACL lets read it. A folder with no DACL at all grants everyone everything.
func DataDirBroadReaders(dir string) ([]string, error) {
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return nil, err
	}
	if dacl == nil {
		return []string{"Everyone (the folder has no access list)"}, nil
	}
	return broadReadersOf(dacl)
}

// broadReadersOf lists the broad groups a DACL lets read.
func broadReadersOf(dacl *windows.ACL) ([]string, error) {
	var sids []*windows.SID
	for _, g := range broadGroups {
		sid, err := windows.CreateWellKnownSid(g.sid)
		if err != nil {
			return nil, err
		}
		sids = append(sids, sid)
	}
	var found []string
	seen := map[string]bool{}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return nil, err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask&readAccess == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		for j, g := range broadGroups {
			if sid.Equals(sids[j]) && !seen[g.name] {
				seen[g.name] = true
				found = append(found, g.name)
			}
		}
	}
	return found, nil
}
