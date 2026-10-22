//go:build windows

package secfile

import (
	"fmt"
	"github.com/subgentic/coral/internal/logsafe"
	"io"
	"log"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The DENY ACE types: they only take access away, so they never make a file less private.
const (
	aceAccessDenied               = 1
	aceAccessDeniedObject         = 6
	aceAccessDeniedCallback       = 10
	aceAccessDeniedCallbackObject = 12
	aceAccessAllowedCallback      = 9 // the same layout as ACCESS_ALLOWED_ACE
)

// getSecurityInfo is windows.GetSecurityInfo; a test replaces it to make the read fail.
var getSecurityInfo = windows.GetSecurityInfo

// principals are the only accounts a secret file may grant anything to: this user, SYSTEM and
// the Administrators group (who can take any file anyway).
type principals struct{ user, system, admins *windows.SID }

func currentPrincipals() (principals, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return principals{}, err
	}
	user, err := tu.User.Sid.Copy()
	if err != nil {
		return principals{}, err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return principals{}, err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return principals{}, err
	}
	return principals{user, system, admins}, nil
}

func (p principals) allowed(sid *windows.SID) bool {
	return sid.Equals(p.user) || sid.Equals(p.system) || sid.Equals(p.admins)
}

// ReadPrivate reads at most max bytes of the secret file at path, after checking on the open
// handle that it is a regular file (not a link or reparse point) with one name, owned by this
// user or by Administrators (a file made from an elevated shell), and that its DACL grants
// access to nobody but this user, SYSTEM and Administrators (DENY entries are ignored; no DACL at
// all means everyone, and is refused). A file that passes but whose DACL is inherited (from the
// data folder or the profile) is made explicit and protected, so a later change to the folder
// can't open it up; that is logged once. Any failure to read the access list refuses the file.
// A missing file is os.ErrNotExist.
func ReadPrivate(path string, max int64) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	if err := CheckOpen(f); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, max))
}

// CheckOpen applies ReadPrivate's checks to a secret file the caller has opened for reading
// (os.Open's access includes READ_CONTROL): a regular file (not a link or reparse point), one
// name, and the owner and DACL rule; a passing file whose DACL is inherited is made private.
// For callers that open the file themselves (a containment-checked open, a mode check on Unix).
func CheckOpen(f *os.File) error {
	path := f.Name()
	h := windows.Handle(f.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrNotPrivate, path, err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return fmt.Errorf("%w: %s must be a regular file, not a link or a folder", ErrNotPrivate, path)
	}
	if info.NumberOfLinks != 1 {
		return fmt.Errorf("%w: %s has another name (a hard link)", ErrNotPrivate, path)
	}
	inherited, err := checkHandle(h, path)
	if err != nil {
		return err
	}
	if inherited {
		if err := Protect(path); err != nil {
			return fmt.Errorf("%w: %s: make its access list private: %v", ErrNotPrivate, path, err)
		}
		log.Printf("[secfile] %s: its access list was inherited (only this user, SYSTEM and Administrators); it is now private to this user", logsafe.Path(path))
	}
	return nil
}

// Explicit reports whether the file at path passes the secret-file check with an explicit,
// protected DACL (what Protect makes), without changing it. For tests of the files Coral writes.
func Explicit(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	inherited, err := checkHandle(windows.Handle(f.Fd()), path)
	if err != nil {
		return false, err
	}
	return !inherited, nil
}

// checkHandle applies the owner and DACL rule to the open file; inherited is true when the file
// passes but its DACL isn't protected.
func checkHandle(h windows.Handle, path string) (inherited bool, err error) {
	p, err := currentPrincipals()
	if err != nil {
		return false, fmt.Errorf("%w: %s: who is this user: %v", ErrNotPrivate, path, err)
	}
	sd, err := getSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, fmt.Errorf("%w: %s: read its access list: %v", ErrNotPrivate, path, err)
	}
	return checkDescriptor(sd, p, path)
}

func checkDescriptor(sd *windows.SECURITY_DESCRIPTOR, p principals, path string) (inherited bool, err error) {
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return false, fmt.Errorf("%w: %s: read its owner: %v", ErrNotPrivate, path, err)
	}
	if !owner.Equals(p.user) && !owner.Equals(p.admins) {
		return false, fmt.Errorf("%w (%s belongs to %s)", ErrExposed, path, owner.String())
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return false, fmt.Errorf("%w: %s: read its access list: %v", ErrNotPrivate, path, err)
	}
	if dacl == nil {
		return false, fmt.Errorf("%w (%s has no access list, so everyone can read it)", ErrExposed, path)
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false, fmt.Errorf("%w: %s: read its access list: %v", ErrNotPrivate, path, err)
		}
		switch ace.Header.AceType {
		case aceAccessDenied, aceAccessDeniedObject, aceAccessDeniedCallback, aceAccessDeniedCallbackObject:
			continue // takes access away
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Mask == 0 {
			continue // grants nothing on this file
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE && ace.Header.AceType != aceAccessAllowedCallback {
			// An object ACE or a type this check doesn't read: refused, never guessed.
			return false, fmt.Errorf("%w: %s: its access list has an entry of type %d this check can't read", ErrNotPrivate, path, ace.Header.AceType)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !p.allowed(sid) {
			return false, fmt.Errorf("%w (%s grants access to %s)", ErrExposed, path, sid.String())
		}
	}
	control, _, err := sd.Control()
	if err != nil {
		return false, fmt.Errorf("%w: %s: read its access list: %v", ErrNotPrivate, path, err)
	}
	return control&windows.SE_DACL_PROTECTED == 0, nil
}

// Protect gives the file at path an explicit, protected DACL: full control for this user and
// SYSTEM, nothing inherited. Call it on a new secret file before writing the secret (the 0600
// passed to os.OpenFile means nothing on Windows).
func Protect(path string) error {
	acl, err := privateACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// ProtectDir gives the folder at path the same protected access list as Protect, inherited by what is created
// in it: this user and SYSTEM only, so a file made there is private whatever the folder's parent allows (os.Chmod
// 0700 does nothing on Windows).
func ProtectDir(path string) error {
	acl, err := aclFor(windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// privateACL is the access list Protect sets: full control for this user and SYSTEM.
func privateACL() (*windows.ACL, error) { return aclFor(windows.NO_INHERITANCE) }

func aclFor(inheritance uint32) (*windows.ACL, error) {
	p, err := currentPrincipals()
	if err != nil {
		return nil, err
	}
	entry := func(sid *windows.SID) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       inheritance,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		}
	}
	return windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry(p.user), entry(p.system)}, nil)
}
