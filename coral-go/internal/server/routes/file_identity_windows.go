//go:build windows

package routes

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// fileHandleInfo is the file's (volume serial, file index, link count), read from a handle
// opened on the name itself without following a reparse point: os.FileInfo doesn't carry them
// on Windows. ok is false for a link or reparse point, or when the file can't be opened.
func fileHandleInfo(path string) (info windows.ByHandleFileInformation, ok bool) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return info, false
	}
	h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return info, false
	}
	defer windows.CloseHandle(h)
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return info, false
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return info, false
	}
	return info, true
}

// fileIdentity is a file's (volume, file index): the same for every hard link to it.
func fileIdentity(path string, _ os.FileInfo) (string, bool) {
	info, ok := fileHandleInfo(path)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%d:%d:%d", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), true
}

// fileLinks is a file's hard-link count (#3554: a shared diff object nothing links has 1).
// When the handle can't say it is 2, not 1: callers delete only what has one link, so a
// file whose count is unknown is kept.
func fileLinks(path string, _ os.FileInfo) uint64 {
	info, ok := fileHandleInfo(path)
	if !ok {
		return 2
	}
	return uint64(info.NumberOfLinks)
}
