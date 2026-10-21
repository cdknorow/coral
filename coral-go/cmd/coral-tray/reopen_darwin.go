package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa

void coralInstallReopenHandler(void);
int coralLaunchedAsLoginItem(void);
*/
import "C"

import "sync/atomic"

var reopenShow atomic.Pointer[func()]

// installReopenHandler makes opening the app again while it runs (Finder, Dock, Launchpad, Spotlight, open Coral.app: macOS sends the running app a "reopen" event and starts
// nothing) call show (#5827). Called once NSApplication runs.
func installReopenHandler(show func()) {
	reopenShow.Store(&show)
	C.coralInstallReopenHandler()
}

//export coralTrayReopen
func coralTrayReopen() {
	if f := reopenShow.Load(); f != nil {
		(*f)()
	}
}

// loginItemLaunch is whether macOS started the app at sign-in (the launch event says so): a start that stays in the background.
func loginItemLaunch() bool { return C.coralLaunchedAsLoginItem() != 0 }
