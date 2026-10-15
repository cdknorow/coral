//go:build windows

package routes

import (
	"os"

	"golang.org/x/sys/windows"
)

// linkCount is the opened file's hard-link count, from its handle (#3601, weak site W9):
// os.FileInfo doesn't carry it on Windows.
func linkCount(f *os.File, _ os.FileInfo) (uint64, bool) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return 0, false
	}
	return uint64(info.NumberOfLinks), true
}
