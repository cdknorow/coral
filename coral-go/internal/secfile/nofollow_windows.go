//go:build windows

package secfile

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// OpenNoFollow is os.OpenFile that never goes through a link at the path (#3920, Security's
// #3903 item 5). Windows has no O_NOFOLLOW, and the files Coral writes in folders an agent can
// write to (its logs, the terminal input debug log, the hook cache) followed a symbolic link or
// junction planted at their path. Here the handle is opened with FILE_FLAG_OPEN_REPARSE_POINT
// (the link itself, never its target) and refused with ErrLink if it is a reparse point or a
// folder, or, for a write, has a second name. Truncation (O_TRUNC) happens only after that
// check, so a link is never truncated. A file opened with O_CREATE gets the private access list
// Protect gives (this user and SYSTEM), set on the handle, not by path. Other processes may
// read, write, rename and delete the file while it is open (as for a log being rotated).
func OpenNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var access uint32
	switch flag & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR) {
	case os.O_RDONLY:
		access = windows.GENERIC_READ
	case os.O_WRONLY:
		access = windows.GENERIC_WRITE
	case os.O_RDWR:
		access = windows.GENERIC_READ | windows.GENERIC_WRITE
	}
	if flag&os.O_CREATE != 0 {
		access |= windows.GENERIC_WRITE | windows.READ_CONTROL | windows.WRITE_DAC
	}
	if flag&os.O_APPEND != 0 && flag&os.O_TRUNC == 0 {
		// Every write goes to the end, as os.OpenFile does it.
		access &^= windows.GENERIC_WRITE
		access |= windows.FILE_APPEND_DATA | windows.FILE_WRITE_ATTRIBUTES | windows.SYNCHRONIZE
	}
	write := flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0
	var disposition uint32
	switch {
	case flag&(os.O_CREATE|os.O_EXCL) == os.O_CREATE|os.O_EXCL:
		disposition = windows.CREATE_NEW
	case flag&os.O_CREATE != 0:
		disposition = windows.OPEN_ALWAYS
	default:
		disposition = windows.OPEN_EXISTING
	}
	attrs := uint32(windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if flag&os.O_CREATE != 0 && perm&0o200 == 0 {
		attrs |= windows.FILE_ATTRIBUTE_READONLY
	}
	h, err := windows.CreateFile(name, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, disposition, attrs, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 ||
		(write && info.NumberOfLinks != 1) {
		windows.CloseHandle(h)
		return nil, &os.PathError{Op: "open", Path: path, Err: ErrLink}
	}
	f := os.NewFile(uintptr(h), path)
	if flag&os.O_CREATE != 0 {
		if err := protectHandle(h); err != nil {
			f.Close()
			return nil, &os.PathError{Op: "open", Path: path, Err: fmt.Errorf("make it private: %w", err)}
		}
	}
	if flag&os.O_TRUNC != 0 {
		if err := f.Truncate(0); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

// protectHandle is Protect on an open handle: full control for this user and SYSTEM, nothing
// inherited.
func protectHandle(h windows.Handle) error {
	p, err := currentPrincipals()
	if err != nil {
		return err
	}
	entry := func(sid *windows.SID) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		}
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry(p.user), entry(p.system)}, nil)
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
