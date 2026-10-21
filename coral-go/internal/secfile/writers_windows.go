//go:build windows

package secfile

import (
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// writeMask are the rights that let an account put another program in a folder or in place of a
// file in it: write or append data (add a file, a subfolder), delete a child, delete, change the
// access list or the owner, and the generic rights that include them.
const writeMask = 0x2 | 0x4 | 0x40 | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_WRITE | windows.GENERIC_ALL

// parentMask are the rights on a folder's parent that let an account move the folder away (delete a
// child) and put another folder at its path, or take the parent over (its access list or owner).
const parentMask = 0x40 | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_ALL

// trustedInstaller is NT SERVICE\TrustedInstaller, which owns the system's own files.
const trustedInstaller = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

// Writers names the accounts besides SYSTEM, Administrators and TrustedInstaller that can change
// what is in the folder at path (H5: a portable Coral's tools folder, where such an account could
// replace the hooks every agent runs). From the folder's own access list: any account holding a
// write-class right on the folder or inherited by the files in it, and its owner (an owner can
// rewrite the access list); no access list at all is everyone. From its parent's: any account that
// can move the folder away and put another at its path (delete child, or the parent's access list or
// owner), marked "through the parent folder". This user is "this user"; other accounts are
// DOMAIN\name, or their SID when it doesn't resolve. An empty list is a folder only an administrator
// can change, as one in Program Files.
func Writers(path string) ([]string, error) {
	p, err := currentPrincipals()
	if err != nil {
		return nil, err
	}
	var who []string
	seen := map[string]bool{}
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			who = append(who, name)
		}
	}
	clean := filepath.Clean(path)
	if err := aclWriters(clean, writeMask, true, p, "", add); err != nil {
		return nil, err
	}
	if parent := filepath.Dir(clean); parent != clean {
		if err := aclWriters(parent, parentMask, false, p, "through the parent folder", add); err != nil {
			return nil, err
		}
	}
	return who, nil
}

// aclWriters adds the accounts that hold one of mask's rights in path's access list (on the folder,
// or inherited by its files when files is set) and its owner, leaving out SYSTEM, Administrators,
// TrustedInstaller and CREATOR OWNER (a placeholder for whoever creates a file). via, when set, says
// how (through the parent folder).
func aclWriters(path string, mask uint32, files bool, p principals, via string, add func(string)) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	note := func(why string) string {
		if via != "" {
			why += ", " + via
		}
		return " (" + why + ")"
	}
	if owner, _, err := sd.Owner(); err == nil && owner != nil && !trusted(owner, p) {
		add(accountName(owner, p) + note("the owner"))
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read the access list of %s: %w", path, err)
	}
	if dacl == nil {
		add("Everyone" + note("no access list"))
		return nil
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("read the access list of %s: %w", path, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || uint32(ace.Mask)&mask == 0 {
			continue
		}
		onFolder := ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0
		onFiles := files && ace.Header.AceFlags&windows.OBJECT_INHERIT_ACE != 0
		if !onFolder && !onFiles {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if trusted(sid, p) || sid.IsWellKnown(windows.WinCreatorOwnerSid) {
			continue
		}
		name := accountName(sid, p)
		if via != "" {
			name += " (" + via + ")"
		}
		add(name)
	}
	return nil
}

// trusted reports SYSTEM, Administrators and TrustedInstaller: the accounts that can change any file
// anyway.
func trusted(sid *windows.SID, p principals) bool {
	return sid.Equals(p.system) || sid.Equals(p.admins) || sid.String() == trustedInstaller
}

// accountName is "this user" for this user's SID, otherwise DOMAIN\name, or the SID string when the
// account doesn't resolve.
func accountName(sid *windows.SID, p principals) string {
	if sid.Equals(p.user) {
		return "this user"
	}
	if account, domain, _, err := sid.LookupAccount(""); err == nil && account != "" {
		if domain != "" {
			return domain + `\` + account
		}
		return account
	}
	return sid.String()
}
