//go:build windows

package safefs

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Windows has no O_NOFOLLOW or FIFOs in the file system: the last name is checked with Lstat
// (a symlink or any other reparse point is refused) and the open handle with SameFile.
const (
	oNoFollow = 0
	oNonBlock = 0
)

// LinkCount is the open file's hard-link count, read from its handle: Windows's FileInfo
// doesn't carry it (#3615). ok is false when the handle can't say.
func LinkCount(f *os.File, _ os.FileInfo) (n uint64, ok bool) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return 0, false
	}
	return uint64(info.NumberOfLinks), true
}

// realPath is where p really is. filepath.EvalSymlinks doesn't follow junctions (mount
// points) in current Go: a junction as the last name comes back unresolved, and one on the way
// to a file fails the whole walk (ENOTDIR), so a junction leading out of a root looked inside
// it (#3615, #3633, found by the Windows CI runs). So p is opened as it is (attributes only, a
// folder too) and the handle's final path is the answer: every symlink and junction on the way
// resolved by the system itself. A missing p is fs.ErrNotExist, as from EvalSymlinks.
func realPath(p string) (string, error) {
	r := filepath.Clean(p)
	name, err := windows.UTF16PtrFromString(r)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", &os.PathError{Op: "open", Path: r, Err: err}
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0) // FILE_NAME_NORMALIZED | VOLUME_NAME_DOS (both 0)
	if err != nil {
		return "", &os.PathError{Op: "GetFinalPathNameByHandle", Path: r, Err: err}
	}
	if int(n) >= len(buf) {
		return "", &os.PathError{Op: "GetFinalPathNameByHandle", Path: r, Err: windows.ERROR_FILENAME_EXCED_RANGE}
	}
	final := windows.UTF16ToString(buf[:n])
	switch {
	case strings.HasPrefix(final, `\\?\UNC\`):
		final = `\\` + final[len(`\\?\UNC\`):] // a share: IsUNC refuses it where it matters
	case strings.HasPrefix(final, `\\?\`):
		final = final[len(`\\?\`):]
	}
	return filepath.Clean(final), nil
}
