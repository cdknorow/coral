//go:build windows

package secfile

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/subgentic/coral/internal/safego"
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
// DOMAIN\name, or their SID when it doesn't resolve within lookupWait. An empty list is a folder only an administrator
// can change, as one in Program Files.
func Writers(path string) ([]string, error) {
	p, err := currentPrincipals()
	if err != nil {
		return nil, err
	}
	var found []writer
	clean := filepath.Clean(path)
	if found, err = aclWriters(found, clean, writeMask, true, p, ""); err != nil {
		return nil, err
	}
	if parent := filepath.Dir(clean); parent != clean {
		if found, err = aclWriters(found, parent, parentMask, false, p, "through the parent folder"); err != nil {
			return nil, err
		}
	}
	names := accountNames(found, p)
	var who []string
	seen := map[string]bool{}
	for i, w := range found {
		name := names[i] + w.note
		if !seen[name] {
			seen[name] = true
			who = append(who, name)
		}
	}
	return who, nil
}

// writer is an account aclWriters found (nil sid: Everyone, for no access list) and why, as
// " (the owner)".
type writer struct {
	sid  *windows.SID
	note string
}

// aclWriters adds to found the accounts that hold one of mask's rights in path's access list (on
// the folder, or inherited by its files when files is set) and its owner, leaving out SYSTEM,
// Administrators, TrustedInstaller and CREATOR OWNER (a placeholder for whoever creates a file). via,
// when set, says how (through the parent folder).
func aclWriters(found []writer, path string, mask uint32, files bool, p principals, via string) ([]writer, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return found, err
	}
	note := func(why string) string {
		if via != "" {
			if why != "" {
				why += ", "
			}
			why += via
		}
		if why == "" {
			return ""
		}
		return " (" + why + ")"
	}
	if owner, _, err := sd.Owner(); err == nil && owner != nil && !trusted(owner, p) {
		if owner, err = owner.Copy(); err == nil {
			found = append(found, writer{owner, note("the owner")})
		}
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return found, fmt.Errorf("read the access list of %s: %w", path, err)
	}
	if dacl == nil {
		return append(found, writer{nil, note("no access list")}), nil
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return found, fmt.Errorf("read the access list of %s: %w", path, err)
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
		if sid, err = sid.Copy(); err == nil {
			found = append(found, writer{sid, note("")})
		}
	}
	return found, nil
}

// trusted reports SYSTEM, Administrators and TrustedInstaller: the accounts that can change any file
// anyway.
func trusted(sid *windows.SID, p principals) bool {
	return sid.Equals(p.system) || sid.Equals(p.admins) || sid.String() == trustedInstaller
}

// lookupWait bounds how long Writers, on Coral's startup path, waits for account names: looking up a
// domain account on a machine that is off its network can wait seconds for a domain controller. An
// account not named in time is reported by its SID string.
var lookupWait = 2 * time.Second

// lookupAccount is (*windows.SID).LookupAccount on this machine; a test replaces it with one that
// hangs.
var lookupAccount = func(sid *windows.SID) (account, domain string, err error) {
	account, domain, _, err = sid.LookupAccount("")
	return account, domain, err
}

// lookups caches account names by SID string, the finished ones and those still being looked up,
// so a slow lookup runs once however often Writers is called.
var lookups = struct {
	sync.Mutex
	m map[string]*lookup
}{m: map[string]*lookup{}}

type lookup struct {
	done chan struct{}
	name string // when done: DOMAIN\name, or "" when the account doesn't resolve
}

// accountNames names found's accounts: "this user" for this user's SID, Everyone for no access list,
// otherwise DOMAIN\name, or the SID string when the account doesn't resolve or isn't named within
// lookupWait (all the lookups share that one wait).
func accountNames(found []writer, p principals) []string {
	names := make([]string, len(found))
	pending := make([]*lookup, len(found))
	for i, w := range found {
		switch {
		case w.sid == nil:
			names[i] = "Everyone"
		case w.sid.Equals(p.user):
			names[i] = "this user"
		default:
			names[i] = w.sid.String()
			pending[i] = startLookup(w.sid)
		}
	}
	deadline := time.NewTimer(lookupWait)
	defer deadline.Stop()
	for i, l := range pending {
		if l == nil {
			continue
		}
		select {
		case <-l.done:
		case <-deadline.C:
			return names // the rest keep their SID strings; their lookups finish into the cache
		}
		if l.name != "" {
			names[i] = l.name
		}
	}
	return names
}

// startLookup returns sid's lookup, starting it unless it is cached.
func startLookup(sid *windows.SID) *lookup {
	key := sid.String()
	lookups.Lock()
	defer lookups.Unlock()
	if l, ok := lookups.m[key]; ok {
		return l
	}
	l := &lookup{done: make(chan struct{})}
	lookups.m[key] = l
	go func() {
		defer safego.Recover("secfile/account lookup")
		defer close(l.done) // runs first, so a waiting Writers is released even after a panic
		if account, domain, err := lookupAccount(sid); err == nil && account != "" {
			if domain != "" {
				account = domain + `\` + account
			}
			l.name = account
		}
	}()
	return l
}
