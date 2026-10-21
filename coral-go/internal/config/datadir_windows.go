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

// DataDirBroadReaders lists who the folder's DACL lets in besides this user, SYSTEM and Administrators:
// the broad groups (Everyone, Authenticated Users, Users) and any other group or account with an allow
// entry. A folder with no DACL at all grants everyone everything.
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

// broadReadersOf lists who a DACL lets in besides this user, SYSTEM and Administrators: every other
// SID with an allow entry that applies to the object (#96 N1: a custom group or a named account counts, not
// only Everyone, Authenticated Users and Users). Broad groups are named plainly; others by account name,
// or by SID when it can't be looked up.
func broadReadersOf(dacl *windows.ACL) ([]string, error) {
	me, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	var ours []*windows.SID
	ours = append(ours, me)
	for _, t := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid, windows.WinCreatorOwnerSid} {
		sid, err := windows.CreateWellKnownSid(t)
		if err != nil {
			return nil, err
		}
		ours = append(ours, sid)
	}
	var broad []*windows.SID
	for _, g := range broadGroups {
		sid, err := windows.CreateWellKnownSid(g.sid)
		if err != nil {
			return nil, err
		}
		broad = append(broad, sid)
	}
	var found []string
	seen := map[string]bool{}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return nil, err
		}
		// An entry that is only for what is made below the folder does not apply to the folder itself.
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask == 0 || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sidIn(sid, ours) {
			continue
		}
		name := sid.String()
		for j, g := range broadGroups {
			if sid.Equals(broad[j]) {
				name = g.name
			}
		}
		if name == sid.String() {
			if account, domain, _, err := sid.LookupAccount(""); err == nil {
				name = account
				if domain != "" {
					name = domain + `\` + account
				}
			}
		}
		if !seen[name] {
			seen[name] = true
			found = append(found, name)
		}
	}
	return found, nil
}

func sidIn(sid *windows.SID, set []*windows.SID) bool {
	for _, s := range set {
		if sid.Equals(s) {
			return true
		}
	}
	return false
}
