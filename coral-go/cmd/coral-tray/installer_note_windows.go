//go:build windows

package main

import "golang.org/x/sys/windows"

// showElevatedInstallerNote tells the person, in a message box, why the installer's "Launch Coral" started nothing. It waits for OK (the tray has nothing else to do:
// it was refused), and a failure to show it is not worth more than the line on stdout.
func showElevatedInstallerNote() {
	t, err1 := windows.UTF16PtrFromString(elevatedInstallerNoteTitle)
	m, err2 := windows.UTF16PtrFromString(elevatedInstallerNote)
	if err1 != nil || err2 != nil {
		return
	}
	_, _ = windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONINFORMATION|windows.MB_SETFOREGROUND)
}
