//go:build windows

package safefs

import (
	"os"

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
