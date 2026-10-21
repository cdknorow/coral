//go:build windows

package config

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// secureExistingDataDir makes an existing data folder that other accounts can read private to this user (G2A, #96):
//
//   - it acts only when the folder's access list lets Everyone, Authenticated Users or Users read it;
//   - this user must own it, and it must not be a link or junction (a link would put the change on a folder that is not the data
//     folder): otherwise ErrSharedDataDir, with the folder and the way out in the message;
//   - the folder gets the protected owner-only list a new data folder gets (inherited by what is in it), and every file and folder in it
//     whose own list lets those groups read is re-protected through a handle opened WITHOUT following a reparse point; links and
//     junctions inside it are skipped and never entered.
//
// The returned text says what was done, for the log and the dashboard. A folder nobody else can read returns "".
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
	if linked {
		return "", sharedDataDirError(dir, groups, "it is a link or junction, and changing its permissions would change the folder it points to")
	}
	owned, err := ownedByUs(dir)
	if err != nil {
		return "", sharedDataDirError(dir, groups, fmt.Sprintf("its owner could not be read: %v", err))
	}
	if !owned {
		return "", sharedDataDirError(dir, groups, "another account owns it, so this account may not change who can read it")
	}
	if err := protectDataDir(dir); err != nil {
		return "", sharedDataDirError(dir, groups, fmt.Sprintf("its permissions could not be changed: %v", err))
	}
	changed, skipped := reprotectContents(dir)
	msg := fmt.Sprintf("Coral made its data folder %s private to this user: %s could read it. %d existing files and folders were re-protected", dir, strings.Join(groups, ", "), changed)
	if skipped > 0 {
		msg += fmt.Sprintf(" (%d links were left alone)", skipped)
	}
	return msg + ".", nil
}

func sharedDataDirError(dir string, groups []string, why string) error {
	return fmt.Errorf("%w: Coral's data folder %s can be read by %s, and Coral can't make it private because %s. It holds the databases, the API key and the tokens. "+
		"Move it (CORAL_DATA_DIR) to a folder only you can read, or take ownership of it and remove those groups from its permissions; "+
		"to run on it anyway, start Coral with --allow-shared-data-dir (or set CORAL_ALLOW_SHARED_DATA_DIR=1)", ErrSharedDataDir, dir, strings.Join(groups, ", "), why)
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

// ownedByCurrentUser says whether this account is the owner of path.
func ownedByCurrentUser(path string) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return false, err
	}
	me, err := currentUserSID()
	if err != nil {
		return false, err
	}
	return owner != nil && owner.Equals(me), nil
}

// reprotectContents re-protects what is under root whose own list lets a broad group read it, and counts what it changed and the links
// it left alone. Nothing is followed: a link or junction is skipped (and, being neither a regular file nor a folder to WalkDir, not entered).
func reprotectContents(root string) (changed, skipped int) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == root {
			return nil
		}
		if d.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			skipped++
			return nil
		}
		if reprotect(path, d.IsDir()) {
			changed++
		}
		return nil
	})
	return changed, skipped
}

// reprotect gives one file or folder the owner-only list when its own list lets a broad group read it. The handle is opened on the name
// itself (FILE_FLAG_OPEN_REPARSE_POINT) and refused if it turns out to be a reparse point, so a link swapped in between the walk and here
// is not followed. It returns whether it changed the object.
func reprotect(path string, isDir bool) bool {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	h, err := windows.CreateFile(p, windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(h, &info) != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return false
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return false
	}
	if dacl != nil {
		if groups, err := broadReadersOf(dacl); err != nil || len(groups) == 0 {
			return false
		}
	}
	acl, err := ownerOnlyACL(isDir)
	if err != nil {
		return false
	}
	return windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil) == nil
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
