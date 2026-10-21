package main

import (
	"os"
)

// What the tray does about the Coral window (#5827): launching Coral.app by hand opens the window, a start that nobody asked for (a login item, the relaunch after an update, a
// restart from Settings) stays in the background, and launching it again while it runs brings the window forward instead of starting a second server.

// opensWindowAtStart is whether a start of the tray opens the Coral window once the server runs. noBrowser is the --no-browser flag (what the updater's relaunch passes);
// loginItem is that macOS started the app at sign-in.
func opensWindowAtStart(noBrowser, loginItem bool) bool {
	return !noBrowser && !loginItem
}

// otherTrayPID is the pid of another running tray of this data folder, or 0: the one a second start of the app hands over to. A start that replaces a tray (a restart from
// Settings waits for it to exit first, restartWaitEnv) is not a second start.
func otherTrayPID(dataDir string, self int, restarting bool, readPID func(string) int) int {
	if restarting {
		return 0
	}
	if pid := readPID(dataDir); pid > 0 && pid != self {
		return pid
	}
	return 0
}

// handOver is what a second start does: ask the running tray to show its window and report whether it did (the caller then exits instead of starting a second server).
func handOver(pid int, ask func(pid int) error) bool {
	return ask(pid) == nil
}

var selfPID = os.Getpid
