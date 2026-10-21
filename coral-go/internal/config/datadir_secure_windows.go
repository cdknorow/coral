//go:build windows

package config

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// secureExistingDataDir makes an existing data folder that other accounts can use private to this user (G2A, #96):
//
//   - it acts only when the folder's access list has an allow entry for anyone but this user, SYSTEM and Administrators
//     (Everyone, Users, another group, a named account);
//   - this user must own it (or an elevated Administrator own it for them), and it must not be a link or junction (a link would
//     put the change on a folder that is not the data folder): otherwise ErrSharedDataDir, with the folder and the way out;
//   - the folder gets the protected owner-only list a new data folder gets (inherited by what is in it), through a handle opened
//     WITHOUT following a reparse point, and EVERY file and folder in it gets the owner-only list too, so an explicit entry for
//     another account on an old file does not survive; links and junctions inside it are skipped and never entered.
//
// The returned text says what was done, for the log and the dashboard. A folder nobody else can use returns "".
func secureExistingDataDir(dir string) (string, error) {
	// A link's own list says nothing about the folder it points to: read the target's (through a handle that follows the link), and
	// never change it.
	linked := isLink(dir)
	readers := DataDirBroadReaders
	if linked {
		readers = linkedDirBroadReaders
	}
	groups, err := readers(dir)
	if err != nil || len(groups) == 0 {
		return "", nil // unreadable list: DataDirWarning reports it; nothing certain to change
	}
	const linkWhy = "it is a link or junction, and changing its permissions would change the folder it points to"
	if linked {
		return "", sharedDataDirError(dir, groups, linkWhy)
	}
	h, err := openNoFollow(dir)
	if err != nil {
		return "", sharedDataDirError(dir, groups, fmt.Sprintf("it could not be opened to change its permissions: %v", err))
	}
	defer windows.CloseHandle(h)
	if isReparseHandle(h) { // swapped for a link since the check above
		return "", sharedDataDirError(dir, groups, linkWhy)
	}
	owned, err := ownedByUs(h)
	if err != nil {
		return "", sharedDataDirError(dir, groups, fmt.Sprintf("its owner could not be read: %v", err))
	}
	if !owned {
		return "", sharedDataDirError(dir, groups, "another account owns it, so this account may not change who can use it. To take it over, from an Administrator prompt run: "+
			fmt.Sprintf(`takeown /F "%s" /R /D Y`, dir)+" and then "+
			fmt.Sprintf(`icacls "%s" /inheritance:r /grant:r "%%USERDOMAIN%%\%%USERNAME%%:(OI)(CI)F" "SYSTEM:(OI)(CI)F" /T`, dir))
	}
	if err := setOwnerOnly(h, true); err != nil {
		return "", sharedDataDirError(dir, groups, fmt.Sprintf("its permissions could not be changed: %v", err))
	}
	changed, skipped, failed := reprotectContents(dir)
	msg := fmt.Sprintf("Coral made its data folder %s private to this user: %s could use it. %d existing files and folders were re-protected", dir, strings.Join(groups, ", "), changed)
	if skipped > 0 {
		msg += fmt.Sprintf(" (%d links were left alone)", skipped)
	}
	msg += "."
	if len(failed) > 0 {
		msg += fmt.Sprintf(" WARNING: %d files or folders could not be changed (for example %s); check their permissions.", len(failed), failed[0])
	}
	return msg, nil
}

func sharedDataDirError(dir string, groups []string, why string) error {
	return fmt.Errorf("%w: Coral's data folder %s can be used by %s, and Coral can't make it private because %s. It holds the databases, the API key and the tokens. "+
		"Move it (CORAL_DATA_DIR) to a folder only you can read, or take ownership of it and remove those accounts from its permissions; "+
		"to run on it anyway, start Coral with --allow-shared-data-dir (or set CORAL_ALLOW_SHARED_DATA_DIR=1)", ErrSharedDataDir, dir, strings.Join(groups, ", "), why)
}

// openNoFollow opens the object named by path itself, not what a reparse point there points to, for reading and changing its list.
func openNoFollow(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(p, windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
}

func isReparseHandle(h windows.Handle) bool {
	var info windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(h, &info) != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

// setOwnerOnly gives the object behind h the protected owner-only list (a folder's inherited by what is made in it).
func setOwnerOnly(h windows.Handle, isDir bool) error {
	acl, err := ownerOnlyACL(isDir)
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// isLink says whether path is a symbolic link or a junction (any reparse point).
func isLink(path string) bool {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return true
	}
	attrs, err := windows.GetFileAttributes(p)
	return err != nil || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

// ownedByUs is ownedByCurrentUser (a variable so a test can stand in for a folder another account owns).
var ownedByUs = ownedByCurrentUser

// ownedByCurrentUser says whether this account may be treated as the owner of the object behind h: it is the owner, or the owner is
// BUILTIN\Administrators (what an elevated session creates) and this process is elevated and in Administrators, who can take
// ownership anyway (#96 N3).
func ownedByCurrentUser(h windows.Handle) (bool, error) {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return false, err
	}
	if owner == nil {
		return false, nil
	}
	me, err := currentUserSID()
	if err != nil {
		return false, err
	}
	if owner.Equals(me) {
		return true, nil
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false, err
	}
	if !owner.Equals(admins) {
		return false, nil
	}
	token := windows.GetCurrentProcessToken()
	member, err := token.IsMember(admins)
	return err == nil && member && token.IsElevated(), nil
}

// reprotectContents gives everything under root the owner-only list, and counts what it changed and the links it left alone, and
// lists what it could not change. Nothing is followed: a link or junction is skipped (and, being neither a regular file nor a folder
// to WalkDir, not entered).
func reprotectContents(root string) (changed, skipped int, failed []string) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			failed = append(failed, path)
			return nil
		}
		if path == root {
			return nil
		}
		if d.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			skipped++
			return nil
		}
		switch reprotect(path, d.IsDir()) {
		case reprotected:
			changed++
		case reprotectFailed:
			failed = append(failed, path)
		case reprotectLink:
			skipped++
		}
		return nil
	})
	return changed, skipped, failed
}

type reprotectResult int

const (
	reprotected reprotectResult = iota
	reprotectFailed
	reprotectLink
)

// reprotect gives one file or folder the owner-only list. The handle is opened on the name itself (FILE_FLAG_OPEN_REPARSE_POINT) and
// refused if it turns out to be a reparse point, so a link swapped in between the walk and here is not followed.
func reprotect(path string, isDir bool) reprotectResult {
	h, err := openNoFollow(path)
	if err != nil {
		return reprotectFailed
	}
	defer windows.CloseHandle(h)
	if isReparseHandle(h) {
		return reprotectLink
	}
	if setOwnerOnly(h, isDir) != nil {
		return reprotectFailed
	}
	return reprotected
}

// ownerOnlyACL is the access list of this user and SYSTEM only; a folder's is inherited by what is made in it.
func ownerOnlyACL(inherit bool) (*windows.ACL, error) {
	user, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	flags := uint32(windows.NO_INHERITANCE)
	if inherit {
		flags = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	entry := func(sid *windows.SID) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       flags,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		}
	}
	return windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry(user), entry(system)}, nil)
}

// linkedDirBroadReaders is DataDirBroadReaders for the folder a link or junction points to: the handle follows the link, so the list
// read is the target's.
func linkedDirBroadReaders(path string) ([]string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
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
