//go:build windows

package secfile

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// writeMask are the rights that let an account put another program in a folder or in place of a
// file in it: write or append data (add a file, a subfolder), delete a child, delete, change the
// access list or the owner, and the generic rights that include them.
const writeMask = 0x2 | 0x4 | 0x40 | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_WRITE | windows.GENERIC_ALL

// Writers names the accounts besides SYSTEM and Administrators that can change what is in the
// folder at path: this user (holding a write right, or owning it, which lets the owner rewrite its
// access list), Everyone, Authenticated Users, Users or INTERACTIVE holding a write right on the
// folder itself or inherited by the files in it. An empty list is a folder only an administrator
// can change, as Program Files is. No access list at all means everyone (H5: a portable Coral's
// tools folder, where any program running as this user could replace the hooks every agent runs).
func Writers(path string) ([]string, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	p, err := currentPrincipals()
	if err != nil {
		return nil, err
	}
	broad := []struct {
		kind windows.WELL_KNOWN_SID_TYPE
		name string
	}{
		{windows.WinWorldSid, "Everyone"},
		{windows.WinAuthenticatedUserSid, "Authenticated Users"},
		{windows.WinBuiltinUsersSid, "Users"},
		{windows.WinInteractiveSid, "INTERACTIVE"},
	}
	var who []string
	seen := map[string]bool{}
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			who = append(who, name)
		}
	}
	if owner, _, err := sd.Owner(); err == nil && owner != nil && owner.Equals(p.user) {
		add("this user (the folder's owner)")
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return nil, fmt.Errorf("read the access list of %s: %w", path, err)
	}
	if dacl == nil {
		return []string{"Everyone (no access list)"}, nil
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return nil, fmt.Errorf("read the access list of %s: %w", path, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask&writeMask == 0 {
			continue
		}
		onFolder := ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0
		onFiles := ace.Header.AceFlags&windows.OBJECT_INHERIT_ACE != 0
		if !onFolder && !onFiles {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid.Equals(p.user) {
			add("this user")
			continue
		}
		for _, b := range broad {
			if sid.IsWellKnown(b.kind) {
				add(b.name)
			}
		}
	}
	return who, nil
}
